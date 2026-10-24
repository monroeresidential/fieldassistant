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

    func testRecognizerErrorWithTranscriptEndsStopped() async throws {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)
        await model.start()
        let continuation = try XCTUnwrap(transcriber.continuation)
        continuation.yield("Need the slab edge detail at grid C")
        await waitUntil { !model.transcript.isEmpty }
        try model.addPhoto(Self.image())

        continuation.finish(throwing: SpeechTranscriberError.interrupted)
        await waitUntil { model.state != .recording }

        XCTAssertEqual(model.state, .stopped)
        XCTAssertEqual(model.transcript, "Need the slab edge detail at grid C")
        XCTAssertEqual(model.notice, "Recording stopped: A call or another app took the microphone.")
        XCTAssertTrue(model.canBuild, "the text heard so far can still be built")
        XCTAssertEqual(transcriber.stopCount, 1, "the microphone is stopped")
        XCTAssertEqual(model.photos.count, 1)
        let photo = try XCTUnwrap(model.photos.first)
        XCTAssertTrue(FileManager.default.fileExists(atPath: DraftFiles.photoURL(draftID: model.draftID, fileName: photo.fileName).path(percentEncoded: false)))
    }

    func testRecognizerErrorWithoutTranscriptFails() async throws {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)
        await model.start()

        try XCTUnwrap(transcriber.continuation).finish(throwing: SpeechTranscriberError.interrupted)
        await waitUntil { model.state != .recording }

        XCTAssertEqual(model.state, .failed("A call or another app took the microphone."))
        XCTAssertNil(model.notice)
        XCTAssertEqual(transcriber.stopCount, 1)
    }

    func testStreamEndWhileRecordingStops() async throws {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)
        await model.start()
        let continuation = try XCTUnwrap(transcriber.continuation)
        continuation.yield("Header height at grid D")
        await waitUntil { !model.transcript.isEmpty }

        continuation.finish()
        await waitUntil { model.state != .recording }

        XCTAssertEqual(model.state, .stopped)
        XCTAssertEqual(transcriber.stopCount, 1, "the microphone does not keep running")
        XCTAssertEqual(model.notice, "Recording stopped: The microphone stopped.")
        XCTAssertTrue(model.canBuild)
    }

    func testStopThenStreamEndHasNoNotice() async throws {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)
        await model.start()
        try XCTUnwrap(transcriber.continuation).yield("Header height")
        await waitUntil { !model.transcript.isEmpty }

        model.stop()
        await model.waitForFinalTranscript()

        XCTAssertEqual(model.state, .stopped)
        XCTAssertNil(model.notice)
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
        let draft = model.makeDraft(companyID: 7, projectID: 99, owner: 42)

        XCTAssertEqual(draft.id, model.draftID)
        XCTAssertEqual(draft.companyID, 7)
        XCTAssertEqual(draft.ownerProcoreUserID, 42)
        XCTAssertEqual(draft.projectID, 99)
        XCTAssertEqual(draft.feature, "rfi")
        XCTAssertEqual(draft.status, .waiting)
        XCTAssertEqual(draft.transcript, "Column at grid B4 is twisted")
        XCTAssertEqual(draft.photos.map(\.id), [kept.id])
        XCTAssertEqual(draft.latitude, 41.8781)
        XCTAssertEqual(draft.longitude, -87.6298)
        XCTAssertGreaterThanOrEqual(draft.capturedAt, before)
    }

    func testWaitForFinalTranscriptTakesTheResultAfterStop() async throws {
        let transcriber = FakeTranscriber(finishesOnStop: false)
        let model = makeModel(transcriber: transcriber)
        await model.start()
        let continuation = try XCTUnwrap(transcriber.continuation)
        continuation.yield("twisted column at the air handler")
        await waitUntil { !model.transcript.isEmpty }
        model.stop()

        let waiting = Task { await model.waitForFinalTranscript(timeout: .seconds(5)) }
        continuation.yield("Twisted column at the air handler.")
        continuation.finish()
        await waiting.value

        XCTAssertEqual(model.transcript, "Twisted column at the air handler.")
        XCTAssertTrue(model.canBuild)
    }

    func testWaitForFinalTranscriptGivesUpAfterTimeout() async throws {
        let transcriber = FakeTranscriber(finishesOnStop: false)
        let model = makeModel(transcriber: transcriber)
        await model.start()
        try XCTUnwrap(transcriber.continuation).yield("slab edge")
        await waitUntil { !model.transcript.isEmpty }
        model.stop()

        await model.waitForFinalTranscript(timeout: .milliseconds(50))

        XCTAssertEqual(model.transcript, "slab edge")
    }

    func testDiscardStopsRecordingAndRemovesFiles() async throws {
        let transcriber = FakeTranscriber()
        let model = makeModel(transcriber: transcriber)
        await model.start()
        try model.addPhoto(Self.image())
        XCTAssertTrue(FileManager.default.fileExists(atPath: DraftFiles.audioURL(for: model.draftID).deletingLastPathComponent().path))

        model.discard()

        XCTAssertEqual(transcriber.stopCount, 1)
        XCTAssertTrue(model.photos.isEmpty)
        XCTAssertFalse(FileManager.default.fileExists(atPath: DraftFiles.audioURL(for: model.draftID).deletingLastPathComponent().path))
    }

    func testDiscardDuringPermissionPromptNeverStartsMic() async {
        let transcriber = FakeTranscriber(holdsAuthorization: true)
        let model = makeModel(transcriber: transcriber)

        let starting = Task { await model.start() }
        await waitUntil { model.state == .requestingPermission && transcriber.isHoldingAuthorization }
        model.discard()
        transcriber.releaseAuthorization()
        await starting.value

        XCTAssertNil(transcriber.startedAudioFile, "the microphone never opens")
        XCTAssertNotEqual(model.state, .recording)
        XCTAssertFalse(FileManager.default.fileExists(atPath: DraftFiles.audioURL(for: model.draftID).deletingLastPathComponent().path))
    }

    #if DEBUG
    func testTypedTranscriptDuringPromptWins() async {
        let transcriber = FakeTranscriber(holdsAuthorization: true)
        let model = makeModel(transcriber: transcriber)

        let starting = Task { await model.start() }
        await waitUntil { model.state == .requestingPermission && transcriber.isHoldingAuthorization }
        model.setTypedTranscript("Typed while the prompt was up")
        transcriber.releaseAuthorization()
        await starting.value

        XCTAssertNil(transcriber.startedAudioFile)
        XCTAssertEqual(model.state, .stopped)
        XCTAssertEqual(model.transcript, "Typed while the prompt was up")
        XCTAssertTrue(model.canBuild)
    }

    func testTypedTranscriptWhileRecordingStopsTheMicAndWins() async throws {
        let transcriber = FakeTranscriber(finishesOnStop: false)
        let model = makeModel(transcriber: transcriber)
        await model.start()
        let continuation = try XCTUnwrap(transcriber.continuation)

        model.setTypedTranscript("Typed text")
        continuation.yield("late recognizer text")
        continuation.finish()
        await model.waitForFinalTranscript(timeout: .seconds(5))

        XCTAssertEqual(transcriber.stopCount, 1)
        XCTAssertEqual(model.transcript, "Typed text")
        XCTAssertEqual(model.state, .stopped)
    }

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
