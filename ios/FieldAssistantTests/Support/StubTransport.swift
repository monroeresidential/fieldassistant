import Foundation
import HTTPTypes
import OpenAPIRuntime

/// `ClientTransport` that returns scripted responses in order and records every request it receives.
final class StubTransport: ClientTransport, @unchecked Sendable {
    struct Recorded {
        var request: HTTPRequest
        var body: [UInt8]?
    }

    struct ScriptExhausted: Error {}

    private let lock = NSLock()
    private var script: [(HTTPResponse, HTTPBody?)]
    private var recorded: [Recorded] = []

    init(_ script: [(HTTPResponse, HTTPBody?)] = []) {
        self.script = script
    }

    /// Convenience: a script of bare status codes with no bodies.
    convenience init(statuses: [HTTPResponse.Status]) {
        self.init(statuses.map { (HTTPResponse(status: $0), nil) })
    }

    var requests: [Recorded] { lock.withLock { recorded } }

    func enqueue(_ status: HTTPResponse.Status) {
        lock.withLock { script.append((HTTPResponse(status: status), nil)) }
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        var bytes: [UInt8]?
        if let body {
            bytes = try await [UInt8](collecting: body, upTo: .max)
        }
        let recordedBytes = bytes
        return try lock.withLock {
            recorded.append(Recorded(request: request, body: recordedBytes))
            guard !script.isEmpty else { throw ScriptExhausted() }
            return script.removeFirst()
        }
    }
}
