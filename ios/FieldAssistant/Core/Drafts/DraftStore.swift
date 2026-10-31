import Foundation
import SwiftData

/// The phone's drafts, kept in SwiftData. The home screen lists them and sync walks them by status.
@MainActor
final class DraftStore {
    private let context: ModelContext
    /// A context references its container weakly and traps once it is gone, so the store keeps it alive.
    private let container: ModelContainer
    /// Saves an insert. Tests pass one that throws to exercise the rollback.
    private let saveInsert: @MainActor (ModelContext) throws -> Void

    init(context: ModelContext, saveInsert: @escaping @MainActor (ModelContext) throws -> Void = { try $0.save() }) {
        self.context = context
        container = context.container
        self.saveInsert = saveInsert
    }

    func insert(_ draft: LocalDraft) throws {
        context.insert(draft)
        do {
            try saveInsert(context)
        } catch {
            // Leave nothing half-inserted behind, so Build can try the same draft id again. The
            // photos are detached first: they stay with the capture (and their files on disk), so
            // the next Build can hand them to a new draft.
            let photos = draft.photos
            for photo in photos { photo.draft = nil }
            context.rollback()
            throw error
        }
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

    /// One owner's queued drafts (`submitPending`), across projects, the oldest `updatedAt` first.
    func draftsPendingSubmit(owner: Int64) -> [LocalDraft] {
        let descriptor = FetchDescriptor<LocalDraft>(
            predicate: #Predicate { $0.submitPending == true && $0.ownerProcoreUserID == owner },
            sortBy: [SortDescriptor(\.updatedAt)]
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
