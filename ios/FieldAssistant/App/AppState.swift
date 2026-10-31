import FieldAssistantAPI
import Foundation
import Observation
import OpenAPIRuntime
import OpenAPIURLSession
import os
import SwiftData

/// App-wide session state: who is signed in and which company/project is selected.
@Observable
@MainActor
final class AppState {
    enum Phase: Equatable {
        case loading
        case signedOut
        case signedIn(Components.Schemas.User)
        /// A session is stored but the server could not be reached; the user stays signed in.
        case offline
    }

    private enum Keys {
        static let deviceID = "deviceID"
        static let selectedCompanyID = "selectedCompanyID"
        static let selectedProjectID = "selectedProjectID"
        static let draftOwnerID = "draftOwnerProcoreUserID"
    }

    /// What happened when a captured draft was sent for extraction.
    enum SendOutcome: Equatable {
        /// The server extracted it; the draft holds the response and opens in Review.
        case sent
        /// No connection; the draft stays `waiting` until it can be sent.
        case waiting
        /// The server refused it; the draft stays `waiting` with the message.
        case failed(String)
    }

    private static let log = Logger(subsystem: "FieldAssistant", category: "AppState")

    private(set) var phase: Phase = .loading
    /// True after a send found no connection, until a later send gets through. Home shows the
    /// "No signal" banner while it is set and a draft is waiting, which covers a network path that
    /// is up but cannot reach the server.
    private(set) var sendingWaitsForSignal = false
    private(set) var isSigningIn = false
    /// Why the last sign-in attempt failed, or `nil`. `.cancelled` means the user closed the sheet.
    private(set) var signInError: AuthError?
    /// Advances each time a session is (re)established, after `/me` has loaded. Screens key their
    /// load tasks on it so they reload after "Sign in again" or a successful offline retry.
    private(set) var sessionGeneration = 0

    /// The Procore user whose drafts this phone shows and sends. Kept in `UserDefaults` so an
    /// offline launch, which has no `/me`, still shows that user's drafts; `nil` when signed out.
    private(set) var draftOwnerID: Int64? {
        didSet { Self.persist(draftOwnerID, forKey: Keys.draftOwnerID, in: defaults) }
    }

    var selectedCompanyID: Int64? {
        didSet { Self.persist(selectedCompanyID, forKey: Keys.selectedCompanyID, in: defaults) }
    }
    var selectedProjectID: Int64? {
        didSet {
            Self.persist(selectedProjectID, forKey: Keys.selectedProjectID, in: defaults)
            // While `loadMe` fills the selection the phase is not signed in yet; it prefetches itself.
            guard selectedProjectID != oldValue else { return }
            switch phase {
            case .signedIn:
                prefetchLookups()
                startRecordSync()
            case .offline:
                // No `/me` yet: a driver for the new project, not run until a tap or reconnect.
                startRecordSync(run: false)
            case .loading, .signedOut:
                break
            }
        }
    }

    @ObservationIgnored private let store: SessionStore
    @ObservationIgnored private let defaults: UserDefaults
    /// The authenticated API client every feature service uses.
    @ObservationIgnored private(set) var client: Client!
    @ObservationIgnored private var authService: AuthService!
    /// The cached project lookup lists. Set once in `init`; implicitly unwrapped because it needs the
    /// client, which is built after the middleware closures that capture `self`.
    @ObservationIgnored private(set) var lookups: LookupStore!
    /// The running lookup prefetch, if any. A new selection or the end of the session cancels it.
    @ObservationIgnored private(set) var prefetchTask: Task<Void, Never>?
    /// The drafts captured on this phone.
    @ObservationIgnored let drafts: DraftStore
    /// The on-device mirror of project records, filled by `recordSync`.
    @ObservationIgnored let records: RecordStore
    @ObservationIgnored let settings: Settings
    /// Whether the phone has a network path. Home observes it for the "No signal" banner.
    @ObservationIgnored let reachability: ReachabilityProviding
    /// Runs queued submits for Review and for `draftSync`. One for the app's lifetime, so both see
    /// the same running drafts, and built in `init` so Review can queue a submit while offline.
    @ObservationIgnored private(set) var submitRunner: SubmitRunner!
    /// Background refresh and processing tasks that run one `draftSync` pass. Registered by the app
    /// at launch; scheduled from `enteredBackground`.
    @ObservationIgnored private(set) var background: BackgroundSync!
    /// Resends waiting drafts, runs queued submits and reconciles submitting ones while a user is
    /// signed in; `nil` otherwise.
    @ObservationIgnored private(set) var draftSync: DraftSync?
    /// Keeps the selected project's records current while a user is signed in, and reports the last
    /// sync (not running until asked) while the session is offline; `nil` otherwise.
    /// Observed, so a screen that read `nil` updates when the driver is created or replaced.
    private(set) var recordSync: RecordSync?
    /// Whether a `bootstrap` is running, so a background task does not start a second one.
    @ObservationIgnored private var isBootstrapping = false
    /// While the session is `offline`, retries `/me` each time the network path comes back.
    @ObservationIgnored private var offlineRetry: Task<Void, Never>?
    /// Advances each time the session ends. A `/me` that was started before then (an offline retry,
    /// a foreground bootstrap, a banner Retry) sees the change and leaves the signed-out state alone.
    @ObservationIgnored private var sessionEpoch = 0

