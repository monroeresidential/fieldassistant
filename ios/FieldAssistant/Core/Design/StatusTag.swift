import SwiftUI

/// The one status tag for drafts, RFIs and pickers: 11/700 text, radius 4, filled or outlined.
struct StatusTag: View {
    enum State: Equatable {
        case submitted(number: String?), queued, waiting, draft, fix, lastUsed, open, closed, custom(String)
    }

    /// Everything that varies between tags, kept apart from the view so tests can check it.
    struct Style: Equatable {
        let text: String
        let foreground: Color
        let background: Color?
        let border: Color?
        let uppercase: Bool
        let tracking: CGFloat
    }

    let state: State

    init(_ state: State) {
        self.state = state
    }

    var body: some View {
        let style = Self.style(for: state)
        Text(style.uppercase ? style.text.uppercased() : style.text)
            .font(Theme.Text.tagText.font)
            .tracking(style.tracking)
            .foregroundStyle(style.foreground)
            .lineLimit(1)
            .padding(.horizontal, 8)
            .padding(.vertical, style.border == nil ? 4 : 3)
            .background(style.background ?? .clear, in: RoundedRectangle(cornerRadius: 4))
            .overlay {
                if let border = style.border {
                    RoundedRectangle(cornerRadius: 4).strokeBorder(border, lineWidth: 1)
                }
            }
            .accessibilityLabel(state == .lastUsed ? style.text : "Status \(style.text)")
    }

    private static let tracking = 11 * 0.04

    static func style(for state: State) -> Style {
        switch state {
        case .submitted(let number):
            filled(number.map { "#\($0) Submitted" } ?? "Submitted")
        case .closed:
            filled("Closed")
        case .fix:
            Style(text: LocalDraftStatus.failed.label, foreground: .white, background: Theme.Color.accent,
                  border: nil, uppercase: false, tracking: tracking)
        case .queued:
            outlined(LocalDraftStatus.submitting.label)
        case .waiting:
            outlined(LocalDraftStatus.waiting.label)
        case .open:
            outlined("Open")
        case .draft:
            Style(text: LocalDraftStatus.draft.label, foreground: Theme.Color.muted, background: nil,
                  border: Theme.Color.hairlineStrong, uppercase: false, tracking: tracking)
        case .lastUsed:
            Style(text: "Last used", foreground: Theme.Color.accent, background: Theme.Color.accentTint,
                  border: nil, uppercase: true, tracking: 11 * 0.06)
        case .custom(let text):
            Style(text: text, foreground: Theme.Color.muted, background: nil,
                  border: Theme.Color.hairlineStrong, uppercase: false, tracking: tracking)
        }
    }

    static func state(for draft: LocalDraftStatus, number: String?) -> State {
        switch draft {
        case .waiting: .waiting
        case .draft: .draft
        case .submitting: .queued
        case .submitted: .submitted(number: number)
        case .failed: .fix
        }
    }

    /// Procore's RFI status; the record's `draft` flag wins over it.
    static func state(forRFIStatus status: String, draft: Bool) -> State {
        if draft { return .draft }
        switch status.lowercased() {
        case "open": return .open
        case "closed": return .closed
        case "draft": return .draft
        case "": return .custom("Unknown")
        default: return .custom(status.replacingOccurrences(of: "_", with: " ").capitalized)
        }
    }

    /// Submitted and Closed: surface fill, primary text.
    private static func filled(_ text: String) -> Style {
        Style(text: text, foreground: Theme.Color.textPrimary, background: Theme.Color.surface,
              border: nil, uppercase: false, tracking: tracking)
    }

    /// Queued, Waiting and Open: accent text and a 1 pt accent border.
    private static func outlined(_ text: String) -> Style {
        Style(text: text, foreground: Theme.Color.accent, background: nil,
              border: Theme.Color.accent, uppercase: false, tracking: tracking)
    }
}

#Preview {
    VStack(alignment: .leading, spacing: 8) {
        StatusTag(.submitted(number: "214"))
        StatusTag(.queued)
        StatusTag(.waiting)
        StatusTag(.draft)
        StatusTag(.fix)
        StatusTag(.lastUsed)
        StatusTag(.open)
        StatusTag(.closed)
        StatusTag(.custom("Pending"))
    }
    .padding()
}
