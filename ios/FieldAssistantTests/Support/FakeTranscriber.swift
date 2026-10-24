import Foundation
@testable import FieldAssistant

/// A `Transcriber` the test drives: it returns a scripted authorization, and once started the test
/// yields cumulative transcripts through `continuation`.
@MainActor
final class FakeTranscriber: Transcriber {
    var authorization: TranscriberAuthorization
    private(set) var continuation: AsyncThrowingStream<String, Error>.Continuation?
    private(set) var startedAudioFile: URL?
    private(set) var stopCount = 0

    init(authorization: TranscriberAuthorization = .granted) {
        self.authorization = authorization
    }

    func requestAuthorization() async -> TranscriberAuthorization {
        authorization
    }

    func start(audioFile: URL) throws -> AsyncThrowingStream<String, Error> {
        startedAudioFile = audioFile
        let (stream, continuation) = AsyncThrowingStream<String, Error>.makeStream()
        self.continuation = continuation
        return stream
    }

    func stop() {
        stopCount += 1
        continuation?.finish()
    }
}

/// A clock that only moves when the test calls `advance(by:)`.
final class ManualClock: Clock, @unchecked Sendable {
    struct Instant: InstantProtocol {
        var offset: Duration

        func advanced(by duration: Duration) -> Instant { Instant(offset: offset + duration) }
        func duration(to other: Instant) -> Duration { other.offset - offset }
        static func < (lhs: Instant, rhs: Instant) -> Bool { lhs.offset < rhs.offset }
    }

    private struct Sleeper {
        let deadline: Instant
        let continuation: CheckedContinuation<Void, Never>
    }

    private let lock = NSLock()
    private var current = Instant(offset: .zero)
    private var sleepers: [Sleeper] = []

    var now: Instant { lock.withLock { current } }
    var minimumResolution: Duration { .zero }
    /// How many tasks are parked in `sleep(until:)`, so a test can advance only once they are.
    var sleeperCount: Int { lock.withLock { sleepers.count } }

    /// Sleepers wake only on `advance(by:)`; a cancelled sleeper stays parked, which is fine for tests.
    func sleep(until deadline: Instant, tolerance: Duration? = nil) async throws {
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            let resumeNow = lock.withLock {
                if deadline <= current { return true }
                sleepers.append(Sleeper(deadline: deadline, continuation: continuation))
                return false
            }
            if resumeNow { continuation.resume() }
        }
        try Task.checkCancellation()
    }

    func advance(by duration: Duration) {
        let due = lock.withLock {
            current = current.advanced(by: duration)
            let due = sleepers.filter { $0.deadline <= current }
            sleepers.removeAll { $0.deadline <= current }
            return due
        }
        due.forEach { $0.continuation.resume() }
    }
}
