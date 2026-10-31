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
                StoreRecoveryView(error: failure, onRetry: { launch.retry() }, onReset: { launch.reset() })
                    .onChange(of: scenePhase) { _, phase in
                        // A failure on a background launch (protected data, low disk) may be gone
                        // by the time the user opens the app; try once before offering the reset.
                        if phase == .active { launch.retry() }
                    }
            }
        }
    }
}

/// What the app launched into: the app itself over an open store, or the recovery screen when the
/// store could not be opened. A retry or a reset that reopens the store swaps the app in.
@MainActor
@Observable
final class AppLaunch {
    enum State {
        case ready(ModelContainer, AppState)
        case failed(AppContainer.OpenFailure)
    }

    private(set) var state: State
    /// The store file a retry or reset opens; nil is the default configuration's. Tests pass a
    /// temporary one. A reset still deletes the default draft files folder.
    @ObservationIgnored private let storeURL: URL?
    @ObservationIgnored private let makeAppState: @MainActor (ModelContainer) -> AppState
    /// Registered at launch when the store failed to open, since BackgroundTasks refuses handlers
    /// registered after launch finishes; it runs its passes on the `AppState` a retry or reset builds.
    @ObservationIgnored private var forwardingBackground: BackgroundSync?

    private static let log = Logger(subsystem: "FieldAssistant", category: "AppLaunch")

    /// - Parameters:
    ///   - opened: the launch's attempt to open the store.
    ///   - storeURL: the store a retry or reset reopens; nil is the default.
    ///   - registersBackgroundTasks: false in tests, whose host registered its handlers at launch.
    ///   - makeAppState: builds the app over an open store.
    init(
        opened: Result<ModelContainer, AppContainer.OpenFailure>,
        storeURL: URL? = nil,
        registersBackgroundTasks: Bool = true,
        makeAppState: @escaping @MainActor (ModelContainer) -> AppState = AppLaunch.liveAppState
    ) {
        self.storeURL = storeURL
        self.makeAppState = makeAppState
        switch opened {
        case .success(let container):
            let appState = makeAppState(container)
            // BackgroundTasks needs its handlers registered before the app finishes launching.
            if registersBackgroundTasks { appState.background.register() }
            state = .ready(container, appState)
        case .failure(let failure):
            Self.log.error("could not open the on-device store: \(failure.detail, privacy: .public)")
            state = .failed(failure)
            let forwarding = BackgroundSync(
                run: { [weak self] in await self?.appState?.runBackgroundPass() },
                cancelPass: { [weak self] in self?.appState?.draftSync?.cancelPass() }
            )
            if registersBackgroundTasks { forwarding.register() }
            forwardingBackground = forwarding
        }
    }

    /// Opens the store again without deleting anything, for a failure that has since cleared. A
    /// store that still cannot be opened stays on the recovery screen with the new error. Does
    /// nothing once the app is running.
    func retry() {
        guard case .failed = state else { return }
        open(after: "a retry")
    }

    /// Deletes the local data and opens a fresh store. A failure stays on the recovery screen with
    /// the new error.
    func reset() {
        do {
            try AppContainer.resetLocalData(storeURL: storeURL ?? AppContainer.defaultStoreURL)
        } catch {
            Self.log.error("could not reset local data: \(String(describing: error), privacy: .public)")
            state = .failed(.open(error))
            return
        }
        open(after: "a reset")
    }

    /// Opens the store and swaps the app in, or stays failed with the new error. Registers nothing
    /// with BackgroundTasks: the handlers registered at launch forward to the new app.
    private func open(after attempt: String) {
        switch AppContainer.makeModelContainer(storeURL: storeURL) {
        case .success(let container):
            state = .ready(container, makeAppState(container))
        case .failure(let failure):
            Self.log.error("could not open the store after \(attempt, privacy: .public): \(failure.detail, privacy: .public)")
            state = .failed(failure)
        }
    }

    private var appState: AppState? {
        if case .ready(_, let appState) = state { appState } else { nil }
    }

    static func liveAppState(_ container: ModelContainer) -> AppState {
        AppState(
            store: KeychainSessionStore(),
            presenter: ASWebAuthenticationSessionPresenter(),
            baseURL: AppConfig.apiBaseURL,
            modelContext: container.mainContext,
            backgroundGrace: SystemBackgroundGrace()
        )
    }
}
