import SwiftUI

/// Two labelled halves of equal width under a top hairline, split by a vertical hairline.
/// The left cell pads 14 10 14 0 and the right 14 0 14 14, as in the handoff's grid.
struct PairedRow<L: View, R: View>: View {
    let leftLabel: String
    let rightLabel: String
    @ViewBuilder var left: () -> L
    @ViewBuilder var right: () -> R

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            // Equal columns, padding inside them, like the handoff's 1fr 1fr grid.
            WeightedRow(spacing: 0, weights: [1, 1]) {
                half(label: leftLabel, content: left)
                    .padding(.vertical, 14)
                    .padding(.trailing, 10)
                    .frame(maxHeight: .infinity, alignment: .top)
                half(label: rightLabel, content: right)
                    .padding(.vertical, 14)
                    .padding(.leading, 14)
                    .frame(maxHeight: .infinity, alignment: .top)
            }
            // The split sits at the centre, where the left column ends, even if its control runs wide.
            .overlay {
                Rectangle()
                    .fill(Theme.Color.hairline)
                    .frame(width: 1)
                    .accessibilityHidden(true)
            }
        }
    }

    private func half<Content: View>(label: String, content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Kicker(label, style: .label, color: Theme.Color.tertiary)
            content()
                .textStyle(Theme.Text.body)
                .foregroundStyle(Theme.Color.textPrimary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    PairedRow(leftLabel: "Due", rightLabel: "Drawing") {
        Text("Oct 30")
    } right: {
        Text("A-201")
    }
    .padding(.horizontal, 20)
}
