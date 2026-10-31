import XCTest
@testable import FieldAssistant

final class BackgroundSyncTests: XCTestCase {
    func testShouldScheduleOnlyWithWork() {
        XCTAssertFalse(BackgroundSync.shouldSchedule(waiting: 0, pendingSubmit: 0))
        XCTAssertTrue(BackgroundSync.shouldSchedule(waiting: 1, pendingSubmit: 0))
        XCTAssertTrue(BackgroundSync.shouldSchedule(waiting: 0, pendingSubmit: 1))
        XCTAssertTrue(BackgroundSync.shouldSchedule(waiting: 2, pendingSubmit: 3))
    }

    func testIdentifiersUseBundleID() throws {
        let bundleID = try XCTUnwrap(Bundle.main.bundleIdentifier)
        XCTAssertTrue(BackgroundSync.refreshID.hasPrefix(bundleID))
        XCTAssertTrue(BackgroundSync.processingID.hasPrefix(bundleID))
        XCTAssertTrue(BackgroundSync.refreshID.hasSuffix(".draftsync.refresh"))
        XCTAssertTrue(BackgroundSync.processingID.hasSuffix(".draftsync.processing"))
        XCTAssertNotEqual(BackgroundSync.refreshID, BackgroundSync.processingID)
    }

    func testPermittedIdentifiersMatchInfoPlist() throws {
        let info = try XCTUnwrap(Bundle(for: AppState.self).infoDictionary)
        let permitted = try XCTUnwrap(info["BGTaskSchedulerPermittedIdentifiers"] as? [String])
        XCTAssertTrue(permitted.contains(BackgroundSync.refreshID))
        XCTAssertTrue(permitted.contains(BackgroundSync.processingID))
        let modes = try XCTUnwrap(info["UIBackgroundModes"] as? [String])
        XCTAssertTrue(modes.contains("fetch"))
        XCTAssertTrue(modes.contains("processing"))
    }
}
