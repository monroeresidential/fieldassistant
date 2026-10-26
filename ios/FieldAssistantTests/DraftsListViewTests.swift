import XCTest
@testable import FieldAssistant

/// The meta line under a draft row: feature, when it was captured, and the photo count.
final class DraftsListViewTests: XCTestCase {
    private var calendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.locale = Locale(identifier: "en_US_POSIX")
        calendar.timeZone = TimeZone(identifier: "America/Chicago")!
        return calendar
    }

    /// Monday 26 October 2026, 16:30 in Chicago.
    private var now: Date { date(day: 26, hour: 16, minute: 30) }

    private func date(day: Int, hour: Int, minute: Int) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: 10, day: day, hour: hour, minute: minute))!
    }

    /// The system time format puts a narrow no-break space (U+202F) before AM/PM, which keeps
    /// "2:14 PM" together on one line; the expectations spell it out.
    func testDraftRowMetaLine() {
        XCTAssertEqual(
            DraftRowMeta.line(feature: "rfi", capturedAt: date(day: 26, hour: 14, minute: 14), photos: 3, now: now, calendar: calendar),
            "RFI · 2:14\u{202F}PM · 3 photos"
        )
        XCTAssertEqual(
            DraftRowMeta.line(feature: "rfi", capturedAt: date(day: 25, hour: 9, minute: 5), photos: 0, now: now, calendar: calendar),
            "RFI · Yesterday"
        )
        XCTAssertEqual(
            DraftRowMeta.line(feature: "rfi", capturedAt: date(day: 26, hour: 8, minute: 0), photos: 0, now: now, calendar: calendar),
            "RFI · 8:00\u{202F}AM"
        )
    }

    func testDraftRowMetaLineOlderDraftShowsWeekdayAndOnePhoto() {
        // Thursday 22 October: older than yesterday, so the weekday name.
        XCTAssertEqual(
            DraftRowMeta.line(feature: "rfi", capturedAt: date(day: 22, hour: 10, minute: 0), photos: 1, now: now, calendar: calendar),
            "RFI · Thu · 1 photo"
        )
    }
}
