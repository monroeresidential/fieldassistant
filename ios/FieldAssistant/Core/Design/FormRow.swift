import SwiftUI

/// A labelled field row: 11/700 label over a 15 pt value, 14 pt vertical padding, a top hairline,
/// and an optional chevron (rows with one are at least 60 pt tall).
struct FormRow<Value: View>: View {
    let label: String
    let chevron: Bool
    let flagged: Bool
    let value: Value

    init(label: String, chevron: Bool = false, flagged: Bool = false, @ViewBuilder value: () -> Value) {
        self.label = label
        self.chevron = chevron
        self.flagged = flagged
        self.value = value()
    }

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            HStack(spacing: 8) {
                VStack(alignment: .leading, spacing: 4) {
                    Kicker(label, style: .label, color: flagged ? Theme.Color.accent : nil)
                    value
                        .textStyle(Theme.Text.body)
                        .foregroundStyle(Theme.Color.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if chevron {
                    Image(systemName: "chevron.right")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(Theme.Color.tertiary)
                        .accessibilityHidden(true)
                }
            }
            .padding(.vertical, 14)
            .frame(minHeight: chevron ? 60 : 44)
        }
        .contentShape(Rectangle())
    }
}

#Preview {
    VStack(spacing: 0) {
        FormRow(label: "Subject") { Text("Clarify beam depth at gridline C") }
        FormRow(label: "Assignee", chevron: true) { Text("Ben West") }
        FormRow(label: "Location", chevron: true, flagged: true) { Text("Choose a location") }
    }
    .padding(.horizontal, 20)
}
