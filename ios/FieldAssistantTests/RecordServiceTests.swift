import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class RecordServiceTests: XCTestCase {
    private static let rfiJSON = #"""
    {"id":501,"number":"12","subject":"Header height at grid C","status":"open","draft":false,"private":false,\#
    "created_at":"2026-10-20T14:00:00Z","updated_at":"2026-10-24T09:30:00Z","assignees":[],"ball_in_courts":[],\#
    "distribution_list":[],"drawing_ids":[],"current_revision":true,"has_revisions":false,"due_date":"2026-11-02",\#
    "questions":[{"id":1,"plain_text_body":"What is the header height?","attachments":[],\#
    "answers":[{"id":2,"plain_text_body":"Use 7'-0\".","attachments":[{"id":3,"name":"col.jpg","url":"/v1/projects/7/records/rfi/501/attachments/3"}]}]}]}
    """#

    private static func page(records: String, deleted: String = "[]") -> (HTTPResponse, HTTPBody?) {
        APIStub.json(.ok, #"""
        {"records":\#(records),"deleted_ids":\#(deleted),"next_since":43,"has_more":false,\#
        "synced_at":"2026-10-25T10:00:00Z","pull":{"kind":"rfi","status":"pulled","pulled_at":"2026-10-25T10:00:00Z"}}
        """#)
    }

    func testPageSendsSinceLimitAndNoCache() async throws {
        let transport = StubTransport([Self.page(records: "[]")])
        let service = RecordService(client: makeClient(transport))

        _ = try await service.page(companyID: 3, projectID: 7, kind: .rfi, since: 42, force: true)

        let request = try XCTUnwrap(transport.requests.first).request
        let path = try XCTUnwrap(request.path)
        XCTAssertTrue(path.hasPrefix("/projects/7/records/rfi?"), path)
        XCTAssertTrue(path.contains("since=42"), path)
        XCTAssertTrue(path.contains("limit=500"), path)
        XCTAssertEqual(request.headerFields[.cacheControl], "no-cache")
        XCTAssertEqual(request.headerFields[HTTPField.Name("X-Company-Id")!], "3")
    }

    func testPageOmitsNoCacheWhenNotForced() async throws {
        let transport = StubTransport([Self.page(records: "[]")])
        let service = RecordService(client: makeClient(transport))

        _ = try await service.page(companyID: 3, projectID: 7, kind: .rfi, since: 0, force: false)

        XCTAssertNil(try XCTUnwrap(transport.requests.first).request.headerFields[.cacheControl])
    }

    func testPageDecodesRecordsAndDeletedIDs() async throws {
        let envelope = #"[{"id":501,"kind":"rfi","updated_at":"2026-10-24T09:30:00Z","change_seq":42,"rfi":\#(Self.rfiJSON)}]"#
        let transport = StubTransport([Self.page(records: envelope, deleted: "[9]")])
        let service = RecordService(client: makeClient(transport))

        let page = try await service.page(companyID: 3, projectID: 7, kind: .rfi, since: 0, force: false)

        XCTAssertEqual(page.deleted_ids, [9])
        XCTAssertEqual(page.next_since, 43)
        XCTAssertFalse(page.has_more)
        XCTAssertEqual(page.pull.status, .pulled)
        let record = try XCTUnwrap(page.records.first)
        XCTAssertEqual(record.change_seq, 42)
        let rfi = try XCTUnwrap(record.rfi)
        XCTAssertEqual(rfi.subject, "Header height at grid C")
        let answer = try XCTUnwrap(rfi.questions.first?.answers.first)
        XCTAssertEqual(answer.plain_text_body, "Use 7'-0\".")
        XCTAssertEqual(answer.attachments.first?.url, "/v1/projects/7/records/rfi/501/attachments/3")
    }

    func testDecodeRFIRecordWithSparseBody() throws {
        let body = #"""
        {"id":1,"number":"1","subject":"S","status":"draft","draft":true,"private":false,\#
        "created_at":"2026-10-20T14:00:00Z","updated_at":"2026-10-20T14:00:00Z","assignees":[],"ball_in_courts":[],\#
        "distribution_list":[],"drawing_ids":[],"current_revision":true,"has_revisions":false,"questions":[]}
        """#
        let record = CachedRecord(
            projectID: 7, kind: .rfi, procoreID: 1, changeSeq: 1,
            updatedAt: Date(timeIntervalSince1970: 0), bodyData: Data(body.utf8),
            number: "1", subject: "S", status: "draft", ballInCourt: "", dueDate: nil
        )

        let rfi = try XCTUnwrap(record.rfi)
        XCTAssertEqual(rfi.assignees.count, 0)
        XCTAssertNil(rfi.due_date)
        XCTAssertNil(rfi.ball_in_court)
        XCTAssertNil(rfi.location)
        XCTAssertNil(rfi.created_by)
        XCTAssertEqual(record.key, "7/rfi/1")
        XCTAssertNil(CachedRecord(
            projectID: 7, kind: .rfi, procoreID: 1, changeSeq: 1, updatedAt: Date(),
            bodyData: Data("nope".utf8), number: "1", subject: "S", status: "", ballInCourt: "", dueDate: nil
        ).rfi)
    }

    func testRFIBodyWithFractionalSecondsDecodesAndRoundTrips() throws {
        let body = Self.rfiJSON.replacingOccurrences(of: "2026-10-24T09:30:00Z", with: "2026-10-24T09:30:00.123Z")
        let decoded = try CachedRecord.decoder.decode(RFIRecord.self, from: Data(body.utf8))
        let record = CachedRecord(
            projectID: 7, kind: .rfi, procoreID: 501, changeSeq: 1, updatedAt: Date(),
            bodyData: Data(body.utf8), number: "12", subject: "", status: "", ballInCourt: "", dueDate: nil
        )
        XCTAssertEqual(record.rfi, decoded)

        let stored = CachedRecord(
            projectID: 7, kind: .rfi, procoreID: 501, changeSeq: 1, updatedAt: Date(),
            bodyData: try CachedRecord.encoder.encode(decoded), number: "12", subject: "", status: "", ballInCourt: "", dueDate: nil
        )
        XCTAssertEqual(stored.rfi, decoded)
    }

    func testNumberSortTakesNumericPart() {
        func sort(_ number: String) -> Int64 {
            CachedRecord(
                projectID: 1, kind: .rfi, procoreID: 1, changeSeq: 1, updatedAt: Date(),
                bodyData: Data(), number: number, subject: "", status: "", ballInCourt: "", dueDate: nil
            ).numberSort
        }
        XCTAssertEqual(sort("12"), 12)
        XCTAssertEqual(sort("A-007"), 7)
        XCTAssertEqual(sort("none"), 0)
    }

    func testSyncPostsKindsAndFull() async throws {
        let transport = StubTransport([APIStub.json(.ok, #"{"pulls":[{"kind":"rfi","status":"failed","code":"procore_unavailable","message":"down"}]}"#)])
        let service = RecordService(client: makeClient(transport))

        let pulls = try await service.sync(companyID: 3, projectID: 7, kinds: [.rfi], full: true)

        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .post)
        XCTAssertEqual(recorded.request.path, "/projects/7/sync")
        let body = try APIStub.jsonObject(recorded.body)
        XCTAssertEqual(body["kinds"] as? [String], ["rfi"])
        XCTAssertEqual(body["full"] as? Bool, true)
        XCTAssertEqual(pulls.first?.status, .failed)
        XCTAssertEqual(pulls.first?.code, "procore_unavailable")
    }

    func testAttachmentDataReadsFilename() async throws {
        var response = HTTPResponse(status: .ok, headerFields: [.contentType: "application/octet-stream"])
        response.headerFields[.contentDisposition] = #"attachment; filename="col.jpg""#
        let transport = StubTransport([(response, HTTPBody([1, 2, 3]))])
        let service = RecordService(client: makeClient(transport))

        let result = try await service.attachmentData(companyID: 3, projectID: 7, rfiID: 501, attachmentID: 3)

        XCTAssertEqual(result.0, Data([1, 2, 3]))
        XCTAssertEqual(result.filename, "col.jpg")
        XCTAssertEqual(transport.requests.first?.request.path, "/projects/7/records/rfi/501/attachments/3")
    }

    func testPageErrorThrowsAPIFailure() async throws {
        let transport = StubTransport([APIStub.error(.forbidden, code: "forbidden_scope", message: "No access.")])
        let service = RecordService(client: makeClient(transport))

        do {
            _ = try await service.page(companyID: 3, projectID: 7, kind: .rfi, since: 0, force: false)
            XCTFail("expected a failure")
        } catch {
            XCTAssertTrue(error is APIFailure)
        }
    }
}
