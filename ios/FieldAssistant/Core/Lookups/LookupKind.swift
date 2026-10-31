import FieldAssistantAPI

typealias LookupRow = Components.Schemas.LookupRow

/// The project lookup lists the backend caches from Procore. Raw values are the API path segments.
enum LookupKind: String, CaseIterable, Codable, Sendable {
    case users
    case locations
    case specSections = "spec_sections"
    case costCodes = "cost_codes"
    case vendors
    case drawings
    /// The people Procore lets manage RFIs on the project; backs the RFI manager picker.
    case rfiManagers = "rfi_managers"

    var apiValue: Operations.getLookup.Input.Path.kindPayload {
        switch self {
        case .users: .users
        case .locations: .locations
        case .specSections: .spec_sections
        case .costCodes: .cost_codes
        case .vendors: .vendors
        case .drawings: .drawings
        case .rfiManagers: .rfi_managers
        }
    }

    var title: String {
        switch self {
        case .users: "People"
        case .locations: "Locations"
        case .specSections: "Spec sections"
        case .costCodes: "Cost codes"
        case .vendors: "Companies"
        case .drawings: "Drawings"
        case .rfiManagers: "RFI managers"
        }
    }

    /// The title as it reads mid-sentence ("Opens the people to choose from"): lowercased, except
    /// that "RFI" keeps its case.
    var accessibilityNoun: String {
        self == .rfiManagers ? title : title.lowercased()
    }
}

extension LookupRow {
    /// The row's role and company joined with " · ", or `nil` when it has neither.
    var subtitle: String? {
        let parts = ["role", "company"].compactMap { key -> String? in
            guard let value = extra?.additionalProperties[key], !value.isEmpty else { return nil }
            return value
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}
