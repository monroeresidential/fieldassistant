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
    private func makeStore(_ transport: ClientTransport) throws -> (LookupStore, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        let client = makeClient(transport, middlewares: [IfNoneMatchMiddleware()])
        let store = LookupStore(context: container.mainContext, service: LookupService(client: client))
        return (store, container)
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
