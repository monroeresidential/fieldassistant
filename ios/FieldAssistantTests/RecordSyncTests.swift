import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

final class RecordSyncTests: XCTestCase {
    /// Kept alive for the test: `ModelContext` does not own its container.
    private var container: ModelContainer?

    override func tearDown() {
        container = nil
        super.tearDown()
    }

    @MainActor
    func testRunOncePagesUntilDone() async throws {
        let transport = StubTransport([
            RecordStub.page(ids: [1, 2, 3], nextSince: 3, hasMore: true),
            RecordStub.page(ids: [4, 5], nextSince: 5, hasMore: true),
            RecordStub.page(ids: [6, 7], nextSince: 7, hasMore: false),
        ])
        let (sync, store) = makeSync(transport)

        await sync.runOnce()

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).count, 7)
        let state = try XCTUnwrap(store.state(projectID: 7, kind: .rfi))
        XCTAssertEqual(state.nextSince, 7)
        XCTAssertEqual(sync.status, .synced(try XCTUnwrap(state.lastSyncedAt)))
        let paths = transport.requests.compactMap(\.request.path)
        XCTAssertEqual(paths.count, 3)
        XCTAssertTrue(paths[0].contains("since=0"), paths[0])
        XCTAssertTrue(paths[1].contains("since=3"), paths[1])
        XCTAssertTrue(paths[2].contains("since=5"), paths[2])
        XCTAssertTrue(transport.requests.allSatisfy { $0.request.headerFields[.cacheControl] == nil })
    }

    @MainActor
    func testRunOnceCoalescesConcurrentCalls() async throws {
        let gate = GateTransport(StubTransport([RecordStub.page(ids: [1], nextSince: 1, hasMore: false)]))
        let (sync, store) = makeSync(gate)

        // Both tasks are queued on the main actor before either runs, so the second finds the
        // first one's run in progress before the gate opens.
        let first = Task { await sync.runOnce() }
        let second = Task { await sync.runOnce() }
        try await gate.waitUntilHeld()
        XCTAssertEqual(sync.status, .syncing)
        gate.release()
        await first.value
        await second.value

        XCTAssertEqual(gate.inner.requests.count, 1)
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).count, 1)
    }

    @MainActor
    func testForcedCallJoiningUnforcedRunRunsOneMorePass() async throws {
        let inner = StubTransport([
            RecordStub.page(ids: [1], nextSince: 1, hasMore: false),
            APIStub.json(.ok, #"{"pulls":[{"kind":"rfi","status":"pulled"}]}"#),
            RecordStub.page(ids: [2], nextSince: 2, hasMore: false),
        ])
        let gate = GateTransport(inner)
        let (sync, store) = makeSync(gate)

        // Queued in order on the main actor: the forced call joins the unforced run before the gate opens.
        let unforced = Task { await sync.runOnce() }
        let forced = Task { await sync.runOnce(force: true) }
        try await gate.waitUntilHeld()
        gate.release()
        await unforced.value
        await forced.value

        let requests = inner.requests
        guard requests.count == 3 else {
            return XCTFail("expected exactly one extra pass, got \(requests.compactMap(\.request.path))")
        }
        XCTAssertTrue(try XCTUnwrap(requests[0].request.path).contains("since=0"))
        XCTAssertEqual(requests[1].request.method, .post)
        XCTAssertEqual(requests[1].request.path, "/projects/7/sync")
        XCTAssertTrue(try XCTUnwrap(requests[2].request.path).contains("since=1"))
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).count, 2)
    }

    @MainActor
    func testRunOnceOfflineKeepsRowsAndReportsFailure() async throws {
        let transport = ScriptedTransport([
            .response(RecordStub.page(ids: [1, 2], nextSince: 2, hasMore: false)),
            .failure(URLError(.notConnectedToInternet)),
        ])
        let (sync, store) = makeSync(transport)
        await sync.runOnce()
        let syncedAt = try XCTUnwrap(store.state(projectID: 7, kind: .rfi)?.lastSyncedAt)

        await sync.runOnce()

        XCTAssertEqual(transport.requests.count, 2, "reachable, so the network was tried")
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).map(\.procoreID), [2, 1])
        let state = try XCTUnwrap(store.state(projectID: 7, kind: .rfi))
        XCTAssertEqual(state.lastSyncedAt, syncedAt)
        XCTAssertEqual(state.nextSince, 2)
        XCTAssertEqual(state.lastErrorCode, "offline")
        XCTAssertEqual(sync.status, .failed(code: "offline", message: APIFailure.offline.userMessage))
    }

    @MainActor
    func testUnreachableWithoutForceSkipsNetwork() async throws {
        let reachability = FakeReachability(isReachable: false)
        let transport = StubTransport([])
        let (sync, store) = makeSync(transport, reachability: reachability)

        await sync.runOnce()

        XCTAssertTrue(transport.requests.isEmpty)
        XCTAssertEqual(sync.status, .failed(code: "offline", message: "No signal"))

        // Once synced, an unreachable pass keeps showing the last sync.
        let synced = makeSync(StubTransport([RecordStub.page(ids: [1], nextSince: 1, hasMore: false)]), store: store)
        await synced.runOnce()
        let syncedAt = try XCTUnwrap(store.state(projectID: 7, kind: .rfi)?.lastSyncedAt)
        reachability.set(false)

        await sync.runOnce()

        XCTAssertTrue(transport.requests.isEmpty)
        XCTAssertEqual(sync.status, .synced(syncedAt))
    }

    @MainActor
    func testFailedPullAppliesPageAndReportsFailure() async throws {
        let transport = StubTransport([
            RecordStub.page(
                ids: [1], nextSince: 1, hasMore: false,
                pull: #"{"kind":"rfi","status":"failed","code":"procore_unavailable","message":"Procore is down"}"#
            ),
        ])
        let (sync, store) = makeSync(transport)

        await sync.runOnce()

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).count, 1)
        XCTAssertEqual(sync.status, .failed(code: "procore_unavailable", message: "Procore is down"))
    }

    @MainActor
    func testForceCallsSyncEndpointFirst() async throws {
        let transport = StubTransport([
            APIStub.json(.ok, #"{"pulls":[{"kind":"rfi","status":"pulled"}]}"#),
            RecordStub.page(ids: [1], nextSince: 1, hasMore: false),
        ])
        let (sync, store) = makeSync(transport)

        await sync.runOnce(force: true, full: true)

        let requests = transport.requests
        XCTAssertEqual(requests.count, 2)
        XCTAssertEqual(requests[0].request.method, .post)
        XCTAssertEqual(requests[0].request.path, "/projects/7/sync")
        XCTAssertEqual(try APIStub.jsonObject(requests[0].body)["full"] as? Bool, true)
        XCTAssertEqual(requests[1].request.method, .get)
        XCTAssertTrue(try XCTUnwrap(requests[1].request.path).hasPrefix("/projects/7/records/rfi?"))
        XCTAssertEqual(sync.status, .synced(try XCTUnwrap(store.state(projectID: 7, kind: .rfi)?.lastSyncedAt)))
    }

    @MainActor
    func testForceSyncsEvenWhenUnreachable() async throws {
        let transport = StubTransport([
            APIStub.json(.ok, #"{"pulls":[{"kind":"rfi","status":"pulled"}]}"#),
            RecordStub.page(ids: [1], nextSince: 1, hasMore: false),
        ])
        let (sync, _) = makeSync(transport, reachability: FakeReachability(isReachable: false))

        await sync.runOnce(force: true)

        XCTAssertEqual(transport.requests.count, 2)
    }

    @MainActor
    func testStopCancelsMidPaging() async throws {
        let inner = StubTransport([
            RecordStub.page(ids: [1, 2], nextSince: 2, hasMore: true),
            RecordStub.page(ids: [3, 4], nextSince: 4, hasMore: true),
            RecordStub.page(ids: [5], nextSince: 5, hasMore: false),
        ])
        let gate = GateTransport(inner) { $0.path?.contains("since=2") == true }
        let (sync, store) = makeSync(gate)
        let run = Task { await sync.runOnce() }
        try await gate.waitUntilHeld()

        sync.stop()
        let syncedAt = try XCTUnwrap(store.state(projectID: 7, kind: .rfi)?.lastSyncedAt)
        XCTAssertEqual(sync.status, .synced(syncedAt), "a stopped driver shows the last sync, not a spinner")
        gate.release()
        await run.value

        XCTAssertEqual(inner.requests.count, 2, "page 3 is never requested")
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).map(\.procoreID), [2, 1])
        XCTAssertEqual(store.state(projectID: 7, kind: .rfi)?.nextSince, 2)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi)?.lastErrorCode, "a cancelled run records nothing")
        XCTAssertEqual(sync.status, .synced(syncedAt))
    }

    // MARK: - Helpers

    @MainActor
    private func makeSync(
        _ transport: ClientTransport,
        reachability: FakeReachability = FakeReachability(isReachable: true)
    ) -> (RecordSync, RecordStore) {
        let container = self.container ?? (try! AppContainer.makeModelContainer(inMemory: true))
        self.container = container
        let store = RecordStore(context: container.mainContext)
        return (makeSync(transport, store: store, reachability: reachability), store)
    }

    @MainActor
    private func makeSync(
        _ transport: ClientTransport,
        store: RecordStore,
        reachability: FakeReachability = FakeReachability(isReachable: true)
    ) -> RecordSync {
        let client = makeClient(transport)
        return RecordSync(
            store: store,
            service: { RecordService(client: client) },
            companyID: 3,
            projectID: 7,
            kinds: [.rfi],
            reachability: reachability
        )
    }
}

