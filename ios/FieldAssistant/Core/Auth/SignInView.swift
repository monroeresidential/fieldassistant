import SwiftUI

/// The signed-out screen (handoff 1a): charcoal ground in both modes, the hero and one
/// "Sign in with Procore" button.
struct SignInView: View {
    let isSigningIn: Bool
    let error: AuthError?
    let onSignIn: () -> Void

    /// The hero's line-height .98 as a gap between its two lines, scaled with Dynamic Type.
    @ScaledMetric(relativeTo: .largeTitle) private var heroLineGap = Theme.Text.hero.lineSpacing

    /// "FIELD ASSISTANT" beside the mark: 12/600 .14em.
    private static let brandKicker = Theme.TextStyle(12, .semibold, relativeTo: .caption, em: 0.14)
    /// The tagline: 17 lh 1.5.
    private static let tagline = Theme.TextStyle(17, .regular, relativeTo: .body, lineHeight: 1.5)
    /// The note under the button: 12 lh 1.5.
    private static let note = Theme.TextStyle(12, .regular, relativeTo: .caption, lineHeight: 1.5)

    var body: some View {
        GeometryReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    header
                    Spacer(minLength: 40)
                    hero
                    Spacer(minLength: 40)
                    footer
                }
                // The handoff's 70 / 56 include the status bar and home indicator; inside the safe
                // area that leaves 16 at the top and 22 at the bottom.
                .padding(.top, 16)
                .padding(.horizontal, 28)
                .padding(.bottom, 22)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height, alignment: .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .background(Theme.Color.charcoal.ignoresSafeArea())
        .environment(\.colorScheme, .dark)
    }

    private var header: some View {
        HStack(spacing: 12) {
            LogoMark(.onDark, size: 44)
            Text("Field Assistant".uppercased())
                .textStyle(Self.brandKicker)
                .foregroundStyle(Theme.Color.textOnDarkSecondary)
            Spacer(minLength: 8)
            if AppConfig.isDev {
                Text("DEV")
                    .textStyle(Theme.Text.label)
                    .foregroundStyle(Theme.Color.textOnDarkSecondary)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .overlay {
                        RoundedRectangle(cornerRadius: 4)
                            .strokeBorder(Theme.Color.textOnDarkSecondary, lineWidth: 1)
                    }
                    .accessibilityLabel("Development build")
            }
        }
    }

    private var hero: some View {
        VStack(alignment: .leading, spacing: 18) {
            Kicker("Procore, by voice", style: .hero)
            // Two lines stacked with a negative gap: SwiftUI clamps negative `lineSpacing` to zero,
            // so line-height .98 is drawn this way. Each line shrinks rather than breaking mid-word.
            VStack(alignment: .leading, spacing: heroLineGap) {
                ForEach(["Field", "Assistant"], id: \.self) { line in
                    Text(line)
                        .textStyle(Theme.Text.hero)
                        .lineLimit(1)
                        .minimumScaleFactor(0.5)
                }
            }
            .foregroundStyle(Color.white)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Field Assistant")
            .accessibilityAddTraits(.isHeader)
            Rectangle()
                .fill(Theme.Color.brand)
                .frame(width: 48, height: 3)
                .accessibilityHidden(true)
            Text("Dictate an RFI on site. Review the draft. Tap once to send it to Procore.")
                .textStyle(Self.tagline)
                .foregroundStyle(Theme.Color.tagline)
                .frame(maxWidth: 300, alignment: .leading)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var footer: some View {
        VStack(spacing: 12) {
            Button(action: onSignIn) {
                if isSigningIn {
                    ProgressView()
                        .tint(Theme.Color.tertiary)
                } else {
                    Text("Sign in with Procore")
                        .multilineTextAlignment(.center)
                }
            }
            .buttonStyle(PrimaryButtonStyle(onDark: true, height: 56))
            .disabled(isSigningIn)
            .accessibilityLabel("Sign in with Procore")

            Text(errorMessage ?? "Sign-in happens on Procore's own page. This app never sees your password.")
                .textStyle(Self.note)
                .foregroundStyle(errorMessage == nil ? Theme.Color.textOnDarkSecondary : Theme.Color.brand)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var errorMessage: String? { error?.userMessage }
}

extension AuthError {
    /// What to tell the user about a failed sign-in; `nil` when they closed the sheet themselves.
    var userMessage: String? {
        switch self {
        case .cancelled:
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

#Preview("Signing in") {
    SignInView(isSigningIn: true, error: nil, onSignIn: {})
}

#Preview("Error") {
    SignInView(isSigningIn: false, error: .server(code: "invalid_code", message: "Code expired"), onSignIn: {})
}
