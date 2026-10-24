import FieldAssistantAPI
import SwiftUI

/// Hosts `Route.review`: loads the local draft, sends it for extraction first if it is still
/// waiting, and builds the review model from the stored server response.
struct ReviewScreen: View {
    let draftID: UUID
    let onSubmitted: () -> Void
    let onLeave: () -> Void

    private enum Load {
        case loading
        case sending
        case ready(LocalDraft, RFIReviewModel)
        case waiting(LocalDraft)
        case failed(LocalDraft, String)
        case missing
    }

    @Environment(AppState.self) private var appState
    @State private var load = Load.loading

    var body: some View {
        Group {
            switch load {
            case .loading:
                ProgressView()
            case .sending:
                ProgressView("Building the RFI…")
            case .ready(let draft, let model):
                RFIReviewView(model: model, draft: draft, onSubmitted: onSubmitted, onRecheck: { await recheck(draft) })
                    .id(ObjectIdentifier(model))
            case .waiting(let draft):
                ContentUnavailableView {
                    Label("No signal", systemImage: "wifi.slash")
                } description: {
                    Text("Recording works; sending waits. This draft is saved on the phone and builds once you have a connection.")
                } actions: {
                    actionButton("Try again") { Task { await send(draft) } }
                    Button("Back to Home", action: onLeave).frame(minHeight: 44)
                }
            case .failed(let draft, let message):
                ContentUnavailableView {
                    Label("Couldn't build the RFI", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(message)
                } actions: {
                    actionButton("Try again") { Task { await send(draft) } }
                    Button("Back to Home", action: onLeave).frame(minHeight: 44)
                }
            case .missing:
                ContentUnavailableView("Draft not found", systemImage: "doc.questionmark", description: Text("This draft is no longer on the phone."))
            }
        }
        .navigationTitle("Review RFI")
        .navigationBarTitleDisplayMode(.inline)
        .task { await start() }
    }

    private func start() async {
        guard case .loading = load else { return }
        guard let draft = appState.ownedDraft(id: draftID) else {
            load = .missing
            return
        }
        if draft.status == .submitted {
            onSubmitted()
            return
        }
        if draft.server == nil {
            await send(draft)
        } else {
            show(draft)
        }
    }

    private func send(_ draft: LocalDraft) async {
        load = .sending
        switch await appState.send(draft) {
        case .sent: show(draft)
        case .waiting: load = .waiting(draft)
        case .failed(let message): load = .failed(draft, message)
        }
    }

    private func show(_ draft: LocalDraft) {
        guard let response = draft.server else {
            load = .waiting(draft)
            return
        }
        let model = RFIReviewModel(
            draft: draft,
            response: response,
            lookups: appState.lookups,
            service: appState.draftService,
            store: appState.drafts,
            submitAsDraftOverride: appState.settings.submitAsDraft
        )
        load = .ready(draft, model)
    }

    /// Asks the server where a draft that is being sent stands, and reopens it with the answer.
    private func recheck(_ draft: LocalDraft) async -> String? {
        do {
            let response = try await appState.draftService.get(id: draft.id, companyID: draft.companyID)
            draft.apply(response)
            try? appState.drafts.save()
        } catch {
            return APIFailure(error).userMessage
        }
        switch draft.status {
        case .submitted:
            DraftFiles.removeAll(for: draft.id)
            onSubmitted()
        case .submitting:
            return "Procore has not confirmed it yet."
        case .waiting, .draft, .failed:
            show(draft)
        }
        return nil
    }

    private func actionButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).font(.headline).frame(minWidth: 160, minHeight: 44)
        }
        .buttonStyle(.borderedProminent)
        .accessibilityLabel(title)
    }
}

/// The extracted RFI as editable rows: flagged fields first ask for a choice, the rest can be
/// changed in place or through a lookup picker. Nothing reaches Procore without Submit.
struct RFIReviewView: View {
    let model: RFIReviewModel
    let draft: LocalDraft
    let onSubmitted: () -> Void
    /// Asks the server about a draft that is being sent; returns a message to show, if any.
    let onRecheck: () async -> String?

