import FieldAssistantAPI
import Foundation

/// Fetches one project lookup list, revalidating with the cached `ETag`. Throws `APIFailure`.
struct LookupService: Sendable {
    enum Fetch: Sendable {
        case notModified
        case rows(Components.Schemas.LookupResponse, etag: String)
    }

    let client: Client

    /// Sends `If-None-Match` when `etag` is given, and `Cache-Control: no-cache` when `force` is set,
    /// which makes the backend refetch the list from Procore.
    func fetch(companyID: Int64, projectID: Int64, kind: LookupKind, etag: String?, force: Bool = false) async throws -> Fetch {
        let output = try await APIFailure.mapping {
            try await client.getLookup(
                path: .init(projectId: projectID, kind: kind.apiValue),
                headers: .init(
                    X_hyphen_Company_hyphen_Id: companyID,
                    If_hyphen_None_hyphen_Match: etag,
                    Cache_hyphen_Control: force ? "no-cache" : nil
                )
            )
        }
        switch output {
        case .ok(let ok):
            let body = try await APIFailure.mapping { try ok.body.json }
            return .rows(body, etag: ok.headers.ETag)
        case .notModified:
            return .notModified
        case .badRequest(let response): throw APIFailure(statusCode: 400, response: response)
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }
}
