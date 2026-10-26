import FieldAssistantAPI
import Foundation
import OpenAPIRuntime

/// Talks to the backend's project mirror: delta pages, a sync trigger and attachment bytes.
/// Throws `APIFailure`.
struct RecordService: Sendable {
    /// Upper bound for one attachment held in memory.
    static let maxAttachmentBytes = 50 * 1024 * 1024

    let client: Client

    /// One page of records changed after `since`. `force` sends `Cache-Control: no-cache`,
    /// which makes the backend pull from Procore before answering.
    func page(companyID: Int64, projectID: Int64, kind: RecordKind, since: Int64, force: Bool) async throws -> RecordPage {
        let output = try await APIFailure.mapping {
            try await client.getRecords(
                path: .init(projectId: projectID, kind: Self.pathKind(kind)),
                query: .init(since: since, limit: 500),
                headers: .init(
                    X_hyphen_Company_hyphen_Id: companyID,
                    Cache_hyphen_Control: force ? "no-cache" : nil
                )
            )
        }
        switch output {
        case .ok(let ok):
            return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    /// Pulls `kinds` from Procore now and returns the outcome of each pull.
    func sync(companyID: Int64, projectID: Int64, kinds: [RecordKind], full: Bool) async throws -> [PullStatus] {
        let output = try await APIFailure.mapping {
            try await client.syncProject(
                path: .init(projectId: projectID),
                headers: .init(X_hyphen_Company_hyphen_Id: companyID),
                body: .json(.init(kinds: kinds.map(Self.syncKind), full: full))
            )
        }
        switch output {
        case .ok(let ok):
            return try await APIFailure.mapping { try ok.body.json.pulls }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    /// The bytes of one RFI attachment, with the filename and content type the backend reports.
    func attachmentData(
        companyID: Int64, projectID: Int64, rfiID: Int64, attachmentID: Int64
    ) async throws -> (Data, filename: String?, contentType: String?) {
        let output = try await APIFailure.mapping {
            try await client.getRFIAttachment(
                path: .init(projectId: projectID, rfiId: rfiID, attachmentId: attachmentID),
                headers: .init(X_hyphen_Company_hyphen_Id: companyID)
            )
        }
        switch output {
        case .ok(let ok):
            let body = try await APIFailure.mapping { try ok.body.binary }
            let data = try await APIFailure.mapping { try await Data(collecting: body, upTo: Self.maxAttachmentBytes) }
            return (
                data,
                filename: Self.filename(fromContentDisposition: ok.headers.Content_hyphen_Disposition),
                contentType: ok.headers.Content_hyphen_Type
            )
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .notFound(let response): throw APIFailure(statusCode: 404, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    private static func pathKind(_ kind: RecordKind) -> Operations.getRecords.Input.Path.kindPayload {
        switch kind {
        case .rfi: .rfi
        }
    }

    private static func syncKind(_ kind: RecordKind) -> Components.Schemas.SyncRequest.kindsPayloadPayload {
        switch kind {
        case .rfi: .rfi
        }
    }

    /// Reads `filename="x"` or `filename=x` from a `Content-Disposition` value, nil when absent.
    static func filename(fromContentDisposition value: String) -> String? {
        for part in value.split(separator: ";").dropFirst() {
            let pair = part.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            guard pair.count == 2, pair[0].lowercased() == "filename" else { continue }
            let name = pair[1].trimmingCharacters(in: CharacterSet(charactersIn: "\""))
            return name.isEmpty ? nil : name
        }
        return nil
    }
}
