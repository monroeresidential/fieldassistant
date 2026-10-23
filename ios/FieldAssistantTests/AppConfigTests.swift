import XCTest
@testable import FieldAssistant

/// The test scheme builds the `Local` configuration, so these are Config/Local.xcconfig's values.
final class AppConfigTests: XCTestCase {
    func testLocalConfigValues() {
        XCTAssertEqual(AppConfig.environment, "local")
        XCTAssertEqual(AppConfig.urlScheme, "fieldassistant")
        XCTAssertFalse(AppConfig.isDev)
        XCTAssertEqual(AppConfig.apiBaseURL.path, "/v1")
    }
}
