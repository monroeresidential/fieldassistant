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

/// `ReachabilityProviding` over `NWPathMonitor`. The monitor reports on the main queue, so changes
/// arrive in order and the value is published on the main actor for views to observe.
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
            MainActor.assumeIsolated { self?.update(reachable) }
        }
        monitor.start(queue: .main)
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
