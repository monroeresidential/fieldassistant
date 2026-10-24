import AVFoundation
import Foundation
import Speech

enum SpeechTranscriberError: LocalizedError {
    case unavailable
    case interrupted

    var errorDescription: String? {
        switch self {
        case .unavailable: "Speech recognition is not available right now."
        case .interrupted: "A call or another app took the microphone."
        }
    }
}

/// On-device English transcription with `SFSpeechRecognizer`, fed by an `AVAudioEngine` tap that
/// also writes the audio to the draft's file. Nothing leaves the phone.
@MainActor
final class SpeechTranscriber: Transcriber {
    private let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US"))
    private var engine: AVAudioEngine?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    /// The running stream's continuation, so an audio interruption can end it.
    private var continuation: AsyncThrowingStream<String, Error>.Continuation?
    private var interruptionObserver: NSObjectProtocol?

    func requestAuthorization() async -> TranscriberAuthorization {
        guard let recognizer, recognizer.supportsOnDeviceRecognition else { return .unsupported }
        guard await Self.requestSpeechAuthorization() == .authorized else { return .deniedSpeech }
        guard await AVAudioApplication.requestRecordPermission() else { return .deniedMicrophone }
        return .granted
    }

    func start(audioFile: URL) throws -> AsyncThrowingStream<String, Error> {
        guard let recognizer, recognizer.isAvailable else { throw SpeechTranscriberError.unavailable }
        stop()
        task?.cancel()

        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.record, mode: .measurement, options: .duckOthers)
        try session.setActive(true, options: .notifyOthersOnDeactivation)

        let engine = AVAudioEngine()
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        // A call or another app holding the input leaves a format with no rate or channels, and a
        // tap installed with it crashes.
        guard format.sampleRate > 0, format.channelCount > 0 else {
            try? session.setActive(false, options: .notifyOthersOnDeactivation)
            throw SpeechTranscriberError.unavailable
        }
        let file = try AVAudioFile(
            forWriting: audioFile,
            settings: [
                AVFormatIDKey: kAudioFormatMPEG4AAC,
                AVSampleRateKey: format.sampleRate,
                AVNumberOfChannelsKey: format.channelCount,
            ],
            commonFormat: format.commonFormat,
            interleaved: format.isInterleaved
        )

        let request = SFSpeechAudioBufferRecognitionRequest()
        request.requiresOnDeviceRecognition = true
        request.shouldReportPartialResults = true
        request.addsPunctuation = true

        let (stream, continuation) = AsyncThrowingStream<String, Error>.makeStream()
        input.installTap(onBus: 0, bufferSize: 4096, format: format, block: Self.tap(TapSink(request: request, file: file)))
        task = recognizer.recognitionTask(with: request, resultHandler: Self.resultHandler(continuation))

        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            task?.cancel()
            task = nil
            try? session.setActive(false, options: .notifyOthersOnDeactivation)
            throw error
        }
        self.engine = engine
        self.request = request
        self.continuation = continuation
        interruptionObserver = NotificationCenter.default.addObserver(
            forName: AVAudioSession.interruptionNotification,
            object: session,
            queue: .main,
            using: Self.interruptionHandler { [weak self] in self?.interrupted() }
        )
        return stream
    }

    /// Stops the microphone and asks the recognizer for its final result; the stream finishes when
    /// that arrives. Safe to call more than once.
    func stop() {
        if let interruptionObserver { NotificationCenter.default.removeObserver(interruptionObserver) }
        interruptionObserver = nil
        continuation = nil
        guard let engine else { return }
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        self.engine = nil
        request = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }

    /// An interruption began (a call, Siri, another app's recording): the engine has already
    /// stopped, so the recording ends here and the stream finishes with the reason.
    private func interrupted() {
        guard engine != nil else { return }
        let continuation = self.continuation
        stop()
        task?.cancel()
        task = nil
        continuation?.finish(throwing: SpeechTranscriberError.interrupted)
    }

    // The callbacks below run on audio and recognizer queues, so they are built in nonisolated
    // functions; a closure formed inside this main-actor class would trap when called off-main.

    private nonisolated static func requestSpeechAuthorization() async -> SFSpeechRecognizerAuthorizationStatus {
        await withCheckedContinuation { continuation in
            SFSpeechRecognizer.requestAuthorization { status in continuation.resume(returning: status) }
        }
    }

    /// Delivered on the main queue (the observer asks for it); calls `onBegan` when an interruption starts.
    private nonisolated static func interruptionHandler(
        _ onBegan: @escaping @MainActor @Sendable () -> Void
    ) -> @Sendable (Notification) -> Void {
        { notification in
            guard let raw = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                  AVAudioSession.InterruptionType(rawValue: raw) == .began else { return }
            MainActor.assumeIsolated { onBegan() }
        }
    }

    private nonisolated static func tap(_ sink: TapSink) -> AVAudioNodeTapBlock {
        { buffer, _ in sink.append(buffer) }
    }

    private nonisolated static func resultHandler(
        _ continuation: AsyncThrowingStream<String, Error>.Continuation
    ) -> @Sendable (SFSpeechRecognitionResult?, Error?) -> Void {
        { result, error in
            if let result {
                continuation.yield(result.bestTranscription.formattedString)
                if result.isFinal { continuation.finish() }
            } else if let error {
                continuation.finish(throwing: error)
            }
        }
    }
}

/// Receives microphone buffers on the audio thread. Only that thread touches it once the tap is
/// installed, and the request and file accept appends from any one thread.
private final class TapSink: @unchecked Sendable {
    private let request: SFSpeechAudioBufferRecognitionRequest
    private let file: AVAudioFile

    init(request: SFSpeechAudioBufferRecognitionRequest, file: AVAudioFile) {
        self.request = request
        self.file = file
    }

    func append(_ buffer: AVAudioPCMBuffer) {
        request.append(buffer)
        try? file.write(from: buffer)
    }
}
