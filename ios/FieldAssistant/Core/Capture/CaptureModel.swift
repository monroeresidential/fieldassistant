import CoreLocation
import Foundation
import Observation
import UIKit

/// One capture session: a live transcript while the user talks, photos, and one location fix,
/// turned into a `LocalDraft` waiting to be sent.
@Observable
@MainActor
final class CaptureModel {
    enum State: Equatable {
        case idle, requestingPermission, recording, stopped
        case denied(TranscriberAuthorization)
        case failed(String)
    }

    let draftID: UUID
    private(set) var state: State = .idle
    private(set) var transcript = ""
    private(set) var photos: [LocalPhoto] = []
    private(set) var elapsed: Duration = .zero
    private(set) var coordinate: CLLocationCoordinate2D?
    /// Why recording ended on its own (a recognizer error, an interruption) while text was kept,
    /// or `nil` when the user stopped it.
    private(set) var notice: String?

    @ObservationIgnored private let transcriber: Transcriber
    @ObservationIgnored private let location: LocationOnce?
    @ObservationIgnored private let clock: any Clock<Duration>
    @ObservationIgnored private let photoWriter: PhotoWriter
    @ObservationIgnored private var timerTask: Task<Void, Never>?
    /// Whether a recognizer stream was started and has not finished yet.
    @ObservationIgnored private var listening = false
    /// Callers of `waitForFinalTranscript` parked until the stream finishes or their timeout fires.
    @ObservationIgnored private var finalWaiters: [UUID: CheckedContinuation<Void, Never>] = [:]
    /// Set once the transcript was typed or the capture discarded; later recognizer results are dropped.
    @ObservationIgnored private var ignoresRecognizer = false
    /// Photos handed to the saver and not yet back. They count against `PhotoLimits.maxPerDraft`
    /// and hold Build, so the draft's photos always match the files on disk.
    private var savingPhotos = 0
    /// Library picks still loading from the picker, before their bytes reach the saver. Counted from
    /// the moment the selection arrives, so Build waits for the whole selection.
    private var pendingPicks = 0
    /// Set by `discard()`; a photo whose save finishes afterwards is deleted instead of added.
    @ObservationIgnored private var discarded = false

    init(
        draftID: UUID = UUID(),
        transcriber: Transcriber,
        location: LocationOnce? = nil,
        clock: any Clock<Duration> = ContinuousClock(),
        photoWriter: PhotoWriter = PhotoWriter()
    ) {
        self.draftID = draftID
        self.transcriber = transcriber
        self.location = location
        self.clock = clock
        self.photoWriter = photoWriter
    }

    var canBuild: Bool {
        state == .stopped && !transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !isSavingPhotos
    }

    /// Whether a photo is still loading from the picker or being written; Build waits for it.
    var isSavingPhotos: Bool { savingPhotos > 0 || pendingPicks > 0 }

    /// A picker selection of `count` photos arrived: they hold Build until each one is saved or
    /// fails to load (`finishPick()`), or the selection is abandoned (`endPicking()`).
    func beginPicking(_ count: Int) {
        pendingPicks += max(0, count)
    }

    /// One picked photo is done: saved, refused, or unreadable.
    func finishPick() {
        pendingPicks = max(0, pendingPicks - 1)
    }

    /// The selection is over; any picks not yet accounted for stop holding Build.
    func endPicking() {
        pendingPicks = 0
    }

    /// Asks for permission, then records until `stop()`. Returns once recording has begun (or failed
    /// to); the transcript keeps updating in the background.
    func start() async {
        guard state == .idle else { return }
        state = .requestingPermission
        let authorization = await transcriber.requestAuthorization()
        // While the prompt was up the capture may have been discarded, typed instead, or left.
        guard !ignoresRecognizer, state == .requestingPermission, !Task.isCancelled else {
            if state == .requestingPermission { state = ignoresRecognizer ? .stopped : .idle }
            return
        }
        guard authorization == .granted else {
            state = .denied(authorization)
            return
        }

        let stream: AsyncThrowingStream<String, Error>
        do {
            _ = try DraftFiles.directory(for: draftID)
            stream = try transcriber.start(audioFile: DraftFiles.audioURL(for: draftID))
        } catch {
            state = .failed(error.localizedDescription)
            return
        }
        state = .recording
        listening = true
        Task { await listen(to: stream) }
        timerTask = Task { await tick() }
        if let location, coordinate == nil {
            Task { coordinate = await location.fix() }
        }
    }

    func stop() {
        guard state == .recording else { return }
        transcriber.stop()
        timerTask?.cancel()
        state = .stopped
    }

    /// The capture screen went away. The camera's full-screen cover hides it too, and the user comes
    /// back to the same recording, so the microphone keeps running under the cover; leaving by any
    /// other route stops it. Stop, not discard: a successful Build also leaves and owns the files now.
    func screenDisappeared(underCamera: Bool) {
        guard !underCamera else { return }
        stop()
    }

    /// Waits for the recognizer's final result after `stop()`, which can land a moment later and
    /// carries the punctuated text. Returns at once when no stream is running, and after `timeout`
    /// if the recognizer never finishes.
    func waitForFinalTranscript(timeout: Duration = .seconds(3)) async {
        guard listening else { return }
        let id = UUID()
        let timer = Task { [weak self] in
            try? await Task.sleep(for: timeout)
            self?.resumeWaiter(id)
        }
        await withCheckedContinuation { finalWaiters[id] = $0 }
        timer.cancel()
    }

