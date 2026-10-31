import CoreLocation
import Foundation
import ImageIO
import UIKit
import UniformTypeIdentifiers

/// What a saved photo may be: its size, its JPEG quality, and how many a draft can hold.
enum PhotoLimits {
    static let longEdge: CGFloat = 2500
    static let jpegQuality: CGFloat = 0.85
    static let maxPerDraft = 10
}

enum PhotoSaverError: LocalizedError {
    case encodingFailed, tooManyPhotos

    var errorDescription: String? {
        switch self {
        case .encodingFailed: "The photo could not be saved."
        case .tooManyPhotos: "A draft can have at most \(PhotoLimits.maxPerDraft) photos."
        }
    }
}

/// The file a save wrote, in a form that can cross from the saver's background task to the main
/// actor (a `LocalPhoto` is a SwiftData model and cannot).
struct SavedPhoto: Sendable {
    let id: UUID
    let fileName: String
    let bytes: Int64

    func makeLocalPhoto() -> LocalPhoto {
        LocalPhoto(id: id, fileName: fileName, bytes: bytes)
    }
}

/// How `CaptureModel` saves photos, injectable so tests can hold a save open. The defaults are the
/// real saver. A library photo never gets the draft's location: it keeps the GPS it had, or none.
struct PhotoWriter: Sendable {
    var library: @Sendable (_ data: Data, _ draftID: UUID) async throws -> SavedPhoto = { data, draftID in
        try PhotoSaver.write(data: data, draftID: draftID, location: nil)
    }

    var camera: @Sendable (
        _ image: UIImage,
        _ metadata: [CFString: Any]?,
        _ draftID: UUID,
        _ location: CLLocationCoordinate2D?
    ) async throws -> SavedPhoto = { image, metadata, draftID, location in
        try PhotoSaver.write(image: image, metadata: metadata, draftID: draftID, location: location)
    }
}

/// Writes a photo into the draft's folder as `<photo id>.jpg`, final as saved: at most 2,500 px on
/// the long edge (never upscaled), JPEG at quality 0.85, and only two pieces of metadata carried
/// over, the orientation tag and the GPS dictionary. A photo without GPS of its own gets the
/// draft's location when there is one. Call it off the main actor; the work is ImageIO decoding
/// and encoding.
enum PhotoSaver {
    /// Library path: the picker's bytes, with whatever metadata they carry.
    nonisolated static func save(data: Data, draftID: UUID, location: CLLocationCoordinate2D?) throws -> LocalPhoto {
        try write(data: data, draftID: draftID, location: location).makeLocalPhoto()
    }

    /// Camera path: the captured image and the picker's `mediaMetadata` (orientation lives there).
    nonisolated static func save(
        image: UIImage,
        metadata: [CFString: Any]?,
        draftID: UUID,
        location: CLLocationCoordinate2D?
    ) throws -> LocalPhoto {
        try write(image: image, metadata: metadata, draftID: draftID, location: location).makeLocalPhoto()
    }

