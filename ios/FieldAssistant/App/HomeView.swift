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
    case drafts
}

/// The signed-in home screen for the selected project: the company and project with the sync
/// state, the feature cards, the record button, recent drafts and a link to the project's
/// Procore RFIs. The project name opens the picker; the gear pushes Settings.
struct HomeView: View {
    let companyID: Int64
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var project: Project?
    @State private var companyName: String?
    @State private var path: [Route] = []
    @State private var showsSettings = false
    @State private var isRetrying = false

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    offlineBanner
                    header
                    featureGrid
                    recordBlock
                    recentSection
                    procoreRFIsRow
                }
                .padding(20)
            }
            .background(Theme.Color.ground.ignoresSafeArea())
            .navigationTitle("Home")
            .toolbar(.hidden, for: .navigationBar)
            .navigationDestination(for: Route.self, destination: destination)
            .navigationDestination(isPresented: $showsSettings) {
                SettingsView(projectName: project.map(Self.title), companyName: companyName)
            }
        }
        .task(id: TitleKey(projectID: projectID, sessionGeneration: appState.sessionGeneration)) { await loadTitles() }
    }

    // MARK: - Header

    /// Company kicker over the project name (opens the picker) and the sync line; Settings on the right.
    private var header: some View {
        HStack(alignment: .top, spacing: 8) {
            VStack(alignment: .leading, spacing: 4) {
                if let companyName {
                    Kicker(companyName, style: .section)
                        .lineLimit(1)
                        .minimumScaleFactor(0.7)
                }
                projectButton
                syncRow
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            settingsButton
        }
    }

    private var projectTitle: String { project.map(Self.title) ?? "This project" }

    private var projectButton: some View {
        Button {
            // Keep the company so the picker opens on its projects.
            appState.selectedProjectID = nil
        } label: {
            // One Text so the chevron follows the last word when the name wraps.
            let chevron = Text(Image(systemName: "chevron.down"))
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(Theme.Color.brand)
            Text("\(Text(projectTitle).foregroundStyle(Theme.Color.textPrimary))\u{2009}\(chevron)")
                .textStyle(Theme.Text.projectTitle)
                .multilineTextAlignment(.leading)
                .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Change project")
        .accessibilityValue(projectTitle)
    }

    private var settingsButton: some View {
        Button {
            showsSettings = true
        } label: {
            Image(systemName: "gearshape")
                .font(.system(size: 20, weight: .regular))
                .foregroundStyle(Theme.Color.textPrimary)
                .frame(width: 44, height: 44)
                .background(Theme.Color.ground, in: Circle())
                .overlay { Circle().strokeBorder(Theme.Color.hairline, lineWidth: 1) }
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Settings")
    }

    /// The state of the RFI mirror, Retry after a failure, and Sync (long press for a full resync).
    private var syncRow: some View {
        HStack(alignment: .center, spacing: 12) {
            TimelineView(.periodic(from: .now, by: 30)) { context in
                Text(Self.syncLine(appState.recordSync?.status, now: context.date))
                    .textStyle(Theme.Text.meta13)
                    .foregroundStyle(Theme.Color.muted)
            }
            if case .failed(let code, _) = appState.recordSync?.status, code != "procore_permission_denied" {
                Button("Retry") {
                    Task { await appState.syncRecords(force: true, full: false) }
                }
                .buttonStyle(TextLinkButtonStyle(size: 13))
                .accessibilityLabel("Retry sync")
            }
            syncButton
        }
    }

    private var syncButton: some View {
        let isSyncing = appState.recordSync?.status == .syncing
        return Button {
            Task { await appState.syncRecords(force: true, full: false) }
        } label: {
            if isSyncing {
                ProgressView().frame(minWidth: 44)
            } else {
                Text("Sync").frame(minWidth: 44)
            }
        }
        .buttonStyle(TextLinkButtonStyle(size: 13))
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

    // MARK: - Features

    /// RFI is the one feature v1 ships; the others show as coming. One column at accessibility sizes.
    private var featureGrid: some View {
        let columns = Array(repeating: GridItem(.flexible(), spacing: 10),
                            count: dynamicTypeSize.isAccessibilitySize ? 1 : 2)
        return LazyVGrid(columns: columns, spacing: 10) {
            FeatureCard(title: "RFI", isSelected: true)
            FeatureCard(title: "Observation", isSelected: false)
            FeatureCard(title: "Punch item", isSelected: false)
            FeatureCard(title: "Daily log", isSelected: false)
        }
    }

    // MARK: - Record

    private var recordBlock: some View {
        VStack(spacing: 0) {
            RecordButton(glyph: .mic, size: 116, halo: 10, isEnabled: true) {
                path.append(.capture(UUID()))
            }
            .accessibilityLabel("Record an RFI")
            .accessibilityHint("Starts recording. Tap, talk, review, send.")
            Text("Record an RFI")
                .textStyle(Theme.Text.label17)
                .foregroundStyle(Theme.Color.textPrimary)
                .padding(.top, 8)
                .accessibilityHidden(true)
            Text("Under a minute, start to Procore")
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.muted)
                .padding(.top, 10)
                .accessibilityHidden(true)
        }
        .multilineTextAlignment(.center)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
    }

    // MARK: - Recent

    private var recentSection: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                Kicker("Recent", style: .section, color: Theme.Color.muted)
                    .accessibilityAddTraits(.isHeader)
                Spacer()
                Button("All drafts") { path.append(.drafts) }
                    .buttonStyle(TextLinkButtonStyle(size: 13))
            }
            RecentDraftsView(projectID: projectID, owner: appState.draftOwnerID ?? -1, limit: 3) { path.append($0) }
        }
    }

    private var procoreRFIsRow: some View {
        Button {
            path.append(.rfis)
        } label: {
            VStack(spacing: 0) {
                FormRow(label: "Project", chevron: true) { Text("Procore RFIs") }
                HairlineDivider()
            }
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Procore RFIs")
        .accessibilityHint("Shows the RFIs already in Procore for this project")
        .accessibilityAddTraits(.isButton)
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
                backTitle: Self.backTitle(for: route, in: path),
                onSubmitted: { path = [.result(id)] },
                onLeave: { path = [] }
            )
        case .result(let id):
            ResultScreen(
                draftID: id,
                projectName: project.map(Self.title),
                onNewRFI: { path = [.capture(UUID())] },
                onDone: {
                    path = []
                    // Pick up the RFI just created in Procore.
                    Task { await appState.syncRecords(force: true, full: false) }
                }
            )
        case .rfis:
            RFIRecordListView(projectID: projectID) { path.append($0) }
        case .rfi(let procoreID):
            RFIRecordDetailView(projectID: projectID, procoreID: procoreID)
        case .drafts:
            DraftsListView(projectID: projectID, owner: appState.draftOwnerID ?? -1) { path.append($0) }
        }
    }

    /// What a pushed screen's back link names: the screen under `route` in `path`. Review sits on
    /// Home after a build or a Recent row, and on Drafts when opened from the list.
    static func backTitle(for route: Route, in path: [Route]) -> String {
        guard let index = path.firstIndex(of: route), index > 0 else { return "Home" }
        return path[index - 1] == .drafts ? "Drafts" : "Home"
    }

    // MARK: - No signal

    /// Offline, or online with drafts waiting while the server is unreachable.
    @ViewBuilder
    private var offlineBanner: some View {
        if appState.phase == .offline {
            noSignalBanner
        } else if let owner = appState.draftOwnerID,
                  !appState.reachability.isReachable || appState.sendingWaitsForSignal {
            WaitingDraftsGate(owner: owner) { noSignalBanner }
        }
    }

    private var noSignalBanner: some View {
        VStack(alignment: .trailing, spacing: 0) {
            OfflineBanner()
            Button {
                Task { await retry() }
            } label: {
                if isRetrying { ProgressView().frame(minWidth: 44) } else { Text("Retry") }
            }
            .buttonStyle(TextLinkButtonStyle(size: 13))
            .disabled(isRetrying)
            .accessibilityLabel("Retry connecting")
        }
    }

    /// Reconnects the session if it started offline, then runs a sync pass, which sends waiting drafts.
    private func retry() async {
        isRetrying = true
        defer { isRetrying = false }
        if appState.phase == .offline { await appState.bootstrap() }
        if appState.phase != .offline { await appState.draftSync?.runOnce() }
    }

    // MARK: - Title

    /// Fetches the project and company names for the header, as the picker does; the screen still
    /// works with a generic title and no company kicker if this fails (offline).
    private func loadTitles() async {
        let scope = ScopeService(client: appState.client)
        async let projects = scope.projects(companyID: companyID)
        async let companies = scope.companies()
        if let list = try? await projects { project = list.first { $0.id == projectID } }
        if let list = try? await companies { companyName = list.first { $0.id == companyID }?.name }
    }

    private struct TitleKey: Equatable {
        let projectID: Int64
        let sessionGeneration: Int
    }

    static func title(_ project: Project) -> String {
        project.display_name.isEmpty ? project.name : project.display_name
    }
}

/// A feature card: the shipped feature selected (2 pt orange border, accent kicker), the rest
/// coming (dashed hairline, tertiary text, not tappable).
private struct FeatureCard: View {
    let title: String
    let isSelected: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Kicker(isSelected ? "Selected" : "Coming", style: .card,
                   color: isSelected ? nil : Theme.Color.tertiary)
            Spacer(minLength: 0)
            Text(title)
                .textStyle(Theme.Text.cardTitle)
                .foregroundStyle(isSelected ? Theme.Color.textPrimary : Theme.Color.tertiary)
                .lineLimit(1)
                .minimumScaleFactor(0.6)
        }
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 88, alignment: .leading)
        .overlay {
            if isSelected {
                RoundedRectangle(cornerRadius: 8).strokeBorder(Theme.Color.brand, lineWidth: 2)
            } else {
                Color.clear.hairlineBorder(radius: 8, dashed: true)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(isSelected ? title : "\(title), coming")
        .accessibilityAddTraits(isSelected ? .isSelected : [])
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
