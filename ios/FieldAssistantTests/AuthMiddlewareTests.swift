import XCTest
import HTTPTypes
import OpenAPIRuntime
@testable import FieldAssistant

final class AuthMiddlewareTests: XCTestCase {
    private static let fixedNow = Date(timeIntervalSince1970: 1_800_000_000)
    private static let baseURL = URL(string: "https://api.example.com/v1")!

    // MARK: - Tests

    func testAddsBearerHeader() async throws {
        let store = InMemorySessionStore(session(access: "a1", expiresIn: 3600))
        let refreshes = Counter()
        let signOuts = Counter()
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts)
        let transport = StubTransport(statuses: [.ok])

        let (response, _) = try await Self.send(through: middleware, transport: transport)

        XCTAssertEqual(response.status, .ok)
        XCTAssertEqual(transport.requests.count, 1)
        XCTAssertEqual(transport.requests.first?.request.headerFields[.authorization], "Bearer a1")
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 0)
    }

    func testRefreshesBeforeExpiry() async throws {
        let store = InMemorySessionStore(session(access: "old", refresh: "r-old", expiresIn: 10))
        let refreshes = Counter()
        let signOuts = Counter()
        let seenRefreshTokens = Recorder<String>()
        let newSession = session(access: "new", refresh: "r-new", expiresIn: 3600)
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts) { token in
            await seenRefreshTokens.append(token)
            return newSession
        }
        let transport = StubTransport(statuses: [.ok])

        let (response, _) = try await Self.send(through: middleware, transport: transport)

        XCTAssertEqual(response.status, .ok)
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 1)
        let tokens = await seenRefreshTokens.values
        XCTAssertEqual(tokens, ["r-old"])
        XCTAssertEqual(transport.requests.count, 1)
        XCTAssertEqual(transport.requests.first?.request.headerFields[.authorization], "Bearer new")
        XCTAssertEqual(store.current?.accessToken, "new")
        XCTAssertEqual(store.current?.refreshToken, "r-new")
    }

    func testRetriesOnceAfter401() async throws {
        let store = InMemorySessionStore(session(access: "stale", refresh: "r1", expiresIn: 3600))
        let refreshes = Counter()
        let signOuts = Counter()
        let fresh = session(access: "fresh", refresh: "r2", expiresIn: 3600)
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts) { _ in fresh }
        let transport = StubTransport(statuses: [.unauthorized, .ok])
        let payload: [UInt8] = Array(#"{"companyId":1}"#.utf8)

        let (response, _) = try await Self.send(
            through: middleware,
            transport: transport,
            method: .put,
            body: HTTPBody(payload, length: .known(Int64(payload.count)), iterationBehavior: .single)
        )

        XCTAssertEqual(response.status, .ok)
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 1)
        let requests = transport.requests
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests.map { $0.request.headerFields[.authorization] }, ["Bearer stale", "Bearer fresh"])
        XCTAssertEqual(requests.map(\.body), [payload, payload], "single-shot body must be replayed on retry")
        XCTAssertEqual(store.current?.accessToken, "fresh")
        let signOutCount = await signOuts.value
        XCTAssertEqual(signOutCount, 0)
    }

    func testRefreshFailureClearsSessionOnce() async throws {
        struct RefreshRejected: Error {}
        let store = InMemorySessionStore(session(access: "a1", refresh: "r1", expiresIn: 3600))
        let refreshes = Counter()
        let signOuts = Counter()
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts) { _ in
            throw RefreshRejected()
        }
        let transport = StubTransport(statuses: [.unauthorized, .unauthorized])

        do {
            _ = try await Self.send(through: middleware, transport: transport)
            XCTFail("expected the refresh error to propagate")
        } catch {
            XCTAssertTrue(error is RefreshRejected, "unexpected error: \(error)")
        }
        XCTAssertNil(store.current)
        XCTAssertEqual(store.clearCount, 1)
        var signOutCount = await signOuts.value
        XCTAssertEqual(signOutCount, 1)

        // No session any more: the request goes out without a bearer and is not refreshed or signed out again.
        let (response, _) = try await Self.send(through: middleware, transport: transport)

        XCTAssertEqual(response.status, .unauthorized)
        let requests = transport.requests
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests[0].request.headerFields[.authorization], "Bearer a1")
        XCTAssertNil(requests[1].request.headerFields[.authorization])
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 1)
        signOutCount = await signOuts.value
        XCTAssertEqual(signOutCount, 1)
    }

    func testConcurrentRequestsShareOneRefresh() async throws {
        let store = InMemorySessionStore(session(access: "old", refresh: "r-old", expiresIn: 5))
        let refreshes = Counter()
        let signOuts = Counter()
        let newSession = session(access: "new", refresh: "r-new", expiresIn: 3600)
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts) { _ in
            try await Task.sleep(for: .milliseconds(100))
            return newSession
        }
        let transport = StubTransport(statuses: Array(repeating: .ok, count: 10))

        let statuses = await withTaskGroup(of: HTTPResponse.Status?.self) { group in
            for _ in 0..<10 {
                group.addTask {
                    try? await Self.send(through: middleware, transport: transport).0.status
                }
            }
            var collected: [HTTPResponse.Status?] = []
            for await status in group { collected.append(status) }
            return collected
        }

        XCTAssertEqual(statuses, Array(repeating: .ok, count: 10))
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 1)
        let requests = transport.requests
        XCTAssertEqual(requests.count, 10)
        XCTAssertTrue(requests.allSatisfy { $0.request.headerFields[.authorization] == "Bearer new" })
    }

    func testRetryUsesNewerStoredSessionWithoutRefresh() async throws {
        let store = InMemorySessionStore(session(access: "stale", refresh: "r1", expiresIn: 3600))
        let refreshes = Counter()
        let signOuts = Counter()
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts)
        let transport = StubTransport(statuses: [.unauthorized, .ok])
        let newer = session(access: "newer", refresh: "r2", expiresIn: 3600)
        let request = HTTPRequest(method: .get, scheme: nil, authority: nil, path: "/me")

        // Another caller stores a fresh session while this request is in flight, before its 401 arrives.
        let (response, _) = try await middleware.intercept(
            request,
            body: nil,
            baseURL: Self.baseURL,
            operationID: "getMe"
        ) { request, body, baseURL in
            if transport.requests.isEmpty { try store.save(newer) }
            return try await transport.send(request, body: body, baseURL: baseURL, operationID: "getMe")
        }

        XCTAssertEqual(response.status, .ok)
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 0)
        XCTAssertEqual(
            transport.requests.map { $0.request.headerFields[.authorization] },
            ["Bearer stale", "Bearer newer"]
        )
        XCTAssertEqual(store.current?.accessToken, "newer")
        let signOutCount = await signOuts.value
        XCTAssertEqual(signOutCount, 0)
    }

    func testPublicAuthOperationsSkipBearerAndRefresh() async throws {
        let store = InMemorySessionStore(session(access: "a1", expiresIn: 5))
        let refreshes = Counter()
        let signOuts = Counter()
        let middleware = makeMiddleware(store: store, refreshes: refreshes, signOuts: signOuts)
        let transport = StubTransport(statuses: [.ok, .unauthorized, .ok])

        for operationID in ["authStart", "authRefresh", "authExchange"] {
            _ = try await Self.send(through: middleware, transport: transport, operationID: operationID)
        }

        let requests = transport.requests
        XCTAssertEqual(requests.count, 3, "a 401 on a public operation must not be retried")
        XCTAssertTrue(requests.allSatisfy { $0.request.headerFields[.authorization] == nil })
        let refreshCount = await refreshes.value
        XCTAssertEqual(refreshCount, 0)
    }

    // MARK: - Helpers

    private func session(access: String, refresh: String = "refresh", expiresIn seconds: TimeInterval) -> Session {
        Session(accessToken: access, refreshToken: refresh, accessExpiresAt: Self.fixedNow.addingTimeInterval(seconds))
    }

    private func makeMiddleware(
        store: InMemorySessionStore,
        refreshes: Counter,
        signOuts: Counter,
        refresh: @escaping @Sendable (String) async throws -> Session = { _ in
            XCTFail("refresh should not be called")
            throw CancellationError()
        }
    ) -> AuthMiddleware {
        let now = Self.fixedNow
        return AuthMiddleware(
            store: store,
            refresh: { token in
                await refreshes.increment()
                return try await refresh(token)
            },
            onSignedOut: { await signOuts.increment() },
            now: { now }
        )
    }

    private static func send(
        through middleware: AuthMiddleware,
        transport: StubTransport,
        method: HTTPRequest.Method = .get,
        body: HTTPBody? = nil,
        operationID: String = "getMe"
    ) async throws -> (HTTPResponse, HTTPBody?) {
        let request = HTTPRequest(method: method, scheme: nil, authority: nil, path: "/me")
                return try await middleware.intercept(
            request,
            body: body,
            baseURL: Self.baseURL,
            operationID: operationID
        ) { request, body, baseURL in
            try await transport.send(request, body: body, baseURL: baseURL, operationID: operationID)
        }
    }
}

private actor Counter {
    private(set) var value = 0
    func increment() { value += 1 }
}

private actor Recorder<Element: Sendable> {
    private(set) var values: [Element] = []
    func append(_ element: Element) { values.append(element) }
}
