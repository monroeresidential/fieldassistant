import Foundation
import SwiftData

/// Builds the app's SwiftData container under `AppMigrationPlan`. Tests pass `inMemory: true`, or a
/// `storeURL` in a temporary folder to exercise an on-disk store.
enum AppContainer {
    enum OpenFailure: Error {
        case open(Error)
    }

    /// Opens the store. A store that cannot be opened (corrupt, or from a build whose schema this
    /// one cannot migrate) comes back as `.failure`, so the app can offer a reset instead of crashing.
    /// - Parameter storeURL: the store file; nil is the default configuration's (`storeURLs()[0]`).
    static func makeModelContainer(inMemory: Bool = false, storeURL: URL? = nil) -> Result<ModelContainer, OpenFailure> {
        let schema = Schema(versionedSchema: AppSchemaV1.self)
        let configuration = if let storeURL {
            ModelConfiguration(schema: schema, url: storeURL)
        } else {
            ModelConfiguration(schema: schema, isStoredInMemoryOnly: inMemory)
        }
        do {
            return .success(try ModelContainer(
                for: schema,
                migrationPlan: AppMigrationPlan.self,
                configurations: [configuration]
            ))
        } catch {
            return .failure(.open(error))
        }
    }

    /// The store file and its SQLite `-wal` and `-shm` siblings.
    /// - Parameter storeURL: the store file; the default is the one the app opens.
    static func storeURLs(storeURL: URL = defaultStoreURL) -> [URL] {
        let path = storeURL.path(percentEncoded: false)
        return [storeURL] + ["-wal", "-shm"].map { URL(filePath: path + $0, directoryHint: .notDirectory) }
    }

    /// Deletes the store files and every draft's audio and photos. Files already gone are not an error.
    static func resetLocalData(storeURL: URL = defaultStoreURL, draftFilesRoot: URL = DraftFiles.root) throws {
        for url in storeURLs(storeURL: storeURL) + [draftFilesRoot] {
            do {
                try FileManager.default.removeItem(at: url)
            } catch CocoaError.fileNoSuchFile {
                continue
            }
        }
    }

    /// Where the default configuration keeps the store: Application Support `default.store`.
    static var defaultStoreURL: URL {
        ModelConfiguration(schema: Schema(versionedSchema: AppSchemaV1.self)).url
    }
}
