import CoreLocation
import UIKit
import XCTest
@testable import FieldAssistant

@MainActor
final class CaptureModelTests: XCTestCase {
    private var draftIDs: [UUID] = []

    override func tearDown() async throws {
        draftIDs.forEach(DraftFiles.removeAll(for:))
        try await super.tearDown()
    }

    private func makeModel(
        transcriber: FakeTranscriber,
        location: LocationOnce? = nil,
        clock: any Clock<Duration> = ContinuousClock()
    ) -> CaptureModel {
        let model = CaptureModel(transcriber: transcriber, location: location, clock: clock)
        draftIDs.append(model.draftID)
        return model
    }

    func testStartWithoutPermissionReportsDenied() async {
        let transcriber = FakeTranscriber(authorization: .deniedMicrophone)
        let model = makeModel(transcriber: transcriber)

        await model.start()

        XCTAssertEqual(model.state, .denied(.deniedMicrophone))
        XCTAssertNil(transcriber.startedAudioFile, "the stream is never started without permission")
        XCTAssertFalse(model.canBuild)
    }

    func testTranscriptUpdatesWhileRecording() async throws {
        let transcriber = FakeTranscriber()
        let clock = ManualClock()
        let model = makeModel(transcriber: transcriber, clock: clock)

        await model.start()
        XCTAssertEqual(model.state, .recording)
        XCTAssertEqual(transcriber.startedAudioFile, DraftFiles.audioURL(for: model.draftID))

        let continuation = try XCTUnwrap(transcriber.continuation)
        continuation.yield("twisted")
        continuation.yield("twisted wood column")
        await waitUntil { model.transcript == "twisted wood column" }

        XCTAssertEqual(model.transcript, "twisted wood column")
        XCTAssertEqual(model.state, .recording)
        XCTAssertFalse(model.canBuild, "nothing builds while still recording")

        XCTAssertEqual(model.elapsed, .zero)
        for second in 1...2 {
            await waitUntil { clock.sleeperCount == 1 }
            clock.advance(by: .seconds(1))
            await waitUntil { model.elapsed == .seconds(second) }
        }
        XCTAssertEqual(model.elapsed, .seconds(2))
    }

    func testStopEnablesBuildOnlyWithText() async throws {
        let empty = FakeTranscriber()
        let silent = makeModel(transcriber: empty)
        await silent.start()
        silent.stop()
        XCTAssertEqual(silent.state, .stopped)
        XCTAssertEqual(empty.stopCount, 1)
        XCTAssertFalse(silent.canBuild)

        let spoken = FakeTranscriber()
        let model = makeModel(transcriber: spoken)
        await model.start()
        try XCTUnwrap(spoken.continuation).yield("  Need the slab edge detail at grid C ")
        await waitUntil { !model.transcript.isEmpty }
        model.stop()

        XCTAssertEqual(model.state, .stopped)
        XCTAssertTrue(model.canBuild)
    }

    func testMakeDraftCarriesPhotosAndLocation() async throws {
        let transcriber = FakeTranscriber()
        let coordinate = CLLocationCoordinate2D(latitude: 41.8781, longitude: -87.6298)
        let model = makeModel(transcriber: transcriber, location: LocationOnce(stub: coordinate))

        await model.start()
        try XCTUnwrap(transcriber.continuation).yield("Column at grid B4 is twisted")
        await waitUntil { model.coordinate != nil && !model.transcript.isEmpty }
        model.stop()

        try model.addPhoto(Self.image())
        try model.addPhoto(Self.image())
        let removed = try XCTUnwrap(model.photos.last)
        let removedURL = DraftFiles.photoURL(draftID: model.draftID, fileName: removed.fileName)
        XCTAssertTrue(FileManager.default.fileExists(atPath: removedURL.path(percentEncoded: false)))
        model.removePhoto(removed)
        XCTAssertFalse(FileManager.default.fileExists(atPath: removedURL.path(percentEncoded: false)), "removing a photo deletes its file")

        let kept = try XCTUnwrap(model.photos.first)
        XCTAssertEqual(model.photos.count, 1)
        XCTAssertTrue(kept.fileName.hasSuffix(".jpg"))
        let keptURL = DraftFiles.photoURL(draftID: model.draftID, fileName: kept.fileName)
        XCTAssertNotNil(UIImage(contentsOfFile: keptURL.path(percentEncoded: false)), "the photo is a readable JPEG on disk")

        let before = Date.now
        let draft = model.makeDraft(companyID: 7, projectID: 99)

        XCTAssertEqual(draft.id, model.draftID)
        XCTAssertEqual(draft.companyID, 7)
        XCTAssertEqual(draft.projectID, 99)
        XCTAssertEqual(draft.feature, "rfi")
        XCTAssertEqual(draft.status, .waiting)
        XCTAssertEqual(draft.transcript, "Column at grid B4 is twisted")
        XCTAssertEqual(draft.photos.map(\.id), [kept.id])
        XCTAssertEqual(draft.latitude, 41.8781)
        XCTAssertEqual(draft.longitude, -87.6298)
        XCTAssertGreaterThanOrEqual(draft.capturedAt, before)
    }

    #if DEBUG
    func testTypedTranscriptSkipsRecording() {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)

        model.setTypedTranscript("Confirm the header size over the garage door")

        XCTAssertEqual(model.state, .stopped)
        XCTAssertEqual(model.transcript, "Confirm the header size over the garage door")
        XCTAssertNil(transcriber.startedAudioFile)
        XCTAssertTrue(model.canBuild)
    }
    #endif

    private static func image() -> UIImage {
        UIGraphicsImageRenderer(size: CGSize(width: 8, height: 8)).image { context in
            UIColor.orange.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 8, height: 8))
        }
    }

    /// Lets the model's background tasks run until `condition` holds, failing after two seconds.
    private func waitUntil(
        _ condition: () -> Bool,
        file: StaticString = #filePath,
        line: UInt = #line
    ) async {
        let deadline = ContinuousClock.now + .seconds(2)
        while !condition() {
            guard ContinuousClock.now < deadline else {
                XCTFail("condition not met within two seconds", file: file, line: line)
                return
            }
            try? await Task.sleep(for: .milliseconds(5))
        }
    }
}
