import XCTest
@testable import FieldAssistant

/// Pure helpers behind the Review screen's header tag and footer.
final class ReviewLayoutTests: XCTestCase {
    @MainActor
    func testCountTagText() {
        XCTAssertEqual(RFIReviewView.needsYou(1), "1 field needs you")
        XCTAssertEqual(RFIReviewView.needsYou(3), "3 fields need you")
    }

    func testFooterSplitsWidthOneToOnePointFour() {
        // 362 = 402 - 2 x 20 padding; 10 between the buttons.
        let widths = WeightedRow.widths(total: 362, count: 2, spacing: 10, weights: [1, 1.4])
        XCTAssertEqual(widths[0], 352 / 2.4, accuracy: 0.001)
        XCTAssertEqual(widths[1], 352 * 1.4 / 2.4, accuracy: 0.001)
    }

    func testChildrenPastTheWeightsWeighOne() {
        XCTAssertEqual(WeightedRow.widths(total: 30, count: 3, spacing: 0, weights: [1]), [10, 10, 10])
        XCTAssertEqual(WeightedRow.widths(total: 30, count: 0, spacing: 10, weights: [1]), [])
    }
}
