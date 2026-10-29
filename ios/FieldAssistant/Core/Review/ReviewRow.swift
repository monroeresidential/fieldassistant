import SwiftUI

/// A Review field as a `FormRow`: tertiary label over the value, top hairline, optional chevron.
/// With an `action` the whole row is one button; rows with a chevron are at least 60 pt tall.
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
        if let action {
            Button(action: action) { row }
                .buttonStyle(.plain)
        } else {
            row
                .accessibilityElement(children: .combine)
        }
    }

    private var row: some View {
        FormRow(label: label, chevron: chevron, flagged: flagged, value: content)
    }
}

#Preview {
    ScrollView {
        VStack(spacing: 0) {
            ReviewRow(label: "Subject") { Text("Clarify beam depth at gridline C") }
            ReviewRow(label: "Assignee", chevron: true, action: {}) { Text("Ben West") }
            ReviewRow(label: "Location", flagged: true, chevron: true, action: {}) {
                Text("Choose a location").foregroundStyle(Theme.Color.tertiary)
            }
        }
        .padding(.horizontal, 20)
    }
}
