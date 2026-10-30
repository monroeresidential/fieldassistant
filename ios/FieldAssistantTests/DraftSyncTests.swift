import FieldAssistantAPI
import Foundation
import HTTPTypes
import OpenAPIRuntime
import SwiftData
import XCTest
@testable import FieldAssistant

final class DraftSyncTests: XCTestCase {
    private let suiteName = "DraftSyncTests.\(UUID().uuidString)"
    /// Kept alive for the test: `ModelContext` does not own its container.
    private var container: ModelContainer?
    /// Kept alive for the test: the sync's `send` closure holds the state weakly, as `AppState` does.
    private var state: AppState?

    override func tearDown() {
        UserDefaults().removePersistentDomain(forName: suiteName)
        state = nil
        container = nil
        super.tearDown()
    }

    @MainActor
    func testWaitingDraftsResendWhenReachable() async throws {
        let first = waitingDraft(transcript: "first", minutesAgo: 2)
        let second = waitingDraft(transcript: "second", minutesAgo: 1)
        let transport = ScriptedTransport([.response(APIStub.draft(id: first.id)), .response(APIStub.draft(id: second.id))])
        let reachability = FakeReachability(isReachable: false)
        let (_, sync) = makeSync(transport: transport, reachability: reachability, drafts: [first, second])

        sync.start()
        await settle()
        XCTAssertTrue(transport.requests.isEmpty, "no pass while there is no network path")

        reachability.set(true)
        try await waitUntil { first.status == .draft && second.status == .draft }

        XCTAssertEqual(transport.requests.map(\.method), [.post, .post])
        XCTAssertEqual(transport.requests.map(\.path), ["/drafts", "/drafts"])
        XCTAssertNil(first.errorMessage)
        XCTAssertEqual(first.subject, "Header height at grid C")
        sync.stop()
    }

