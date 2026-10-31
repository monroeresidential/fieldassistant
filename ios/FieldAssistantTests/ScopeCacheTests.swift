import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

@MainActor
final class ScopeCacheTests: XCTestCase {
    func testProjectListFetchedOncePerCompanyPerSession() async throws {
        let transport = StubTransport([
            Self.projects(#"[{"id":99,"name":"Tower A","display_name":"001 - Tower A"}]"#),
            Self.projects(#"[{"id":99,"name":"Tower A","display_name":"001 - Tower A"},{"id":100,"name":"Tower B","display_name":"002 - Tower B"}]"#),
        ])
        let cache = ScopeCache(service: ScopeService(client: makeClient(transport)))

        for _ in 0..<3 {
            let list = try await cache.projects(companyID: 7)
            XCTAssertEqual(list.map(\.id), [99])
        }
        XCTAssertEqual(transport.requests.count, 1, "three reads, one request")

        let refreshed = try await cache.projects(companyID: 7, force: true)
        XCTAssertEqual(refreshed.map(\.id), [99, 100])
        XCTAssertEqual(transport.requests.count, 2, "force fetches again")
        let cached = try await cache.projects(companyID: 7)
        XCTAssertEqual(cached.map(\.id), [99, 100], "the forced answer replaces the cached one")
        XCTAssertEqual(transport.requests.count, 2)
    }

    func testCompanyChangeDoesNotRefetchCompanies() async throws {
        let transport = StubTransport([
            Self.companies(),
            Self.projects(#"[{"id":99,"name":"Tower A","display_name":"001 - Tower A"}]"#),
            Self.projects(#"[{"id":200,"name":"Depot","display_name":"010 - Depot"}]"#),
        ])
        let cache = ScopeCache(service: ScopeService(client: makeClient(transport)))

        _ = try await cache.companies()
        _ = try await cache.projects(companyID: 7)
        _ = try await cache.companies()
        let other = try await cache.projects(companyID: 8)
        _ = try await cache.projects(companyID: 7)
        _ = try await cache.companies()

        XCTAssertEqual(other.map(\.id), [200])
        XCTAssertEqual(
            transport.requests.map(\.request.path),
            ["/companies", "/companies/7/projects", "/companies/8/projects"]
        )
    }

    func testClearDropsCache() async throws {
        let transport = StubTransport([
            Self.companies(),
            Self.projects(#"[{"id":99,"name":"Tower A","display_name":"001 - Tower A"}]"#),
            Self.companies(),
            Self.projects(#"[{"id":99,"name":"Tower A","display_name":"001 - Tower A"}]"#),
        ])
        let cache = ScopeCache(service: ScopeService(client: makeClient(transport)))
        _ = try await cache.companies()
        _ = try await cache.projects(companyID: 7)

        cache.clear()
        _ = try await cache.companies()
        _ = try await cache.projects(companyID: 7)

        XCTAssertEqual(
            transport.requests.map(\.request.path),
            ["/companies", "/companies/7/projects", "/companies", "/companies/7/projects"]
        )
    }

    func testFailedFetchCachesNothing() async throws {
        let transport = StubTransport([
            APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is down"),
            Self.companies(),
        ])
        let cache = ScopeCache(service: ScopeService(client: makeClient(transport)))

        do {
            _ = try await cache.companies()
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(APIFailure(error), .server("Procore is down"))
        }
        let list = try await cache.companies()

        XCTAssertEqual(list.map(\.id), [7, 8])
        XCTAssertEqual(transport.requests.count, 2)
    }

    /// The picker's "Last used" tag follows the project chosen in this session, falling back to the
    /// one `/me` reported.
    func testPickerLastUsedPrefersCurrentSelection() {
        XCTAssertEqual(ProjectPickerView.lastUsedProjectID(selected: 100, meLast: 99), 100)
        XCTAssertEqual(ProjectPickerView.lastUsedProjectID(selected: nil, meLast: 99), 99)
        XCTAssertNil(ProjectPickerView.lastUsedProjectID(selected: nil, meLast: nil))
    }

    private static func companies() -> (HTTPResponse, HTTPBody?) {
        APIStub.json(.ok, #"[{"id":7,"name":"Monroe Residential"},{"id":8,"name":"Acme Builders"}]"#)
    }

    private static func projects(_ json: String) -> (HTTPResponse, HTTPBody?) {
        APIStub.json(.ok, json)
    }
}
