import Foundation
import HTTPTypes
import OpenAPIRuntime

/// Attaches the stored access token to every authenticated request and keeps the session fresh.
///
/// - Refreshes before sending when the access token expires within 30 seconds.
/// - On a 401, refreshes once and retries the request once.
/// - Concurrent callers share a single in-flight refresh.
/// - If the refresh is rejected (`AuthRefreshError.rejected`), clears the store, calls `onSignedOut` once per
///   sign-out and rethrows. Any other refresh error is rethrown and the session is kept.
/// - If the retried request is still a 401 whose body code is `reauth_required` (Procore signed the user
///   out), clears the store, calls `onReauthRequired` once per sign-out and returns the response.
/// - A 401 that arrives after the session was cleared is returned as is, never refreshed.
actor AuthMiddleware: ClientMiddleware {
    /// Operations that are public (`security: []` in the contract) and must not carry a bearer.
    static let publicOperationIDs: Set<String> = ["authStart", "authExchange", "authRefresh"]
    static let refreshLeeway: TimeInterval = 30
    /// Largest single-shot request body buffered so it can be replayed after a 401.
    static let maxReplayableBodyBytes = 1024 * 1024
    /// Largest retried 401 body read for its error code; a larger one is returned unread.
    static let maxErrorBodyBytes = 64 * 1024

    private let store: SessionStore
    private let refresh: @Sendable (String) async throws -> Session
    private let onSignedOut: @Sendable () async -> Void
    private let onReauthRequired: @Sendable () async -> Void
    private let now: @Sendable () -> Date

    private var inFlightRefresh: Task<Session, Error>?
    private var hasSignaledSignOut = false

    init(
        store: SessionStore,
        refresh: @Sendable @escaping (String) async throws -> Session,
        onSignedOut: @Sendable @escaping () async -> Void,
        onReauthRequired: @Sendable @escaping () async -> Void,
        now: @Sendable @escaping () -> Date = Date.init
    ) {
        self.store = store
        self.refresh = refresh
        self.onSignedOut = onSignedOut
        self.onReauthRequired = onReauthRequired
        self.now = now
    }

    func intercept(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String,
        next: @Sendable (HTTPRequest, HTTPBody?, URL) async throws -> (HTTPResponse, HTTPBody?)
    ) async throws -> (HTTPResponse, HTTPBody?) {
        guard !Self.publicOperationIDs.contains(operationID) else {
            return try await next(request, body, baseURL)
        }
        guard var session = try currentSession() else {
            // Not signed in: let the server answer; there is nothing to refresh.
            return try await next(request, body, baseURL)
        }
        if session.accessExpiresAt.timeIntervalSince(now()) <= Self.refreshLeeway {
            session = try await refreshedSession(replacing: session)
        }

        let replay = try await ReplayableBody(body, limit: Self.maxReplayableBodyBytes)
        let (response, responseBody) = try await next(authorized(request, with: session), replay.firstAttempt, baseURL)
        guard response.status == .unauthorized, replay.canRetry else {
            return (response, responseBody)
        }

        guard let retrySession = try await sessionForRetry(replacing: session) else {
            return (response, responseBody)
        }
        let (retryResponse, retryBody) = try await next(authorized(request, with: retrySession), replay.retryAttempt, baseURL)
        guard retryResponse.status == .unauthorized else {
            return (retryResponse, retryBody)
        }
        let (code, readBody) = try await Self.errorCode(of: retryBody)
        if code == "reauth_required" {
            await signalReauthRequired()
        }
        return (retryResponse, readBody)
    }

    // MARK: - Session

    private func currentSession() throws -> Session? {
        let session = try store.load()
        if session != nil { hasSignaledSignOut = false }
        return session
    }

    /// Session to retry a 401 with, or `nil` when the user was signed out while the request was in flight.
    /// The old refresh token must not be spent then: it would revive a session after `onSignedOut` fired.
    private func sessionForRetry(replacing stale: Session) async throws -> Session? {
        if let inFlightRefresh {
            return try await inFlightRefresh.value
        }
        guard try store.load() != nil else { return nil }
        return try await refreshedSession(replacing: stale)
    }

    /// Returns a session newer than `stale`, joining an in-flight refresh or starting one.
    private func refreshedSession(replacing stale: Session) async throws -> Session {
        if let inFlightRefresh {
            return try await inFlightRefresh.value
        }
        // Another request may already have refreshed since `stale` was read; use that rather than
        // spending the (possibly rotated) refresh token again.
        if let current = try currentSession(), current.accessToken != stale.accessToken,
           current.accessExpiresAt.timeIntervalSince(now()) > Self.refreshLeeway {
            return current
        }

        let refresh = self.refresh
        let refreshToken = stale.refreshToken
        let task = Task { try await refresh(refreshToken) }
        inFlightRefresh = task
        let result = await task.result
        inFlightRefresh = nil

        switch result {
        case .success(let session):
            try store.save(session)
            hasSignaledSignOut = false
            return session
        case .failure(let error as AuthRefreshError):
            try? store.clear()
            if !hasSignaledSignOut {
                hasSignaledSignOut = true
                await onSignedOut()
            }
            throw error
        case .failure(let error):
            // Transient failure (network, server error): keep the session so a later request can retry.
            throw error
        }
    }

    /// A fresh session was refused too: Procore no longer accepts the user's tokens. Ends the session
    /// as a rejected refresh does, so later requests go out unauthenticated and do not signal again.
    private func signalReauthRequired() async {
        try? store.clear()
        guard !hasSignaledSignOut else { return }
        hasSignaledSignOut = true
        await onReauthRequired()
    }

    /// Reads an error body's `code` and returns it with a body the caller can still read. A body that
    /// is missing, larger than `maxErrorBodyBytes` or not JSON has no code.
    private static func errorCode(of body: HTTPBody?) async throws -> (String?, HTTPBody?) {
        guard let body else { return (nil, nil) }
        if case .known(let length) = body.length, length > maxErrorBodyBytes { return (nil, body) }
        let bytes = try await [UInt8](collecting: body, upTo: maxErrorBodyBytes)
        let code = (try? JSONDecoder().decode(ErrorCode.self, from: Data(bytes)))?.code
        return (code, HTTPBody(bytes))
    }

    private struct ErrorCode: Decodable {
        let code: String
    }

    private func authorized(_ request: HTTPRequest, with session: Session) -> HTTPRequest {
        var request = request
        request.headerFields[.authorization] = "Bearer \(session.accessToken)"
        return request
    }
}

/// A request body prepared so it can be sent twice when possible.
private struct ReplayableBody {
    /// Body for the first attempt.
    let firstAttempt: HTTPBody?
    /// Body for the retry; only meaningful when `canRetry` is true.
    let retryAttempt: HTTPBody?
    /// False when the original body is single-shot and too large (or of unknown length) to buffer,
    /// in which case a 401 is returned to the caller instead of being retried.
    let canRetry: Bool

    init(_ body: HTTPBody?, limit: Int) async throws {
        guard let body else {
            (firstAttempt, retryAttempt, canRetry) = (nil, nil, true)
            return
        }
        if body.iterationBehavior == .multiple {
            (firstAttempt, retryAttempt, canRetry) = (body, body, true)
            return
        }
        if case .known(let length) = body.length, length <= limit {
            let bytes = try await [UInt8](collecting: body, upTo: limit)
            (firstAttempt, retryAttempt, canRetry) = (HTTPBody(bytes), HTTPBody(bytes), true)
            return
        }
        (firstAttempt, retryAttempt, canRetry) = (body, nil, false)
    }
}