    @MainActor
    func testOfflineFailureStopsPassAndKeepsWaiting() async throws {
        let first = waitingDraft(transcript: "first", minutesAgo: 2)
        let second = waitingDraft(transcript: "second", minutesAgo: 1)
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second])

        await sync.runOnce()

        XCTAssertEqual(transport.requests.count, 1, "the pass stops at the first offline failure")
        XCTAssertEqual(first.status, .waiting)
        XCTAssertEqual(second.status, .waiting)
        XCTAssertNil(first.errorMessage, "no signal is not an error to show on the row")
    }

    @MainActor
    func testServerFailureKeepsDraftWaitingWithMessageAndContinues() async throws {
        let first = waitingDraft(transcript: "first", minutesAgo: 2)
        let second = waitingDraft(transcript: "second", minutesAgo: 1)
        let transport = ScriptedTransport([
            .response(APIStub.error(.badGateway, code: "procore_unavailable", message: "Procore is not responding.")),
            .response(APIStub.draft(id: second.id)),
        ])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second])

        await sync.runOnce()

        XCTAssertEqual(first.status, .waiting)
        XCTAssertEqual(first.errorMessage, "Procore is not responding.")
        XCTAssertEqual(second.status, .draft)
    }

    @MainActor
    func testSubmittingDraftReconcilesOnLaunch() async throws {
        let draft = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "t",
                               capturedAt: .now.addingTimeInterval(-600))
        let folder = try DraftFiles.directory(for: draft.id)
        try Data("audio".utf8).write(to: DraftFiles.audioURL(for: draft.id))
        let transport = ScriptedTransport([.response(APIStub.draft(id: draft.id, status: "submitted"))])
        let (_, sync) = makeSync(transport: transport, drafts: [draft])

        await sync.runOnce()

        XCTAssertEqual(draft.status, .submitted)
        XCTAssertEqual(transport.requests.map(\.method), [.get])
        XCTAssertEqual(transport.requests.first?.path, "/drafts/\(draft.id.uuidString.lowercased())")
        XCTAssertEqual(transport.requests.first?.headerFields[HTTPField.Name("X-Company-Id")!], "7")
        XCTAssertFalse(FileManager.default.fileExists(atPath: folder.path(percentEncoded: false)), "files are removed once Procore has the RFI")
    }

    @MainActor
    func testSubmittingDraftReconcilesToFailedAndStillSubmittingIsLeftAlone() async throws {
        let failed = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "a",
                                capturedAt: .now.addingTimeInterval(-600))
        let running = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "b",
                                 capturedAt: .now.addingTimeInterval(-300))
        let runningFolder = try DraftFiles.directory(for: running.id)
        defer { DraftFiles.removeAll(for: running.id) }
        let transport = ScriptedTransport([
            .response(APIStub.draft(id: failed.id, status: "failed")),
            .response(APIStub.draft(id: running.id, status: "submitting", subject: "Changed on server")),
        ])
        let (_, sync) = makeSync(transport: transport, drafts: [failed, running])

        await sync.runOnce()

        XCTAssertEqual(failed.status, .failed)
        XCTAssertEqual(running.status, .submitting)
        XCTAssertNil(running.subject, "a draft still submitting is left as it was")
        XCTAssertTrue(FileManager.default.fileExists(atPath: runningFolder.path(percentEncoded: false)))
    }

    @MainActor
    func testReconcileSkipsDraftSubmittedMomentsAgo() async throws {
        // Review marks the draft submitting (and stamps it) before its POST reaches the server; a
        // pass in that window would GET `reviewed` and fold the live submit back to `draft`.
        let draft = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "t",
                               capturedAt: .now.addingTimeInterval(-600))
        draft.updatedAt = .now
        let transport = ScriptedTransport([.response(APIStub.draft(id: draft.id, status: "reviewed"))])
        let (_, sync) = makeSync(transport: transport, drafts: [draft])

        await sync.runOnce()

        XCTAssertTrue(transport.requests.isEmpty, "an in-flight submit is not fetched")
        XCTAssertEqual(draft.status, .submitting)
    }

    @MainActor
    func testOfflineLaunchThenReachableSendsWaitingDrafts() async throws {
        UserDefaults(suiteName: suiteName)!.set(NSNumber(value: Int64(42)), forKey: "draftOwnerProcoreUserID")
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let reachability = FakeReachability(isReachable: false)
        let state = makeState(transport: transport, reachability: reachability)
        self.state = state
        let draft = waitingDraft(transcript: "captured with no signal", minutesAgo: 1)
        try state.drafts.insert(draft)

        await state.bootstrap()
        XCTAssertEqual(state.phase, .offline)
        XCTAssertNil(state.draftSync)

        transport.append(.response(APIStub.me(procoreUserID: 42)))
        transport.append(.response(APIStub.draft(id: draft.id)))
        reachability.set(true)
        try await waitUntil { draft.status == .draft }

        XCTAssertEqual(transport.requests.map(\.path), ["/me", "/me", "/drafts"])
        XCTAssertEqual(transport.requests.last?.method, .post)
        guard case .signedIn = state.phase else { return XCTFail("expected signedIn, got \(state.phase)") }
        XCTAssertNotNil(state.draftSync)
    }

    @MainActor
    func testBecomingActiveWhileOfflineRetriesSignIn() async throws {
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet)), .response(APIStub.me(procoreUserID: 42))])
        let state = makeState(transport: transport, reachability: FakeReachability(isReachable: true))
        self.state = state
        await state.bootstrap()
        XCTAssertEqual(state.phase, .offline)

        await state.becameActive()

        guard case .signedIn = state.phase else { return XCTFail("expected signedIn, got \(state.phase)") }
        XCTAssertNotNil(state.draftSync)
    }

    @MainActor
    func testCreateOfflineMarksWaiting() async throws {
        let draft = waitingDraft(transcript: "no signal", minutesAgo: 0)
        let (state, _) = makeSync(transport: FailingTransport(error: URLError(.notConnectedToInternet)), drafts: [draft])

        let outcome = await state.send(draft)

        XCTAssertEqual(outcome, .waiting)
        XCTAssertEqual(draft.status, .waiting)
        XCTAssertNil(draft.errorMessage)
        XCTAssertTrue(state.sendingWaitsForSignal)
    }

    @MainActor
    func testOnlyTheCurrentOwnersDraftsSync() async throws {
        let mine = waitingDraft(transcript: "mine", minutesAgo: 1)
        let theirs = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 43, transcript: "theirs")
        let transport = ScriptedTransport([.response(APIStub.draft(id: mine.id))])
        let (_, sync) = makeSync(transport: transport, drafts: [mine, theirs])

        await sync.runOnce()

        XCTAssertEqual(mine.status, .draft)
        XCTAssertEqual(theirs.status, .waiting)
        XCTAssertEqual(transport.requests.count, 1)
    }

    @MainActor
    func testNoOwnerDoesNothing() async throws {
        let draft = waitingDraft(transcript: "mine", minutesAgo: 1)
        let transport = ScriptedTransport([])
        let (_, sync) = makeSync(transport: transport, drafts: [draft], owner: nil)

        await sync.runOnce()

        XCTAssertTrue(transport.requests.isEmpty)
        XCTAssertEqual(draft.status, .waiting)
    }

    @MainActor
    func testOverlappingRunsCoalesce() async throws {
        let first = waitingDraft(transcript: "first", minutesAgo: 2)
        let second = waitingDraft(transcript: "second", minutesAgo: 1)
        let transport = ScriptedTransport([.response(APIStub.draft(id: first.id)), .response(APIStub.draft(id: second.id))])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second])

        async let a: Void = sync.runOnce()
        async let b: Void = sync.runOnce()
        _ = await (a, b)

        XCTAssertEqual(transport.requests.count, 2, "one POST per draft, not one per trigger")
        XCTAssertEqual(first.status, .draft)
        XCTAssertEqual(second.status, .draft)
    }

    @MainActor
    func testStopCancelsPassWithoutRecordingAnError() async throws {
        let first = waitingDraft(transcript: "first", minutesAgo: 2)
        let second = waitingDraft(transcript: "second", minutesAgo: 1)
        let transport = ScriptedTransport([.hang])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second])

        let run = Task { await sync.runOnce() }
        try await waitUntil { transport.requests.count == 1 }
        sync.stop()
        await run.value

        XCTAssertEqual(transport.requests.count, 1, "a cancelled pass sends nothing more")
        XCTAssertEqual(first.status, .waiting)
        XCTAssertNil(first.errorMessage)
        XCTAssertNil(second.errorMessage)
    }

    @MainActor
    func testSignInStartsSyncAndSignOutStopsIt() async throws {
        let transport = ScriptedTransport([.response(APIStub.me(procoreUserID: 42)), .response((HTTPResponse(status: .noContent), nil))])
        let state = makeState(transport: transport, reachability: FakeReachability(isReachable: true))
        XCTAssertNil(state.draftSync)

        await state.bootstrap()
        XCTAssertNotNil(state.draftSync, "sync starts once /me has loaded")

        await state.signOut()
        XCTAssertNil(state.draftSync, "sync stops when the session ends")
    }

    @MainActor
    func testQueuedDraftRunsWhenReachable() async throws {
        let draft = queuedDraft(transcript: "queued", minutesAgo: 5)
        let transport = ScriptedTransport([
            .response(APIStub.submit(.ok, draftID: draft.id)),
            .response(APIStub.draft(id: draft.id, status: "submitted")),
        ])
        let reachability = FakeReachability(isReachable: false)
        let (_, sync) = makeSync(transport: transport, reachability: reachability, drafts: [draft])

        sync.start()
        await settle()
        XCTAssertTrue(transport.requests.isEmpty, "a queued draft waits for a network path")
        XCTAssertTrue(draft.submitPending)

        reachability.set(true)
        try await waitUntil { draft.status == .submitted }

        let path = "/drafts/\(draft.id.uuidString.lowercased())"
        XCTAssertEqual(transport.requests.map(\.method), [.post, .get])
        XCTAssertEqual(transport.requests.map(\.path), [path + "/submit", path])
        XCTAssertFalse(draft.submitPending)
        sync.stop()
    }

    @MainActor
    func testReconcileSkipsPendingDraft() async throws {
        // A queued draft whose run is in flight (Review started it): the server still says
        // `reviewed` until the POST lands, so reconcile must not fold it back to `draft`.
        let draft = queuedDraft(transcript: "queued", minutesAgo: 10)
        let transport = ScriptedTransport([.hang, .response(APIStub.draft(id: draft.id, status: "reviewed"))])
        let (state, sync) = makeSync(transport: transport, drafts: [draft])
        let run = Task { _ = await state.submitRunner.run(draft) }
        try await waitUntil { transport.requests.count == 1 }
        XCTAssertTrue(sync.isRunning(draftID: draft.id))

        await sync.runOnce()

        XCTAssertEqual(transport.requests.count, 1, "the running draft is neither run again nor fetched")
        XCTAssertEqual(draft.status, .submitting)
        XCTAssertTrue(draft.submitPending)
        run.cancel()
        await run.value
    }

    @MainActor
    func testQueuedDraftOnlyRunsForItsOwner() async throws {
        let mine = queuedDraft(transcript: "mine", minutesAgo: 5)
        let theirs = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 43, status: .submitting, transcript: "theirs",
                                capturedAt: .now.addingTimeInterval(-600))
        theirs.submitPending = true
        let transport = ScriptedTransport([
            .response(APIStub.submit(.ok, draftID: mine.id)),
            .response(APIStub.draft(id: mine.id, status: "submitted")),
        ])
        let (_, sync) = makeSync(transport: transport, drafts: [mine, theirs])

        await sync.runOnce()

        XCTAssertEqual(mine.status, .submitted)
        XCTAssertFalse(mine.submitPending)
        XCTAssertEqual(theirs.status, .submitting)
        XCTAssertTrue(theirs.submitPending)
        XCTAssertEqual(transport.requests.count, 2)
    }

    @MainActor
    func testQueuedRunStopsPassWhenOffline() async throws {
        let first = queuedDraft(transcript: "first", minutesAgo: 10)
        let second = queuedDraft(transcript: "second", minutesAgo: 5)
        // Not queued: a submit the server owns, which reconcile would otherwise fetch.
        let owned = LocalDraft(companyID: 7, projectID: 99, ownerProcoreUserID: 42, status: .submitting, transcript: "owned",
                               capturedAt: .now.addingTimeInterval(-900))
        let transport = ScriptedTransport([.failure(URLError(.notConnectedToInternet))])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second, owned])

        await sync.runOnce()

        XCTAssertEqual(transport.requests.count, 1, "the pass ends at the first offline failure")
        XCTAssertEqual(transport.requests.first?.path, "/drafts/\(first.id.uuidString.lowercased())/submit")
        XCTAssertTrue(first.submitPending)
        XCTAssertTrue(second.submitPending)
        XCTAssertEqual(first.status, .submitting)
        XCTAssertEqual(owned.status, .submitting)
    }

    @MainActor
    func testQueuedServerFailureSkipsToNextDraft() async throws {
        let first = queuedDraft(transcript: "first", minutesAgo: 10)
        let photoID = UUID()
        first.photos.append(LocalPhoto(id: photoID, fileName: "a.jpg", bytes: 4, createdAt: first.capturedAt))
        let folder = try DraftFiles.directory(for: first.id)
        defer { DraftFiles.removeAll(for: first.id) }
        try Data([0xFF, 0xD8, 0xFF, 0xD9]).write(to: folder.appending(path: "a.jpg"))
        let second = queuedDraft(transcript: "second", minutesAgo: 5)
        let transport = ScriptedTransport([
            .response(APIStub.error(.internalServerError, code: "internal", message: "Something broke")),
            .response(APIStub.submit(.ok, draftID: second.id)),
            .response(APIStub.draft(id: second.id, status: "submitted")),
        ])
        let (_, sync) = makeSync(transport: transport, drafts: [first, second])

        await sync.runOnce()

        XCTAssertEqual(transport.requests.map(\.method), [.put, .post, .get])
        XCTAssertTrue(first.submitPending, "the failing draft retries on the next pass")
        XCTAssertEqual(first.status, .submitting)
        XCTAssertNil(first.errorMessage)
        XCTAssertEqual(second.status, .submitted)
        XCTAssertFalse(second.submitPending)
    }

    @MainActor
    func testSignOutCancelsRunningSubmit() async throws {
        let draft = queuedDraft(transcript: "queued", minutesAgo: 5)
        let transport = ScriptedTransport([.hang, .response((HTTPResponse(status: .noContent), nil))])
        let state = makeState(transport: transport, reachability: FakeReachability(isReachable: true))
        self.state = state
        try state.drafts.insert(draft)
        let run = Task { await state.submitRunner.run(draft) == .stillPending(.server) }
        try await waitUntil { transport.requests.count == 1 }
        XCTAssertTrue(state.submitRunner.isRunning(draft.id))

        await state.signOut()

        XCTAssertFalse(state.submitRunner.isRunning(draft.id), "no run continues under the next user")
        let endedPending = await run.value
        XCTAssertTrue(endedPending)
        XCTAssertTrue(draft.submitPending, "the draft stays queued for its owner")
        XCTAssertEqual(draft.status, .submitting)
    }

    // MARK: - Helpers

    /// A draft Submit has queued: `submitting` with the flag set, last touched `minutesAgo`.
    private func queuedDraft(transcript: String, minutesAgo: Double) -> LocalDraft {
        let draft = LocalDraft(
            companyID: 7,
            projectID: 99,
            ownerProcoreUserID: 42,
            status: .submitting,
            transcript: transcript,
            capturedAt: Date.now.addingTimeInterval(-minutesAgo * 60)
        )
        draft.submitPending = true
        return draft
    }

    private func waitingDraft(transcript: String, minutesAgo: Double) -> LocalDraft {
        LocalDraft(
            companyID: 7,
            projectID: 99,
            ownerProcoreUserID: 42,
            transcript: transcript,
            capturedAt: Date.now.addingTimeInterval(-minutesAgo * 60)
        )
    }

    /// An `AppState` holding a valid session over `transport`, with `drafts` stored, and a sync
    /// that sends through `AppState.send` for `owner`.
    @MainActor
    private func makeSync(
        transport: ClientTransport,
        reachability: FakeReachability = FakeReachability(isReachable: true),
        drafts: [LocalDraft],
        owner: Int64? = 42
    ) -> (AppState, DraftSync) {
        let state = makeState(transport: transport, reachability: reachability)
        self.state = state
        for draft in drafts { try! state.drafts.insert(draft) }
        let client: Client = state.client
        let sync = DraftSync(
            store: state.drafts,
            service: { DraftService(client: client) },
            runner: state.submitRunner,
            reachability: reachability,
            owner: { owner },
            send: { [weak state] draft in await state?.send(draft) ?? .waiting }
        )
        return (state, sync)
    }

    @MainActor
    private func makeState(transport: ClientTransport, reachability: FakeReachability) -> AppState {
        let container = self.container ?? (try! AppContainer.makeModelContainer(inMemory: true))
        self.container = container
        return AppState(
            store: InMemorySessionStore(Session(accessToken: "acc1", refreshToken: "ref1", accessExpiresAt: Date().addingTimeInterval(3600))),
            presenter: FakeWebAuthPresenter(error: CancellationError()),
            baseURL: APIStub.baseURL,
            modelContext: container.mainContext,
            transport: transport,
            defaults: UserDefaults(suiteName: suiteName)!,
            reachability: reachability
        )
    }

    /// Lets queued main-actor work run.
    @MainActor
    private func settle() async {
        for _ in 0..<20 { await Task.yield() }
    }

    @MainActor
    private func waitUntil(timeout: Duration = .seconds(2), _ condition: @MainActor () -> Bool) async throws {
        let deadline = ContinuousClock.now.advanced(by: timeout)
        while !condition() {
            guard ContinuousClock.now < deadline else { return XCTFail("condition not met in time") }
            try await Task.sleep(for: .milliseconds(10))
        }
    }
}

