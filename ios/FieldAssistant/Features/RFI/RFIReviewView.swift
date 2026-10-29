import FieldAssistantAPI
import SwiftUI

/// Hosts `Route.review`: loads the local draft, sends it for extraction first if it is still
/// waiting, and builds the review model from the stored server response.
struct ReviewScreen: View {
    let draftID: UUID
    /// The screen under this one, named by the back link.
    let backTitle: String
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
                    .tint(Theme.Color.tertiary)
            case .sending:
                ProgressView {
                    Text("Building the RFI…")
                        .textStyle(Theme.Text.meta13)
                        .foregroundStyle(Theme.Color.muted)
                }
                .tint(Theme.Color.tertiary)
            case .ready(let draft, let model):
                RFIReviewView(model: model, draft: draft, backTitle: backTitle, onSubmitted: onSubmitted,
                              onRecheck: { await recheck(draft) })
                    .id(ObjectIdentifier(model))
            case .waiting(let draft):
                FailureMessage(
                    title: "No signal",
                    message: "Recording works; sending waits. This draft is saved on the phone and builds once you have a connection."
                ) {
                    retryActions(draft)
                }
            case .failed(let draft, let message):
                FailureMessage(title: "Couldn't build the RFI", message: message) {
                    retryActions(draft)
                }
            case .missing:
                FailureMessage(title: "Draft not found", message: "This draft is no longer on the phone.")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.Color.ground.ignoresSafeArea())
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

    /// "Try again" sends the draft again; "Back to Home" leaves it on the phone.
    private func retryActions(_ draft: LocalDraft) -> some View {
        VStack(spacing: 4) {
            Button("Try again") { Task { await send(draft) } }
                .buttonStyle(PrimaryButtonStyle(height: 52))
                .accessibilityLabel("Try again")
            Button("Back to Home", action: onLeave)
                .buttonStyle(TextLinkButtonStyle())
        }
    }
}

/// The extracted RFI as editable rows: flagged fields first ask for a choice, the rest can be
/// changed in place or through a lookup picker. Nothing reaches Procore without Submit.
struct RFIReviewView: View {
    let model: RFIReviewModel
    let draft: LocalDraft
    /// The screen the back link returns to.
    let backTitle: String
    let onSubmitted: () -> Void
    /// Asks the server about a draft that is being sent; returns a message to show, if any.
    let onRecheck: () async -> String?

    /// A field whose lookup picker is pushed.
    private struct PickerTarget: Hashable, Identifiable {
        let field: RFIField
        var id: RFIField { field }
    }

