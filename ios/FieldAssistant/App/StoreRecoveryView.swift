import SwiftUI

/// Shown at launch instead of the app when the on-device store cannot be opened. Try again reopens
/// it without deleting anything; the way on when that keeps failing is to delete the local data.
/// The error stays on screen, small, for whoever is asked about it.
struct StoreRecoveryView: View {
    let error: AppContainer.OpenFailure
    let onRetry: () -> Void
    let onReset: () -> Void

    var body: some View {
        GeometryReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    VStack(alignment: .leading, spacing: 12) {
                        Kicker("Field Assistant")
                        Text("Local data couldn't be opened")
                            .textStyle(Theme.Text.sectionTitle)
                            .foregroundStyle(Theme.Color.textPrimary)
                            .accessibilityAddTraits(.isHeader)
                        Text("This phone's saved drafts and lookups could not be read. Reset local data to continue; drafts not yet sent will be lost.")
                            .textStyle(Theme.Text.body)
                            .foregroundStyle(Theme.Color.textSecondary)
                        Text(error.detail)
                            .textStyle(Theme.Text.meta12)
                            .foregroundStyle(Theme.Color.tertiary)
                            .textSelection(.enabled)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 40)
                    VStack(spacing: 10) {
                        Button("Reset local data", action: onReset)
                            .buttonStyle(PrimaryButtonStyle())
                        Button("Try again", action: onRetry)
                            .buttonStyle(SecondaryButtonStyle())
                            .accessibilityHint("Opens the local data again without deleting anything")
                    }
                }
                .padding(.top, 40)
                .padding(.horizontal, 24)
                .padding(.bottom, 22)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height, alignment: .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .background(Theme.Color.ground.ignoresSafeArea())
    }
}

extension AppContainer.OpenFailure {
    /// The underlying error, for the recovery screen's small print and the log.
    var detail: String {
        switch self {
        case .open(let error): String(describing: error)
        }
    }
}

#Preview {
    StoreRecoveryView(error: .open(CocoaError(.fileReadCorruptFile)), onRetry: {}, onReset: {})
}
