import SwiftData

/// The first versioned shape of the on-device store: the five models the builds before versioning
/// stored under a plain `Schema`. Same models, same hash, so those stores open as this version.
/// A later build that changes a model adds `AppSchemaV2` and a stage to `AppMigrationPlan`.
enum AppSchemaV1: VersionedSchema {
    static let versionIdentifier = Schema.Version(1, 0, 0)
    static let models: [any PersistentModel.Type] = [
        CachedLookup.self, LocalDraft.self, LocalPhoto.self, CachedRecord.self, ProjectSyncState.self,
    ]
}

/// Every schema version the app has shipped, oldest first, and the stages between them.
enum AppMigrationPlan: SchemaMigrationPlan {
    static let schemas: [any VersionedSchema.Type] = [AppSchemaV1.self]
    static let stages: [MigrationStage] = []
}
