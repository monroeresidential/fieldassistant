import XCTest
@testable import FieldAssistant

/// The Settings project row's line under the project name.
final class SettingsViewTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_793_000_000)
    private let locale = Locale(identifier: "en_US")
    private var calendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        calendar.locale = locale
        return calendar
    }

    @MainActor
    private func line(company: String?, refreshed: Date?) -> String? {
        SettingsView.projectLine(companyName: company, lookupsRefreshedAt: refreshed, now: now,
                                 locale: locale, calendar: calendar)
    }

    @MainActor
    func testCompanyAndRefreshTime() {
        XCTAssertEqual(line(company: "Monroe Residential", refreshed: now.addingTimeInterval(-9 * 60)),
                       "Monroe Residential · lookups refreshed 9 min. ago")
    }

    @MainActor
    func testNoFetchedAtLeavesTheCompanyAlone() {
        XCTAssertEqual(line(company: "Monroe Residential", refreshed: nil), "Monroe Residential")
    }

    @MainActor
    func testNoCompanyLeavesTheRefreshTimeAlone() {
        XCTAssertEqual(line(company: nil, refreshed: now.addingTimeInterval(-30)), "lookups refreshed just now")
        XCTAssertEqual(line(company: "", refreshed: now.addingTimeInterval(-30)), "lookups refreshed just now")
        XCTAssertNil(line(company: nil, refreshed: nil))
    }

    @MainActor
    func testUnderSixtySecondsIsJustNow() {
        XCTAssertEqual(line(company: "Monroe Residential", refreshed: now.addingTimeInterval(-59)),
                       "Monroe Residential · lookups refreshed just now")
        XCTAssertEqual(line(company: "Monroe Residential", refreshed: now),
                       "Monroe Residential · lookups refreshed just now")
        XCTAssertNotEqual(line(company: "Monroe Residential", refreshed: now.addingTimeInterval(-60)),
                          "Monroe Residential · lookups refreshed just now")
    }

    @MainActor
    func testPhotoSizeValueFormatsLongEdge() {
        XCTAssertEqual(SettingsView.photoSizeValue(PhotoLimits.longEdge, locale: locale), "2,500 px")
        XCTAssertEqual(SettingsView.photoSizeValue(800, locale: locale), "800 px")
    }
}
