import SwiftUI

/// A collapsible row's header: 52 tall, 14/500 muted title with a `chevron.down` that turns up when open.
struct DisclosureRow: View {
    let title: String
    let accessibilityTitle: String
    @Binding var expanded: Bool

    private static let titleStyle = Theme.TextStyle(14, .medium, relativeTo: .subheadline)

    var body: some View {
        Button {
            withAnimation(.easeInOut(duration: 0.2)) { expanded.toggle() }
        } label: {
            HStack(spacing: 8) {
                Text(title)
                    .textStyle(Self.titleStyle)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.down")
                    .font(.system(size: 16, weight: .medium))
                    .rotationEffect(.degrees(expanded ? 180 : 0))
                    .accessibilityHidden(true)
            }
            .foregroundStyle(Theme.Color.muted)
            .frame(minHeight: 52)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(accessibilityTitle)
        .accessibilityValue(expanded ? "Expanded" : "Collapsed")
    }
}

/// The original dictation, collapsed until the user asks for it; hairlines above and below.
struct TranscriptDisclosure: View {
    let transcript: String
    @State private var expanded = false

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            DisclosureRow(
                title: "Field notes · original transcript",
                accessibilityTitle: "Field notes, original transcript",
                expanded: $expanded
            )
            if expanded {
                Text(transcript)
                    .textStyle(Theme.Text.body)
                    .foregroundStyle(Theme.Color.muted)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.bottom, 14)
                    .textSelection(.enabled)
            }
            HairlineDivider()
        }
    }
}

#Preview {
    TranscriptDisclosure(transcript: "RFI for the architect. Beam depth at gridline C doesn't match the structural drawings. Need an answer by Friday.")
        .padding(.horizontal, 20)
}
