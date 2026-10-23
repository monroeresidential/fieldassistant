import FieldAssistantAPI
import SwiftUI

/// The RFIs of the selected project, with pull to refresh.
struct RFIListView: View {
    let companyID: Int64
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @State private var model: RFIListModel
    /// The last loaded list, kept on screen while a refresh is in flight.
    @State private var lastLoaded: [RFISummary]?

    init(client: Client, companyID: Int64, projectID: Int64) {
        self.companyID = companyID
        self.projectID = projectID
        _model = State(initialValue: RFIListModel(client: client))
    }

    var body: some View {
        content
            .task(id: projectID) {
                lastLoaded = nil
                await reload()
            }
            .onChange(of: model.state) { _, state in
                if case .loaded(let items) = state { lastLoaded = items }
            }
    }

    @ViewBuilder
    private var content: some View {
        switch model.state {
        case .loaded(let items):
            list(items)
        case .idle, .loading:
            if let lastLoaded {
                list(lastLoaded)
            } else {
                ProgressView("Loading RFIs")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        case .failed(let failure):
            APIFailureView(failure: failure, subject: "RFIs") {
                Task { await reload() }
            }
        }
    }

    private func list(_ items: [RFISummary]) -> some View {
        List(items, id: \.id) { rfi in
            RFIRow(rfi: rfi)
        }
        .listStyle(.plain)
        .overlay {
            if items.isEmpty {
                ContentUnavailableView("No RFIs", systemImage: "tray", description: Text("This project has no RFIs yet."))
            }
        }
        .refreshable { await reload() }
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
            } actions: {
                actionButton("Sign in again") { Task { await appState.signIn() } }
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
