import Foundation
import SwiftData

/// One project's copy of one lookup list, with the `ETag` that revalidates it.
@Model
final class CachedLookup {
    /// `"<projectID>/<kind>"`. `#Unique` over the two fields needs iOS 18, so on iOS 17 this
    /// derived key carries the one-record-per-project-and-kind constraint.
    @Attribute(.unique) var key: String
    var projectID: Int64
    var kind: String
    var etag: String
    var rowsData: Data
    var unavailable: Bool
    var reason: String?
    var fetchedAt: Date

    init(projectID: Int64, kind: String, etag: String, rowsData: Data, unavailable: Bool, reason: String?, fetchedAt: Date) {
        key = Self.key(projectID: projectID, kind: kind)
        self.projectID = projectID
        self.kind = kind
        self.etag = etag
        self.rowsData = rowsData
        self.unavailable = unavailable
        self.reason = reason
        self.fetchedAt = fetchedAt
    }

    var rows: [LookupRow] {
        (try? JSONDecoder().decode([LookupRow].self, from: rowsData)) ?? []
    }

    static func key(projectID: Int64, kind: String) -> String {
        "\(projectID)/\(kind)"
    }
}
