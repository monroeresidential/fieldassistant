import XCTest
@testable import FieldAssistant

final class RecordButtonStateTests: XCTestCase {
    /// Stop is live only while recording and Build only once recording has stopped; neither while
    /// a build is under way. A control that is not live draws with the surface/tertiary tokens.
    func testRecordControlStateMapsToDisabledStyle() {
        typealias State = CaptureView.ControlState
        let off = State(stopEnabled: false, buildEnabled: false)

        XCTAssertEqual(State(for: .idle, isBuilding: false), off)
        XCTAssertEqual(State(for: .requestingPermission, isBuilding: false), off)
        XCTAssertEqual(State(for: .recording, isBuilding: false), State(stopEnabled: true, buildEnabled: false))
        XCTAssertEqual(State(for: .stopped, isBuilding: false), State(stopEnabled: false, buildEnabled: true))
        XCTAssertEqual(State(for: .denied(.deniedMicrophone), isBuilding: false), off)
        XCTAssertEqual(State(for: .denied(.deniedSpeech), isBuilding: false), off)
        XCTAssertEqual(State(for: .denied(.unsupported), isBuilding: false), off)
        XCTAssertEqual(State(for: .failed("The recognizer stopped."), isBuilding: false), off)

        // Building: finishing the transcript or sending the draft.
        XCTAssertEqual(State(for: .stopped, isBuilding: true), off)
        XCTAssertEqual(State(for: .recording, isBuilding: true), off)
    }
}
