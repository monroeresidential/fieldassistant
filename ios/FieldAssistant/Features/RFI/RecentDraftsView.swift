import SwiftData
import SwiftUI

/// The project's drafts on this phone, newest first, as rows of a `List` section. Tapping a row
/// opens the screen its status calls for. A SwiftData query keeps the rows live while sync and
/// review change statuses.
struct RecentDraftsView: View {
    let onOpen: (Route) -> Void
    @Query private var drafts: [LocalDraft]

    init(projectID: Int64, limit: Int = 20, onOpen: @escaping (Route) -> Void) {
        self.onOpen = onOpen
        var descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.projectID == projectID },
            sortBy: [SortDescriptor(\.capturedAt, order: .reverse)]
        )
        descriptor.fetchLimit = limit
        _drafts = Query(descriptor)
    }

    var body: some View {
        if drafts.isEmpty {
            Text("Drafts you record show up here.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .frame(minHeight: 44)
        } else {
            ForEach(drafts) { draft in
                Button {
                    onOpen(Self.route(for: draft))
                } label: {
                    DraftRow(draft: draft)
                }
                .buttonStyle(.plain)
                .accessibilityHint(Self.hint(for: draft.status))
            }
        }
    }

    /// Waiting drafts open Review, which sends them first; a submitted one opens its result.
    static func route(for draft: LocalDraft) -> Route {
        switch draft.status {
        case .submitted: .result(draft.id)
        case .waiting, .draft, .submitting, .failed: .review(draft.id)
        }
    }

    private static func hint(for status: LocalDraftStatus) -> String {
        switch status {
        case .waiting: "Sends the draft, then opens it for review"
        case .draft, .failed: "Opens the draft for review"
        case .submitting: "Shows the draft while Procore confirms it"
        case .submitted: "Shows the RFI created in Procore"
        }
    }
}

private struct DraftRow: View {
    let draft: LocalDraft

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text(draft.subject ?? "RFI draft")
                    .font(.body.weight(.medium))
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                Text(draft.capturedAt, format: .relative(presentation: .named))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                if let detail {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            Spacer(minLength: 8)
            DraftStatusChip(status: draft.status, number: draft.resultNumber)
        }
        .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    /// The third line the handoff gives waiting and queued drafts, or the last error.
    private var detail: String? {
        switch draft.status {
        case .waiting: draft.errorMessage ?? "Needs connection to build the draft"
        case .submitting: "Reviewed. Sends automatically, once."
        case .failed: draft.errorMessage
        case .draft, .submitted: nil
        }
    }
}

/// The status tag on a draft row: label from `LocalDraftStatus`, emphasis by how urgent it is.
struct DraftStatusChip: View {
    let status: LocalDraftStatus
    var number: String?

    var body: some View {
        Text(text)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .foregroundStyle(status == .failed ? Color.white : color)
            .background {
                if status == .failed {
                    Capsule().fill(Color.orange)
                } else {
                    Capsule().strokeBorder(color, lineWidth: 1)
                }
            }
            .accessibilityLabel("Status \(text)")
    }

    private var text: String {
        if status == .submitted, let number { return "#\(number) \(status.label)" }
        return status.label
    }

    private var color: Color {
        switch status {
        case .waiting, .submitting: .orange
        case .draft, .submitted: .secondary
        case .failed: .orange
        }
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    let waiting = LocalDraft(companyID: 1, projectID: 7, transcript: "Twisted column", capturedAt: .now.addingTimeInterval(-300))
    let submitted = LocalDraft(companyID: 1, projectID: 7, status: .submitted, transcript: "Header size")
    submitted.subject = "Header size over garage door"
    submitted.resultNumber = "214"
    container.mainContext.insert(waiting)
    container.mainContext.insert(submitted)
    return List {
        Section("Recent") { RecentDraftsView(projectID: 7) { _ in } }
    }
    .modelContainer(container)
}
