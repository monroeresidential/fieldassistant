import SwiftUI

/// The project's RFIs from the on-device mirror: search, a row of filter tags and pull to sync.
/// Works offline; it reads the store, and the store follows `RecordSync`. Rows open the RFI detail.
struct RFIRecordListView: View {
    let projectID: Int64
    let onOpen: (Route) -> Void

    @Environment(AppState.self) private var appState
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var query = ""
    @State private var choice: FilterChoice = .all
    /// Value snapshots, never `CachedRecord`: a sync may delete the model a row came from.
    @State private var rows: [RecordRow] = []

    private static let rowInsets = EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20)

    /// At accessibility text sizes the header scrolls with the rows, so the list keeps room.
    private var scrollsHeader: Bool { dynamicTypeSize.isAccessibilitySize }

    var body: some View {
        VStack(spacing: 0) {
            if !scrollsHeader { header }
            List {
                if scrollsHeader {
                    header
                        .listRowInsets(EdgeInsets())
                        .listRowSeparator(.hidden)
                        .listRowBackground(Theme.Color.ground)
                }
                ForEach(rows) { record in
                    Button {
                        onOpen(.rfi(record.procoreID))
                    } label: {
                        RFIRecordRow(record: record)
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint("Opens this RFI")
                    .listRowInsets(Self.rowInsets)
                    .chromeRow()
                }
                if rows.isEmpty {
                    emptyState
                        .listRowInsets(EdgeInsets(top: 12, leading: 0, bottom: 0, trailing: 0))
                        .listRowSeparator(.hidden)
                        .listRowBackground(Theme.Color.ground)
                }
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .scrollDismissesKeyboard(.interactively)
            .refreshable { await sync() }
        }
        .background(Theme.Color.ground.ignoresSafeArea())
        .designNavigationTitle("RFIs", .hidden)
        // Refetch when the search, the filter or the sync status changes.
        .task(id: FetchKey(projectID: projectID, query: query, choice: choice, status: appState.recordSync?.status)) {
            reload()
        }
    }

    // MARK: - Header

    /// Title, search field and filter tags on the 20 pt screen padding.
    private var header: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("RFIs")
                .textStyle(Theme.Text.screenTitle)
                .foregroundStyle(Theme.Color.textPrimary)
                .accessibilityAddTraits(.isHeader)
                .padding(.horizontal, 20)
                .padding(.bottom, 20)
            SearchField("Number or subject", text: $query)
                .padding(.horizontal, 20)
                .padding(.bottom, 8)
            filterRow
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .background(Theme.Color.ground)
    }

    /// One tag per filter, the active one filled; scrolls sideways when the tags do not fit.
    private var filterRow: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(FilterChoice.allCases) { option in
                    FilterTag(title: option.rawValue, isActive: option == choice) { choice = option }
                }
            }
            .padding(.horizontal, 20)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Filter RFIs")
    }

    // MARK: - Filter

    enum FilterChoice: String, CaseIterable, Identifiable {
        case all = "All"
        case open = "Open"
        case closed = "Closed"
        case draft = "Draft"
        case mine = "Ball in my court"

        var id: String { rawValue }
    }

    private var userName: String {
        if case .signedIn(let user) = appState.phase { return user.name }
        return ""
    }

    /// "Ball in my court" needs the signed-in name; an empty name would match every unassigned row.
    private var recordFilter: RecordFilter {
        switch choice {
        case .all: .all
        case .open: .status("open")
        case .closed: .status("closed")
        case .draft: .status("draft")
        case .mine: userName.isEmpty ? .all : .ballInCourt(userName: userName)
        }
    }

    // MARK: - Empty states

    @ViewBuilder
    private var emptyState: some View {
        let status = appState.recordSync?.status
        let neverSynced = appState.records.state(projectID: projectID, kind: .rfi)?.lastSyncedAt == nil
        let trimmed = query.trimmingCharacters(in: .whitespaces)
        if !trimmed.isEmpty {
            FailureMessage(title: "No results for \u{201C}\(trimmed)\u{201D}",
                           message: "Check the spelling or try a new search.")
        } else if choice != .all {
            FailureMessage(title: "No matching RFIs", message: "No RFIs match \u{201C}\(choice.rawValue)\u{201D}.")
        } else if case .failed(code: "procore_permission_denied", let reason) = status {
            FailureMessage(title: "No RFI access", message: Self.noAccessMessage(reason))
        } else if neverSynced, status == .syncing {
            ProgressView {
                Text("Syncing RFIs")
                    .textStyle(Theme.Text.meta13)
                    .foregroundStyle(Theme.Color.muted)
            }
            .tint(Theme.Color.tertiary)
            .frame(maxWidth: .infinity, minHeight: 88)
        } else if neverSynced {
            FailureMessage(title: "Not synced yet. Pull to sync.", message: nil) {
                Button("Sync now") { Task { await sync() } }
                    .buttonStyle(TextLinkButtonStyle())
                    .accessibilityLabel("Sync now")
                    .accessibilityHint("Fetches this project's RFIs from Procore")
            }
        } else {
            FailureMessage(title: "No RFIs yet", message: "This project has no RFIs in Procore.")
        }
    }

    /// Procore's reason the user cannot see the project's RFIs, or the generic sentence without one.
    static func noAccessMessage(_ reason: String?) -> String {
        let trimmed = reason?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? "Your Procore account cannot see RFIs on this project." : trimmed
    }

    private func sync() async {
        await appState.syncRecords(force: true, full: false)
    }

    private func reload() {
        rows = appState.records.records(projectID: projectID, kind: .rfi, query: query, filter: recordFilter)
    }

    private struct FetchKey: Equatable {
        let projectID: Int64
        let query: String
        let choice: FilterChoice
        let status: RecordSync.Status?
    }
}

