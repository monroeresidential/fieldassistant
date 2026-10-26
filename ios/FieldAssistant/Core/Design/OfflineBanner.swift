import SwiftUI

/// The no-signal strip: charcoal (panel in dark mode), white 13/500 text, radius 8.
struct OfflineBanner: View {
    let text: String
    @Environment(\.colorScheme) private var colorScheme

    init(_ text: String = "No signal. Recording works; sending waits.") {
        self.text = text
    }

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "wifi.slash")
                .font(.system(size: 16, weight: .medium))
                .accessibilityHidden(true)
            Text(text)
                .font(.publicSans(13, weight: .medium, relativeTo: .footnote))
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .foregroundStyle(Color.white)
        .padding(.vertical, 10)
        .padding(.horizontal, 14)
        .background(colorScheme == .dark ? Theme.Color.panel : Theme.Color.charcoal,
                    in: RoundedRectangle(cornerRadius: 8))
        .accessibilityElement(children: .combine)
    }
}

#Preview {
    OfflineBanner().padding()
}
