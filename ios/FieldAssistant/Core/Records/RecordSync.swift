import Foundation
import Observation

/// Keeps one project's on-device mirror current: optionally asks the backend to pull from Procore,
/// then pages the delta from the stored cursor until the backend has no more. One driver per
/// selected project; `AppState` replaces it when the project changes and stops it on sign-out.
@MainActor
@Observable
final class RecordSync {
    enum Status: Equatable {
        case idle
        case syncing
        case synced(Date)
        case failed(code: String?, message: String)
    }

    private(set) var status: Status = .idle

    @ObservationIgnored private let store: RecordStore
    @ObservationIgnored private let service: () -> RecordService
    @ObservationIgnored private let companyID: Int64
    @ObservationIgnored private let projectID: Int64
    @ObservationIgnored private let kinds: [RecordKind]
    @ObservationIgnored private let reachability: any ReachabilityProviding
    /// The running pass, if any. A call that arrives while it runs waits for it instead of starting another.
    @ObservationIgnored private var run: Task<Void, Never>?
    /// Identifies `run`, so a pass cancelled by `stop` cannot clear its successor when it ends.
    @ObservationIgnored private var runID = 0
    /// Set by calls that join the running pass, so the run can tell whether it owes them another pass.
    @ObservationIgnored private var pendingForce = false
    @ObservationIgnored private var pendingFull = false

    init(
        store: RecordStore,
        service: @escaping () -> RecordService,
        companyID: Int64,
        projectID: Int64,
        kinds: [RecordKind],
        reachability: any ReachabilityProviding
    ) {
        self.store = store
        self.service = service
        self.companyID = companyID
        self.projectID = projectID
        self.kinds = kinds
        self.reachability = reachability
    }

    /// Runs one pass, or, if one is running, waits for it and returns. `force` asks the backend to
    /// pull from Procore first even without a network path showing; `full` makes that pull a full sweep.
    /// A forced or full call that joins a pass which was neither makes the run do one more pass with
    /// those flags, so a manual Sync during a foreground run still reaches the sync endpoint.
    func runOnce(force: Bool = false, full: Bool = false) async {
        if let run {
            pendingForce = pendingForce || force
            pendingFull = pendingFull || full
            await run.value
            return
        }
        runID += 1
        let id = runID
        pendingForce = false
        pendingFull = false
        let task = Task { [weak self] in
            guard let self else { return }
            var passForce = force
            var passFull = full
            while true {
                await performPass(force: passForce, full: passFull)
                if Task.isCancelled { break }
                // Consume what joined during this pass (or before it started).
                let joinedForce = pendingForce
                let joinedFull = pendingFull
                pendingForce = false
                pendingFull = false
                guard (joinedForce && !passForce) || (joinedFull && !passFull) else { break }
                passForce = passForce || joinedForce
                passFull = passFull || joinedFull
            }
            if runID == id { run = nil }
        }
        run = task
        await task.value
    }

    /// Cancels the running pass. A cancelled pass applies nothing more and records nothing; the
    /// status falls back to the last sync so a stopped driver never shows a spinner.
    func stop() {
        run?.cancel()
        run = nil
        pendingForce = false
        pendingFull = false
        if status == .syncing {
            status = lastSyncedAt().map(Status.synced) ?? .idle
        }
    }

    // MARK: - Private

    private func performPass(force: Bool, full: Bool) async {
        guard force || reachability.isReachable else {
            if let syncedAt = lastSyncedAt() {
                status = .synced(syncedAt)
            } else {
                status = .failed(code: "offline", message: "No signal")
            }
            return
        }
        status = .syncing
        do {
            if force || full {
                _ = try await service().sync(companyID: companyID, projectID: projectID, kinds: kinds, full: full)
                if Task.isCancelled { return }
            }
            for kind in kinds {
                guard try await pageAll(kind) else { return }
            }
        } catch {
            if Task.isCancelled { return }
            let failure = APIFailure(error)
            let code = Self.code(for: failure)
            for kind in kinds {
                try? store.markFailed(projectID: projectID, kind: kind, code: code, message: failure.userMessage)
            }
            status = .failed(code: code, message: failure.userMessage)
            return
        }
        status = settledStatus()
    }

    /// Pages `kind` from its stored cursor until the backend has no more. Returns `false` when the
    /// pass was cancelled; a page that arrives after cancellation is dropped.
    private func pageAll(_ kind: RecordKind) async throws -> Bool {
        var since = store.state(projectID: projectID, kind: kind)?.nextSince ?? 0
        while true {
            let page = try await service().page(
                companyID: companyID, projectID: projectID, kind: kind, since: since, force: false
            )
            if Task.isCancelled { return false }
            try store.apply(page, projectID: projectID, kind: kind)
            guard page.has_more else { return true }
            since = page.next_since
        }
    }

    /// After a pass that threw nothing: failed if a pull reported an error, otherwise synced.
    private func settledStatus() -> Status {
        for kind in kinds {
            if let state = store.state(projectID: projectID, kind: kind), let message = state.lastError {
                return .failed(code: state.lastErrorCode, message: message)
            }
        }
        if let syncedAt = lastSyncedAt() { return .synced(syncedAt) }
        return .idle
    }

    /// The oldest last-sync time across the kinds, or nil if any kind has never synced.
    private func lastSyncedAt() -> Date? {
        var oldest: Date?
        for kind in kinds {
            guard let syncedAt = store.state(projectID: projectID, kind: kind)?.lastSyncedAt else { return nil }
            oldest = min(oldest ?? syncedAt, syncedAt)
        }
        return oldest
    }

    /// The error-body code the failure came from, where `APIFailure` keeps enough to say.
    private static func code(for failure: APIFailure) -> String? {
        switch failure {
        case .offline: "offline"
        case .permissionDenied: "procore_permission_denied"
        case .reauthRequired: "reauth_required"
        case .validation: "bad_request"
        case .server: nil
        }
    }
}
