import Foundation
import Network
import Observation

/// Whether the phone has a network path, and a stream of its changes. Draft sync resends waiting
/// drafts when it turns true; Home shows the "No signal" banner while it is false.
@MainActor
protocol ReachabilityProviding: AnyObject, Sendable {
    var isReachable: Bool { get }
    /// Yields the new value on every change. Each access returns a new stream.
    var updates: AsyncStream<Bool> { get }
}

/// `ReachabilityProviding` over `NWPathMonitor`, which reports on a background queue; the value is
/// published on the main actor so views observe it.
@Observable
@MainActor
final class Reachability: ReachabilityProviding {
    /// Optimistic until the monitor's first report, so a launch with signal does not skip its sync pass.
    private(set) var isReachable = true

    @ObservationIgnored private let monitor = NWPathMonitor()
    @ObservationIgnored private var continuations: [UUID: AsyncStream<Bool>.Continuation] = [:]

    init() {
        monitor.pathUpdateHandler = { [weak self] path in
            let reachable = path.status == .satisfied
            Task { @MainActor [weak self] in self?.update(reachable) }
        }
        monitor.start(queue: DispatchQueue(label: "tools.fieldassistant.reachability"))
    }

    deinit {
        monitor.cancel()
    }

    var updates: AsyncStream<Bool> {
        let (stream, continuation) = AsyncStream.makeStream(of: Bool.self)
        let id = UUID()
        continuations[id] = continuation
        continuation.onTermination = { [weak self] _ in
            Task { @MainActor [weak self] in self?.continuations[id] = nil }
        }
        return stream
    }

    private func update(_ reachable: Bool) {
        guard reachable != isReachable else { return }
        isReachable = reachable
        for continuation in continuations.values { continuation.yield(reachable) }
    }
}
