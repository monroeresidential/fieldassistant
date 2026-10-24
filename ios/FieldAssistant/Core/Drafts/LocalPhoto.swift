import Foundation
import SwiftData

/// A photo taken for a draft. The image is at `DraftFiles.photoURL(draftID:fileName:)`.
@Model
final class LocalPhoto {
    var id: UUID
    var fileName: String
    var createdAt: Date
    var draft: LocalDraft?

    init(id: UUID = UUID(), fileName: String, createdAt: Date = .now) {
        self.id = id
        self.fileName = fileName
        self.createdAt = createdAt
    }
}
