import Foundation
import SwiftData

enum RecordFilter: Equatable {
    case all
    case status(String)
    case ballInCourt(userName: String)
}

/// One record in a list, copied out of SwiftData. Views hold these rather than `CachedRecord`, so a
/// sync that deletes a row can never leave a screen reading an invalidated model.
struct RecordRow: Identifiable, Equatable {
    let procoreID: Int64
    let number: String
    let subject: String
    let status: String
    let ballInCourt: String
    let dueDate: Date?

    var id: Int64 { procoreID }

    init(_ record: CachedRecord) {
        procoreID = record.procoreID
        number = record.number
        subject = record.subject
        status = record.status
        ballInCourt = record.ballInCourt
        dueDate = record.dueDate
    }
}

/// The on-device mirror of a project's records in SwiftData. Applies delta pages from the backend
/// and serves the local list.
@MainActor
final class RecordStore {
    let context: ModelContext
    /// A context references its container weakly and traps once it is gone, so the store keeps it alive.
    private let container: ModelContainer

    init(context: ModelContext) {
        self.context = context
        container = context.container
    }

    /// Nil before the first sync of this project and kind.
    func state(projectID: Int64, kind: RecordKind) -> ProjectSyncState? {
        let key = ProjectSyncState.key(projectID: projectID, kind: kind)
        var descriptor = FetchDescriptor<ProjectSyncState>(predicate: #Predicate { $0.key == key })
        descriptor.fetchLimit = 1
        return try? context.fetch(descriptor).first
    }

    /// Upserts the page's records, removes `deleted_ids`, advances the cursor and records the outcome
    /// of the pull. A failed pull still applies the page. One save.
    func apply(_ page: RecordPage, projectID: Int64, kind: RecordKind) throws {
        let kindName = kind.rawValue
        let existing = try context.fetch(FetchDescriptor<CachedRecord>(
            predicate: #Predicate { $0.projectID == projectID && $0.kind == kindName }
        ))
        var byID = Dictionary(uniqueKeysWithValues: existing.map { ($0.procoreID, $0) })

        for envelope in page.records {
            guard let rfi = envelope.rfi else { continue }
            let body = try CachedRecord.encoder.encode(rfi)
            let due = Self.date(fromFullDate: rfi.due_date)
            let ball = rfi.ball_in_court?.name ?? ""
            if let row = byID[envelope.id] {
                row.changeSeq = envelope.change_seq
                row.updatedAt = envelope.updated_at
                row.bodyData = body
                row.number = rfi.number
                row.numberSort = CachedRecord.numberSort(for: rfi.number)
                row.subject = rfi.subject
                row.status = rfi.status
                row.ballInCourt = ball
                row.dueDate = due
            } else {
                let row = CachedRecord(
                    projectID: projectID, kind: kind, procoreID: envelope.id, changeSeq: envelope.change_seq,
                    updatedAt: envelope.updated_at, bodyData: body, number: rfi.number, subject: rfi.subject,
                    status: rfi.status, ballInCourt: ball, dueDate: due
                )
                context.insert(row)
                byID[envelope.id] = row
            }
        }
        for id in page.deleted_ids {
            if let row = byID.removeValue(forKey: id) { context.delete(row) }
        }

        let state = stateRow(projectID: projectID, kind: kind)
        state.nextSince = page.next_since
        if let syncedAt = page.synced_at { state.lastSyncedAt = syncedAt }
        if page.pull.status == .failed {
            state.lastErrorCode = page.pull.code
            state.lastError = page.pull.message ?? "Sync failed"
        } else {
            state.lastErrorCode = nil
            state.lastError = nil
        }
        try context.save()
    }

    /// Records a failure that happened before a page arrived. Rows and cursor stay as they were.
    func markFailed(projectID: Int64, kind: RecordKind, code: String?, message: String) throws {
        let state = stateRow(projectID: projectID, kind: kind)
        state.lastErrorCode = code
        state.lastError = message
        try context.save()
    }

    /// The project's records as value snapshots, highest number first, then by number text.
    func records(projectID: Int64, kind: RecordKind, query: String, filter: RecordFilter) -> [RecordRow] {
        let kindName = kind.rawValue
        let descriptor = FetchDescriptor<CachedRecord>(
            predicate: #Predicate { $0.projectID == projectID && $0.kind == kindName }
        )
        var rows = (try? context.fetch(descriptor)) ?? []
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !needle.isEmpty {
            rows = rows.filter {
                $0.number.localizedCaseInsensitiveContains(needle) || $0.subject.localizedCaseInsensitiveContains(needle)
            }
        }
        switch filter {
        case .all: break
        case .status(let status):
            let wanted = status.lowercased()
            rows = rows.filter { $0.status.lowercased() == wanted }
        case .ballInCourt(let userName):
            rows = rows.filter { $0.ballInCourt == userName }
        }
        return rows.sorted {
            $0.numberSort != $1.numberSort ? $0.numberSort > $1.numberSort : $0.number > $1.number
        }.map(RecordRow.init)
    }

    func record(projectID: Int64, kind: RecordKind, procoreID: Int64) -> CachedRecord? {
        let key = CachedRecord.key(projectID: projectID, kind: kind, procoreID: procoreID)
        var descriptor = FetchDescriptor<CachedRecord>(predicate: #Predicate { $0.key == key })
        descriptor.fetchLimit = 1
        return try? context.fetch(descriptor).first
    }

    /// Drops one project's records and cursor for `kind`, so the next pull pages from 0. One save.
    func clear(projectID: Int64, kind: RecordKind) throws {
        let kindName = kind.rawValue
        try context.delete(
            model: CachedRecord.self,
            where: #Predicate { $0.projectID == projectID && $0.kind == kindName }
        )
        let key = ProjectSyncState.key(projectID: projectID, kind: kind)
        try context.delete(model: ProjectSyncState.self, where: #Predicate { $0.key == key })
        try context.save()
    }

    /// Drops every mirrored record and cursor, for sign-out.
    func clearAll() throws {
        try context.delete(model: CachedRecord.self)
        try context.delete(model: ProjectSyncState.self)
        try context.save()
    }

    private func stateRow(projectID: Int64, kind: RecordKind) -> ProjectSyncState {
        if let state = state(projectID: projectID, kind: kind) { return state }
        let state = ProjectSyncState(projectID: projectID, kind: kind)
        context.insert(state)
        return state
    }

    /// `due_date` is an OpenAPI `date` (`yyyy-MM-dd`), stored as midnight UTC.
    private static func date(fromFullDate value: String?) -> Date? {
        guard let value else { return nil }
        return try? Date.ISO8601FormatStyle(timeZone: TimeZone(identifier: "UTC") ?? .gmt)
            .year().month().day().dateSeparator(.dash).parse(value)
    }
}
