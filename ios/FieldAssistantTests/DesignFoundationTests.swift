import SwiftUI
import UIKit
import XCTest
@testable import FieldAssistant

final class DesignFoundationTests: XCTestCase {
    func testPublicSansIsRegistered() {
        for name in ["PublicSans-Regular", "PublicSans-Medium", "PublicSans-SemiBold", "PublicSans-Bold"] {
            XCTAssertNotNil(UIFont(name: name, size: 17), "\(name) is not registered")
        }
    }

    func testTokensResolveInDark() {
        let dark = UITraitCollection(userInterfaceStyle: .dark)
        let light = UITraitCollection(userInterfaceStyle: .light)

        assertColor(Theme.Color.ground, in: dark, equals: 0x2F3437)
        assertColor(Theme.Color.accent, in: dark, equals: 0xF26122)
        assertColor(Theme.Color.surface, in: dark, equals: 0x3A4044)
        assertColor(Theme.Color.muted, in: dark, equals: 0xB6BCC0)
        assertColor(Theme.Color.accentTint, in: dark, equals: 0x3A4044)
        assertColor(Theme.Color.ground, in: light, equals: 0xFFFFFF)
        assertColor(Theme.Color.accent, in: light, equals: 0xC2410C)
        assertColor(Theme.Color.muted, in: light, equals: 0x5C6368)
        assertColor(Theme.Color.accentTint, in: light, equals: 0xFEF0E8)
        // Review's count tag keeps the deep accent on charcoal (handoff 1m).
        assertColor(Theme.Color.tagFillOnDark, in: dark, equals: 0xC2410C)
        assertColor(Theme.Color.radioBorder, in: dark, equals: 0x8A9196)
        assertColor(Theme.Color.radioBorder, in: light, equals: 0xB6BCC0)
    }

    @MainActor
    func testTypeStylesScaleWithDynamicType() {
        // Theme.Text.body is Public Sans Regular 15 relative to .body: lay a line out in its font at
        // two text sizes and compare the heights.
        func height(at size: DynamicTypeSize) -> CGFloat {
            let line = Text("Ag").font(Theme.Text.body.font).fixedSize().environment(\.dynamicTypeSize, size)
            return UIHostingController(rootView: line)
                .sizeThatFits(in: CGSize(width: CGFloat.greatestFiniteMagnitude, height: .greatestFiniteMagnitude))
                .height
        }
        let large = height(at: .large)
        let huge = height(at: .accessibility3)

        // At the default size the line is Public Sans's natural height for 15 pt.
        XCTAssertEqual(large, 15 * Theme.TextStyle.naturalLineHeight, accuracy: 1.5)
        XCTAssertGreaterThan(huge, large * 1.5)
    }

    func testKickerTrackingIsFourteenPercent() {
        XCTAssertEqual(Theme.Text.kicker.tracking, 11 * 0.14, accuracy: 0.01)
        XCTAssertEqual(Theme.Text.cardKicker.tracking, 10 * 0.12, accuracy: 0.01)
    }

    func testLineSpacingHitsHandoffLineHeights() throws {
        let font = try XCTUnwrap(UIFont(name: Font.publicSansName(for: .regular), size: 15))
        XCTAssertEqual(font.lineHeight / font.pointSize, Theme.TextStyle.naturalLineHeight, accuracy: 0.01)

        XCTAssertEqual(Theme.Text.hero.lineSpacing, 56 * (0.98 - 1.175), accuracy: 0.2)  // about -10.9
        XCTAssertEqual(Theme.Text.body.lineSpacing, 15 * (1.55 - 1.175), accuracy: 0.2)  // about 5.6
        XCTAssertEqual(Theme.Text.transcript.lineSpacing, 24 * (1.35 - 1.175), accuracy: 0.2)
        XCTAssertEqual(Theme.Text.rowTitle.lineSpacing, 0)
    }

    private func assertColor(_ color: SwiftUI.Color, in traits: UITraitCollection, equals hex: UInt32,
                             file: StaticString = #filePath, line: UInt = #line) {
        let resolved = UIColor(color).resolvedColor(with: traits)
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        XCTAssertTrue(resolved.getRed(&r, green: &g, blue: &b, alpha: &a), file: file, line: line)
        XCTAssertEqual(r, CGFloat((hex >> 16) & 0xFF) / 255, accuracy: 0.002, file: file, line: line)
        XCTAssertEqual(g, CGFloat((hex >> 8) & 0xFF) / 255, accuracy: 0.002, file: file, line: line)
        XCTAssertEqual(b, CGFloat(hex & 0xFF) / 255, accuracy: 0.002, file: file, line: line)
        XCTAssertEqual(a, 1, accuracy: 0.002, file: file, line: line)
    }
}
