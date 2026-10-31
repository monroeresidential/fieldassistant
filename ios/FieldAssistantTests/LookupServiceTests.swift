import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class LookupServiceTests: XCTestCase {
    func testFetchSendsIfNoneMatchAndParses304() async throws {
        let transport = StubTransport(statuses: [.notModified])
        let service = LookupService(client: makeClient(transport, middlewares: [IfNoneMatchMiddleware()]))

        let result = try await service.fetch(companyID: 7, projectID: 99, kind: .users, etag: #""v1""#)

        guard case .notModified = result else { return XCTFail("expected notModified, got \(result)") }
        let request = try XCTUnwrap(transport.requests.first).request
        XCTAssertEqual(request.path, "/projects/99/lookups/users")
        XCTAssertEqual(request.headerFields[HTTPField.Name("X-Company-Id")!], "7")
        XCTAssertEqual(request.headerFields[.ifNoneMatch], #""v1""#)
        XCTAssertNil(request.headerFields[.cacheControl])
    }

    func testFetchParsesRowsAndETag() async throws {
        let transport = StubTransport([
            APIStub.lookup(rows: #"[{"id":11,"label":"Ben West","extra":{"role":"Architect"}}]"#, etag: #""v2""#),
        ])
        let service = LookupService(client: makeClient(transport))

        let result = try await service.fetch(companyID: 7, projectID: 99, kind: .users, etag: nil)

        guard case .rows(let response, let etag) = result else { return XCTFail("expected rows, got \(result)") }
        XCTAssertEqual(etag, #""v2""#)
        XCTAssertEqual(response.rows.map(\.label), ["Ben West"])
        XCTAssertEqual(response.rows.first?.subtitle, "Architect")
        XCTAssertFalse(response.unavailable)
        XCTAssertNil(try XCTUnwrap(transport.requests.first).request.headerFields[.ifNoneMatch])
    }

    func testForceSendsNoCache() async throws {
        let transport = StubTransport([APIStub.lookup(kind: "spec_sections")])
        let service = LookupService(client: makeClient(transport))

        _ = try await service.fetch(companyID: 7, projectID: 99, kind: .specSections, etag: nil, force: true)

        let request = try XCTUnwrap(transport.requests.first).request
        XCTAssertEqual(request.path, "/projects/99/lookups/spec_sections")
        XCTAssertEqual(request.headerFields[.cacheControl], "no-cache")
    }

    func testFetchErrorThrowsAPIFailure() async throws {
        let transport = StubTransport([
            APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is unavailable."),
        ])
        let service = LookupService(client: makeClient(transport))

        do {
            _ = try await service.fetch(companyID: 7, projectID: 99, kind: .users, etag: nil)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .server("Procore is unavailable."))
        }
    }

    func testGeneratedClientPercentEncodesIfNoneMatchWithoutMiddleware() async throws {
        // Documents why `IfNoneMatchMiddleware` exists: the backend compares the header verbatim.
        let transport = StubTransport(statuses: [.notModified])
        let service = LookupService(client: makeClient(transport))

        _ = try await service.fetch(companyID: 7, projectID: 99, kind: .users, etag: #""v1""#)

        XCTAssertEqual(transport.requests.first?.request.headerFields[.ifNoneMatch], "%22v1%22")
    }

    /// The backend sources RFI manager candidates from Procore's potential RFI managers, so the
    /// manager picker reads that list; assignees and distribution stay on the people list.
    func testRFIManagerPickerRequestsRFIManagers() async throws {
        XCTAssertEqual(RFIField.rfiManagerId.lookupKind, .rfiManagers)
        XCTAssertEqual(RFIField.assigneeIds.lookupKind, .users)
        XCTAssertEqual(RFIField.distributionIds.lookupKind, .users)
        let transport = StubTransport([APIStub.lookup(kind: "rfi_managers")])
        let service = LookupService(client: makeClient(transport))

        let kind = try XCTUnwrap(RFIField.rfiManagerId.lookupKind)
        _ = try await service.fetch(companyID: 7, projectID: 99, kind: kind, etag: nil)

        XCTAssertEqual(try XCTUnwrap(transport.requests.first).request.path, "/projects/99/lookups/rfi_managers")
    }

    func testSubtitleJoinsRoleAndCompany() {
        let row = LookupRow(id: 1, label: "Ben West", extra: .init(additionalProperties: ["role": "Architect", "company": "West Design"]))
        XCTAssertEqual(row.subtitle, "Architect · West Design")
        XCTAssertNil(LookupRow(id: 2, label: "Level 2").subtitle)
    }
}
