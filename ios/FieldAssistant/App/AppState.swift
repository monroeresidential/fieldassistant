import FieldAssistantAPI
import Foundation
import Observation
import OpenAPIRuntime
import OpenAPIURLSession

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

    private(set) var phase: Phase = .loading
    private(set) var isSigningIn = false
    /// Why the last sign-in attempt failed, or `nil`. `.cancelled` means the user closed the sheet.
    private(set) var signInError: AuthError?

    var selectedCompanyID: Int64? {
        didSet { Self.persist(selectedCompanyID, forKey: Keys.selectedCompanyID, in: defaults) }
    }
    var selectedProjectID: Int64? {
        didSet { Self.persist(selectedProjectID, forKey: Keys.selectedProjectID, in: defaults) }
    }

    @ObservationIgnored private let store: SessionStore
    @ObservationIgnored private let defaults: UserDefaults
    /// The authenticated API client every feature service uses.
    @ObservationIgnored private(set) var client: Client!
    @ObservationIgnored private var authService: AuthService!

    init(
        store: SessionStore,
        presenter: WebAuthPresenter,
        baseURL: URL,
        transport: ClientTransport? = nil,
        defaults: UserDefaults = .standard
    ) {
        self.store = store
        self.defaults = defaults
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
            transport: transport ?? URLSessionTransport(),
            middlewares: [middleware]
        )
        self.client = client
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
    }

    func signOut() async {
        await authService.signOut()
        endSession()
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
        case .unauthorized:
            try? store.clear()
            endSession()
        case .default:
            phase = .offline
        }
    }

    private func handleSignedOut() {
        endSession()
    }

    /// The selection belongs to the signed-in user, so it is dropped whenever the session ends.
    /// Setting the IDs to nil also removes their `UserDefaults` keys.
    private func endSession() {
        selectedCompanyID = nil
        selectedProjectID = nil
        phase = .signedOut
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
