import Foundation
import SwiftData

/// The phone's drafts, kept in SwiftData. The home screen lists them and sync walks them by status.
@MainActor
final class DraftStore {
    private let context: ModelContext
    /// A context references its container weakly and traps once it is gone, so the store keeps it alive.
    private let container: ModelContainer

    init(context: ModelContext) {
        self.context = context
        container = context.container
    }

    func insert(_ draft: LocalDraft) throws {
        context.insert(draft)
        try context.save()
    }

    func draft(id: UUID) -> LocalDraft? {
        var descriptor = FetchDescriptor<LocalDraft>(predicate: #Predicate { $0.id == id })
        descriptor.fetchLimit = 1
        return try? context.fetch(descriptor).first
    }

    /// One owner's drafts in a project, newest capture first.
    func recent(projectID: Int64, owner: Int64, limit: Int = 50) -> [LocalDraft] {
        var descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.projectID == projectID && $0.ownerProcoreUserID == owner },
            sortBy: [SortDescriptor(\.capturedAt, order: .reverse)]
        )
        descriptor.fetchLimit = limit
        return (try? context.fetch(descriptor)) ?? []
    }

    /// One owner's drafts in one status, across projects, oldest capture first.
    func drafts(withStatus status: LocalDraftStatus, owner: Int64) -> [LocalDraft] {
        let raw = status.rawValue
        let descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.statusRaw == raw && $0.ownerProcoreUserID == owner },
            sortBy: [SortDescriptor(\.capturedAt)]
        )
        return (try? context.fetch(descriptor)) ?? []
    }

    /// A draft by id, only if `owner` captured it.
    func draft(id: UUID, owner: Int64) -> LocalDraft? {
        draft(id: id).flatMap { $0.ownerProcoreUserID == owner ? $0 : nil }
    }

    func save() throws {
        try context.save()
    }

    /// Deletes the draft, its photos and its folder on disk.
    func delete(_ draft: LocalDraft) throws {
        let id = draft.id
        context.delete(draft)
        try context.save()
        DraftFiles.removeAll(for: id)
    }
}
