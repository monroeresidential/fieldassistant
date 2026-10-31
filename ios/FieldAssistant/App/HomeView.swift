import FieldAssistantAPI
import SwiftData
import SwiftUI

/// The screens pushed from Home. Draft screens carry the draft id; `rfi` carries the Procore RFI id.
/// `review` also carries the screen it was opened from, which its back link names.
enum Route: Hashable {
    /// The screen under Review: Home after a build or a Recent row, Drafts from the list.
    enum Origin: Hashable { case home, drafts }

    case capture(UUID)
    case review(UUID, from: Origin = .home)
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
    /// The project-name chevron, scaling with the title it follows.
    @ScaledMetric(relativeTo: .title) private var chevronSize: CGFloat = 20

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
                .font(.system(size: chevronSize, weight: .semibold))
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
                .font(.system(size: 20, weight: .medium))
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
                ProgressView().tint(Theme.Color.tertiary).frame(minWidth: 44)
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

    /// RFI is the one feature v1 ships, shown as not available (with Procore's reason) when the
    /// backend says the user cannot create RFIs on the project; the others show as coming. One
    /// column at accessibility sizes.
    private var featureGrid: some View {
        let columns = Array(repeating: GridItem(.flexible(), spacing: 10),
                            count: dynamicTypeSize.isAccessibilitySize ? 1 : 2)
        let rfi = appState.rfiAvailability
        return LazyVGrid(columns: columns, spacing: 10) {
            FeatureCard(title: "RFI", state: rfi.available ? .selected : .unavailable(reason: rfi.reason))
            FeatureCard(title: "Observation", state: .coming)
            FeatureCard(title: "Punch item", state: .coming)
            FeatureCard(title: "Daily log", state: .coming)
        }
    }

    // MARK: - Record

    private var recordBlock: some View {
        let availability = appState.rfiAvailability
        return VStack(spacing: 0) {
            RecordButton(glyph: .mic, size: 116, halo: 10, isEnabled: availability.available) {
                path.append(.capture(UUID()))
            }
            .accessibilityLabel("Record an RFI")
            .accessibilityHint(availability.available ? "Starts recording. Tap, talk, review, send." : Self.recordHint(availability))
            Text("Record an RFI")
                .textStyle(Theme.Text.label17)
                .foregroundStyle(Theme.Color.textPrimary)
                .padding(.top, 8)
                .accessibilityHidden(true)
            Text(Self.recordHint(availability))
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.muted)
                .padding(.top, 10)
                .accessibilityHidden(true)
        }
        .multilineTextAlignment(.center)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
    }

    /// The line under the record button: how fast it is, or that RFIs cannot be created here.
    static func recordHint(_ availability: FeatureAvailability) -> String {
        availability.available ? "Under a minute, start to Procore" : "RFIs are not available on this project"
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
        case .review(let id, _):
            ReviewScreen(
                draftID: id,
                backTitle: Self.backTitle(for: route),
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
            DraftsListView(projectID: projectID, owner: appState.draftOwnerID ?? -1) {
                path.append(Self.openedFromDrafts($0))
            }
        }
    }

    /// What Review's back link names: the screen it pops to, carried by the route itself. Reading
    /// `path` here is not safe: the destination is built from the path of HomeView's last body
    /// pass, which does not hold the route being pushed yet.
    static func backTitle(for route: Route) -> String {
        guard case .review(_, from: .drafts) = route else { return "Home" }
        return "Drafts"
    }

    /// A route the Drafts list opens: a review remembers it came from Drafts.
    static func openedFromDrafts(_ route: Route) -> Route {
        guard case .review(let id, _) = route else { return route }
        return .review(id, from: .drafts)
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
                if isRetrying { ProgressView().tint(Theme.Color.tertiary).frame(minWidth: 44) } else { Text("Retry") }
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

/// A feature card: the shipped feature selected (2 pt orange border, accent kicker), or not
/// available on the project (dashed hairline, muted text, Procore's reason beneath the kicker, at
/// most three lines so the row stays short; VoiceOver reads all of it); the rest coming (dashed
/// hairline, tertiary text). The title sits at the bottom of every card so a row's titles line up.
/// None is tappable.
private struct FeatureCard: View {
    enum State {
        case selected
        case unavailable(reason: String?)
        case coming
    }

    let title: String
    let state: State

    private var isSelected: Bool {
        if case .selected = state { return true }
        return false
    }

    private var kicker: String {
        switch state {
        case .selected: "Selected"
        case .unavailable: "Not available"
        case .coming: "Coming"
        }
    }

    private var ink: Color {
        switch state {
        case .selected: Theme.Color.textPrimary
        case .unavailable: Theme.Color.muted
        case .coming: Theme.Color.tertiary
        }
    }

    private var reason: String? {
        guard case .unavailable(let reason) = state, let reason, !reason.isEmpty else { return nil }
        return reason
    }

    private var accessibilityLabel: String {
        switch state {
        case .selected: title
        case .unavailable: "\(title), not available"
        case .coming: "\(title), coming"
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Kicker(kicker, style: .card, color: isSelected ? nil : ink)
            if let reason {
                Text(reason)
                    .textStyle(Theme.Text.meta12)
                    .foregroundStyle(Theme.Color.muted)
                    .lineLimit(3)
                    .truncationMode(.tail)
            }
            Spacer(minLength: 0)
            Text(title)
                .textStyle(Theme.Text.cardTitle)
                .foregroundStyle(ink)
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
        .accessibilityLabel(accessibilityLabel)
        .accessibilityValue(reason ?? "")
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

#Preview("RFI not available") {
    VStack(spacing: 22) {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: 2), spacing: 10) {
            FeatureCard(
                title: "RFI",
                state: .unavailable(reason: "You have Read Only access to the RFI tool on this project. Ask a Procore admin for Standard access.")
            )
            FeatureCard(title: "Observation", state: .coming)
            FeatureCard(title: "Punch item", state: .coming)
            FeatureCard(title: "Daily log", state: .coming)
        }
        RecordButton(glyph: .mic, size: 116, halo: 10, isEnabled: false) {}
        Text(HomeView.recordHint(FeatureAvailability(available: false, reason: nil)))
            .textStyle(Theme.Text.meta13)
            .foregroundStyle(Theme.Color.muted)
    }
    .padding(20)
    .background(Theme.Color.ground)
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true).get()
    HomeView(companyID: 1, projectID: 7)
        .environment(AppState.preview(container: container))
        .modelContainer(container)
}
