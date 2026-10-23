import FieldAssistantAPI
import SwiftUI

/// The RFIs of the selected project, with pull to refresh.
struct RFIListView: View {
    let companyID: Int64
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @State private var model: RFIListModel

    init(client: Client, companyID: Int64, projectID: Int64) {
        self.companyID = companyID
        self.projectID = projectID
        _model = State(initialValue: RFIListModel(client: client))
    }

    /// Reload when the project changes or the session is re-established (re-sign-in, offline retry).
    private struct LoadKey: Equatable {
        let projectID: Int64
        let sessionGeneration: Int
    }

    var body: some View {
        // One List for every state, so its identity (and a running pull-to-refresh) survives `.loading`.
        List(model.lastLoaded ?? [], id: \.id) { rfi in
            RFIRow(rfi: rfi)
        }
        .listStyle(.plain)
        .overlay { overlay }
        .refreshable { await reload() }
        .task(id: LoadKey(projectID: projectID, sessionGeneration: appState.sessionGeneration)) {
            await reload()
        }
    }

    @ViewBuilder
    private var overlay: some View {
        switch model.state {
        case .idle, .loading:
            if model.lastLoaded == nil {
                ProgressView("Loading RFIs")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(Color(.systemBackground))
            }
        case .loaded(let items):
            if items.isEmpty {
                ContentUnavailableView("No RFIs", systemImage: "tray", description: Text("This project has no RFIs yet."))
            }
        case .failed(let failure):
            APIFailureView(failure: failure, subject: "RFIs") {
                Task { await reload() }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Color(.systemBackground))
        }
    }

    private func reload() async {
        await model.load(companyID: companyID, projectID: projectID)
    }
}

private struct RFIRow: View {
    let rfi: RFISummary

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("#\(rfi.number)")
                    .font(.subheadline.monospacedDigit().weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                StatusChip(status: rfi.status)
            }
            Text(rfi.subject)
                .font(.body)
            if let due = rfi.dueDate {
                Label("Due \(due.formatted(date: .abbreviated, time: .omitted))", systemImage: "calendar")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
    }
}

private struct StatusChip: View {
    let status: String

    var body: some View {
        Text(status.capitalized)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(color.opacity(0.18), in: Capsule())
            .foregroundStyle(color)
            .accessibilityLabel("Status \(status)")
    }

    private var color: Color {
        switch status.lowercased() {
        case "open": .orange
        case "closed": .green
        default: .gray
        }
    }
}

/// Explains an `APIFailure` and offers the one action that can fix it.
struct APIFailureView: View {
    let failure: APIFailure
    /// What failed to load, for example "RFIs".
    let subject: String
    let retry: () -> Void

    @Environment(AppState.self) private var appState

    var body: some View {
        switch failure {
        case .reauthRequired:
            ContentUnavailableView {
                Label("Session expired", systemImage: "person.crop.circle.badge.exclamationmark")
            } description: {
                Text("Sign in again to keep working.")
                if let message = appState.signInError?.userMessage {
                    Text(message).foregroundStyle(.red)
                }
            } actions: {
                Button {
                    Task { await appState.signIn() }
                } label: {
                    Group {
                        if appState.isSigningIn { ProgressView() } else { Text("Sign in again").font(.headline) }
                    }
                    .frame(minWidth: 160, minHeight: 44)
                }
                .buttonStyle(.borderedProminent)
                .disabled(appState.isSigningIn)
                .accessibilityLabel("Sign in again")
            }
        case .offline:
            ContentUnavailableView {
                Label("You're offline", systemImage: "wifi.slash")
            } description: {
                Text("Check your connection and try again.")
            } actions: {
                actionButton("Retry", action: retry)
            }
        case .permissionDenied(let message):
            ContentUnavailableView {
                Label("No access", systemImage: "lock")
            } description: {
                Text("\(message)\n\nAn admin must grant you access to \(subject) in Procore.")
            } actions: {
                actionButton("Retry", action: retry)
            }
        case .server(let message):
            ContentUnavailableView {
                Label("Couldn't load \(subject)", systemImage: "exclamationmark.triangle")
            } description: {
                Text(message)
            } actions: {
                actionButton("Retry", action: retry)
            }
        }
    }

    private func actionButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.headline)
                .frame(minWidth: 160, minHeight: 44)
        }
        .buttonStyle(.borderedProminent)
        .accessibilityLabel(title)
    }
}
