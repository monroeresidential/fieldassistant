import SwiftUI
import UIKit

/// Design tokens from the iOS design handoff (docs/design/2026-10-23-ios-handoff/README.md).
enum Theme {
    /// Colour tokens. Adaptive tokens carry a light and a dark value; the rest are fixed.
    enum Color {
        // Fixed
        static let brand = fixed(0xF26122)
        static let charcoal = fixed(0x2F3437)
        static let panel = fixed(0x3A4044)
        static let tertiary = fixed(0x8A9196)
        static let textOnDarkBody = fixed(0xE9EBEC)
        static let textOnDarkSecondary = fixed(0xB6BCC0)
        static let tagline = fixed(0xD5D9DC)
        static let groundDark = fixed(0x2F3437)
        /// Review's count tag fill in dark mode: the light accent, kept deep on charcoal (handoff 1m).
        static let tagFillOnDark = fixed(0xC2410C)

        // Adaptive: light / dark
        static let accent = adaptive(light: 0xC2410C, dark: 0xF26122)
        static let accentTint = adaptive(light: 0xFEF0E8, dark: 0x3A4044)
        static let muted = adaptive(light: 0x5C6368, dark: 0xB6BCC0)
        static let textPrimary = adaptive(light: 0x2F3437, dark: 0xFFFFFF)
        static let textSecondary = adaptive(light: 0x5C6368, dark: 0xB6BCC0)
        static let hairline = adaptive(light: 0xE1E4E6, dark: 0xFFFFFF, darkAlpha: 0.12)
        static let hairlineStrong = adaptive(light: 0xE1E4E6, dark: 0xFFFFFF, darkAlpha: 0.18)
        static let surface = adaptive(light: 0xF4F5F6, dark: 0x3A4044)
        static let ground = adaptive(light: 0xFFFFFF, dark: 0x2F3437)
        static let halo = adaptive(light: 0xFEF0E8, dark: 0xF26122, darkAlpha: 0.18)
        static let flaggedBackground = adaptive(light: 0xFEF0E8, dark: 0x3A4044)
        /// The dashed border of an add-photo tile.
        static let dashedBorder = adaptive(light: 0xB6BCC0, dark: 0xFFFFFF, darkAlpha: 0.30)
        /// The ring of an unselected candidate radio on Review.
        static let radioBorder = adaptive(light: 0xB6BCC0, dark: 0x8A9196)

        private static func fixed(_ hex: UInt32) -> SwiftUI.Color {
            SwiftUI.Color(uiColor: uiColor(hex))
        }

        private static func adaptive(light: UInt32, dark: UInt32, darkAlpha: CGFloat = 1) -> SwiftUI.Color {
            let lightColor = uiColor(light)
            let darkColor = uiColor(dark, alpha: darkAlpha)
            return SwiftUI.Color(uiColor: UIColor { traits in
                traits.userInterfaceStyle == .dark ? darkColor : lightColor
            })
        }

        private static func uiColor(_ hex: UInt32, alpha: CGFloat = 1) -> UIColor {
            UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255,
                    green: CGFloat((hex >> 8) & 0xFF) / 255,
                    blue: CGFloat(hex & 0xFF) / 255,
                    alpha: alpha)
        }
    }

    /// A type-scale entry: Public Sans font, letter-spacing in points and extra line spacing in points.
    struct TextStyle: Sendable {
        let size: CGFloat
        let font: Font
        let tracking: CGFloat
        let lineSpacing: CGFloat

        /// Public Sans's natural line height as a multiple of its size: hhea ascent 1900 + descent 450
        /// over 2000 units per em. SwiftUI's `lineSpacing` adds to this height.
        static let naturalLineHeight: CGFloat = 1.175

        /// `em` is the handoff's letter-spacing; `lineHeight` its line-height multiple (nil keeps
        /// the font's natural height). A multiple below the natural height gives negative spacing,
        /// which tightens the lines (the hero's .98).
        init(_ size: CGFloat, _ weight: Font.Weight, relativeTo textStyle: Font.TextStyle,
             em: CGFloat = 0, lineHeight: CGFloat? = nil, monospacedDigits: Bool = false) {
            self.size = size
            let font = Font.publicSans(size, weight: weight, relativeTo: textStyle)
            self.font = monospacedDigits ? font.monospacedDigit() : font
            self.tracking = size * em
            self.lineSpacing = lineHeight.map { size * ($0 - Self.naturalLineHeight) } ?? 0
        }
    }

    /// The handoff's type scale. Uppercase kickers and labels upper-case their text at the call site.
    enum Text {
        static let screenTitle = TextStyle(32, .bold, relativeTo: .largeTitle, em: -0.02)
        /// Home's project name.
        static let projectTitle = TextStyle(28, .bold, relativeTo: .title, em: -0.02)
        /// Home's "Record an RFI" label.
        static let label17 = TextStyle(17, .bold, relativeTo: .headline)
        static let hero = TextStyle(56, .bold, relativeTo: .largeTitle, em: -0.03, lineHeight: 0.98)
        static let heroKicker = TextStyle(13, .bold, relativeTo: .footnote, em: 0.16)
        static let resultNumber = TextStyle(84, .bold, relativeTo: .largeTitle, em: -0.04, monospacedDigits: true)
        /// The created RFI's subject on Result.
        static let resultSubject = TextStyle(20, .semibold, relativeTo: .title3, lineHeight: 1.3)
        /// Result's definition list: terms and values.
        static let definition = TextStyle(14, .regular, relativeTo: .subheadline)
        /// Result's dates and times.
        static let definitionTabular = TextStyle(14, .regular, relativeTo: .subheadline, monospacedDigits: true)
        static let body = TextStyle(15, .regular, relativeTo: .body, lineHeight: 1.55)
        static let rowTitle = TextStyle(15, .medium, relativeTo: .body)
        /// Review's Subject value.
        static let fieldTitle = TextStyle(16, .semibold, relativeTo: .body, lineHeight: 1.4)
        static let rowTitleStrong = TextStyle(17, .semibold, relativeTo: .body)
        static let label = TextStyle(11, .bold, relativeTo: .caption2, em: 0.1)
        static let kicker = TextStyle(11, .bold, relativeTo: .caption2, em: 0.14)
        static let cardKicker = TextStyle(10, .bold, relativeTo: .caption2, em: 0.12)
        static let cardTitle = TextStyle(20, .bold, relativeTo: .title3)
        static let meta12 = TextStyle(12, .regular, relativeTo: .caption)
        /// A Settings row's line under its title.
        static let rowSubtitle = TextStyle(12, .regular, relativeTo: .caption, lineHeight: 1.4)
        /// Settings' build line: version, environment and host.
        static let buildLine = TextStyle(11, .regular, relativeTo: .caption2, monospacedDigits: true)
        static let meta13 = TextStyle(13, .regular, relativeTo: .footnote)
        static let meta13Medium = TextStyle(13, .medium, relativeTo: .footnote)
        static let transcript = TextStyle(24, .medium, relativeTo: .title2, em: -0.01, lineHeight: 1.35)
        static let sectionTitle = TextStyle(24, .bold, relativeTo: .title2, em: -0.02)
        static let buttonLarge = TextStyle(17, .bold, relativeTo: .headline, em: 0.01)
        static let buttonMedium = TextStyle(15, .bold, relativeTo: .subheadline)
        static let tagText = TextStyle(11, .bold, relativeTo: .caption2)
    }
}