    /// `save(data:draftID:location:)` returning a value that can leave a background task.
    nonisolated static func write(data: Data, draftID: UUID, location: CLLocationCoordinate2D?) throws -> SavedPhoto {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { throw PhotoSaverError.encodingFailed }
        let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any] ?? [:]
        return try write(
            source: source,
            orientation: orientation(in: properties),
            gps: properties[kCGImagePropertyGPSDictionary] as? [CFString: Any],
            draftID: draftID,
            location: location
        )
    }

    /// `save(image:metadata:draftID:location:)` returning a value that can leave a background task.
    /// The camera's pixels are stored unrotated with the turn in `imageOrientation`; they are
    /// re-encoded losslessly enough (quality 1) to go through the same thumbnail path as a library
    /// photo, and the turn is written as the orientation tag.
    nonisolated static func write(
        image: UIImage,
        metadata: [CFString: Any]?,
        draftID: UUID,
        location: CLLocationCoordinate2D?
    ) throws -> SavedPhoto {
        guard let cgImage = image.cgImage else { throw PhotoSaverError.encodingFailed }
        let buffer = NSMutableData()
        guard let encoder = CGImageDestinationCreateWithData(buffer, UTType.jpeg.identifier as CFString, 1, nil) else {
            throw PhotoSaverError.encodingFailed
        }
        CGImageDestinationAddImage(encoder, cgImage, [kCGImageDestinationLossyCompressionQuality: 1.0] as CFDictionary)
        guard CGImageDestinationFinalize(encoder),
              let source = CGImageSourceCreateWithData(buffer as CFData, nil)
        else { throw PhotoSaverError.encodingFailed }
        let metadata = metadata ?? [:]
        return try write(
            source: source,
            orientation: orientation(in: metadata) ?? CGImagePropertyOrientation(image.imageOrientation),
            gps: metadata[kCGImagePropertyGPSDictionary] as? [CFString: Any],
            draftID: draftID,
            location: location
        )
    }

    /// Pure: the properties dictionary written to the destination (orientation + GPS only).
    static func outputProperties(
        orientation: CGImagePropertyOrientation?,
        gps: [CFString: Any]?,
        location: CLLocationCoordinate2D?
    ) -> [CFString: Any] {
        var properties: [CFString: Any] = [kCGImageDestinationLossyCompressionQuality: PhotoLimits.jpegQuality]
        if let orientation {
            properties[kCGImagePropertyOrientation] = orientation.rawValue
        }
        if let gps {
            properties[kCGImagePropertyGPSDictionary] = gps
        } else if let location {
            properties[kCGImagePropertyGPSDictionary] = [
                kCGImagePropertyGPSLatitude: abs(location.latitude),
                kCGImagePropertyGPSLatitudeRef: location.latitude < 0 ? "S" : "N",
                kCGImagePropertyGPSLongitude: abs(location.longitude),
                kCGImagePropertyGPSLongitudeRef: location.longitude < 0 ? "W" : "E",
            ] as [CFString: Any]
        }
        return properties
    }

    private nonisolated static func orientation(in properties: [CFString: Any]) -> CGImagePropertyOrientation? {
        guard let value = properties[kCGImagePropertyOrientation] as? NSNumber else { return nil }
        return CGImagePropertyOrientation(rawValue: value.uint32Value)
    }

    /// Downscales without applying the orientation (the tag carries it), then writes the JPEG to a
    /// temporary file in the draft folder and renames it into place, so a half-written photo never
    /// sits under its final name.
    private nonisolated static func write(
        source: CGImageSource,
        orientation: CGImagePropertyOrientation?,
        gps: [CFString: Any]?,
        draftID: UUID,
        location: CLLocationCoordinate2D?
    ) throws -> SavedPhoto {
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceThumbnailMaxPixelSize: PhotoLimits.longEdge,
            kCGImageSourceCreateThumbnailWithTransform: false,
        ]
        guard let thumbnail = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
            throw PhotoSaverError.encodingFailed
        }

        let folder = try DraftFiles.directory(for: draftID)
        let id = UUID()
        let fileName = "\(id.uuidString).jpg"
        let finalURL = DraftFiles.photoURL(draftID: draftID, fileName: fileName)
        let tempURL = folder.appending(path: ".\(fileName).tmp", directoryHint: .notDirectory)
        defer { try? FileManager.default.removeItem(at: tempURL) }

        guard let destination = CGImageDestinationCreateWithURL(tempURL as CFURL, UTType.jpeg.identifier as CFString, 1, nil) else {
            throw PhotoSaverError.encodingFailed
        }
        let properties = outputProperties(orientation: orientation, gps: gps, location: location)
        CGImageDestinationAddImage(destination, thumbnail, properties as CFDictionary)
        guard CGImageDestinationFinalize(destination) else { throw PhotoSaverError.encodingFailed }
        // Sized before the rename, so nothing that can throw runs once the `.jpg` exists.
        let size = try FileManager.default.attributesOfItem(atPath: tempURL.path(percentEncoded: false))[.size] as? NSNumber
        try FileManager.default.moveItem(at: tempURL, to: finalURL)
        return SavedPhoto(id: id, fileName: fileName, bytes: size?.int64Value ?? 0)
    }
}

private extension CGImagePropertyOrientation {
    nonisolated init(_ orientation: UIImage.Orientation) {
        switch orientation {
        case .up: self = .up
        case .upMirrored: self = .upMirrored
        case .down: self = .down
        case .downMirrored: self = .downMirrored
        case .left: self = .left
        case .leftMirrored: self = .leftMirrored
        case .right: self = .right
        case .rightMirrored: self = .rightMirrored
        @unknown default: self = .up
        }
    }
}
