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