    /// A field whose lookup picker is pushed.
    private struct PickerTarget: Hashable, Identifiable {
        let field: RFIField
        var id: RFIField { field }
    }

    @Environment(AppState.self) private var appState
    @State private var picker: PickerTarget?
    /// Set while Procore has not confirmed a submit; the screen stays read-only until it does.
    @State private var pendingMessage: String?
    @State private var isRechecking = false
    @State private var failure: APIFailure?
    @State private var saveNote: String?
    @State private var scrollTarget: RFIField?
    /// Bumped to scroll to the header, where a message from Procore not tied to a row shows.
    @State private var scrollToTopRequest = 0
    private static let topID = "top"

    private static let sendingMessage = "Sending to Procore. It will update automatically."

    init(
        model: RFIReviewModel,
        draft: LocalDraft,
        onSubmitted: @escaping () -> Void = {},
        onRecheck: @escaping () async -> String? = { nil }
    ) {
        self.model = model
        self.draft = draft
        self.onSubmitted = onSubmitted
        self.onRecheck = onRecheck
        _pendingMessage = State(initialValue: draft.status == .submitting ? Self.sendingMessage : nil)
    }

    /// Editing stops while a save or submit runs and while a submit waits for Procore.
    private var isLocked: Bool { model.isSaving || model.isSubmitting || pendingMessage != nil }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 0) {
                    header
                        .id(Self.topID)
                    VStack(spacing: 0) {
                        primaryRows
                        moreGroup
                        photosRow
                    }
                    .disabled(isLocked)
                    TranscriptDisclosure(transcript: draft.transcript)
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: scrollTarget) { _, target in
                guard let target else { return }
                withAnimation { proxy.scrollTo(target, anchor: .top) }
                scrollTarget = nil
            }
            .onChange(of: scrollToTopRequest) {
                withAnimation { proxy.scrollTo(Self.topID, anchor: .top) }
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(spacing: 0) {
                if let formMessage = model.formMessage {
                    // Repeated by the footer so it stays in view while the rows scroll.
                    Label(formMessage, systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote)
                        .foregroundStyle(.orange)
                        .lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 6)
                        .background(.bar)
                        .accessibilityLabel("Procore says: \(formMessage)")
                }
                if let saveNote {
                    Text(saveNote)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 6)
                        .background(.bar)
                }
                ReviewFooter(
                    blockingCount: model.unresolvedCount,
                    isSubmitting: model.isSubmitting,
                    onSave: { Task { await save() } },
                    onSubmit: { Task { await submit() } }
                )
                .disabled(model.isSaving || pendingMessage != nil)
            }
        }
        .navigationDestination(item: $picker) { target in
            pickerView(for: target.field)
        }
        .alert(
            "Couldn't send the RFI",
            isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } }),
            presenting: failure
        ) { _ in
            Button("OK", role: .cancel) {}
        } message: { failure in
            Text(failure.userMessage)
        }
    }

    // MARK: - Header

    @ViewBuilder
    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            if model.unresolvedCount > 0 {
                Text(Self.needsYou(model.unresolvedCount))
                    .font(.caption.weight(.bold))
                    .textCase(.uppercase)
                    .foregroundStyle(.white)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
                    .background(Capsule().fill(Color.orange))
                    .accessibilityLabel(Self.needsYou(model.unresolvedCount))
            }
            if let formMessage = model.formMessage {
                Label(formMessage, systemImage: "exclamationmark.triangle.fill")
                    .font(.subheadline)
                    .foregroundStyle(.orange)
                    .accessibilityLabel("Procore says: \(formMessage)")
            }
            if let pendingMessage {
                HStack(spacing: 12) {
                    ProgressView()
                    Text(pendingMessage).font(.subheadline)
                    Spacer(minLength: 0)
                    Button {
                        Task { await recheck() }
                    } label: {
                        if isRechecking { ProgressView() } else { Text("Check").font(.subheadline.weight(.semibold)) }
                    }
                    .frame(minWidth: 44, minHeight: 44)
                    .disabled(isRechecking)
                    .accessibilityLabel("Check whether Procore has the RFI")
                }
                .padding(12)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color(.secondarySystemBackground)))
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, hasHeader ? 12 : 0)
    }

    private var hasHeader: Bool {
        model.unresolvedCount > 0 || model.formMessage != nil || pendingMessage != nil
    }

    static func needsYou(_ count: Int) -> String {
        count == 1 ? "1 field needs you" : "\(count) fields need you"
    }

    // MARK: - Rows

    @ViewBuilder
    private var primaryRows: some View {
        fieldRow(.subject)
        fieldRow(.questionBody)
        fieldRow(.assigneeIds)
        fieldRow(.rfiManagerId)
        fieldRow(.locationId)
        fieldRow(.specificationSectionId)
        pairOrRows(.dueDate, .drawingNumber)
        pairOrRows(.scheduleImpact, .costImpact)
    }

    /// Shows two fields side by side, or one under the other when either needs attention.
    @ViewBuilder
    private func pairOrRows(_ left: RFIField, _ right: RFIField) -> some View {
        if model.unresolved(for: left) == nil, model.unresolved(for: right) == nil {
            PairedRow(leftLabel: left.label, rightLabel: right.label) {
                compactControl(left)
            } right: {
                compactControl(right)
            }
            .id(left)
        } else {
            fieldRow(left)
            fieldRow(right)
        }
    }

    private var moreGroup: some View {
        VStack(spacing: 0) {
            DisclosureGroup(isExpanded: Binding(get: { model.moreExpanded }, set: { model.moreExpanded = $0 })) {
                VStack(spacing: 0) {
                    fieldRow(.distributionIds)
                    fieldRow(.responsibleContractorId)
                    fieldRow(.costCodeId)
                    toggleRow(.private)
                    toggleRow(.draft)
                }
            } label: {
                Text("More")
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .accessibilityLabel("More fields")
            }
            .padding(.horizontal, 16)
            Divider()
        }
    }

    @ViewBuilder
    private var photosRow: some View {
        let photos = draft.photos.sorted { $0.createdAt < $1.createdAt }
        if !photos.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                ReviewLabel(text: "Photos · \(photos.count)")
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(photos) { photo in
                            PhotoThumbnail(url: DraftFiles.photoURL(draftID: draft.id, fileName: photo.fileName), side: 64)
                                .accessibilityLabel("Photo")
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 8)
            .padding(.horizontal, 16)
            Divider()
        }
    }

    /// One field as a full-width row: the flagged form when the field has a flag, else its editor.
    @ViewBuilder
    private func fieldRow(_ field: RFIField) -> some View {
        Group {
            if let flag = model.unresolved(for: field), let kind = field.lookupKind {
                UnresolvedRow(
                    label: field.label,
                    field: flag,
                    kind: kind,
                    onChoose: { model.choose($0, for: field) },
                    onSomeoneElse: { picker = PickerTarget(field: field) }
                )
            } else if field.lookupKind != nil {
                lookupRow(field)
            } else {
                EditableRow(label: field.label, flag: model.unresolved(for: field)) {
                    fullControl(field)
                }
            }
        }
        .id(field)
    }

    private func lookupRow(_ field: RFIField) -> some View {
        ReviewRow(label: field.label, chevron: true, action: { picker = PickerTarget(field: field) }) {
            if let value = displayValue(field) {
                Text(value).foregroundStyle(.primary)
            } else {
                Text(Self.placeholder(field)).foregroundStyle(.secondary)
            }
        }
        .accessibilityHint("Opens the \(field.lookupKind?.title.lowercased() ?? "list") to choose from")
    }

    private func toggleRow(_ field: RFIField) -> some View {
        let override = field == .draft ? appState.settings.submitAsDraft : nil
        return VStack(spacing: 0) {
            Toggle(isOn: Binding(
                get: { override ?? (field == .draft ? model.payload.draft : model.payload._private) },
                set: { model.setBool($0, for: field) }
            )) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(field.label)
                    if override != nil {
                        Text("Set in Settings").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            .disabled(override != nil)
            .frame(minHeight: 44)
            .padding(.vertical, 4)
            .padding(.horizontal, 16)
            Divider()
        }
        .id(field)
    }

    /// The editor for a typed field in a full-width row.
    @ViewBuilder
    private func fullControl(_ field: RFIField) -> some View {
        switch field {
        case .subject:
            TextField("Subject", text: textBinding(field), axis: .vertical)
                .accessibilityLabel("Subject")
        case .questionBody:
            TextEditor(text: textBinding(field))
                .frame(minHeight: 120)
                .scrollContentBackground(.hidden)
                // TextEditor insets its text; pull it back so it lines up with the label and the other rows.
                .padding(.horizontal, -5)
                .padding(.vertical, -8)
                .accessibilityLabel("Question")
        case .dueDate:
            DueDateControl(date: model.dueDate) { model.setDate($0) }
        case .scheduleImpact, .costImpact:
            ImpactControl(label: field.label, impact: impact(field)) { model.setImpact($0, for: field) }
        default:
            EmptyView()
        }
    }

    /// The editor for a field in one half of a paired row.
    @ViewBuilder
    private func compactControl(_ field: RFIField) -> some View {
        if field.lookupKind != nil {
            Button {
                picker = PickerTarget(field: field)
            } label: {
                Text(displayValue(field) ?? Self.placeholder(field))
                    .foregroundStyle(displayValue(field) == nil ? Color.secondary : Color.primary)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(field.label), \(displayValue(field) ?? "not set")")
            .accessibilityHint("Opens the \(field.lookupKind?.title.lowercased() ?? "list") to choose from")
        } else {
            fullControl(field)
        }
    }

    // MARK: - Picker

    @ViewBuilder
    private func pickerView(for field: RFIField) -> some View {
        if let kind = field.lookupKind {
            let cached = appState.lookups.set(projectID: draft.projectID, kind: kind)
            let rows = cached?.rows ?? []
            let reason = cached?.unavailable == true ? (cached?.reason ?? "Procore did not share this list.") : nil
            if field.isMulti {
                LookupPickerView(
                    kind: kind,
                    rows: rows,
                    unavailableReason: reason,
                    selection: .constant(nil),
                    multi: true,
                    selections: Binding(get: { ids(field) }, set: { model.setIDs($0, for: field) })
                )
            } else {
                LookupPickerView(
                    kind: kind,
                    rows: rows,
                    unavailableReason: reason,
                    selection: Binding(
                        get: { selectedID(field, rows: rows) },
                        set: { id in
                            guard let id, let row = rows.first(where: { $0.id == id }) else { return }
                            model.choose(row, for: field)
                            picker = nil
                        }
                    )
                )
            }
        }
    }

    // MARK: - Values

    private func ids(_ field: RFIField) -> [Int64] {
        switch field {
        case .assigneeIds: model.payload.assignee_ids
        case .distributionIds: model.payload.distribution_ids
        default: selectedID(field, rows: []).map { [$0] } ?? []
        }
    }

    private func selectedID(_ field: RFIField, rows: [LookupRow]) -> Int64? {
        switch field {
        case .rfiManagerId: model.payload.rfi_manager_id
        case .locationId: model.payload.location_id
        case .specificationSectionId: model.payload.specification_section_id
        case .responsibleContractorId: model.payload.responsible_contractor_id
        case .costCodeId: model.payload.cost_code_id
        case .drawingNumber:
            rows.first { ($0.extra?.additionalProperties["number"] ?? $0.label) == model.payload.drawing_number }?.id
        default: nil
        }
    }

    /// The value a lookup row shows, or `nil` when the field is empty.
    private func displayValue(_ field: RFIField) -> String? {
        guard let kind = field.lookupKind else { return nil }
        if field == .drawingNumber { return model.payload.drawing_number }
        let values = ids(field).map { model.label(forID: $0, kind: kind) ?? "Unknown (#\($0))" }
        return values.isEmpty ? nil : values.joined(separator: ", ")
    }

    private static func placeholder(_ field: RFIField) -> String {
        switch field.lookupKind {
        case .users: field.isMulti ? "Choose people" : "Choose a person"
        case .locations: "Choose a location"
        case .specSections: "Choose a spec section"
        case .drawings: "Choose a drawing"
        case .vendors: "Choose a company"
        case .costCodes: "Choose a cost code"
        case nil: "Not set"
        }
    }

    private func textBinding(_ field: RFIField) -> Binding<String> {
        Binding(
            get: {
                switch field {
                case .subject: model.payload.subject ?? ""
                case .questionBody: model.payload.question_body ?? ""
                default: ""
                }
            },
            set: { model.setText($0, for: field) }
        )
    }

    private func impact(_ field: RFIField) -> Components.Schemas.Impact? {
        field == .scheduleImpact ? model.payload.schedule_impact : model.payload.cost_impact
    }

    // MARK: - Actions

    private func save() async {
        guard !isLocked else { return }
        saveNote = nil
        guard let failure = await model.saveDraft() else {
            // The server may say an earlier submit got through after all.
            switch draft.status {
            case .submitted: onSubmitted()
            case .submitting: pendingMessage = Self.sendingMessage
            case .waiting, .draft, .failed: saveNote = "Draft saved"
            }
            return
        }
        if Task.isCancelled { return }
        if failure == .offline {
            saveNote = "Saved on this phone. Sent the next time you save or submit with signal."
        } else {
            self.failure = failure
        }
    }

    private func submit() async {
        // A tap while a save or submit runs is ignored rather than reported.
        guard !isLocked else { return }
        saveNote = nil
        switch await model.submit() {
        case .submitted:
            onSubmitted()
        case .needsFixes:
            let flagged = (RFIField.primary + RFIField.more).first { model.unresolved(for: $0) != nil }
            if let flagged, RFIField.more.contains(flagged) { model.moreExpanded = true }
            if let flagged {
                scrollTarget = flagged
            } else if model.formMessage != nil {
                // Only a form-level message: show the header callout.
                scrollToTopRequest += 1
            }
        case .pending(let message):
            pendingMessage = message
        case .failed(let failure):
            if Task.isCancelled { return }
            self.failure = failure
        }
    }

    private func recheck() async {
        isRechecking = true
        defer { isRechecking = false }
        if let message = await onRecheck() { pendingMessage = message }
    }
}

/// A full-width row whose content is an editor. Unlike `ReviewRow` it keeps the editor its own
/// accessibility element, and shows a flag's reason under the label.
private struct EditableRow<Content: View>: View {
    let label: String
    let flag: UnresolvedField?
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                if flag?.blocking == true {
                    Rectangle().fill(Color.orange).frame(width: 4)
                }
                VStack(alignment: .leading, spacing: 4) {
                    HStack(alignment: .firstTextBaseline) {
                        ReviewLabel(text: flag?.blocking == false ? "Optional: \(label)" : label, flagged: flag?.blocking == true)
                        if let flag {
                            Spacer(minLength: 8)
                            Text(UnresolvedRow.reasonText(for: flag))
                                .font(.footnote)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.trailing)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    content()
                        .font(.body)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.vertical, 8)
                .padding(.horizontal, flag?.blocking == true ? 12 : 16)
                .frame(minHeight: 44)
            }
            .background(flag?.blocking == true ? Color.orange.opacity(0.12) : Color.clear)
            Divider()
        }
    }
}

