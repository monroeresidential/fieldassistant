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
}
