import SwiftData
import SwiftUI

@main
struct FieldAssistantApp: App {
    private let container: ModelContainer
    @State private var appState: AppState
    @Environment(\.scenePhase) private var scenePhase

    init() {
        let container: ModelContainer
        do {
            container = try AppContainer.makeModelContainer()
        } catch {
            fatalError("Could not open the on-device store: \(error)")
        }
        self.container = container
        let appState = AppState(
            store: KeychainSessionStore(),
            presenter: ASWebAuthenticationSessionPresenter(),
            baseURL: AppConfig.apiBaseURL,
            modelContext: container.mainContext
        )
        // BackgroundTasks needs its handlers registered before the app finishes launching.
        appState.background.register()
        _appState = State(initialValue: appState)
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appState)
                .task { await appState.bootstrap() }
                .onChange(of: scenePhase) { _, phase in
                    // Coming back to the app is a chance to reconnect, send waiting drafts and settle submitting ones.
                    if phase == .active { Task { await appState.becameActive() } }
                    // Going away with a draft waiting or queued asks iOS for a background run.
                    if phase == .background { appState.enteredBackground() }
                }
        }
        .modelContainer(container)
    }
}
