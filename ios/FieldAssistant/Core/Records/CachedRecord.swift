import FieldAssistantAPI
import Foundation
import SwiftData

typealias RecordPage = Components.Schemas.RecordPage
typealias RFIRecord = Components.Schemas.RFIRecord
typealias PullStatus = Components.Schemas.PullStatus

/// The record kinds the project mirror serves. RFIs are the first.
enum RecordKind: String, Sendable {
    case rfi
}

/// One mirrored project record. The full projection is kept as JSON in `bodyData`;
/// the flat columns exist so lists can sort and filter without decoding it.
@Model
final class CachedRecord {
    /// `"<projectID>/<kind>/<procoreID>"`. Derived because `#Unique` over several fields needs iOS 18.
    @Attribute(.unique) var key: String
    var projectID: Int64
    var kind: String
    var procoreID: Int64
    var changeSeq: Int64
    var updatedAt: Date
    var bodyData: Data
    var number: String
    /// The first run of digits in `number`, 0 when it has none, so "12" sorts after "9".
    var numberSort: Int64
    var subject: String
    var status: String
    var ballInCourt: String
    var dueDate: Date?

    init(
        projectID: Int64, kind: RecordKind, procoreID: Int64, changeSeq: Int64, updatedAt: Date, bodyData: Data,
        number: String, subject: String, status: String, ballInCourt: String, dueDate: Date?
    ) {
        key = Self.key(projectID: projectID, kind: kind, procoreID: procoreID)
        self.projectID = projectID
        self.kind = kind.rawValue
        self.procoreID = procoreID
        self.changeSeq = changeSeq
        self.updatedAt = updatedAt
        self.bodyData = bodyData
        self.number = number
        self.numberSort = Self.numberSort(for: number)
        self.subject = subject
        self.status = status
        self.ballInCourt = ballInCourt
        self.dueDate = dueDate
    }

    /// The decoded RFI projection, or nil when `bodyData` does not decode.
    var rfi: RFIRecord? {
        try? Self.decoder.decode(RFIRecord.self, from: bodyData)
    }

    static func key(projectID: Int64, kind: RecordKind, procoreID: Int64) -> String {
        "\(projectID)/\(kind.rawValue)/\(procoreID)"
    }

    private static func numberSort(for number: String) -> Int64 {
        let digits = number.drop { !$0.isASCII || !$0.isNumber }.prefix { $0.isASCII && $0.isNumber }
        return Int64(digits) ?? 0
    }

    /// Matches the generated client's JSON coding, which writes dates as RFC 3339.
    private static var decoder: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }
}
