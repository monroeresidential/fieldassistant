import FieldAssistantAPI
import Foundation

typealias Company = Components.Schemas.Company
typealias Project = Components.Schemas.Project

/// Lists the companies and projects the signed-in user can see and saves their choice server-side.
/// Every method throws `APIFailure`.
struct ScopeService {
    let client: Client

    func companies() async throws -> [Company] {
        let output = try await APIFailure.mapping { try await client.listCompanies() }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    func projects(companyID: Int64) async throws -> [Project] {
        let output = try await APIFailure.mapping { try await client.listProjects(path: .init(companyId: companyID)) }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    func saveSelection(companyID: Int64, projectID: Int64) async throws {
        let body = Components.Schemas.Selection(company_id: companyID, project_id: projectID)
        let output = try await APIFailure.mapping { try await client.putMeSelection(body: .json(body)) }
        switch output {
        case .noContent: return
        case .badRequest(let response): throw APIFailure(statusCode: 400, response: response)
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }
}
