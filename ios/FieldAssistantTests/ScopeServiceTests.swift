import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class ScopeServiceTests: XCTestCase {
    func testCompaniesAndProjectsDecode() async throws {
        let transport = StubTransport([
            APIStub.json(.ok, #"[{"id":7,"name":"Monroe Residential"},{"id":8,"name":"Acme Builders"}]"#),
            APIStub.json(.ok, #"""
            [{"id":99,"name":"Tower A","display_name":"001 - Tower A"},
             {"id":100,"name":"Tower B","display_name":"002 - Tower B"},
             {"id":101,"name":"Garage","display_name":"003 - Garage"}]
            """#),
        ])
        let service = ScopeService(client: makeClient(transport))

        let companies = try await service.companies()
        let projects = try await service.projects(companyID: 7)

        XCTAssertEqual(companies.map(\.id), [7, 8])
        XCTAssertEqual(companies.first?.name, "Monroe Residential")
        XCTAssertEqual(projects.map(\.id), [99, 100, 101])
        XCTAssertEqual(projects.first?.display_name, "001 - Tower A")
        XCTAssertEqual(transport.requests.map(\.request.path), ["/companies", "/companies/7/projects"])
    }

    func testSaveSelectionSendsPut() async throws {
        let transport = StubTransport(statuses: [.noContent])
        let service = ScopeService(client: makeClient(transport))

        try await service.saveSelection(companyID: 7, projectID: 99)

        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .put)
        XCTAssertEqual(recorded.request.path, "/me/selection")
        let body = try APIStub.jsonObject(recorded.body)
        XCTAssertEqual(body["company_id"] as? Int, 7)
        XCTAssertEqual(body["project_id"] as? Int, 99)
        XCTAssertEqual(body.count, 2)
    }

    func testProjectsForInvisibleCompanyIsPermissionDenied() async throws {
        let transport = StubTransport([
            APIStub.error(.forbidden, code: "forbidden_scope", message: "You cannot access this company."),
        ])
        let service = ScopeService(client: makeClient(transport))

        do {
            _ = try await service.projects(companyID: 5)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(APIFailure(error), .permissionDenied("You cannot access this company."))
        }
    }
}

func makeClient(_ transport: ClientTransport, middlewares: [ClientMiddleware] = []) -> Client {
    Client(serverURL: APIStub.baseURL, configuration: .fieldAssistant, transport: transport, middlewares: middlewares)
}

/// `ClientTransport` that always fails with the given error, as URLSession does when offline.
struct FailingTransport: ClientTransport {
    let error: Error

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        throw error
    }
}