    /// Abandons the capture: stops the microphone and deletes the audio and photos on disk. A
    /// `start()` still waiting on the permission prompt returns without opening the microphone.
    func discard() {
        ignoresRecognizer = true
        discarded = true
        if state == .recording { transcriber.stop() }
        if state == .requestingPermission || state == .recording { state = .stopped }
        timerTask?.cancel()
        photos = []
        DraftFiles.removeAll(for: draftID)
    }

    /// Adds a library photo from the picker's bytes. The saving (downscale, metadata strip, JPEG
    /// encode) runs off the main actor. The photo keeps the GPS it had and never gets the draft's
    /// location. Throws `PhotoSaverError.tooManyPhotos` once the draft has
    /// `PhotoLimits.maxPerDraft` photos.
    func addPhoto(data: Data) async throws {
        let draftID = draftID, write = photoWriter.library
        try await addPhoto {
            try await write(data, draftID)
        }
    }

    /// Adds a camera photo with the picker's `mediaMetadata`, tagged with the draft's location when
    /// there is one; see `addPhoto(data:)`.
    func addPhoto(image: UIImage, metadata: [CFString: Any]?) async throws {
        let draftID = draftID, location = coordinate, write = photoWriter.camera
        // The metadata is an immutable dictionary of property-list values from the camera picker,
        // read but never changed by the saver, so handing it to the background task is safe.
        nonisolated(unsafe) let metadata = metadata
        try await addPhoto {
            try await write(image, metadata, draftID, location)
        }
    }

    /// Counts photos still being saved against the limit, so quick taps cannot pass it.
    private func addPhoto(_ save: @escaping @Sendable () async throws -> SavedPhoto) async throws {
        guard photos.count + savingPhotos < PhotoLimits.maxPerDraft else { throw PhotoSaverError.tooManyPhotos }
        savingPhotos += 1
        defer { savingPhotos -= 1 }
        let saved = try await Task.detached(priority: .userInitiated, operation: save).value
        guard !discarded else {
            // The capture was abandoned while the photo was being written; its folder is gone.
            DraftFiles.removeAll(for: draftID)
            return
        }
        photos.append(saved.makeLocalPhoto())
    }

    func removePhoto(_ photo: LocalPhoto) {
        photos.removeAll { $0.id == photo.id }
        try? FileManager.default.removeItem(at: DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName))
    }

    /// The draft to store, owned by the signed-in Procore user so other accounts on the phone never see it.
    /// It takes this capture's photos, which the model keeps: when storing the draft fails,
    /// `DraftStore.insert` detaches them again and the next Build hands them to a new draft.
    func makeDraft(companyID: Int64, projectID: Int64, owner: Int64) -> LocalDraft {
        LocalDraft(
            id: draftID,
            companyID: companyID,
            projectID: projectID,
            ownerProcoreUserID: owner,
            status: .waiting,
            transcript: transcript,
            capturedAt: .now,
            latitude: coordinate?.latitude,
            longitude: coordinate?.longitude,
            photos: photos
        )
    }

    #if DEBUG
    /// Skips the microphone; for the simulator and demos in debug builds.
    /// Stops the microphone first if it is running, so a late recognizer result cannot replace the text.
    func setTypedTranscript(_ text: String) {
        ignoresRecognizer = true
        if state == .recording {
            transcriber.stop()
            timerTask?.cancel()
        }
        transcript = text
        state = .stopped
    }
    #endif

    /// Takes every transcript the recognizer sends, including the final one that arrives after
    /// `stop()`. An error or the end of the stream only counts while still recording (after stop it
    /// is the recognizer winding down, for example no speech heard); it then ends the recording.
    private func listen(to stream: AsyncThrowingStream<String, Error>) async {
        defer {
            listening = false
            for id in Array(finalWaiters.keys) { resumeWaiter(id) }
        }
        do {
            for try await text in stream where !ignoresRecognizer {
                transcript = text
            }
            endedWhileRecording(reason: nil)
        } catch {
            endedWhileRecording(reason: error.localizedDescription)
        }
    }

    /// The recognizer stopped by itself (an error, an interruption, or a stream that just ended)
    /// while the user was still recording. The microphone stops. Text heard so far is kept and can
    /// be built, with a notice; with nothing heard, the capture has failed.
    private func endedWhileRecording(reason: String?) {
        guard !ignoresRecognizer, state == .recording else { return }
        transcriber.stop()
        timerTask?.cancel()
        let reason = reason ?? "The microphone stopped."
        if transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            state = .failed(reason)
        } else {
            notice = "Recording stopped: \(reason)"
            state = .stopped
        }
    }

    private func resumeWaiter(_ id: UUID) {
        finalWaiters.removeValue(forKey: id)?.resume()
    }

    /// Adds a second to `elapsed` each second of the injected clock. It counts ticks rather than
    /// reading instants: generic code over the stored `any Clock<Duration>` crashes Swift 6.4, and
    /// a recording timer does not need sub-second accuracy.
    private func tick() async {
        while !Task.isCancelled {
            do {
                try await clock.sleep(for: .seconds(1), tolerance: nil)
            } catch {
                return
            }
            elapsed += .seconds(1)
        }
    }
}
