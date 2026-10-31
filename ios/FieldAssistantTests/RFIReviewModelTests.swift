import FieldAssistantAPI
import Foundation
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

@MainActor
final class RFIReviewModelTests: XCTestCase {
    private let draftID = UUID(uuidString: "3C2B1A09-8F7E-4D6C-9B5A-1F2E3D4C5B6A")!
    private var draftPath: String { "/drafts/\(draftID.uuidString.lowercased())" }

    override func tearDown() {
        DraftFiles.removeAll(for: draftID)
        super.tearDown()
    }

    func testInitialStateCountsUnresolvedAndExpandsMore() throws {
        let (model, _, _) = try makeModel(StubTransport())

        XCTAssertEqual(model.unresolvedCount, 1)
        XCTAssertEqual(model.advisoryCount, 1)
        XCTAssertTrue(model.moreExpanded, "cost_code_id is set")
        XCTAssertEqual(model.unresolved(for: .assigneeIds)?.reason, .ambiguous)
        XCTAssertNil(model.unresolved(for: .subject))
        XCTAssertEqual(model.payload.cost_code_id, 401)
    }

    func testMoreStaysClosedWhenOnlyTheDraftToggleIsOn() throws {
        let container = try AppContainer.makeModelContainer(inMemory: true).get()
        let store = DraftStore(context: container.mainContext)
        let client = makeClient(StubTransport())
        let body = draftBody(status: "extracted", unresolved: "[]", draft: true)
            .replacingOccurrences(of: #""cost_code_id":401,"#, with: "")
        let response = try LocalDraft.decoder.decode(DraftResponse.self, from: Data(body.utf8))
        let local = LocalDraft(id: draftID, companyID: 7, projectID: 99, transcript: "t")
        try store.insert(local)
        local.apply(response)

        let model = RFIReviewModel(
            draft: local,
            response: response,
            lookups: LookupStore(context: container.mainContext, service: LookupService(client: client)),
            service: DraftService(client: client),
            store: store,
            runner: SubmitRunner(store: store, service: { DraftService(client: client) }),
            submitAsDraftOverride: nil
        )

        XCTAssertTrue(model.payload.draft)
        XCTAssertFalse(model.moreExpanded, "the draft toggle is on by default and does not open More")
    }

    func testChoosingCandidateClearsFlagAndSetsID() throws {
        let (model, _, _) = try makeModel(StubTransport())
        let candidate = try XCTUnwrap(model.unresolved(for: .assigneeIds)?.candidates.first { $0.id == 12 })

        model.choose(candidate, for: .assigneeIds)

        XCTAssertEqual(model.payload.assignee_ids, [12])
        XCTAssertNil(model.unresolved(for: .assigneeIds))
        XCTAssertEqual(model.unresolvedCount + model.advisoryCount, 1)
        XCTAssertEqual(model.label(forID: 12, kind: .users), "Mike Alvarez")
    }

    func testSubmitSavesThenSubmitsThenPolls() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed"),
            APIStub.submit(.accepted, jobID: "job-5", draftID: draftID),
            APIStub.job(id: "job-5", draftID: draftID, status: "succeeded"),
            draftJSON(
                status: "submitted",
                unresolved: "[]",
                result: #"{"procore_id":501,"number":"214","url":"https://sandbox.procore.com/rfis/501"}"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)
        let leftover = try DraftFiles.directory(for: draftID).appending(path: "audio.m4a")
        try Data("audio".utf8).write(to: leftover)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .submitted(number: "214", url: "https://sandbox.procore.com/rfis/501"))
        XCTAssertEqual(local.status, .submitted)
        XCTAssertEqual(local.resultNumber, "214")
        XCTAssertFalse(FileManager.default.fileExists(atPath: leftover.path(percentEncoded: false)), "files are removed once Procore has the RFI")
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .post, .get, .get])
        XCTAssertEqual(transport.requests.map(\.request.path), [
            draftPath, draftPath + "/submit", "/jobs/job-5", draftPath,
        ])
        XCTAssertFalse(model.isSubmitting)
    }

    func testSubmit422ReflagsRows() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed"),
            APIStub.submit(.accepted, draftID: draftID),
            APIStub.job(draftID: draftID, status: "failed"),
            draftJSON(
                status: "failed",
                unresolved: #"[{"field":"due_date","reason":"procore_validation","message":"can't be blank","candidates":[],"blocking":true}]"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .needsFixes)
        XCTAssertEqual(model.unresolved(for: .dueDate)?.message, "can't be blank")
        XCTAssertNil(model.unresolved(for: .assigneeIds), "the server's list replaces the local one")
        XCTAssertEqual(model.unresolvedCount, 1)
        XCTAssertEqual(local.status, .failed)
    }

    func testSubmitWith400UnresolvedShowsFields() async throws {
        let message = "draft has unresolved fields: assignee_ids, rfi_manager_id"
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.badRequest, code: "bad_request", message: message),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .needsFixes)
        let assignee = try XCTUnwrap(model.unresolved(for: .assigneeIds))
        XCTAssertEqual(assignee.reason, .procore_validation)
        XCTAssertEqual(assignee.message, message)
        XCTAssertTrue(assignee.blocking)
        XCTAssertNotNil(model.unresolved(for: .rfiManagerId))
        XCTAssertEqual(model.unresolvedCount, 2)
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .post, .get], "the refusal re-reads the draft")
        // Nothing was queued; the refusal is kept on the phone so the draft reads Fix.
        XCTAssertEqual(local.status, .failed)
        XCTAssertFalse(local.submitPending)
    }

    func testSubmit400FormAndUnknownNamesDoNotAddRows() async throws {
        let message = "draft has unresolved fields: _form, mystery_field"
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.badRequest, code: "bad_request", message: message),
        ])
        let (model, _, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .needsFixes)
        XCTAssertEqual(model.formMessage, message)
        XCTAssertTrue(model.unresolved.isEmpty)
    }

    func testSubmitIgnoredWhileSubmitting() async throws {
        let gate = GateTransport(StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.submit(.ok, draftID: draftID),
            draftJSON(status: "submitted", unresolved: "[]", result: #"{"procore_id":501,"number":"214"}"#),
        ]))
        let (model, _, _) = try makeModel(gate)
        let first = Task { await model.submit() }
        try await gate.waitUntilHeld()

        let second = await model.submit()
        let saved = await model.saveDraft()

        guard case .pending = second else { return XCTFail("expected pending, got \(second)") }
        XCTAssertNil(saved)
        XCTAssertEqual(gate.inner.requests.count, 0, "the second calls send nothing")
        gate.release()
        let outcome = await first.value
        XCTAssertEqual(outcome, .submitted(number: "214", url: nil))
        XCTAssertEqual(gate.inner.requests.map(\.request.method), [.patch, .post, .get])
    }

    func testEditDuringSaveIsKept() async throws {
        let gate = GateTransport(StubTransport([draftJSON(status: "reviewed")]))
        let (model, local, _) = try makeModel(gate)
        let save = Task { await model.saveDraft() }
        try await gate.waitUntilHeld()

        model.setText("Header height at grid D", for: .subject)
        model.setID(13, for: .assigneeIds)
        gate.release()
        let failure = await save.value

        XCTAssertNil(failure)
        XCTAssertEqual(model.payload.subject, "Header height at grid D", "the server echoed the old subject")
        XCTAssertEqual(model.payload.assignee_ids, [13])
        XCTAssertNil(model.unresolved(for: .assigneeIds), "a field edited in flight drops the server's flag")
        XCTAssertNotNil(model.unresolved(for: .locationId), "untouched fields take the server's flags")
        XCTAssertEqual(model.payload.cost_code_id, 401)
        XCTAssertEqual(local.server?.payload.subject, "Header height at grid D")
    }

    func testFailedSaveKeepsEditsLocally() async throws {
        let (model, _, container) = try makeModel(FailingTransport(error: URLError(.notConnectedToInternet)))
        model.setText("Edited offline", for: .subject)

        let failure = await model.saveDraft()

        XCTAssertEqual(failure, .offline)
        let id = draftID
        let reloaded = try XCTUnwrap(ModelContext(container).fetch(FetchDescriptor<LocalDraft>(predicate: #Predicate { $0.id == id })).first)
        XCTAssertEqual(reloaded.server?.payload.subject, "Edited offline")
        XCTAssertEqual(reloaded.subject, "Edited offline")
    }

    func testCancelledSubmitLeavesDraftSubmitting() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.submit(.accepted, draftID: draftID),
            APIStub.job(draftID: draftID, status: "running"),
        ])
        let (model, local, _) = try makeModel(transport)
        let submit = Task { await model.submit() }
        // Once the first job poll is in, the wait is sleeping before the next poll.
        for _ in 0..<500 where transport.requests.count < 3 {
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTAssertEqual(transport.requests.count, 3)

        submit.cancel()
        let outcome = await submit.value

        guard case .pending = outcome else { return XCTFail("expected pending, got \(outcome)") }
        XCTAssertEqual(local.status, .submitting)
        XCTAssertFalse(model.isSubmitting)
    }

    func testSubmitAsDraftOverrideAppliedOnSave() async throws {
        let transport = StubTransport([draftJSON(status: "reviewed", draft: false)])
        let (model, _, _) = try makeModel(transport, draft: true, submitAsDraftOverride: false)

        let failure = await model.saveDraft()

        XCTAssertNil(failure)
        let body = try APIStub.jsonObject(try XCTUnwrap(transport.requests.first).body)
        let payload = try XCTUnwrap(body["payload"] as? [String: Any])
        XCTAssertEqual(payload["draft"] as? Bool, false)
        XCTAssertEqual(model.payload.draft, false)
    }

    func testAdvisoryFlagsOnSubmittedDraftStillSubmit() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed"),
            APIStub.submit(.ok, draftID: draftID),
            draftJSON(
                status: "submitted",
                unresolved: #"[{"field":"location_id","reason":"not_found","candidates":[],"blocking":false}]"#,
                result: #"{"procore_id":502,"number":"215"}"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .submitted(number: "215", url: nil))
        XCTAssertEqual(local.status, .submitted)
    }

    func testFormFlagBecomesFormMessage() throws {
        let unresolved = #"[{"field":"_form","reason":"procore_validation","message":"Something else is wrong","candidates":[],"blocking":true}]"#
        let (model, _, _) = try makeModel(StubTransport(), unresolved: unresolved)

        XCTAssertEqual(model.formMessage, "Something else is wrong")
        XCTAssertTrue(model.unresolved.isEmpty)
        XCTAssertNil(RFIField(serverField: "_form"))
        XCTAssertEqual(RFIField(serverField: "cost_code_id"), .costCodeId)
    }

    func testSubmitTransportFailureKeepsSubmittingAndPending() async throws {
        let transport = ScriptedTransport([
            .response(draftJSON(status: "reviewed", unresolved: "[]")),
            .failure(URLError(.networkConnectionLost)),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        guard case .pending = outcome else { return XCTFail("expected pending, got \(outcome)") }
        XCTAssertEqual(local.status, .submitting, "the POST may have landed; only the server can say")
        XCTAssertEqual(transport.requests.map(\.method), [.patch, .post])
        XCTAssertFalse(model.isSubmitting)
    }

    func testDefinitiveSubmitRefusalPersistsFailed() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.forbidden, code: "procore_permission_denied", message: "No RFI access"),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .failed(.permissionDenied("No RFI access")))
        XCTAssertEqual(local.status, .failed)
        XCTAssertEqual(local.errorMessage, "No RFI access")
        XCTAssertFalse(local.submitPending)
    }

    func testLockedSaveRefreshesFromServer() async throws {
        let transport = StubTransport([
            APIStub.error(.badRequest, code: "bad_request", message: "draft is submitted or being submitted"),
            draftJSON(status: "submitting", unresolved: "[]"),
        ])
        let (model, local, _) = try makeModel(transport)

        let failure = await model.saveDraft()

        XCTAssertNil(failure, "no alert: the draft is already on its way")
        XCTAssertEqual(local.status, .submitting)
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .get])
        XCTAssertEqual(transport.requests.last?.request.path, draftPath)
    }

    func testLockedSubmitReportsServerStatusWithoutPosting() async throws {
        let transport = StubTransport([
            APIStub.error(.badRequest, code: "bad_request", message: "draft is submitted or being submitted"),
            draftJSON(status: "submitted", unresolved: "[]", result: #"{"procore_id":501,"number":"214"}"#),
        ])
        let (model, local, _) = try makeModel(transport)
        let leftover = try DraftFiles.directory(for: draftID).appending(path: "audio.m4a")
        try Data("audio".utf8).write(to: leftover)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .submitted(number: "214", url: nil))
        XCTAssertEqual(local.status, .submitted)
        XCTAssertFalse(FileManager.default.fileExists(atPath: leftover.path(percentEncoded: false)))
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .get], "no second POST")
    }

    /// The save is refused as locked, but by the time the phone asks, the server's submit has
    /// failed: the draft takes the server's state and flags, the user's edit stays, and nothing is
    /// queued or posted until the user has looked at it.
    func testLockedSaveWithFailedServerDraftStopsAndKeepsEdits() async throws {
        let transport = StubTransport([
            APIStub.error(.badRequest, code: "bad_request", message: "draft is submitted or being submitted"),
            draftJSON(
                status: "failed",
                unresolved: #"[{"field":"rfi_manager_id","reason":"procore_validation","message":"rfi_manager_id invalid","candidates":[],"blocking":true}]"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)
        model.setText("Header height at grid D", for: .subject)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .pending(message: RFIReviewModel.lockedStopMessage))
        XCTAssertEqual(RFIReviewModel.lockedStopMessage, "This draft changed on the server. Review it, then submit again.")
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .get], "no POST")
        XCTAssertEqual(model.payload.subject, "Header height at grid D", "the unsaved edit is kept")
        XCTAssertEqual(local.server?.payload.subject, "Header height at grid D")
        XCTAssertEqual(local.status, .failed)
        XCTAssertFalse(local.submitPending, "nothing is queued")
        let owed = try LocalDraft.decoder.decode(RFIDraftPayload.self, from: try XCTUnwrap(local.pendingPayloadData))
        XCTAssertEqual(owed.subject, "Header height at grid D", "the edit is still owed to the server")
        XCTAssertEqual(model.unresolved.map(\.field), ["rfi_manager_id"], "the server's flags show")
        XCTAssertFalse(model.isSubmitting)
    }

    func testPollErrorAfterQueuedIsPending() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.submit(.accepted, jobID: "job-5", draftID: draftID),
            APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is down"),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        guard case .pending = outcome else { return XCTFail("expected pending, got \(outcome)") }
        XCTAssertEqual(local.status, .submitting)
    }

    func testSubmitOfflineQueuesAndReturnsQueuedMessage() async throws {
        let (model, local, container) = try makeModel(FailingTransport(error: URLError(.notConnectedToInternet)))
        model.setText("Edited offline", for: .subject)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .pending(message: "Queued. It sends on its own when you have signal."))
        XCTAssertEqual(local.status, .submitting)
        XCTAssertTrue(local.submitPending)
        let saved = try LocalDraft.decoder.decode(RFIDraftPayload.self, from: try XCTUnwrap(local.pendingPayloadData))
        XCTAssertEqual(saved.subject, "Edited offline", "the reviewed payload waits on the phone for the runner")
        let id = draftID
        let reloaded = try XCTUnwrap(ModelContext(container).fetch(FetchDescriptor<LocalDraft>(predicate: #Predicate { $0.id == id })).first)
        XCTAssertTrue(reloaded.submitPending, "the queued intent survives a relaunch")
        XCTAssertNotNil(reloaded.pendingPayloadData)
        XCTAssertFalse(model.isSubmitting)
    }

    func testSubmitRefusalShowsFixAndPersistsFailed() async throws {
        let message = "draft has unresolved fields: rfi_manager_id"
        let transport = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.badRequest, code: "bad_request", message: message),
            draftJSON(
                status: "reviewed",
                unresolved: #"[{"field":"rfi_manager_id","reason":"not_found","candidates":[],"blocking":true}]"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .needsFixes)
        XCTAssertEqual(model.unresolved(for: .rfiManagerId)?.message, message)
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .post, .get])
        XCTAssertEqual(local.status, .failed)
        XCTAssertEqual(local.errorMessage, RFIReviewModel.fixFlaggedMessage, "the rows name the fields; the line says what to do")
        XCTAssertFalse(local.submitPending)
        XCTAssertEqual(local.server?.unresolved.map(\.field), ["rfi_manager_id"], "the server's flags survive on the phone")
    }

    func testInlineUnresolvedRefusalStoresFixMessageOtherRefusalsKeepServerText() async throws {
        let unresolvedMessage = "draft has unresolved fields: rfi_manager_id"
        let unresolved = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.badRequest, code: "bad_request", message: unresolvedMessage),
            draftJSON(status: "reviewed", unresolved: "[]"),
        ])
        let (flagged, flaggedDraft, flaggedContainer) = try makeModel(unresolved)
        _ = await flagged.submit()
        XCTAssertEqual(flaggedDraft.errorMessage, "Fix the flagged fields and submit again.")

        let otherMessage = "Idempotency-Key must equal the draft id"
        let other = StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.error(.badRequest, code: "bad_request", message: otherMessage),
            draftJSON(status: "reviewed", unresolved: "[]"),
        ])
        let (model, local, container) = try makeModel(other)
        let outcome = await model.submit()
        XCTAssertEqual(outcome, .failed(.validation(otherMessage)))
        XCTAssertEqual(local.errorMessage, otherMessage, "other refusals keep the server's message")
        withExtendedLifetime((flaggedContainer, container)) {}
    }

    func testSubmitAcceptedPathUnchanged() async throws {
        let transport = StubTransport([
            draftJSON(status: "reviewed"),
            APIStub.submit(.accepted, jobID: "job-5", draftID: draftID),
            APIStub.job(id: "job-5", draftID: draftID, status: "succeeded"),
            draftJSON(
                status: "submitted",
                unresolved: "[]",
                result: #"{"procore_id":501,"number":"214","url":"https://sandbox.procore.com/rfis/501"}"#
            ),
        ])
        let (model, local, _) = try makeModel(transport)
        let leftover = try DraftFiles.directory(for: draftID).appending(path: "audio.m4a")
        try Data("audio".utf8).write(to: leftover)

        let outcome = await model.submit()

        XCTAssertEqual(outcome, .submitted(number: "214", url: "https://sandbox.procore.com/rfis/501"))
        XCTAssertEqual(local.status, .submitted)
        XCTAssertEqual(local.resultNumber, "214")
        XCTAssertFalse(local.submitPending)
        XCTAssertNil(local.pendingPayloadData, "the PATCH landed, so nothing is left to save")
        XCTAssertFalse(FileManager.default.fileExists(atPath: leftover.path(percentEncoded: false)))
        XCTAssertEqual(transport.requests.map(\.request.method), [.patch, .post, .get, .get])
        XCTAssertEqual(transport.requests.map(\.request.path), [
            draftPath, draftPath + "/submit", "/jobs/job-5", draftPath,
        ])
        XCTAssertFalse(model.isSubmitting)
    }

    func testUnknownBlockingFieldDoesNotCountAsUnresolved() throws {
        let unresolved = #"[{"field":"mystery_field","reason":"procore_validation","candidates":[],"blocking":true}]"#
        let (model, _, _) = try makeModel(StubTransport(), unresolved: unresolved)

        XCTAssertEqual(model.unresolvedCount, 0, "nothing on screen could clear it")
    }

    // MARK: - Queued drafts and failed photos

    func testStopSendingWhileRunningIsRefused() async throws {
        let gate = GateTransport(StubTransport([
            draftJSON(status: "reviewed", unresolved: "[]"),
            APIStub.submit(.ok, draftID: draftID),
            draftJSON(status: "submitted", unresolved: "[]", result: #"{"procore_id":501,"number":"214"}"#),
        ]), gating: { $0.method == .post })
        let (model, local, _) = try makeModel(gate)
        let submit = Task { await model.submit() }
        try await gate.waitUntilHeld()
        XCTAssertTrue(model.isQueued)

        let outcome = await model.stopSending()

        XCTAssertEqual(outcome, .sending)
        XCTAssertTrue(local.submitPending, "the run in progress owns the draft")
        XCTAssertEqual(local.status, .submitting)
        gate.release()
        let submitted = await submit.value
        XCTAssertEqual(submitted, .submitted(number: "214", url: nil))
        XCTAssertEqual(gate.inner.requests.map(\.request.method), [.patch, .post, .get], "Stop sent nothing")
    }

    func testStopSendingAsksServerAndReportsAlreadySent() async throws {
        let transport = StubTransport([draftJSON(status: "submitting", unresolved: "[]")])
        let (model, local, _) = try makeModel(transport)
        try queue(local, model: model)

        let outcome = await model.stopSending()

        XCTAssertEqual(outcome, .alreadySent)
        XCTAssertEqual(transport.requests.map(\.request.method), [.get])
        XCTAssertEqual(transport.requests.first?.request.path, draftPath)
        XCTAssertEqual(local.status, .submitting, "the server's state is applied")
        XCTAssertFalse(local.submitPending, "the server owns the draft now")
        XCTAssertNil(local.pendingPayloadData, "the server takes no more edits")
        XCTAssertFalse(model.isQueued)
    }

    func testStopSendingWithSignalServerNotSentStops() async throws {
        let transport = StubTransport([draftJSON(status: "reviewed", unresolved: "[]")])
        let (model, local, _) = try makeModel(transport)
        try queue(local, model: model)

        let outcome = await model.stopSending()

        XCTAssertEqual(outcome, .stopped)
        XCTAssertEqual(transport.requests.map(\.request.method), [.get])
        XCTAssertEqual(local.status, .draft)
        XCTAssertFalse(local.submitPending)
        XCTAssertNotNil(local.pendingPayloadData, "the reviewed edits still wait for the next save")
    }

    func testStopSendingOfflineClearsFlagAndUnlocks() async throws {
        let transport = StubTransport()
        let (model, local, container) = try makeModel(transport, isReachable: { false })
        try queue(local, model: model)

        let outcome = await model.stopSending()

        XCTAssertEqual(outcome, .stopped)
        XCTAssertTrue(transport.requests.isEmpty, "with no signal the flag clears at once")
        XCTAssertFalse(model.isQueued)
        XCTAssertEqual(local.status, .draft)
        XCTAssertNotNil(local.pendingPayloadData, "the reviewed edits are kept")
        let id = draftID
        let reloaded = try XCTUnwrap(ModelContext(container).fetch(FetchDescriptor<LocalDraft>(predicate: #Predicate { $0.id == id })).first)
        XCTAssertFalse(reloaded.submitPending, "the stop is saved")
        XCTAssertEqual(reloaded.status, .draft)
    }

    func testRemovePhotoMarksRemovedAndDeletesFile() async throws {
        let (model, local, container) = try makeModel(StubTransport())
        let file = DraftFiles.photoURL(draftID: draftID, fileName: "big.jpg")
        _ = try DraftFiles.directory(for: draftID)
        try Data([0xFF, 0xD8, 0xFF, 0xD9]).write(to: file)
        let photo = LocalPhoto(fileName: "big.jpg", bytes: 4)
        local.photos.append(photo)
        photo.uploadState = .failed
        photo.failureMessage = "Photo is larger than 15 MB."
        local.status = .failed
        local.errorMessage = "Photo is larger than 15 MB."
        try container.mainContext.save()

        model.removePhoto(photo)

        XCTAssertEqual(photo.uploadState, .removed, "the runner sends the DELETE later")
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path(percentEncoded: false)), "the file goes now")
        XCTAssertEqual(local.status, .draft, "the photo was all that stopped it, so Submit is open again")
        XCTAssertNil(local.errorMessage)
        let id = photo.id
        let reloaded = try XCTUnwrap(ModelContext(container).fetch(FetchDescriptor<LocalPhoto>(predicate: #Predicate { $0.id == id })).first)
        XCTAssertEqual(reloaded.uploadState, .removed, "the removal is saved")
    }

    func testRemovePhotoWithoutMessageClearsFix() async throws {
        let (model, local, container) = try makeModel(StubTransport())
        let photo = LocalPhoto(fileName: "odd.jpg", bytes: 4)
        local.photos.append(photo)
        // The server refused the photo without saying why, so the runner wrote its own line.
        photo.uploadState = .failed
        photo.failureMessage = nil
        local.status = .failed
        local.errorMessage = SubmitRunner.photoRefusedMessage
        try container.mainContext.save()

        model.removePhoto(photo)

        XCTAssertEqual(local.status, .draft, "the fallback message still counts as the photo's failure")
        XCTAssertNil(local.errorMessage)
    }

    func testRemovePhotoLeavesAnUnrelatedFailureAlone() async throws {
        let (model, local, container) = try makeModel(StubTransport())
        let photo = LocalPhoto(fileName: "odd.jpg", bytes: 4)
        local.photos.append(photo)
        photo.uploadState = .failed
        photo.failureMessage = nil
        local.status = .failed
        local.errorMessage = "Procore did not accept this RFI."
        try container.mainContext.save()

        model.removePhoto(photo)

        XCTAssertEqual(local.status, .failed)
        XCTAssertEqual(local.errorMessage, "Procore did not accept this RFI.")
    }

    // MARK: - Fixtures

    private let defaultUnresolved = #"""
    [{"field":"assignee_ids","reason":"ambiguous","message":"Two people named Mike","candidates":\#
    [{"id":12,"label":"Mike Alvarez","extra":{"role":"Architect"}},{"id":13,"label":"Mike Brandt"}],"blocking":true},\#
    {"field":"location_id","reason":"not_found","candidates":[],"blocking":false}]
    """#

    private func draftJSON(
        status: String,
        unresolved: String? = nil,
        draft: Bool = false,
        result: String? = nil
    ) -> (HTTPResponse, HTTPBody?) {
        APIStub.json(.ok, draftBody(status: status, unresolved: unresolved, draft: draft, result: result))
    }

    private func draftBody(status: String, unresolved: String?, draft: Bool, result: String? = nil) -> String {
        var fields = [
            #""id":"\#(draftID.uuidString.lowercased())""#,
            #""feature":"rfi""#,
            #""project_id":99"#,
            #""status":"\#(status)""#,
            #""payload":{"subject":"Header height at grid C","assignee_ids":[],"distribution_ids":[],"cost_code_id":401,"private":false,"draft":\#(draft)}"#,
            #""unresolved":\#(unresolved ?? defaultUnresolved)"#,
            #""attachments":[]"#,
            #""confidence":{}"#,
            #""transcript":"Need header height, ask Mike""#,
            #""created_at":"2026-10-24T17:45:12Z""#,
            #""updated_at":"2026-10-24T17:46:00Z""#,
        ]
        if let result { fields.append(#""result":\#(result)"#) }
        return "{" + fields.joined(separator: ",") + "}"
    }

    private func makeModel(
        _ transport: ClientTransport,
        unresolved: String? = nil,
        draft: Bool = false,
        submitAsDraftOverride: Bool? = nil,
        isReachable: @escaping @MainActor () -> Bool = { true }
    ) throws -> (RFIReviewModel, LocalDraft, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true).get()
        let store = DraftStore(context: container.mainContext)
        let client = makeClient(transport)
        let lookups = LookupStore(context: container.mainContext, service: LookupService(client: client))
        let local = LocalDraft(id: draftID, companyID: 7, projectID: 99, transcript: "Need header height, ask Mike")
        try store.insert(local)
        let body = draftBody(status: "extracted", unresolved: unresolved, draft: draft)
        let response = try LocalDraft.decoder.decode(DraftResponse.self, from: Data(body.utf8))
        local.apply(response)
        try store.save()
        let model = RFIReviewModel(
            draft: local,
            response: response,
            lookups: lookups,
            service: DraftService(client: client),
            store: store,
            runner: SubmitRunner(store: store, service: { DraftService(client: client) }),
            submitAsDraftOverride: submitAsDraftOverride,
            isReachable: isReachable
        )
        return (model, local, container)
    }

    /// Puts the draft in the state a Submit tapped with no signal leaves it: Queued, with the
    /// reviewed payload waiting on the phone.
    private func queue(_ local: LocalDraft, model: RFIReviewModel) throws {
        local.status = .submitting
        local.submitPending = true
        local.pendingPayloadData = try LocalDraft.encoder.encode(model.payload)
        XCTAssertTrue(model.isQueued)
    }
}

/// `ClientTransport` that holds every request (or, with `gating`, each matching one) until
/// `release()`, then forwards it to `inner`, so a test can act while a request is in flight.
final class GateTransport: ClientTransport, @unchecked Sendable {
    let inner: StubTransport
    private let gating: @Sendable (HTTPRequest) -> Bool
    private let lock = NSLock()
    private var released = false
    private var held = 0
    private var waiters: [CheckedContinuation<Void, Never>] = []

    init(_ inner: StubTransport, gating: @escaping @Sendable (HTTPRequest) -> Bool = { _ in true }) {
        self.inner = inner
        self.gating = gating
    }

    /// Returns once a request is waiting at the gate, or fails after about five seconds.
    func waitUntilHeld() async throws {
        for _ in 0..<500 {
            if lock.withLock({ held > 0 }) { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        XCTFail("no request reached the gate")
    }

    func release() {
        let pending = lock.withLock {
            released = true
            defer { waiters = [] }
            return waiters
        }
        pending.forEach { $0.resume() }
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        guard gating(request) else {
            return try await inner.send(request, body: body, baseURL: baseURL, operationID: operationID)
        }
        await withCheckedContinuation { continuation in
            let resumeNow = lock.withLock {
                held += 1
                if released { return true }
                waiters.append(continuation)
                return false
            }
            if resumeNow { continuation.resume() }
        }
        return try await inner.send(request, body: body, baseURL: baseURL, operationID: operationID)
    }
}
