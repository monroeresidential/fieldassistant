import Foundation
import UIKit

/// Asks iOS for the grace period it gives an app that leaves the foreground, so a pass already
/// running (a Submit tapped just before the phone is pocketed) keeps uploading. `DraftSync` holds
/// one for each pass. Injected, so tests and previews run without the system's.
@MainActor
protocol BackgroundGrace {
    /// Begins a grace period. `expired` runs if the system ends it first; it must end the period.
    func begin(name: String, expired: @escaping @MainActor @Sendable () -> Void) -> UIBackgroundTaskIdentifier
    func end(_ identifier: UIBackgroundTaskIdentifier)
}

/// `UIApplication`'s background task: the app's grace period.
struct SystemBackgroundGrace: BackgroundGrace {
    func begin(name: String, expired: @escaping @MainActor @Sendable () -> Void) -> UIBackgroundTaskIdentifier {
        UIApplication.shared.beginBackgroundTask(withName: name, expirationHandler: expired)
    }

    func end(_ identifier: UIBackgroundTaskIdentifier) {
        guard identifier != .invalid else { return }
        UIApplication.shared.endBackgroundTask(identifier)
    }
}

/// Keeps the signed-in user's drafts moving without a screen open: sends `waiting` drafts for
/// extraction once there is a connection, runs the queued submits (`submitPending`) through the
/// `SubmitRunner`, and reconciles `submitting` drafts the server owns (left by a killed app or a
/// submit that outlived its screen) from the server. Other statuses are never touched here.
@MainActor
final class DraftSync {
    private let store: DraftStore
    private let service: @MainActor () -> DraftService
    /// Runs queued submits; shared with Review, so a draft is never run twice at once.
    private let runner: SubmitRunner
    private let reachability: ReachabilityProviding
    /// The Procore user whose drafts sync; `nil` means nobody is signed in and a pass does nothing.
    private let owner: @MainActor () -> Int64?
    /// Sends one waiting draft. `AppState.send`, so Build, Recent, Retry and sync share one path.
    private let send: @MainActor (LocalDraft) async -> AppState.SendOutcome
    /// Holds each pass through the app's grace period after it leaves the foreground; `nil` runs
    /// passes without one (tests).
    private let grace: BackgroundGrace?
    /// The grace period each running pass holds, by pass id.
    private var graceIDs: [Int: UIBackgroundTaskIdentifier] = [:]

    private var listener: Task<Void, Never>?
    /// The running pass, if any. Overlapping triggers wait for it instead of starting a second one.
    private var pass: Task<Void, Never>?
    /// Identifies `pass`, so a pass cancelled by `stop` cannot clear its successor when it ends.
    private var passID = 0
    /// Set when a trigger arrives during a pass; the pass then runs once more before it ends.
    private var passAgain = false

    /// A `submitting` draft stamped this recently may be a submit still on its way to the server
    /// (Review stamps it before the POST), so reconciliation skips it. Covers URLSession's 60 s timeout.
    static let inFlightWindow: TimeInterval = 90

    init(
        store: DraftStore,
        service: @escaping @MainActor () -> DraftService,
        runner: SubmitRunner,
        reachability: ReachabilityProviding,
        owner: @escaping @MainActor () -> Int64?,
        send: @escaping @MainActor (LocalDraft) async -> AppState.SendOutcome,
        grace: BackgroundGrace? = nil
    ) {
        self.store = store
        self.service = service
        self.runner = runner
        self.reachability = reachability
        self.owner = owner
        self.send = send
        self.grace = grace
    }

    /// Runs a pass now and again each time the network path comes back. The app also calls
    /// `runOnce` when it becomes active.
    func start() {
        stop()
        let updates = reachability.updates
        listener = Task { [weak self] in
            await self?.runOnce()
            for await reachable in updates where reachable {
                await self?.runOnce()
            }
        }
    }

    /// Cancels the listener and any running pass. A cancelled pass records nothing.
    func stop() {
        listener?.cancel()
        listener = nil
        pass?.cancel()
        pass = nil
        passAgain = false
    }

    /// Cancels the running pass, if any, and leaves the listener running. A cancelled pass records
    /// nothing. For a background task the system has expired.
    func cancelPass() {
        pass?.cancel()
        pass = nil
        passAgain = false
    }

