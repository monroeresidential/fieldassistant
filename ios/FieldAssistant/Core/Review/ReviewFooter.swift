import SwiftUI

/// Sticky bottom bar over a hairline: save as a draft or submit, with any note above the buttons.
/// Submit stays disabled while a blocking flag remains or a submit is running.
struct ReviewFooter<Notes: View>: View {
    let blockingCount: Int
    let isSubmitting: Bool
    let onSave: () -> Void
    let onSubmit: () -> Void
    @ViewBuilder var notes: () -> Notes

    init(
        blockingCount: Int,
        isSubmitting: Bool,
        onSave: @escaping () -> Void,
        onSubmit: @escaping () -> Void,
        @ViewBuilder notes: @escaping () -> Notes
    ) {
        self.blockingCount = blockingCount
        self.isSubmitting = isSubmitting
        self.onSave = onSave
        self.onSubmit = onSubmit
        self.notes = notes
    }

    private var submitDisabled: Bool { blockingCount > 0 || isSubmitting }

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            VStack(spacing: 10) {
                notes()
                WeightedRow(spacing: 10, weights: [1, 1.4]) {
                    Button(action: onSave) {
                        Text("Save draft")
                    }
                    .buttonStyle(SecondaryButtonStyle(height: 54))
                    .disabled(isSubmitting)
                    .accessibilityLabel("Save draft")

                    Button(action: onSubmit) {
                        HStack(spacing: 8) {
                            if isSubmitting { ProgressView().tint(Theme.Color.tertiary) }
                            Text("Submit to Procore")
                                .textStyle(Theme.Text.buttonMedium)
                        }
                    }
                    .buttonStyle(PrimaryButtonStyle(height: 54))
                    .disabled(submitDisabled)
                    .accessibilityLabel(isSubmitting ? "Submitting to Procore" : "Submit to Procore")
                    .accessibilityHint(blockingCount > 0 ? "Resolve the flagged fields first" : "")
                }
            }
            .padding(.horizontal, 20)
            .padding(.top, 12)
            // The handoff's 44 bottom includes the 34 pt home indicator.
            .padding(.bottom, 10)
        }
        .background(Theme.Color.ground.ignoresSafeArea())
    }
}

extension ReviewFooter where Notes == EmptyView {
    init(blockingCount: Int, isSubmitting: Bool, onSave: @escaping () -> Void, onSubmit: @escaping () -> Void) {
        self.init(blockingCount: blockingCount, isSubmitting: isSubmitting, onSave: onSave, onSubmit: onSubmit) {
            EmptyView()
        }
    }
}

/// Lays its children out in one row, sharing the width in proportion to `weights` (the footer's
/// flex 1 and 1.4, the paired rows' equal columns); every child gets the height of the tallest.
struct WeightedRow: Layout {
    var spacing: CGFloat
    var weights: [CGFloat]

    /// Each child's width out of `total`; children past the end of `weights` weigh 1.
    static func widths(total: CGFloat, count: Int, spacing: CGFloat, weights: [CGFloat]) -> [CGFloat] {
        guard count > 0 else { return [] }
        let available = max(0, total - spacing * CGFloat(count - 1))
        let shares = (0..<count).map { $0 < weights.count ? weights[$0] : 1 }
        let sum = shares.reduce(0, +)
        return shares.map { sum > 0 ? available * $0 / sum : 0 }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width
            ?? subviews.map { $0.sizeThatFits(.unspecified).width }.reduce(0, +) + spacing * CGFloat(max(0, subviews.count - 1))
        let widths = Self.widths(total: width, count: subviews.count, spacing: spacing, weights: weights)
        let height = zip(subviews, widths)
            .map { $0.sizeThatFits(ProposedViewSize(width: $1, height: nil)).height }
            .max() ?? 0
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let widths = Self.widths(total: bounds.width, count: subviews.count, spacing: spacing, weights: weights)
        var x = bounds.minX
        for (subview, width) in zip(subviews, widths) {
            subview.place(at: CGPoint(x: x, y: bounds.minY), proposal: ProposedViewSize(width: width, height: bounds.height))
            x += width + spacing
        }
    }
}

#Preview("Ready") {
    ReviewFooter(blockingCount: 0, isSubmitting: false, onSave: {}, onSubmit: {})
}

#Preview("Blocked") {
    ReviewFooter(blockingCount: 1, isSubmitting: false, onSave: {}, onSubmit: {})
}

#Preview("Submitting") {
    ReviewFooter(blockingCount: 0, isSubmitting: true, onSave: {}, onSubmit: {}) {
        Text("Draft saved").textStyle(Theme.Text.meta13).foregroundStyle(Theme.Color.muted)
    }
}
