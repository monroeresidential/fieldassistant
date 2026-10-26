import SwiftUI

/// The Field Assistant mark from the asset catalog, square at `size`. Decorative: the screen
/// beside it names the app, so VoiceOver skips it.
struct LogoMark: View {
    enum Variant {
        case onLight, onDark, monoWhite, monoCharcoal

        var assetName: String {
            switch self {
            case .onLight: "MarkOnLight"
            case .onDark: "MarkOnDark"
            case .monoWhite: "MarkMonoWhite"
            case .monoCharcoal: "MarkMonoCharcoal"
            }
        }
    }

    let variant: Variant
    let size: CGFloat

    init(_ variant: Variant, size: CGFloat) {
        self.variant = variant
        self.size = size
    }

    var body: some View {
        Image(variant.assetName)
            .resizable()
            .scaledToFit()
            .frame(width: size, height: size)
            .accessibilityHidden(true)
    }
}

#Preview {
    HStack(spacing: 16) {
        LogoMark(.onLight, size: 44)
        LogoMark(.monoCharcoal, size: 28)
        LogoMark(.onDark, size: 44).padding(4).background(Theme.Color.charcoal)
        LogoMark(.monoWhite, size: 28).padding(4).background(Theme.Color.charcoal)
    }
    .padding()
}