    /// Runs one pass, or, if one is running, makes it run once more and waits for it. The pass holds
    /// the app's grace period, so leaving the app does not suspend it at once; if the system ends
    /// the grace period first, the pass is cancelled (and records nothing).
    func runOnce() async {
        if let pass {
            passAgain = true
            await pass.value
            return
        }
        passID += 1
        let id = passID
        beginGrace(for: id)
        let task = Task { [weak self] in
            guard let self else { return }
            repeat {
                passAgain = false
                await performPass()
            } while passAgain && !Task.isCancelled
            if passID == id { pass = nil }
            endGrace(for: id)
        }
        pass = task
        await task.value
    }

    /// Whether a submit run is in progress for the draft, from a pass or from Review.
    func isRunning(draftID: UUID) -> Bool {
        runner.isRunning(draftID)
    }

    // MARK: - Private

    private func beginGrace(for id: Int) {
        guard let grace else { return }
        let identifier = grace.begin(name: "DraftSync pass") { [weak self] in
            guard let self else { return }
            if passID == id { cancelPass() }
            endGrace(for: id)
        }
        guard identifier != .invalid else { return }
        graceIDs[id] = identifier
    }

    private func endGrace(for id: Int) {
        guard let identifier = graceIDs.removeValue(forKey: id) else { return }
        grace?.end(identifier)
    }

    private func performPass() async {
        guard reachability.isReachable, let owner = owner() else { return }
        guard await resendWaiting(owner: owner) else { return }
        guard await runQueued(owner: owner) else { return }
        await reconcileSubmitting(owner: owner)
    }

    /// Sends each waiting draft, oldest first. Returns `false` when the pass should end: no
    /// connection, or cancelled. A refused draft stays `waiting` with its message and the pass goes on.
    private func resendWaiting(owner: Int64) async -> Bool {
        for draft in store.drafts(withStatus: .waiting, owner: owner) {
            // A screen may have sent it while this pass waited on an earlier draft.
            guard draft.status == .waiting else { continue }
            let outcome = await send(draft)
            if Task.isCancelled || outcome == .waiting { return false }
        }
        return true
    }

    /// Runs each queued submit, oldest first. Returns `false` when the pass should end: no
    /// connection, a session that needs signing in again, or cancelled. A draft refused, or left
    /// queued by a server failure of its own, does not hold up the next one.
    private func runQueued(owner: Int64) async -> Bool {
        for draft in store.draftsPendingSubmit(owner: owner) {
            // Review or Stop may have settled it, or Review may be running it, while this pass waited.
            guard draft.submitPending, !runner.isRunning(draft.id) else { continue }
            let outcome = await runner.run(draft)
            if Task.isCancelled { return false }
            switch outcome {
            case .stillPending(.offline), .stillPending(.reauthRequired):
                return false
            case .stillPending(.server), .stillPending(.alreadyRunning), .accepted, .refused, .photoFailed:
                continue
            }
        }
        return true
    }

    /// Fetches each submitting draft the server owns and records where the server has it. A queued
    /// draft (`submitPending`) is the runner's and is skipped. A draft still `submitting` there is
    /// left as it is; `submitted` also deletes its files.
    private func reconcileSubmitting(owner: Int64) async {
        let inFlightSince = Date.now.addingTimeInterval(-Self.inFlightWindow)
        for draft in store.drafts(withStatus: .submitting, owner: owner) {
            guard draft.status == .submitting, !draft.submitPending, draft.updatedAt < inFlightSince else { continue }
            let response: DraftResponse
            do {
                response = try await service().get(id: draft.id, companyID: draft.companyID)
            } catch {
                // The DraftService reports cancellation as a server failure, so check the task first.
                if Task.isCancelled || APIFailure(error) == .offline { return }
                // Leave the draft as it is; the next pass tries again.
                continue
            }
            let status = LocalDraftStatus(server: response.status)
            if status != .submitting {
                draft.apply(response)
                try? store.save()
                if status == .submitted { DraftFiles.removeAll(for: draft.id) }
            }
            if Task.isCancelled { return }
        }
    }
}
