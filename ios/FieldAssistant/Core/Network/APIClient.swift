import Foundation
import FieldAssistantAPI
import OpenAPIRuntime
import OpenAPIURLSession

/// Builds the generated API `Client` with the auth middleware attached.
enum APIClient {
    static func make(baseURL: URL, middleware: AuthMiddleware) -> Client {
        Client(serverURL: baseURL, configuration: .fieldAssistant, transport: URLSessionTransport(), middlewares: [middleware])
    }
}
