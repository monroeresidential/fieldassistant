import AuthenticationServices
import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

final class AppStateTests: XCTestCase {
    private let suiteName = "AppStateTests.\(UUID().uuidString)"
    /// Kept alive for the test: `ModelContext` does not own its container.
    private var container: ModelContainer?

    override func tearDown() {
        UserDefaults().removePersistentDomain(forName: suiteName)
        container = nil
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
    func testSignOutClearsSelection() async throws {
        let transport = StubTransport([APIStub.me()])
        transport.enqueue(.noContent)
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        state.selectedCompanyID = 7
        state.selectedProjectID = 99

        await state.signOut()

        XCTAssertNil(state.selectedCompanyID)
        XCTAssertNil(state.selectedProjectID)
        let defaults = try XCTUnwrap(UserDefaults(suiteName: suiteName))
        XCTAssertNil(defaults.object(forKey: "selectedCompanyID"))
        XCTAssertNil(defaults.object(forKey: "selectedProjectID"))
        let next = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        XCTAssertNil(next.selectedCompanyID)
        XCTAssertNil(next.selectedProjectID)
    }

    @MainActor
    func testReauthRequiredClearsSelection() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99

        let state = makeState(
            store: InMemorySessionStore(validSession()),
            transport: StubTransport([APIStub.me(reauthRequired: true, lastCompanyID: 3, lastProjectID: 30)])
        )
        await state.bootstrap()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertNil(state.selectedCompanyID)
        XCTAssertNil(state.selectedProjectID)
        let next = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        XCTAssertNil(next.selectedCompanyID)
        XCTAssertNil(next.selectedProjectID)
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

    @MainActor
    func testBootstrapWithStoredSelectionPrefetchesLookups() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route("/projects/99/lookups/", notModified(count: LookupKind.allCases.count))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)

        await state.bootstrap()
        await state.prefetchTask?.value

