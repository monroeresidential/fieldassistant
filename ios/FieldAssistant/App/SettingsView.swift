import SwiftUI

/// Submitting preference, project lookups, account, and the build this phone runs.
struct SettingsView: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        let settings = appState.settings
        Form {
            Section("Submitting") {
                Toggle(isOn: Binding(
                    get: { settings.submitAsDraft ?? true },
                    set: { settings.submitAsDraft = $0 }
                )) {
                    Text("Submit as Procore draft")
                }
                .frame(minHeight: 44)
                .accessibilityHint("When on, RFIs arrive in Procore as drafts for a last check there")
            }

            Section("Project") {
                Button {
                    refreshLookups()
                } label: {
                    HStack {
                        Label("Refresh people and locations", systemImage: "arrow.clockwise")
                        Spacer()
                        if appState.lookups.isPrefetching { ProgressView() }
                    }
                    .frame(minHeight: 44)
                    .contentShape(Rectangle())
                }
                .disabled(appState.lookups.isPrefetching || appState.selectedProjectID == nil)
                .accessibilityLabel(appState.lookups.isPrefetching ? "Refreshing people and locations" : "Refresh people and locations")

                Button("Change project") {
                    appState.selectedProjectID = nil
                }
                .frame(minHeight: 44)
                .accessibilityLabel("Change project")
            }

            Section {
                if case .signedIn(let user) = appState.phase {
                    LabeledContent("Signed in as", value: user.name)
                        .frame(minHeight: 44)
                }
                Button("Sign out", role: .destructive) {
                    Task { await appState.signOut() }
                }
                .frame(minHeight: 44)
                .accessibilityLabel("Sign out")
            } header: {
                Text("Account")
            } footer: {
                Text(Self.buildLine)
                    .font(.caption2)
                    .padding(.top, 16)
            }
        }
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func refreshLookups() {
        guard let companyID = appState.selectedCompanyID, let projectID = appState.selectedProjectID else { return }
        let lookups = appState.lookups!
        Task { await lookups.prefetch(companyID: companyID, projectID: projectID, force: true) }
    }

    /// "Field Assistant 1.0 (12) · local · localhost:8080"
    private static var buildLine: String {
        let info = Bundle.main.infoDictionary
        let version = info?["CFBundleShortVersionString"] as? String ?? "?"
        let build = info?["CFBundleVersion"] as? String ?? "?"
        let host = AppConfig.apiBaseURL.host(percentEncoded: false).map { host in
            AppConfig.apiBaseURL.port.map { "\(host):\($0)" } ?? host
        } ?? ""
        return "Field Assistant \(version) (\(build)) · \(AppConfig.environment) · \(host)"
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    NavigationStack { SettingsView() }
        .environment(AppState.preview(container: container))
        .modelContainer(container)
}
