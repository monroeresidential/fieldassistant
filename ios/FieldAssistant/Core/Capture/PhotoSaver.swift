import Foundation
import UIKit

enum PhotoSaverError: LocalizedError {
    case encodingFailed

    var errorDescription: String? {
        switch self {
        case .encodingFailed: "The photo could not be saved."
        }
    }
}

/// Writes a captured photo into the draft's folder as JPEG. Downscaling and EXIF stripping happen
/// at upload, not here.
enum PhotoSaver {
    static func save(_ image: UIImage, draftID: UUID) throws -> LocalPhoto {
        guard let data = image.jpegData(compressionQuality: 0.85) else { throw PhotoSaverError.encodingFailed }
        _ = try DraftFiles.directory(for: draftID)
        let id = UUID()
        let fileName = "\(id.uuidString).jpg"
        try data.write(to: DraftFiles.photoURL(draftID: draftID, fileName: fileName), options: .atomic)
        return LocalPhoto(id: id, fileName: fileName)
    }
}