        // Record sync runs alongside the prefetch; this test is about lookups.
        let requests = transport.requests.filter { !($0.path ?? "").contains("/records/") }
        let paths = requests.map { $0.path ?? "" }
        XCTAssertEqual(paths.first, "/me")
        XCTAssertEqual(Set(paths.dropFirst()), Set(LookupKind.allCases.map { "/projects/99/lookups/\($0.rawValue)" }))
        XCTAssertEqual(paths.count, 1 + LookupKind.allCases.count, "one prefetch, not one per selection change")
        XCTAssertTrue(requests.dropFirst().allSatisfy {
            $0.headerFields[HTTPField.Name("X-Company-Id")!] == "7"
        })
    }

    @MainActor
    func testSelectingProjectPrefetchesLookups() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me())
        transport.route("/projects/42/lookups/", notModified(count: LookupKind.allCases.count))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        XCTAssertNil(state.prefetchTask, "no selection, nothing to prefetch")

        state.selectedCompanyID = 7
        state.selectedProjectID = 42
        await state.prefetchTask?.value

        let lookupPaths = transport.paths.filter { $0.contains("/lookups/") }
        XCTAssertEqual(Set(lookupPaths), Set(LookupKind.allCases.map { "/projects/42/lookups/\($0.rawValue)" }))
        XCTAssertEqual(lookupPaths.count, LookupKind.allCases.count)
    }

    @MainActor
    func testReselectingProjectRevalidatesWithRawETag() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route(
            "/projects/99/lookups/",
            LookupKind.allCases.map { _ in APIStub.lookup(etag: #""u1""#) } + notModified(count: LookupKind.allCases.count)
        )
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        await state.prefetchTask?.value

        state.selectedProjectID = nil
        state.selectedProjectID = 99
        await state.prefetchTask?.value

        let revalidations = transport.requests.filter { ($0.path ?? "").contains("/lookups/") }.suffix(LookupKind.allCases.count)
        XCTAssertEqual(revalidations.map { $0.headerFields[.ifNoneMatch] }, Array(repeating: #""u1""#, count: 6))
        XCTAssertEqual(state.lookups.set(projectID: 99, kind: .users)?.etag, #""u1""#)
    }

    @MainActor
    func testSignOutClearsCachedLookups() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route(
            "/projects/99/lookups/",
            LookupKind.allCases.map { _ in APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#) }
        )
        transport.route("/auth/logout", (HTTPResponse(status: .noContent), nil))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        await state.prefetchTask?.value
        XCTAssertEqual(state.lookups.rows(projectID: 99, kind: .users).map(\.label), ["Ben West"])

        await state.signOut()

        for kind in LookupKind.allCases {
            XCTAssertNil(state.lookups.set(projectID: 99, kind: kind), "\(kind) is cleared")
        }
        let count = try XCTUnwrap(container).mainContext.fetchCount(FetchDescriptor<CachedLookup>())
        XCTAssertEqual(count, 0)
        XCTAssertEqual(transport.paths.filter { !$0.contains("/records/") }.last, "/auth/logout")
    }

    @MainActor
    func testSignOutCancelsRunningPrefetch() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route("/auth/logout", (HTTPResponse(status: .noContent), nil))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        let prefetch = try XCTUnwrap(state.prefetchTask)

        await state.signOut()

        XCTAssertTrue(prefetch.isCancelled)
        XCTAssertNil(state.prefetchTask)
    }

    @MainActor
    func testClearingProjectCancelsRunningPrefetch() async throws {
        let transport = StubTransport([APIStub.me(lastCompanyID: 7, lastProjectID: 99)])
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        let prefetch = try XCTUnwrap(state.prefetchTask)

        state.selectedProjectID = nil

        XCTAssertTrue(prefetch.isCancelled)
        XCTAssertNil(state.prefetchTask)
    }

    @MainActor
    func testSelectionWhileSignedOutDoesNotPrefetch() async throws {
        let transport = StubTransport([])
        let state = makeState(store: InMemorySessionStore(), transport: transport)
        await state.bootstrap()

        state.selectedCompanyID = 7
        state.selectedProjectID = 99

        XCTAssertNil(state.prefetchTask)
        XCTAssertTrue(transport.requests.isEmpty)
    }

    @MainActor
    func testLateMeAfterSignOutIsIgnored() async throws {
        let store = InMemorySessionStore(validSession())
        let inner = StubTransport(
            [(HTTPResponse(status: .noContent), nil), APIStub.me(procoreUserID: 42, lastCompanyID: 7, lastProjectID: 99)]
                + LookupKind.allCases.map { _ in APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#) }
        )
        let gate = GateTransport(inner) { $0.path == "/me" }
        let state = makeState(store: store, transport: gate)
        let bootstrap = Task { await state.bootstrap() }
        try await gate.waitUntilHeld()

        await state.signOut()
        gate.release()
        await bootstrap.value
        await state.prefetchTask?.value

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertNil(state.draftSync)
        XCTAssertNil(state.draftOwnerID)
        XCTAssertNil(state.selectedCompanyID)
        XCTAssertNil(state.selectedProjectID)
        XCTAssertNil(state.prefetchTask)
        XCTAssertEqual(try XCTUnwrap(container).mainContext.fetchCount(FetchDescriptor<CachedLookup>()), 0)
        XCTAssertEqual(inner.requests.map(\.request.path), ["/auth/logout", "/me"], "no lookups after sign-out")
    }

    @MainActor
    func testSettingsRefreshIsCancelledBySignOut() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route("/projects/99/lookups/", notModified(count: LookupKind.allCases.count))
        transport.route("/auth/logout", (HTTPResponse(status: .noContent), nil))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        await state.prefetchTask?.value

        state.refreshLookups(force: true)
        let refresh = try XCTUnwrap(state.prefetchTask)
        await state.signOut()

        XCTAssertTrue(refresh.isCancelled)
        XCTAssertNil(state.prefetchTask)
    }

    @MainActor
    func testSignedInWithProjectStartsRecordSync() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route("/projects/99/records/rfi", RecordStub.page(ids: [1, 2], nextSince: 2, hasMore: false))
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)

        await state.bootstrap()
        let sync = try XCTUnwrap(state.recordSync)
        await state.syncRecords(force: false, full: false)

        XCTAssertEqual(state.records.records(projectID: 99, kind: .rfi, query: "", filter: .all).count, 2)
        XCTAssertEqual(sync.status, .synced(try XCTUnwrap(state.records.state(projectID: 99, kind: .rfi)?.lastSyncedAt)))
        XCTAssertEqual(
            transport.paths.filter { $0.hasPrefix("/projects/99/records") }.count, 1,
            "the launch run and the call coalesce"
        )
    }

    @MainActor
    func testSyncRecordsWithoutDriverDoesNothing() async throws {
        let transport = RouteTransport()
        let state = makeState(store: InMemorySessionStore(), transport: transport)
        await state.bootstrap()

        await state.syncRecords(force: true, full: true)

        XCTAssertNil(state.recordSync)
        XCTAssertTrue(transport.paths.isEmpty)
    }

    @MainActor
    func testProjectSwitchMidSyncWritesNothingUnderNewProject() async throws {
        let transport = RouteTransport { ($0.path ?? "").hasPrefix("/projects/99/records/rfi") && ($0.path ?? "").contains("since=2") }
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route(
            "/projects/99/records/rfi",
            RecordStub.page(ids: [1, 2], nextSince: 2, hasMore: true),
            RecordStub.page(ids: [3, 4], nextSince: 4, hasMore: false)
        )
        transport.route(
            "/projects/100/records/rfi",
            RecordStub.page(ids: [], nextSince: 0, hasMore: false),
            RecordStub.page(ids: [], nextSince: 0, hasMore: false)
        )
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        let syncA = try XCTUnwrap(state.recordSync)
        // Joins project 99's run, so awaiting it waits for that run to end.
        let runA = Task { await syncA.runOnce() }
        try await transport.waitUntilHeld()

        state.selectedProjectID = 100
        let syncB = try XCTUnwrap(state.recordSync)
        XCTAssertFalse(syncA === syncB)
        transport.release()
        await runA.value
        await state.syncRecords(force: false, full: false)

        XCTAssertTrue(state.records.records(projectID: 100, kind: .rfi, query: "", filter: .all).isEmpty)
        XCTAssertEqual(state.records.records(projectID: 99, kind: .rfi, query: "", filter: .all).map(\.procoreID), [2, 1])
        XCTAssertEqual(state.records.state(projectID: 99, kind: .rfi)?.nextSince, 2, "the held page is dropped")
    }

    @MainActor
    func testSignOutMidSyncDropsPageAndClears() async throws {
        let transport = RouteTransport { ($0.path ?? "").hasPrefix("/projects/99/records/rfi") && ($0.path ?? "").contains("since=2") }
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route("/auth/logout", (HTTPResponse(status: .noContent), nil))
        transport.route(
            "/projects/99/records/rfi",
            RecordStub.page(ids: [1, 2], nextSince: 2, hasMore: true),
            RecordStub.page(ids: [3, 4], nextSince: 4, hasMore: false)
        )
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        let sync = try XCTUnwrap(state.recordSync)
        let run = Task { await sync.runOnce() }
        try await transport.waitUntilHeld()

        await state.signOut()
        transport.release()
        await run.value

        XCTAssertNil(state.recordSync)
        let context = try XCTUnwrap(container).mainContext
        XCTAssertEqual(try context.fetchCount(FetchDescriptor<CachedRecord>()), 0)
        XCTAssertEqual(try context.fetchCount(FetchDescriptor<ProjectSyncState>()), 0)
    }

    @MainActor
    func testBecameActiveRunsRecordSync() async throws {
        let transport = RouteTransport()
        transport.route("/me", APIStub.me(lastCompanyID: 7, lastProjectID: 99))
        transport.route(
            "/projects/99/records/rfi",
            RecordStub.page(ids: [1], nextSince: 1, hasMore: false),
            RecordStub.page(ids: [2], nextSince: 2, hasMore: false)
        )
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        await state.syncRecords(force: false, full: false)

        await state.becameActive()

        XCTAssertEqual(state.records.records(projectID: 99, kind: .rfi, query: "", filter: .all).map(\.procoreID), [2, 1])
    }

    @MainActor
    func testOfflineLaunchWithSelectionShowsLastSync() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99
        let syncedAt = Date(timeIntervalSince1970: 1_793_000_000)
        let context = try XCTUnwrap(container).mainContext
        context.insert(ProjectSyncState(projectID: 99, kind: .rfi, nextSince: 2, lastSyncedAt: syncedAt))
        try context.save()
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let state = makeState(
            store: InMemorySessionStore(validSession()), transport: transport,
            reachability: FakeReachability(isReachable: false)
        )

        await state.bootstrap()

        XCTAssertEqual(state.phase, .offline)
        let sync = try XCTUnwrap(state.recordSync, "offline Home needs the driver for its header")
        XCTAssertEqual(sync.status, .synced(syncedAt))
        XCTAssertEqual(transport.requests.map(\.path), ["/me"], "the offline driver is not run")
    }

    @MainActor
    func testOfflineLaunchNeverSyncedIsIdle() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99
        let state = makeState(
            store: InMemorySessionStore(validSession()),
            transport: ScriptedTransport([.failure(URLError(.notConnectedToInternet))]),
            reachability: FakeReachability(isReachable: false)
        )

        await state.bootstrap()

        XCTAssertEqual(state.phase, .offline)
        XCTAssertEqual(try XCTUnwrap(state.recordSync).status, .idle)
    }

    @MainActor
    func testOfflineLaunchWithoutSelectionHasNoDriver() async throws {
        let state = makeState(
            store: InMemorySessionStore(validSession()),
            transport: ScriptedTransport([.failure(URLError(.notConnectedToInternet))]),
            reachability: FakeReachability(isReachable: false)
        )

        await state.bootstrap()

        XCTAssertEqual(state.phase, .offline)
        XCTAssertNil(state.recordSync)
    }

    @MainActor
    func testBackgroundPassBootstrapsWhenNotStarted() async throws {
        // A cold background launch: the scene never connected, so nothing has bootstrapped yet.
        let draft = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "t",
                               capturedAt: .now.addingTimeInterval(-600))
        draft.updatedAt = .now.addingTimeInterval(-600)
        let transport = ScriptedTransport([.response(APIStub.me(procoreUserID: 42))])
        let state = makeState(
            store: InMemorySessionStore(validSession()), transport: transport,
            reachability: FakeReachability(isReachable: true)
        )
        try state.drafts.insert(draft)
        XCTAssertNil(state.draftSync)

        await state.runBackgroundPass()

        XCTAssertNotNil(state.draftSync)
        guard case .signedIn = state.phase else { return XCTFail("expected signedIn, got \(state.phase)") }
        let paths = transport.requests.map(\.path)
        XCTAssertEqual(paths.first, "/me")
        XCTAssertTrue(paths.contains("/drafts/\(draft.id.uuidString.lowercased())"), "one pass ran: \(paths)")
        cancelBackgroundWork(state)
    }

    @MainActor
    func testBackgroundPassBootstrapsFromOffline() async throws {
        // A cold launch in the basement left the session offline with no draft sync; by the time
        // the background task runs, the signal is back.
        let draft = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "t",
                               capturedAt: .now.addingTimeInterval(-600))
        draft.updatedAt = .now.addingTimeInterval(-600)
        let transport = ScriptedTransport([
            .failure(URLError(.notConnectedToInternet)),
            .response(APIStub.me(procoreUserID: 42)),
        ])
        let state = makeState(
            store: InMemorySessionStore(validSession()), transport: transport,
            reachability: FakeReachability(isReachable: true)
        )
        try state.drafts.insert(draft)
        await state.bootstrap()
        XCTAssertEqual(state.phase, .offline)
        XCTAssertNil(state.draftSync)

        await state.runBackgroundPass()

        XCTAssertNotNil(state.draftSync, "the sync starts once /me succeeds")
        guard case .signedIn = state.phase else { return XCTFail("expected signedIn, got \(state.phase)") }
        let paths = transport.requests.map(\.path)
        XCTAssertEqual(Array(paths.prefix(2)), ["/me", "/me"])
        XCTAssertTrue(paths.contains("/drafts/\(draft.id.uuidString.lowercased())"), "one pass ran: \(paths)")
        cancelBackgroundWork(state)
    }

    @MainActor
    func testBackgroundPassWithoutSessionDoesNothing() async throws {
        let transport = ScriptedTransport([])
        let state = makeState(
            store: InMemorySessionStore(), transport: transport,
            reachability: FakeReachability(isReachable: true)
        )

        await state.runBackgroundPass()

        XCTAssertNil(state.draftSync)
        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertTrue(transport.requests.isEmpty)
    }

    @MainActor
    func testSuccessfulBootstrapReplacesOfflineDriver() async throws {
        let first = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        first.selectedCompanyID = 7
        first.selectedProjectID = 99
        let transport = ScriptedTransport([
            .failure(URLError(.notConnectedToInternet)),
            .response(APIStub.me(lastCompanyID: 7, lastProjectID: 99)),
        ])
        let state = makeState(
            store: InMemorySessionStore(validSession()), transport: transport,
            reachability: FakeReachability(isReachable: false)
        )
        await state.bootstrap()
        let offlineSync = try XCTUnwrap(state.recordSync)

        await state.bootstrap()

        guard case .signedIn = state.phase else { return XCTFail("expected signedIn, got \(state.phase)") }
        let sync = try XCTUnwrap(state.recordSync)
        XCTAssertFalse(sync === offlineSync, "a running driver replaces the offline one")
        cancelBackgroundWork(state)
    }

    @MainActor
    func testSignOutRemovesDownloadedAttachments() async throws {
        let file = try placeAttachmentFile()
        let transport = StubTransport([APIStub.me()])
        transport.enqueue(.noContent)
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport)
        await state.bootstrap()
        XCTAssertTrue(FileManager.default.fileExists(atPath: file.path))

        await state.signOut()

        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: AttachmentFiles.directory.path))
    }

    @MainActor
    func testBootstrapWithoutSessionClearsRecordsAndAttachments() async throws {
        let state = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        let context = try XCTUnwrap(container).mainContext
        for id: Int64 in [1, 2] {
            context.insert(CachedRecord(
                projectID: 99, kind: .rfi, procoreID: id, changeSeq: id, updatedAt: Date(), bodyData: Data(),
                number: "\(id)", subject: "Subject", status: "open", ballInCourt: "", dueDate: nil
            ))
        }
        context.insert(ProjectSyncState(projectID: 99, kind: .rfi, nextSince: 2, lastSyncedAt: Date()))
        try context.save()
        let file = try placeAttachmentFile()

        await state.bootstrap()

        XCTAssertEqual(state.phase, .signedOut)
        XCTAssertEqual(try context.fetchCount(FetchDescriptor<CachedRecord>()), 0)
        XCTAssertEqual(try context.fetchCount(FetchDescriptor<ProjectSyncState>()), 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
    }

    // MARK: - Helpers

    @MainActor
    func testSessionGenerationAdvancesOnBootstrapAndSignIn() async throws {
        let signedOut = makeState(store: InMemorySessionStore(), transport: StubTransport([]))
        await signedOut.bootstrap()
        XCTAssertEqual(signedOut.sessionGeneration, 0, "no session, nothing to reload")

        await signedOut.signIn() // presenter cancels
        XCTAssertEqual(signedOut.sessionGeneration, 0, "a failed sign-in does not advance the generation")

        let transport = StubTransport([
            APIStub.me(),
            APIStub.authStartOK(),
            APIStub.tokenPair(access: "acc2", refresh: "ref2"),
            APIStub.me(),
        ])
        let presenter = FakeWebAuthPresenter(callback: URL(string: "fieldassistant://auth?code=abc")!)
        let state = makeState(store: InMemorySessionStore(validSession()), transport: transport, presenter: presenter)

        await state.bootstrap()
        XCTAssertEqual(state.sessionGeneration, 1)

        await state.signIn()
        XCTAssertEqual(state.sessionGeneration, 2, "re-sign-in tells screens to reload")
    }

    private func notModified(count: Int) -> [(HTTPResponse, HTTPBody?)] {
        Array(repeating: (HTTPResponse(status: .notModified), nil), count: count)
    }

    private func validSession() -> Session {
        Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: Date().addingTimeInterval(3600))
    }

    /// A file in the attachment download directory, as `AttachmentViewerModel` would leave one.
    private func placeAttachmentFile() throws -> URL {
        let directory = AttachmentFiles.directory.appendingPathComponent("123", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let file = directory.appendingPathComponent("plan.pdf")
        try Data("pdf".utf8).write(to: file)
        return file
    }

    /// Stops the sync and prefetch a signed-in state started, so they do not outlive the test.
    @MainActor
    private func cancelBackgroundWork(_ state: AppState) {
        state.recordSync?.stop()
        state.draftSync?.stop()
        state.prefetchTask?.cancel()
    }

    @MainActor
    private func makeState(
        store: InMemorySessionStore,
        transport: ClientTransport,
        presenter: WebAuthPresenter = FakeWebAuthPresenter(error: CancellationError()),
        reachability: ReachabilityProviding? = nil
    ) -> AppState {
        let container = self.container ?? (try! AppContainer.makeModelContainer(inMemory: true).get())
        self.container = container
        return AppState(
            store: store,
            presenter: presenter,
            baseURL: APIStub.baseURL,
            modelContext: container.mainContext,
            transport: transport,
            defaults: UserDefaults(suiteName: suiteName)!,
            reachability: reachability
        )
    }
}
