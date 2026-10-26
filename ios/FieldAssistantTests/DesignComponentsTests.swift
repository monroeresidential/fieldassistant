import SwiftUI
import XCTest
@testable import FieldAssistant

@MainActor
final class DesignComponentsTests: XCTestCase {
    func testStatusTagStyleForEveryState() {
        let states: [StatusTag.State] = [
            .submitted(number: "214"), .submitted(number: nil), .queued, .waiting, .draft, .fix,
            .lastUsed, .open, .closed, .custom("Pending"),
        ]
        for state in states {
            let style = StatusTag.style(for: state)
            XCTAssertNotEqual(style.foreground, Color.accentColor, "\(state) falls back to the default accent")
            XCTAssertFalse(style.text.isEmpty, "\(state) has no text")
        }
        XCTAssertEqual(StatusTag.style(for: .submitted(number: "214")).text, "#214 Submitted")
        XCTAssertEqual(StatusTag.style(for: .submitted(number: nil)).text, "Submitted")

        let queued = StatusTag.style(for: .queued)
        XCTAssertEqual(StatusTag.style(for: .open), StatusTag.Style(
            text: "Open", foreground: queued.foreground, background: nil, border: queued.border,
            uppercase: false, tracking: queued.tracking))
        XCTAssertEqual(StatusTag.style(for: .closed).background, StatusTag.style(for: .submitted(number: nil)).background)
        XCTAssertTrue(StatusTag.style(for: .lastUsed).uppercase)
        XCTAssertEqual(StatusTag.style(for: .fix).foreground, Color.white)
    }

    func testDraftStatusMapsToTagState() {
        XCTAssertEqual(StatusTag.state(for: .waiting, number: nil), .waiting)
        XCTAssertEqual(StatusTag.state(for: .draft, number: nil), .draft)
        XCTAssertEqual(StatusTag.state(for: .submitting, number: nil), .queued)
        XCTAssertEqual(StatusTag.state(for: .submitted, number: "214"), .submitted(number: "214"))
        XCTAssertEqual(StatusTag.state(for: .failed, number: nil), .fix)
        // Tag text keeps the draft list's words.
        XCTAssertEqual(StatusTag.style(for: .waiting).text, LocalDraftStatus.waiting.label)
        XCTAssertEqual(StatusTag.style(for: .draft).text, LocalDraftStatus.draft.label)
        XCTAssertEqual(StatusTag.style(for: .queued).text, LocalDraftStatus.submitting.label)
        XCTAssertEqual(StatusTag.style(for: .fix).text, LocalDraftStatus.failed.label)
    }

    func testRFIStatusMapsToTagState() {
        XCTAssertEqual(StatusTag.state(forRFIStatus: "open", draft: false), .open)
        XCTAssertEqual(StatusTag.state(forRFIStatus: "Closed", draft: false), .closed)
        XCTAssertEqual(StatusTag.state(forRFIStatus: "open", draft: true), .draft)
        XCTAssertEqual(StatusTag.state(forRFIStatus: "draft", draft: false), .draft)
        XCTAssertEqual(StatusTag.state(forRFIStatus: "pending", draft: false), .custom("Pending"))
        XCTAssertEqual(StatusTag.state(forRFIStatus: "", draft: false), .custom("Unknown"))
    }

    func testKickerStylesUseTokens() {
        XCTAssertEqual(Kicker.Style.section.textStyle.font, Theme.Text.kicker.font)
        XCTAssertEqual(Kicker.Style.section.textStyle.tracking, Theme.Text.kicker.tracking)
        XCTAssertEqual(Kicker.Style.card.textStyle.font, Theme.Text.cardKicker.font)
        XCTAssertEqual(Kicker.Style.card.textStyle.tracking, Theme.Text.cardKicker.tracking)
        XCTAssertEqual(Kicker.Style.hero.textStyle.font, Theme.Text.heroKicker.font)
        XCTAssertEqual(Kicker.Style.label.textStyle.font, Theme.Text.label.font)
        XCTAssertEqual(Kicker.Style.section.color, Theme.Color.accent)
        XCTAssertEqual(Kicker.Style.label.color, Theme.Color.muted)
        XCTAssertEqual(Kicker.Style.tertiary.color, Theme.Color.tertiary)
    }
}
