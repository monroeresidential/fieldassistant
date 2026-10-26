import SwiftUI

/// The project's RFIs from the on-device mirror: search, a filter menu and pull to sync.
/// Works offline; it reads the store, and the store follows `RecordSync`.
struct RFIRecordListView: View {
    let projectID: Int64

    @Environment(AppState.self) private var appState
    @State private var query = ""
    @State private var choice: FilterChoice = .all
    /// Value snapshots, never `CachedRecord`: a sync may delete the model a row came from.
    @State private var rows: [RecordRow] = []

    var body: some View {
        List(rows) { record in
            NavigationLink(value: Route.rfi(record.procoreID)) {
                RFIRecordRow(record: record)
            }
        }
        .listStyle(.plain)
        .overlay { emptyState }
        .searchable(text: $query, prompt: "Number or subject")
        .refreshable { await appState.syncRecords(force: true, full: false) }
        .toolbar { ToolbarItem(placement: .topBarTrailing) { filterMenu } }
        .navigationTitle("RFIs")
        .navigationBarTitleDisplayMode(.inline)
        // Refetch when the search, the filter or the sync status changes.
        .task(id: FetchKey(projectID: projectID, query: query, choice: choice, status: appState.recordSync?.status)) {
            reload()
        }
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

    private var filterMenu: some View {
        Menu {
            Picker("Filter", selection: $choice) {
                ForEach(FilterChoice.allCases) { choice in
                    Text(choice.rawValue).tag(choice)
                }
            }
        } label: {
            Image(systemName: choice == .all
                ? "line.3.horizontal.decrease.circle"
                : "line.3.horizontal.decrease.circle.fill")
                .frame(minWidth: 44, minHeight: 44)
        }
        .accessibilityLabel("Filter RFIs")
        .accessibilityValue(choice.rawValue)
    }

    // MARK: - Empty states

    @ViewBuilder
    private var emptyState: some View {
        if rows.isEmpty {
            let status = appState.recordSync?.status
            let neverSynced = appState.records.state(projectID: projectID, kind: .rfi)?.lastSyncedAt == nil
            if !query.trimmingCharacters(in: .whitespaces).isEmpty {
                ContentUnavailableView.search(text: query)
            } else if choice != .all {
                ContentUnavailableView("No matching RFIs", systemImage: "line.3.horizontal.decrease.circle",
                                       description: Text("No RFIs match “\(choice.rawValue)”."))
            } else if case .failed(code: "procore_permission_denied", _) = status {
                ContentUnavailableView("No RFI access", systemImage: "lock",
                                       description: Text("Your Procore account cannot see RFIs on this project."))
            } else if neverSynced, status == .syncing {
                ProgressView("Syncing RFIs")
            } else if neverSynced {
                ContentUnavailableView("Not synced yet. Pull to sync.", systemImage: "arrow.down.circle")
            } else {
                ContentUnavailableView("No RFIs yet", systemImage: "tray",
                                       description: Text("This project has no RFIs in Procore."))
            }
        }
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

/// One RFI in the list: number, subject, status tag, ball in court and due date.
private struct RFIRecordRow: View {
    let record: RecordRow

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                if !record.number.isEmpty {
                    Text("#\(record.number)")
                        .font(.subheadline.monospacedDigit().weight(.semibold))
                        .foregroundStyle(RecordPalette.muted)
                }
                Spacer()
                StatusTag(status: record.status)
            }
            Text(record.subject)
                .font(.body.weight(.medium))
                .lineLimit(2)
            if !record.ballInCourt.isEmpty || record.dueDate != nil {
                HStack(spacing: 12) {
                    if !record.ballInCourt.isEmpty {
                        Label(record.ballInCourt, systemImage: "person")
                    }
                    if let due = record.dueDate {
                        Label("Due \(RecordFormat.dueDate(due))", systemImage: "calendar")
                    }
                }
                .font(.footnote)
                .foregroundStyle(RecordPalette.muted)
                .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        .padding(.vertical, 4)
        .accessibilityElement(children: .combine)
    }
}

/// Handoff tokens used by the record screens until the design pass.
enum RecordPalette {
    static let accent = Color(red: 0xC2 / 255, green: 0x41 / 255, blue: 0x0C / 255)
    static let muted = Color(red: 0x5C / 255, green: 0x63 / 255, blue: 0x68 / 255)
    static let surface = Color(red: 0xF4 / 255, green: 0xF5 / 255, blue: 0xF6 / 255)
}

enum RecordFormat {
    /// Due dates are calendar days stored as midnight UTC, so they format in UTC.
    static func dueDate(_ date: Date) -> String {
        date.formatted(Date.FormatStyle(date: .abbreviated, time: .omitted, timeZone: .gmt))
    }
}

/// RFI status as a small tag: accent outline for open, neutral surface fill otherwise.
struct StatusTag: View {
    let status: String

    var body: some View {
        let isOpen = status.lowercased() == "open"
        Text(status.capitalized)
            .font(.system(size: 11, weight: .bold))
            .textCase(.uppercase)
            .padding(.horizontal, 6)
            .padding(.vertical, 3)
            .foregroundStyle(isOpen ? RecordPalette.accent : RecordPalette.muted)
            .background(isOpen ? Color.clear : RecordPalette.surface, in: RoundedRectangle(cornerRadius: 4))
            .overlay {
                if isOpen { RoundedRectangle(cornerRadius: 4).strokeBorder(RecordPalette.accent, lineWidth: 1) }
            }
            .accessibilityLabel("Status \(status)")
    }
}
