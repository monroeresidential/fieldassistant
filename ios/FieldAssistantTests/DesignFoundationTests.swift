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
        assertColor(Theme.Color.ground, in: light, equals: 0xFFFFFF)
        assertColor(Theme.Color.accent, in: light, equals: 0xC2410C)
    }

    func testTypeStylesScaleWithDynamicType() throws {
        // Theme.Text.body is Public Sans Regular 15 relative to .body. SwiftUI does not expose the
        // UIFont behind a Font, so apply the same UIFontMetrics scaling that relativeTo: uses.
        _ = Theme.Text.body
        let base = try XCTUnwrap(UIFont(name: Font.publicSansName(for: .regular), size: 15))
        let metrics = UIFontMetrics(forTextStyle: .body)

        let large = metrics.scaledFont(for: base, compatibleWith: UITraitCollection(preferredContentSizeCategory: .large))
        let huge = metrics.scaledFont(for: base, compatibleWith: UITraitCollection(preferredContentSizeCategory: .accessibilityExtraLarge))

        XCTAssertEqual(large.pointSize, 15, accuracy: 0.01)
        XCTAssertGreaterThan(huge.pointSize, 15)
    }

    func testKickerTrackingIsFourteenPercent() {
        XCTAssertEqual(Theme.Text.kicker.tracking, 11 * 0.14, accuracy: 0.01)
        XCTAssertEqual(Theme.Text.cardKicker.tracking, 10 * 0.12, accuracy: 0.01)
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
