import SwiftData
import XCTest
@testable import FieldAssistant

@MainActor
final class AppSchemaTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory
            .appending(path: "AppSchemaTests-\(UUID().uuidString)", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    private var storeURL: URL {
        directory.appending(path: "default.store", directoryHint: .notDirectory)
    }

    /// A store written by the builds before the versioned schema (a plain `Schema`) opens under
    /// `AppMigrationPlan` with its rows intact.
    func testCurrentStoreOpensUnderVersionedSchema() throws {
        let draftID = UUID()
        do {
            let schema = Schema([
                CachedLookup.self, LocalDraft.self, LocalPhoto.self,
                CachedRecord.self, ProjectSyncState.self,
            ])
            let configuration = ModelConfiguration(schema: schema, url: storeURL)
            let container = try ModelContainer(for: schema, configurations: [configuration])
            let context = ModelContext(container)
            let draft = LocalDraft(
                id: draftID,
                companyID: 7,
                projectID: 99,
                ownerProcoreUserID: 42,
                transcript: "Slab edge detail at grid C",
                photos: [LocalPhoto(fileName: "a.jpg", bytes: 1200), LocalPhoto(fileName: "b.jpg", bytes: 3400)]
            )
            context.insert(draft)
            context.insert(CachedLookup(
                projectID: 99, kind: "people", etag: "\"v1\"", rowsData: Data("[]".utf8),
                unavailable: false, reason: nil, fetchedAt: .now
            ))
            try context.save()
        }

        let container = try AppContainer.makeModelContainer(storeURL: storeURL).get()
        let context = ModelContext(container)
        let drafts = try context.fetch(FetchDescriptor<LocalDraft>())
        XCTAssertEqual(drafts.count, 1)
        let draft = try XCTUnwrap(drafts.first)
        XCTAssertEqual(draft.id, draftID)
        XCTAssertEqual(draft.ownerProcoreUserID, 42)
        XCTAssertEqual(draft.transcript, "Slab edge detail at grid C")
        XCTAssertEqual(draft.photos.map(\.fileName).sorted(), ["a.jpg", "b.jpg"])
        XCTAssertEqual(draft.photos.map(\.bytes).sorted(), [1200, 3400])
        let lookups = try context.fetch(FetchDescriptor<CachedLookup>())
        XCTAssertEqual(lookups.map(\.key), ["99/people"])
        XCTAssertEqual(lookups.first?.etag, "\"v1\"")
    }

    func testVersionedSchemaListsEveryModel() {
        let names = AppSchemaV1.models.map { String(describing: $0) }
        XCTAssertEqual(names, ["CachedLookup", "LocalDraft", "LocalPhoto", "CachedRecord", "ProjectSyncState"])
        XCTAssertEqual(AppSchemaV1.versionIdentifier, Schema.Version(1, 0, 0))
        XCTAssertEqual(AppMigrationPlan.schemas.map { String(describing: $0) }, ["AppSchemaV1"])
        XCTAssertTrue(AppMigrationPlan.stages.isEmpty)
    }

    func testResetLocalDataRemovesStoreFiles() throws {
        let draftFilesRoot = directory.appending(path: "drafts", directoryHint: .isDirectory)
        let draftFolder = draftFilesRoot.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: draftFolder, withIntermediateDirectories: true)
        try Data("jpeg".utf8).write(to: draftFolder.appending(path: "a.jpg"))
        try Data("not a database".utf8).write(to: storeURL)
        try Data("stale".utf8).write(to: URL(filePath: storeURL.path(percentEncoded: false) + "-wal"))

        guard case .failure = AppContainer.makeModelContainer(storeURL: storeURL) else {
            return XCTFail("a garbage store should not open")
        }

        try AppContainer.resetLocalData(storeURL: storeURL, draftFilesRoot: draftFilesRoot)

        for url in AppContainer.storeURLs(storeURL: storeURL) {
            XCTAssertFalse(FileManager.default.fileExists(atPath: url.path(percentEncoded: false)), url.lastPathComponent)
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: draftFilesRoot.path(percentEncoded: false)))
        guard case .success = AppContainer.makeModelContainer(storeURL: storeURL) else {
            return XCTFail("the store should open after a reset")
        }
    }

    /// The URLs a reset removes are the ones the default configuration opens.
    func testStoreURLsNameTheDefaultStore() {
        let store = URL.applicationSupportDirectory.appending(path: "default.store", directoryHint: .notDirectory)
        XCTAssertEqual(
            AppContainer.storeURLs().map { $0.path(percentEncoded: false) },
            [store.path(percentEncoded: false), store.path(percentEncoded: false) + "-wal", store.path(percentEncoded: false) + "-shm"]
        )
    }

    func testMakeModelContainerInMemorySucceeds() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true).get()
        let context = ModelContext(container)
        context.insert(LocalDraft(companyID: 1, projectID: 2, transcript: "In memory"))
        try context.save()
        XCTAssertEqual(try context.fetch(FetchDescriptor<LocalDraft>()).count, 1)
    }
}
