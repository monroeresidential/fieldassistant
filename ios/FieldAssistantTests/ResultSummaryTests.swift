import Foundation
import SwiftData
import XCTest
@testable import FieldAssistant

/// The definition list on the Result screen: which rows show and how each value reads.
@MainActor
final class ResultSummaryTests: XCTestCase {
    private var calendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.locale = Locale(identifier: "en_US")
        calendar.timeZone = TimeZone(identifier: "America/Chicago")!
        return calendar
    }

    private func date(day: Int, hour: Int, minute: Int, second: Int = 0) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: 10, day: day, hour: hour, minute: minute, second: second))!
    }

    func testResultSummaryBuildsAllRows() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        let draft = try makeDraft(in: container, dueDate: "2026-10-30", photos: 3,
                                  capturedAt: date(day: 30, hour: 14, minute: 13, second: 12),
                                  updatedAt: date(day: 30, hour: 14, minute: 14))

        let summary = ResultSummary(draft: draft, projectName: "118 W Illinois", assigneeName: "Mike Torres",
                                    now: date(day: 30, hour: 14, minute: 20), calendar: calendar)

        XCTAssertEqual(summary.rows.map(\.term), ["Project", "Assigned", "Due", "Attached", "Sent"])
        XCTAssertEqual(summary.rows.map(\.value), [
            "118 W Illinois", "Mike Torres", "Fri, Oct 30", "3 photos",
            // The system time format keeps "2:14 PM" together with a narrow no-break space, and the
            // elapsed time keeps "48 s" together with a no-break space.
            "2:14\u{202F}PM · 48\u{00A0}s from record",
        ])
        XCTAssertEqual(summary.rows.map(\.tabular), [false, false, true, false, true])
    }

    /// Review Focus 5: a draft without photos or an assignee omits the row rather than printing
    /// "0 photos" or a dash.
    func testResultSummaryOmitsMissingRows() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        let draft = try makeDraft(in: container, dueDate: nil, photos: 0,
                                  capturedAt: date(day: 30, hour: 14, minute: 13, second: 12),
                                  updatedAt: date(day: 30, hour: 14, minute: 14))

        let summary = ResultSummary(draft: draft, projectName: "118 W Illinois", assigneeName: nil,
                                    now: date(day: 30, hour: 14, minute: 20), calendar: calendar)

        XCTAssertEqual(summary.rows.map(\.term), ["Project", "Sent"])
        XCTAssertFalse(summary.rows.contains { $0.value.contains("0 photos") || $0.value == "—" || $0.value == "-" })
    }

    func testResultSummarySentMinutes() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        let draft = try makeDraft(in: container, dueDate: nil, photos: 1,
                                  capturedAt: date(day: 30, hour: 14, minute: 10, second: 55),
                                  updatedAt: date(day: 30, hour: 14, minute: 14))

        let summary = ResultSummary(draft: draft, projectName: nil, assigneeName: nil,
                                    now: date(day: 30, hour: 14, minute: 20), calendar: calendar)

        XCTAssertEqual(summary.rows.map(\.term), ["Attached", "Sent"])
        XCTAssertEqual(summary.rows.map(\.value), ["1 photo", "2:14\u{202F}PM · 3\u{00A0}min from record"])
    }

    /// Reopened another day, Sent carries the date; past an hour it drops the elapsed time.
    func testResultSummarySentOnAnEarlierDay() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        let draft = try makeDraft(in: container, dueDate: nil, photos: 0,
                                  capturedAt: date(day: 30, hour: 9, minute: 0),
                                  updatedAt: date(day: 30, hour: 14, minute: 14))

        let summary = ResultSummary(draft: draft, projectName: nil, assigneeName: nil,
                                    now: date(day: 31, hour: 8, minute: 0), calendar: calendar)

        XCTAssertEqual(summary.rows.map(\.value), ["Oct 30 at 2:14\u{202F}PM"])
    }

    // MARK: - Helpers

    private func makeDraft(in container: ModelContainer, dueDate: String?, photos: Int,
                           capturedAt: Date, updatedAt: Date) throws -> LocalDraft {
        let due = dueDate.map { #","due_date":"\#($0)""# } ?? ""
        let body = #"""
        {"id":"3c2b1a09-8f7e-4d6c-9b5a-1f2e3d4c5b6a","feature":"rfi","project_id":99,"status":"submitted",
         "payload":{"subject":"Core through beam at grid C-4","assignee_ids":[12],"distribution_ids":[],
                    "private":false,"draft":false\#(due)},
         "unresolved":[],"confidence":{},"transcript":"Core through beam",
         "attachments":[],
         "result":{"procore_id":5001,"number":"214","url":"https://sandbox.procore.com/rfis/5001"},
         "created_at":"2026-10-30T19:13:20Z","updated_at":"2026-10-30T19:14:00Z"}
        """#
        let response = try LocalDraft.decoder.decode(DraftResponse.self, from: Data(body.utf8))
        let draft = LocalDraft(companyID: 7, projectID: 99, transcript: "Core through beam", capturedAt: capturedAt)
        container.mainContext.insert(draft)
        draft.apply(response)
        draft.updatedAt = updatedAt
        draft.photos = (0..<photos).map { LocalPhoto(fileName: "photo-\($0).jpg") }
        try container.mainContext.save()
        return draft
    }
}
