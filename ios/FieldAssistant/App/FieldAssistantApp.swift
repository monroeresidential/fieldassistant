import SwiftUI

@main
struct FieldAssistantApp: App {
    @State private var appState = AppState(
        store: KeychainSessionStore(),
        presenter: ASWebAuthenticationSessionPresenter(),
        baseURL: AppConfig.apiBaseURL
    )

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appState)
                .task { await appState.bootstrap() }
        }
    }
}
