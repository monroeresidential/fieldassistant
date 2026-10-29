import Foundation

/// The definition list under a created RFI (handoff 1g): Project, Assigned, Due, Attached and Sent,
/// each omitted when there is nothing to say rather than shown as "0 photos" or a dash.
struct ResultSummary: Equatable {
    struct Row: Equatable {
        let term: String
        let value: String
        /// Dates and times set in tabular figures.
        let tabular: Bool
    }

    let rows: [Row]

    /// `assigneeName` is resolved by the caller from the cached people list. Dates and times format
    /// with the calendar's locale and time zone.
    init(draft: LocalDraft, projectName: String?, assigneeName: String?, now: Date, calendar: Calendar = .current) {
        var rows: [Row] = []
        if let projectName = projectName?.nonEmpty {
            rows.append(Row(term: "Project", value: projectName, tabular: false))
        }
        if let assigneeName = assigneeName?.nonEmpty {
            rows.append(Row(term: "Assigned", value: assigneeName, tabular: false))
        }
        if let due = draft.server?.payload.due_date.flatMap({ Self.dueDate($0, calendar: calendar) }) {
            rows.append(Row(term: "Due", value: due, tabular: true))
        }
        let photos = draft.photos.count
        if photos > 0 {
            rows.append(Row(term: "Attached", value: photos == 1 ? "1 photo" : "\(photos) photos", tabular: false))
        }
        rows.append(Row(term: "Sent",
                        value: Self.sent(draft.updatedAt, capturedAt: draft.capturedAt, now: now, calendar: calendar),
                        tabular: true))
        self.rows = rows
    }

    /// "Fri, Oct 30" from the payload's ISO date, read as that day in the calendar's time zone.
    private static func dueDate(_ iso: String, calendar: Calendar) -> String? {
        let parse = Date.ISO8601FormatStyle(timeZone: calendar.timeZone).year().month().day().dateSeparator(.dash)
        guard let date = try? parse.parse(iso) else { return nil }
        return date.formatted(style(calendar).weekday(.abbreviated).month(.abbreviated).day())
    }

    /// "2:14 PM · 48 s from record": the send time (with the date, "Oct 30 at 2:14 PM", when it was
    /// not today) and, within the hour, how long after the recording started it went.
    private static func sent(_ sentAt: Date, capturedAt: Date, now: Date, calendar: Calendar) -> String {
        let time = calendar.isDate(sentAt, inSameDayAs: now)
            ? sentAt.formatted(style(calendar).hour().minute())
            : sentAt.formatted(style(calendar).month(.abbreviated).day().hour().minute())
        let elapsed = Int(sentAt.timeIntervalSince(capturedAt))
        switch elapsed {
        // A no-break space keeps the number with its unit when the line wraps.
        case 0..<60: return "\(time) · \(elapsed)\u{00A0}s from record"
        case 60..<3600: return "\(time) · \(elapsed / 60)\u{00A0}min from record"
        default: return time
        }
    }

    private static func style(_ calendar: Calendar) -> Date.FormatStyle {
        Date.FormatStyle(locale: calendar.locale ?? .current, calendar: calendar, timeZone: calendar.timeZone)
    }
}

private extension String {
    var nonEmpty: String? { isEmpty ? nil : self }
}
