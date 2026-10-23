import AuthenticationServices
import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class AuthServiceTests: XCTestCase {
    private let deviceID = "device-1"

    // MARK: - signIn

    func testSignInStoresSession() async throws {
        let store = InMemorySessionStore()
        let transport = StubTransport([APIStub.authStartOK(), APIStub.tokenPair(access: "acc1", refresh: "ref1", expiresIn: 600)])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        let service = makeService(transport: transport, store: store, presenter: presenter)
        let before = Date()

        try await service.signIn()

        let session = try XCTUnwrap(store.load())
        XCTAssertEqual(session.accessToken, "acc1")
        XCTAssertEqual(session.refreshToken, "ref1")
        XCTAssertEqual(session.accessExpiresAt.timeIntervalSince(before), 600, accuracy: 5)

        XCTAssertEqual(presenter.openedURLs, [URL(string: "https://login.procore.com/oauth/authorize?x=1")!])
        XCTAssertEqual(presenter.callbackSchemes, ["fieldassistant"])

        let requests = transport.requests
        XCTAssertEqual(requests.map(\.request.path), ["/auth/start", "/auth/exchange"])
        let start = try APIStub.jsonObject(requests[0].body)
        let challenge = try XCTUnwrap(start["code_challenge"] as? String)
        XCTAssertGreaterThanOrEqual(challenge.count, 43)
        XCTAssertEqual(start["device_id"] as? String, deviceID)

        let exchange = try APIStub.jsonObject(requests[1].body)
        XCTAssertEqual(exchange["code"] as? String, "abc")
        XCTAssertEqual(exchange["device_id"] as? String, deviceID)
        let verifier = try XCTUnwrap(exchange["code_verifier"] as? String)
        XCTAssertEqual(verifier.count, 43)
        XCTAssertNil(verifier.rangeOfCharacter(from: CharacterSet(charactersIn: "+/=")), "verifier must be base64url without padding")
        XCTAssertEqual(APIStub.challenge(for: verifier), challenge, "challenge must be base64url(SHA-256(verifier))")
    }

    func testSignInUsesFreshVerifierPerAttempt() async throws {
        let transport = StubTransport([
            APIStub.authStartOK(), APIStub.tokenPair(access: "a1", refresh: "r1"),
            APIStub.authStartOK(), APIStub.tokenPair(access: "a2", refresh: "r2"),
        ])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        let service = makeService(transport: transport, store: InMemorySessionStore(), presenter: presenter)

        try await service.signIn()
        try await service.signIn()

        let verifiers = try transport.requests
            .filter { $0.request.path == "/auth/exchange" }
            .map { try APIStub.jsonObject($0.body)["code_verifier"] as? String }
        XCTAssertEqual(verifiers.count, 2)
        XCTAssertNotEqual(verifiers[0], verifiers[1])
    }

    func testSignInWhenUserCancelsLeavesNoSession() async throws {
        let store = InMemorySessionStore()
        let transport = StubTransport([APIStub.authStartOK()])
        let presenter = FakeWebAuthPresenter(error: ASWebAuthenticationSessionError(.canceledLogin))
        let service = makeService(transport: transport, store: store, presenter: presenter)

        do {
            try await service.signIn()
            XCTFail("expected cancellation")
        } catch {
            XCTAssertEqual(error as? AuthError, .cancelled, "unexpected error: \(error)")
        }
        XCTAssertNil(try store.load())
        XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/start"], "no exchange after cancel")
    }

    func testSignInCallbackWithoutCodeIsInvalidCallback() async throws {
        let store = InMemorySessionStore()
        let transport = StubTransport([APIStub.authStartOK()])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth")!)
        let service = makeService(transport: transport, store: store, presenter: presenter)

        do {
            try await service.signIn()
            XCTFail("expected invalidCallback")
        } catch {
            XCTAssertEqual(error as? AuthError, .invalidCallback, "unexpected error: \(error)")
        }
        XCTAssertNil(try store.load())
        XCTAssertEqual(transport.requests.count, 1)
    }

    func testSignInCallbackErrorThrowsServerError() async throws {
        let cases: [(code: String, message: String)] = [
            ("access_denied", "Procore sign-in was declined."),
            ("procore_unavailable", "Procore could not be reached. Try again."),
            ("bad_request", "Sign-in failed. Try again."),
            ("internal", "Sign-in failed. Try again."),
        ]
        for (code, message) in cases {
            let store = InMemorySessionStore()
            let transport = StubTransport([APIStub.authStartOK()])
            let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?error=\(code)")!)
            let service = makeService(transport: transport, store: store, presenter: presenter)

            do {
                try await service.signIn()
                XCTFail("expected server error for \(code)")
            } catch {
                XCTAssertEqual(error as? AuthError, .server(code: code, message: message), "unexpected error: \(error)")
            }
            XCTAssertNil(try store.load())
            XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/start"], "no exchange after callback error \(code)")
        }
    }

    func testSignInExchangeErrorIsServerError() async throws {
        let store = InMemorySessionStore()
        let transport = StubTransport([APIStub.authStartOK(), APIStub.error(.badRequest, code: "invalid_code", message: "Code expired")])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        let service = makeService(transport: transport, store: store, presenter: presenter)

        do {
            try await service.signIn()
            XCTFail("expected server error")
        } catch {
            XCTAssertEqual(error as? AuthError, .server(code: "invalid_code", message: "Code expired"), "unexpected error: \(error)")
        }
        XCTAssertNil(try store.load())
    }

    // MARK: - refresh

    func testRefreshReturnsRotatedSession() async throws {
        let transport = StubTransport([APIStub.tokenPair(access: "acc2", refresh: "ref2", expiresIn: 900)])
        let service = makeService(transport: transport)

        let session = try await service.refresh("ref1")

        XCTAssertEqual(session.accessToken, "acc2")
        XCTAssertEqual(session.refreshToken, "ref2")
        XCTAssertEqual(session.accessExpiresAt.timeIntervalSinceNow, 900, accuracy: 5)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/refresh"])
        XCTAssertEqual(try APIStub.jsonObject(transport.requests[0].body)["refresh_token"] as? String, "ref1")
    }

    func testRefreshRejectedOn400And401() async throws {
        for status in [HTTPResponse.Status.unauthorized, .badRequest] {
            let service = makeService(transport: StubTransport([APIStub.error(status, code: "refresh_revoked")]))
            do {
                _ = try await service.refresh("ref1")
                XCTFail("expected rejection for \(status)")
            } catch {
                XCTAssertEqual(error as? AuthRefreshError, .rejected, "status \(status): unexpected error \(error)")
            }
        }
    }

    func testRefreshServerErrorIsNotRejection() async throws {
        let service = makeService(transport: StubTransport([APIStub.error(.serviceUnavailable, code: "unavailable", message: "Down")]))
        do {
            _ = try await service.refresh("ref1")
            XCTFail("expected an error")
        } catch {
            XCTAssertNil(error as? AuthRefreshError, "a 5xx must not sign the user out")
            XCTAssertEqual(error as? AuthError, .server(code: "unavailable", message: "Down"))
        }
    }

    func testRefreshTransportErrorIsNotRejection() async throws {
        let service = makeService(transport: StubTransport([]))
        do {
            _ = try await service.refresh("ref1")
            XCTFail("expected an error")
        } catch {
            XCTAssertNil(error as? AuthRefreshError, "a transport failure must not sign the user out")
        }
    }

    // MARK: - signOut

    func testSignOutCallsLogoutAndClearsStore() async throws {
        let store = InMemorySessionStore(Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: .distantFuture))
        let transport = StubTransport(statuses: [.noContent])
        let service = makeService(transport: transport, store: store)

        await service.signOut()

        XCTAssertNil(store.current)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/logout"])
        XCTAssertEqual(transport.requests.first?.request.headerFields[.authorization], "Bearer acc1")
    }

    func testSignOutClearsStoreEvenWhenLogoutFails() async throws {
        let store = InMemorySessionStore(Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: .distantFuture))
        let service = makeService(transport: StubTransport([]), store: store)

        await service.signOut()

        XCTAssertNil(store.current)
        XCTAssertEqual(store.clearCount, 1)
    }

    // MARK: - Helpers

    private func makeService(
        transport: StubTransport,
        store: InMemorySessionStore = InMemorySessionStore(),
        presenter: WebAuthPresenter = FakeWebAuthPresenter(error: CancellationError())
    ) -> AuthService {
        let middleware = AuthMiddleware(
            store: store,
            refresh: { _ in throw AuthRefreshError.rejected },
            onSignedOut: {}
        )
        let client = Client(serverURL: APIStub.baseURL, transport: transport, middlewares: [middleware])
        return AuthService(client: client, store: store, presenter: presenter, deviceID: deviceID)
    }
}
