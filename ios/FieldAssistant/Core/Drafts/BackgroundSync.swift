@preconcurrency import BackgroundTasks
import Foundation
import OSLog

/// Gives the draft queue a chance to run while the app is in the background: one app-refresh task
/// (short) and one processing task (longer, needs a connection). Both run exactly one `DraftSync`
/// pass. They are scheduled when the app goes to the background with a draft waiting or queued, and
/// again after a pass when work remains. iOS decides when, if ever, they run.
@MainActor
final class BackgroundSync {
    nonisolated static let refreshID = "\(Bundle.main.bundleIdentifier!).draftsync.refresh"
    nonisolated static let processingID = "\(Bundle.main.bundleIdentifier!).draftsync.processing"

    /// How long after a request is made the system may start it.
    private static let earliestDelay: TimeInterval = 15

    private static let log = Logger(subsystem: "FieldAssistant", category: "BackgroundSync")

    /// Pure: schedule when any draft is waiting or queued.
    nonisolated static func shouldSchedule(waiting: Int, pendingSubmit: Int) -> Bool {
        waiting > 0 || pendingSubmit > 0
    }

    private let run: @MainActor () async -> Void
    private let cancelPass: @MainActor () -> Void
    private let counts: @MainActor () -> (waiting: Int, pendingSubmit: Int)

    /// - Parameters:
    ///   - run: one `DraftSync` pass.
    ///   - cancelPass: cancels the running pass without recording anything; called when the system
    ///     expires a task.
    ///   - counts: the signed-in user's waiting and queued drafts, to decide whether to reschedule.
    init(
        run: @escaping @MainActor () async -> Void,
        cancelPass: @escaping @MainActor () -> Void = {},
        counts: @escaping @MainActor () -> (waiting: Int, pendingSubmit: Int) = { (0, 0) }
    ) {
        self.run = run
        self.cancelPass = cancelPass
        self.counts = counts
    }

    /// Registers the launch handlers for both identifiers. Must run before the app finishes launching.
    func register() {
        for id in [Self.refreshID, Self.processingID] {
            let registered = BGTaskScheduler.shared.register(forTaskWithIdentifier: id, using: .main) { task in
                // Delivered on the main queue (`using: .main`).
                MainActor.assumeIsolated { self.handle(task) }
            }
            if !registered { Self.log.error("could not register \(id, privacy: .public)") }
        }
    }

    /// Submits both requests when there is work. A request already pending for the same identifier
    /// is replaced by the new one.
    func schedule(waiting: Int, pendingSubmit: Int) {
        guard Self.shouldSchedule(waiting: waiting, pendingSubmit: pendingSubmit) else { return }
        let earliest = Date(timeIntervalSinceNow: Self.earliestDelay)

        let refresh = BGAppRefreshTaskRequest(identifier: Self.refreshID)
        refresh.earliestBeginDate = earliest

        let processing = BGProcessingTaskRequest(identifier: Self.processingID)
        processing.earliestBeginDate = earliest
        processing.requiresNetworkConnectivity = true
        processing.requiresExternalPower = false

        for request in [refresh, processing] as [BGTaskRequest] {
            do {
                try BGTaskScheduler.shared.submit(request)
            } catch {
                // Not available (Background App Refresh off, Simulator, too many requests); the
                // foreground triggers still run the queue.
                Self.log.error("could not schedule \(request.identifier, privacy: .public): \(error.localizedDescription, privacy: .public)")
            }
        }
    }

    func cancelAll() {
        BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: Self.refreshID)
        BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: Self.processingID)
    }

    // MARK: - Private

    /// Runs one pass, then completes the task. Reschedules when work remains, otherwise drops the
    /// sibling request, which has nothing left to do.
    private func handle(_ task: BGTask) {
        let pass = Task { [self] in
            await run()
            let remaining = counts()
            if Self.shouldSchedule(waiting: remaining.waiting, pendingSubmit: remaining.pendingSubmit) {
                schedule(waiting: remaining.waiting, pendingSubmit: remaining.pendingSubmit)
            } else {
                cancelAll()
            }
            task.setTaskCompleted(success: !Task.isCancelled)
        }
        task.expirationHandler = { [self] in
            // May arrive off the main queue.
            Task { @MainActor in
                cancelPass()
                pass.cancel()
                // The pass's own completion call comes next and reports failure (cancelled).
            }
        }
    }
}
