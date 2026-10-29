import SwiftData
import SwiftUI

/// Every draft of the project on this phone, newest first, with the queue state of each:
/// waiting for signal, queued to send, or the Procore message that stopped it. Rows open the
/// same screens as Home's Recent rows.
struct DraftsListView: View {
    let onOpen: (Route) -> Void
    @Environment(AppState.self) private var appState
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Query private var drafts: [LocalDraft]

    private static let footerStyle = Theme.TextStyle(12, .regular, relativeTo: .caption, lineHeight: 1.5)
    private static let rowInsets = EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20)

    /// `owner` is the signed-in Procore user; other users' drafts on the phone never show.
    init(projectID: Int64, owner: Int64, onOpen: @escaping (Route) -> Void) {
        self.onOpen = onOpen
        _drafts = Query(FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.projectID == projectID && $0.ownerProcoreUserID == owner },
            sortBy: [SortDescriptor(\.capturedAt, order: .reverse)]
        ))
    }

    /// At accessibility text sizes the footer scrolls after the rows, so the list keeps room.
    private var scrollsFooter: Bool { dynamicTypeSize.isAccessibilitySize }

    var body: some View {
        List {
            Text("Drafts")
                .textStyle(Theme.Text.screenTitle)
                .foregroundStyle(Theme.Color.textPrimary)
                .accessibilityAddTraits(.isHeader)
                .frame(maxWidth: .infinity, alignment: .leading)
                .listRowInsets(EdgeInsets(top: 8, leading: 20, bottom: 10, trailing: 20))
                .listRowSeparator(.hidden, edges: .top)
                .chromeRow()
            if drafts.isEmpty {
                Text("Drafts you record show up here.")
                    .textStyle(Theme.Text.meta13)
                    .foregroundStyle(Theme.Color.muted)
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .listRowInsets(Self.rowInsets)
                    .chromeRow()
            }
            ForEach(drafts) { draft in
                Button {
                    onOpen(RecentDraftsView.route(for: draft, isSynced: isSynced))
                } label: {
                    DraftRow(draft: draft, showsDetail: true)
                }
                .buttonStyle(.plain)
                .accessibilityHint(RecentDraftsView.hint(for: draft.status))
                .listRowInsets(Self.rowInsets)
                .chromeRow()
            }
            if scrollsFooter {
                footer
                    .listRowSeparator(.hidden, edges: .bottom)
                    .chromeRow()
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(Theme.Color.ground.ignoresSafeArea())
        .safeAreaInset(edge: .bottom, spacing: 0) {
            // Pinned to the bottom, except at accessibility sizes where it scrolls after the rows.
            if !scrollsFooter { footer }
        }
        .designNavigationTitle("Drafts", .hidden)
    }

    /// Why a retried submit is safe: 12 lh 1.5, tertiary, centred.
    private var footer: some View {
        Text("Each submit carries the draft's own key, so a retry after a dropped connection never posts twice.")
            .textStyle(Self.footerStyle)
            .foregroundStyle(Theme.Color.tertiary)
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
            .background(Theme.Color.ground)
    }

    /// Whether the project's mirror holds the RFI with this Procore id.
    private func isSynced(_ procoreID: Int64, projectID: Int64) -> Bool {
        appState.records.record(projectID: projectID, kind: .rfi, procoreID: procoreID) != nil
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    let waiting = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, transcript: "Spalling", capturedAt: .now.addingTimeInterval(-300))
    waiting.subject = "Spalling at loading dock column"
    let queued = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitting, transcript: "Window")
    queued.subject = "Window rough opening, unit 1204"
    let failed = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .failed, transcript: "Fire caulk",
                            capturedAt: .now.addingTimeInterval(-86_400))
    failed.subject = "Fire caulk at level 2 shaft"
    failed.errorMessage = "Procore: Location is required on this project"
    [waiting, queued, failed].forEach { container.mainContext.insert($0) }
    return NavigationStack { DraftsListView(projectID: 7, owner: 42) { _ in } }
        .modelContainer(container)
        .environment(AppState.preview(container: container))
}
