import SwiftUI

/// The handoff's uppercase kicker: a section header, card kicker, hero kicker or field label.
struct Kicker: View {
    enum Style {
        /// Section header: 11/700 .14em, accent.
        case section
        /// Card kicker: 10/700 .12em, accent.
        case card
        /// Hero kicker on charcoal: 13/700 .16em, brand orange.
        case hero
        /// Field label: 11/700 .1em, muted.
        case label
        /// Quiet section header: 11/700 .14em, tertiary.
        case tertiary

        var textStyle: Theme.TextStyle {
            switch self {
            case .section, .tertiary: Theme.Text.kicker
            case .card: Theme.Text.cardKicker
            case .hero: Theme.Text.heroKicker
            case .label: Theme.Text.label
            }
        }

        var color: Color {
            switch self {
            case .section, .card: Theme.Color.accent
            case .hero: Theme.Color.brand
            case .label: Theme.Color.muted
            case .tertiary: Theme.Color.tertiary
            }
        }
    }

    let text: String
    let style: Style
    let color: Color?

    /// `color` overrides the style's colour, e.g. a flagged label in accent.
    init(_ text: String, style: Style = .section, color: Color? = nil) {
        self.text = text
        self.style = style
        self.color = color
    }

    var body: some View {
        Text(text.uppercased())
            .textStyle(style.textStyle)
            .foregroundStyle(color ?? style.color)
    }
}

#Preview {
    VStack(alignment: .leading, spacing: 12) {
        Kicker("Overview")
        Kicker("Selected", style: .card)
        Kicker("Procore, by voice", style: .hero)
        Kicker("Subject", style: .label)
        Kicker("Coming", style: .tertiary)
    }
    .padding()
}
