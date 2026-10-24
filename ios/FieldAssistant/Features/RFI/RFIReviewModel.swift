import FieldAssistantAPI
import Foundation
import Observation

/// The state behind the review screen: the editable payload, the flagged fields, saving, and the
/// submit flow through the job poll. The server recomputes flags on every save, so its list
/// replaces the local one after each response.
@Observable
@MainActor
final class RFIReviewModel {
    enum SubmitOutcome: Equatable {
        case submitted(number: String?, url: String?)
        /// Blocking flags remain; the rows (and `formMessage`) say what to fix.
        case needsFixes
        case failed(APIFailure)
    }

    private(set) var payload: RFIDraftPayload
    /// Flags tied to a field. A `_form` flag is kept out of this list and shown as `formMessage`.
    private(set) var unresolved: [UnresolvedField]
    /// Procore's message for an error not tied to one field.
    private(set) var formMessage: String?
    var moreExpanded: Bool
    private(set) var isSaving = false
    private(set) var isSubmitting = false

    /// Flags that stop submit until settled.
    var unresolvedCount: Int { unresolved.filter(\.blocking).count }
    /// Flags shown on their rows that do not stop submit.
    var advisoryCount: Int { unresolved.filter { !$0.blocking }.count }

    @ObservationIgnored private let draft: LocalDraft
    @ObservationIgnored private let lookups: LookupStore
    @ObservationIgnored private let service: DraftService
    @ObservationIgnored private let store: DraftStore
    @ObservationIgnored private let submitAsDraftOverride: Bool?
    /// Labels of every candidate the server offered, by lookup kind, so a chosen row keeps its
    /// label after its flag is cleared even when the cached list does not have it.
    @ObservationIgnored private var candidateLabels: [LookupKind: [Int64: String]] = [:]

    private static let formField = "_form"
    /// The prefix of the backend's 400 when submit finds unresolved fields.
    private static let unresolvedPrefix = "draft has unresolved fields:"

    init(
        draft: LocalDraft,
        response: DraftResponse,
        lookups: LookupStore,
        service: DraftService,
        store: DraftStore,
        submitAsDraftOverride: Bool?
    ) {
        self.draft = draft
        self.lookups = lookups
        self.service = service
        self.store = store
        self.submitAsDraftOverride = submitAsDraftOverride
        payload = response.payload
        unresolved = response.unresolved.filter { $0.field != Self.formField }
        formMessage = response.unresolved.first { $0.field == Self.formField }?.message
        moreExpanded = false
        rememberCandidates(response.unresolved)
        moreExpanded = RFIField.more.contains { isSet($0) || unresolved(for: $0) != nil }
    }

    func unresolved(for field: RFIField) -> UnresolvedField? {
        unresolved.first { $0.field == field.rawValue }
    }

    /// The due date as midnight in the device's time zone, matching the day Procore shows.
    var dueDate: Date? {
        payload.due_date.flatMap { try? Self.dateStyle.parse($0) }
    }

    // MARK: - Editing

    /// Applies a picked lookup row: sets the id (appends for a multi field) and clears the flag.
    func choose(_ row: LookupRow, for field: RFIField) {
        if field == .drawingNumber {
            setText(row.extra?.additionalProperties["number"] ?? row.label, for: field)
        } else if field.isMulti {
            let current = ids(for: field)
            setIDs(current.contains(row.id) ? current : current + [row.id], for: field)
        } else {
            setID(row.id, for: field)
        }
        clearFlag(field)
    }

    func setID(_ id: Int64?, for field: RFIField) {
        switch field {
        case .rfiManagerId: payload.rfi_manager_id = id
        case .locationId: payload.location_id = id
        case .specificationSectionId: payload.specification_section_id = id
        case .responsibleContractorId: payload.responsible_contractor_id = id
        case .costCodeId: payload.cost_code_id = id
        case .assigneeIds, .distributionIds: return setIDs(id.map { [$0] } ?? [], for: field)
        default: return assertionFailure("\(field) does not hold an id")
        }
        if id != nil { clearFlag(field) }
    }

    func setIDs(_ ids: [Int64], for field: RFIField) {
        switch field {
        case .assigneeIds: payload.assignee_ids = ids
        case .distributionIds: payload.distribution_ids = ids
        default: return assertionFailure("\(field) does not hold a list of ids")
        }
        if !ids.isEmpty { clearFlag(field) }
    }

    /// Sets a text field. Empty text clears the field rather than sending an empty string.
    func setText(_ text: String, for field: RFIField) {
        let value = text.isEmpty ? nil : text
        switch field {
        case .subject: payload.subject = value
        case .questionBody: payload.question_body = value
        case .drawingNumber: payload.drawing_number = value
        default: return assertionFailure("\(field) is not a text field")
        }
        if value != nil { clearFlag(field) }
    }

    func setDate(_ date: Date?) {
        payload.due_date = date.map { $0.formatted(Self.dateStyle) }
        if date != nil { clearFlag(.dueDate) }
    }

    func setImpact(_ impact: Components.Schemas.Impact?, for field: RFIField) {
        switch field {
        case .scheduleImpact: payload.schedule_impact = impact
        case .costImpact: payload.cost_impact = impact
        default: return assertionFailure("\(field) is not an impact field")
        }
        if impact != nil { clearFlag(field) }
    }

    func setBool(_ value: Bool, for field: RFIField) {
        switch field {
        case .private: payload._private = value
        case .draft: payload.draft = value
        default: return assertionFailure("\(field) is not a toggle")
        }
        clearFlag(field)
    }

