import SwiftUI

extension View {
    /// An inline navigation bar title in Public Sans. The system title stays set, for the back
    /// menu and VoiceOver's screen change; a principal item draws it in the type scale.
    func designNavigationTitle(_ title: String) -> some View {
        navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(title)
                        .textStyle(Theme.Text.navigationTitle)
                        .foregroundStyle(Theme.Color.textPrimary)
                        .lineLimit(1)
                        .accessibilityAddTraits(.isHeader)
                }
            }
    }
}
