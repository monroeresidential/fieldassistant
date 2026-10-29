import FieldAssistantAPI
import SwiftUI

/// Two-step picker: choose a company, then one of its projects. The choice is saved server-side and in `AppState`.
struct ProjectPickerView: View {
    private enum Step: Equatable {
        case companies
        case projects(companyID: Int64, companyName: String?)
    }

    private enum Load<Value: Equatable>: Equatable {
        case loading
        case loaded(Value)
        case failed(APIFailure)
    }

    /// Reload projects when the company changes or the session is re-established.
    private struct ProjectsKey: Equatable {
        let companyID: Int64
        let sessionGeneration: Int
    }

    @Environment(AppState.self) private var appState
    @State private var step: Step?
    @State private var companies: Load<[Company]> = .loading
    @State private var projects: Load<[Project]> = .loading
    @State private var search = ""
    @State private var savingProjectID: Int64?
    @State private var saveError: APIFailure?
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    /// At accessibility text sizes the header and footer scroll with the rows, so the list keeps room.
    private var scrollsHeader: Bool { dynamicTypeSize.isAccessibilitySize }

    var body: some View {
        NavigationStack {
            Group {
                switch currentStep {
                case .companies:
                    companiesStep
                case .projects(let companyID, let companyName):
                    projectsStep(companyID: companyID, companyName: companyName)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .background(Theme.Color.ground.ignoresSafeArea())
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Sign out") { Task { await appState.signOut() } }
                        .font(Self.toolbarFont)
                        .accessibilityLabel("Sign out")
                }
            }
            // System toolbar buttons (iOS 26 sizes them), in accent ink.
            .tint(Theme.Color.accent)
        }
        .alert(
            "Couldn't save your project",
            isPresented: Binding(get: { saveError != nil }, set: { if !$0 { saveError = nil } }),
            presenting: saveError
        ) { _ in
            Button("OK", role: .cancel) {}
        } message: { failure in
            Text(Self.message(for: failure))
        }
    }

    /// Opens on the projects of an already-selected company (after "Change project").
    private var currentStep: Step {
        if let step { return step }
        if let id = appState.selectedCompanyID { return .projects(companyID: id, companyName: nil) }
        return .companies
    }

    private var scope: ScopeService { ScopeService(client: appState.client) }

    // MARK: - Companies

    private var companiesStep: some View {
        let header = PickerHeader(title: "Choose a company")
        return VStack(spacing: 0) {
            switch companies {
            case .loading:
                header
                PickerLoading(title: "Loading companies")
            case .failed(let failure):
                header
                APIFailureView(failure: failure, subject: "companies") { Task { await loadCompanies() } }
            case .loaded(let list):
                if !scrollsHeader { header }
                List {
                    if scrollsHeader { header.pickerChromeRow() }
                    ForEach(list, id: \.id) { company in
                        Button {
                            search = ""
                            step = .projects(companyID: company.id, companyName: company.name)
                        } label: {
                            PickerRow(title: company.name)
                        }
                        .accessibilityLabel("Company \(company.name)")
                        .pickerRow()
                    }
                    if list.isEmpty {
                        FailureMessage(title: "No companies", message: "Your Procore account has no companies.")
                            .pickerChromeRow()
                    }
                }
                .pickerList()
                .refreshable { await loadCompanies() }
            }
        }
        .task(id: appState.sessionGeneration) { await loadCompanies() }
    }

    private func loadCompanies() async {
        do {
            companies = .loaded(try await scope.companies())
        } catch {
            if Task.isCancelled { return }
            companies = .failed(APIFailure(error))
        }
    }

    // MARK: - Projects

