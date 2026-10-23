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
    /// The most recent successful result, kept so a refresh can show it while `state` is `.loading`.
    private(set) var lastLoaded: [RFISummary]?

    @ObservationIgnored private let client: Client

    init(client: Client) {
        self.client = client
    }

    func load(companyID: Int64, projectID: Int64) async {
        let previous = state
        state = .loading
        do {
            let items = try await fetch(companyID: companyID, projectID: projectID)
            lastLoaded = items
            state = .loaded(items)
        } catch {
            // A cancelled load (refresh interrupted, view gone) puts back what was showing
            // instead of reporting an error or leaving the list stuck in `.loading`.
            if Task.isCancelled {
                if state == .loading { state = previous == .loading ? .idle : previous }
                return
            }
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
