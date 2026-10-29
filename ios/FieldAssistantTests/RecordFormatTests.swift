import XCTest
@testable import FieldAssistant

/// The RFI list row's third line: ball in court and due date, whichever are present.
final class RecordFormatTests: XCTestCase {
    /// 30 October 2026 at midnight UTC, the way the mirror stores a due day.
    private var due: Date {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar.date(from: DateComponents(year: 2026, month: 10, day: 30))!
    }

    func testListMetaJoinsBallInCourtAndDueDate() {
        XCTAssertEqual(
            RecordFormat.listMeta(ballInCourt: "Ben West", dueDate: due),
            "Ben West · Due \(RecordFormat.dueDate(due))"
        )
    }

    func testListMetaWithOnePart() {
        XCTAssertEqual(RecordFormat.listMeta(ballInCourt: "Ben West", dueDate: nil), "Ben West")
        XCTAssertEqual(RecordFormat.listMeta(ballInCourt: "", dueDate: due), "Due \(RecordFormat.dueDate(due))")
    }

    func testListMetaIsNilWhenEmpty() {
        XCTAssertNil(RecordFormat.listMeta(ballInCourt: "", dueDate: nil))
    }

    func testDueDateIsTheUTCCalendarDay() {
        XCTAssertTrue(RecordFormat.dueDate(due).contains("30"))
    }
}
