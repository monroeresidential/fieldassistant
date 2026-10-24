import Foundation
import OpenAPIRuntime

/// Reads RFC 3339 timestamps with or without fractional seconds, since the Go backend sends both,
/// and writes them with fractional seconds. The generated client's default rejects the fractional form.
struct LenientISO8601DateTranscoder: DateTranscoder {
    private static let fractional = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
    private static let whole = Date.ISO8601FormatStyle()

    func encode(_ date: Date) throws -> String {
        Self.fractional.format(date)
    }

    func decode(_ text: String) throws -> Date {
        if let date = try? Self.fractional.parse(text) { return date }
        if let date = try? Self.whole.parse(text) { return date }
        throw DecodingError.dataCorrupted(.init(codingPath: [], debugDescription: "Not an RFC 3339 date: \(text)"))
    }
}

extension Configuration {
    /// The configuration every app `Client` is built with.
    static let fieldAssistant = Configuration(dateTranscoder: LenientISO8601DateTranscoder())
}
