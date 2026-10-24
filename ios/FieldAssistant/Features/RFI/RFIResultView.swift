import SwiftUI

/// Hosts `Route.result`: loads the submitted draft from the phone.
struct ResultScreen: View {
    let draftID: UUID
    let onNewRFI: () -> Void
    let onDone: () -> Void

    @Environment(AppState.self) private var appState

    var body: some View {
        if let draft = appState.drafts.draft(id: draftID) {
            RFIResultView(draft: draft, onNewRFI: onNewRFI, onDone: onDone)
        } else {
            ContentUnavailableView("Draft not found", systemImage: "doc.questionmark", description: Text("This draft is no longer on the phone."))
        }
    }
}

/// What Procore created: the RFI number, its subject and a link to open it there.
struct RFIResultView: View {
    let draft: LocalDraft
    var onNewRFI: () -> Void = {}
    var onDone: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Spacer()
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 56))
                .foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            Text("Created in Procore")
                .font(.caption.weight(.bold))
                .textCase(.uppercase)
                .foregroundStyle(Color.accentColor)
            Text(draft.resultNumber.map { "RFI \($0)" } ?? "RFI created")
                .font(.system(size: 56, weight: .bold).monospacedDigit())
                .minimumScaleFactor(0.5)
                .lineLimit(1)
            if let subject = draft.subject {
                Text(subject)
                    .font(.title3.weight(.semibold))
            }
            Spacer()
            VStack(spacing: 12) {
                if let raw = draft.resultURL, let url = URL(string: raw) {
                    Link(destination: url) {
                        Label("Open in Procore", systemImage: "arrow.up.right.square")
                            .font(.headline)
                            .frame(maxWidth: .infinity, minHeight: 44)
                    }
                    .buttonStyle(.borderedProminent)
                    .accessibilityLabel("Open in Procore")
                }
                Button(action: onNewRFI) {
                    Text("New RFI")
                        .font(.headline)
                        .frame(maxWidth: .infinity, minHeight: 44)
                }
                .buttonStyle(.bordered)
                .accessibilityLabel("Record a new RFI")
                Button(action: onDone) {
                    Text("Done")
                        .frame(maxWidth: .infinity, minHeight: 44)
                }
                .accessibilityLabel("Done, back to Home")
            }
        }
        .padding(24)
        .frame(maxWidth: .infinity, alignment: .leading)
        .navigationBarBackButtonHidden(true)
        .navigationBarTitleDisplayMode(.inline)
    }
}

#Preview {
    let draft = LocalDraft(companyID: 1, projectID: 7, status: .submitted, transcript: "Twisted column")
    draft.subject = "Twisted steel column at the air handler platform"
    draft.resultNumber = "214"
    draft.resultURL = "https://sandbox.procore.com"
    return NavigationStack { RFIResultView(draft: draft) }
}