/// A `ReachabilityProviding` the test flips by hand.
@MainActor
final class FakeReachability: ReachabilityProviding {
    private(set) var isReachable: Bool
    private var continuations: [UUID: AsyncStream<Bool>.Continuation] = [:]

    init(isReachable: Bool) {
        self.isReachable = isReachable
    }

    var updates: AsyncStream<Bool> {
        let (stream, continuation) = AsyncStream.makeStream(of: Bool.self)
        continuations[UUID()] = continuation
        return stream
    }

    func set(_ reachable: Bool) {
        isReachable = reachable
        for continuation in continuations.values { continuation.yield(reachable) }
    }
}

/// `ClientTransport` that plays a script of responses, thrown errors, or a call that waits until
/// cancelled, recording each request.
final class ScriptedTransport: ClientTransport, @unchecked Sendable {
    enum Step {
        case response((HTTPResponse, HTTPBody?))
        case failure(Error)
        /// Suspends until the calling task is cancelled, then throws `CancellationError`.
        case hang
    }

    private let lock = NSLock()
    private var script: [Step]
    private var recorded: [HTTPRequest] = []

    init(_ script: [Step]) {
        self.script = script
    }

    func append(_ step: Step) {
        lock.withLock { script.append(step) }
    }

    var requests: [HTTPRequest] { lock.withLock { recorded } }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        let step: Step? = lock.withLock {
            recorded.append(request)
            return script.isEmpty ? nil : script.removeFirst()
        }
        switch step {
        case .response(let response): return response
        case .failure(let error): throw error
        case .hang:
            try await Task.sleep(for: .seconds(60))
            throw CancellationError()
        case nil: throw StubTransport.ScriptExhausted()
        }
    }
}
