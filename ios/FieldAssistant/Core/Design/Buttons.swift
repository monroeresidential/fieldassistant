import SwiftUI

/// The filled call to action: accent on white, brand orange on dark grounds; white 17/700, radius 6.
struct PrimaryButtonStyle: ButtonStyle {
    var onDark: Bool
    var height: CGFloat

    init(onDark: Bool = false, height: CGFloat = 56) {
        self.onDark = onDark
        self.height = height
    }

    func makeBody(configuration: Configuration) -> some View {
        PrimaryButton(configuration: configuration, onDark: onDark, height: height)
    }

    private struct PrimaryButton: View {
        let configuration: ButtonStyleConfiguration
        let onDark: Bool
        let height: CGFloat
        @Environment(\.isEnabled) private var isEnabled

        var body: some View {
            configuration.label
                .textStyle(Theme.Text.buttonLarge)
                .foregroundStyle(isEnabled ? Color.white : Theme.Color.tertiary)
                .frame(maxWidth: .infinity, minHeight: height)
                .background(fill, in: RoundedRectangle(cornerRadius: 6))
                .contentShape(RoundedRectangle(cornerRadius: 6))
                .opacity(configuration.isPressed ? 0.85 : 1)
        }

        private var fill: Color {
            guard isEnabled else { return Theme.Color.surface }
            return onDark ? Theme.Color.brand : Theme.Color.accent
        }
    }
}

/// The outline button: 1.5 pt charcoal border (white on dark grounds), 15/700, radius 6.
struct SecondaryButtonStyle: ButtonStyle {
    var onDark: Bool
    var height: CGFloat

    init(onDark: Bool = false, height: CGFloat = 54) {
        self.onDark = onDark
        self.height = height
    }

    func makeBody(configuration: Configuration) -> some View {
        SecondaryButton(configuration: configuration, onDark: onDark, height: height)
    }

    private struct SecondaryButton: View {
        let configuration: ButtonStyleConfiguration
        let onDark: Bool
        let height: CGFloat
        @Environment(\.isEnabled) private var isEnabled

        var body: some View {
            configuration.label
                .textStyle(Theme.Text.buttonMedium)
                .foregroundStyle(isEnabled ? ink : Theme.Color.tertiary)
                .frame(maxWidth: .infinity, minHeight: height)
                .background(isEnabled ? Color.clear : Theme.Color.surface, in: RoundedRectangle(cornerRadius: 6))
                .overlay {
                    if isEnabled {
                        RoundedRectangle(cornerRadius: 6).strokeBorder(ink, lineWidth: 1.5)
                    }
                }
                .contentShape(RoundedRectangle(cornerRadius: 6))
                .opacity(configuration.isPressed ? 0.85 : 1)
        }

        /// Text and border: charcoal in light mode and white in dark, or white on a dark ground.
        private var ink: Color { onDark ? Color.white : Theme.Color.textPrimary }
    }
}

/// A text link such as Cancel, Change or Done, with a 44 pt minimum hit height.
struct TextLinkButtonStyle: ButtonStyle {
    var color: Color
    var size: CGFloat

    init(color: Color = Theme.Color.accent, size: CGFloat = 14) {
        self.color = color
        self.size = size
    }

    func makeBody(configuration: Configuration) -> some View {
        TextLink(configuration: configuration, color: color, size: size)
    }

    private struct TextLink: View {
        let configuration: ButtonStyleConfiguration
        let color: Color
        let size: CGFloat
        @Environment(\.isEnabled) private var isEnabled

        var body: some View {
            configuration.label
                .font(.publicSans(size, weight: .semibold, relativeTo: .subheadline))
                .foregroundStyle(isEnabled ? color : Theme.Color.tertiary)
                .frame(minHeight: 44)
                .contentShape(Rectangle())
                .opacity(configuration.isPressed ? 0.85 : 1)
        }
    }
}

#Preview {
    VStack(spacing: 12) {
        Button("Submit to Procore") {}.buttonStyle(PrimaryButtonStyle())
        Button("Build RFI") {}.buttonStyle(PrimaryButtonStyle(height: 52)).disabled(true)
        Button("New RFI") {}.buttonStyle(SecondaryButtonStyle())
        Button("Done") {}.buttonStyle(TextLinkButtonStyle())
    }
    .padding()
}
