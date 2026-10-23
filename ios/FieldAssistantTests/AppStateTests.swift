import AuthenticationServices
import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class AppStateTests: XCTestCase {
    private let suiteName = "AppStateTests.\(UUID().uuidString)"

    override func tearDown() {
        UserDefaults().removePersistentDomain(forName: suiteName)
        super.tearDown()
    }

    @MainActor
    func testBootstrapWithSessionLoadsMe() async throws {
        let store = InMemorySessionStore(validSession())
        let transport = StubTransport([APIStub.me(procoreUserID: 42)])
        let state = makeState(store: store, transport: transport)

        await state.bootstrap()

        guard case .signedIn(let user) = state.phase else {
            return XCTFail("expected signedIn, got \(state.phase)")
        }
        XCTAssertEqual(user.procore_user_id, 42)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/me"])
        XCTAssertEqual(transport.requests.first?.request.headerFields[.authorization], "Bearer acc1")
    }

    @MainActor
    func testBootstrapWithoutSessionIsSignedOut() async throws {
        let transport = StubTransport([])
        let state = makeState(store: InMemorySessionStore(), transport: transport)
        XCTAssertEqual(state.phase, .loading)

        await state.bootstrap()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertTrue(transport.requests.isEmpty, "no network call without a session")
    }

    @MainActor
    func testBootstrapFillsSelectionFromLastIDsWhenUnset() async throws {
        let state = makeState(
            store: InMemorySessionStore(validSession()),
            transport: StubTransport([APIStub.me(lastCompanyID: 3, lastProjectID: 30)])
        )

        await state.bootstrap()

        XCTAssertEqual(state.selectedCompanyID, 3)
        XCTAssertEqual(state.selectedProjectID, 30)
    }

    @MainActor
    func testBootstrapKeepsLocalSelectionOverLastIDs() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99

        let state = makeState(
            store: InMemorySessionStore(validSession()),
            transport: StubTransport([APIStub.me(lastCompanyID: 3, lastProjectID: 30)])
        )
        await state.bootstrap()

        XCTAssertEqual(state.selectedCompanyID, 7)
        XCTAssertEqual(state.selectedProjectID, 99)
    }

    @MainActor
    func testSelectionPersistsAcrossInstances() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99

        let second = makeState(store: InMemorySessionStore(), transport: StubTransport([]))

        XCTAssertEqual(second.selectedCompanyID, 7)
        XCTAssertEqual(second.selectedProjectID, 99)
    }

    @MainActor
    func testReauthRequiredShowsSignedOutOnce() async throws {
        let store = InMemorySessionStore(validSession())
        let transport = StubTransport([APIStub.me(reauthRequired: true)])
        let state = makeState(store: store, transport: transport)

        await state.bootstrap()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertNil(store.current)
        XCTAssertEqual(store.clearCount, 1)

        // A second bootstrap finds no session and makes no further calls.
        await state.bootstrap()
        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertEqual(transport.requests.count, 1)
        XCTAssertEqual(store.clearCount, 1)
    }

    @MainActor
    func testBootstrapOfflineKeepsSession() async throws {
        let store = InMemorySessionStore(validSession())
        let state = makeState(store: store, transport: StubTransport([]))

        await state.bootstrap()

        XCTAssertEqual(state.phase, .offline)
        XCTAssertEqual(store.current?.accessToken, "acc1")
        XCTAssertEqual(store.clearCount, 0)
    }

    @MainActor
    func testDeviceIDIsGeneratedOnceAndReused() async throws {
        let transport1 = StubTransport([APIStub.error(.badRequest)])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        await makeState(store: InMemorySessionStore(), transport: transport1, presenter: presenter).signIn()
        let transport2 = StubTransport([APIStub.error(.badRequest)])
        await makeState(store: InMemorySessionStore(), transport: transport2, presenter: presenter).signIn()

        let id1 = try XCTUnwrap(APIStub.jsonObject(transport1.requests.first?.body)["device_id"] as? String)
        let id2 = try XCTUnwrap(APIStub.jsonObject(transport2.requests.first?.body)["device_id"] as? String)
        XCTAssertNotNil(UUID(uuidString: id1))
        XCTAssertEqual(id1, id2)
        XCTAssertEqual(UserDefaults(suiteName: suiteName)?.string(forKey: "deviceID"), id1)
    }

    @MainActor
    func testSignInLoadsMeAndShowsSignedIn() async throws {
        let store = InMemorySessionStore()
        let transport = StubTransport([
            APIStub.authStartOK(),
            APIStub.tokenPair(access: "acc1", refresh: "ref1"),
            APIStub.me(procoreUserID: 42),
        ])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        let state = makeState(store: store, transport: transport, presenter: presenter)

        await state.signIn()

        guard case .signedIn(let user) = state.phase else {
            return XCTFail("expected signedIn, got \(state.phase)")
        }
        XCTAssertEqual(user.procore_user_id, 42)
        XCTAssertNil(state.signInError)
        XCTAssertFalse(state.isSigningIn)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/start", "/auth/exchange", "/me"])
        XCTAssertEqual(transport.requests.last?.request.headerFields[.authorization], "Bearer acc1")
    }

    @MainActor
    func testSignInCancelledStaysSignedOutWithoutError() async throws {
        let state = makeState(
            store: InMemorySessionStore(),
            transport: StubTransport([APIStub.authStartOK()]),
            presenter: FakeWebAuthPresenter(error: ASWebAuthenticationSessionError(.canceledLogin))
        )
        await state.bootstrap()

        await state.signIn()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertEqual(state.signInError, .cancelled)
        XCTAssertFalse(state.isSigningIn)
    }

    @MainActor
    func testSignOutCallsLogoutAndShowsSignedOut() async throws {
        let store = InMemorySessionStore(validSession())
        let transport = StubTransport([APIStub.me()])
        transport.enqueue(.noContent)
        let state = makeState(store: store, transport: transport)
        await state.bootstrap()

        await state.signOut()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertNil(store.current)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/me", "/auth/logout"])
    }

    @MainActor
    func testRejectedRefreshSignsOut() async throws {
        let store = InMemorySessionStore(
            Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: Date().addingTimeInterval(5))
        )
        // The access token is about to expire, so the middleware refreshes before /me; the refresh is revoked.
        let transport = StubTransport([APIStub.error(.unauthorized, code: "refresh_revoked")])
        let state = makeState(store: store, transport: transport)

        await state.bootstrap()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertNil(store.current)
        XCTAssertEqual(transport.requests.map(\.request.path), ["/auth/refresh"])
    }

    // MARK: - Helpers

    private func validSession() -> Session {
        Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: Date().addingTimeInterval(3600))
    }

    @MainActor
    private func makeState(
        store: InMemorySessionStore,
        transport: StubTransport,
        presenter: WebAuthPresenter = FakeWebAuthPresenter(error: CancellationError())
    ) -> AppState {
        AppState(
            store: store,
            presenter: presenter,
            baseURL: APIStub.baseURL,
            transport: transport,
            defaults: UserDefaults(suiteName: suiteName)!
        )
    }
}