    /// The lookup label for an id: the cached list first, then the candidates the server offered.
    func label(forID id: Int64, kind: LookupKind) -> String? {
        if let row = lookups.rows(projectID: draft.projectID, kind: kind).first(where: { $0.id == id }) {
            return row.label
        }
        return candidateLabels[kind]?[id]
    }

    // MARK: - Saving and submitting

    /// Stores the payload on the server and locally, then takes the server's revalidated payload
    /// and flags. Returns the failure, or `nil` on success.
    func saveDraft() async -> APIFailure? {
        isSaving = true
        defer { isSaving = false }
        if let submitAsDraftOverride { payload.draft = submitAsDraftOverride }
        do {
            let response = try await service.save(id: draft.id, companyID: draft.companyID, payload: payload)
            take(response)
            return nil
        } catch {
            return APIFailure(error)
        }
    }

    /// Saves (the server validates again), queues the submit and waits for the job.
    func submit() async -> SubmitOutcome {
        isSubmitting = true
        defer { isSubmitting = false }
        if let failure = await saveDraft() { return .failed(failure) }

        let previous = draft.status
        draft.status = .submitting
        try? store.save()

        let start: DraftService.SubmitStart
        do {
            start = try await service.submit(id: draft.id, companyID: draft.companyID)
        } catch {
            // Nothing was queued, so the draft goes back to where it was.
            draft.status = previous
            try? store.save()
            if case .validation(let message) = APIFailure(error), flagFields(from: message) {
                return .needsFixes
            }
            return .failed(APIFailure(error))
        }

        let response: DraftResponse
        switch start {
        case .submitted(let submitted):
            response = submitted
        case .running(let jobID):
            do {
                response = try await service.waitForJob(id: jobID, draftID: draft.id, companyID: draft.companyID)
            } catch {
                // The job is queued: the draft stays `submitting` and is reconciled on relaunch.
                return .failed(Task.isCancelled || error is CancellationError ? APIFailure(CancellationError()) : APIFailure(error))
            }
        }

        take(response)
        switch response.status {
        case .submitted:
            DraftFiles.removeAll(for: draft.id)
            return .submitted(number: response.result?.number, url: response.result?.url)
        case .failed where unresolvedCount > 0 || formMessage != nil:
            return .needsFixes
        case .failed:
            return .failed(.server(response.error?.message ?? "Procore did not accept this RFI."))
        case .submitting:
            return .failed(.server("Procore has not confirmed this RFI yet. It stays in Sending and is checked again later."))
        case .extracted, .reviewed:
            return .failed(.server("The RFI was not sent. Try again."))
        }
    }

    // MARK: - Private

    /// Applies a server response here and to the local draft, and saves the local draft.
    private func take(_ response: DraftResponse) {
        payload = response.payload
        unresolved = response.unresolved.filter { $0.field != Self.formField }
        formMessage = response.unresolved.first { $0.field == Self.formField }?.message
        rememberCandidates(response.unresolved)
        draft.apply(response)
        try? store.save()
    }

    /// Flags the fields a submit 400 lists, with the server's message as the reason. Returns
    /// `false` when the message names no fields.
    private func flagFields(from message: String) -> Bool {
        guard message.hasPrefix(Self.unresolvedPrefix) else { return false }
        let names = message.dropFirst(Self.unresolvedPrefix.count)
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        guard !names.isEmpty else { return false }
        for name in names {
            let candidates = unresolved.first { $0.field == name }?.candidates ?? []
            unresolved.removeAll { $0.field == name }
            unresolved.append(UnresolvedField(
                field: name,
                reason: .procore_validation,
                message: message,
                candidates: candidates,
                blocking: true
            ))
        }
        return true
    }

    private func rememberCandidates(_ flags: [UnresolvedField]) {
        for flag in flags {
            guard let kind = RFIField(serverField: flag.field)?.lookupKind else { continue }
            for row in flag.candidates { candidateLabels[kind, default: [:]][row.id] = row.label }
        }
    }

    private func clearFlag(_ field: RFIField) {
        unresolved.removeAll { $0.field == field.rawValue }
    }

    private func ids(for field: RFIField) -> [Int64] {
        switch field {
        case .assigneeIds: payload.assignee_ids
        case .distributionIds: payload.distribution_ids
        default: []
        }
    }

    /// Whether a field holds a value. Toggles count as set when on.
    private func isSet(_ field: RFIField) -> Bool {
        switch field {
        case .subject: payload.subject != nil
        case .questionBody: payload.question_body != nil
        case .assigneeIds: !payload.assignee_ids.isEmpty
        case .rfiManagerId: payload.rfi_manager_id != nil
        case .locationId: payload.location_id != nil
        case .specificationSectionId: payload.specification_section_id != nil
        case .dueDate: payload.due_date != nil
        case .drawingNumber: payload.drawing_number != nil
        case .scheduleImpact: payload.schedule_impact != nil
        case .costImpact: payload.cost_impact != nil
        case .distributionIds: !payload.distribution_ids.isEmpty
        case .responsibleContractorId: payload.responsible_contractor_id != nil
        case .costCodeId: payload.cost_code_id != nil
        case .private: payload._private
        case .draft: payload.draft
        }
    }

    /// OpenAPI `date` (`yyyy-MM-dd`) in the device's time zone.
    private static var dateStyle: Date.ISO8601FormatStyle {
        Date.ISO8601FormatStyle(timeZone: .current).year().month().day().dateSeparator(.dash)
    }
}
