import FieldAssistantAPI
import SwiftData
import SwiftUI

/// The screens pushed from Home. Draft screens carry the draft id; `rfi` carries the Procore RFI id.
enum Route: Hashable {
    case capture(UUID)
    case review(UUID)
    case result(UUID)
    case rfis
    case rfi(Int64)
}

/// The signed-in home screen for the selected project: the sync header, the record button, recent
/// drafts, a link to the project's Procore RFIs, and project and account controls.
struct HomeView: View {
    let companyID: Int64
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @State private var project: Project?
    @State private var path: [Route] = []
    @State private var showsSettings = false
    @State private var isRetrying = false

    var body: some View {
        NavigationStack(path: $path) {
            List {
                syncSection
                recordSection
                Section("Recent") {
                    RecentDraftsView(projectID: projectID, owner: appState.draftOwnerID ?? -1) { path.append($0) }
                }
                Section {
                    Button {
                        path.append(.rfis)
                    } label: {
                        HStack {
                            Label("Procore RFIs", systemImage: "list.bullet.rectangle")
                                .foregroundStyle(.primary)
                            Spacer()
                            Image(systemName: "chevron.right")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(.tertiary)
                                .accessibilityHidden(true)
                        }
                        .frame(minHeight: 44)
                        .contentShape(Rectangle())
                    }
                    .accessibilityLabel("Procore RFIs")
                    .accessibilityHint("Shows the RFIs already in Procore for this project")
                }
            }
            .navigationTitle(project.map(Self.title) ?? "Field Assistant")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { toolbar }
            .safeAreaInset(edge: .top, spacing: 0) {
                if appState.phase == .offline {
                    noSignalBanner
                } else if let owner = appState.draftOwnerID,
                          !appState.reachability.isReachable || appState.sendingWaitsForSignal {
                    WaitingDraftsGate(owner: owner) { noSignalBanner }
                }
            }
            .navigationDestination(for: Route.self, destination: destination)
            .navigationDestination(isPresented: $showsSettings) { SettingsView() }
        }
        .task(id: TitleKey(projectID: projectID, sessionGeneration: appState.sessionGeneration)) { await loadProject() }
    }

    // MARK: - Sections

