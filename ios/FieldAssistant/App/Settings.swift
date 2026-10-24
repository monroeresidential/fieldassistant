import Foundation
import Observation

/// The user's preferences on this phone, kept in `UserDefaults`.
@Observable
@MainActor
final class Settings {
    private enum Keys {
        static let submitAsDraft = "submitAsDraft"
    }

    /// Whether submits create the RFI as a Procore draft. `nil` until the user touches the toggle,
    /// so the extracted payload's own value stands until then.
    var submitAsDraft: Bool? {
        didSet {
            if let submitAsDraft {
                defaults.set(submitAsDraft, forKey: Keys.submitAsDraft)
            } else {
                defaults.removeObject(forKey: Keys.submitAsDraft)
            }
        }
    }

    @ObservationIgnored private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        submitAsDraft = (defaults.object(forKey: Keys.submitAsDraft) as? NSNumber)?.boolValue
    }
}
