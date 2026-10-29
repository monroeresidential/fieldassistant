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
                FailureMessage(title: "Not available on this project", message: reason)
                    .frame(maxHeight: .infinity, alignment: .top)
                    .padding(.top, 24)
            } else {
                list
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.Color.ground.ignoresSafeArea())
        .designNavigationTitle(kind.title)
    }

    private var list: some View {
        let shown = LookupFilter.filter(rows, query: query)
        return VStack(spacing: 0) {
            SearchField("Search \(kind.title.lowercased())", text: $query)
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 10)
            List {
                ForEach(shown, id: \.id) { row in
                    Button { toggle(row.id) } label: {
                        LookupPickerRow(row: row, selected: isSelected(row.id))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel([row.label, row.subtitle].compactMap { $0 }.joined(separator: ", "))
                    .accessibilityAddTraits(isSelected(row.id) ? .isSelected : [])
                    .listRowInsets(EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20))
                    .listRowSeparatorTint(Theme.Color.hairline)
                    .alignmentGuide(.listRowSeparatorTrailing) { $0.width }
                    .listRowBackground(Theme.Color.ground)
                }
                if rows.isEmpty {
                    emptyMessage(Kicker("No \(kind.title.lowercased()) on this project"))
                } else if shown.isEmpty {
                    emptyMessage(FailureMessage(title: "No results for \u{201C}\(query)\u{201D}",
                                                message: "Check the spelling or try a new search."))
                }
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .scrollDismissesKeyboard(.immediately)
        }
    }

    /// An empty-list message placed in the list: centred, no rules.
    private func emptyMessage(_ content: some View) -> some View {
        content
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 20)
            .listRowInsets(EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20))
            .listRowSeparator(.hidden)
            .listRowBackground(Theme.Color.ground)
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

/// One lookup choice: 17/600 name over a 12 pt muted subtitle, min height 60, accent checkmark when chosen.
private struct LookupPickerRow: View {
    let row: LookupRow
    let selected: Bool

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(row.label)
                    .textStyle(Theme.Text.rowTitleStrong)
                    .foregroundStyle(Theme.Color.textPrimary)
                if let subtitle = row.subtitle {
                    Text(subtitle)
                        .textStyle(Theme.Text.meta12)
                        .foregroundStyle(Theme.Color.muted)
                }
            }
            Spacer(minLength: 0)
            if selected {
                Image(systemName: "checkmark")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(Theme.Color.accent)
                    .accessibilityHidden(true)
            }
        }
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, minHeight: 60, alignment: .leading)
        .contentShape(Rectangle())
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
