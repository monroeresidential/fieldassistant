import CryptoKit
import Foundation
import HTTPTypes
import OpenAPIRuntime
@testable import FieldAssistant

/// Scripted `WebAuthPresenter` that returns a fixed callback URL or throws, and records what it was asked to open.
final class FakeWebAuthPresenter: WebAuthPresenter, @unchecked Sendable {
    private let lock = NSLock()
    private let result: Result<URL, Error>
    private var opened: [(url: URL, scheme: String)] = []

    init(callback: URL) { result = .success(callback) }
    init(error: Error) { result = .failure(error) }

    var openedURLs: [URL] { lock.withLock { opened.map(\.url) } }
    var callbackSchemes: [String] { lock.withLock { opened.map(\.scheme) } }

    func authenticate(url: URL, callbackScheme: String) async throws -> URL {
        lock.withLock { opened.append((url, callbackScheme)) }
        return try result.get()
    }
}

enum APIStub {
    static let baseURL = URL(string: "https://api.example.com/v1")!

    /// A JSON response the generated client will decode.
    static func json(_ status: HTTPResponse.Status, _ body: String) -> (HTTPResponse, HTTPBody?) {
        (HTTPResponse(status: status, headerFields: [.contentType: "application/json"]), HTTPBody(body))
    }

    static func authStartOK(authorizeURL: String = "https://login.procore.com/oauth/authorize?x=1") -> (HTTPResponse, HTTPBody?) {
        json(.ok, #"{"authorize_url":"\#(authorizeURL)","state":"st1"}"#)
    }

    static func tokenPair(access: String, refresh: String, expiresIn: Int = 3600) -> (HTTPResponse, HTTPBody?) {
        json(.ok, #"{"access_token":"\#(access)","refresh_token":"\#(refresh)","expires_in":\#(expiresIn)}"#)
    }

    static func error(_ status: HTTPResponse.Status, code: String = "err", message: String = "failed") -> (HTTPResponse, HTTPBody?) {
        json(status, #"{"code":"\#(code)","message":"\#(message)"}"#)
    }

    static func me(
        procoreUserID: Int64 = 42,
        reauthRequired: Bool = false,
        lastCompanyID: Int64? = nil,
        lastProjectID: Int64? = nil
    ) -> (HTTPResponse, HTTPBody?) {
        var fields = [
            #""id":"6f1c2c1e-6a43-4f7e-9a59-2a0f3b9f1d11""#,
            #""procore_user_id":\#(procoreUserID)"#,
            #""email":"pat@example.com""#,
            #""name":"Pat Field""#,
            #""reauth_required":\#(reauthRequired)"#,
        ]
        if let lastCompanyID { fields.append(#""last_company_id":\#(lastCompanyID)"#) }
        if let lastProjectID { fields.append(#""last_project_id":\#(lastProjectID)"#) }
        return json(.ok, "{" + fields.joined(separator: ",") + "}")
    }

    /// A 200 lookup list with an `ETag`. `rows` is the JSON array body of `rows`.
    static func lookup(
        kind: String = "users",
        rows: String = "[]",
        etag: String = #""v1""#,
        unavailable: Bool = false,
        reason: String? = nil
    ) -> (HTTPResponse, HTTPBody?) {
        var fields = [
            #""kind":"\#(kind)""#,
            #""rows":\#(rows)"#,
            #""unavailable":\#(unavailable)"#,
            #""fetched_at":"2026-10-24T10:00:00Z""#,
        ]
        if let reason { fields.append(#""reason":"\#(reason)""#) }
        var response = HTTPResponse(status: .ok, headerFields: [.contentType: "application/json"])
        response.headerFields[.eTag] = etag
        return (response, HTTPBody("{" + fields.joined(separator: ",") + "}"))
    }

    /// Decodes a recorded JSON request body into a string dictionary.
    static func jsonObject(_ bytes: [UInt8]?) throws -> [String: Any] {
        let data = Data(bytes ?? [])
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// Base64url without padding of SHA-256(`verifier`'s UTF-8 bytes).
    static func challenge(for verifier: String) -> String {
        Data(SHA256.hash(data: Data(verifier.utf8)))
            .base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
