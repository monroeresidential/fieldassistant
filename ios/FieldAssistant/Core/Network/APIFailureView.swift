import SwiftUI

/// Explains an `APIFailure` and offers the one action that can fix it: a kicker title, body text
/// and an outline button, centred in the space it is given and scrolling at large text sizes.
struct APIFailureView: View {
    let failure: APIFailure
    /// What failed to load, for example "companies".
    let subject: String
    let retry: () -> Void

    @Environment(AppState.self) private var appState

    var body: some View {
        GeometryReader { proxy in
            ScrollView {
                message
                    .frame(maxWidth: .infinity, minHeight: proxy.size.height)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
    }

    @ViewBuilder
    private var message: some View {
        switch failure {
        case .reauthRequired:
            FailureMessage(title: "Session expired", message: "Sign in again to keep working.",
                           error: appState.signInError?.userMessage) {
                Button {
                    Task { await appState.signIn() }
                } label: {
                    if appState.isSigningIn {
                        ProgressView().tint(Theme.Color.tertiary)
                    } else {
                        Text("Sign in again")
                    }
                }
                .buttonStyle(SecondaryButtonStyle())
                .disabled(appState.isSigningIn)
                .accessibilityLabel("Sign in again")
            }
        case .offline:
            FailureMessage(title: "You're offline", message: "Check your connection and try again.") {
                actionButton("Retry", action: retry)
            }
        case .permissionDenied(let message):
            FailureMessage(title: "No access",
                           message: "\(message)\n\nAn admin must grant you access to \(subject) in Procore.") {
                actionButton("Retry", action: retry)
            }
        case .validation(let message), .notFound(let message), .server(let message):
            FailureMessage(title: "Couldn't load \(subject)", message: message) {
                actionButton("Retry", action: retry)
            }
        }
    }

    private func actionButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(title, action: action)
            .buttonStyle(SecondaryButtonStyle())
            .accessibilityLabel(title)
    }
}

/// A centred kicker title, an optional body message and an optional action; also used for empty lists.
struct FailureMessage<Action: View>: View {
    let title: String
    let message: String?
    var error: String?
    @ViewBuilder var action: Action

    init(title: String, message: String?, error: String? = nil, @ViewBuilder action: () -> Action) {
        self.title = title
        self.message = message
        self.error = error
        self.action = action()
    }

    var body: some View {
        VStack(spacing: 12) {
            Kicker(title)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)
            if let message {
                Text(message)
                    .textStyle(Theme.Text.body)
                    .foregroundStyle(Theme.Color.textSecondary)
                    .multilineTextAlignment(.center)
            }
            if let error {
                Text(error)
                    .textStyle(Theme.Text.body)
                    .foregroundStyle(Theme.Color.accent)
                    .multilineTextAlignment(.center)
            }
            action
                .padding(.top, 12)
        }
        .frame(maxWidth: 360)
        .padding(20)
        .frame(maxWidth: .infinity)
    }
}

extension FailureMessage where Action == EmptyView {
    /// A message with no action, such as an empty list.
    init(title: String, message: String?) {
        self.init(title: title, message: message) { EmptyView() }
    }
}
