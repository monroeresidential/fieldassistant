import SwiftUI

/// A 48 pt search field on the surface fill, radius 8, with a leading magnifying glass.
struct SearchField: View {
    let placeholder: String
    @Binding var text: String

    init(_ placeholder: String, text: Binding<String>) {
        self.placeholder = placeholder
        self._text = text
    }

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 18, weight: .medium))
                .foregroundStyle(Theme.Color.tertiary)
                .accessibilityHidden(true)
            TextField(placeholder, text: $text)
                .font(.publicSans(15, weight: .regular, relativeTo: .body))
                .foregroundStyle(Theme.Color.textPrimary)
                .autocorrectionDisabled()
                .submitLabel(.search)
        }
        .padding(.horizontal, 14)
        .frame(minHeight: 48)
        .background(Theme.Color.surface, in: RoundedRectangle(cornerRadius: 8))
    }
}

#Preview {
    SearchField("Search projects", text: .constant("")).padding()
}
