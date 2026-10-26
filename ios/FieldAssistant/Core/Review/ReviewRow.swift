import SwiftUI

/// The field label shared by `ReviewRow` and `PairedRow`: the label kicker, accent when flagged.
struct ReviewLabel: View {
    let text: String
    var flagged = false

    var body: some View {
        Kicker(text, style: .label, color: flagged ? Theme.Color.accent : nil)
    }
}

/// A labelled field row: caption label, content, optional chevron, hairline divider.
/// With an `action` the whole row is one button, so it keeps a 44 pt hit target.
struct ReviewRow<Content: View>: View {
    let label: String
    var flagged = false
    var chevron = false
    var action: (() -> Void)?
    @ViewBuilder var content: () -> Content

    init(
        label: String,
        flagged: Bool = false,
        chevron: Bool = false,
        action: (() -> Void)? = nil,
        @ViewBuilder content: @escaping () -> Content
    ) {
        self.label = label
        self.flagged = flagged
        self.chevron = chevron
        self.action = action
        self.content = content
    }

    var body: some View {
        VStack(spacing: 0) {
            if let action {
                Button(action: action) { rowBody }
                    .buttonStyle(.plain)
            } else {
                rowBody
                    .accessibilityElement(children: .combine)
            }
            Divider()
        }
    }

    private var rowBody: some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 4) {
                ReviewLabel(text: label, flagged: flagged)
                content()
                    .font(.body)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            if chevron {
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.tertiary)
                    .accessibilityHidden(true)
            }
        }
        .padding(.vertical, 8)
        .padding(.horizontal, 16)
        .frame(minHeight: 44)
        .contentShape(Rectangle())
    }
}

#Preview {
    ScrollView {
        VStack(spacing: 0) {
            ReviewRow(label: "Subject") { Text("Clarify beam depth at gridline C") }
            ReviewRow(label: "Assignee", chevron: true, action: {}) { Text("Ben West") }
            ReviewRow(label: "Location", flagged: true, chevron: true, action: {}) {
                Text("Choose a location").foregroundStyle(.secondary)
            }
        }
    }
}
