import OSLog
import SwiftData
import SwiftUI

@main
struct FieldAssistantApp: App {
    @State private var launch: AppLaunch
    @Environment(\.scenePhase) private var scenePhase

    init() {
        _launch = State(initialValue: AppLaunch(opened: AppContainer.makeModelContainer()))
    }

    var body: some Scene {
        WindowGroup {
            switch launch.state {
            case .ready(let container, let appState):
                RootView()
                    .environment(appState)
                    .task { await appState.bootstrap() }
                    .onChange(of: scenePhase) { _, phase in
                        // Coming back to the app is a chance to reconnect, send waiting drafts and settle submitting ones.
                        if phase == .active { Task { await appState.becameActive() } }
                        // Going away with a draft waiting or queued asks iOS for a background run.
                        if phase == .background { appState.enteredBackground() }
                    }
                    .modelContainer(container)
            case .failed(let failure):
                StoreRecoveryView(error: failure) { launch.reset() }
            }
        }
    }
}

/// What the app launched into: the app itself over an open store, or the recovery screen when the
/// store could not be opened. A reset that reopens the store swaps the app in.
@MainActor
@Observable
final class AppLaunch {
    enum State {
        case ready(ModelContainer, AppState)
        case failed(AppContainer.OpenFailure)
    }

    private(set) var state: State
    /// Registered at launch when the store failed to open, since BackgroundTasks refuses handlers
    /// registered after launch finishes; it runs its passes on the `AppState` a reset builds.
    @ObservationIgnored private var forwardingBackground: BackgroundSync?

    private static let log = Logger(subsystem: "FieldAssistant", category: "AppLaunch")

    init(opened: Result<ModelContainer, AppContainer.OpenFailure>) {
        switch opened {
        case .success(let container):
            let appState = Self.makeAppState(container)
            // BackgroundTasks needs its handlers registered before the app finishes launching.
            appState.background.register()
            state = .ready(container, appState)
        case .failure(let failure):
            Self.log.error("could not open the on-device store: \(failure.detail, privacy: .public)")
            state = .failed(failure)
            let forwarding = BackgroundSync(
                run: { [weak self] in await self?.appState?.runBackgroundPass() },
                cancelPass: { [weak self] in self?.appState?.draftSync?.cancelPass() }
            )
            forwarding.register()
            forwardingBackground = forwarding
        }
    }

    /// Deletes the local data and opens a fresh store. A failure stays on the recovery screen with
    /// the new error.
    func reset() {
        do {
            try AppContainer.resetLocalData()
        } catch {
            Self.log.error("could not reset local data: \(String(describing: error), privacy: .public)")
            state = .failed(.open(error))
            return
        }
        switch AppContainer.makeModelContainer() {
        case .success(let container):
            state = .ready(container, Self.makeAppState(container))
        case .failure(let failure):
            Self.log.error("could not open the store after a reset: \(failure.detail, privacy: .public)")
            state = .failed(failure)
        }
    }

    private var appState: AppState? {
        if case .ready(_, let appState) = state { appState } else { nil }
    }

    private static func makeAppState(_ container: ModelContainer) -> AppState {
        AppState(
            store: KeychainSessionStore(),
            presenter: ASWebAuthenticationSessionPresenter(),
            baseURL: AppConfig.apiBaseURL,
            modelContext: container.mainContext,
            backgroundGrace: SystemBackgroundGrace()
        )
    }
}
