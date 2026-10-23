import SwiftUI

/// The signed-out screen: one large "Sign in with Procore" button.
struct SignInView: View {
    let isSigningIn: Bool
    let error: AuthError?
    let onSignIn: () -> Void

    var body: some View {
        VStack(spacing: 24) {
            Spacer()
            Text("Field Assistant")
                .font(.largeTitle.bold())
            Spacer()
            if let message = errorMessage {
                Text(message)
                    .font(.callout)
                    .foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }
            Button(action: onSignIn) {
                Group {
                    if isSigningIn {
                        ProgressView()
                            .tint(.white)
                    } else {
                        Text("Sign in with Procore")
                            .font(.title3.weight(.semibold))
                    }
                }
                .frame(maxWidth: .infinity, minHeight: 56)
            }
            .buttonStyle(.borderedProminent)
            .disabled(isSigningIn)
            .accessibilityLabel("Sign in with Procore")
        }
        .padding(24)
    }

    private var errorMessage: String? {
        switch error {
        case .none, .cancelled:
            nil
        case .invalidCallback:
            "Sign-in did not complete. Please try again."
        case .server(_, let message):
            message
        }
    }
}

#Preview("Idle") {
    SignInView(isSigningIn: false, error: nil, onSignIn: {})
}

#Preview("Error") {
    SignInView(isSigningIn: false, error: .server(code: "invalid_code", message: "Code expired"), onSignIn: {})
}
