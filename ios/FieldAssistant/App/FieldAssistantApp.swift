import SwiftData
import SwiftUI

@main
struct FieldAssistantApp: App {
    private let container: ModelContainer
    @State private var appState: AppState

    init() {
        let container: ModelContainer
        do {
            container = try AppContainer.makeModelContainer()
        } catch {
            fatalError("Could not open the on-device store: \(error)")
        }
        self.container = container
        _appState = State(initialValue: AppState(
            store: KeychainSessionStore(),
            presenter: ASWebAuthenticationSessionPresenter(),
            baseURL: AppConfig.apiBaseURL,
            modelContext: container.mainContext
        ))
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appState)
                .task { await appState.bootstrap() }
        }
        .modelContainer(container)
    }
}
