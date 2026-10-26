import XCTest
@testable import FieldAssistant

/// The Home sync header's status line.
final class HomeViewTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_793_000_000)

    @MainActor
    func testNeverSynced() {
        XCTAssertEqual(HomeView.syncLine(nil, now: now), "Not synced yet")
        XCTAssertEqual(HomeView.syncLine(.idle, now: now), "Not synced yet")
    }

    @MainActor
    func testSyncing() {
        XCTAssertEqual(HomeView.syncLine(.syncing, now: now), "Syncing…")
    }

    @MainActor
    func testSyncedWithinAMinuteIsJustNow() {
        XCTAssertEqual(HomeView.syncLine(.synced(now.addingTimeInterval(-59)), now: now), "Synced just now")
    }

    @MainActor
    func testSyncedEarlierIsRelative() {
        let line = HomeView.syncLine(.synced(now.addingTimeInterval(-120)), now: now)
        let formatter = RelativeDateTimeFormatter()
        formatter.unitsStyle = .short
        XCTAssertEqual(line, "Synced \(formatter.localizedString(for: now.addingTimeInterval(-120), relativeTo: now))")
        XCTAssertTrue(line.contains("2"), line)
    }

    @MainActor
    func testPermissionDenied() {
        let status = RecordSync.Status.failed(code: "procore_permission_denied", message: "No access")
        XCTAssertEqual(HomeView.syncLine(status, now: now), "No RFI access on this project")
    }

    @MainActor
    func testOtherFailure() {
        XCTAssertEqual(
            HomeView.syncLine(.failed(code: "procore_unavailable", message: "Procore is down"), now: now),
            "Sync failed, showing what you have"
        )
        XCTAssertEqual(HomeView.syncLine(.failed(code: nil, message: "Server error"), now: now), "Sync failed, showing what you have")
    }
}
