import Foundation
@testable import FieldAssistant

/// Thread-safe in-memory `SessionStore` for tests; records how often it was cleared.
final class InMemorySessionStore: SessionStore, @unchecked Sendable {
    private let lock = NSLock()
    private var session: Session?
    private var clears = 0

    init(_ session: Session? = nil) {
        self.session = session
    }

    var clearCount: Int { lock.withLock { clears } }
    var current: Session? { lock.withLock { session } }

    func load() throws -> Session? { lock.withLock { session } }

    func save(_ s: Session) throws { lock.withLock { session = s } }

    func clear() throws {
        lock.withLock {
            session = nil
            clears += 1
        }
    }
}