    var draftService: DraftService { DraftService(client: client) }

    init(
        store: SessionStore,
        presenter: WebAuthPresenter,
        baseURL: URL,
        modelContext: ModelContext,
        transport: ClientTransport? = nil,
        defaults: UserDefaults = .standard,
        reachability: ReachabilityProviding? = nil
    ) {
        self.store = store
        self.defaults = defaults
        self.reachability = reachability ?? Reachability()
        drafts = DraftStore(context: modelContext)
        records = RecordStore(context: modelContext)
        settings = Settings(defaults: defaults)
        selectedCompanyID = Self.storedID(forKey: Keys.selectedCompanyID, in: defaults)
        selectedProjectID = Self.storedID(forKey: Keys.selectedProjectID, in: defaults)
        draftOwnerID = Self.storedID(forKey: Keys.draftOwnerID, in: defaults)

        let middleware = AuthMiddleware(
            store: store,
            refresh: { [weak self] refreshToken in
                guard let service = await self?.authService else { throw CancellationError() }
                return try await service.refresh(refreshToken)
            },
            onSignedOut: { [weak self] in
                await self?.handleSignedOut()
            }
        )
        let client = Client(
            serverURL: baseURL,
            configuration: .fieldAssistant,
            transport: transport ?? URLSessionTransport(),
            middlewares: [middleware, IfNoneMatchMiddleware()]
        )
        self.client = client
        lookups = LookupStore(context: modelContext, service: LookupService(client: client))
        submitRunner = SubmitRunner(store: drafts, service: { DraftService(client: client) })
        authService = AuthService(
            client: client,
            store: store,
            presenter: presenter,
            deviceID: Self.deviceID(in: defaults)
        )
        background = BackgroundSync(
            run: { [weak self] in await self?.runBackgroundPass() },
            cancelPass: { [weak self] in self?.draftSync?.cancelPass() },
            counts: { [weak self] in
                guard let self, let owner = draftOwnerID else { return (0, 0) }
                return (
                    drafts.drafts(withStatus: .waiting, owner: owner).count,
                    drafts.draftsPendingSubmit(owner: owner).count
                )
            }
        )
    }

    /// Decides the first screen: signed out without a stored session, otherwise loads the user.
    func bootstrap() async {
        isBootstrapping = true
        defer { isBootstrapping = false }
        let session: Session?
        do {
            session = try store.load()
        } catch {
            session = nil
        }
        guard session != nil else {
            // Whatever a previous user left behind (a session lost without a sign-out) is not shown.
            clearUserFiles()
            phase = .signedOut
            return
        }
        await loadMe()
        advanceSessionGeneration()
    }

    func signIn() async {
        guard !isSigningIn else { return }
        isSigningIn = true
        signInError = nil
        defer { isSigningIn = false }
        do {
            try await authService.signIn()
        } catch let error as AuthError {
            signInError = error
            return
        } catch {
            signInError = .server(code: "unavailable", message: error.localizedDescription)
            return
        }
        await loadMe()
        advanceSessionGeneration()
    }

    func signOut() async {
        cancelPrefetch()
        await authService.signOut()
        endSession()
    }

    /// The signed-in user's draft with this id, or `nil` if it is missing or someone else's.
    func ownedDraft(id: UUID) -> LocalDraft? {
        draftOwnerID.flatMap { drafts.draft(id: id, owner: $0) }
    }

