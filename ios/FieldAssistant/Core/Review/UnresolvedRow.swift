import SwiftUI

/// A field the extraction could not settle: shows why, lists candidates to pick from, and offers a full picker.
/// A blocking flag uses the flagged style; an advisory one is prefixed "Optional:" in the secondary colour.
struct UnresolvedRow: View {
    let label: String
    let field: UnresolvedField
    let kind: LookupKind
    let onChoose: (LookupRow) -> Void
    let onSomeoneElse: () -> Void

    init(
        label: String,
        field: UnresolvedField,
        kind: LookupKind = .users,
        onChoose: @escaping (LookupRow) -> Void,
        onSomeoneElse: @escaping () -> Void
    ) {
        self.label = label
        self.field = field
        self.kind = kind
        self.onChoose = onChoose
        self.onSomeoneElse = onSomeoneElse
    }

    private var blocking: Bool { field.blocking }
    private var accent: Color { blocking ? .orange : .secondary }
    private var title: String { blocking ? label : "Optional: \(label)" }

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
        VStack(spacing: 0) {
            HStack(spacing: 0) {
                Rectangle().fill(accent).frame(width: 4)
                VStack(alignment: .leading, spacing: 4) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(title.uppercased())
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(accent)
                        Spacer(minLength: 8)
                        Text(Self.reasonText(for: field))
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.trailing)
                    }
                    .accessibilityElement(children: .combine)
                    ForEach(field.candidates, id: \.id) { candidate in
                        candidateRow(candidate)
                    }
                    Button(action: onSomeoneElse) {
                        Text(kind == .users ? "Someone else…" : "Choose another…")
                            .font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(blocking ? Color.orange : Color.accentColor)
                    .accessibilityLabel(kind == .users ? "Choose someone else for \(label)" : "Choose another for \(label)")
                }
                .padding(.vertical, 8)
                .padding(.horizontal, 12)
            }
            .background(blocking ? Color.orange.opacity(0.12) : Color.clear)
            .clipShape(UnevenRoundedRectangle(bottomTrailingRadius: 8, topTrailingRadius: 8))
            Divider()
        }
    }

    private func candidateRow(_ candidate: LookupRow) -> some View {
        Button { onChoose(candidate) } label: {
            HStack(spacing: 12) {
                Image(systemName: "circle")
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 2) {
                    Text(candidate.label).font(.body.weight(.semibold))
                    if let subtitle = candidate.subtitle {
                        Text(subtitle).font(.caption).foregroundStyle(.secondary)
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
    }
}
