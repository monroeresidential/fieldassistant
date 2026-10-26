import SwiftUI

/// Explains an `APIFailure` and offers the one action that can fix it.
struct APIFailureView: View {
    let failure: APIFailure
    /// What failed to load, for example "companies".
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
        case .validation(let message), .server(let message):
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
