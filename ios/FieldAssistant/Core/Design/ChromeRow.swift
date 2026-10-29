import SwiftUI

extension View {
    /// A list row on the page ground with hairline separators running to the trailing edge
    /// (Drafts and the RFI list).
    func chromeRow() -> some View {
        listRowBackground(Theme.Color.ground)
            .listRowSeparatorTint(Theme.Color.hairline)
            .alignmentGuide(.listRowSeparatorTrailing) { $0.width }
    }
}
