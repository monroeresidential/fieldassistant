import FieldAssistantAPI
import Foundation

typealias RFIDraftPayload = Components.Schemas.RFIDraftPayload
typealias UnresolvedField = Components.Schemas.UnresolvedField
typealias JobStatus = Components.Schemas.JobStatus

/// The phone's client for the draft endpoints: create, fetch, save, submit and job polling.
/// Every method throws `APIFailure`; a 400 on a draft route becomes `.validation` with the server's message.
struct DraftService: Sendable {
    enum SubmitStart: Sendable {
        /// Procore already has the record; carries the refetched draft.
        case submitted(DraftResponse)
        /// The submit job is queued or running; poll it with `waitForJob`.
        case running(jobID: String)
    }

    let client: Client

    /// Sends the captured transcript for extraction. The local id is the server id, so a retried
    /// create returns the existing draft instead of making a second one.
    func create(_ draft: LocalDraft) async throws -> DraftResponse {
        var location: Components.Schemas.CreateDraftRequest.locationPayload?
        if let lat = draft.latitude, let lng = draft.longitude {
            location = .init(lat: lat, lng: lng)
        }
        let request = Components.Schemas.CreateDraftRequest(
            id: Self.apiID(draft.id),
            feature: .rfi,
            project_id: draft.projectID,
            transcript: draft.transcript,
            captured_at: draft.capturedAt,
            location: location
        )
        let companyID = draft.companyID
        let output = try await APIFailure.mapping {
            try await client.createDraft(headers: .init(X_hyphen_Company_hyphen_Id: companyID), body: .json(request))
        }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .badRequest(let response): throw APIFailure(badRequest: response)
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    func get(id: UUID, companyID: Int64) async throws -> DraftResponse {
        let output = try await APIFailure.mapping {
            try await client.getDraft(path: .init(draftId: Self.apiID(id)), headers: .init(X_hyphen_Company_hyphen_Id: companyID))
        }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .notFound(let response): throw APIFailure(statusCode: 404, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    /// Stores the reviewed payload; the response is revalidated against the lookups.
    func save(id: UUID, companyID: Int64, payload: RFIDraftPayload) async throws -> DraftResponse {
        let output = try await APIFailure.mapping {
            try await client.saveDraft(
                path: .init(draftId: Self.apiID(id)),
                headers: .init(X_hyphen_Company_hyphen_Id: companyID),
                body: .json(.init(payload: payload))
            )
        }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .badRequest(let response): throw APIFailure(badRequest: response)
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .notFound(let response): throw APIFailure(statusCode: 404, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    /// Queues the submit with the draft id as `Idempotency-Key`, so a retry never posts twice.
    /// A draft Procore already has comes back as `.submitted` with the draft refetched.
    func submit(id: UUID, companyID: Int64) async throws -> SubmitStart {
        let output = try await APIFailure.mapping {
            try await client.submitDraft(
                path: .init(draftId: Self.apiID(id)),
                headers: .init(X_hyphen_Company_hyphen_Id: companyID, Idempotency_hyphen_Key: Self.apiID(id))
            )
        }
        switch output {
        case .ok:
            return .submitted(try await get(id: id, companyID: companyID))
        case .accepted(let accepted):
            let body = try await APIFailure.mapping { try accepted.body.json }
            return .running(jobID: body.job_id)
        case .badRequest(let response): throw APIFailure(badRequest: response)
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .notFound(let response): throw APIFailure(statusCode: 404, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    func job(id: String, companyID: Int64) async throws -> JobStatus {
        let output = try await APIFailure.mapping {
            try await client.getJob(path: .init(jobId: id), headers: .init(X_hyphen_Company_hyphen_Id: companyID))
        }
        switch output {
        case .ok(let ok): return try await APIFailure.mapping { try ok.body.json }
        case .unauthorized(let response): throw APIFailure(statusCode: 401, response: response)
        case .forbidden(let response): throw APIFailure(statusCode: 403, response: response)
        case .notFound(let response): throw APIFailure(statusCode: 404, response: response)
        case .badGateway(let response): throw APIFailure(statusCode: 502, response: response)
        case .default(let status, let response): throw APIFailure(statusCode: status, response: response)
        }
    }

    /// Polls the submit job every `poll` until it succeeds or fails, then returns the draft. Once
    /// `timeout` has passed it returns the draft as it stands, still `submitting` if the job runs on.
    /// Counts sleeps rather than reading instants: generic code over `any Clock<Duration>` crashes
    /// Swift 6.4 (see `CaptureModel.tick`).
    func waitForJob(
        id: String,
        draftID: UUID,
        companyID: Int64,
        poll: Duration = .seconds(2),
        timeout: Duration = .seconds(60),
        clock: any Clock<Duration> = ContinuousClock()
    ) async throws -> DraftResponse {
        var waited = Duration.zero
        while waited < timeout {
            let status = try await job(id: id, companyID: companyID).status
            if status == .succeeded || status == .failed { break }
            do {
                try await clock.sleep(for: poll, tolerance: nil)
            } catch {
                throw APIFailure(error)
            }
            waited += poll
        }
        return try await get(id: draftID, companyID: companyID)
    }

    /// The backend's lowercase UUID form, used for path ids and the idempotency key.
    private static func apiID(_ id: UUID) -> String {
        id.uuidString.lowercased()
    }
}
