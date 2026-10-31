import SwiftUI

struct RootView: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        switch appState.phase {
        case .loading:
            ProgressView()
                .tint(Theme.Color.tertiary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.Color.ground.ignoresSafeArea())
        case .signedOut:
            SignInView(
                isSigningIn: appState.isSigningIn,
                error: appState.signInError,
                note: AppState.signInNote(for: appState.signOutReason)
            ) {
                Task { await appState.signIn() }
            }
        case .signedIn, .offline:
            // Offline keeps the user in the app with their last selection; HomeView shows a banner.
            if let companyID = appState.selectedCompanyID, let projectID = appState.selectedProjectID {
                HomeView(companyID: companyID, projectID: projectID)
            } else {
                ProjectPickerView()
            }
        }
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true).get()
    RootView()
        .environment(
            AppState(
                store: KeychainSessionStore(),
                presenter: ASWebAuthenticationSessionPresenter(),
                baseURL: URL(string: "https://example.invalid/v1")!,
                modelContext: container.mainContext
            )
        )
        .modelContainer(container)
}
