import FieldAssistantAPI
import SwiftData
import XCTest
@testable import FieldAssistant

final class RecordStoreTests: XCTestCase {
    private struct Row {
        var id: Int64
        var seq: Int64
        var number: String
        var subject = "Subject"
        var status = "open"
        var ballInCourt: String?
        var due: String?
    }

    private static func rfiJSON(_ row: Row) -> String {
        let ball = row.ballInCourt.map { #","ball_in_court":{"id":1,"name":"\#($0)"}"# } ?? ""
        let due = row.due.map { #","due_date":"\#($0)""# } ?? ""
        return #"""
        {"id":\#(row.id),"number":"\#(row.number)","subject":"\#(row.subject)","status":"\#(row.status)","draft":false,"private":false,\#
        "created_at":"2026-10-20T14:00:00Z","updated_at":"2026-10-24T09:30:00Z","assignees":[],"ball_in_courts":[],\#
        "distribution_list":[],"drawing_ids":[],"current_revision":true,"has_revisions":false,"questions":[]\#(ball)\#(due)}
        """#
    }

    private static func page(
        _ rows: [Row], deleted: [Int64] = [], nextSince: Int64, pull: String = #"{"kind":"rfi","status":"pulled"}"#
    ) throws -> RecordPage {
        let records = rows.map {
            #"{"id":\#($0.id),"kind":"rfi","updated_at":"2026-10-24T09:30:00Z","change_seq":\#($0.seq),"rfi":\#(rfiJSON($0))}"#
        }.joined(separator: ",")
        let json = #"""
        {"records":[\#(records)],"deleted_ids":\#(deleted),"next_since":\#(nextSince),"has_more":false,\#
        "synced_at":"2026-10-25T10:00:00Z","pull":\#(pull)}
        """#
        return try CachedRecord.decoder.decode(RecordPage.self, from: Data(json.utf8))
    }

    @MainActor
    private func makeStore() throws -> (RecordStore, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        return (RecordStore(context: container.mainContext), container)
    }

    @MainActor
    func testApplyInsertsUpdatesAndDeletes() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([Row(id: 1, seq: 1, number: "1"), Row(id: 2, seq: 2, number: "2")], nextSince: 2), projectID: 7, kind: .rfi)

        try store.apply(
            Self.page([Row(id: 1, seq: 3, number: "1", subject: "Changed")], deleted: [2], nextSince: 3), projectID: 7, kind: .rfi
        )

        let rows = store.records(projectID: 7, kind: .rfi, query: "", filter: .all)
        XCTAssertEqual(rows.map(\.procoreID), [1])
        XCTAssertEqual(rows.first?.subject, "Changed")
        XCTAssertEqual(store.record(projectID: 7, kind: .rfi, procoreID: 1)?.changeSeq, 3)
        XCTAssertNil(store.record(projectID: 7, kind: .rfi, procoreID: 2))
        XCTAssertEqual(store.state(projectID: 7, kind: .rfi)?.nextSince, 3)
        XCTAssertNotNil(store.state(projectID: 7, kind: .rfi)?.lastSyncedAt)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi)?.lastError)
    }

    @MainActor
    func testApplyStoresBodyAndFlatColumns() throws {
        let (store, _) = try makeStore()
        let row = Row(id: 5, seq: 1, number: "RFI-10", status: "Open", ballInCourt: "Ana Ruiz", due: "2026-11-02")
        try store.apply(Self.page([row], nextSince: 1), projectID: 7, kind: .rfi)

        let record = try XCTUnwrap(store.record(projectID: 7, kind: .rfi, procoreID: 5))
        XCTAssertEqual(record.ballInCourt, "Ana Ruiz")
        XCTAssertEqual(record.numberSort, 10)
        XCTAssertEqual(record.rfi?.subject, "Subject")
        let due = try XCTUnwrap(record.dueDate)
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try XCTUnwrap(TimeZone(identifier: "UTC"))
        XCTAssertEqual(
            calendar.dateComponents([.year, .month, .day, .hour], from: due),
            DateComponents(year: 2026, month: 11, day: 2, hour: 0)
        )
    }

    @MainActor
    func testApplyIsOneSavePerPage() throws {
        let (store, _) = try makeStore()
        let rows = (1...300).map { Row(id: Int64($0), seq: Int64($0), number: "\($0)") }

        try store.apply(Self.page(rows, nextSince: 300), projectID: 7, kind: .rfi)

        XCTAssertFalse(store.context.hasChanges)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<CachedRecord>()), 300)
    }

    @MainActor
    func testRecordsSortedByNumberDescending() throws {
        let (store, _) = try makeStore()
        let rows = ["12", "9", "RFI-10", "3"].enumerated().map { Row(id: Int64($0.offset + 1), seq: 1, number: $0.element) }
        try store.apply(Self.page(rows, nextSince: 1), projectID: 7, kind: .rfi)

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).map(\.number), ["12", "RFI-10", "9", "3"])
    }

    @MainActor
    func testQueryMatchesNumberOrSubject() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([
            Row(id: 1, seq: 1, number: "1", subject: "Header height at grid C"),
            Row(id: 2, seq: 1, number: "RFI-22", subject: "Slab edge"),
            Row(id: 3, seq: 1, number: "3", subject: "Door hardware"),
        ], nextSince: 1), projectID: 7, kind: .rfi)

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "HEADER", filter: .all).map(\.procoreID), [1])
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "rfi-22", filter: .all).map(\.procoreID), [2])
        XCTAssertTrue(store.records(projectID: 7, kind: .rfi, query: "nothing", filter: .all).isEmpty)
    }

    @MainActor
    func testStatusAndBallInCourtFilters() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([
            Row(id: 1, seq: 1, number: "1", status: "Open", ballInCourt: "Ana Ruiz"),
            Row(id: 2, seq: 1, number: "2", status: "closed", ballInCourt: "Ben West"),
            Row(id: 3, seq: 1, number: "3", status: "open"),
        ], nextSince: 1), projectID: 7, kind: .rfi)

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .status("open")).map(\.procoreID), [3, 1])
        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .status("Closed")).map(\.procoreID), [2])
        XCTAssertEqual(
            store.records(projectID: 7, kind: .rfi, query: "", filter: .ballInCourt(userName: "Ana Ruiz")).map(\.procoreID), [1]
        )
    }

    @MainActor
    func testClearAllRemovesRecordsAndState() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([Row(id: 1, seq: 1, number: "1")], nextSince: 1), projectID: 7, kind: .rfi)

        try store.clearAll()

        XCTAssertTrue(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).isEmpty)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi))
    }

    @MainActor
    func testRecordsReturnsSnapshotsThatOutliveTheirRows() throws {
        let (store, _) = try makeStore()
        let row = Row(id: 5, seq: 1, number: "5", subject: "Slab edge", status: "open", ballInCourt: "Ana Ruiz", due: "2026-11-02")
        try store.apply(Self.page([row], nextSince: 1), projectID: 7, kind: .rfi)
        let snapshot = try XCTUnwrap(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).first)

        try store.apply(Self.page([], deleted: [5], nextSince: 2), projectID: 7, kind: .rfi)

        XCTAssertNil(store.record(projectID: 7, kind: .rfi, procoreID: 5))
        XCTAssertEqual(snapshot.id, 5)
        XCTAssertEqual(snapshot.number, "5")
        XCTAssertEqual(snapshot.subject, "Slab edge")
        XCTAssertEqual(snapshot.status, "open")
        XCTAssertEqual(snapshot.ballInCourt, "Ana Ruiz")
        XCTAssertNotNil(snapshot.dueDate)
    }

    @MainActor
    func testClearDropsOneProjectsRowsAndState() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([Row(id: 1, seq: 1, number: "1"), Row(id: 2, seq: 2, number: "2")], nextSince: 2), projectID: 7, kind: .rfi)
        try store.apply(Self.page([Row(id: 3, seq: 3, number: "3")], nextSince: 3), projectID: 8, kind: .rfi)

        try store.clear(projectID: 7, kind: .rfi)

        XCTAssertFalse(store.context.hasChanges)
        XCTAssertTrue(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).isEmpty)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi))
        XCTAssertEqual(store.records(projectID: 8, kind: .rfi, query: "", filter: .all).map(\.procoreID), [3])
        XCTAssertEqual(store.state(projectID: 8, kind: .rfi)?.nextSince, 3)
    }

    @MainActor
    func testMarkFailedKeepsRowsAndRecordsError() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([Row(id: 1, seq: 1, number: "1")], nextSince: 1), projectID: 7, kind: .rfi)

        try store.markFailed(projectID: 7, kind: .rfi, code: "procore_unavailable", message: "Procore is down")

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).count, 1)
        let state = try XCTUnwrap(store.state(projectID: 7, kind: .rfi))
        XCTAssertEqual(state.lastErrorCode, "procore_unavailable")
        XCTAssertEqual(state.lastError, "Procore is down")
        XCTAssertEqual(state.nextSince, 1)
    }

    @MainActor
    func testApplyFailedPullAppliesDeletedIDsAndRecordsError() throws {
        let (store, _) = try makeStore()
        try store.apply(Self.page([
            Row(id: 1, seq: 1, number: "1"), Row(id: 2, seq: 2, number: "2"), Row(id: 3, seq: 3, number: "3"),
        ], nextSince: 3), projectID: 7, kind: .rfi)

        let failed = #"{"kind":"rfi","status":"failed","code":"procore_permission_denied","message":"No access"}"#
        try store.apply(Self.page([], deleted: [1, 2], nextSince: 4, pull: failed), projectID: 7, kind: .rfi)

        XCTAssertEqual(store.records(projectID: 7, kind: .rfi, query: "", filter: .all).map(\.procoreID), [3])
        let state = try XCTUnwrap(store.state(projectID: 7, kind: .rfi))
        XCTAssertEqual(state.lastErrorCode, "procore_permission_denied")
        XCTAssertEqual(state.lastError, "No access")
        XCTAssertEqual(state.nextSince, 4)

        // A later good page clears the error.
        try store.apply(Self.page([], nextSince: 4), projectID: 7, kind: .rfi)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi)?.lastError)
        XCTAssertNil(store.state(projectID: 7, kind: .rfi)?.lastErrorCode)
    }
}
