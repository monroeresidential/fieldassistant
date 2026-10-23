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

    @Environment(AppState.self) private var appState
    @State private var step: Step?
    @State private var companies: Load<[Company]> = .loading
    @State private var projects: Load<[Project]> = .loading
    @State private var search = ""
    @State private var savingProjectID: Int64?
    @State private var saveError: APIFailure?

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
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Sign out") { Task { await appState.signOut() } }
                        .frame(minHeight: 44)
                        .accessibilityLabel("Sign out")
                }
            }
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
        Group {
            switch companies {
            case .loading:
                ProgressView("Loading companies").frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed(let failure):
                APIFailureView(failure: failure, subject: "companies") { Task { await loadCompanies() } }
            case .loaded(let list):
                List(list, id: \.id) { company in
                    Button {
                        search = ""
                        step = .projects(companyID: company.id, companyName: company.name)
                    } label: {
                        row(company.name)
                    }
                    .accessibilityLabel("Company \(company.name)")
                }
                .overlay {
                    if list.isEmpty {
                        ContentUnavailableView("No companies", systemImage: "building.2", description: Text("Your Procore account has no companies."))
                    }
                }
                .refreshable { await loadCompanies() }
            }
        }
        .navigationTitle("Choose a company")
        .task { await loadCompanies() }
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
        Group {
            switch projects {
            case .loading:
                ProgressView("Loading projects").frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed(let failure):
                APIFailureView(failure: failure, subject: "this company's projects") {
                    Task { await loadProjects(companyID: companyID) }
                }
            case .loaded(let list):
                let shown = filtered(list)
                List(shown, id: \.id) { project in
                    Button {
                        Task { await choose(project, companyID: companyID) }
                    } label: {
                        HStack {
                            row(HomeView.title(project))
                            if savingProjectID == project.id { ProgressView() }
                        }
                    }
                    .disabled(savingProjectID != nil)
                    .accessibilityLabel("Project \(HomeView.title(project))")
                }
                .overlay {
                    if list.isEmpty {
                        ContentUnavailableView("No projects", systemImage: "folder", description: Text("You have no projects in this company."))
                    } else if shown.isEmpty {
                        ContentUnavailableView.search(text: search)
                    }
                }
                .refreshable { await loadProjects(companyID: companyID) }
            }
        }
        .searchable(text: $search, prompt: "Search projects")
        .navigationTitle(companyName ?? "Choose a project")
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button("Companies", systemImage: "chevron.left") {
                    search = ""
                    step = .companies
                }
                .labelStyle(.titleAndIcon)
                .frame(minHeight: 44)
                .accessibilityLabel("Back to companies")
            }
        }
        .task(id: companyID) { await loadProjects(companyID: companyID) }
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

    private func row(_ title: String) -> some View {
        Text(title)
            .foregroundStyle(.primary)
            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            .contentShape(Rectangle())
    }

    private static func message(for failure: APIFailure) -> String {
        switch failure {
        case .permissionDenied(let message), .server(let message): message
        case .reauthRequired: "Your session expired. Sign in again."
        case .offline: "You're offline."
        }
    }
}
