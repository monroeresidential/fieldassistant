import FieldAssistantAPI
import SwiftUI

/// The signed-in home screen for the selected project: its RFIs, with project and account controls.
struct HomeView: View {
    let companyID: Int64
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @State private var project: Project?
    @State private var isRetrying = false

    var body: some View {
        NavigationStack {
            RFIListView(client: appState.client, companyID: companyID, projectID: projectID)
                // A fresh list model per project; reloads within a project follow `sessionGeneration`.
                .id(projectID)
                .navigationTitle(project.map(Self.title) ?? "RFIs")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { toolbar }
                .safeAreaInset(edge: .top, spacing: 0) {
                    if appState.phase == .offline { offlineBanner }
                }
        }
        .task(id: TitleKey(projectID: projectID, sessionGeneration: appState.sessionGeneration)) { await loadProject() }
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

    private var offlineBanner: some View {
        HStack {
            Label("Offline. Some data may not load.", systemImage: "wifi.slash")
                .font(.footnote.weight(.medium))
            Spacer()
            Button {
                Task {
                    isRetrying = true
                    await appState.bootstrap()
                    isRetrying = false
                }
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
