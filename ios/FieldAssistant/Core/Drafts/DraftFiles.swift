import Foundation

/// Where a draft's audio and photos live on disk: Application Support `drafts/<draft id>/`.
/// The folders are excluded from iCloud and device backups: the audio never leaves the phone.
enum DraftFiles {
    /// The draft's folder, created if it does not exist yet. Call it before writing to
    /// `audioURL(for:)` or `photoURL(draftID:fileName:)`, which only build paths.
    static func directory(for draftID: UUID) throws -> URL {
        let url = folder(for: draftID)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        // Set on every call, so folders made before the flag existed are covered too.
        try excludeFromBackup(root)
        try excludeFromBackup(url)
        return url
    }

    static func audioURL(for draftID: UUID) -> URL {
        folder(for: draftID).appending(path: "audio.m4a", directoryHint: .notDirectory)
    }

    static func photoURL(draftID: UUID, fileName: String) -> URL {
        folder(for: draftID).appending(path: fileName, directoryHint: .notDirectory)
    }

    /// Deletes the draft's folder and everything in it. A folder that is already gone is not an error.
    static func removeAll(for draftID: UUID) {
        try? FileManager.default.removeItem(at: folder(for: draftID))
    }

    private static var root: URL {
        URL.applicationSupportDirectory.appending(path: "drafts", directoryHint: .isDirectory)
    }

    private static func folder(for draftID: UUID) -> URL {
        root.appending(path: draftID.uuidString, directoryHint: .isDirectory)
    }

    private static func excludeFromBackup(_ url: URL) throws {
        var url = url
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try url.setResourceValues(values)
    }
}
