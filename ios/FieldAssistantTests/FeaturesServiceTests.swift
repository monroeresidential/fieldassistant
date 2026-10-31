import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class FeaturesServiceTests: XCTestCase {
    func testFeaturesDecodeAndKeyByName() async throws {
        let transport = StubTransport([
            APIStub.features(rfiAvailable: false, reason: "You have Read Only access to RFIs on this project."),
        ])
        let service = FeaturesService(client: makeClient(transport))

        let features = try await service.features(projectID: 99, companyID: 7)

        XCTAssertEqual(
            features,
            ["rfi": FeatureAvailability(available: false, reason: "You have Read Only access to RFIs on this project.")]
        )
        let request = try XCTUnwrap(transport.requests.first).request
        XCTAssertEqual(request.path, "/projects/99/features")
        XCTAssertEqual(request.headerFields[HTTPField.Name("X-Company-Id")!], "7")
        XCTAssertNil(request.headerFields[.cacheControl], "the backend's ten-minute answer is fine by default")
    }

    func testFeaturesNoCacheSendsHeader() async throws {
        let transport = StubTransport([APIStub.features()])
        let service = FeaturesService(client: makeClient(transport))

        let features = try await service.features(projectID: 99, companyID: 7, noCache: true)

        XCTAssertEqual(features["rfi"], FeatureAvailability(available: true, reason: nil))
        XCTAssertEqual(try XCTUnwrap(transport.requests.first).request.headerFields[.cacheControl], "no-cache")
    }

    func testFeaturesErrorThrowsAPIFailure() async throws {
        let transport = StubTransport([
            APIStub.error(.forbidden, code: "procore_permission_denied", message: "No access to this project."),
        ])
        let service = FeaturesService(client: makeClient(transport))

        do {
            _ = try await service.features(projectID: 99, companyID: 7)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .permissionDenied("No access to this project."))
        }
    }
}
