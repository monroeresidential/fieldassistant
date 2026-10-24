import SwiftUI

/// Sticky bottom bar: save as a draft or submit. Submit stays disabled while a blocking flag remains or a submit is running.
struct ReviewFooter: View {
    let blockingCount: Int
    let isSubmitting: Bool
    let onSave: () -> Void
    let onSubmit: () -> Void

    private var submitDisabled: Bool { blockingCount > 0 || isSubmitting }

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 12) {
                Button(action: onSave) {
                    Text("Save draft")
                        .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.bordered)
                .disabled(isSubmitting)
                .accessibilityLabel("Save draft")

                Button(action: onSubmit) {
                    HStack(spacing: 8) {
                        if isSubmitting { ProgressView().tint(.white) }
                        Text("Submit to Procore")
                    }
                    .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.borderedProminent)
                .disabled(submitDisabled)
                .accessibilityLabel(isSubmitting ? "Submitting to Procore" : "Submit to Procore")
                .accessibilityHint(blockingCount > 0 ? "Resolve the flagged fields first" : "")
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
        .background(.bar)
    }
}

#Preview("Ready") {
    ReviewFooter(blockingCount: 0, isSubmitting: false, onSave: {}, onSubmit: {})
}

#Preview("Blocked") {
    ReviewFooter(blockingCount: 1, isSubmitting: false, onSave: {}, onSubmit: {})
}

#Preview("Submitting") {
    ReviewFooter(blockingCount: 0, isSubmitting: true, onSave: {}, onSubmit: {})
}
