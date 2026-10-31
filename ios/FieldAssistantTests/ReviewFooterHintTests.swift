import XCTest
@testable import FieldAssistant

/// What the Review footer says about a disabled Submit.
final class ReviewFooterHintTests: XCTestCase {
    func testNoBlockersNoHint() {
        XCTAssertNil(ReviewFooterHint.text(blockingCount: 0, failedPhotoCount: 0))
    }

    func testFieldFlagsKeepTheFieldHint() {
        XCTAssertEqual(ReviewFooterHint.text(blockingCount: 2, failedPhotoCount: 0), "Resolve the flagged fields first")
    }

    func testOnlyFailedPhotosSaysRemoveThePhoto() {
        XCTAssertEqual(ReviewFooterHint.text(blockingCount: 1, failedPhotoCount: 1), "Remove the failed photo first.")
    }

    func testFieldFlagsAlongsideFailedPhotosKeepTheFieldHint() {
        XCTAssertEqual(ReviewFooterHint.text(blockingCount: 3, failedPhotoCount: 1), "Resolve the flagged fields first")
    }
}
