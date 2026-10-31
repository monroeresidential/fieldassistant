import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

final class LookupStoreTests: XCTestCase {
    @MainActor
    func testRefreshStoresRows() async throws {
        let transport = StubTransport([
            APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"},{"id":12,"label":"Ana Ruiz"}]"#, etag: #""v1""#),
        ])
        let (store, container) = try makeStore(transport)

        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: false)

        XCTAssertEqual(store.rows(projectID: 99, kind: .users).map(\.id), [11, 12])
        XCTAssertEqual(store.set(projectID: 99, kind: .users)?.etag, #""v1""#)
        XCTAssertTrue(store.rows(projectID: 99, kind: .locations).isEmpty)
        XCTAssertTrue(store.rows(projectID: 100, kind: .users).isEmpty)
        // The refresh saves, so nothing is left pending in the context.
        XCTAssertFalse(container.mainContext.hasChanges)
    }

    @MainActor
    func testRefreshWith304KeepsRows() async throws {
        let transport = StubTransport([APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#, etag: #""v1""#)])
        transport.enqueue(.notModified)
        let (store, _) = try makeStore(transport)
        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: false)

        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: false)

        XCTAssertEqual(store.rows(projectID: 99, kind: .users).map(\.label), ["Ben West"])
        XCTAssertEqual(transport.requests.last?.request.headerFields[.ifNoneMatch], #""v1""#)
    }

    @MainActor
    func testRefreshReplacesRowsOnNewETag() async throws {
        let transport = StubTransport([
            APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#, etag: #""v1""#),
            APIStub.lookup(rows: #"[{"id":12,"label":"Ana Ruiz"}]"#, etag: #""v2""#),
        ])
        let (store, container) = try makeStore(transport)
        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: false)

        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: true)

        XCTAssertEqual(store.rows(projectID: 99, kind: .users).map(\.label), ["Ana Ruiz"])
        XCTAssertEqual(store.set(projectID: 99, kind: .users)?.etag, #""v2""#)
        XCTAssertEqual(try container.mainContext.fetchCount(FetchDescriptor<CachedLookup>()), 1, "one row per project and kind")
    }

    @MainActor
    func testPrefetchContinuesPastOneFailure() async throws {
        let transport = RoutingTransport { kind in
            kind == "locations"
                ? APIStub.error(.internalServerError, code: "internal", message: "boom")
                : APIStub.lookup(kind: kind, rows: #"[{"id":1,"label":"\#(kind)"}]"#)
        }
        let (store, _) = try makeStore(transport)

        await store.prefetch(companyID: 7, projectID: 99)

        XCTAssertFalse(store.isPrefetching)
        XCTAssertEqual(Set(transport.kinds), Set(LookupKind.allCases.map(\.rawValue)))
        for kind in LookupKind.allCases where kind != .locations {
            XCTAssertEqual(store.rows(projectID: 99, kind: kind).map(\.label), [kind.rawValue], "\(kind) loads")
        }
        XCTAssertTrue(store.rows(projectID: 99, kind: .locations).isEmpty)
        XCTAssertNil(store.set(projectID: 99, kind: .locations))
    }

    @MainActor
    func testUnavailableKindStoredWithReason() async throws {
        let reason = "Your Procore permissions do not allow reading the vendor directory."
        let transport = StubTransport([APIStub.lookup(kind: "vendors", unavailable: true, reason: reason)])
        let (store, _) = try makeStore(transport)

        try await store.refresh(companyID: 7, projectID: 99, kind: .vendors, force: false)

        let cached = try XCTUnwrap(store.set(projectID: 99, kind: .vendors))
        XCTAssertTrue(cached.unavailable)
        XCTAssertEqual(cached.reason, reason)
        XCTAssertTrue(store.rows(projectID: 99, kind: .vendors).isEmpty)
    }

    @MainActor
    func testClearAllDeletesEveryProject() async throws {
        let transport = StubTransport([
            APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#),
            APIStub.lookup(kind: "locations", rows: #"[{"id":5,"label":"Level 2"}]"#),
        ])
        let (store, container) = try makeStore(transport)
        try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: false)
        try await store.refresh(companyID: 7, projectID: 100, kind: .locations, force: false)

        store.clearAll()

        XCTAssertTrue(store.rows(projectID: 99, kind: .users).isEmpty)
        XCTAssertNil(store.set(projectID: 100, kind: .locations))
        XCTAssertEqual(try container.mainContext.fetchCount(FetchDescriptor<CachedLookup>()), 0)
    }

    @MainActor
    func testIsPrefetchingStaysTrueUntilLatestPrefetchFinishes() async throws {
        let transport = ProjectGatedTransport(gated: [1, 2])
        let (store, _) = try makeStore(transport)
        let kinds = LookupKind.allCases.count

        let first = Task { await store.prefetch(companyID: 7, projectID: 1) }
        await transport.waitUntilParked(projectID: 1, count: kinds)
        let second = Task { await store.prefetch(companyID: 7, projectID: 2) }
        await transport.waitUntilParked(projectID: 2, count: kinds)

        first.cancel()
        transport.release(projectID: 1)
        await first.value
        XCTAssertTrue(store.isPrefetching, "the newer prefetch is still running")

        transport.release(projectID: 2)
        await second.value
        XCTAssertFalse(store.isPrefetching)
    }

    @MainActor
    func testRefreshFinishingAfterClearAllWritesNothing() async throws {
        let gate = GateTransport(StubTransport([APIStub.lookup(rows: #"[{"id":11,"label":"Ben West"}]"#)]))
        let (store, container) = try makeStore(gate)
        // Not cancelled: only the clear tells the refresh its rows belong to an ended session.
        let refresh = Task { try await store.refresh(companyID: 7, projectID: 99, kind: .users, force: true) }
        try await gate.waitUntilHeld()

        store.clearAll()
        gate.release()
        try await refresh.value

        XCTAssertNil(store.set(projectID: 99, kind: .users))
        XCTAssertEqual(try container.mainContext.fetchCount(FetchDescriptor<CachedLookup>()), 0)
    }

    @MainActor
    private func makeStore(_ transport: ClientTransport) throws -> (LookupStore, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true).get()
        let client = makeClient(transport, middlewares: [IfNoneMatchMiddleware()])
        let store = LookupStore(context: container.mainContext, service: LookupService(client: client))
        return (store, container)
    }
}

/// `ClientTransport` that holds lookup requests for the gated projects until `release(projectID:)`,
/// then answers 304. Requests for other projects answer 304 at once.
final class ProjectGatedTransport: ClientTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var gated: Set<Int64>
    private var parked: [Int64: [CheckedContinuation<Void, Never>]] = [:]

    init(gated: Set<Int64>) {
        self.gated = gated
    }

    func waitUntilParked(projectID: Int64, count: Int) async {
        while lock.withLock({ parked[projectID, default: []].count }) < count {
            await Task.yield()
        }
    }

    func release(projectID: Int64) {
        let released = lock.withLock {
            gated.remove(projectID)
            return parked.removeValue(forKey: projectID) ?? []
        }
        released.forEach { $0.resume() }
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        let segments = request.path?.split(separator: "/") ?? []
        let projectID = segments.count > 1 ? Int64(segments[1]) ?? 0 : 0
        await withCheckedContinuation { continuation in
            let isParked = lock.withLock {
                guard gated.contains(projectID) else { return false }
                parked[projectID, default: []].append(continuation)
                return true
            }
            if !isParked { continuation.resume() }
        }
        return (HTTPResponse(status: .notModified), nil)
    }
}

/// `ClientTransport` that answers each lookup request by its `kind` path segment, for concurrent fetches.
final class RoutingTransport: ClientTransport, @unchecked Sendable {
    private let lock = NSLock()
    private let respond: @Sendable (String) -> (HTTPResponse, HTTPBody?)
    private var recordedKinds: [String] = []

    init(_ respond: @escaping @Sendable (String) -> (HTTPResponse, HTTPBody?)) {
        self.respond = respond
    }

    var kinds: [String] { lock.withLock { recordedKinds } }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        let kind = String(request.path?.split(separator: "/").last ?? "")
        lock.withLock { recordedKinds.append(kind) }
        return respond(kind)
    }
}
