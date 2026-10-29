import SwiftUI

/// The flagged card's reason on the right of the label: 12/600. File scope because `FlaggedCard`
/// is generic and cannot hold a stored static.
private let flaggedReasonStyle = Theme.TextStyle(12, .semibold, relativeTo: .caption)

/// The flagged card around a field that needs the user: a 4 pt orange bar on the left, the flagged
/// background, radius 0 8 8 0, padding 14, the label and the reason in accent on one line.
/// An advisory flag keeps the card's shape without the fill, in quieter colours, labelled "Optional:".
struct FlaggedCard<Content: View>: View {
    let label: String
    let reason: String
    let blocking: Bool
    @ViewBuilder var content: () -> Content


    private var title: String { blocking ? label : "Optional: \(label)" }
    private var tint: Color { blocking ? Theme.Color.accent : Theme.Color.muted }

    var body: some View {
        HStack(spacing: 0) {
            Rectangle()
                .fill(blocking ? Theme.Color.brand : Theme.Color.tertiary)
                .frame(width: 4)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 10) {
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Kicker(title, style: .label, color: tint)
                            .fixedSize()
                        Spacer(minLength: 8)
                        reasonText
                            .multilineTextAlignment(.trailing)
                    }
                    VStack(alignment: .leading, spacing: 4) {
                        Kicker(title, style: .label, color: tint)
                        reasonText
                    }
                }
                .accessibilityElement(children: .combine)
                content()
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(blocking ? Theme.Color.flaggedBackground : Color.clear)
        .clipShape(UnevenRoundedRectangle(bottomTrailingRadius: 8, topTrailingRadius: 8))
        .padding(.vertical, 8)
    }

    private var reasonText: some View {
        Text(reason)
            .textStyle(flaggedReasonStyle)
            .foregroundStyle(tint)
    }
}

/// A field the extraction could not settle: shows why, lists candidates to pick from, and offers a full picker.
/// A blocking flag uses the flagged card; an advisory one is prefixed "Optional:" without the fill.
struct UnresolvedRow: View {
    let label: String
    let field: UnresolvedField
    let kind: LookupKind
    /// Candidates already in the field's value; their radios show filled.
    let selectedIDs: Set<Int64>
    let onChoose: (LookupRow) -> Void
    let onSomeoneElse: () -> Void

    init(
        label: String,
        field: UnresolvedField,
        kind: LookupKind,
        selectedIDs: Set<Int64> = [],
        onChoose: @escaping (LookupRow) -> Void,
        onSomeoneElse: @escaping () -> Void
    ) {
        self.label = label
        self.field = field
        self.kind = kind
        self.selectedIDs = selectedIDs
        self.onChoose = onChoose
        self.onSomeoneElse = onSomeoneElse
    }

    /// A candidate's name: 15/600.
    private static let nameStyle = Theme.TextStyle(15, .semibold, relativeTo: .body)

    static func reasonText(for field: UnresolvedField) -> String {
        switch field.reason {
        case .ambiguous: "More than one match"
        case .not_found: "Not found in the project"
        case .not_in_lookup: "Not in the project list"
        case .date_unparseable: "Could not read the date"
        case .procore_validation, .invalid_value:
            field.message.flatMap { $0.isEmpty ? nil : $0 } ?? "Procore rejected this value"
        }
    }

    var body: some View {
        FlaggedCard(label: label, reason: Self.reasonText(for: field), blocking: field.blocking) {
            if !field.candidates.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(field.candidates, id: \.id) { candidate in
                        candidateRow(candidate)
                    }
                }
            }
            Button(action: onSomeoneElse) {
                Text(kind == .users ? "Someone else…" : "Choose another…")
            }
            .buttonStyle(TextLinkButtonStyle(size: 13))
            .accessibilityLabel(kind == .users ? "Choose someone else for \(label)" : "Choose another for \(label)")
        }
    }

    private func candidateRow(_ candidate: LookupRow) -> some View {
        let selected = selectedIDs.contains(candidate.id)
        return Button { onChoose(candidate) } label: {
            HStack(spacing: 12) {
                Radio(selected: selected)
                VStack(alignment: .leading, spacing: 2) {
                    Text(candidate.label)
                        .textStyle(Self.nameStyle)
                        .foregroundStyle(Theme.Color.textPrimary)
                    if let subtitle = candidate.subtitle {
                        Text(subtitle)
                            .textStyle(Theme.Text.meta12)
                            .foregroundStyle(Theme.Color.muted)
                    }
                }
                Spacer(minLength: 0)
            }
            .frame(maxWidth: .infinity, minHeight: 48, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel([candidate.label, candidate.subtitle].compactMap { $0 }.joined(separator: ", "))
        .accessibilityHint("Selects this for \(label)")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A 22 pt radio: a 2 pt ring, or filled accent with a white centre when selected.
private struct Radio: View {
    let selected: Bool

    var body: some View {
        ZStack {
            if selected {
                Circle().fill(Theme.Color.accent)
                Circle().fill(Color.white).frame(width: 8, height: 8)
            } else {
                Circle().strokeBorder(Theme.Color.radioBorder, lineWidth: 2)
            }
        }
        .frame(width: 22, height: 22)
        .accessibilityHidden(true)
    }
}

#Preview {
    let people = [
        LookupRow(id: 1, label: "Mike Alvarez", extra: .init(additionalProperties: ["role": "Architect", "company": "West Design"])),
        LookupRow(id: 2, label: "Mike Brandt", extra: .init(additionalProperties: ["role": "Electrician"])),
    ]
    return ScrollView {
        VStack(spacing: 0) {
            UnresolvedRow(
                label: "Assignee",
                field: UnresolvedField(field: "assignee", reason: .ambiguous, candidates: people, blocking: true),
                kind: .users,
                selectedIDs: [2],
                onChoose: { _ in }, onSomeoneElse: {}
            )
            UnresolvedRow(
                label: "Location",
                field: UnresolvedField(field: "location", reason: .not_found, candidates: [], blocking: false),
                kind: .locations,
                onChoose: { _ in }, onSomeoneElse: {}
            )
            UnresolvedRow(
                label: "Subject",
                field: UnresolvedField(field: "subject", reason: .procore_validation, message: "Subject can't be blank", candidates: [], blocking: true),
                kind: .locations,
                onChoose: { _ in }, onSomeoneElse: {}
            )
        }
        .padding(.horizontal, 20)
    }
}
