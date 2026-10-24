import FieldAssistantAPI
import Foundation
import SwiftData
import XCTest
@testable import FieldAssistant

final class DraftStoreTests: XCTestCase {
    @MainActor
    func testInsertAndRecentOrdersNewestFirst() throws {
        let (store, _) = try makeStore()
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let older = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, transcript: "older", capturedAt: start)
        let newer = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, transcript: "newer", capturedAt: start.addingTimeInterval(60))
        let otherProject = LocalDraft(companyID: 7, projectID: 100, ownerProcoreUserID: 42, transcript: "other", capturedAt: start.addingTimeInterval(120))

        try store.insert(older)
        try store.insert(newer)
        try store.insert(otherProject)

        XCTAssertEqual(store.recent(projectID: 99, owner: 42).map(\.transcript), ["newer", "older"])
        XCTAssertEqual(store.recent(projectID: 99, owner: 42, limit: 1).map(\.transcript), ["newer"])
        XCTAssertEqual(store.draft(id: older.id)?.transcript, "older")
        XCTAssertNil(store.draft(id: UUID()))
        XCTAssertEqual(store.drafts(withStatus: .waiting, owner: 42).count, 3)
        XCTAssertTrue(store.drafts(withStatus: .draft, owner: 42).isEmpty)
    }

    @MainActor
    func testRecentDraftsExcludeOtherOwners() throws {
        let (store, _) = try makeStore()
        let mine = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, transcript: "mine")
        let theirs = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 43, transcript: "theirs")
        try store.insert(mine)
        try store.insert(theirs)

        XCTAssertEqual(store.recent(projectID: 99, owner: 42).map(\.transcript), ["mine"])
        XCTAssertEqual(store.recent(projectID: 99, owner: 43).map(\.transcript), ["theirs"])
        XCTAssertEqual(store.drafts(withStatus: .waiting, owner: 42).map(\.transcript), ["mine"])
        XCTAssertEqual(store.draft(id: mine.id, owner: 42)?.transcript, "mine")
        XCTAssertNil(store.draft(id: theirs.id, owner: 42), "another user's draft is not loadable by id")
    }

    @MainActor
    func testApplyServerResponseMapsStatusAndResult() throws {
        let (store, container) = try makeStore()
        let draft = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, transcript: "Need the slab edge detail")
        try store.insert(draft)
        let response = try decodeResponse(
            id: draft.id,
            status: "submitted",
            result: #"{"procore_id":501,"number":"214","url":"https://sandbox.procore.com/rfis/501"}"#
        )

        draft.apply(response)
        try store.save()

        let stored = try XCTUnwrap(store.draft(id: draft.id))
        XCTAssertEqual(stored.status, .submitted)
        XCTAssertEqual(stored.subject, "Slab edge detail at grid C")
        XCTAssertEqual(stored.resultNumber, "214")
        XCTAssertEqual(stored.resultURL, "https://sandbox.procore.com/rfis/501")
        XCTAssertEqual(stored.updatedAt, response.updated_at)
        XCTAssertEqual(stored.server?.result?.procore_id, 501)
        XCTAssertEqual(stored.server?.payload.subject, "Slab edge detail at grid C")
        XCTAssertEqual(stored.server?.updated_at, response.updated_at, "the stored copy round-trips fractional seconds")
        XCTAssertEqual(store.drafts(withStatus: .submitted, owner: 42).map(\.id), [draft.id])
        XCTAssertFalse(container.mainContext.hasChanges)
    }

    func testStatusMappingExtractedAndReviewedAreDraft() {
        XCTAssertEqual(LocalDraftStatus(server: .extracted), .draft)
        XCTAssertEqual(LocalDraftStatus(server: .reviewed), .draft)
        XCTAssertEqual(LocalDraftStatus(server: .submitting), .submitting)
        XCTAssertEqual(LocalDraftStatus(server: .submitted), .submitted)
        XCTAssertEqual(LocalDraftStatus(server: .failed), .failed)
        XCTAssertEqual(LocalDraftStatus.submitting.label, "Sending")
        XCTAssertEqual(LocalDraftStatus.failed.label, "Fix")
    }

    @MainActor
    func testDeleteRemovesFiles() throws {
        let (store, container) = try makeStore()
        let draft = LocalDraft(companyID: 7, projectID: 99, transcript: "Delete me")
        let photo = LocalPhoto(fileName: "\(UUID().uuidString).jpg")
        draft.photos.append(photo)
        try store.insert(draft)
        let directory = try DraftFiles.directory(for: draft.id)
        try Data("audio".utf8).write(to: DraftFiles.audioURL(for: draft.id))
        try Data("jpeg".utf8).write(to: DraftFiles.photoURL(draftID: draft.id, fileName: photo.fileName))
        XCTAssertTrue(FileManager.default.fileExists(atPath: directory.path))

        try store.delete(draft)

        XCTAssertFalse(FileManager.default.fileExists(atPath: directory.path))
        XCTAssertNil(store.draft(id: draft.id))
        XCTAssertTrue(try container.mainContext.fetch(FetchDescriptor<LocalPhoto>()).isEmpty, "photos cascade with the draft")
        // A folder that is already gone is not an error.
        DraftFiles.removeAll(for: draft.id)
    }

    func testDraftsFolderExcludedFromBackup() throws {
        let draftID = UUID()
        defer { DraftFiles.removeAll(for: draftID) }
        // Start from a folder an earlier build made without the flag.
        var root = URL.applicationSupportDirectory.appending(path: "drafts", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        var cleared = URLResourceValues()
        cleared.isExcludedFromBackup = false
        try root.setResourceValues(cleared)

        let directory = try DraftFiles.directory(for: draftID)

        // Fresh URLs, so the values are read from disk rather than a URL's cache.
        let freshRoot = URL(filePath: root.path(percentEncoded: false), directoryHint: .isDirectory)
        let freshDraft = URL(filePath: directory.path(percentEncoded: false), directoryHint: .isDirectory)
        XCTAssertEqual(try freshRoot.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup, true)
        XCTAssertEqual(try freshDraft.resourceValues(forKeys: [.isExcludedFromBackupKey]).isExcludedFromBackup, true)
    }

    @MainActor
    private func makeStore() throws -> (DraftStore, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        return (DraftStore(context: container.mainContext), container)
    }

    private func decodeResponse(id: UUID, status: String, result: String) throws -> Components.Schemas.DraftResponse {
        let json = """
        {"id":"\(id.uuidString)","feature":"rfi","project_id":99,"status":"\(status)",
         "payload":{"subject":"Slab edge detail at grid C","assignee_ids":[],"distribution_ids":[],"private":false,"draft":false},
         "unresolved":[],"confidence":{},"transcript":"Need the slab edge detail","model_version":"test-model",
         "result":\(result),
         "created_at":"2026-10-24T15:00:00Z","updated_at":"2026-10-24T15:01:02.345Z"}
        """
        return try LocalDraft.decoder.decode(Components.Schemas.DraftResponse.self, from: Data(json.utf8))
    }
}