    private func projectsStep(companyID: Int64, companyName: String?) -> some View {
        let header = PickerHeader(title: "Choose a project") {
            companyKicker(companyID: companyID, companyName: companyName)
        } field: {
            SearchField("Search projects", text: $search)
        }
        let footer = PickerFooter()
        return VStack(spacing: 0) {
            switch projects {
            case .loading:
                header
                PickerLoading(title: "Loading projects")
            case .failed(let failure):
                header
                APIFailureView(failure: failure, subject: "this company's projects") {
                    Task { await loadProjects(companyID: companyID) }
                }
            case .loaded(let list):
                let shown = filtered(list)
                if !scrollsHeader { header }
                List {
                    if scrollsHeader { header.pickerChromeRow() }
                    ForEach(shown, id: \.id) { project in
                        let isLastUsed = project.id == lastUsedProjectID
                        Button {
                            Task { await choose(project, companyID: companyID) }
                        } label: {
                            PickerRow(title: HomeView.title(project), isLastUsed: isLastUsed,
                                      isSaving: savingProjectID == project.id)
                        }
                        .disabled(savingProjectID != nil)
                        .accessibilityLabel("Project \(HomeView.title(project))")
                        .accessibilityValue(isLastUsed ? "Last used" : "")
                        .pickerRow()
                    }
                    if list.isEmpty {
                        FailureMessage(title: "No projects", message: "You have no projects in this company.")
                            .pickerChromeRow()
                    } else if shown.isEmpty {
                        FailureMessage(title: "No results for \u{201C}\(search)\u{201D}",
                                       message: "Check the spelling or try a new search.")
                            .pickerChromeRow()
                    }
                    if scrollsHeader { footer.pickerChromeRow() }
                }
                .pickerList()
                .refreshable { await loadProjects(companyID: companyID) }
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            // Pinned to the bottom, except at accessibility sizes where it scrolls after the rows.
            if !scrollsHeader { footer }
        }
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button {
                    search = ""
                    step = .companies
                } label: {
                    Label("Companies", systemImage: "chevron.left")
                        .labelStyle(.titleAndIcon)
                }
                .font(Self.toolbarFont)
                .accessibilityLabel("Back to companies")
            }
        }
        .task(id: ProjectsKey(companyID: companyID, sessionGeneration: appState.sessionGeneration)) {
            await loadProjects(companyID: companyID)
        }
        .task(id: appState.sessionGeneration) {
            // The kicker names the company, and offers the switch only when there is more than one.
            if case .loaded = companies { return }
            await loadCompanies()
        }
    }

    /// The company as an accent kicker: a button with `chevron.down` back to the companies when
    /// there is more than one, a static label otherwise (v1 has one company).
    @ViewBuilder
    private func companyKicker(companyID: Int64, companyName: String?) -> some View {
        let list = loadedCompanies
        if let name = companyName ?? list?.first(where: { $0.id == companyID })?.name {
            if let list, list.count > 1 {
                Button {
                    search = ""
                    step = .companies
                } label: {
                    HStack(spacing: 4) {
                        CompanyKicker(name: name)
                        Image(systemName: "chevron.down")
                            .font(.system(size: 14, weight: .semibold))
                            .foregroundStyle(Theme.Color.accent)
                            .accessibilityHidden(true)
                    }
                    .frame(minHeight: 44)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Company \(name)")
                .accessibilityHint("Choose another company")
            } else {
                CompanyKicker(name: name)
            }
        }
    }

    private var loadedCompanies: [Company]? {
        if case .loaded(let list) = companies { return list }
        return nil
    }

    private func filtered(_ list: [Project]) -> [Project] {
        let query = search.trimmingCharacters(in: .whitespaces)
        guard !query.isEmpty else { return list }
        return list.filter {
            $0.display_name.localizedCaseInsensitiveContains(query) || $0.name.localizedCaseInsensitiveContains(query)
        }
    }

    private func loadProjects(companyID: Int64) async {
        projects = .loading
        do {
            projects = .loaded(try await scope.projects(companyID: companyID))
        } catch {
            if Task.isCancelled { return }
            projects = .failed(APIFailure(error))
        }
    }

    /// Saves the selection on the server, then applies it locally. Offline still applies it:
    /// the next `/me` load keeps the local choice and a later save resyncs.
    private func choose(_ project: Project, companyID: Int64) async {
        savingProjectID = project.id
        defer { savingProjectID = nil }
        do {
            try await scope.saveSelection(companyID: companyID, projectID: project.id)
        } catch {
            let failure = APIFailure(error)
            guard failure == .offline else {
                saveError = failure
                return
            }
        }
        appState.selectedCompanyID = companyID
        appState.selectedProjectID = project.id
    }

    // MARK: - Helpers

    /// The project the signed-in user last chose, from `/me`; tagged "Last used".
    private var lastUsedProjectID: Int64? {
        if case .signedIn(let user) = appState.phase { return user.last_project_id }
        return nil
    }

    private static let toolbarFont = Theme.Text.toolbarButton.font

    private static func message(for failure: APIFailure) -> String {
        switch failure {
        case .permissionDenied(let message), .validation(let message), .server(let message): message
        case .reauthRequired: "Your session expired. Sign in again."
        case .offline: "You're offline."
        }
    }
}

