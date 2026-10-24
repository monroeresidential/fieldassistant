import SwiftUI

/// Two labelled halves side by side, split by a vertical divider, over a hairline.
struct PairedRow<L: View, R: View>: View {
    let leftLabel: String
    let rightLabel: String
    @ViewBuilder var left: () -> L
    @ViewBuilder var right: () -> R

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                half(label: leftLabel, content: left)
                Divider()
                half(label: rightLabel, content: right)
            }
            .fixedSize(horizontal: false, vertical: true)
            Divider()
        }
    }

    private func half<Content: View>(label: String, content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            ReviewLabel(text: label)
            content().font(.body)
        }
        .padding(.vertical, 8)
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    PairedRow(leftLabel: "Due", rightLabel: "Drawing") {
        Text("Oct 30")
    } right: {
        Text("A-201")
    }
}
