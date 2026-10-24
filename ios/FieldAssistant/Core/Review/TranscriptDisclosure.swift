import SwiftUI

/// The original dictation, collapsed until the user asks for it.
struct TranscriptDisclosure: View {
    let transcript: String
    @State private var expanded = false

    var body: some View {
        VStack(spacing: 0) {
            DisclosureGroup(isExpanded: $expanded) {
                Text(transcript)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 8)
                    .textSelection(.enabled)
            } label: {
                Text("Field notes · original transcript")
                    .font(.subheadline)
                    .frame(minHeight: 44, alignment: .leading)
                    .accessibilityLabel("Field notes, original transcript")
            }
            .padding(.horizontal, 16)
            Divider()
        }
    }
}

#Preview {
    TranscriptDisclosure(transcript: "RFI for the architect. Beam depth at gridline C doesn't match the structural drawings. Need an answer by Friday.")
}