/// Kicker, screen title and an optional field, on the 20 pt screen padding.
private struct PickerHeader<Kick: View, Field: View>: View {
    let title: String
    @ViewBuilder var kicker: Kick
    @ViewBuilder var field: Field

    init(title: String, @ViewBuilder kicker: () -> Kick, @ViewBuilder field: () -> Field) {
        self.title = title
        self.kicker = kicker()
        self.field = field()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 6) {
                kicker
                Text(title)
                    .textStyle(Theme.Text.screenTitle)
                    .foregroundStyle(Theme.Color.textPrimary)
                    .accessibilityAddTraits(.isHeader)
            }
            field
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 20)
        .padding(.top, 4)
        .padding(.bottom, 20)
    }
}

extension PickerHeader where Kick == EmptyView, Field == EmptyView {
    init(title: String) {
        self.init(title: title, kicker: { EmptyView() }, field: { EmptyView() })
    }
}

/// The company name as a 12/600 .12em accent kicker.
private struct CompanyKicker: View {
    let name: String

    private static let style = Theme.TextStyle(12, .semibold, relativeTo: .caption, em: 0.12)

    var body: some View {
        Text(name.uppercased())
            .textStyle(Self.style)
            .foregroundStyle(Theme.Color.accent)
    }
}

/// The note about lookups prefetching: 12 lh 1.5, tertiary, centred.
private struct PickerFooter: View {
    private static let style = Theme.TextStyle(12, .regular, relativeTo: .caption, lineHeight: 1.5)

    var body: some View {
        Text("People, locations and spec sections download when you choose a project, so pickers work without signal.")
            .textStyle(Self.style)
            .foregroundStyle(Theme.Color.tertiary)
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.horizontal, 20)
            .padding(.top, 12)
            .padding(.bottom, 8)
            .background(Theme.Color.ground)
    }
}

private struct PickerLoading: View {
    let title: String

    var body: some View {
        ProgressView {
            Text(title)
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.muted)
        }
        .tint(Theme.Color.tertiary)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// A 72 pt picker row: 17/600 title, an optional "Last used" tag and a tertiary chevron.
private struct PickerRow: View {
    let title: String
    var isLastUsed = false
    var isSaving = false

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        HStack(spacing: 12) {
            // At accessibility sizes the tag goes under the title so neither truncates.
            if dynamicTypeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: 8) {
                    titleText
                    if isLastUsed { StatusTag(.lastUsed) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                titleText
                    .frame(maxWidth: .infinity, alignment: .leading)
                if isLastUsed { StatusTag(.lastUsed) }
            }
            if isSaving { ProgressView().tint(Theme.Color.tertiary) }
            Image(systemName: "chevron.right")
                .font(.system(size: 18, weight: .medium))
                .foregroundStyle(Theme.Color.tertiary)
                .accessibilityHidden(true)
        }
        .padding(.vertical, 16)
        .frame(minHeight: 72)
        .contentShape(Rectangle())
    }

    private var titleText: some View {
        Text(title)
            .textStyle(Theme.Text.rowTitleStrong)
            .foregroundStyle(Theme.Color.textPrimary)
    }
}

private extension View {
    /// Row insets 20 on the sides and hairline rules between rows.
    func pickerRow() -> some View {
        listRowInsets(EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20))
            .listRowSeparator(.visible)
            .listRowSeparatorTint(Theme.Color.hairline)
            .listRowBackground(Theme.Color.ground)
    }

    /// A header, footer or message placed in the list: its own padding, no rules.
    func pickerChromeRow() -> some View {
        listRowInsets(EdgeInsets())
            .listRowSeparator(.hidden)
            .listRowBackground(Theme.Color.ground)
    }

    func pickerList() -> some View {
        listStyle(.plain)
            .scrollContentBackground(.hidden)
            .background(Theme.Color.ground)
    }
}
