import FieldAssistantAPI
import Foundation
import OpenAPIRuntime

/// A failure the UI can explain to a field user, built from any error a service call throws.
enum APIFailure: Error, Equatable {
    /// Procore (or the company scope) does not let this user see the data; the message says why.
    case permissionDenied(String)
    /// The session is no longer valid and the user has to sign in again.
    case reauthRequired
    /// The device could not reach the server.
    case offline
    /// The server refused the request as it stands (a 400 on a draft route), with its message to show.
    case validation(String)
    /// Anything else, with a message to show.
    case server(String)

    /// Maps thrown errors: `APIFailure` passes through, `ClientError` is unwrapped,
    /// connectivity `URLError`s become `.offline`, and a rejected refresh means signing in again.
    init(_ error: Error) {
        switch error {
        case let failure as APIFailure:
            self = failure
        case let clientError as ClientError:
            self = APIFailure(clientError.underlyingError)
        case is AuthRefreshError:
            self = .reauthRequired
        case let urlError as URLError where Self.offlineCodes.contains(urlError.code):
            self = .offline
        default:
            self = .server(error.localizedDescription)
        }
    }

    /// Maps a non-success response by its `Error.code`, falling back to the status code.
    init(statusCode: Int, response: Components.Responses._Error) {
        let payload = try? response.body.json
        let message = payload?.message ?? HTTPURLResponse.localizedString(forStatusCode: statusCode)
        switch payload?.code {
        case "reauth_required":
            self = .reauthRequired
        case "procore_permission_denied", "forbidden_scope":
            self = .permissionDenied(message)
        case .some:
            self = statusCode == 401 ? .reauthRequired : .server(message)
        case .none:
            switch statusCode {
            case 401: self = .reauthRequired
            case 403: self = .permissionDenied(message)
            default: self = .server(message)
            }
        }
    }

    /// Maps a 400 from a draft route: `bad_request` carries a message the review screen shows as is.
    init(badRequest response: Components.Responses._Error) {
        if let payload = try? response.body.json, payload.code == "bad_request" {
            self = .validation(payload.message)
        } else {
            self = APIFailure(statusCode: 400, response: response)
        }
    }

    /// Runs a generated-client call or body read, rethrowing any error as `APIFailure`.
    /// Runs on the caller's actor, so main-actor callers can pass closures that touch their state.
    static func mapping<T>(
        isolation: isolated (any Actor)? = #isolation,
        _ operation: () async throws -> T
    ) async throws -> T {
        do {
            return try await operation()
        } catch {
            throw APIFailure(error)
        }
    }

    private static let offlineCodes: Set<URLError.Code> = [
        .notConnectedToInternet, .timedOut, .cannotConnectToHost, .networkConnectionLost,
    ]
}
