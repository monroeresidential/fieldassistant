import XCTest
@testable import FieldAssistant

/// Due dates on the RFI list and detail, and the list row's third line: ball in court and due date,
/// whichever are present. A fixed calendar, locale and "now" pin the shape.
final class RecordFormatTests: XCTestCase {
    private let locale = Locale(identifier: "en_US")

    /// The phone's calendar: Chicago, west of UTC, so a midnight-UTC day is the evening before locally.
    private var calendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "America/Chicago")!
        return calendar
    }

    /// A day at midnight UTC, the way the mirror stores a due day.
    private func day(_ year: Int, _ month: Int, _ day: Int) -> Date {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        return utc.date(from: DateComponents(year: year, month: month, day: day))!
    }

    /// 29 October 2026, 10:00 in Chicago.
    private var now: Date {
        calendar.date(from: DateComponents(year: 2026, month: 10, day: 29, hour: 10))!
    }

    private func due(_ date: Date, now: Date? = nil) -> String {
        RecordFormat.dueDate(date, now: now ?? self.now, calendar: calendar, locale: locale)
    }

    func testDueDateInTheCurrentYearDropsTheYear() {
        XCTAssertEqual(due(day(2026, 10, 30)), "Oct 30")
    }

    func testDueDateInAnotherYearKeepsTheYear() {
        XCTAssertEqual(due(day(2027, 1, 4)), "Jan 4, 2027")
        XCTAssertEqual(due(day(2025, 12, 31)), "Dec 31, 2025")
    }

    func testDueDateIsTheUTCCalendarDay() {
        // Midnight UTC on 1 January 2027 is 31 December 2026 in Chicago; the due day is still 1 January.
        XCTAssertEqual(due(day(2027, 1, 1)), "Jan 1, 2027")
    }

    func testDueDateYearComesFromTheLocalNow() {
        // 31 December 2026 at 20:00 in Chicago is already 2027 in UTC; the phone's year is 2026.
        let newYearsEve = calendar.date(from: DateComponents(year: 2026, month: 12, day: 31, hour: 20))!
        XCTAssertEqual(due(day(2026, 12, 31), now: newYearsEve), "Dec 31")
        XCTAssertEqual(due(day(2027, 1, 2), now: newYearsEve), "Jan 2, 2027")
    }

    func testListMetaJoinsBallInCourtAndDueDate() {
        XCTAssertEqual(
            RecordFormat.listMeta(ballInCourt: "Ben West", dueDate: day(2026, 10, 30), now: now, calendar: calendar, locale: locale),
            "Ben West · Due Oct 30"
        )
    }

    func testListMetaWithOnePart() {
        XCTAssertEqual(RecordFormat.listMeta(ballInCourt: "Ben West", dueDate: nil), "Ben West")
        XCTAssertEqual(
            RecordFormat.listMeta(ballInCourt: "", dueDate: day(2027, 3, 2), now: now, calendar: calendar, locale: locale),
            "Due Mar 2, 2027"
        )
    }

    func testListMetaIsNilWhenEmpty() {
        XCTAssertNil(RecordFormat.listMeta(ballInCourt: "", dueDate: nil))
    }
}