    @Environment(AppState.self) private var appState
    @Environment(\.dismiss) private var dismiss
    @Environment(\.colorScheme) private var colorScheme
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
        backTitle: String = "Home",
        onSubmitted: @escaping () -> Void = {},
        onRecheck: @escaping () async -> String? = { nil }
    ) {
        self.model = model
        self.draft = draft
        self.backTitle = backTitle
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
                .padding(.horizontal, 20)
                .padding(.bottom, 24)
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
        .background(Theme.Color.ground.ignoresSafeArea())
        .safeAreaInset(edge: .bottom, spacing: 0) {
            ReviewFooter(
                blockingCount: model.unresolvedCount,
                isSubmitting: model.isSubmitting,
                onSave: { Task { await save() } },
                onSubmit: { Task { await submit() } }
            ) {
                footerNotes
            }
            .disabled(model.isSaving || pendingMessage != nil)
        }
        // The back link replaces the system bar; the edge swipe still pops.
        .toolbar(.hidden, for: .navigationBar)
        .keepsInteractivePop()
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

    /// The count tag: 11/700 .06em uppercase.
    private static let countTagStyle = Theme.TextStyle(11, .bold, relativeTo: .caption2, em: 0.06)

    /// Back link, title with the count tag, then any message from Procore or a submit in flight.
    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            backLink
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .bottom, spacing: 8) {
                    title
                    Spacer(minLength: 0)
                    countTag
                }
                VStack(alignment: .leading, spacing: 8) {
                    title
                    countTag
                }
            }
            if let formMessage = model.formMessage {
                Label(formMessage, systemImage: "exclamationmark.triangle.fill")
                    .textStyle(Theme.Text.meta13Medium)
                    .foregroundStyle(Theme.Color.accent)
                    .padding(.top, 8)
                    .accessibilityLabel("Procore says: \(formMessage)")
            }
            if let pendingMessage {
                HStack(spacing: 12) {
                    ProgressView()
                        .tint(Theme.Color.tertiary)
                    Text(pendingMessage)
                        .textStyle(Theme.Text.meta13)
                        .foregroundStyle(Theme.Color.textPrimary)
                    Spacer(minLength: 0)
                    Button {
                        Task { await recheck() }
                    } label: {
                        if isRechecking { ProgressView().tint(Theme.Color.tertiary) } else { Text("Check") }
                    }
                    .buttonStyle(TextLinkButtonStyle(size: 13))
                    .frame(minWidth: 44)
                    .disabled(isRechecking)
                    .accessibilityLabel("Check whether Procore has the RFI")
                }
                .padding(12)
                .background(Theme.Color.surface, in: RoundedRectangle(cornerRadius: 8))
                .padding(.top, 8)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.bottom, 14)
    }

    /// "‹ Home" or "‹ Drafts": 14/600 accent with a 16 pt semibold chevron; pops this screen.
    private var backLink: some View {
        Button {
            dismiss()
        } label: {
            HStack(spacing: 2) {
                Image(systemName: "chevron.left")
                    .font(.system(size: 16, weight: .semibold))
                Text(backTitle)
            }
        }
        .buttonStyle(TextLinkButtonStyle(size: 14))
        .accessibilityLabel("Back to \(backTitle)")
    }

    private var title: some View {
        Text("Review RFI")
            .textStyle(Theme.Text.screenTitle)
            .foregroundStyle(Theme.Color.textPrimary)
            .accessibilityAddTraits(.isHeader)
    }

    /// "1 field needs you": accent on the tint in light mode, white on accent in dark; hidden at zero.
    @ViewBuilder
    private var countTag: some View {
        if model.unresolvedCount > 0 {
            let dark = colorScheme == .dark
            Text(Self.needsYou(model.unresolvedCount).uppercased())
                .textStyle(Self.countTagStyle)
                .foregroundStyle(dark ? Color.white : Theme.Color.accent)
                .padding(.horizontal, 9)
                .padding(.vertical, 5)
                .background(dark ? Theme.Color.accent : Theme.Color.accentTint, in: RoundedRectangle(cornerRadius: 4))
                .padding(.bottom, 6)
                .accessibilityLabel(Self.needsYou(model.unresolvedCount))
        }
    }

    /// The Procore message (repeated here so it stays in view while the rows scroll) and the save note.
    @ViewBuilder
    private var footerNotes: some View {
        if let formMessage = model.formMessage {
            Label(formMessage, systemImage: "exclamationmark.triangle.fill")
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.accent)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityLabel("Procore says: \(formMessage)")
        }
        if let saveNote {
            Text(saveNote)
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.muted)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
        }
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

    /// The fields people rarely set, behind a collapsed "More" row.
    private var moreGroup: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            DisclosureRow(
                title: "More",
                accessibilityTitle: "More fields",
                expanded: Binding(get: { model.moreExpanded }, set: { model.moreExpanded = $0 })
            )
            if model.moreExpanded {
                fieldRow(.distributionIds)
                fieldRow(.responsibleContractorId)
                fieldRow(.costCodeId)
                toggleRow(.private)
                toggleRow(.draft)
            }
        }
    }

    @ViewBuilder
    private var photosRow: some View {
        let photos = draft.photos.sorted { $0.createdAt < $1.createdAt }
        if !photos.isEmpty {
            VStack(alignment: .leading, spacing: 0) {
                HairlineDivider()
                VStack(alignment: .leading, spacing: 10) {
                    Kicker("Photos · \(photos.count)", style: .label, color: Theme.Color.tertiary)
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 10) {
                            ForEach(photos) { photo in
                                PhotoThumbnail(url: DraftFiles.photoURL(draftID: draft.id, fileName: photo.fileName), side: 64)
                                    .accessibilityLabel("Photo")
                            }
                        }
                    }
                }
                .padding(.vertical, 14)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
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
                    selectedIDs: Set(ids(field)),
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
                Text(value).foregroundStyle(Theme.Color.textPrimary)
            } else {
                Text(Self.placeholder(field)).foregroundStyle(Theme.Color.tertiary)
            }
        }
        .accessibilityHint("Opens the \(field.lookupKind?.title.lowercased() ?? "list") to choose from")
    }

    private func toggleRow(_ field: RFIField) -> some View {
        let override = field == .draft ? appState.settings.submitAsDraft : nil
        return VStack(spacing: 0) {
            HairlineDivider()
            Toggle(isOn: Binding(
                get: { override ?? (field == .draft ? model.payload.draft : model.payload._private) },
                set: { model.setBool($0, for: field) }
            )) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(field.label)
                        .textStyle(Theme.Text.rowTitle)
                        .foregroundStyle(Theme.Color.textPrimary)
                    if override != nil {
                        Text("Set in Settings")
                            .textStyle(Theme.Text.meta12)
                            .foregroundStyle(Theme.Color.muted)
                    }
                }
            }
            .tint(Theme.Color.brand)
            .disabled(override != nil)
            .frame(minHeight: 44)
            .padding(.vertical, 14)
        }
        .id(field)
    }

    /// The editor for a typed field in a full-width row.
    @ViewBuilder
    private func fullControl(_ field: RFIField) -> some View {
        switch field {
        case .subject:
            TextField("Subject", text: textBinding(field), axis: .vertical)
                .textStyle(Theme.Text.fieldTitle)
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
                    .foregroundStyle(displayValue(field) == nil ? Theme.Color.tertiary : Theme.Color.textPrimary)
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
/// accessibility element; a flagged field sits in the flagged card with its reason beside the label.
private struct EditableRow<Content: View>: View {
    let label: String
    let flag: UnresolvedField?
    @ViewBuilder var content: () -> Content

    var body: some View {
        if let flag {
            FlaggedCard(label: label, reason: UnresolvedRow.reasonText(for: flag), blocking: flag.blocking) {
                content()
                    .textStyle(Theme.Text.body)
                    .foregroundStyle(Theme.Color.textPrimary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        } else {
            FormRow(label: label) {
                content()
            }
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
                    .tint(Theme.Color.accent)
                    .accessibilityLabel("Due date")
                Button {
                    onChange(nil)
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(Theme.Color.tertiary)
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
            .foregroundStyle(Theme.Color.accent)
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
            Menu {
                Picker(label, selection: Binding(get: { Choice(impact?.status) }, set: { choose($0) })) {
                    ForEach(Choice.allCases, id: \.self) { Text($0.title).tag($0) }
                }
            } label: {
                HStack(spacing: 6) {
                    Text(Choice(impact?.status).title)
                        .foregroundStyle(impact == nil ? Theme.Color.tertiary : Theme.Color.textPrimary)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(Theme.Color.tertiary)
                        .accessibilityHidden(true)
                }
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
            }
            .accessibilityLabel(label)
            .accessibilityValue(Choice(impact?.status).title)
            if impact?.status == .yes_known {
                TextField("Value", text: Binding(
                    get: { impact?.value ?? "" },
                    set: { onChange(.init(status: .yes_known, value: $0.isEmpty ? nil : $0)) }
                ))
                .textFieldStyle(.plain)
                .padding(.horizontal, 10)
                .frame(minHeight: 44)
                .background(Theme.Color.surface, in: RoundedRectangle(cornerRadius: 6))
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
        RFIReviewView(model: model, draft: draft, backTitle: "Home")
            .navigationTitle("Review RFI")
            .navigationBarTitleDisplayMode(.inline)
    }
    .environment(appState)
    .modelContainer(container)
}
