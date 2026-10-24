import FieldAssistantAPI
import Foundation
import SwiftData

typealias DraftResponse = Components.Schemas.DraftResponse

/// Where a draft stands on the phone. The server's statuses fold into these; `waiting` means the
/// draft has not reached the server yet.
enum LocalDraftStatus: String, Codable, Sendable {
    case waiting, draft, submitting, submitted, failed

    init(server: DraftResponse.statusPayload) {
        switch server {
        case .extracted, .reviewed: self = .draft
        case .submitting: self = .submitting
        case .submitted: self = .submitted
        case .failed: self = .failed
        }
    }

    var label: String {
        switch self {
        case .waiting: "Waiting"
        case .draft: "Draft"
        case .submitting: "Sending"
        case .submitted: "Submitted"
        case .failed: "Fix"
        }
    }
}

/// A captured RFI on this phone, from recording until Procore has it. Audio and photo files live
/// in `DraftFiles.directory(for: id)`.
@Model
final class LocalDraft {
    @Attribute(.unique) var id: UUID
    var companyID: Int64
    var projectID: Int64
    var feature: String
    /// The status's raw value. SwiftData predicates on iOS 17 cannot compare enum properties, so
    /// the string is what is stored and queried; `status` is the typed view of it.
    var statusRaw: String
    var transcript: String
    var capturedAt: Date
    var latitude: Double?
    var longitude: Double?
    @Relationship(deleteRule: .cascade, inverse: \LocalPhoto.draft) var photos: [LocalPhoto]
    /// The last `DraftResponse` from the server, JSON-encoded with `LocalDraft.encoder`.
    var serverData: Data?
    var subject: String?
    var resultNumber: String?
    var resultURL: String?
    var errorMessage: String?
    var updatedAt: Date

    init(
        id: UUID = UUID(),
        companyID: Int64,
        projectID: Int64,
        feature: String = "rfi",
        status: LocalDraftStatus = .waiting,
        transcript: String,
        capturedAt: Date = .now,
        latitude: Double? = nil,
        longitude: Double? = nil,
        photos: [LocalPhoto] = []
    ) {
        self.id = id
        self.companyID = companyID
        self.projectID = projectID
        self.feature = feature
        statusRaw = status.rawValue
        self.transcript = transcript
        self.capturedAt = capturedAt
        self.latitude = latitude
        self.longitude = longitude
        self.photos = photos
        updatedAt = capturedAt
    }

    var status: LocalDraftStatus {
        get { LocalDraftStatus(rawValue: statusRaw) ?? .waiting }
        set { statusRaw = newValue.rawValue }
    }

    /// The last server response, or `nil` before the draft has reached the server.
    var server: DraftResponse? {
        get { serverData.flatMap { try? Self.decoder.decode(DraftResponse.self, from: $0) } }
        set { serverData = newValue.flatMap { try? Self.encoder.encode($0) } }
    }

    /// Records a server response: status, subject, Procore result, any error, and the response itself.
    func apply(_ response: DraftResponse) {
        status = LocalDraftStatus(server: response.status)
        subject = response.payload.subject
        resultNumber = response.result?.number
        resultURL = response.result?.url
        errorMessage = response.error?.message
        server = response
        updatedAt = response.updated_at
    }

    /// Codes `DraftResponse` the way the API sends it: ISO 8601 dates, with or without fractional seconds.
    static var decoder: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            try LenientISO8601DateTranscoder().decode(decoder.singleValueContainer().decode(String.self))
        }
        return decoder
    }

    static var encoder: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .custom { date, encoder in
            var container = encoder.singleValueContainer()
            try container.encode(LenientISO8601DateTranscoder().encode(date))
        }
        return encoder
    }
}
