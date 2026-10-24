import Foundation

/// Keeps the signed-in user's drafts moving without a screen open: sends `waiting` drafts for
/// extraction once there is a connection, and reconciles `submitting` drafts (left by a killed app
/// or a submit that outlived its screen) from the server. Other statuses are never touched here.
@MainActor
final class DraftSync {
    private let store: DraftStore
    private let service: @MainActor () -> DraftService
    private let reachability: ReachabilityProviding
    /// The Procore user whose drafts sync; `nil` means nobody is signed in and a pass does nothing.
    private let owner: @MainActor () -> Int64?
    /// Sends one waiting draft. `AppState.send`, so Build, Recent, Retry and sync share one path.
    private let send: @MainActor (LocalDraft) async -> AppState.SendOutcome

    private var listener: Task<Void, Never>?
    /// The running pass, if any. Overlapping triggers wait for it instead of starting a second one.
    private var pass: Task<Void, Never>?
    /// Identifies `pass`, so a pass cancelled by `stop` cannot clear its successor when it ends.
    private var passID = 0
    /// Set when a trigger arrives during a pass; the pass then runs once more before it ends.
    private var passAgain = false

    init(
        store: DraftStore,
        service: @escaping @MainActor () -> DraftService,
        reachability: ReachabilityProviding,
        owner: @escaping @MainActor () -> Int64?,
        send: @escaping @MainActor (LocalDraft) async -> AppState.SendOutcome
    ) {
        self.store = store
        self.service = service
        self.reachability = reachability
        self.owner = owner
        self.send = send
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

    /// Runs one pass, or, if one is running, makes it run once more and waits for it.
    func runOnce() async {
        if let pass {
            passAgain = true
            await pass.value
            return
        }
        passID += 1
        let id = passID
        let task = Task { [weak self] in
            guard let self else { return }
            repeat {
                passAgain = false
                await performPass()
            } while passAgain && !Task.isCancelled
            if passID == id { pass = nil }
        }
        pass = task
        await task.value
    }

    // MARK: - Private

    private func performPass() async {
        guard reachability.isReachable, let owner = owner() else { return }
        guard await resendWaiting(owner: owner) else { return }
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

    /// Fetches each submitting draft and records where the server has it. A draft still
    /// `submitting` there is left as it is; `submitted` also deletes its files.
    private func reconcileSubmitting(owner: Int64) async {
        for draft in store.drafts(withStatus: .submitting, owner: owner) {
            guard draft.status == .submitting else { continue }
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
