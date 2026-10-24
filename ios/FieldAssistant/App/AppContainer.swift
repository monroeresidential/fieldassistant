import SwiftData

/// Builds the app's SwiftData container. Tests pass `inMemory: true`.
enum AppContainer {
    static func makeModelContainer(inMemory: Bool = false) throws -> ModelContainer {
        let schema = Schema([CachedLookup.self])
        let configuration = ModelConfiguration(schema: schema, isStoredInMemoryOnly: inMemory)
        return try ModelContainer(for: schema, configurations: [configuration])
    }
}
