import Foundation

/// The signed-in user's API tokens.
struct Session: Codable, Sendable {
    var accessToken: String
    var refreshToken: String
    var accessExpiresAt: Date
}

/// Persists the current `Session`. Implementations must be safe to call from any thread.
protocol SessionStore: Sendable {
    func load() throws -> Session?
    func save(_ s: Session) throws
    func clear() throws
}

/// Thrown by the refresh closure when the server definitively rejects the refresh token.
/// Only this error signs the user out; any other refresh failure (e.g. a network error) keeps the session.
public enum AuthRefreshError: Error, Sendable {
    case rejected
}
