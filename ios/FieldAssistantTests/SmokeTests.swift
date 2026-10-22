import XCTest
import FieldAssistantAPI
@testable import FieldAssistant

final class SmokeTests: XCTestCase {
    func testGeneratedClientTypesExist() {
        _ = Components.Schemas.Company(id: 1, name: "Monroe")
        XCTAssertEqual(AppConfig.apiBaseURL.path, "/v1")
    }
}
