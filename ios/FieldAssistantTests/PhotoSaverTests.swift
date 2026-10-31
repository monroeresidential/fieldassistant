import CoreLocation
import ImageIO
import UIKit
import UniformTypeIdentifiers
import XCTest
@testable import FieldAssistant

final class PhotoSaverTests: XCTestCase {
    private var draftIDs: [UUID] = []

    override func tearDown() {
        draftIDs.forEach(DraftFiles.removeAll(for:))
        super.tearDown()
    }

    private func newDraftID() -> UUID {
        let id = UUID()
        draftIDs.append(id)
        return id
    }

    func testSaveDownscalesLongEdgeTo2500() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 4000, height: 3000), draftID: draftID, location: nil)

        let properties = try Self.properties(of: photo, draftID: draftID)
        let width = try XCTUnwrap(properties[kCGImagePropertyPixelWidth] as? Int)
        let height = try XCTUnwrap(properties[kCGImagePropertyPixelHeight] as? Int)
        XCTAssertLessThanOrEqual(width, 2500)
        XCTAssertEqual(width, 2500)
        XCTAssertEqual(height, 1875, "the height scales with the width")
    }

    func testSaveKeepsOrientationTagWithoutRotatingPixels() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 4000, height: 3000, orientation: 6), draftID: draftID, location: nil)

        let properties = try Self.properties(of: photo, draftID: draftID)
        XCTAssertEqual(properties[kCGImagePropertyOrientation] as? Int, 6)
        XCTAssertEqual(properties[kCGImagePropertyPixelWidth] as? Int, 2500)
        XCTAssertEqual(properties[kCGImagePropertyPixelHeight] as? Int, 1875, "pixels stay as stored; the tag rotates them on display")

        // What the capture thumbnail loads: UIKit applies the tag, so the photo shows upright.
        let url = DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName)
        let shown = try XCTUnwrap(UIImage(contentsOfFile: url.path(percentEncoded: false)))
        XCTAssertEqual(shown.imageOrientation, .right)
        XCTAssertEqual(shown.size, CGSize(width: 1875, height: 2500))
    }

    func testSaveKeepsGPSAndDropsOtherEXIF() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 4000, height: 3000), draftID: draftID, location: nil)

        let properties = try Self.properties(of: photo, draftID: draftID)
        let gps = try XCTUnwrap(properties[kCGImagePropertyGPSDictionary] as? [CFString: Any])
        XCTAssertEqual(try XCTUnwrap(gps[kCGImagePropertyGPSLatitude] as? Double), 40.7128, accuracy: 0.0001)
        XCTAssertEqual(gps[kCGImagePropertyGPSLatitudeRef] as? String, "N")
        let exif = properties[kCGImagePropertyExifDictionary] as? [CFString: Any] ?? [:]
        XCTAssertNil(exif[kCGImagePropertyExifDateTimeOriginal], "capture time is stripped")
        let tiff = properties[kCGImagePropertyTIFFDictionary] as? [CFString: Any] ?? [:]
        XCTAssertNil(tiff[kCGImagePropertyTIFFMake], "camera make is stripped")
    }

    func testSaveCameraPhotoWritesDraftLocation() throws {
        let draftID = newDraftID()
        let image = UIImage(cgImage: Self.cgImage(width: 400, height: 300))
        let photo = try PhotoSaver.save(
            image: image,
            metadata: [kCGImagePropertyOrientation: 6],
            draftID: draftID,
            location: CLLocationCoordinate2D(latitude: 41.89, longitude: -87.63)
        )

        let properties = try Self.properties(of: photo, draftID: draftID)
        let gps = try XCTUnwrap(properties[kCGImagePropertyGPSDictionary] as? [CFString: Any])
        XCTAssertEqual(try XCTUnwrap(gps[kCGImagePropertyGPSLatitude] as? Double), 41.89, accuracy: 0.0001)
        XCTAssertEqual(gps[kCGImagePropertyGPSLatitudeRef] as? String, "N")
        XCTAssertEqual(try XCTUnwrap(gps[kCGImagePropertyGPSLongitude] as? Double), 87.63, accuracy: 0.0001)
        XCTAssertEqual(gps[kCGImagePropertyGPSLongitudeRef] as? String, "W")
        XCTAssertEqual(properties[kCGImagePropertyOrientation] as? Int, 6)
    }

    func testSaveLibraryPhotoWithoutGPSAndNoLocationHasNoGPS() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 800, height: 600, gps: false), draftID: draftID, location: nil)

        let properties = try Self.properties(of: photo, draftID: draftID)
        XCTAssertNil(properties[kCGImagePropertyGPSDictionary])
    }

    func testSaveSmallPhotoIsNotUpscaled() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 800, height: 600), draftID: draftID, location: nil)

        let properties = try Self.properties(of: photo, draftID: draftID)
        XCTAssertEqual(properties[kCGImagePropertyPixelWidth] as? Int, 800)
        XCTAssertEqual(properties[kCGImagePropertyPixelHeight] as? Int, 600)
    }

    func testSaveSetsBytes() throws {
        let draftID = newDraftID()
        let photo = try PhotoSaver.save(data: Self.jpeg(width: 800, height: 600), draftID: draftID, location: nil)

        let url = DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName)
        let size = try XCTUnwrap(FileManager.default.attributesOfItem(atPath: url.path(percentEncoded: false))[.size] as? Int64)
        XCTAssertGreaterThan(photo.bytes, 0)
        XCTAssertEqual(photo.bytes, size)
        let folder = try FileManager.default.contentsOfDirectory(atPath: url.deletingLastPathComponent().path(percentEncoded: false))
        XCTAssertEqual(folder, [photo.fileName], "the temporary file is renamed into place, nothing left behind")
        XCTAssertEqual(photo.fileName, "\(photo.id.uuidString).jpg")
        XCTAssertEqual(photo.uploadState, .pending)
        XCTAssertNil(photo.failureMessage)
    }

    func testOutputPropertiesContainOnlyOrientationGPSAndQuality() throws {
        let fromLocation = PhotoSaver.outputProperties(
            orientation: .right,
            gps: nil,
            location: CLLocationCoordinate2D(latitude: -33.86, longitude: 151.21)
        )
        XCTAssertEqual(
            Set(fromLocation.keys.map { $0 as String }),
            [
                kCGImageDestinationLossyCompressionQuality as String,
                kCGImagePropertyOrientation as String,
                kCGImagePropertyGPSDictionary as String,
            ]
        )
        XCTAssertEqual(fromLocation[kCGImageDestinationLossyCompressionQuality] as? CGFloat, 0.85)
        XCTAssertEqual(fromLocation[kCGImagePropertyOrientation] as? UInt32, CGImagePropertyOrientation.right.rawValue)
        let gps = try XCTUnwrap(fromLocation[kCGImagePropertyGPSDictionary] as? [CFString: Any])
        XCTAssertEqual(Set(gps.keys.map { $0 as String }), [
            kCGImagePropertyGPSLatitude as String, kCGImagePropertyGPSLatitudeRef as String,
            kCGImagePropertyGPSLongitude as String, kCGImagePropertyGPSLongitudeRef as String,
        ])
        XCTAssertEqual(gps[kCGImagePropertyGPSLatitudeRef] as? String, "S")
        XCTAssertEqual(gps[kCGImagePropertyGPSLongitudeRef] as? String, "E")
        XCTAssertEqual(gps[kCGImagePropertyGPSLatitude] as? Double, 33.86)

        let ownGPS: [CFString: Any] = [kCGImagePropertyGPSLatitude: 1.5, kCGImagePropertyGPSLatitudeRef: "N"]
        let fromPhoto = PhotoSaver.outputProperties(
            orientation: nil,
            gps: ownGPS,
            location: CLLocationCoordinate2D(latitude: -33.86, longitude: 151.21)
        )
        XCTAssertEqual(
            Set(fromPhoto.keys.map { $0 as String }),
            [kCGImageDestinationLossyCompressionQuality as String, kCGImagePropertyGPSDictionary as String],
            "no orientation tag when there is none; the photo's own GPS wins over the draft's"
        )
        let kept = try XCTUnwrap(fromPhoto[kCGImagePropertyGPSDictionary] as? [CFString: Any])
        XCTAssertEqual(kept[kCGImagePropertyGPSLatitude] as? Double, 1.5)

        let bare = PhotoSaver.outputProperties(orientation: nil, gps: nil, location: nil)
        XCTAssertEqual(Set(bare.keys.map { $0 as String }), [kCGImageDestinationLossyCompressionQuality as String])
    }

    // MARK: - Fixtures

    private static func properties(of photo: LocalPhoto, draftID: UUID) throws -> [CFString: Any] {
        let url = DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName)
        let source = try XCTUnwrap(CGImageSourceCreateWithURL(url as CFURL, nil))
        XCTAssertEqual(CGImageSourceGetType(source) as String?, UTType.jpeg.identifier)
        return try XCTUnwrap(CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any])
    }

    private static func cgImage(width: Int, height: Int) -> CGImage {
        let context = CGContext(
            data: nil,
            width: width,
            height: height,
            bitsPerComponent: 8,
            bytesPerRow: 0,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
        )!
        context.setFillColor(UIColor.orange.cgColor)
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        context.setFillColor(UIColor.blue.cgColor)
        context.fill(CGRect(x: 0, y: 0, width: width / 4, height: height / 4))
        return context.makeImage()!
    }

    /// A JPEG the way a phone's library hands it over: camera make, capture time, orientation and GPS.
    private static func jpeg(width: Int, height: Int, orientation: Int = 6, gps: Bool = true) -> Data {
        let data = NSMutableData()
        let destination = CGImageDestinationCreateWithData(data, UTType.jpeg.identifier as CFString, 1, nil)!
        var properties: [CFString: Any] = [
            kCGImagePropertyOrientation: orientation,
            kCGImagePropertyTIFFDictionary: [kCGImagePropertyTIFFMake: "Apple", kCGImagePropertyTIFFModel: "iPhone 17"],
            kCGImagePropertyExifDictionary: [kCGImagePropertyExifDateTimeOriginal: "2026:10:30 09:41:00"],
        ]
        if gps {
            properties[kCGImagePropertyGPSDictionary] = [
                kCGImagePropertyGPSLatitude: 40.7128,
                kCGImagePropertyGPSLatitudeRef: "N",
                kCGImagePropertyGPSLongitude: 74.0060,
                kCGImagePropertyGPSLongitudeRef: "W",
            ]
        }
        CGImageDestinationAddImage(destination, cgImage(width: width, height: height), properties as CFDictionary)
        CGImageDestinationFinalize(destination)
        return data as Data
    }
}
