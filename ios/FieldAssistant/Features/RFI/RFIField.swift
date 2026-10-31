/// The reviewable fields of an RFI draft. Raw values are the payload's JSON keys, which are also
/// the `field` names the server uses in `unresolved`.
enum RFIField: String, CaseIterable, Sendable {
    case subject
    case questionBody = "question_body"
    case assigneeIds = "assignee_ids"
    case rfiManagerId = "rfi_manager_id"
    case locationId = "location_id"
    case specificationSectionId = "specification_section_id"
    case dueDate = "due_date"
    case drawingNumber = "drawing_number"
    case scheduleImpact = "schedule_impact"
    case costImpact = "cost_impact"
    case distributionIds = "distribution_ids"
    case responsibleContractorId = "responsible_contractor_id"
    case costCodeId = "cost_code_id"
    case `private`
    case draft

    /// The rows the design handoff shows, in its order.
    static let primary: [RFIField] = [
        .subject, .questionBody, .assigneeIds, .rfiManagerId, .locationId, .specificationSectionId,
        .dueDate, .drawingNumber, .scheduleImpact, .costImpact,
    ]

    /// The fields the handoff omits, shown in the "More" group.
    static let more: [RFIField] = [.distributionIds, .responsibleContractorId, .costCodeId, .private, .draft]

    /// Maps a server `unresolved[].field` name. `_form` (a Procore error not tied to one field) and
    /// unknown names give `nil`.
    init?(serverField: String) {
        self.init(rawValue: serverField)
    }

    var label: String {
        switch self {
        case .subject: "Subject"
        case .questionBody: "Question"
        case .assigneeIds: "Assignee"
        case .rfiManagerId: "RFI manager"
        case .locationId: "Location"
        case .specificationSectionId: "Spec section"
        case .dueDate: "Due"
        case .drawingNumber: "Drawing"
        case .scheduleImpact: "Schedule impact"
        case .costImpact: "Cost impact"
        case .distributionIds: "Distribution"
        case .responsibleContractorId: "Responsible contractor"
        case .costCodeId: "Cost code"
        case .private: "Private"
        case .draft: "Submit as Procore draft"
        }
    }

    /// The lookup list a picker for this field draws from, or `nil` for typed fields.
    var lookupKind: LookupKind? {
        switch self {
        case .assigneeIds, .distributionIds: .users
        case .rfiManagerId: .rfiManagers
        case .locationId: .locations
        case .specificationSectionId: .specSections
        case .drawingNumber: .drawings
        case .responsibleContractorId: .vendors
        case .costCodeId: .costCodes
        case .subject, .questionBody, .dueDate, .scheduleImpact, .costImpact, .private, .draft: nil
        }
    }

    /// Whether the field holds a list of ids.
    var isMulti: Bool {
        self == .assigneeIds || self == .distributionIds
    }
}
