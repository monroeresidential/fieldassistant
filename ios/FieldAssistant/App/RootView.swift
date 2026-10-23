import SwiftUI

struct RootView: View {
    @Environment(AppState.self) private var appState

    var body: some View {
        switch appState.phase {
        case .loading:
            ProgressView()
        case .signedOut:
            SignInView(isSigningIn: appState.isSigningIn, error: appState.signInError) {
                Task { await appState.signIn() }
            }
        case .signedIn, .offline:
            // Replaced by the company/project picker in Task 16.
            Text("Signed in")
        }
    }
}

#Preview {
    RootView()
        .environment(
            AppState(
                store: KeychainSessionStore(),
                presenter: ASWebAuthenticationSessionPresenter(),
                baseURL: URL(string: "https://example.invalid/v1")!
            )
        )
}
