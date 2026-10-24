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

    @ObservationIgnored private let transcriber: Transcriber
    @ObservationIgnored private let location: LocationOnce?
    @ObservationIgnored private let clock: any Clock<Duration>
    @ObservationIgnored private var timerTask: Task<Void, Never>?
    /// Whether a recognizer stream was started and has not finished yet.
    @ObservationIgnored private var listening = false
    /// Callers of `waitForFinalTranscript` parked until the stream finishes or their timeout fires.
    @ObservationIgnored private var finalWaiters: [UUID: CheckedContinuation<Void, Never>] = [:]
    /// Set once the transcript was typed or the capture discarded; later recognizer results are dropped.
    @ObservationIgnored private var ignoresRecognizer = false

    init(
        draftID: UUID = UUID(),
        transcriber: Transcriber,
        location: LocationOnce? = nil,
        clock: any Clock<Duration> = ContinuousClock()
    ) {
        self.draftID = draftID
        self.transcriber = transcriber
        self.location = location
        self.clock = clock
    }

    var canBuild: Bool {
        state == .stopped && !transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
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
        if state == .recording { transcriber.stop() }
        if state == .requestingPermission || state == .recording { state = .stopped }
        timerTask?.cancel()
        photos = []
        DraftFiles.removeAll(for: draftID)
    }

    func addPhoto(_ image: UIImage) throws {
        photos.append(try PhotoSaver.save(image, draftID: draftID))
    }

    func removePhoto(_ photo: LocalPhoto) {
        photos.removeAll { $0.id == photo.id }
        try? FileManager.default.removeItem(at: DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName))
    }

    /// The draft to store, owned by the signed-in Procore user so other accounts on the phone never see it.
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
    /// `stop()`. An error only counts while still recording; after stop it is the recognizer
    /// winding down (for example, no speech heard).
    private func listen(to stream: AsyncThrowingStream<String, Error>) async {
        defer {
            listening = false
            for id in Array(finalWaiters.keys) { resumeWaiter(id) }
        }
        do {
            for try await text in stream where !ignoresRecognizer {
                transcript = text
            }
        } catch {
            guard !ignoresRecognizer else { return }
            if state == .recording {
                transcriber.stop()
                timerTask?.cancel()
                state = .failed(error.localizedDescription)
            }
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
