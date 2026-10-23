import Foundation

/// Build-time configuration read from Info.plist, populated from Config/{Local,Dev,Prod}.xcconfig.
enum AppConfig {
    /// Base URL of the Field Assistant API, including the `/v1` prefix.
    static var apiBaseURL: URL {
        let raw = infoString("API_BASE_URL")
        guard let url = URL(string: raw), url.scheme != nil, url.host != nil else {
            fatalError("API_BASE_URL '\(raw)' in Info.plist is not a valid absolute URL.")
        }
        return url
    }

    /// Which backend this build talks to: `local`, `dev` or `prod`.
    static var environment: String { infoString("API_ENV") }

    /// The app's registered URL scheme; the API redirects sign-in back to `<scheme>://auth`.
    static var urlScheme: String {
        guard let types = Bundle.main.object(forInfoDictionaryKey: "CFBundleURLTypes") as? [[String: Any]],
              let schemes = types.first?["CFBundleURLSchemes"] as? [String],
              let scheme = schemes.first, !scheme.isEmpty else {
            fatalError("CFBundleURLTypes in Info.plist has no URL scheme; set APP_URL_SCHEME in Config/*.xcconfig.")
        }
        return scheme
    }

    /// True for the Dev configuration, which shows a DEV badge.
    static var isDev: Bool { environment == "dev" }

    private static func infoString(_ key: String) -> String {
        guard let raw = Bundle.main.object(forInfoDictionaryKey: key) as? String, !raw.isEmpty else {
            fatalError("\(key) is missing from Info.plist; set it in Config/Local.xcconfig, Config/Dev.xcconfig or Config/Prod.xcconfig.")
        }
        return raw
    }
}
