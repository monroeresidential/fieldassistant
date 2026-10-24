import Foundation
import HTTPTypes
import OpenAPIRuntime

/// Sends `If-None-Match` verbatim.
///
/// The generated client encodes header values in URI "simple" style, so an entity tag's quotes leave
/// as `%22`. The backend compares `If-None-Match` to its quoted `ETag` byte for byte, so without this
/// a cached lookup list would never revalidate to a 304.
struct IfNoneMatchMiddleware: ClientMiddleware {
    func intercept(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String,
        next: @Sendable (HTTPRequest, HTTPBody?, URL) async throws -> (HTTPResponse, HTTPBody?)
    ) async throws -> (HTTPResponse, HTTPBody?) {
        var request = request
        if let value = request.headerFields[.ifNoneMatch], let decoded = value.removingPercentEncoding {
            request.headerFields[.ifNoneMatch] = decoded
        }
        return try await next(request, body, baseURL)
    }
}
