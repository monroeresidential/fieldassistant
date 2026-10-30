import Foundation
import SwiftData

/// Where a photo stands on its way to the server. `pending` until the server has stored its bytes,
/// `failed` when it was refused for good, `removed` once dropped from the draft.
enum PhotoUploadState: String, Codable, Sendable {
    case pending, stored, failed, removed
}

/// A photo taken for a draft. The image is at `DraftFiles.photoURL(draftID:fileName:)`, already
/// final (downscaled, metadata stripped) when the photo is made.
@Model
final class LocalPhoto {
    var id: UUID
    var fileName: String
    var createdAt: Date
    var draft: LocalDraft?
    /// The JPEG's size on disk. The default lets SwiftData migrate stores made before the field
    /// existed (those photos report 0 bytes).
    var bytes: Int64 = 0
    /// The upload state's raw value, stored as a string for the same reason as `LocalDraft.statusRaw`;
    /// `uploadState` is the typed view of it. The default lets older stores migrate as `pending`.
    var uploadStateRaw: String = "pending"
    /// Why the server refused the photo, when `uploadState` is `failed`.
    var failureMessage: String?

    init(id: UUID = UUID(), fileName: String, bytes: Int64 = 0, createdAt: Date = .now) {
        self.id = id
        self.fileName = fileName
        self.bytes = bytes
        self.createdAt = createdAt
    }

    var uploadState: PhotoUploadState {
        get { PhotoUploadState(rawValue: uploadStateRaw) ?? .pending }
        set { uploadStateRaw = newValue.rawValue }
    }
}
