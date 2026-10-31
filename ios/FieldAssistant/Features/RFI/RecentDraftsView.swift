import SwiftData
import SwiftUI

/// The project's latest drafts on this phone, newest first, as hairline-ruled rows on Home.
/// Tapping a row opens the screen its status calls for. A SwiftData query keeps the rows live
/// while sync and review change statuses.
struct RecentDraftsView: View {
    let onOpen: (Route) -> Void
    @Environment(AppState.self) private var appState
    @Query private var drafts: [LocalDraft]

    /// `owner` is the signed-in Procore user; other users' drafts on the phone never show.
    init(projectID: Int64, owner: Int64, limit: Int = 3, onOpen: @escaping (Route) -> Void) {
        self.onOpen = onOpen
        var descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.projectID == projectID && $0.ownerProcoreUserID == owner },
            sortBy: [SortDescriptor(\.capturedAt, order: .reverse)]
        )
        descriptor.fetchLimit = limit
        _drafts = Query(descriptor)
    }

    var body: some View {
        if drafts.isEmpty {
            Text("Drafts you record show up here.")
                .textStyle(Theme.Text.meta13)
                .foregroundStyle(Theme.Color.muted)
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
        } else {
            VStack(spacing: 0) {
                ForEach(drafts) { draft in
                    HairlineDivider()
                    Button {
                        onOpen(Self.route(for: draft, isSynced: isSynced))
                    } label: {
                        DraftRow(draft: draft)
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint(Self.hint(for: draft.status))
                }
                HairlineDivider()
            }
        }
    }

    /// Whether the project's mirror holds the RFI with this Procore id.
    private func isSynced(_ procoreID: Int64, projectID: Int64) -> Bool {
        appState.records.record(projectID: projectID, kind: .rfi, procoreID: procoreID) != nil
    }

    /// Waiting drafts open Review, which sends them first. A submitted one opens the synced RFI
    /// when the mirror has it, otherwise the Result screen (the RFI has not synced yet, or the
    /// draft predates `resultProcoreID`).
    static func route(for draft: LocalDraft, isSynced: (_ procoreID: Int64, _ projectID: Int64) -> Bool) -> Route {
        switch draft.status {
        case .submitted:
            if let procoreID = draft.resultProcoreID, isSynced(procoreID, draft.projectID) {
                return .rfi(procoreID)
            }
            return .result(draft.id)
        case .waiting, .draft, .submitting, .failed:
            return .review(draft.id)
        }
    }

    static func hint(for status: LocalDraftStatus) -> String {
        switch status {
        case .waiting: "Sends the draft, then opens it for review"
        case .draft, .failed: "Opens the draft for review"
        case .submitting: "Shows the draft while Procore confirms it"
        case .submitted: "Opens the RFI created in Procore"
        }
    }
}

/// Where a draft queued on the phone (`submitPending`) stands, for its row.
enum QueuedState: Equatable {
    case waitingForSignal, sending, queued

    /// `nil` when the draft is not queued. No signal wins over a run in progress (the run is about
    /// to stop), and a run in progress over plain Queued.
    init?(submitPending: Bool, reachable: Bool, running: Bool) {
        guard submitPending else { return nil }
        if !reachable {
            self = .waitingForSignal
        } else if running {
            self = .sending
        } else {
            self = .queued
        }
    }

    var segments: [String] {
        switch self {
        case .waitingForSignal: ["Queued", "waiting for signal"]
        case .sending: ["Queued", "sending"]
        case .queued: ["Queued"]
        }
    }
}

/// The meta line under a draft's title: "RFI · 2:14 PM · 3 photos", then where a queued draft
/// stands: "· Queued · waiting for signal".
enum DraftRowMeta {
    /// The feature upper-cased; the capture time today, "Yesterday" the day before, otherwise the
    /// weekday; the photo count only when there are photos; the queued state when there is one.
    /// Formats with the calendar's locale and time zone.
    static func line(
        feature: String,
        capturedAt: Date,
        photos: Int,
        queued: QueuedState? = nil,
        now: Date,
        calendar: Calendar = .current
    ) -> String {
        var segments = [feature.uppercased(), when(capturedAt, now: now, calendar: calendar)]
        if photos > 0 { segments.append(photos == 1 ? "1 photo" : "\(photos) photos") }
        if let queued { segments += queued.segments }
        return segments.joined(separator: " · ")
    }

