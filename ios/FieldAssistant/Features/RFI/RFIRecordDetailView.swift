import FieldAssistantAPI
import SwiftUI

/// One RFI from the on-device mirror: overview, where, impacts, the question thread with
/// attachments, the field notes and a link to Procore. Optional fields that are missing are left out.
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
                ContentUnavailableView("This RFI is no longer in the project", systemImage: "doc.questionmark",
                                       description: Text("It was removed in Procore, or you no longer have access to it."))
            } else {
                ContentUnavailableView("RFI not on this phone", systemImage: "doc.questionmark",
                                       description: Text("Sync the project to fetch it again."))
            }
        }
        .navigationTitle(rfi.map { $0.number.isEmpty ? "Draft RFI" : "RFI #\($0.number)" } ?? "RFI")
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

    // MARK: - Content

    private func content(_ rfi: RFIRecord) -> some View {
        let fieldNotes = rfi.questions.compactMap { Self.split($0.plain_text_body).notes }
        return List {
            overview(rfi)
            whereSection(rfi)
            impacts(rfi)
            ForEach(Array(rfi.questions.enumerated()), id: \.element.id) { index, question in
                questionSection(question, index: index, count: rfi.questions.count)
            }
            if !fieldNotes.isEmpty {
                Section {
                    ForEach(Array(fieldNotes.enumerated()), id: \.offset) { _, notes in
                        Text(notes).textSelection(.enabled)
                    }
                } header: {
                    Kicker("Field notes")
                }
            }
            if let link = rfi.link.flatMap(URL.init(string:)) {
                Section {
                    Button {
                        openURL(link)
                    } label: {
                        Label("Open in Procore", systemImage: "arrow.up.right.square")
                            .foregroundStyle(Theme.Color.accent)
                            .frame(minHeight: 44)
                    }
                    .accessibilityLabel("Open in Procore")
                    .accessibilityHint("Opens this RFI in Procore")
                }
            }
        }
    }

    @ViewBuilder
    private func overview(_ rfi: RFIRecord) -> some View {
        Section {
            if !rfi.number.isEmpty { field("Number", "#\(rfi.number)") }
            field("Subject", rfi.subject)
            ReviewField(label: "Status") { StatusTag(StatusTag.state(forRFIStatus: rfi.status, draft: rfi.draft)) }
            field("Ball in court", rfi.ball_in_court?.name ?? Self.names(rfi.ball_in_courts))
            field("Due", dueDate.map(RecordFormat.dueDate))
            field("Created", Self.joined(rfi.created_by?.name, rfi.created_at.formatted(date: .abbreviated, time: .shortened)))
            field("RFI manager", rfi.rfi_manager?.name)
            field("Assignees", Self.names(rfi.assignees) ?? rfi.assignee?.name)
            field("Received from", rfi.received_from?.name)
            field("Responsible contractor", rfi.responsible_contractor?.name)
            field("Distribution", Self.names(rfi.distribution_list))
        } header: {
            Kicker("Overview")
        }
    }

    @ViewBuilder
    private func whereSection(_ rfi: RFIRecord) -> some View {
        let values = [rfi.location?.name, rfi.specification_section?.name, rfi.cost_code?.name, rfi.drawing_number]
        if values.contains(where: { !($0 ?? "").isEmpty }) {
            Section {
                field("Location", rfi.location?.name)
                field("Spec section", rfi.specification_section?.name)
                field("Cost code", rfi.cost_code?.name)
                field("Drawing number", rfi.drawing_number)
            } header: {
                Kicker("Where")
            }
        }
    }

    @ViewBuilder
    private func impacts(_ rfi: RFIRecord) -> some View {
        let cost = Self.impact(rfi.cost_impact)
        let schedule = Self.impact(rfi.schedule_impact)
        if cost != nil || schedule != nil {
            Section {
                field("Cost impact", cost)
                field("Schedule impact", schedule)
            } header: {
                Kicker("Impacts")
            }
        }
    }

    private func questionSection(_ question: Components.Schemas.RFIQuestion, index: Int, count: Int) -> some View {
        Section {
            entry(
                body: Self.split(question.plain_text_body).question,
                meta: Self.joined(question.created_by?.name, question.question_date?.formatted(date: .abbreviated, time: .shortened))
            )
            ForEach(question.attachments, id: \.id) { attachment in
                AttachmentRow(attachment: attachment, projectID: projectID, rfiID: procoreID)
            }
            ForEach(Array(question.answers.enumerated()), id: \.element.id) { answerIndex, answer in
                VStack(alignment: .leading, spacing: 6) {
                    ReviewLabel(text: question.answers.count > 1 ? "Answer \(answerIndex + 1)" : "Answer")
                    entry(
                        body: answer.plain_text_body,
                        meta: Self.joined(answer.created_by?.name, answer.answer_date?.formatted(date: .abbreviated, time: .shortened))
                    )
                }
                ForEach(answer.attachments, id: \.id) { attachment in
                    AttachmentRow(attachment: attachment, projectID: projectID, rfiID: procoreID)
                }
            }
        } header: {
            Kicker(count > 1 ? "Question \(index + 1)" : "Question")
        }
    }

    // MARK: - Rows

    /// A labelled row, or nothing when the value is missing or empty.
    @ViewBuilder
    private func field(_ label: String, _ value: String?) -> some View {
        if let value, !value.isEmpty {
            ReviewField(label: label) { Text(value).textSelection(.enabled) }
        }
    }

    private func entry(body: String, meta: String?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            if !body.isEmpty {
                Text(body).textSelection(.enabled)
            }
            if let meta {
                Text(meta)
                    .font(.footnote)
                    .foregroundStyle(Theme.Color.muted)
            }
        }
        .padding(.vertical, 4)
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

/// A read-only labelled field: the review screen's caption label over its value.
private struct ReviewField<Content: View>: View {
    let label: String
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            ReviewLabel(text: label)
            content()
        }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}
