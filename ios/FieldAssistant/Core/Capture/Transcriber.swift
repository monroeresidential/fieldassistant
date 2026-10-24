import Foundation

/// Whether the phone may transcribe: both microphone and speech recognition must be allowed, and
/// the device must support on-device recognition for English.
enum TranscriberAuthorization: Equatable, Sendable {
    case granted, deniedMicrophone, deniedSpeech, unsupported
}

/// Turns the microphone into a live transcript while saving the audio. Main-actor isolated because
/// the real implementation owns an audio engine and a recognizer that are not thread-safe.
@MainActor
protocol Transcriber: Sendable {
    func requestAuthorization() async -> TranscriberAuthorization
    /// Starts recording into `audioFile`. Each element is the full transcript so far, not a delta.
    /// The stream finishes after `stop()` once the recognizer delivers its final result.
    func start(audioFile: URL) throws -> AsyncThrowingStream<String, Error>
    func stop()
}
