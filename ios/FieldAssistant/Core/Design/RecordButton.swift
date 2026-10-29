import SwiftUI

/// The orange record or stop circle with its halo ring. The caller supplies the VoiceOver label
/// with `.accessibilityLabel`. Disabled: surface fill, tertiary glyph, no halo.
struct RecordButton: View {
    enum Glyph { case mic, stop }

    let glyph: Glyph
    let size: CGFloat
    let halo: CGFloat
    let isEnabled: Bool
    let action: () -> Void
    /// A parent's `.disabled(true)` changes the look as well as stopping taps.
    @Environment(\.isEnabled) private var environmentEnabled

    private var enabled: Bool { isEnabled && environmentEnabled }

    init(glyph: Glyph, size: CGFloat, halo: CGFloat, isEnabled: Bool, action: @escaping () -> Void) {
        self.glyph = glyph
        self.size = size
        self.halo = halo
        self.isEnabled = isEnabled
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            ZStack {
                Circle()
                    .fill(enabled ? Theme.Color.halo : Color.clear)
                    .frame(width: size + 2 * halo, height: size + 2 * halo)
                Circle()
                    .fill(enabled ? Theme.Color.brand : Theme.Color.surface)
                    .frame(width: size, height: size)
                glyphView
            }
            .frame(minWidth: 44, minHeight: 44)
            .contentShape(Circle())
        }
        .buttonStyle(Pressable())
        .disabled(!isEnabled)
    }

    @ViewBuilder
    private var glyphView: some View {
        let ink = enabled ? Color.white : Theme.Color.tertiary
        switch glyph {
        case .mic:
            // 46 pt at a 116 pt button.
            Image(systemName: "mic")
                .font(.system(size: size * 46 / 116, weight: .medium))
                .foregroundStyle(ink)
        case .stop:
            // A 26 pt rounded square at a 76 pt button.
            let side = size * 26 / 76
            RoundedRectangle(cornerRadius: side * 0.15)
                .fill(ink)
                .frame(width: side, height: side)
        }
    }

    private struct Pressable: ButtonStyle {
        func makeBody(configuration: Configuration) -> some View {
            configuration.label.opacity(configuration.isPressed ? 0.85 : 1)
        }
    }
}

#Preview {
    VStack(spacing: 24) {
        RecordButton(glyph: .mic, size: 116, halo: 10, isEnabled: true) {}
            .accessibilityLabel("Record an RFI")
        RecordButton(glyph: .mic, size: 116, halo: 10, isEnabled: false) {}
        RecordButton(glyph: .stop, size: 76, halo: 8, isEnabled: true) {}
        RecordButton(glyph: .stop, size: 76, halo: 8, isEnabled: false) {}
    }
}
