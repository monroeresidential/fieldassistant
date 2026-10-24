import SwiftUI

enum LookupFilter {
    /// Case-insensitive match on the label or any `extra` value. A blank query returns every row.
    static func filter(_ rows: [LookupRow], query: String) -> [LookupRow] {
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !needle.isEmpty else { return rows }
        return rows.filter { row in
            row.label.localizedCaseInsensitiveContains(needle)
                || (row.extra?.additionalProperties.values.contains { $0.localizedCaseInsensitiveContains(needle) } ?? false)
        }
    }
}

/// Searchable list of one project lookup. Single selection writes `selection`; `multi` writes `selections`.
struct LookupPickerView: View {
    let kind: LookupKind
    let rows: [LookupRow]
    let unavailableReason: String?
    @Binding var selection: Int64?
    let multi: Bool
    let selections: Binding<[Int64]>?

    @State private var query = ""

    init(
        kind: LookupKind,
        rows: [LookupRow],
        unavailableReason: String? = nil,
        selection: Binding<Int64?>,
        multi: Bool = false,
        selections: Binding<[Int64]>? = nil
    ) {
        self.kind = kind
        self.rows = rows
        self.unavailableReason = unavailableReason
        self._selection = selection
        self.multi = multi
        self.selections = selections
    }

    var body: some View {
        Group {
            if let reason = unavailableReason {
                ContentUnavailableView(
                    "Not available on this project",
                    systemImage: "exclamationmark.triangle",
                    description: Text(reason)
                )
            } else {
                list
            }
        }
        .navigationTitle(kind.title)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var list: some View {
        List(LookupFilter.filter(rows, query: query), id: \.id) { row in
            Button { toggle(row.id) } label: {
                HStack(spacing: 12) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(row.label).font(.body)
                        if let subtitle = row.subtitle {
                            Text(subtitle).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    Spacer(minLength: 0)
                    if isSelected(row.id) {
                        Image(systemName: "checkmark")
                            .foregroundStyle(Color.accentColor)
                            .accessibilityHidden(true)
                    }
                }
                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel([row.label, row.subtitle].compactMap { $0 }.joined(separator: ", "))
            .accessibilityAddTraits(isSelected(row.id) ? .isSelected : [])
        }
        .listStyle(.plain)
        .searchable(text: $query, prompt: "Search \(kind.title.lowercased())")
        .overlay {
            if rows.isEmpty {
                ContentUnavailableView("No \(kind.title.lowercased()) on this project", systemImage: "tray")
            } else if LookupFilter.filter(rows, query: query).isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
    }

    private func isSelected(_ id: Int64) -> Bool {
        multi ? (selections?.wrappedValue.contains(id) ?? false) : selection == id
    }

    private func toggle(_ id: Int64) {
        guard multi, let selections else {
            selection = id
            return
        }
        if let index = selections.wrappedValue.firstIndex(of: id) {
            selections.wrappedValue.remove(at: index)
        } else {
            selections.wrappedValue.append(id)
        }
    }
}

#Preview("Single") {
    @Previewable @State var selection: Int64?
    let rows = [
        LookupRow(id: 1, label: "Mike Alvarez", extra: .init(additionalProperties: ["role": "Architect"])),
        LookupRow(id: 2, label: "Mike Brandt", extra: .init(additionalProperties: ["role": "Electrician", "company": "Volt Co"])),
        LookupRow(id: 3, label: "Ana Ruiz"),
    ]
    NavigationStack {
        LookupPickerView(kind: .users, rows: rows, selection: $selection)
    }
}

#Preview("Multi") {
    @Previewable @State var selection: Int64?
    @Previewable @State var picked: [Int64] = [3]
    let rows = [LookupRow(id: 3, label: "Ana Ruiz"), LookupRow(id: 4, label: "Ben West")]
    NavigationStack {
        LookupPickerView(kind: .users, rows: rows, selection: $selection, multi: true, selections: $picked)
    }
}

#Preview("Unavailable") {
    @Previewable @State var selection: Int64?
    NavigationStack {
        LookupPickerView(kind: .specSections, rows: [], unavailableReason: "Procore did not return spec sections.", selection: $selection)
    }
}
