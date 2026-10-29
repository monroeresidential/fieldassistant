import SwiftUI

/// Whether `designNavigationTitle` draws the title in the bar.
enum DesignNavigationTitleVisibility {
    /// Drawn in the bar in Public Sans.
    case visible
    /// Set for VoiceOver's screen change, the next screen's back button and the back menu, but not
    /// drawn: for screens that show their own 32 pt title in the content.
    case hidden
}

extension View {
    /// An inline navigation bar title in Public Sans. The system title stays set, for the back
    /// menu and VoiceOver's screen change; a principal item draws it in the type scale, or, when
    /// `hidden`, takes the title's place with nothing so the in-content title is not repeated.
    func designNavigationTitle(_ title: String, _ visibility: DesignNavigationTitleVisibility = .visible) -> some View {
        navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    switch visibility {
                    case .visible:
                        Text(title)
                            .textStyle(Theme.Text.navigationTitle)
                            .foregroundStyle(Theme.Color.textPrimary)
                            .lineLimit(1)
                            .accessibilityAddTraits(.isHeader)
                    case .hidden:
                        Color.clear
                            .frame(width: 1, height: 1)
                            .accessibilityHidden(true)
                    }
                }
            }
    }
}
