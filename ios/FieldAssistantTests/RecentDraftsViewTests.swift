import SwiftData
import XCTest
@testable import FieldAssistant

/// Where a Recent row sends the user: a submitted draft opens the synced RFI when the mirror
/// holds it, otherwise its Result screen; every other status opens Review.
final class RecentDraftsViewTests: XCTestCase {
    @MainActor
    func testSubmittedDraftOpensSyncedRFI() {
        let draft = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitted, transcript: "Header size")
        draft.resultProcoreID = 183206
        var asked: [(Int64, Int64)] = []
        let route = RecentDraftsView.route(for: draft) { procoreID, projectID in
            asked.append((procoreID, projectID))
            return true
        }
        XCTAssertEqual(route, .rfi(183206))
        XCTAssertEqual(asked.count, 1)
        XCTAssertEqual(asked.first?.0, 183206)
        XCTAssertEqual(asked.first?.1, 7)
    }

    @MainActor
    func testSubmittedDraftNotYetSyncedOpensResult() {
        let draft = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitted, transcript: "Header size")
        draft.resultProcoreID = 183206
        XCTAssertEqual(RecentDraftsView.route(for: draft) { _, _ in false }, .result(draft.id))
    }

    @MainActor
    func testSubmittedDraftWithoutProcoreIDOpensResult() {
        let draft = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitted, transcript: "Header size")
        XCTAssertEqual(RecentDraftsView.route(for: draft) { _, _ in XCTFail("mirror must not be asked"); return true }, .result(draft.id))
    }

    @MainActor
    func testOtherStatusesOpenReview() {
        for status in [LocalDraftStatus.waiting, .draft, .submitting, .failed] {
            let draft = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: status, transcript: "Twisted column")
            draft.resultProcoreID = 1
            XCTAssertEqual(RecentDraftsView.route(for: draft) { _, _ in true }, .review(draft.id), "\(status)")
        }
    }

    /// A queued draft's meta line says whether it waits for signal, is sending, or is queued.
    func testRowMetaQueuedStates() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.locale = Locale(identifier: "en_US_POSIX")
        calendar.timeZone = TimeZone(identifier: "America/Chicago")!
        let now = calendar.date(from: DateComponents(year: 2026, month: 10, day: 26, hour: 16, minute: 30))!
        let yesterday = calendar.date(from: DateComponents(year: 2026, month: 10, day: 25, hour: 9, minute: 5))!
        func line(_ queued: QueuedState?) -> String {
            DraftRowMeta.line(feature: "rfi", capturedAt: yesterday, photos: 2, queued: queued, now: now, calendar: calendar)
        }

        XCTAssertEqual(line(.waitingForSignal), "RFI · Yesterday · 2 photos · Queued · waiting for signal")
        XCTAssertEqual(line(.sending), "RFI · Yesterday · 2 photos · Queued · sending")
        XCTAssertEqual(line(.queued), "RFI · Yesterday · 2 photos · Queued")
        XCTAssertEqual(line(nil), "RFI · Yesterday · 2 photos")
    }

    /// Which queued state a row shows: no signal wins, then a run in progress, else plain Queued.
    func testQueuedStateForRow() {
        XCTAssertNil(QueuedState(submitPending: false, reachable: false, running: true))
        XCTAssertEqual(QueuedState(submitPending: true, reachable: false, running: true), .waitingForSignal)
        XCTAssertEqual(QueuedState(submitPending: true, reachable: true, running: true), .sending)
        XCTAssertEqual(QueuedState(submitPending: true, reachable: true, running: false), .queued)
    }

    @MainActor
    func testApplyRecordsTheProcoreID() throws {
        let draft = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, transcript: "Header size")
        let json = """
        {"id":"\(draft.id.uuidString)","feature":"rfi","project_id":7,"status":"submitted",
         "payload":{"subject":"Header size over garage door","assignee_ids":[],"distribution_ids":[],"private":false,"draft":false},
         "unresolved":[],"confidence":{},"transcript":"Header size","model_version":"test-model",
         "attachments":[],
         "result":{"procore_id":183206,"number":"1","url":"https://sandbox.procore.com/rfi/183206"},
         "created_at":"2026-10-26T15:00:00Z","updated_at":"2026-10-26T15:01:02.345Z"}
        """
        let response = try LocalDraft.decoder.decode(DraftResponse.self, from: Data(json.utf8))
        draft.apply(response)
        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(draft.resultProcoreID, 183206)
        XCTAssertEqual(draft.resultNumber, "1")
    }
}
