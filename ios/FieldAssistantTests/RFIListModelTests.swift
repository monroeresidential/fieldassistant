import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class RFIListModelTests: XCTestCase {
    @MainActor
    func testRFIListLoads() async throws {
        let transport = StubTransport([
            APIStub.json(.ok, #"""
            [{"id":1,"number":"001","subject":"Footing depth","status":"open","due_date":"2026-11-03","assignees":["Pat Field"]},
             {"id":2,"number":"002","subject":"Window spec","status":"closed","assignees":[]}]
            """#),
        ])
        let model = RFIListModel(client: makeClient(transport))
        XCTAssertEqual(model.state, .idle)

        await model.load(companyID: 7, projectID: 99)

        guard case .loaded(let items) = model.state else {
            return XCTFail("expected loaded, got \(model.state)")
        }
        XCTAssertEqual(items.map(\.number), ["001", "002"])
        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.path, "/projects/99/rfis")
        XCTAssertEqual(recorded.request.headerFields[HTTPField.Name("X-Company-Id")!], "7")

        let due = try XCTUnwrap(items[0].dueDate)
        let parts = Calendar.current.dateComponents([.year, .month, .day], from: due)
        XCTAssertEqual([parts.year, parts.month, parts.day], [2026, 11, 3])
        XCTAssertNil(items[1].dueDate)
    }

    @MainActor
    func testRFIListPermissionDeniedShowsMessage() async throws {
        let message = "Your Procore permissions do not allow viewing RFIs."
        let transport = StubTransport([
            APIStub.error(.forbidden, code: "procore_permission_denied", message: message),
        ])
        let model = RFIListModel(client: makeClient(transport))

        await model.load(companyID: 7, projectID: 99)

        XCTAssertEqual(model.state, .failed(.permissionDenied(message)))
    }

    @MainActor
    func testRFIListOfflineShowsOffline() async throws {
        let model = RFIListModel(client: makeClient(FailingTransport(error: URLError(.notConnectedToInternet))))

        await model.load(companyID: 7, projectID: 99)

        XCTAssertEqual(model.state, .failed(.offline))
    }

    @MainActor
    func testRFIListReauthRequired() async throws {
        let transport = StubTransport([
            APIStub.error(.unauthorized, code: "reauth_required", message: "Sign in again."),
        ])
        let model = RFIListModel(client: makeClient(transport))

        await model.load(companyID: 7, projectID: 99)

        XCTAssertEqual(model.state, .failed(.reauthRequired))
    }

    @MainActor
    func testRFIListUpstreamUnavailableIsServerFailure() async throws {
        let transport = StubTransport([
            APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is unavailable."),
        ])
        let model = RFIListModel(client: makeClient(transport))

        await model.load(companyID: 7, projectID: 99)

        XCTAssertEqual(model.state, .failed(.server("Procore is unavailable.")))
    }

    @MainActor
    func testRefreshKeepsPreviousItemsWhileLoading() async throws {
        let transport = GatedTransport([Self.rfis(["001", "002"]), Self.rfis(["001", "002", "003"])])
        let model = RFIListModel(client: makeClient(transport))
        await model.load(companyID: 7, projectID: 99)

        transport.closeGate()
        let refresh = Task { await model.load(companyID: 7, projectID: 99) }
        await transport.waitUntilBlocked()

        XCTAssertEqual(model.state, .loading)
        XCTAssertEqual(model.lastLoaded?.map(\.number), ["001", "002"], "previous items stay on screen during refresh")

        transport.openGate()
        await refresh.value

        guard case .loaded(let items) = model.state else {
            return XCTFail("expected loaded, got \(model.state)")
        }
        XCTAssertEqual(items.map(\.number), ["001", "002", "003"])
        XCTAssertEqual(model.lastLoaded?.map(\.number), ["001", "002", "003"])
    }

    @MainActor
    func testCancelledRefreshRestoresPreviousState() async throws {
        let transport = GatedTransport([Self.rfis(["001"]), Self.rfis(["001", "002"])])
        let model = RFIListModel(client: makeClient(transport))
        await model.load(companyID: 7, projectID: 99)
        let before = model.state

        transport.closeGate()
        let refresh = Task { await model.load(companyID: 7, projectID: 99) }
        await transport.waitUntilBlocked()
        refresh.cancel()
        transport.openGate()
        await refresh.value

        XCTAssertEqual(model.state, before, "a cancelled refresh must not leave the list stuck in .loading")
    }

    @MainActor
    func testReloadAfterSessionGenerationChange() async throws {
        // The view re-runs `load` when `AppState.sessionGeneration` changes; the model must recover from
        // `.failed(.reauthRequired)` on that reload.
        let transport = StubTransport([
            APIStub.error(.unauthorized, code: "reauth_required", message: "Sign in again."),
            Self.rfis(["001"]),
        ])
        let model = RFIListModel(client: makeClient(transport))
        await model.load(companyID: 7, projectID: 99)
        XCTAssertEqual(model.state, .failed(.reauthRequired))

        await model.load(companyID: 7, projectID: 99)

        guard case .loaded(let items) = model.state else {
            return XCTFail("expected loaded, got \(model.state)")
        }
        XCTAssertEqual(items.map(\.number), ["001"])
    }

    private static func rfis(_ numbers: [String]) -> (HTTPResponse, HTTPBody?) {
        let items = numbers.enumerated().map { index, number in
            #"{"id":\#(index + 1),"number":"\#(number)","subject":"S\#(number)","status":"open","assignees":[]}"#
        }
        return APIStub.json(.ok, "[" + items.joined(separator: ",") + "]")
    }
}

/// Wraps `StubTransport` and, while its gate is closed, holds each request until `openGate()`.
/// A request released after its task was cancelled throws `CancellationError`, as URLSession does.
final class GatedTransport: ClientTransport, @unchecked Sendable {
    private let inner: StubTransport
    private let lock = NSLock()
    private var isClosed = false
    private var waiting: [CheckedContinuation<Void, Never>] = []

    init(_ script: [(HTTPResponse, HTTPBody?)]) {
        inner = StubTransport(script)
    }

    func closeGate() { lock.withLock { isClosed = true } }

    func openGate() {
        let released = lock.withLock {
            isClosed = false
            defer { waiting = [] }
            return waiting
        }
        released.forEach { $0.resume() }
    }

    /// Returns once a request is parked at the closed gate.
    func waitUntilBlocked() async {
        while lock.withLock({ waiting.isEmpty }) {
            await Task.yield()
        }
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        await withCheckedContinuation { continuation in
            let parked = lock.withLock {
                guard isClosed else { return false }
                waiting.append(continuation)
                return true
            }
            if !parked { continuation.resume() }
        }
        try Task.checkCancellation()
        return try await inner.send(request, body: body, baseURL: baseURL, operationID: operationID)
    }
}

