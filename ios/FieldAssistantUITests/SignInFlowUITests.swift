import XCTest

/// Live smoke test of the sign-in chain up to Procore's login page.
/// Needs a running backend at the Dev API_BASE_URL and network access, so it
/// only runs when FA_UI_LIVE=1 is set in the environment; CI skips it.
final class SignInFlowUITests: XCTestCase {
    func testTapSignInReachesProcoreLoginPage() throws {
        try XCTSkipUnless(ProcessInfo.processInfo.environment["FA_UI_LIVE"] == "1",
                          "set FA_UI_LIVE=1 with a backend running to exercise the live sign-in chain")

        let app = XCUIApplication()
        app.launch()

        let button = app.buttons["Sign in with Procore"]
        XCTAssertTrue(button.waitForExistence(timeout: 10), "sign-in button not shown")
        button.tap()

        // iOS asks for consent before opening ASWebAuthenticationSession.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let continueButton = springboard.buttons["Continue"]
        if continueButton.waitForExistence(timeout: 10) {
            continueButton.tap()
        }

        // The web sheet is hosted by a system process; look for Procore's login page text.
        let webHost = XCUIApplication(bundleIdentifier: "com.apple.SafariViewService")
        let loginText = webHost.staticTexts.matching(NSPredicate(format: "label CONTAINS[c] 'log in' OR label CONTAINS[c] 'sign in' OR label CONTAINS[c] 'email'")).firstMatch
        let reached = loginText.waitForExistence(timeout: 30)

        let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        shot.name = "procore-login-sheet"
        shot.lifetime = .keepAlways
        add(shot)

        XCTAssertTrue(reached, "Procore login page did not appear in the web sheet")
    }
}
