import AuthenticationServices
import CryptoKit
import FieldAssistantAPI
import Foundation
import os
import Security

/// Presents the Procore login page and returns the app callback URL it redirects to.
protocol WebAuthPresenter: Sendable {
    func authenticate(url: URL, callbackScheme: String) async throws -> URL
}

enum AuthError: Error, Equatable {
    /// The user closed the login sheet.
    case cancelled
    /// The callback URL carried neither a one-time `code` nor an `error`.
    case invalidCallback
    /// The API answered with an error (or an unusable response).
    case server(code: String, message: String)
}

/// Runs the PKCE sign-in against the Field Assistant API, refreshes tokens and signs out.
actor AuthService {
    static let callbackScheme = "fieldassistant"

    private let client: Client
    private let store: SessionStore
    private let presenter: WebAuthPresenter
    private let deviceID: String

    init(client: Client, store: SessionStore, presenter: WebAuthPresenter, deviceID: String) {
        self.client = client
        self.store = store
        self.presenter = presenter
        self.deviceID = deviceID
    }

    /// Signs in through the browser and stores the resulting session.
    /// The PKCE verifier exists only in this call's locals.
    func signIn() async throws {
        let verifier = try PKCE.makeVerifier()
        let challenge = PKCE.challenge(for: verifier)

        let start = try await client.authStart(body: .json(.init(device_id: deviceID, code_challenge: challenge)))
        let authorizeURL: URL
        switch start {
        case .ok(let ok):
            let raw = try ok.body.json.authorize_url
            guard let url = URL(string: raw) else {
                throw AuthError.server(code: "invalid_response", message: "The server returned an invalid sign-in URL.")
            }
            authorizeURL = url
        case .badRequest(let error):
            throw try Self.serverError(error)
        case .default(_, let error):
            throw try Self.serverError(error)
        }

        let callback: URL
        do {
            callback = try await presenter.authenticate(url: authorizeURL, callbackScheme: Self.callbackScheme)
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
            throw AuthError.cancelled
        }
        let items = URLComponents(url: callback, resolvingAgainstBaseURL: false)?.queryItems ?? []
        if let errorCode = items.first(where: { $0.name == "error" })?.value {
            throw Self.callbackError(errorCode)
        }
        guard let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty else {
            throw AuthError.invalidCallback
        }

        let exchange = try await client.authExchange(
            body: .json(.init(code: code, device_id: deviceID, code_verifier: verifier))
        )
        switch exchange {
        case .ok(let ok):
            try store.save(Self.session(from: try ok.body.json))
        case .badRequest(let error):
            throw try Self.serverError(error)
        case .default(_, let error):
            throw try Self.serverError(error)
        }
    }

    /// Exchanges `refreshToken` for a new session.
    /// Throws `AuthRefreshError.rejected` only when the server refuses the token (400/401); any other
    /// failure throws something else so the caller keeps the session.
    func refresh(_ refreshToken: String) async throws -> Session {
        let output = try await client.authRefresh(body: .json(.init(refresh_token: refreshToken)))
        switch output {
        case .ok(let ok):
            return Self.session(from: try ok.body.json)
        case .unauthorized:
            throw AuthRefreshError.rejected
        case .default(let status, _) where status == 400 || status == 401:
            throw AuthRefreshError.rejected
        case .default(_, let error):
            throw try Self.serverError(error)
        }
    }

    /// Revokes the session on the server (best effort) and forgets it locally.
    func signOut() async {
        _ = try? await client.authLogout()
        try? store.clear()
    }

    // MARK: - Helpers

    private static func session(from tokens: Components.Schemas.TokenPair) -> Session {
        Session(
            accessToken: tokens.access_token,
            refreshToken: tokens.refresh_token,
            accessExpiresAt: Date().addingTimeInterval(TimeInterval(tokens.expires_in))
        )
    }

    /// Maps the callback's `error` value to a user-facing failure. The API sends
    /// `access_denied`, `bad_request`, `procore_unavailable` or `internal`.
    static func callbackError(_ code: String) -> AuthError {
        let message: String
        switch code {
        case "access_denied":
            message = "Procore sign-in was declined."
        case "procore_unavailable":
            message = "Procore could not be reached. Try again."
        default:
            message = "Sign-in failed. Try again."
        }
        return .server(code: code, message: message)
    }

    private static func serverError(_ response: Components.Responses._Error) throws -> AuthError {
        let body = try response.body.json
        return .server(code: body.code, message: body.message)
    }
}

/// RFC 7636 proof key helpers (S256).
enum PKCE {
    /// 32 random bytes, base64url without padding (43 characters).
    static func makeVerifier() throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        guard status == errSecSuccess else {
            throw AuthError.server(code: "random_unavailable", message: "Could not generate a sign-in key.")
        }
        return base64URL(Data(bytes))
    }

    /// Base64url without padding of SHA-256 over the verifier's UTF-8 bytes.
    static func challenge(for verifier: String) -> String {
        base64URL(Data(SHA256.hash(data: Data(verifier.utf8))))
    }

    private static func base64URL(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

/// Presents the login page in an `ASWebAuthenticationSession` that shares cookies with Safari.
struct ASWebAuthenticationSessionPresenter: WebAuthPresenter {
    @MainActor
    func authenticate(url: URL, callbackScheme: String) async throws -> URL {
        let anchorProvider = PresentationAnchorProvider()
        var session: ASWebAuthenticationSession?
        let callback = try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<URL, Error>) in
            let pending = OSAllocatedUnfairLock<CheckedContinuation<URL, Error>?>(initialState: continuation)
            let finish: @Sendable (Result<URL, Error>) -> Void = { result in
                pending.withLock { stored in
                    stored?.resume(with: result)
                    stored = nil
                }
            }
            let webSession = ASWebAuthenticationSession(url: url, callbackURLScheme: callbackScheme) { callbackURL, error in
                if let callbackURL {
                    finish(.success(callbackURL))
                } else {
                    finish(.failure(error ?? ASWebAuthenticationSessionError(.canceledLogin)))
                }
            }
            webSession.presentationContextProvider = anchorProvider
            webSession.prefersEphemeralWebBrowserSession = false
            session = webSession
            if !webSession.start() {
                finish(.failure(ASWebAuthenticationSessionError(.presentationContextInvalid)))
            }
        }
        // The session and its (weakly held) context provider must outlive the browser sheet.
        withExtendedLifetime((session, anchorProvider)) {}
        return callback
    }
}

private final class PresentationAnchorProvider: NSObject, ASWebAuthenticationPresentationContextProviding {
    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            let windows = UIApplication.shared.connectedScenes
                .compactMap { $0 as? UIWindowScene }
                .flatMap(\.windows)
            return windows.first(where: \.isKeyWindow) ?? windows.first ?? ASPresentationAnchor()
        }
    }
}