    private static func when(_ date: Date, now: Date, calendar: Calendar) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return format(date, template: "jmm", calendar: calendar) }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(date, inSameDayAs: yesterday) {
            return "Yesterday"
        }
        return format(date, template: "EEE", calendar: calendar)
    }

    private static func format(_ date: Date, template: String, calendar: Calendar) -> String {
        let formatter = DateFormatter()
        formatter.calendar = calendar
        formatter.locale = calendar.locale ?? .current
        formatter.timeZone = calendar.timeZone
        formatter.setLocalizedDateFormatFromTemplate(template)
        return formatter.string(from: date)
    }
}

/// One draft: title 15/500, the meta line 12 tertiary, and its status tag. The Drafts screen
/// adds a third line for waiting, queued and failed drafts and gives the row more room.
struct DraftRow: View {
    let draft: LocalDraft
    var showsDetail = false

    @Environment(AppState.self) private var appState

    /// Where a queued draft stands. Reachability and the runner are both observed, so the row
    /// moves between waiting, sending and Queued on its own.
    private var queued: QueuedState? {
        QueuedState(
            submitPending: draft.submitPending,
            reachable: appState.reachability.isReachable,
            running: appState.submitRunner.isRunning(draft.id)
        )
    }

    /// Photos the user removed are on their way out and are not counted.
    private var photoCount: Int {
        draft.photos.filter { $0.uploadState != .removed }.count
    }

    private static let metaStyle = Theme.TextStyle(12, .regular, relativeTo: .caption, monospacedDigits: true)
    private static let emphasisStyle = Theme.TextStyle(12, .medium, relativeTo: .caption)

    @Environment(\.dynamicTypeSize) private var dynamicTypeSize

    var body: some View {
        // At accessibility sizes the tag goes under the text so the title keeps the width.
        let layout = dynamicTypeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8))
            : AnyLayout(HStackLayout(spacing: 12))
        layout {
            VStack(alignment: .leading, spacing: showsDetail ? 3 : 2) {
                Text(draft.subject ?? "RFI draft")
                    .textStyle(Theme.Text.rowTitle)
                    .foregroundStyle(Theme.Color.textPrimary)
                    .lineLimit(showsDetail ? 2 : 1)
                Text(DraftRowMeta.line(feature: draft.feature, capturedAt: draft.capturedAt,
                                       photos: photoCount, queued: queued, now: .now))
                    .textStyle(Self.metaStyle)
                    .foregroundStyle(Theme.Color.tertiary)
                if showsDetail, let detail {
                    Text(detail.text)
                        .textStyle(detail.isAlert ? Self.emphasisStyle : Theme.Text.meta12)
                        .foregroundStyle(detail.isAlert ? Theme.Color.accent : Theme.Color.muted)
                        .lineLimit(2)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            StatusTag(StatusTag.state(for: draft.status, number: draft.resultNumber))
        }
        .padding(.vertical, showsDetail ? 14 : 10)
        .frame(maxWidth: .infinity, minHeight: showsDetail ? 72 : 60, alignment: .leading)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    /// The third line the handoff gives waiting and queued drafts, or the last error. Alerts
    /// (waiting, failed) are accent 12/500; the queued note is muted.
    private var detail: (text: String, isAlert: Bool)? {
        switch draft.status {
        case .waiting: (draft.errorMessage ?? "Needs connection to build the draft", true)
        case .submitting: ("Reviewed. Sends automatically, once.", false)
        case .failed: draft.errorMessage.map { ($0, true) }
        case .draft, .submitted: nil
        }
    }
}

#Preview {
    let container = try! AppContainer.makeModelContainer(inMemory: true)
    let waiting = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, transcript: "Twisted column", capturedAt: .now.addingTimeInterval(-300))
    let submitted = LocalDraft(companyID: 1, projectID: 7, ownerProcoreUserID: 42, status: .submitted, transcript: "Header size")
    submitted.subject = "Header size over garage door"
    submitted.resultNumber = "214"
    container.mainContext.insert(waiting)
    container.mainContext.insert(submitted)
    return RecentDraftsView(projectID: 7, owner: 42) { _ in }
        .padding(20)
        .modelContainer(container)
        .environment(AppState.preview(container: container))
}
