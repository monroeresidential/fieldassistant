import SwiftUI

/// Submitting preference, project lookups, account, and the build this phone runs (handoff 1h).
struct SettingsView: View {
    /// The project and company names as Home shows them; `nil` when Home could not load them.
    var projectName: String?
    var companyName: String?

    @Environment(AppState.self) private var appState
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                Text("Settings")
                    .textStyle(Theme.Text.screenTitle)
                    .foregroundStyle(Theme.Color.textPrimary)
                    .accessibilityAddTraits(.isHeader)
                submittingSection
                projectSection
                accountSection
                footer
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .padding(.bottom, 22)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(Theme.Color.ground.ignoresSafeArea())
        .designNavigationTitle("Settings", .hidden)
    }

    // MARK: - Sections

    private var submittingSection: some View {
        let settings = appState.settings
        return SettingsSection("Submitting") {
            SettingsRow {
                Toggle(isOn: Binding(
                    get: { settings.submitAsDraft ?? true },
                    set: { settings.submitAsDraft = $0 }
                )) {
                    RowText(title: "Submit as Procore draft",
                            subtitle: "RFIs land as drafts until you open them in Procore.")
                }
                .tint(Theme.Color.brand)
                .accessibilityLabel("Submit as Procore draft")
                .accessibilityHint("RFIs land as drafts until you open them in Procore.")
            }
        }
    }

    private var projectSection: some View {
        SettingsSection("Project") {
            SettingsRow {
                TimelineView(.periodic(from: .now, by: 30)) { context in
                    RowText(title: projectName ?? "This project",
                            subtitle: Self.projectLine(companyName: companyName, lookupsRefreshedAt: lookupsRefreshedAt,
                                                       now: context.date))
                }
                Button("Change") {
                    appState.selectedProjectID = nil
                }
                .buttonStyle(TextLinkButtonStyle(size: 14))
                .accessibilityLabel("Change project")
            }
            refreshButton
        }
    }

    private var refreshButton: some View {
        let isPrefetching = appState.lookups.isPrefetching
        return Button {
            appState.refreshLookups(force: true)
        } label: {
            SettingsRow {
                RowText(title: "Refresh people and locations", subtitle: "Pull the latest from Procore now")
                if isPrefetching {
                    ProgressView().tint(Theme.Color.tertiary)
                } else {
                    Image(systemName: "arrow.clockwise")
                        .font(.system(size: 18, weight: .medium))
                        .foregroundStyle(Theme.Color.accent)
                        .accessibilityHidden(true)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(isPrefetching || appState.selectedProjectID == nil)
        .accessibilityLabel(isPrefetching ? "Refreshing people and locations" : "Refresh people and locations")
        .accessibilityHint("Pull the latest from Procore now")
    }

    private var accountSection: some View {
        SettingsSection("Account") {
            if case .signedIn(let user) = appState.phase {
                SettingsRow {
                    RowText(title: user.name, subtitle: "\(user.email) · Procore")
                }
            }
            VStack(spacing: 0) {
                HairlineDivider()
                Button {
                    Task { await appState.signOut() }
                } label: {
                    Text("Sign out")
                        .frame(maxWidth: .infinity, minHeight: 52, alignment: .leading)
                }
                .buttonStyle(TextLinkButtonStyle(size: 15))
                .accessibilityLabel("Sign out")
            }
        }
    }

    private var footer: some View {
        HStack(spacing: 8) {
            LogoMark(colorScheme == .dark ? .onDark : .onLight, size: 28)
            Text(Self.buildLine)
                .textStyle(Theme.Text.buildLine)
                .foregroundStyle(Theme.Color.tertiary)
        }
    }

    // MARK: - Values

    /// When this project's lookup lists were last fetched: the newest of its cached lists.
    private var lookupsRefreshedAt: Date? {
        guard let projectID = appState.selectedProjectID else { return nil }
        return LookupKind.allCases.compactMap { appState.lookups.set(projectID: projectID, kind: $0)?.fetchedAt }.max()
    }

    /// "Monroe Residential · lookups refreshed 9 min ago"; either part is left out when unknown.
    /// `locale` and `calendar` are for tests; the screen uses the phone's.
    static func projectLine(companyName: String?, lookupsRefreshedAt: Date?, now: Date,
                            locale: Locale = .current, calendar: Calendar = .current) -> String? {
        var parts: [String] = []
        if let companyName, !companyName.isEmpty { parts.append(companyName) }
        if let date = lookupsRefreshedAt {
            if now.timeIntervalSince(date) < 60 {
                parts.append("lookups refreshed just now")
            } else {
                let formatter = RelativeDateTimeFormatter()
                formatter.locale = locale
                formatter.calendar = calendar
                formatter.unitsStyle = .short
                parts.append("lookups refreshed \(formatter.localizedString(for: date, relativeTo: now))")
            }
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// "Field Assistant 1.0 (12) · local · localhost:8080"
    private static var buildLine: String {
        let info = Bundle.main.infoDictionary
        let version = info?["CFBundleShortVersionString"] as? String ?? "?"
        let build = info?["CFBundleVersion"] as? String ?? "?"
        let host = AppConfig.apiBaseURL.host(percentEncoded: false).map { host in
            AppConfig.apiBaseURL.port.map { "\(host):\($0)" } ?? host
        } ?? ""
        return "Field Assistant \(version) (\(build)) · \(AppConfig.environment) · \(host)"
    }
}

/// A muted section kicker over its rows, closed by a hairline.
private struct SettingsSection<Content: View>: View {
    let title: String
    let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Kicker(title, style: .section, color: Theme.Color.muted)
                .padding(.bottom, 8)
                .accessibilityAddTraits(.isHeader)
            content
            HairlineDivider()
        }
    }
}

/// A 64 pt row under a hairline: its text on the left, a control or value on the right.
private struct SettingsRow<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        VStack(spacing: 0) {
            HairlineDivider()
            HStack(spacing: 12) { content }
                .padding(.vertical, 14)
                .frame(maxWidth: .infinity, minHeight: 64, alignment: .leading)
        }
    }
}

/// A row's title 15/500 over its muted 12 pt line.
private struct RowText: View {
    let title: String
    let subtitle: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .textStyle(Theme.Text.rowTitle)
                .foregroundStyle(Theme.Color.textPrimary)
            if let subtitle {
                Text(subtitle)
                    .textStyle(Theme.Text.rowSubtitle)
                    .foregroundStyle(Theme.Color.muted)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .fixedSize(horizontal: false, vertical: true)
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    NavigationStack { SettingsView(projectName: "118 W Illinois", companyName: "Monroe Residential") }
        .environment(AppState.preview(container: container))
        .modelContainer(container)
}
