import SwiftUI

extension Font {
    /// Public Sans at `size` points, scaled with Dynamic Type like `relativeTo`.
    /// The four bundled weights are 400, 500, 600 and 700; any other weight falls back to the nearest.
    static func publicSans(_ size: CGFloat, weight: Font.Weight, relativeTo textStyle: Font.TextStyle) -> Font {
        Font.custom(publicSansName(for: weight), size: size, relativeTo: textStyle)
    }

    static func publicSansName(for weight: Font.Weight) -> String {
        switch weight {
        case .ultraLight, .thin, .light, .regular: "PublicSans-Regular"
        case .medium: "PublicSans-Medium"
        case .semibold: "PublicSans-SemiBold"
        default: "PublicSans-Bold"
        }
    }
}

extension View {
    /// Applies a type-scale entry: its font, letter-spacing and extra line spacing.
    func textStyle(_ style: Theme.TextStyle) -> some View {
        font(style.font)
            .tracking(style.tracking)
            .lineSpacing(style.lineSpacing)
    }
}
