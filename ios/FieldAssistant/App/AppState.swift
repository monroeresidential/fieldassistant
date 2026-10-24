import FieldAssistantAPI
import Foundation
import Observation
import OpenAPIRuntime
import OpenAPIURLSession
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

    private(set) var phase: Phase = .loading
    /// True after a send found no connection, until a later send gets through. Home shows the
    /// "No signal" banner while it is set and a draft is waiting.
    private(set) var sendingWaitsForSignal = false
    private(set) var isSigningIn = false
    /// Why the last sign-in attempt failed, or `nil`. `.cancelled` means the user closed the sheet.
    private(set) var signInError: AuthError?
    /// Advances each time a session is (re)established, after `/me` has loaded. Screens key their
    /// load tasks on it so they reload after "Sign in again" or a successful offline retry.
    private(set) var sessionGeneration = 0

    var selectedCompanyID: Int64? {
        didSet { Self.persist(selectedCompanyID, forKey: Keys.selectedCompanyID, in: defaults) }
    }
    var selectedProjectID: Int64? {
        didSet {
            Self.persist(selectedProjectID, forKey: Keys.selectedProjectID, in: defaults)
            // While `loadMe` fills the selection the phase is not signed in yet; it prefetches itself.
            if selectedProjectID != oldValue, case .signedIn = phase { prefetchLookups() }
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
    @ObservationIgnored let settings: Settings

    var draftService: DraftService { DraftService(client: client) }

    init(
        store: SessionStore,
        presenter: WebAuthPresenter,
        baseURL: URL,
        modelContext: ModelContext,
        transport: ClientTransport? = nil,
        defaults: UserDefaults = .standard
    ) {
        self.store = store
        self.defaults = defaults
        drafts = DraftStore(context: modelContext)
        settings = Settings(defaults: defaults)
        selectedCompanyID = Self.storedID(forKey: Keys.selectedCompanyID, in: defaults)
        selectedProjectID = Self.storedID(forKey: Keys.selectedProjectID, in: defaults)

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
        authService = AuthService(
            client: client,
            store: store,
            presenter: presenter,
            deviceID: Self.deviceID(in: defaults)
        )
    }

    /// Decides the first screen: signed out without a stored session, otherwise loads the user.
    func bootstrap() async {
        let session: Session?
        do {
            session = try store.load()
        } catch {
            session = nil
        }
        guard session != nil else {
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
            if failure == .offline {
                sendingWaitsForSignal = true
                return .waiting
            }
            draft.errorMessage = failure.userMessage
            try? drafts.save()
            return .failed(failure.userMessage)
        }
    }

    /// Sends every waiting draft of a project, oldest first, stopping at the first one that finds no connection.
    func sendWaiting(projectID: Int64) async {
        for draft in drafts.drafts(withStatus: .waiting) where draft.projectID == projectID {
            if await send(draft) == .waiting { return }
        }
    }

    // MARK: - Private

    private func loadMe() async {
        let output: Operations.getMe.Output
        do {
            output = try await client.getMe()
        } catch where Self.isRefreshRejection(error) {
            // The middleware already cleared the store and reported the sign-out.
            endSession()
            return
        } catch {
            // Offline or transport failure: a field user must not be logged out for that.
            phase = .offline
            return
        }

        switch output {
        case .ok(let ok):
            guard let user = try? ok.body.json else {
                phase = .offline
                return
            }
            if user.reauth_required {
                try? store.clear()
                endSession()
                return
            }
            if selectedCompanyID == nil, let id = user.last_company_id { selectedCompanyID = id }
            if selectedProjectID == nil, let id = user.last_project_id { selectedProjectID = id }
            phase = .signedIn(user)
            prefetchLookups()
        case .unauthorized:
            try? store.clear()
            endSession()
        case .default:
            phase = .offline
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
    /// The cached lookups belong to the signed-in user too, so they are deleted here as well.
    private func endSession() {
        cancelPrefetch()
        lookups.clearAll()
        selectedCompanyID = nil
        selectedProjectID = nil
        phase = .signedOut
    }

    /// Refreshes the selected project's lookup lists in the background, replacing any running prefetch.
    /// Clearing the project only cancels the running one.
    private func prefetchLookups() {
        cancelPrefetch()
        guard let companyID = selectedCompanyID, let projectID = selectedProjectID else { return }
        let lookups: LookupStore = lookups
        prefetchTask = Task { await lookups.prefetch(companyID: companyID, projectID: projectID) }
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