/// A filter tag: 14/600 in a 4 pt outline, filled charcoal with white text when active
/// (white with charcoal text in dark mode), inside a 44 pt hit area.
private struct FilterTag: View {
    let title: String
    let isActive: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .textStyle(Theme.Text.filterTag)
                .foregroundStyle(isActive ? Theme.Color.ground : Theme.Color.textPrimary)
                .lineLimit(1)
                .fixedSize()
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(isActive ? Theme.Color.textPrimary : Color.clear, in: RoundedRectangle(cornerRadius: 4))
                .overlay {
                    if !isActive {
                        RoundedRectangle(cornerRadius: 4).strokeBorder(Theme.Color.hairlineStrong, lineWidth: 1)
                    }
                }
                .frame(minHeight: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title)
        .accessibilityAddTraits(isActive ? .isSelected : [])
    }
}

/// One RFI in the list, at least 72 pt: number and status tag, the subject on up to two lines,
/// then ball in court and due date.
private struct RFIRecordRow: View {
    let record: RecordRow

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .center, spacing: 12) {
                if !record.number.isEmpty {
                    Text("#\(record.number)")
                        .textStyle(Theme.Text.rowNumber)
                        .foregroundStyle(Theme.Color.tertiary)
                }
                Spacer(minLength: 0)
                StatusTag(StatusTag.state(forRFIStatus: record.status, draft: false))
            }
            Text(record.subject)
                .textStyle(Theme.Text.rowTitle)
                .foregroundStyle(Theme.Color.textPrimary)
                .lineLimit(2)
            if let meta = RecordFormat.listMeta(ballInCourt: record.ballInCourt, dueDate: record.dueDate) {
                Text(meta)
                    .textStyle(Theme.Text.meta12)
                    .foregroundStyle(Theme.Color.tertiary)
                    .lineLimit(dynamicTypeSize.isAccessibilitySize ? 3 : 1)
            }
        }
        .padding(.vertical, 14)
        .frame(maxWidth: .infinity, minHeight: 72, alignment: .leading)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

enum RecordFormat {
    /// "Oct 30" when the due day falls in the current year, "Oct 30, 2027" otherwise. Due dates are
    /// calendar days stored as midnight UTC, so the day formats in UTC; "now" takes its year from
    /// `calendar`, the phone's own.
    static func dueDate(_ date: Date, now: Date = .now, calendar: Calendar = .autoupdatingCurrent,
                        locale: Locale = .autoupdatingCurrent) -> String {
        var dayCalendar = calendar
        dayCalendar.timeZone = .gmt
        let style = Date.FormatStyle(locale: locale, calendar: dayCalendar, timeZone: .gmt).month(.abbreviated).day()
        let sameYear = dayCalendar.component(.year, from: date) == calendar.component(.year, from: now)
        return date.formatted(sameYear ? style : style.year())
    }

    /// A list row's third line, "Ben West · Due Oct 30", from whichever parts are present.
    static func listMeta(ballInCourt: String, dueDate: Date?, now: Date = .now,
                         calendar: Calendar = .autoupdatingCurrent, locale: Locale = .autoupdatingCurrent) -> String? {
        var parts: [String] = []
        if !ballInCourt.isEmpty { parts.append(ballInCourt) }
        if let dueDate { parts.append("Due \(Self.dueDate(dueDate, now: now, calendar: calendar, locale: locale))") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
