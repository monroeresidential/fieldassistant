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
        XCTAssertFalse(FileManager.default.fileExists(atPath: leftover.path()), "files are removed once Procore has the RFI")
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
        XCTAssertNotEqual(local.status, .submitting)
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
            #""confidence":{}"#,
            #""transcript":"Need header height, ask Mike""#,
            #""created_at":"2026-10-24T17:45:12Z""#,
            #""updated_at":"2026-10-24T17:46:00Z""#,
        ]
        if let result { fields.append(#""result":\#(result)"#) }
        return "{" + fields.joined(separator: ",") + "}"
    }

    private func makeModel(
        _ transport: StubTransport,
        unresolved: String? = nil,
        draft: Bool = false,
        submitAsDraftOverride: Bool? = nil
    ) throws -> (RFIReviewModel, LocalDraft, ModelContainer) {
        let container = try AppContainer.makeModelContainer(inMemory: true)
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
            submitAsDraftOverride: submitAsDraftOverride
        )
        return (model, local, container)
    }
}
