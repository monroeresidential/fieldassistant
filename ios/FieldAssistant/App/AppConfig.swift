import Foundation

/// Build-time configuration read from Info.plist, populated from Config/*.xcconfig.
enum AppConfig {
    /// Base URL of the Field Assistant API, including the `/v1` prefix.
    static var apiBaseURL: URL {
        guard let raw = Bundle.main.object(forInfoDictionaryKey: "API_BASE_URL") as? String,
              !raw.isEmpty else {
            fatalError("API_BASE_URL is missing from Info.plist; set it in Config/Dev.xcconfig or Config/Prod.xcconfig.")
        }
        guard let url = URL(string: raw), url.scheme != nil, url.host != nil else {
            fatalError("API_BASE_URL '\(raw)' in Info.plist is not a valid absolute URL.")
        }
        return url
    }
}
