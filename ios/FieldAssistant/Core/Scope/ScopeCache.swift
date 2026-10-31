import FieldAssistantAPI
import Foundation
import Observation

/// The signed-in user's companies and each company's projects, fetched once per session: the
/// picker and Home's title read them from here, so switching projects or companies costs no
/// request. `force` (pull-to-refresh, Retry) fetches again. `AppState` owns one and clears it when
/// the session ends or is re-established. Every method throws `APIFailure`.
@MainActor
@Observable
final class ScopeCache {
    @ObservationIgnored private let service: ScopeService
    @ObservationIgnored private var companyList: [Company]?
    @ObservationIgnored private var projectLists: [Int64: [Project]] = [:]
    /// Advances on `clear()`. A fetch started before then does not fill the cache it no longer belongs to.
    @ObservationIgnored private var generation = 0

    init(service: ScopeService) {
        self.service = service
    }

    func companies(force: Bool = false) async throws -> [Company] {
        if !force, let companyList { return companyList }
        let started = generation
        let list = try await service.companies()
        if started == generation { companyList = list }
        return list
    }

    func projects(companyID: Int64, force: Bool = false) async throws -> [Project] {
        if !force, let list = projectLists[companyID] { return list }
        let started = generation
        let list = try await service.projects(companyID: companyID)
        if started == generation { projectLists[companyID] = list }
        return list
    }

    /// Saves the user's choice on the server; see `ScopeService.saveSelection`.
    func saveSelection(companyID: Int64, projectID: Int64) async throws {
        try await service.saveSelection(companyID: companyID, projectID: projectID)
    }

    /// Forgets every list; the next read fetches again.
    func clear() {
        generation += 1
        companyList = nil
        projectLists = [:]
    }
}
