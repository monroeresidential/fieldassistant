import XCTest
@testable import FieldAssistant

final class LookupPickerFilterTests: XCTestCase {
    private let rows = [
        LookupRow(id: 1, label: "Mike Alvarez", extra: .init(additionalProperties: ["role": "Architect", "company": "West Design"])),
        LookupRow(id: 2, label: "Mike Brandt", extra: .init(additionalProperties: ["role": "Electrician"])),
        LookupRow(id: 3, label: "Level 2"),
    ]

    func testFilterMatchesLabelAndExtraCaseInsensitive() {
        XCTAssertEqual(LookupFilter.filter(rows, query: "mIkE").map(\.id), [1, 2])
        XCTAssertEqual(LookupFilter.filter(rows, query: "architect").map(\.id), [1])
        XCTAssertEqual(LookupFilter.filter(rows, query: "WEST").map(\.id), [1])
        XCTAssertEqual(LookupFilter.filter(rows, query: "level").map(\.id), [3])
        XCTAssertTrue(LookupFilter.filter(rows, query: "zzz").isEmpty)
    }

    func testEmptyQueryReturnsAll() {
        XCTAssertEqual(LookupFilter.filter(rows, query: "").map(\.id), [1, 2, 3])
        XCTAssertEqual(LookupFilter.filter(rows, query: "  ").map(\.id), [1, 2, 3])
    }

    /// The picker rows' VoiceOver hint keeps the RFI acronym and lowercases the other titles.
    func testAccessibilityNounKeepsAcronym() {
        XCTAssertEqual(LookupKind.rfiManagers.accessibilityNoun, "RFI managers")
        XCTAssertEqual(LookupKind.users.accessibilityNoun, "people")
        XCTAssertEqual(LookupKind.specSections.accessibilityNoun, "spec sections")
    }
}
