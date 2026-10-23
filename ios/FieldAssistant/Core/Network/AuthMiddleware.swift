import Foundation
import HTTPTypes
import OpenAPIRuntime

/// Attaches the stored access token to every authenticated request and keeps the session fresh.
///
/// - Refreshes before sending when the access token expires within 30 seconds.
/// - On a 401, refreshes once and retries the request once.
/// - Concurrent callers share a single in-flight refresh.
/// - If a refresh fails, clears the store, calls `onSignedOut` once per sign-out and rethrows.
actor AuthMiddleware: ClientMiddleware {
    /// Operations that are public (`security: []` in the contract) and must not carry a bearer.
    static let publicOperationIDs: Set<String> = ["authStart", "authExchange", "authRefresh"]
    static let refreshLeeway: TimeInterval = 30
    /// Largest single-shot request body buffered so it can be replayed after a 401.
    static let maxReplayableBodyBytes = 1024 * 1024

    private let store: SessionStore
    private let refresh: @Sendable (String) async throws -> Session
    private let onSignedOut: @Sendable () async -> Void
    private let now: @Sendable () -> Date

    private var inFlightRefresh: Task<Session, Error>?
    private var hasSignaledSignOut = false

    init(
        store: SessionStore,
        refresh: @Sendable @escaping (String) async throws -> Session,
        onSignedOut: @Sendable @escaping () async -> Void,
        now: @Sendable @escaping () -> Date = Date.init
    ) {
        self.store = store
        self.refresh = refresh
        self.onSignedOut = onSignedOut
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

        session = try await refreshedSession(replacing: session)
        return try await next(authorized(request, with: session), replay.retryAttempt, baseURL)
    }

    // MARK: - Session

    private func currentSession() throws -> Session? {
        let session = try store.load()
        if session != nil { hasSignaledSignOut = false }
        return session
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
        case .failure(let error):
            try? store.clear()
            if !hasSignaledSignOut {
                hasSignaledSignOut = true
                await onSignedOut()
            }
            throw error
        }
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