/// Due date: a compact date picker with a clear button, or "Add date" when empty.
private struct DueDateControl: View {
    let date: Date?
    let onChange: (Date?) -> Void

    var body: some View {
        if let date {
            HStack(spacing: 4) {
                DatePicker("Due", selection: Binding(get: { date }, set: { onChange($0) }), displayedComponents: .date)
                    .labelsHidden()
                    .accessibilityLabel("Due date")
                Button {
                    onChange(nil)
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                        .frame(minWidth: 44, minHeight: 44)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear due date")
            }
        } else {
            Button {
                onChange(Calendar.current.date(byAdding: .day, value: 7, to: Calendar.current.startOfDay(for: .now)))
            } label: {
                Text("Add date")
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(Color.accentColor)
            .accessibilityLabel("Add a due date")
        }
    }
}

/// Schedule or cost impact: a status menu, plus the value when the impact is known.
private struct ImpactControl: View {
    let label: String
    let impact: Components.Schemas.Impact?
    let onChange: (Components.Schemas.Impact?) -> Void

    private enum Choice: Hashable, CaseIterable {
        case notStated, yesKnown, yesUnknown, no, tbd

        var title: String {
            switch self {
            case .notStated: "Not stated"
            case .yesKnown: "Yes, known"
            case .yesUnknown: "Yes, unknown"
            case .no: "No"
            case .tbd: "TBD"
            }
        }

        var status: Components.Schemas.Impact.statusPayload? {
            switch self {
            case .notStated: nil
            case .yesKnown: .yes_known
            case .yesUnknown: .yes_unknown
            case .no: .no
            case .tbd: .tbd
            }
        }

        init(_ status: Components.Schemas.Impact.statusPayload?) {
            self = Self.allCases.first { $0.status == status } ?? .notStated
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Picker(label, selection: Binding(get: { Choice(impact?.status) }, set: { choose($0) })) {
                ForEach(Choice.allCases, id: \.self) { Text($0.title).tag($0) }
            }
            .pickerStyle(.menu)
            .labelsHidden()
            .frame(minHeight: 44)
            .accessibilityLabel(label)
            if impact?.status == .yes_known {
                TextField("Value", text: Binding(
                    get: { impact?.value ?? "" },
                    set: { onChange(.init(status: .yes_known, value: $0.isEmpty ? nil : $0)) }
                ))
                .textFieldStyle(.roundedBorder)
                .frame(minHeight: 44)
                .accessibilityLabel("\(label) value")
            }
        }
    }

    private func choose(_ choice: Choice) {
        guard let status = choice.status else { return onChange(nil) }
        onChange(.init(status: status, value: status == .yes_known ? impact?.value : nil))
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    let appState = AppState.preview(container: container)
    let json = """
    {"id":"7f0c2d4e-1b2a-4c3d-9e8f-001122334455","feature":"rfi","project_id":7,"status":"extracted",
     "payload":{"subject":"Twisted steel column at the air handler platform",
       "question_body":"Can we shim and weld it where it sits, or does it need to be reset?",
       "assignee_ids":[],"distribution_ids":[],"private":false,"draft":true,"due_date":"2026-10-31",
       "schedule_impact":{"status":"tbd"}},
     "unresolved":[{"field":"assignee_ids","reason":"ambiguous","blocking":true,
       "candidates":[{"id":11,"label":"Ben West","extra":{"role":"Architect"}},{"id":12,"label":"Ben Wu"}]},
       {"field":"location_id","reason":"not_found","blocking":false,"candidates":[]}],
     "confidence":{},"transcript":"Question for Ben West. The steel column at the air handler platform is twisted.",
     "created_at":"2026-10-24T17:45:12Z","updated_at":"2026-10-24T17:45:12Z"}
    """
    let response = try! LocalDraft.decoder.decode(DraftResponse.self, from: Data(json.utf8))
    let draft = LocalDraft(id: UUID(uuidString: "7F0C2D4E-1B2A-4C3D-9E8F-001122334455")!, companyID: 1, projectID: 7, transcript: response.transcript)
    draft.apply(response)
    let model = RFIReviewModel(
        draft: draft,
        response: response,
        lookups: appState.lookups,
        service: appState.draftService,
        store: appState.drafts,
        submitAsDraftOverride: nil
    )
    return NavigationStack {
        RFIReviewView(model: model, draft: draft)
            .navigationTitle("Review RFI")
            .navigationBarTitleDisplayMode(.inline)
    }
    .environment(appState)
    .modelContainer(container)
}