    /// Sends a stored draft's transcript for extraction. The draft's id makes a resend idempotent.
    func send(_ draft: LocalDraft) async -> SendOutcome {
        do {
            let response = try await draftService.create(draft)
            draft.apply(response)
            draft.errorMessage = nil
            try? drafts.save()
            sendingWaitsForSignal = false
            return .sent
        } catch {
            let failure = APIFailure(error)
            // Backing out of the screen cancels the send; that is not a failure to record.
            if Task.isCancelled { return .failed(failure.userMessage) }
            if failure == .offline {
                sendingWaitsForSignal = true
                return .waiting
            }
            draft.errorMessage = failure.userMessage
            try? drafts.save()
            return .failed(failure.userMessage)
        }
    }

    /// The app came to the foreground: retry the session if it is offline (a successful `/me`
    /// starts the sync), otherwise run a sync pass.
    func becameActive() async {
        if phase == .offline {
            await bootstrap()
        } else {
            await draftSync?.runOnce()
            await recordSync?.runOnce()
        }
    }

    /// The app went to the background: ask iOS for a background run when a draft is waiting or queued.
    func enteredBackground() {
        guard let owner = draftOwnerID else {
            background.cancelAll()
            return
        }
        background.schedule(
            waiting: drafts.drafts(withStatus: .waiting, owner: owner).count,
            pendingSubmit: drafts.draftsPendingSubmit(owner: owner).count
        )
    }

    /// One background task's worth of work: a single draft pass. When iOS relaunches the app for a
    /// background task the scene never connects, so `RootView` has not bootstrapped and there is no
    /// `draftSync` yet: bootstrap first (a stored session loads `/me`, which starts the sync). A
    /// bootstrap already running, from the foreground, is left to finish. If there is still no sync
    /// (signed out, or offline), there is nothing to run.
    func runBackgroundPass() async {
        if draftSync == nil, phase == .loading, !isBootstrapping {
            await bootstrap()
        }
        await draftSync?.runOnce()
    }

    /// Syncs the selected project's records now. `force` has the backend pull from Procore first;
    /// `full` makes that pull a full sweep. Does nothing while no project sync is running.
    func syncRecords(force: Bool, full: Bool) async {
        await recordSync?.runOnce(force: force, full: full)
    }

    // MARK: - Private

    private func loadMe() async {
        let epoch = sessionEpoch
        let output: Operations.getMe.Output
        do {
            output = try await client.getMe()
        } catch {
            // The session ended while `/me` was in flight: whatever it says belongs to nobody now.
            guard epoch == sessionEpoch else { return }
            loadMeFailed(error)
            return
        }
        guard epoch == sessionEpoch else { return }
        apply(output)
    }

    private func loadMeFailed(_ error: Error) {
        if Self.isRefreshRejection(error) {
            // The middleware already cleared the store and reported the sign-out.
            endSession()
        } else {
            // Offline or transport failure: a field user must not be logged out for that.
            enterOffline()
        }
    }

    private func apply(_ output: Operations.getMe.Output) {
        switch output {
        case .ok(let ok):
            guard let user = try? ok.body.json else {
                enterOffline()
                return
            }
            if user.reauth_required {
                try? store.clear()
                endSession()
                return
            }
            if selectedCompanyID == nil, let id = user.last_company_id { selectedCompanyID = id }
            if selectedProjectID == nil, let id = user.last_project_id { selectedProjectID = id }
            draftOwnerID = user.procore_user_id
            phase = .signedIn(user)
            // Back online (by this retry or another path): the offline listener's job is done.
            offlineRetry?.cancel()
            offlineRetry = nil
            prefetchLookups()
            startDraftSync()
            startRecordSync()
        case .unauthorized:
            try? store.clear()
            endSession()
        case .default:
            enterOffline()
        }
    }

    private func advanceSessionGeneration() {
        if phase != .signedOut { sessionGeneration += 1 }
    }

    private func handleSignedOut() {
        endSession()
    }

    /// The selection belongs to the signed-in user, so it is dropped whenever the session ends.
    /// Setting the IDs to nil also removes their `UserDefaults` keys.
    /// The cached lookups and mirrored records belong to the signed-in user too, so they are deleted here as well.
    private func endSession() {
        sessionEpoch += 1
        cancelPrefetch()
        offlineRetry?.cancel()
        offlineRetry = nil
        draftSync?.stop()
        draftSync = nil
        // Review may be running a submit too; nothing runs on under the next user.
        submitRunner.cancelAll()
        recordSync?.stop()
        recordSync = nil
        lookups.clearAll()
        clearUserFiles()
        selectedCompanyID = nil
        selectedProjectID = nil
        // Drafts stay on the phone (a forced re-sign-in must not lose field work); they are hidden
        // until their owner signs in again.
        draftOwnerID = nil
        phase = .signedOut
    }

