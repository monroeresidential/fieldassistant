import SwiftUI

/// A 1 pt rule in the hairline token.
struct HairlineDivider: View {
    var body: some View {
        Rectangle()
            .fill(Theme.Color.hairline)
            .frame(height: 1)
            .accessibilityHidden(true)
    }
}

extension View {
    /// A 1 pt rounded border: `hairline`, or `hairlineStrong` for cards and buttons on dark;
    /// dashed for placeholders such as the add-photo button and features not shipped yet.
    func hairlineBorder(radius: CGFloat = 8, strong: Bool = false, dashed: Bool = false) -> some View {
        overlay {
            RoundedRectangle(cornerRadius: radius)
                .strokeBorder(strong ? Theme.Color.hairlineStrong : Theme.Color.hairline,
                              style: StrokeStyle(lineWidth: 1, dash: dashed ? [4, 4] : []))
        }
    }
}

#Preview {
    VStack(spacing: 16) {
        HairlineDivider()
        Text("Card").frame(maxWidth: .infinity, minHeight: 88).hairlineBorder()
        Text("Coming").frame(maxWidth: .infinity, minHeight: 88).hairlineBorder(dashed: true)
    }
    .padding()
}
