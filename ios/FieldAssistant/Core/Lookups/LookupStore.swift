import Foundation
import Observation
import SwiftData

/// The on-device cache of each project's lookup lists, kept in SwiftData and revalidated by `ETag`.
@Observable
@MainActor
final class LookupStore {
    private(set) var isPrefetching = false
    /// Bumped on every save, and read by `set(projectID:kind:)`, so views showing rows re-render.
    private var revision = 0

    @ObservationIgnored private let context: ModelContext
    /// A context references its container weakly and traps once it is gone, so the store keeps it alive.
    @ObservationIgnored private let container: ModelContainer
    @ObservationIgnored private let service: LookupService

    init(context: ModelContext, service: LookupService) {
        self.context = context
        container = context.container
        self.service = service
    }

    /// The cached rows, or none when the list was never loaded or is unavailable to this user.
    func rows(projectID: Int64, kind: LookupKind) -> [LookupRow] {
        set(projectID: projectID, kind: kind)?.rows ?? []
    }

    /// The cached list for a project and kind, or `nil` if it has never been fetched.
    func set(projectID: Int64, kind: LookupKind) -> CachedLookup? {
        _ = revision
        let key = CachedLookup.key(projectID: projectID, kind: kind.rawValue)
        var descriptor = FetchDescriptor<CachedLookup>(predicate: #Predicate { $0.key == key })
        descriptor.fetchLimit = 1
        return try? context.fetch(descriptor).first
    }

    /// Refreshes all six lists concurrently. A list that fails keeps its cached copy.
    func prefetch(companyID: Int64, projectID: Int64, force: Bool = false) async {
        isPrefetching = true
        defer { isPrefetching = false }
        await withTaskGroup(of: Void.self) { group in
            for kind in LookupKind.allCases {
                group.addTask {
                    try? await self.refresh(companyID: companyID, projectID: projectID, kind: kind, force: force)
                }
            }
        }
    }

    /// Fetches one list, revalidating with the cached `ETag`, and stores a changed result.
    /// Throws `APIFailure`; a failure or a 304 leaves the cached copy as it was.
    func refresh(companyID: Int64, projectID: Int64, kind: LookupKind, force: Bool) async throws {
        let result = try await service.fetch(
            companyID: companyID,
            projectID: projectID,
            kind: kind,
            etag: set(projectID: projectID, kind: kind)?.etag,
            force: force
        )
        guard case .rows(let response, let etag) = result else { return }
        let rowsData = try JSONEncoder().encode(response.rows)
        // Looked up again after the await, in case a concurrent refresh inserted the record meanwhile.
        if let record = set(projectID: projectID, kind: kind) {
            record.etag = etag
            record.rowsData = rowsData
            record.unavailable = response.unavailable
            record.reason = response.reason
            record.fetchedAt = response.fetched_at
        } else {
            context.insert(CachedLookup(
                projectID: projectID,
                kind: kind.rawValue,
                etag: etag,
                rowsData: rowsData,
                unavailable: response.unavailable,
                reason: response.reason,
                fetchedAt: response.fetched_at
            ))
        }
        try context.save()
        revision += 1
    }
}
