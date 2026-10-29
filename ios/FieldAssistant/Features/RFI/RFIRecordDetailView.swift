import FieldAssistantAPI
import SwiftUI

/// One RFI from the on-device mirror as a scrolling page: the number, subject and status, then
/// overview, where, impacts, the question and answers with attachments, the field notes and a link
/// to Procore. Optional fields that are missing are left out.
struct RFIRecordDetailView: View {
    let projectID: Int64
    let procoreID: Int64

    @Environment(AppState.self) private var appState
    @Environment(\.openURL) private var openURL
    /// Decoded values only, never `CachedRecord`: a sync may delete the model while this screen is open.
    @State private var rfi: RFIRecord?
    @State private var dueDate: Date?
    /// Set once the RFI has been shown, so a later sync that removes it says so.
    @State private var wasShown = false

    var body: some View {
        Group {
            if let rfi {
                content(rfi)
            } else if wasShown {
                missing(title: "This RFI is no longer in the project",
                        message: "It was removed in Procore, or you no longer have access to it.")
            } else {
                missing(title: "RFI not on this phone", message: "Sync the project to fetch it again.")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background(Theme.Color.ground.ignoresSafeArea())
        .navigationBarTitleDisplayMode(.inline)
        // Re-read after each sync so the screen follows changes made in Procore.
        .task(id: appState.recordSync?.status) { reload() }
    }

    private func reload() {
        let record = appState.records.record(projectID: projectID, kind: .rfi, procoreID: procoreID)
        rfi = record?.rfi
        dueDate = record?.dueDate
        if rfi != nil { wasShown = true }
    }

    private func missing(title: String, message: String) -> some View {
        ScrollView {
            FailureMessage(title: title, message: message)
                .padding(.top, 40)
        }
    }

    // MARK: - Content

    private func content(_ rfi: RFIRecord) -> some View {
        let fieldNotes = rfi.questions.compactMap { Self.split($0.plain_text_body).notes }
        return ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                header(rfi)
                overview(rfi)
                whereSection(rfi)
                impacts(rfi)
                ForEach(Array(rfi.questions.enumerated()), id: \.element.id) { index, question in
                    questionSections(question, index: index, count: rfi.questions.count)
                }
                if !fieldNotes.isEmpty {
                    DetailSection("Field notes") {
                        ForEach(Array(fieldNotes.enumerated()), id: \.offset) { _, notes in
                            EntryRow(text: notes, meta: nil)
                        }
                    }
                }
                if let link = rfi.link.flatMap(URL.init(string:)) {
                    Button {
                        openURL(link)
                    } label: {
                        HStack(spacing: 10) {
                            Text("Open in Procore")
                            Image(systemName: "arrow.up.right.square")
                                .font(.system(size: 18, weight: .semibold))
                                .accessibilityHidden(true)
                        }
                    }
                    .buttonStyle(PrimaryButtonStyle())
                    .accessibilityLabel("Open in Procore")
                    .accessibilityHint("Opens this RFI in Procore")
                }
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .padding(.bottom, 24)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// "RFI" kicker, the number as the screen title, the subject and the status tag.
    private func header(_ rfi: RFIRecord) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Kicker("RFI", style: .tertiary)
                .accessibilityHidden(true)
            Text(rfi.number.isEmpty ? "Draft RFI" : "#\(rfi.number)")
                .textStyle(Theme.Text.recordNumber)
                .foregroundStyle(Theme.Color.textPrimary)
                .accessibilityLabel(rfi.number.isEmpty ? "Draft RFI" : "RFI \(rfi.number)")
                .accessibilityAddTraits(.isHeader)
            if !rfi.subject.isEmpty {
                Text(rfi.subject)
                    .textStyle(Theme.Text.resultSubject)
                    .foregroundStyle(Theme.Color.textPrimary)
                    .textSelection(.enabled)
            }
            StatusTag(StatusTag.state(forRFIStatus: rfi.status, draft: rfi.draft))
                .padding(.top, 4)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func overview(_ rfi: RFIRecord) -> some View {
        DetailSection("Overview") {
            field("Ball in court", rfi.ball_in_court?.name ?? Self.names(rfi.ball_in_courts))
            field("Due", dueDate.map(RecordFormat.dueDate))
            field("Created", Self.joined(rfi.created_by?.name, rfi.created_at.formatted(date: .abbreviated, time: .shortened)))
            field("RFI manager", rfi.rfi_manager?.name)
            field("Assignees", Self.names(rfi.assignees) ?? rfi.assignee?.name)
            field("Received from", rfi.received_from?.name)
            field("Responsible contractor", rfi.responsible_contractor?.name)
            field("Distribution", Self.names(rfi.distribution_list))
        }
    }

    @ViewBuilder
    private func whereSection(_ rfi: RFIRecord) -> some View {
        let values = [rfi.location?.name, rfi.specification_section?.name, rfi.cost_code?.name, rfi.drawing_number]
        if values.contains(where: { !($0 ?? "").isEmpty }) {
            DetailSection("Where") {
                field("Location", rfi.location?.name)
                field("Spec section", rfi.specification_section?.name)
                field("Cost code", rfi.cost_code?.name)
                field("Drawing number", rfi.drawing_number)
            }
        }
    }

    @ViewBuilder
    private func impacts(_ rfi: RFIRecord) -> some View {
        let cost = Self.impact(rfi.cost_impact)
        let schedule = Self.impact(rfi.schedule_impact)
        if cost != nil || schedule != nil {
            DetailSection("Impacts") {
                field("Cost impact", cost)
                field("Schedule impact", schedule)
            }
        }
    }

    /// The question with its attachments, then one section per answer with its attachments.
    @ViewBuilder
    private func questionSections(_ question: Components.Schemas.RFIQuestion, index: Int, count: Int) -> some View {
        DetailSection(count > 1 ? "Question \(index + 1)" : "Question") {
            EntryRow(
                text: Self.split(question.plain_text_body).question,
                meta: Self.joined(question.created_by?.name, question.question_date?.formatted(date: .abbreviated, time: .shortened))
            )
            ForEach(question.attachments, id: \.id) { attachment in
                AttachmentRow(attachment: attachment, projectID: projectID, rfiID: procoreID)
            }
        }
        ForEach(Array(question.answers.enumerated()), id: \.element.id) { answerIndex, answer in
            DetailSection(question.answers.count > 1 ? "Answer \(answerIndex + 1)" : "Answer") {
                EntryRow(
                    text: answer.plain_text_body,
                    meta: Self.joined(answer.created_by?.name, answer.answer_date?.formatted(date: .abbreviated, time: .shortened))
                )
                ForEach(answer.attachments, id: \.id) { attachment in
                    AttachmentRow(attachment: attachment, projectID: projectID, rfiID: procoreID)
                }
            }
        }
    }

    // MARK: - Rows

    /// A labelled row, or nothing when the value is missing or empty.
    @ViewBuilder
    private func field(_ label: String, _ value: String?) -> some View {
        if let value, !value.isEmpty {
            FormRow(label: label) { Text(value).textSelection(.enabled) }
                .accessibilityElement(children: .combine)
        }
    }

    // MARK: - Formatting

    /// The question text before the "Field notes:" line, and the notes after it (nil when absent).
    static func split(_ body: String) -> (question: String, notes: String?) {
        guard let range = body.range(of: "Field notes:") else { return (body, nil) }
        let question = body[..<range.lowerBound].trimmingCharacters(in: .whitespacesAndNewlines)
        let notes = body[range.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
        return (question, notes.isEmpty ? nil : notes)
    }

    private static func names(_ refs: [Components.Schemas.Ref]) -> String? {
        let names = refs.map(\.name).filter { !$0.isEmpty }
        return names.isEmpty ? nil : names.joined(separator: ", ")
    }

    /// "Pat Field · Oct 20, 2026" from whichever parts are present.
    private static func joined(_ parts: String?...) -> String? {
        let present = parts.compactMap { $0 }.filter { !$0.isEmpty }
        return present.isEmpty ? nil : present.joined(separator: " · ")
    }

    /// "Yes · $1,200" from Procore's impact status and value.
    private static func impact(_ impact: Components.Schemas.ImpactRef?) -> String? {
        guard let impact else { return nil }
        let status: String? = switch impact.status {
        case nil, "": nil
        case "yes_known": "Yes"
        case "yes_unknown": "Yes, amount unknown"
        case "no": "No"
        case "tbd": "TBD"
        case "n_a": "N/A"
        case let other?: other.replacingOccurrences(of: "_", with: " ").capitalized
        }
        return joined(status, impact.value)
    }
}

/// A section of the detail: its kicker over hairline-ruled rows, closed by a hairline.
private struct DetailSection<Content: View>: View {
    let title: String
    let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Kicker(title)
                .padding(.bottom, 8)
                .accessibilityAddTraits(.isHeader)
            content
            HairlineDivider()
        }
    }
}

/// A question, answer or field note: the text at body size with its author and date under it.
private struct EntryRow: View {
    let text: String
    let meta: String?

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            VStack(alignment: .leading, spacing: 6) {
                if !text.isEmpty {
                    Text(text)
                        .textStyle(Theme.Text.body)
                        .foregroundStyle(Theme.Color.textPrimary)
                        .textSelection(.enabled)
                }
                if let meta {
                    Text(meta)
                        .textStyle(Theme.Text.meta12)
                        .foregroundStyle(Theme.Color.tertiary)
                }
            }
            .padding(.vertical, 14)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        }
    }
}
