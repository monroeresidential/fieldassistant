import SwiftData
import SwiftUI

/// The project's drafts on this phone, newest first, as rows of a `List` section. Tapping a row
/// opens the screen its status calls for. A SwiftData query keeps the rows live while sync and
/// review change statuses.
struct RecentDraftsView: View {
    let onOpen: (Route) -> Void
    @Environment(AppState.self) private var appState
    @Query private var drafts: [LocalDraft]

    /// `owner` is the signed-in Procore user; other users' drafts on the phone never show.
    init(projectID: Int64, owner: Int64, limit: Int = 20, onOpen: @escaping (Route) -> Void) {
        self.onOpen = onOpen
        var descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.projectID == projectID && $0.ownerProcoreUserID == owner },
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
                    onOpen(Self.route(for: draft, isSynced: isSynced))
                } label: {
                    DraftRow(draft: draft)
                }
                .buttonStyle(.plain)
                .accessibilityHint(Self.hint(for: draft.status))
            }
        }
    }

    /// Whether the project's mirror holds the RFI with this Procore id.
    private func isSynced(_ procoreID: Int64, projectID: Int64) -> Bool {
        appState.records.record(projectID: projectID, kind: .rfi, procoreID: procoreID) != nil
    }

    /// Waiting drafts open Review, which sends them first. A submitted one opens the synced RFI
    /// when the mirror has it, otherwise the Result screen (the RFI has not synced yet, or the
    /// draft predates `resultProcoreID`).
    static func route(for draft: LocalDraft, isSynced: (_ procoreID: Int64, _ projectID: Int64) -> Bool) -> Route {
        switch draft.status {
        case .submitted:
            if let procoreID = draft.resultProcoreID, isSynced(procoreID, draft.projectID) {
                return .rfi(procoreID)
            }
            return .result(draft.id)
        case .waiting, .draft, .submitting, .failed:
            return .review(draft.id)
        }
    }

    private static func hint(for status: LocalDraftStatus) -> String {
        switch status {
        case .waiting: "Sends the draft, then opens it for review"
        case .draft, .failed: "Opens the draft for review"
        case .submitting: "Shows the draft while Procore confirms it"
        case .submitted: "Opens the RFI created in Procore"
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
            StatusTag(StatusTag.state(for: draft.status, number: draft.resultNumber))
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

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    let waiting = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, transcript: "Twisted column", capturedAt: .now.addingTimeInterval(-300))
    let submitted = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitted, transcript: "Header size")
    submitted.subject = "Header size over garage door"
    submitted.resultNumber = "214"
    container.mainContext.insert(waiting)
    container.mainContext.insert(submitted)
    return List {
        Section("Recent") { RecentDraftsView(projectID: 7, owner: 42) { _ in } }
    }
    .modelContainer(container)
    .environment(AppState.preview(container: container))
}
