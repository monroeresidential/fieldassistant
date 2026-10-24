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

    func addPhoto(_ image: UIImage) throws {
        photos.append(try PhotoSaver.save(image, draftID: draftID))
    }

    func removePhoto(_ photo: LocalPhoto) {
        photos.removeAll { $0.id == photo.id }
        try? FileManager.default.removeItem(at: DraftFiles.photoURL(draftID: draftID, fileName: photo.fileName))
    }

    func makeDraft(companyID: Int64, projectID: Int64) -> LocalDraft {
        LocalDraft(
            id: draftID,
            companyID: companyID,
            projectID: projectID,
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
    func setTypedTranscript(_ text: String) {
        transcript = text
        state = .stopped
    }
    #endif

    /// Takes every transcript the recognizer sends, including the final one that arrives after
    /// `stop()`. An error only counts while still recording; after stop it is the recognizer
    /// winding down (for example, no speech heard).
    private func listen(to stream: AsyncThrowingStream<String, Error>) async {
        do {
            for try await text in stream {
                transcript = text
            }
        } catch {
            if state == .recording {
                transcriber.stop()
                timerTask?.cancel()
                state = .failed(error.localizedDescription)
            }
        }
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
