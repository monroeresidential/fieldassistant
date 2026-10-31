import SwiftUI

/// Hosts `Route.result`: loads the submitted draft from the phone and names its assignees from
/// the cached people list.
struct ResultScreen: View {
    let draftID: UUID
    /// The project's name as Home shows it; `nil` when Home could not load it (offline).
    var projectName: String?
    /// Why New RFI is disabled (RFIs are not available on the project), or `nil` to enable it.
    var newRFIBlockedHint: String?
    let onNewRFI: () -> Void
    let onDone: () -> Void

    @Environment(AppState.self) private var appState

    var body: some View {
        if let draft = appState.ownedDraft(id: draftID) {
            RFIResultView(draft: draft, projectName: projectName, assigneeName: assigneeName(draft),
                          newRFIBlockedHint: newRFIBlockedHint, onNewRFI: onNewRFI, onDone: onDone)
        } else {
            FailureMessage(title: "Draft not found", message: "This draft is no longer on the phone.")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Theme.Color.ground.ignoresSafeArea())
        }
    }

    /// The assignees the cached people list can name, joined; `nil` when none can be named.
    private func assigneeName(_ draft: LocalDraft) -> String? {
        guard let ids = draft.server?.payload.assignee_ids, !ids.isEmpty else { return nil }
        let people = appState.lookups.rows(projectID: draft.projectID, kind: .users)
        let names = ids.compactMap { id in people.first { $0.id == id }?.label }
        return names.isEmpty ? nil : names.joined(separator: ", ")
    }
}

/// What Procore created (handoff 1g): the RFI number, its subject, a summary of what was sent and
/// a link to open it there. Charcoal ground in both modes.
struct RFIResultView: View {
    let draft: LocalDraft
    var projectName: String?
    var assigneeName: String?
    /// Why New RFI is disabled, shown under it and read as its hint; `nil` enables it.
    var newRFIBlockedHint: String?
    var onNewRFI: () -> Void = {}
    var onDone: () -> Void = {}

    var body: some View {
        GeometryReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    Spacer(minLength: 0)
                    content
                    Spacer(minLength: 40)
                    buttons
                }
                // The handoff's 70 / 56 include the status bar and home indicator; inside the safe
                // area that leaves 16 at the top and 22 at the bottom.
                .padding(.top, 16)
                .padding(.horizontal, 28)
                .padding(.bottom, 22)
                .frame(maxWidth: .infinity, minHeight: proxy.size.height, alignment: .leading)
            }
            .scrollBounceBehavior(.basedOnSize)
        }
        .background(Theme.Color.charcoal.ignoresSafeArea())
        .environment(\.colorScheme, .dark)
        .toolbar(.hidden, for: .navigationBar)
        .navigationBarBackButtonHidden(true)
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 16) {
            Image(systemName: "checkmark")
                .font(.system(size: 28, weight: .semibold))
                .foregroundStyle(Color.white)
                .frame(width: 56, height: 56)
                .background(Theme.Color.brand, in: Circle())
                .accessibilityHidden(true)
            Kicker("Created in Procore", style: .hero)
            title
            if let subject = draft.subject {
                Text(subject)
                    .textStyle(Theme.Text.resultSubject)
                    .foregroundStyle(Theme.Color.textOnDarkBody)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Rectangle()
                .fill(Theme.Color.brand)
                .frame(width: 48, height: 3)
                .accessibilityHidden(true)
            definitionList
        }
    }

    /// "RFI 214", or "Draft RFI" when Procore gave a draft no number yet.
    @ViewBuilder
    private var title: some View {
        if let number = draft.resultNumber {
            Text("RFI \(number)")
                .textStyle(Theme.Text.resultNumber)
                .foregroundStyle(Color.white)
                .lineLimit(1)
                .minimumScaleFactor(0.5)
                .accessibilityAddTraits(.isHeader)
        } else {
            Text("Draft RFI")
                .textStyle(Theme.Text.sectionTitle)
                .foregroundStyle(Color.white)
                .accessibilityAddTraits(.isHeader)
        }
    }

    private var definitionList: some View {
        let summary = ResultSummary(draft: draft, projectName: projectName, assigneeName: assigneeName, now: .now)
        return Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 24, verticalSpacing: 8) {
            ForEach(summary.rows, id: \.term) { row in
                GridRow {
                    Text(row.term)
                        .textStyle(Theme.Text.definition)
                        .foregroundStyle(Theme.Color.textOnDarkSecondary)
                    Text(row.value)
                        .textStyle(row.tabular ? Theme.Text.definitionTabular : Theme.Text.definition)
                        .foregroundStyle(Color.white)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private var buttons: some View {
        VStack(spacing: 10) {
            if let raw = draft.resultURL, let url = URL(string: raw) {
                Link(destination: url) {
                    HStack(spacing: 10) {
                        Text("Open in Procore")
                        Image(systemName: "arrow.up.right.square")
                            .font(.system(size: 18, weight: .semibold))
                            .accessibilityHidden(true)
                    }
                }
                .buttonStyle(PrimaryButtonStyle(onDark: true))
                .accessibilityLabel("Open in Procore")
            }
            Button("New RFI", action: onNewRFI)
                .buttonStyle(SecondaryButtonStyle(onDark: true))
                .disabled(newRFIBlockedHint != nil)
                .accessibilityLabel("Record a new RFI")
                .accessibilityHint(newRFIBlockedHint ?? "")
            if let newRFIBlockedHint {
                Text(newRFIBlockedHint)
                    .textStyle(Theme.Text.meta13)
                    .foregroundStyle(Theme.Color.textOnDarkSecondary)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity)
                    .accessibilityHidden(true)
            }
            Button(action: onDone) {
                Text("Done").frame(maxWidth: .infinity)
            }
            .buttonStyle(TextLinkButtonStyle(color: Theme.Color.textOnDarkSecondary))
            .accessibilityLabel("Done, back to Home")
        }
    }
}

#Preview("Created") {
    let container = try! AppContainer.makeModelContainer(inMemory: true).get()
    let draft = LocalDraft(companyID: 1, projectID: 7, status: .submitted, transcript: "Twisted column",
                           capturedAt: .now.addingTimeInterval(-48))
    container.mainContext.insert(draft)
    draft.subject = "Core through beam at grid C-4 for duct routing, Level 3"
    draft.resultNumber = "214"
    draft.resultURL = "https://sandbox.procore.com"
    draft.photos = (0..<3).map { LocalPhoto(fileName: "photo-\($0).jpg") }
    return NavigationStack {
        RFIResultView(draft: draft, projectName: "118 W Illinois", assigneeName: "Mike Torres")
    }
    .modelContainer(container)
}

#Preview("Draft RFI") {
    let draft = LocalDraft(companyID: 1, projectID: 7, status: .submitted, transcript: "Twisted column")
    draft.subject = "Twisted steel column at the air handler platform"
    draft.resultURL = "https://sandbox.procore.com"
    return NavigationStack { RFIResultView(draft: draft) }
}
