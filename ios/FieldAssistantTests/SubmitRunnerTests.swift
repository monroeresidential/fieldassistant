import FieldAssistantAPI
import Foundation
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

@MainActor
final class SubmitRunnerTests: XCTestCase {
    private let draftID = UUID(uuidString: "5A4B3C2D-1E0F-4A9B-8C7D-6E5F4A3B2C1D")!
    private let photoA = UUID(uuidString: "0A000000-0000-4000-8000-00000000000A")!
    private let photoB = UUID(uuidString: "0B000000-0000-4000-8000-00000000000B")!
    /// Kept alive for the test: `ModelContext` does not own its container.
    private var container: ModelContainer?

    private var draftPath: String { "/drafts/\(draftID.uuidString.lowercased())" }

    private func photoPath(_ id: UUID) -> String { draftPath + "/attachments/\(id.uuidString.lowercased())" }

    override func tearDown() {
        DraftFiles.removeAll(for: draftID)
        container = nil
        super.tearDown()
    }

    func testRunOrderSavePhotosSubmitPoll() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.draft(id: draftID, status: "reviewed")),
            .response(APIStub.attachment(id: photoA, filename: "a.jpg", state: "stored", bytes: 11)),
            .response(APIStub.attachment(id: photoB, filename: "b.jpg", state: "stored", bytes: 12)),
            .response(APIStub.submit(.accepted, jobID: "job-9", draftID: draftID)),
            .response(APIStub.job(id: "job-9", draftID: draftID, status: "succeeded")),
            .response(APIStub.draft(id: draftID, status: "submitted")),
        ])
        let (runner, store, draft) = try makeRunner(transport, photos: [(photoB, "b.jpg", 2), (photoA, "a.jpg", 1)])
        draft.pendingPayloadData = try LocalDraft.encoder.encode(try XCTUnwrap(draft.server).payload)
        try store.save()

        let outcome = await runner.run(draft)

        guard case .accepted(let response?) = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(response.status, .submitted)
        XCTAssertEqual(transport.requests.map(\.method), [.patch, .put, .put, .post, .get, .get])
        XCTAssertEqual(transport.requests.map(\.path), [
            draftPath, photoPath(photoA), photoPath(photoB), draftPath + "/submit", "/jobs/job-9", draftPath,
        ])
        XCTAssertNil(draft.pendingPayloadData, "a saved payload is cleared")
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .submitted)
        XCTAssertFalse(runner.isRunning(draftID))
    }

    func testRunSkipsStoredPhotos() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.attachment(id: photoB, filename: "b.jpg", state: "stored", bytes: 12)),
            .response(APIStub.submit(.ok, draftID: draftID)),
            .response(APIStub.draft(id: draftID, status: "submitted")),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1), (photoB, "b.jpg", 2)])
        photo(photoA, in: draft).uploadState = .stored

        let outcome = await runner.run(draft)

        guard case .accepted = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(transport.requests.map(\.method), [.put, .post, .get])
        XCTAssertEqual(transport.requests.first?.path, photoPath(photoB))
        XCTAssertEqual(photo(photoB, in: draft).bytes, 12, "the server's byte count is kept")
    }

    func testRunStopsOnOfflineAndKeepsPending() async throws {
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1), (photoB, "b.jpg", 2)])

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .stillPending(.offline))
        XCTAssertEqual(transport.requests.count, 1, "nothing more is sent without signal")
        XCTAssertTrue(draft.submitPending)
        XCTAssertEqual(draft.status, .submitting)
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .pending)
        XCTAssertNil(draft.errorMessage)
    }

    func testRunRefusal400PersistsFailedWithMessageAndFetchesDraft() async throws {
        let message = "draft has unresolved fields: assignee_ids"
        let unresolved = #"[{"field":"assignee_ids","reason":"not_found","candidates":[],"blocking":true}]"#
        let transport = ScriptedTransport([
            .response(APIStub.error(.badRequest, code: "bad_request", message: message)),
            .response(draftJSON(status: "reviewed", unresolved: unresolved)),
        ])
        let (runner, _, draft) = try makeRunner(transport)

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .refused(.validation(message)))
        XCTAssertEqual(transport.requests.map(\.method), [.post, .get])
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .failed)
        XCTAssertEqual(draft.errorMessage, message)
        XCTAssertEqual(draft.server?.unresolved.map(\.field), ["assignee_ids"], "the server's flags are kept on the phone")
        let id = draftID
        let reloaded = try XCTUnwrap(ModelContext(try XCTUnwrap(container)).fetch(
            FetchDescriptor<LocalDraft>(predicate: #Predicate { $0.id == id })
        ).first)
        XCTAssertEqual(reloaded.status, .failed, "the refusal is saved")
        XCTAssertFalse(reloaded.submitPending)
    }

    func testRunPhotoPut400MarksPhotoFailedAndStops() async throws {
        let message = "photo must be a JPEG under 20 MiB"
        let transport = ScriptedTransport([
            .response(APIStub.error(.badRequest, code: "bad_request", message: message)),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1), (photoB, "b.jpg", 2)])

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .photoFailed(photo(photoA, in: draft)))
        XCTAssertEqual(transport.requests.map(\.method), [.put], "no further upload and no POST")
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .failed)
        XCTAssertEqual(photo(photoA, in: draft).failureMessage, message)
        XCTAssertEqual(photo(photoB, in: draft).uploadState, .pending)
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .failed)
        XCTAssertEqual(draft.errorMessage, message)
    }

    func testRunPhotoPut404MarksPhotoFailed() async throws {
        let message = "attachment is not on this draft"
        let transport = ScriptedTransport([
            .response(APIStub.error(.notFound, code: "not_found", message: message)),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .photoFailed(photo(photoA, in: draft)))
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .failed)
        XCTAssertEqual(photo(photoA, in: draft).failureMessage, message)
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .failed)
        XCTAssertEqual(draft.errorMessage, message)
    }

    func testSubmit200AlreadySubmittedIsAccepted() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.submit(.ok, draftID: draftID)),
            .response(draftJSON(status: "submitted", result: #"{"procore_id":501,"number":"214"}"#)),
        ])
        let (runner, _, draft) = try makeRunner(transport)
        let leftover = try DraftFiles.directory(for: draftID).appending(path: "audio.m4a")
        try Data("audio".utf8).write(to: leftover)

        let outcome = await runner.run(draft)

        guard case .accepted(let response?) = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(response.status, .submitted)
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(draft.resultNumber, "214")
        XCTAssertFalse(FileManager.default.fileExists(atPath: leftover.path(percentEncoded: false)), "files are removed once Procore has the RFI")
    }

    func testPollErrorAfter202ClearsPendingAndReturnsAccepted() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.submit(.accepted, jobID: "job-5", draftID: draftID)),
            .failure(URLError(.notConnectedToInternet)),
        ])
        let (runner, _, draft) = try makeRunner(transport)

        let outcome = await runner.run(draft)

        guard case .accepted(let response?) = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(response.status, .submitting, "the draft as it stands: queued on the server")
        XCTAssertFalse(draft.submitPending, "the server owns the draft once it answered 202")
        XCTAssertEqual(draft.status, .submitting)
    }

    func testRelaunchMidQueueResumesWithoutReupload() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.attachment(id: photoA, filename: "a.jpg", state: "stored", bytes: 11)),
            .failure(URLError(.networkConnectionLost)),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1), (photoB, "b.jpg", 2)])

        let first = await runner.run(draft)
        XCTAssertEqual(first, .stillPending(.offline))

        // A relaunch: a fresh context over the same store, and a new runner.
        let relaunched = DraftStore(context: ModelContext(try XCTUnwrap(container)))
        let reloaded = try XCTUnwrap(relaunched.draft(id: draftID))
        XCTAssertTrue(reloaded.submitPending)
        XCTAssertEqual(reloaded.status, .submitting)
        let client = makeClient(transport)
        let second = SubmitRunner(store: relaunched, service: { DraftService(client: client) })
        transport.append(.response(APIStub.attachment(id: photoB, filename: "b.jpg", state: "stored", bytes: 12)))
        transport.append(.response(APIStub.submit(.ok, draftID: draftID)))
        transport.append(.response(APIStub.draft(id: draftID, status: "submitted")))

        let outcome = await second.run(reloaded)

        guard case .accepted = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(transport.requests.map(\.method), [.put, .put, .put, .post, .get])
        XCTAssertEqual(transport.requests.dropFirst(2).map(\.path), [photoPath(photoB), draftPath + "/submit", draftPath],
                       "the stored photo is not sent again")
        XCTAssertFalse(reloaded.submitPending)
        XCTAssertEqual(reloaded.status, .submitted)
    }

    func testRunSaveNotFoundRefusesDraft() async throws {
        let message = "draft not found"
        let transport = ScriptedTransport([
            .response(APIStub.error(.notFound, code: "not_found", message: message)),
            .response(APIStub.error(.notFound, code: "not_found", message: message)),
        ])
        let (runner, _, draft) = try makeRunner(transport)
        draft.pendingPayloadData = try LocalDraft.encoder.encode(try XCTUnwrap(draft.server).payload)

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .refused(.notFound(message)), "a draft the server lost is not retried every pass")
        XCTAssertEqual(transport.requests.map(\.method), [.patch, .get])
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .failed)
        XCTAssertEqual(draft.errorMessage, message)
    }

    func testReauthDuringRunKeepsQueued() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.error(.unauthorized, code: "reauth_required", message: "Sign in again")),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .stillPending(.reauthRequired))
        XCTAssertTrue(draft.submitPending, "the RFI sends after the next sign-in")
        XCTAssertEqual(draft.status, .submitting)
        XCTAssertNil(draft.errorMessage)
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .pending)
    }

    func testCancelAllEndsRunAndKeepsQueued() async throws {
        let transport = ScriptedTransport([.hang])
        let (runner, _, draft) = try makeRunner(transport)
        let run = Task { await runner.run(draft) == .stillPending(.server) }
        for _ in 0..<500 where transport.requests.isEmpty {
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTAssertTrue(runner.isRunning(draftID))

        runner.cancelAll()

        XCTAssertFalse(runner.isRunning(draftID))
        let endedPending = await run.value
        XCTAssertTrue(endedPending)
        XCTAssertTrue(draft.submitPending)
        XCTAssertEqual(draft.status, .submitting)
        XCTAssertEqual(transport.requests.count, 1)
    }

    func testPollErrorWithoutServerCopyIsAccepted() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.submit(.accepted, jobID: "job-5", draftID: draftID)),
            .failure(URLError(.notConnectedToInternet)),
        ])
        let (runner, _, draft) = try makeRunner(transport)
        draft.serverData = nil

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .accepted(nil))
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .submitting)
    }

    func testRunMissingPhotoFileMarksPhotoFailed() async throws {
        let transport = ScriptedTransport([])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])
        try FileManager.default.removeItem(at: DraftFiles.photoURL(draftID: draftID, fileName: "a.jpg"))

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .photoFailed(photo(photoA, in: draft)))
        XCTAssertTrue(transport.requests.isEmpty)
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .failed)
        XCTAssertEqual(photo(photoA, in: draft).failureMessage, "Photo file is missing on this phone.")
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .failed)
    }

    func testRunUnreadablePhotoFileStaysPending() async throws {
        let transport = ScriptedTransport([])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])
        let fileURL = DraftFiles.photoURL(draftID: draftID, fileName: "a.jpg")
        try FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: fileURL.path(percentEncoded: false))
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: fileURL.path(percentEncoded: false)) }

        let outcome = await runner.run(draft)

        XCTAssertEqual(outcome, .stillPending(.server), "a read error other than a missing file may pass")
        XCTAssertTrue(transport.requests.isEmpty)
        XCTAssertEqual(photo(photoA, in: draft).uploadState, .pending)
        XCTAssertTrue(draft.submitPending)
        XCTAssertNil(draft.errorMessage)
    }

    func testRemovedPhotoDeletedBeforeSubmit() async throws {
        let transport = ScriptedTransport([
            .response((HTTPResponse(status: .noContent), nil)),
            .response(APIStub.draft(id: draftID, status: "reviewed")),
            .response(APIStub.submit(.ok, draftID: draftID)),
            .response(APIStub.draft(id: draftID, status: "submitted")),
        ])
        let (runner, store, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1), (photoB, "b.jpg", 2)])
        photo(photoA, in: draft).uploadState = .removed
        photo(photoB, in: draft).uploadState = .stored
        try store.save()

        let outcome = await runner.run(draft)

        guard case .accepted = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(transport.requests.map(\.method), [.delete, .get, .post, .get], "DELETE, read back, then submit")
        XCTAssertEqual(transport.requests.first?.path, photoPath(photoA))
        XCTAssertEqual(draft.photos.map(\.id), [photoB], "the removed photo leaves the draft")
        let removed = photoA
        let rows = try XCTUnwrap(container).mainContext.fetch(FetchDescriptor<LocalPhoto>(predicate: #Predicate { $0.id == removed }))
        XCTAssertTrue(rows.isEmpty, "the removed photo's row is deleted")
    }

    func testLockedPUTIsTreatedAsAccepted() async throws {
        // An earlier POST landed but its answer was lost: the server refuses the upload as locked.
        let transport = ScriptedTransport([
            .response(APIStub.error(.badRequest, code: "bad_request", message: SubmitRunner.lockedMessage)),
            .response(draftJSON(status: "submitted", result: #"{"procore_id":501,"number":"214"}"#)),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])

        let outcome = await runner.run(draft)

        guard case .accepted(let response?) = outcome else { return XCTFail("expected accepted, got \(outcome)") }
        XCTAssertEqual(response.status, .submitted)
        XCTAssertEqual(transport.requests.map(\.method), [.put, .get], "no POST after a locked upload")
        XCTAssertEqual(transport.requests.map(\.path), [photoPath(photoA), draftPath])
        XCTAssertFalse(draft.submitPending)
        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(draft.resultNumber, "214")
        XCTAssertNotEqual(photo(photoA, in: draft).uploadState, .failed, "a locked upload is not a failed photo")
    }

    func testServerFailureRecordsQueueFailureAndProgressClearsIt() async throws {
        let transport = ScriptedTransport([
            .response(APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is unavailable")),
            .response(APIStub.attachment(id: photoA, filename: "a.jpg", state: "stored", bytes: 11)),
            .failure(URLError(.notConnectedToInternet)),
        ])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])

        let first = await runner.run(draft)

        XCTAssertEqual(first, .stillPending(.server))
        XCTAssertTrue(draft.submitPending)
        XCTAssertNotNil(draft.lastQueueFailureAt, "a server failure is recorded for the row")
        XCTAssertNotNil(draft.lastQueueFailureMessage)

        // The next run uploads the photo (progress), then loses the signal before the POST.
        let second = await runner.run(draft)

        XCTAssertEqual(second, .stillPending(.offline))
        XCTAssertNil(draft.lastQueueFailureAt, "progress clears the failure")
        XCTAssertNil(draft.lastQueueFailureMessage)
        XCTAssertTrue(draft.submitPending)
    }

    func testOfflineRunRecordsNoQueueFailure() async throws {
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let (runner, _, draft) = try makeRunner(transport, photos: [(photoA, "a.jpg", 1)])

        _ = await runner.run(draft)

        XCTAssertNil(draft.lastQueueFailureAt, "no signal is not the server's failure")
    }

    // MARK: - Helpers

    /// A runner over an in-memory store holding one queued draft (`submitting`, `submitPending`)
    /// that has reached the server, with `photos` (id, file name, seconds after capture) on disk.
    private func makeRunner(
        _ transport: ClientTransport,
        photos: [(UUID, String, TimeInterval)] = []
    ) throws -> (SubmitRunner, DraftStore, LocalDraft) {
        let container = try AppContainer.makeModelContainer(inMemory: true)
        self.container = container
        let store = DraftStore(context: container.mainContext)
        let capturedAt = Date.now.addingTimeInterval(-600)
        let draft = LocalDraft(id: draftID, companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting,
                               transcript: "Need header height", capturedAt: capturedAt)
        let folder = try DraftFiles.directory(for: draftID)
        for (id, fileName, offset) in photos {
            try Data([0xFF, 0xD8, 0xFF, 0xD9]).write(to: folder.appending(path: fileName))
            draft.photos.append(LocalPhoto(id: id, fileName: fileName, bytes: 4, createdAt: capturedAt.addingTimeInterval(offset)))
        }
        try store.insert(draft)
        let response = try LocalDraft.decoder.decode(DraftResponse.self, from: Data(draftBody(status: "reviewed").utf8))
        draft.apply(response)
        draft.status = .submitting
        draft.submitPending = true
        try store.save()
        let client = makeClient(transport)
        return (SubmitRunner(store: store, service: { DraftService(client: client) }), store, draft)
    }

    private func photo(_ id: UUID, in draft: LocalDraft) -> LocalPhoto {
        draft.photos.first { $0.id == id }!
    }

    private func draftJSON(status: String, unresolved: String = "[]", result: String? = nil) -> (HTTPResponse, HTTPBody?) {
        APIStub.json(.ok, draftBody(status: status, unresolved: unresolved, result: result))
    }

    private func draftBody(status: String, unresolved: String = "[]", result: String? = nil) -> String {
        var fields = [
            #""id":"\#(draftID.uuidString.lowercased())""#,
            #""feature":"rfi""#,
            #""project_id":99"#,
            #""status":"\#(status)""#,
            #""payload":{"subject":"Header height at grid C","assignee_ids":[],"distribution_ids":[],"private":false,"draft":false}"#,
            #""unresolved":\#(unresolved)"#,
            #""attachments":[]"#,
            #""confidence":{}"#,
            #""transcript":"Need header height""#,
            #""created_at":"2026-10-24T17:45:12Z""#,
            #""updated_at":"2026-10-24T17:46:00Z""#,
        ]
        if let result { fields.append(#""result":\#(result)"#) }
        return "{" + fields.joined(separator: ",") + "}"
    }
}