/// Builders for `getRecords` responses.
enum RecordStub {
    static func rfiJSON(id: Int64) -> String {
        #"""
        {"id":\#(id),"number":"\#(id)","subject":"Subject \#(id)","status":"open","draft":false,"private":false,\#
        "created_at":"2026-10-20T14:00:00Z","updated_at":"2026-10-24T09:30:00Z","assignees":[],"ball_in_courts":[],\#
        "distribution_list":[],"drawing_ids":[],"current_revision":true,"has_revisions":false,"questions":[]}
        """#
    }

    /// A 200 `RecordPage` whose records have `change_seq` equal to their id.
    static func page(
        ids: [Int64],
        nextSince: Int64,
        hasMore: Bool,
        pull: String = #"{"kind":"rfi","status":"pulled"}"#
    ) -> (HTTPResponse, HTTPBody?) {
        let records = ids.map {
            #"{"id":\#($0),"kind":"rfi","updated_at":"2026-10-24T09:30:00Z","change_seq":\#($0),"rfi":\#(rfiJSON(id: $0))}"#
        }.joined(separator: ",")
        return APIStub.json(.ok, #"""
        {"records":[\#(records)],"deleted_ids":[],"next_since":\#(nextSince),"has_more":\#(hasMore),\#
        "synced_at":"2026-10-25T10:00:00Z","pull":\#(pull)}
        """#)
    }
}

/// `ClientTransport` that answers each request from the first route whose path prefix matches, in
/// order, and can hold matching requests until `release()`. Unrouted requests get a 503. Lookup
/// prefetch and record sync run concurrently in `AppState`, so a single ordered script would race.
final class RouteTransport: ClientTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var routes: [(prefix: String, responses: [(HTTPResponse, HTTPBody?)])] = []
    private var recorded: [HTTPRequest] = []
    private let gating: @Sendable (HTTPRequest) -> Bool
    private var released = false
    private var held = 0
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(gating: @escaping @Sendable (HTTPRequest) -> Bool = { _ in false }) {
        self.gating = gating
    }

    func route(_ prefix: String, _ responses: (HTTPResponse, HTTPBody?)...) {
        route(prefix, responses)
    }

    func route(_ prefix: String, _ responses: [(HTTPResponse, HTTPBody?)]) {
        lock.withLock { routes.append((prefix, responses)) }
    }

    var requests: [HTTPRequest] { lock.withLock { recorded } }
    var paths: [String] { requests.compactMap(\.path) }

    /// Returns once a request is waiting at the gate, or fails after about five seconds.
    func waitUntilHeld() async throws {
        for _ in 0..<500 {
            if lock.withLock({ held > 0 }) { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTFail("no request reached the gate")
    }

    func release() {
        let pending = lock.withLock {
            released = true
            defer { waiters = [] }
            return waiters
        }
        pending.forEach { $0.resume() }
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        if gating(request) {
            await withCheckedContinuation { continuation in
                let resumeNow = lock.withLock {
                    held += 1
                    if released { return true }
                    waiters.append(continuation)
                    return false
                }
                if resumeNow { continuation.resume() }
            }
        }
        return lock.withLock {
            recorded.append(request)
            let path = request.path ?? ""
            guard let index = routes.firstIndex(where: { path.hasPrefix($0.prefix) && !$0.responses.isEmpty }) else {
                return APIStub.error(.serviceUnavailable, code: "procore_unavailable", message: "unrouted")
            }
            return routes[index].responses.removeFirst()
        }
    }
}