    /// Deletes the mirrored records and downloaded attachments. Failures are logged, not thrown:
    /// the session ends either way.
    private func clearUserFiles() {
        do {
            try records.clearAll()
        } catch {
            Self.log.error("Clearing the record mirror failed: \(error.localizedDescription, privacy: .public)")
        }
        do {
            try AttachmentFiles.removeAll()
        } catch {
            Self.log.error("Removing downloaded attachments failed: \(error.localizedDescription, privacy: .public)")
        }
    }

    /// Refreshes the selected project's lookup lists in the background, replacing any running
    /// prefetch. `force` skips the `ETag` revalidation (Settings' "Refresh people and locations").
    /// Runs as `prefetchTask`, so a project change or the end of the session cancels it.
    func refreshLookups(force: Bool) {
        cancelPrefetch()
        guard let companyID = selectedCompanyID, let projectID = selectedProjectID else { return }
        let lookups: LookupStore = lookups
        prefetchTask = Task { await lookups.prefetch(companyID: companyID, projectID: projectID, force: force) }
    }

    /// Clearing the project only cancels the running prefetch.
    private func prefetchLookups() {
        refreshLookups(force: false)
    }

    /// A stored session that cannot reach the server. Waits for the network path to come back and
    /// bootstraps again; once `/me` succeeds the sync starts and sends what is waiting, and the
    /// listener is cancelled. Signing out cancels it too.
    private func enterOffline() {
        phase = .offline
        // Home shows the stored mirror offline, so its header needs a driver for the last sync time
        // and the Sync button. It is not run here; `/me` succeeding replaces it with a running one.
        if recordSync == nil { startRecordSync(run: false) }
        guard offlineRetry == nil else { return }
        let updates = reachability.updates
        offlineRetry = Task { [weak self] in
            for await reachable in updates where reachable {
                guard let self, phase == .offline else { return }
                await bootstrap()
            }
        }
    }

    /// Starts a fresh sync for the signed-in user, replacing any running one.
    private func startDraftSync() {
        draftSync?.stop()
        let client: Client = client
        let sync = DraftSync(
            store: drafts,
            service: { DraftService(client: client) },
            runner: submitRunner,
            reachability: reachability,
            owner: { [weak self] in self?.draftOwnerID },
            send: { [weak self] draft in await self?.send(draft) ?? .waiting }
        )
        draftSync = sync
        sync.start()
    }

    /// Starts a fresh record sync for the selected project, replacing any running one, and runs it
    /// once unless `run` is false. Without a selected project there is no driver.
    private func startRecordSync(run: Bool = true) {
        recordSync?.stop()
        recordSync = nil
        guard let companyID = selectedCompanyID, let projectID = selectedProjectID else { return }
        let client: Client = client
        let sync = RecordSync(
            store: records,
            service: { RecordService(client: client) },
            companyID: companyID,
            projectID: projectID,
            kinds: [.rfi],
            reachability: reachability
        )
        recordSync = sync
        if run { Task { await sync.runOnce() } }
    }

    private func cancelPrefetch() {
        prefetchTask?.cancel()
        prefetchTask = nil
    }

    /// The generated client wraps middleware errors in `ClientError`.
    private static func isRefreshRejection(_ error: Error) -> Bool {
        if error is AuthRefreshError { return true }
        if let clientError = error as? ClientError { return clientError.underlyingError is AuthRefreshError }
        return false
    }

    private static func deviceID(in defaults: UserDefaults) -> String {
        if let existing = defaults.string(forKey: Keys.deviceID), !existing.isEmpty {
            return existing
        }
        let id = UUID().uuidString
        defaults.set(id, forKey: Keys.deviceID)
        return id
    }

    private static func storedID(forKey key: String, in defaults: UserDefaults) -> Int64? {
        (defaults.object(forKey: key) as? NSNumber)?.int64Value
    }

    private static func persist(_ id: Int64?, forKey key: String, in defaults: UserDefaults) {
        if let id {
            defaults.set(NSNumber(value: id), forKey: key)
        } else {
            defaults.removeObject(forKey: key)
        }
    }
}

#if DEBUG
extension AppState {
    /// A signed-out app state for SwiftUI previews: no server, preview-only defaults.
    static func preview(container: ModelContainer) -> AppState {
        AppState(
            store: KeychainSessionStore(),
            presenter: ASWebAuthenticationSessionPresenter(),
            baseURL: URL(string: "https://example.invalid/v1")!,
            modelContext: container.mainContext,
            defaults: UserDefaults(suiteName: "preview") ?? .standard
        )
    }
}
#endif