    /// Project name, the state of the RFI mirror, and a Sync button (long press for a full resync).
    private var syncSection: some View {
        Section {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(project.map(Self.title) ?? "This project")
                            .font(.headline)
                        TimelineView(.periodic(from: .now, by: 30)) { context in
                            Text(Self.syncLine(appState.recordSync?.status, now: context.date))
                                .font(.footnote)
                                .foregroundStyle(Theme.Color.muted)
                        }
                    }
                    .accessibilityElement(children: .combine)
                    if case .failed(let code, _) = appState.recordSync?.status, code != "procore_permission_denied" {
                        Button("Retry") {
                            Task { await appState.syncRecords(force: true, full: false) }
                        }
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(Theme.Color.accent)
                        .buttonStyle(.borderless)
                        .frame(minHeight: 44)
                        .accessibilityLabel("Retry sync")
                    }
                }
                Spacer()
                syncButton
            }
            .frame(minHeight: 44)
        }
    }

    private var syncButton: some View {
        let isSyncing = appState.recordSync?.status == .syncing
        return Button {
            Task { await appState.syncRecords(force: true, full: false) }
        } label: {
            Group {
                if isSyncing { ProgressView() } else { Label("Sync", systemImage: "arrow.triangle.2.circlepath") }
            }
            .frame(minWidth: 44, minHeight: 44)
        }
        .buttonStyle(.borderless)
        // Enabled offline too: the driver reports an unreachable server as a failed sync.
        .disabled(isSyncing)
        .contextMenu {
            Button("Full resync", systemImage: "arrow.clockwise") {
                Task { await appState.syncRecords(force: true, full: true) }
            }
            .accessibilityLabel("Full resync")
        }
        .accessibilityLabel(isSyncing ? "Syncing RFIs" : "Sync RFIs")
        .accessibilityHint("Fetches changes from Procore. Long press for a full resync.")
        .accessibilityAction(named: "Full resync") {
            Task { await appState.syncRecords(force: true, full: true) }
        }
    }

    /// One line describing the mirror: "Synced 2 min ago", "Syncing…", or why it failed.
    static func syncLine(_ status: RecordSync.Status?, now: Date) -> String {
        switch status {
        case nil, .idle:
            return "Not synced yet"
        case .syncing:
            return "Syncing…"
        case .synced(let date):
            if now.timeIntervalSince(date) < 60 { return "Synced just now" }
            let formatter = RelativeDateTimeFormatter()
            formatter.unitsStyle = .short
            return "Synced \(formatter.localizedString(for: date, relativeTo: now))"
        case .failed(code: "procore_permission_denied", _):
            return "No RFI access on this project"
        case .failed:
            return "Sync failed, showing what you have"
        }
    }

    private var recordSection: some View {
        Section {
            Button {
                path.append(.capture(UUID()))
            } label: {
                VStack(spacing: 12) {
                    ZStack {
                        Circle()
                            .fill(Color.accentColor.opacity(0.15))
                            .frame(width: 136, height: 136)
                        Circle()
                            .fill(Color.accentColor)
                            .frame(width: 116, height: 116)
                        Image(systemName: "mic.fill")
                            .font(.system(size: 46, weight: .semibold))
                            .foregroundStyle(.white)
                    }
                    VStack(spacing: 4) {
                        Text("Record an RFI")
                            .font(.headline)
                            .foregroundStyle(.primary)
                        Text("Tap, talk, review, send")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 16)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Record an RFI")
            .accessibilityHint("Starts recording. Tap, talk, review, send.")
            .accessibilityAddTraits(.isButton)
        }
    }

    @ViewBuilder
    private func destination(_ route: Route) -> some View {
        switch route {
        case .capture(let id):
            CaptureScreen(
                draftID: id,
                companyID: companyID,
                projectID: projectID,
                onBuilt: { path = [.review(id)] },
                onLeave: { path = [] }
            )
        case .review(let id):
            ReviewScreen(
                draftID: id,
                onSubmitted: { path = [.result(id)] },
                onLeave: { path = [] }
            )
        case .result(let id):
            ResultScreen(
                draftID: id,
                onNewRFI: { path = [.capture(UUID())] },
                onDone: {
                    path = []
                    // Pick up the RFI just created in Procore.
                    Task { await appState.syncRecords(force: true, full: false) }
                }
            )
        case .rfis:
            RFIRecordListView(projectID: projectID)
        case .rfi(let procoreID):
            RFIRecordDetailView(projectID: projectID, procoreID: procoreID)
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            Button("Change project", systemImage: "arrow.left.arrow.right") {
                // Keep the company so the picker opens on its projects.
                appState.selectedProjectID = nil
            }
            .labelStyle(.titleAndIcon)
            .frame(minHeight: 44)
            .accessibilityLabel("Change project")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Menu {
                Button("Settings", systemImage: "gearshape") {
                    showsSettings = true
                }
                .accessibilityLabel("Settings")
                Button("Sign out", systemImage: "rectangle.portrait.and.arrow.right", role: .destructive) {
                    Task { await appState.signOut() }
                }
                .accessibilityLabel("Sign out")
            } label: {
                Image(systemName: "person.crop.circle")
                    .frame(minWidth: 44, minHeight: 44)
            }
            .accessibilityLabel("Account")
        }
    }

    // MARK: - No signal

    private var noSignalBanner: some View {
        HStack {
            Label("No signal. Recording works; sending waits.", systemImage: "wifi.slash")
                .font(.footnote.weight(.medium))
            Spacer()
            Button {
                Task { await retry() }
            } label: {
                if isRetrying {
                    ProgressView()
                } else {
                    Text("Retry").font(.footnote.weight(.semibold))
                }
            }
            .frame(minWidth: 44, minHeight: 44)
            .disabled(isRetrying)
            .accessibilityLabel("Retry connecting")
        }
        .padding(.horizontal)
        .background(.yellow.opacity(0.25))
    }

    /// Reconnects the session if it started offline, then runs a sync pass, which sends waiting drafts.
    private func retry() async {
        isRetrying = true
        defer { isRetrying = false }
        if appState.phase == .offline { await appState.bootstrap() }
        if appState.phase != .offline { await appState.draftSync?.runOnce() }
    }

    // MARK: - Title

    /// Fetches the project for its title; the screen still works with a generic title if this fails.
    private func loadProject() async {
        guard let projects = try? await ScopeService(client: appState.client).projects(companyID: companyID) else { return }
        project = projects.first { $0.id == projectID }
    }

    private struct TitleKey: Equatable {
        let projectID: Int64
        let sessionGeneration: Int
    }

    static func title(_ project: Project) -> String {
        project.display_name.isEmpty ? project.name : project.display_name
    }
}

/// Shows its content only while the owner has a waiting draft. The SwiftData query keeps it live as
/// sync sends drafts, so Home does not fetch in `body`.
private struct WaitingDraftsGate<Content: View>: View {
    @Query private var waiting: [LocalDraft]
    private let content: () -> Content

    init(owner: Int64, @ViewBuilder content: @escaping () -> Content) {
        let raw = LocalDraftStatus.waiting.rawValue
        var descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.statusRaw == raw && $0.ownerProcoreUserID == owner }
        )
        descriptor.fetchLimit = 1
        _waiting = Query(descriptor)
        self.content = content
    }

    var body: some View {
        if !waiting.isEmpty { content() }
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    HomeView(companyID: 1, projectID: 7)
        .environment(AppState.preview(container: container))
        .modelContainer(container)
}
