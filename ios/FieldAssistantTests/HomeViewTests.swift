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

    /// Spec section 3: a project the user cannot see shows Procore's reason, not the generic line.
    @MainActor
    func testPermissionDeniedShowsProcoreReason() {
        let status = RecordSync.Status.failed(code: "procore_permission_denied", message: "You do not have access to RFIs on this project")
        XCTAssertEqual(HomeView.syncLine(status, now: now), "You do not have access to RFIs on this project")
    }

    @MainActor
    func testPermissionDeniedWithoutReasonKeepsGenericLine() {
        let status = RecordSync.Status.failed(code: "procore_permission_denied", message: "  ")
        XCTAssertEqual(HomeView.syncLine(status, now: now), "No RFI access on this project")
    }

    @MainActor
    func testNoAccessMessagesFallBackWhenReasonIsMissing() {
        XCTAssertEqual(HomeView.noAccessMessage("Permission denied"), "Permission denied")
        XCTAssertEqual(HomeView.noAccessMessage(nil), "No RFI access on this project")
        XCTAssertEqual(HomeView.noAccessMessage(""), "No RFI access on this project")
        XCTAssertEqual(RFIRecordListView.noAccessMessage(" Permission denied "), "Permission denied")
        XCTAssertEqual(RFIRecordListView.noAccessMessage(nil), "Your Procore account cannot see RFIs on this project.")
        XCTAssertEqual(RFIRecordListView.noAccessMessage("\n"), "Your Procore account cannot see RFIs on this project.")
    }

    @MainActor
    func testOtherFailure() {
        XCTAssertEqual(
            HomeView.syncLine(.failed(code: "procore_unavailable", message: "Procore is down"), now: now),
            "Sync failed, showing what you have"
        )
        XCTAssertEqual(HomeView.syncLine(.failed(code: nil, message: "Server error"), now: now), "Sync failed, showing what you have")
    }

    /// The title comes from the route alone, so it is right on the first push from Drafts, when
    /// the destination is built before HomeView's path holds the new route.
    @MainActor
    func testReviewBackLinkNamesTheScreenItPopsTo() {
        let id = UUID()
        // Home: after a build (`onBuilt` sets `[.review(id)]`) and from a Recent row.
        XCTAssertEqual(HomeView.backTitle(for: .review(id)), "Home")
        XCTAssertEqual(HomeView.backTitle(for: RecentDraftsView.route(for: draftWaitingReview(id)) { _, _ in false }), "Home")
        // Drafts: the list's route goes through `openedFromDrafts`, first push and later ones alike.
        let fromDrafts = HomeView.openedFromDrafts(.review(id))
        XCTAssertEqual(fromDrafts, .review(id, from: .drafts))
        XCTAssertEqual(HomeView.backTitle(for: fromDrafts), "Drafts")
    }

    @MainActor
    func testOpenedFromDraftsLeavesOtherRoutesAlone() {
        let id = UUID()
        XCTAssertEqual(HomeView.openedFromDrafts(.result(id)), .result(id))
        XCTAssertEqual(HomeView.openedFromDrafts(.rfi(7)), .rfi(7))
    }

    @MainActor
    func testRecordHintFollowsAvailability() {
        XCTAssertEqual(HomeView.recordHint(FeatureAvailability(available: true, reason: nil)), "Under a minute, start to Procore")
        XCTAssertEqual(
            HomeView.recordHint(FeatureAvailability(available: false, reason: "Read only on RFIs.")),
            "RFIs are not available on this project"
        )
    }

    /// Result's New RFI follows the same availability as Home's record button.
    @MainActor
    func testNewRFIHintFollowsAvailability() {
        XCTAssertNil(HomeView.newRFIBlockedHint(FeatureAvailability(available: true, reason: nil)))
        XCTAssertEqual(
            HomeView.newRFIBlockedHint(FeatureAvailability(available: false, reason: "Read only on RFIs.")),
            "RFIs are not available on this project"
        )
    }

    private func draftWaitingReview(_ id: UUID) -> LocalDraft {
        LocalDraft(id: id, companyID: 1, projectID: 7, status: .draft, transcript: "Beam at C-4")
    }
}
