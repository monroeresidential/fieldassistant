import FieldAssistantAPI
import HTTPTypes
import OpenAPIRuntime
import XCTest
@testable import FieldAssistant

@MainActor
final class AttachmentViewerModelTests: XCTestCase {
    private let attachment = Components.Schemas.RFIAttachment(
        id: 3, name: "Column photo", filename: "col.jpg", url: "/v1/projects/7/records/rfi/501/attachments/3"
    )

    private func makeModel(_ transport: StubTransport) -> AttachmentViewerModel {
        AttachmentViewerModel(
            attachment: attachment,
            service: RecordService(client: makeClient(transport)),
            companyID: 3, projectID: 7, rfiID: 501
        )
    }

    func testOpenWithoutSignalNeedsSignal() async {
        let transport = StubTransport()
        let model = makeModel(transport)

        await model.open(reachable: false)

        XCTAssertEqual(model.state, .needsSignal)
        XCTAssertTrue(transport.requests.isEmpty)
    }

    func testOpenDownloadsToTempFile() async throws {
        // The contract declares only application/octet-stream; the generated client rejects other types.
        var response = HTTPResponse(status: .ok, headerFields: [.contentType: "application/octet-stream"])
        response.headerFields[.contentDisposition] = #"attachment; filename="col.jpg""#
        let transport = StubTransport([(response, HTTPBody([1, 2, 3]))])
        let model = makeModel(transport)

        await model.open(reachable: true)

        guard case .ready(let url) = model.state else { return XCTFail("state \(model.state)") }
        XCTAssertEqual(url.lastPathComponent, "col.jpg")
        let temp = FileManager.default.temporaryDirectory.standardizedFileURL.path
        XCTAssertTrue(url.standardizedFileURL.path.hasPrefix(temp), url.path)
        XCTAssertEqual(try Data(contentsOf: url), Data([1, 2, 3]))
        XCTAssertEqual(transport.requests.first?.request.path, "/projects/7/records/rfi/501/attachments/3")
    }

    func testOpenShowsFailureMessage() async {
        let transport = StubTransport([APIStub.error(.notFound, code: "not_found", message: "Attachment is gone.")])
        let model = makeModel(transport)

        await model.open(reachable: true)

        XCTAssertEqual(model.state, .failed("Attachment is gone."))
    }

    func testFileNamePrefersReportedThenFilenameThenNameThenID() {
        XCTAssertEqual(AttachmentViewerModel.fileName(reported: "server.jpg", attachment: attachment), "server.jpg")
        XCTAssertEqual(AttachmentViewerModel.fileName(reported: nil, attachment: attachment), "col.jpg")
        let named = Components.Schemas.RFIAttachment(id: 9, name: "plan.pdf", url: "/x")
        XCTAssertEqual(AttachmentViewerModel.fileName(reported: nil, attachment: named), "plan.pdf")
        let unnamed = Components.Schemas.RFIAttachment(id: 9, name: "", url: "/x")
        XCTAssertEqual(AttachmentViewerModel.fileName(reported: nil, attachment: unnamed), "9")
        XCTAssertEqual(AttachmentViewerModel.fileName(reported: "a/b.pdf", attachment: named), "a_b.pdf")
    }
}
