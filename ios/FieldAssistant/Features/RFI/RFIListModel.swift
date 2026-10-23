import FieldAssistantAPI
import Foundation
import Observation

typealias RFISummary = Components.Schemas.RFISummary

/// Loads the RFIs of one project.
@Observable
@MainActor
final class RFIListModel {
    enum State: Equatable {
        case idle
        case loading
        case loaded([RFISummary])
        case failed(APIFailure)
    }

    private(set) var state: State = .idle

    @ObservationIgnored private let client: Client

    init(client: Client) {
        self.client = client
    }

    func load(companyID: Int64, projectID: Int64) async {
        state = .loading
        do {
            state = .loaded(try await fetch(companyID: companyID, projectID: projectID))
        } catch {
            // A superseded load (project changed, view gone) must not overwrite newer state with an error.
            if Task.isCancelled { return }
            state = .failed(APIFailure(error))
        }
    }

    private func fetch(companyID: Int64, projectID: Int64) async throws -> [RFISummary] {
        let output = try await APIFailure.mapping {
            try await client.listRFIs(path: .init(projectId: projectID), headers: .init(X_hyphen_Company_hyphen_Id: companyID))
        }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }
}

extension RFISummary {
    /// `due_date` is an OpenAPI `date` (`yyyy-MM-dd`), read as midnight in the device's time zone
    /// so the displayed day matches the one in Procore.
    var dueDate: Date? {
        guard let due_date else { return nil }
        return try? Date.ISO8601FormatStyle(timeZone: .current).year().month().day().dateSeparator(.dash).parse(due_date)
    }
}
