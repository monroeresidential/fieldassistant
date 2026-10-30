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
        /// Procore refused the RFI; the flagged rows and `formMessage` say what to fix. A failure
        /// that only carries a `_form` message also lands here with no blocking flags: Submit stays
        /// enabled and the screen shows the message.
        case needsFixes
        /// The submit is queued but Procore has not confirmed it: the server queued it and the job
        /// poll timed out, failed or was cancelled (Check or the sync settles it), or the phone has
        /// not reached the server yet and the sync sends it when there is signal. The draft stays
        /// `submitting` either way.
        case pending(message: String)
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

    /// Flags that stop submit until settled. Only flags on a review row count: a blocking flag on a
    /// field the phone has no row for could never be cleared, so it must not disable Submit.
    var unresolvedCount: Int { unresolved.filter { $0.blocking && RFIField(serverField: $0.field) != nil }.count }
    /// Flags shown on their rows that do not stop submit.
    var advisoryCount: Int { unresolved.filter { !$0.blocking }.count }

    @ObservationIgnored private let draft: LocalDraft
    @ObservationIgnored private let lookups: LookupStore
    @ObservationIgnored private let service: DraftService
    @ObservationIgnored private let store: DraftStore
    @ObservationIgnored private let runner: SubmitRunner
    @ObservationIgnored private let submitAsDraftOverride: Bool?
    /// Labels of every candidate the server offered, by lookup kind, so a chosen row keeps its
    /// label after its flag is cleared even when the cached list does not have it.
    @ObservationIgnored private var candidateLabels: [LookupKind: [Int64: String]] = [:]

    private static let formField = "_form"
    /// The prefix of the backend's 400 when submit finds unresolved fields.
    private static let unresolvedPrefix = "draft has unresolved fields:"
    private static let pendingMessage = "Procore has not confirmed the RFI yet; it will update automatically."
    /// The submit is on the phone, waiting for the runner to reach the server.
    static let queuedMessage = "Queued. It sends on its own when you have signal."
    private static let alreadySendingMessage = "This RFI is already being saved or sent."

    init(
        draft: LocalDraft,
        response: DraftResponse,
        lookups: LookupStore,
        service: DraftService,
        store: DraftStore,
        runner: SubmitRunner,
        submitAsDraftOverride: Bool?
    ) {
        self.draft = draft
        self.lookups = lookups
        self.service = service
        self.store = store
        self.runner = runner
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

    /// Stores the payload on the phone, then on the server, and takes the server's revalidated
    /// payload and flags. Returns the failure, or `nil` on success. Does nothing while a save or
    /// submit is already running.
    func saveDraft() async -> APIFailure? {
        guard !isSaving, !isSubmitting else { return nil }
        isSaving = true
        defer { isSaving = false }
        return await persist()
    }

    /// Saves (the server validates again), then queues the submit on the phone and runs it: the
    /// runner uploads the photos, posts the submit and waits for the job. With no signal the draft
    /// stays queued and the sync sends it later. Does nothing while a save or submit is already running.
    func submit() async -> SubmitOutcome {
        guard !isSubmitting, !isSaving else { return .pending(message: Self.alreadySendingMessage) }
        isSubmitting = true
        defer { isSubmitting = false }
        if let failure = await persist() {
            switch failure {
            case .offline, .server:
                // No answer: the payload waits on the phone and the runner saves it first.
                break
            case .validation, .permissionDenied, .reauthRequired, .notFound:
                return .failed(failure)
            }
        }
        // The save found the draft already sent (a submit whose answer was lost); report where it stands.
        switch draft.status {
        case .submitted:
            return .submitted(number: draft.resultNumber, url: draft.resultURL)
        case .submitting:
            return .pending(message: Self.pendingMessage)
        case .waiting, .draft, .failed:
            break
        }

        draft.status = .submitting
        draft.submitPending = true
        // Sync leaves a submit this recent alone: until the POST lands the server still says `reviewed`.
        draft.updatedAt = .now
        try? store.save()

        switch await runner.run(draft) {
        case .accepted(let response):
            return takeAccepted(response)
        case .stillPending:
            return .pending(message: Self.queuedMessage)
        case .refused(let failure):
            if case .validation(let message) = failure, flagFields(from: message) { return .needsFixes }
            return .failed(failure)
        case .photoFailed:
            // The failed photo's tile says what to fix.
            return .needsFixes
        }
    }

    /// Takes the server's answer to an accepted submit and says what it means for the screen.
    private func takeAccepted(_ response: DraftResponse) -> SubmitOutcome {
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
            return .pending(message: Self.pendingMessage)
        case .extracted, .reviewed:
            return .failed(.server("The RFI was not sent. Try again."))
        }
    }

    // MARK: - Private

    /// Writes the payload to the phone, then PATCHes it. The local copy goes first so a failed
    /// request (offline is the normal jobsite case) still keeps the edits on the phone, and
    /// `pendingPayloadData` holds it until a PATCH lands, so a queued submit saves it first.
    private func persist() async -> APIFailure? {
        if let submitAsDraftOverride { payload.draft = submitAsDraftOverride }
        storeLocally()
        let sent = payload
        draft.pendingPayloadData = try? LocalDraft.encoder.encode(sent)
        try? store.save()
        do {
            let response = try await service.save(id: draft.id, companyID: draft.companyID, payload: sent)
            take(response, sent: sent)
            // Edits made while the PATCH was out are still owed to the server.
            draft.pendingPayloadData = payload == sent ? nil : try? LocalDraft.encoder.encode(payload)
            try? store.save()
            return nil
        } catch {
            let failure = APIFailure(error)
            guard failure == .validation(SubmitRunner.lockedMessage) else { return failure }
            return await refreshLocked()
        }
    }

    /// The server refused a save because the draft is already submitting or submitted (an earlier
    /// submit got through but its answer was lost). Takes the server's copy, which moves the local
    /// status on, instead of failing every save from now on.
    private func refreshLocked() async -> APIFailure? {
        do {
            let response = try await service.get(id: draft.id, companyID: draft.companyID)
            // The server no longer takes edits, so nothing is left to save.
            draft.pendingPayloadData = nil
            take(response)
            if draft.status == .submitted { DraftFiles.removeAll(for: draft.id) }
            return nil
        } catch {
            return APIFailure(error)
        }
    }

    /// Applies a server response here and to the local draft, and saves the local draft. With
    /// `sent`, fields the user changed after that payload was sent keep the user's value and lose
    /// any server flag; the server's value wins only for fields unchanged since.
    private func take(_ response: DraftResponse, sent: RFIDraftPayload? = nil) {
        var merged = response.payload
        var edited: Set<String> = []
        if let sent {
            for access in Self.fieldAccess where access.differs(sent, payload) {
                access.copy(payload, &merged)
                edited.insert(access.field.rawValue)
            }
        }
        payload = merged
        unresolved = response.unresolved.filter { $0.field != Self.formField && !edited.contains($0.field) }
        formMessage = response.unresolved.first { $0.field == Self.formField }?.message
        rememberCandidates(response.unresolved)
        draft.apply(response)
        if !edited.isEmpty { storeLocally() }
        try? store.save()
    }

    /// Writes the current payload into the local draft's stored server copy and saves it.
    private func storeLocally() {
        guard var server = draft.server else { return }
        server.payload = payload
        draft.server = server
        draft.subject = payload.subject
        try? store.save()
    }

    /// Flags the fields a submit 400 lists, with the server's message as the reason. `_form` sets
    /// `formMessage`; names with no review row are skipped, since nothing could clear them.
    /// Returns `false` when the message flags nothing.
    private func flagFields(from message: String) -> Bool {
        guard message.hasPrefix(Self.unresolvedPrefix) else { return false }
        let names = message.dropFirst(Self.unresolvedPrefix.count)
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
        var flagged = false
        for name in names {
            if name == Self.formField {
                formMessage = message
                flagged = true
                continue
            }
            guard RFIField(serverField: name) != nil else { continue }
            let candidates = unresolved.first { $0.field == name }?.candidates ?? []
            unresolved.removeAll { $0.field == name }
            unresolved.append(UnresolvedField(
                field: name,
                reason: .procore_validation,
                message: message,
                candidates: candidates,
                blocking: true
            ))
            flagged = true
        }
        return flagged
    }

    /// Compares and copies one payload field, for merging a save response with later edits.
    private struct FieldAccess {
        let field: RFIField
        let differs: (RFIDraftPayload, RFIDraftPayload) -> Bool
        let copy: (RFIDraftPayload, inout RFIDraftPayload) -> Void

        init<Value: Equatable>(_ field: RFIField, _ keyPath: WritableKeyPath<RFIDraftPayload, Value>) {
            self.field = field
            differs = { $0[keyPath: keyPath] != $1[keyPath: keyPath] }
            copy = { source, target in target[keyPath: keyPath] = source[keyPath: keyPath] }
        }
    }

    private static var fieldAccess: [FieldAccess] {
        RFIField.allCases.map { field in
            switch field {
            case .subject: FieldAccess(field, \.subject)
            case .questionBody: FieldAccess(field, \.question_body)
            case .assigneeIds: FieldAccess(field, \.assignee_ids)
            case .rfiManagerId: FieldAccess(field, \.rfi_manager_id)
            case .locationId: FieldAccess(field, \.location_id)
            case .specificationSectionId: FieldAccess(field, \.specification_section_id)
            case .dueDate: FieldAccess(field, \.due_date)
            case .drawingNumber: FieldAccess(field, \.drawing_number)
            case .scheduleImpact: FieldAccess(field, \.schedule_impact)
            case .costImpact: FieldAccess(field, \.cost_impact)
            case .distributionIds: FieldAccess(field, \.distribution_ids)
            case .responsibleContractorId: FieldAccess(field, \.responsible_contractor_id)
            case .costCodeId: FieldAccess(field, \.cost_code_id)
            case .private: FieldAccess(field, \._private)
            case .draft: FieldAccess(field, \.draft)
            }
        }
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

    /// Whether a field holds a value, for opening "More". `private` counts when on; the `draft`
    /// toggle never does, since it is on by default.
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
        case .draft: false
        }
    }

    /// OpenAPI `date` (`yyyy-MM-dd`) in the device's time zone.
    private static var dateStyle: Date.ISO8601FormatStyle {
        Date.ISO8601FormatStyle(timeZone: .current).year().month().day().dateSeparator(.dash)
    }
}
