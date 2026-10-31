import FieldAssistantAPI
import Foundation

/// Whether the signed-in user may use a feature on a project, and Procore's reason when not.
struct FeatureAvailability: Equatable, Sendable {
    let available: Bool
    let reason: String?
}

/// Asks the backend which features the signed-in user may use on a project. Throws `APIFailure`.
struct FeaturesService: Sendable {
    let client: Client

    /// The project's features keyed by name (`rfi`). The backend keeps its answer for ten minutes;
    /// `noCache` sends `Cache-Control: no-cache`, which makes it ask Procore again.
    func features(projectID: Int64, companyID: Int64, noCache: Bool = false) async throws -> [String: FeatureAvailability] {
        let output = try await APIFailure.mapping {
            try await client.getProjectFeatures(
                path: .init(projectId: projectID),
                headers: .init(
                    X_hyphen_Company_hyphen_Id: companyID,
                    Cache_hyphen_Control: noCache ? "no-cache" : nil
                )
            )
        }
        switch output {
        case .ok(let ok):
            let body = try await APIFailure.mapping { try ok.body.json }
            return Dictionary(
                body.features.map { ($0.feature.rawValue, FeatureAvailability(available: $0.available, reason: $0.reason)) },
                uniquingKeysWith: { _, last in last }
            )
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }
}
