import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

final class DraftServiceTests: XCTestCase {
    private let draftID = UUID(uuidString: "7B1D2C3E-4F50-4A6B-8C7D-9E0F1A2B3C4D")!
    private let idempotencyKey = HTTPField.Name("Idempotency-Key")!
    private let companyHeader = HTTPField.Name("X-Company-Id")!

    private func localDraft() -> LocalDraft {
        LocalDraft(
            id: draftID,
            companyID: 7,
            projectID: 99,
            transcript: "Need header height at grid C",
            capturedAt: Date(timeIntervalSince1970: 1_793_000_000),
            latitude: 41.88,
            longitude: -87.63
        )
    }

    func testCreatePostsTranscriptAndIdempotentID() async throws {
        let transport = StubTransport([APIStub.draft(id: draftID)])
        let service = DraftService(client: makeClient(transport))

        let response = try await service.create(localDraft())

        XCTAssertEqual(response.id, draftID.uuidString.lowercased())
        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .post)
        XCTAssertEqual(recorded.request.path, "/drafts")
        XCTAssertEqual(recorded.request.headerFields[companyHeader], "7")
        let body = try APIStub.jsonObject(recorded.body)
        XCTAssertEqual((body["id"] as? String)?.lowercased(), draftID.uuidString.lowercased())
        XCTAssertEqual(body["feature"] as? String, "rfi")
        XCTAssertEqual(body["project_id"] as? Int, 99)
        XCTAssertEqual(body["transcript"] as? String, "Need header height at grid C")
        XCTAssertNotNil(body["captured_at"] as? String)
        let location = try XCTUnwrap(body["location"] as? [String: Any])
        XCTAssertEqual(location["lat"] as? Double, 41.88)
        XCTAssertEqual(location["lng"] as? Double, -87.63)
    }

    func testCreateOfflineThrowsOffline() async throws {
        let service = DraftService(client: makeClient(FailingTransport(error: URLError(.notConnectedToInternet))))

        do {
            _ = try await service.create(localDraft())
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .offline)
        }
    }

    func testSaveSendsPayloadAndReturnsDraft() async throws {
        let transport = StubTransport([APIStub.draft(id: draftID, status: "reviewed", subject: "Edited")])
        let service = DraftService(client: makeClient(transport))
        let payload = RFIDraftPayload(subject: "Edited", assignee_ids: [11], distribution_ids: [], _private: false, draft: false)

        let response = try await service.save(id: draftID, companyID: 7, payload: payload)

        XCTAssertEqual(response.status, .reviewed)
        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .patch)
        XCTAssertEqual(recorded.request.path, "/drafts/\(draftID.uuidString.lowercased())")
        let sent = try XCTUnwrap(APIStub.jsonObject(recorded.body)["payload"] as? [String: Any])
        XCTAssertEqual(sent["subject"] as? String, "Edited")
        XCTAssertEqual(sent["assignee_ids"] as? [Int], [11])
    }

    func testSubmitSendsIdempotencyKeyEqualToDraftID() async throws {
        let transport = StubTransport([APIStub.submit(.accepted, jobID: "job-9", draftID: draftID)])
        let service = DraftService(client: makeClient(transport))

        let start = try await service.submit(id: draftID, companyID: 7)

        guard case .running(let jobID) = start else { return XCTFail("expected running, got \(start)") }
        XCTAssertEqual(jobID, "job-9")
        let request = try XCTUnwrap(transport.requests.first).request
        XCTAssertEqual(request.method, .post)
        XCTAssertEqual(request.path, "/drafts/\(draftID.uuidString.lowercased())/submit")
        XCTAssertEqual(request.headerFields[idempotencyKey], draftID.uuidString.lowercased())
        XCTAssertEqual(request.headerFields[companyHeader], "7")
    }

    func testSubmit200IsAlreadySubmitted() async throws {
        let transport = StubTransport([
            APIStub.submit(.ok, draftID: draftID),
            APIStub.draft(id: draftID, status: "submitted"),
        ])
        let service = DraftService(client: makeClient(transport))

        let start = try await service.submit(id: draftID, companyID: 7)

        guard case .submitted(let draft) = start else { return XCTFail("expected submitted, got \(start)") }
        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(transport.requests.map(\.request.method), [.post, .get])
        XCTAssertEqual(transport.requests.last?.request.path, "/drafts/\(draftID.uuidString.lowercased())")
    }

    func testSubmit400ThrowsValidationWithServerMessage() async throws {
        let transport = StubTransport([
            APIStub.error(.badRequest, code: "bad_request", message: "Pick an assignee before sending."),
        ])
        let service = DraftService(client: makeClient(transport))

        do {
            _ = try await service.submit(id: draftID, companyID: 7)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .validation("Pick an assignee before sending."))
        }
    }

    func testWaitForJobPollsUntilSucceeded() async throws {
        let transport = StubTransport([
            APIStub.job(draftID: draftID, status: "queued"),
            APIStub.job(draftID: draftID, status: "running"),
            APIStub.job(draftID: draftID, status: "succeeded"),
            APIStub.draft(id: draftID, status: "submitted"),
        ])
        let service = DraftService(client: makeClient(transport))
        let clock = ImmediateClock()

        let draft = try await service.waitForJob(id: "job-1", draftID: draftID, companyID: 7, clock: clock)

        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(transport.requests.map(\.request.path), [
            "/jobs/job-1", "/jobs/job-1", "/jobs/job-1", "/drafts/\(draftID.uuidString.lowercased())",
        ])
        XCTAssertEqual(clock.sleeps, [.seconds(2), .seconds(2)])
    }

    func testWaitForJobStopsOnFailed() async throws {
        let transport = StubTransport([
            APIStub.job(draftID: draftID, status: "failed"),
            APIStub.draft(id: draftID, status: "failed"),
        ])
        let service = DraftService(client: makeClient(transport))

        let draft = try await service.waitForJob(id: "job-1", draftID: draftID, companyID: 7, clock: ImmediateClock())

        XCTAssertEqual(draft.status, .failed)
        XCTAssertEqual(transport.requests.count, 2)
    }

    func testWaitForJobTimeoutFallsBackToDraft() async throws {
        let polls = 30
        let transport = StubTransport(
            Array(repeating: APIStub.job(draftID: draftID, status: "running"), count: polls)
                + [APIStub.draft(id: draftID, status: "submitting")]
        )
        let service = DraftService(client: makeClient(transport))
        let clock = ImmediateClock()

        let draft = try await service.waitForJob(id: "job-1", draftID: draftID, companyID: 7, clock: clock)

        XCTAssertEqual(draft.status, .submitting)
        XCTAssertEqual(transport.requests.filter { $0.request.path == "/jobs/job-1" }.count, polls)
        XCTAssertEqual(transport.requests.last?.request.path, "/drafts/\(draftID.uuidString.lowercased())")
        XCTAssertEqual(clock.sleeps.reduce(.zero, +), .seconds(60))
    }

    func testCreateSendsAttachmentManifestInCapturedOrder() async throws {
        let transport = StubTransport([APIStub.draft(id: draftID)])
        let service = DraftService(client: makeClient(transport))
        let draft = localDraft()
        let start = Date(timeIntervalSince1970: 1_793_000_100)
        let first = LocalPhoto(fileName: "first.jpg", bytes: 1_200, createdAt: start)
        let second = LocalPhoto(fileName: "second.jpg", bytes: 3_400, createdAt: start.addingTimeInterval(30))
        // Appended out of capture order: the manifest follows `createdAt`, not the array.
        draft.photos = [second, first]

        _ = try await service.create(draft)

        let body = try APIStub.jsonObject(try XCTUnwrap(transport.requests.first).body)
        let attachments = try XCTUnwrap(body["attachments"] as? [[String: Any]])
        XCTAssertEqual(attachments.count, 2)
        XCTAssertEqual(attachments.map { ($0["id"] as? String)?.lowercased() }, [
            first.id.uuidString.lowercased(), second.id.uuidString.lowercased(),
        ])
        XCTAssertEqual(attachments.map { $0["filename"] as? String }, ["first.jpg", "second.jpg"])
        XCTAssertEqual(attachments.map { $0["content_type"] as? String }, ["image/jpeg", "image/jpeg"])
        XCTAssertEqual(attachments.map { $0["bytes"] as? Int }, [1_200, 3_400])
    }

    func testCreateWithoutPhotosOmitsAttachments() async throws {
        let transport = StubTransport([APIStub.draft(id: draftID)])
        let service = DraftService(client: makeClient(transport))

        _ = try await service.create(localDraft())

        let body = try APIStub.jsonObject(try XCTUnwrap(transport.requests.first).body)
        XCTAssertNil(body["attachments"], "a draft without photos sends no manifest")
    }

    func testUploadAttachmentPutsJPEGWithCompanyHeader() async throws {
        let attachmentID = UUID()
        let fileURL = try writeTemporaryJPEG(Data([0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02, 0xFF, 0xD9]))
        defer { try? FileManager.default.removeItem(at: fileURL) }
        let transport = StubTransport([APIStub.attachment(id: attachmentID, state: "stored", bytes: 8)])
        let service = DraftService(client: makeClient(transport))

        let attachment = try await service.uploadAttachment(
            draftID: draftID, attachmentID: attachmentID, companyID: 7, fileURL: fileURL
        )

        XCTAssertEqual(attachment.id.lowercased(), attachmentID.uuidString.lowercased())
        XCTAssertEqual(attachment.state, .stored)
        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .put)
        XCTAssertEqual(
            recorded.request.path,
            "/drafts/\(draftID.uuidString.lowercased())/attachments/\(attachmentID.uuidString.lowercased())"
        )
        XCTAssertEqual(recorded.request.headerFields[.contentType], "image/jpeg")
        XCTAssertEqual(recorded.request.headerFields[companyHeader], "7")
        XCTAssertEqual(recorded.body, [0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02, 0xFF, 0xD9])
    }

    func testUploadAttachment404ThrowsNotFound() async throws {
        let fileURL = try writeTemporaryJPEG(Data([0xFF, 0xD8, 0xFF, 0xD9]))
        defer { try? FileManager.default.removeItem(at: fileURL) }
        let transport = StubTransport([
            APIStub.error(.notFound, code: "not_found", message: "attachment is not on this draft"),
        ])
        let service = DraftService(client: makeClient(transport))

        do {
            _ = try await service.uploadAttachment(draftID: draftID, attachmentID: UUID(), companyID: 7, fileURL: fileURL)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .notFound("attachment is not on this draft"))
            XCTAssertEqual((error as? APIFailure)?.userMessage, "attachment is not on this draft")
        }
    }

    func testUploadAttachment400ThrowsValidationWithServerMessage() async throws {
        let fileURL = try writeTemporaryJPEG(Data([0xFF, 0xD8, 0xFF, 0xD9]))
        defer { try? FileManager.default.removeItem(at: fileURL) }
        let transport = StubTransport([
            APIStub.error(.badRequest, code: "bad_request", message: "photo must be a JPEG under 20 MiB"),
        ])
        let service = DraftService(client: makeClient(transport))

        do {
            _ = try await service.uploadAttachment(draftID: draftID, attachmentID: UUID(), companyID: 7, fileURL: fileURL)
            XCTFail("expected a failure")
        } catch {
            XCTAssertEqual(error as? APIFailure, .validation("photo must be a JPEG under 20 MiB"))
        }
    }

    func testDeleteAttachmentSendsDELETEAnd204Returns() async throws {
        let attachmentID = UUID()
        let transport = StubTransport(statuses: [.noContent])
        let service = DraftService(client: makeClient(transport))

        try await service.deleteAttachment(draftID: draftID, attachmentID: attachmentID, companyID: 7)

        let recorded = try XCTUnwrap(transport.requests.first)
        XCTAssertEqual(recorded.request.method, .delete)
        XCTAssertEqual(
            recorded.request.path,
            "/drafts/\(draftID.uuidString.lowercased())/attachments/\(attachmentID.uuidString.lowercased())"
        )
        XCTAssertEqual(recorded.request.headerFields[companyHeader], "7")
    }

    /// Writes `data` to a fresh file in the temporary directory and returns its URL.
    private func writeTemporaryJPEG(_ data: Data) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).jpg")
        try data.write(to: url)
        return url
    }

    func testClientDecodesFractionalAndWholeSecondDates() async throws {
        let transport = StubTransport([
            APIStub.draft(id: draftID, createdAt: "2026-10-24T17:45:12.123456Z", updatedAt: "2026-10-24T17:45:12Z"),
        ])
        let service = DraftService(client: makeClient(transport))

        let draft = try await service.get(id: draftID, companyID: 7)

        let whole = try Date("2026-10-24T17:45:12Z", strategy: .iso8601)
        XCTAssertEqual(draft.updated_at, whole)
        XCTAssertEqual(draft.created_at.timeIntervalSince(whole), 0.123, accuracy: 0.001)
    }
}

/// A clock whose sleeps return at once, recording each requested duration.
final class ImmediateClock: Clock, @unchecked Sendable {
    private let lock = NSLock()
    private var current = ManualClock.Instant(offset: .zero)
    private var recorded: [Duration] = []

    var now: ManualClock.Instant { lock.withLock { current } }
    var minimumResolution: Duration { .zero }
    var sleeps: [Duration] { lock.withLock { recorded } }

    func sleep(until deadline: ManualClock.Instant, tolerance: Duration? = nil) async throws {
        lock.withLock {
            recorded.append(current.duration(to: deadline))
            current = deadline
        }
    }
}
