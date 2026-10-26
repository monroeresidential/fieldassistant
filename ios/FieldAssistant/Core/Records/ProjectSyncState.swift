import Foundation
import SwiftData

/// Where one project's mirror of one record kind stands: the cursor for the next delta page
/// and the outcome of the last sync.
@Model
final class ProjectSyncState {
    /// `"<projectID>/<kind>"`. Derived because `#Unique` over two fields needs iOS 18.
    @Attribute(.unique) var key: String
    var projectID: Int64
    var kind: String
    var nextSince: Int64
    var lastSyncedAt: Date?
    var lastError: String?
    var lastErrorCode: String?

    init(
        projectID: Int64, kind: RecordKind, nextSince: Int64 = 0,
        lastSyncedAt: Date? = nil, lastError: String? = nil, lastErrorCode: String? = nil
    ) {
        key = Self.key(projectID: projectID, kind: kind)
        self.projectID = projectID
        self.kind = kind.rawValue
        self.nextSince = nextSince
        self.lastSyncedAt = lastSyncedAt
        self.lastError = lastError
        self.lastErrorCode = lastErrorCode
    }

    static func key(projectID: Int64, kind: RecordKind) -> String {
        "\(projectID)/\(kind.rawValue)"
    }
}
