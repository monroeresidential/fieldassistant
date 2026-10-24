# Milestone 2 iOS: Capture, Review and Submit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A user taps Record on the home screen, dictates an RFI with a live on-device transcript and photos, taps Build RFI, fixes the flagged fields on the review screen, taps Submit, and sees the Procore RFI number and link.

**Architecture:** Four new Core modules (`Capture`, `Drafts`, `Lookups`, `Review`) and an extended `Features/RFI`. Drafts and cached lookups live in SwiftData. All network calls go through the generated `FieldAssistantAPI` client (contract 1.4.0 from the backend plan) via two thin services, `LookupService` and `DraftService`, that map errors to the existing `APIFailure`. Screens use plain native SwiftUI with the handoff's structure and copy; the handoff's visual design is a later pass.

**Tech Stack:** Swift 6 strict concurrency, SwiftUI, SwiftData, `Speech` (`SFSpeechRecognizer`, on-device), `AVFoundation`, `PhotosUI`, `CoreLocation`, `Network` (`NWPathMonitor`), XCTest; XcodeGen project; `swift-openapi-generator` client.

**Spec:** `docs/superpowers/specs/2026-10-24-milestone-2-rfi-extract-review-design.md` (sections Decided: iPhone, Non-goals, Decided: verification and Drew's steps) and the screen structure in `docs/design/2026-10-23-ios-handoff/README.md` (screens 1c, 1e, 1f, 1g, 1h, 1j; structure, copy and states only).

## Global Constraints

- Depends on the backend plan through Task 5 (contract 1.4.0 synced into `ios/Packages/FieldAssistantAPI/.../openapi.yaml`). Start this plan on branch `milestone-2` after that commit exists.
- Swift tools `6.0`, `SWIFT_STRICT_CONCURRENCY: complete`, iOS deployment target `17.0`; the Xcode project is generated (`make ios-project`), never committed.
- Tests: `cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile`; a single class with `-only-testing:FieldAssistantTests/<Class>`. Pass signal is the exit code (and `** TEST SUCCEEDED **` without `-quiet`).
- On-device recognition only: `SFSpeechRecognizer.supportsOnDeviceRecognition` must be true and `request.requiresOnDeviceRecognition = true`; audio never leaves the device.
- Audio files and photos live under the app's Application Support directory, `drafts/<draft id>/`; the folder is deleted when the draft reaches `submitted`.
- Photos are captured and stored but never uploaded in this milestone.
- Draft statuses on the phone: `waiting`, `draft`, `submitting`, `submitted`, `failed`. Server statuses map: `extracted`/`reviewed` → `draft`, `submitting` → `submitting`, `submitted` → `submitted`, `failed` → `failed`.
- `#if DEBUG` guards the "Type instead" control (Local and Dev are debug configurations; Prod is release).
- Every tappable control keeps a minimum 44 pt hit target and an accessibility label, as the existing screens do.
- Only `AuthRefreshError.rejected` signs the user out; any other failure keeps the session (existing rule).
- Submit polls `GET /jobs/{id}` every 2 s for up to 60 s, then falls back to `GET /drafts/{id}`.
- Commit after every task with the message given in the task.

## Review Focus

1. **Microphone or speech permission is denied.** Capture must show a message with a Settings link and never crash or record silently. Test in Task 3 (`testStartWithoutPermissionReportsDenied`).
2. **Build RFI with no network.** The draft must be saved as `waiting`, the user returned home with the banner, and the draft sent automatically when connectivity returns or when opened. Test in Task 4 (`testCreateOfflineMarksWaiting`) and Task 8 (`testWaitingDraftsResendWhenReachable`).
3. **The user chooses a candidate for a flagged field.** The flag must clear, the count in the title must drop, and the payload must carry that ID. Test in Task 6 (`testChoosingCandidateClearsFlagAndSetsID`).
4. **Submit returns 422.** The review screen must flag the named rows with Procore's message and the draft must not show as submitted. Test in Task 6 (`testSubmit422ReflagsRows`).
5. **The app is killed while a submit job is running.** On relaunch the draft must reconcile from the server (`submitted` or `failed`) rather than stay `submitting` forever. Test in Task 8 (`testSubmittingDraftReconcilesOnLaunch`).

---

### Task 1: SwiftData container and cached lookups

**Files:**
- Create: `ios/FieldAssistant/App/AppContainer.swift`, `ios/FieldAssistant/Core/Lookups/LookupKind.swift`, `Core/Lookups/CachedLookup.swift`, `Core/Lookups/LookupService.swift`, `Core/Lookups/LookupStore.swift`
- Test: `ios/FieldAssistantTests/LookupStoreTests.swift`, `FieldAssistantTests/LookupServiceTests.swift`
- Modify: `ios/FieldAssistant/App/FieldAssistantApp.swift` (inject the container), `FieldAssistantTests/Support/APITestSupport.swift` (add `APIStub.lookup(...)`)

**Interfaces:**
- `enum AppContainer { static func makeModelContainer(inMemory: Bool = false) throws -> ModelContainer }` with schema `[CachedLookup.self, LocalDraft.self, LocalPhoto.self]` (the draft models arrive in Task 2; register them here when that task lands).
- `enum LookupKind: String, CaseIterable, Codable, Sendable { case users, locations, specSections = "spec_sections", costCodes = "cost_codes", vendors, drawings }` with `var apiValue: Operations.getLookup.Input.Path.kindPayload` and `var title: String` ("People", "Locations", "Spec sections", "Cost codes", "Companies", "Drawings").
- `typealias LookupRow = Components.Schemas.LookupRow`; `extension LookupRow { var subtitle: String? }` joining `extra.role` and `extra.company` with " · " when present.
- `@Model final class CachedLookup { var projectID: Int64; var kind: String; var etag: String; var rowsData: Data; var unavailable: Bool; var reason: String?; var fetchedAt: Date }` with `var rows: [LookupRow]` decoding `rowsData` (JSONDecoder) and a unique composite via `#Unique<CachedLookup>([\.projectID, \.kind])`.
- `struct LookupService { let client: Client; enum Fetch { case notModified; case rows(Components.Schemas.LookupResponse, etag: String) }; func fetch(companyID: Int64, projectID: Int64, kind: LookupKind, etag: String?, force: Bool = false) async throws -> Fetch }` sends `If-None-Match` when `etag` is given and `Cache-Control: no-cache` when `force`; errors throw `APIFailure`.
- `@Observable @MainActor final class LookupStore { init(context: ModelContext, service: LookupService); func rows(projectID: Int64, kind: LookupKind) -> [LookupRow]; func set(projectID:kind:) -> CachedLookup?; func prefetch(companyID: Int64, projectID: Int64, force: Bool = false) async; func refresh(companyID: Int64, projectID: Int64, kind: LookupKind, force: Bool) async throws; private(set) var isPrefetching: Bool }`. `prefetch` refreshes all six kinds concurrently with a `TaskGroup`, swallowing per-kind failures (the cached copy stays).

- [ ] **Step 1: Write the failing tests.** `LookupServiceTests.testFetchSendsIfNoneMatchAndParses304` (StubTransport returns 304 → `.notModified`; request header `If-None-Match` equals the given etag; path `/projects/99/lookups/users`; header `X-Company-Id: 7`), `testFetchParsesRowsAndETag` (200 with `ETag` header and `{"kind":"users","rows":[{"id":11,"label":"Ben West","extra":{"role":"Architect"}}],"unavailable":false,"fetched_at":"2026-10-24T10:00:00Z"}` → `.rows` with one row, etag captured), `testForceSendsNoCache`. `LookupStoreTests` on an in-memory container: `testRefreshStoresRows`, `testRefreshWith304KeepsRows`, `testPrefetchContinuesPastOneFailure` (one kind 500s, the others load), `testUnavailableKindStoredWithReason`.
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/LookupServiceTests -only-testing:FieldAssistantTests/LookupStoreTests` → build fails on missing types.
- [ ] **Step 3: Implement** the five files; `FieldAssistantApp` creates the container once and passes it with `.modelContainer(_:)`; `AppState` gains `let lookups: LookupStore` built in `init` from a `ModelContext` parameter (tests pass an in-memory one).
- [ ] **Step 4: Call `prefetch`** from `AppState` when `selectedProjectID` changes to a non-nil value and after `loadMe` succeeds with a stored selection.
- [ ] **Step 5: Run** the two classes → pass; run the full suite → pass.
- [ ] **Step 6: Commit** `feat(ios): SwiftData container and cached project lookups with ETag refresh`.

### Task 2: Local drafts

**Files:**
- Create: `ios/FieldAssistant/Core/Drafts/LocalDraft.swift`, `Core/Drafts/LocalPhoto.swift`, `Core/Drafts/DraftStore.swift`, `Core/Drafts/DraftFiles.swift`
- Test: `ios/FieldAssistantTests/DraftStoreTests.swift`

**Interfaces:**
- `enum LocalDraftStatus: String, Codable, Sendable { case waiting, draft, submitting, submitted, failed }` with `init(server: Components.Schemas.DraftResponse.statusPayload)` using the mapping in Global Constraints, and `var label: String` ("Waiting", "Draft", "Sending", "Submitted", "Fix").
- `@Model final class LocalDraft { @Attribute(.unique) var id: UUID; var companyID: Int64; var projectID: Int64; var feature: String; var status: LocalDraftStatus; var transcript: String; var capturedAt: Date; var latitude: Double?; var longitude: Double?; @Relationship(deleteRule: .cascade) var photos: [LocalPhoto]; var serverData: Data?; var subject: String?; var resultNumber: String?; var resultURL: String?; var errorMessage: String?; var updatedAt: Date }` with `var server: Components.Schemas.DraftResponse?` (decode/encode `serverData`) and `func apply(_ response: DraftResponse)` setting status, subject, result fields, `serverData`, `updatedAt`.
- `@Model final class LocalPhoto { var id: UUID; var fileName: String; var createdAt: Date; var draft: LocalDraft? }`.
- `enum DraftFiles { static func directory(for draftID: UUID) throws -> URL; static func audioURL(for draftID: UUID) -> URL; static func photoURL(draftID: UUID, fileName: String) -> URL; static func removeAll(for draftID: UUID) }` under Application Support `drafts/<id>/`.
- `@MainActor final class DraftStore { init(context: ModelContext); func insert(_ draft: LocalDraft) throws; func draft(id: UUID) -> LocalDraft?; func recent(projectID: Int64, limit: Int = 50) -> [LocalDraft] (newest `capturedAt` first); func drafts(withStatus: LocalDraftStatus) -> [LocalDraft]; func save() throws; func delete(_ draft: LocalDraft) throws (also `DraftFiles.removeAll`) }`.

- [ ] **Step 1: Write the failing tests**: `testInsertAndRecentOrdersNewestFirst`, `testApplyServerResponseMapsStatusAndResult` (a `DraftResponse` JSON with `status: "submitted"`, `result: {procore_id: 501, number: "214", url: "https://..."}` → `.submitted`, `resultNumber == "214"`), `testStatusMappingExtractedAndReviewedAreDraft`, `testDeleteRemovesFiles` (create the directory, delete, assert it is gone).
- [ ] **Step 2: Run** → fails; implement; run → pass.
- [ ] **Step 3: Commit** `feat(ios): local drafts and photos in SwiftData with on-disk files`.

### Task 3: Capture: on-device transcription, photos, location

**Files:**
- Create: `ios/FieldAssistant/Core/Capture/Transcriber.swift`, `Core/Capture/SpeechTranscriber.swift`, `Core/Capture/PhotoSaver.swift`, `Core/Capture/LocationOnce.swift`, `Core/Capture/CaptureModel.swift`
- Test: `ios/FieldAssistantTests/CaptureModelTests.swift`, `FieldAssistantTests/Support/FakeTranscriber.swift`

**Interfaces:**
- `protocol Transcriber: Sendable { func requestAuthorization() async -> TranscriberAuthorization; func start(audioFile: URL) throws -> AsyncThrowingStream<String, Error>; func stop() }` where each stream element is the full cumulative transcript so far; `enum TranscriberAuthorization { case granted, deniedMicrophone, deniedSpeech, unsupported }`.
- `final class SpeechTranscriber: Transcriber`: `SFSpeechRecognizer(locale: Locale(identifier: "en-US"))`, `requiresOnDeviceRecognition = true`, `AVAudioEngine` tap writing the buffers to `audioFile` with `AVAudioFile`; `unsupported` when `supportsOnDeviceRecognition == false`.
- `enum PhotoSaver { static func save(_ image: UIImage, draftID: UUID) throws -> LocalPhoto }` writes JPEG quality 0.85 to `DraftFiles.photoURL`, file name `<uuid>.jpg`.
- `final class LocationOnce: NSObject, CLLocationManagerDelegate { func fix(timeout: Duration = .seconds(5)) async -> CLLocationCoordinate2D? }` one `requestLocation`, nil on timeout or denial.
- `@Observable @MainActor final class CaptureModel { enum State: Equatable { case idle, requestingPermission, recording, stopped, denied(TranscriberAuthorization), failed(String) }; let draftID: UUID; private(set) var state: State; private(set) var transcript: String; private(set) var photos: [LocalPhoto]; private(set) var elapsed: Duration; private(set) var coordinate: CLLocationCoordinate2D?; init(draftID: UUID = UUID(), transcriber: Transcriber, location: LocationOnce? = nil, clock: any Clock<Duration> = ContinuousClock()); func start() async; func stop(); func addPhoto(_ image: UIImage) throws; func removePhoto(_ photo: LocalPhoto); var canBuild: Bool { state == .stopped && !transcript.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }; func makeDraft(companyID: Int64, projectID: Int64) -> LocalDraft` (status `waiting`, transcript, photos, coordinate, `capturedAt` = now). `#if DEBUG func setTypedTranscript(_ text: String)` sets transcript and state `.stopped` without recording `#endif`.
- `FakeTranscriber` in tests: scripted authorization and a `continuation` the test drives.

- [ ] **Step 1: Write the failing tests**: `testStartWithoutPermissionReportsDenied` (fake returns `.deniedMicrophone` → `state == .denied(.deniedMicrophone)`, stream never started), `testTranscriptUpdatesWhileRecording` (yield "twisted", "twisted wood column" → transcript is the last value, state `.recording`), `testStopEnablesBuildOnlyWithText` (stop with empty transcript → `canBuild == false`; with text → true), `testMakeDraftCarriesPhotosAndLocation`, `testTypedTranscriptSkipsRecording` (DEBUG).
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/CaptureModelTests` → fails; implement the five files.
- [ ] **Step 3: Run** → pass. Build the app for the simulator to confirm `Speech` and `AVFoundation` link (`xcodebuild build -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' ...`).
- [ ] **Step 4: Commit** `feat(ios): on-device speech capture with photos and one location fix`.

### Task 4: Draft service (create, get, save, submit, job polling)

**Files:**
- Create: `ios/FieldAssistant/Core/Drafts/DraftService.swift`
- Test: `ios/FieldAssistantTests/DraftServiceTests.swift`
- Modify: `FieldAssistantTests/Support/APITestSupport.swift` (`APIStub.draft(...)`, `APIStub.job(...)`)

**Interfaces:**
- `typealias DraftResponse = Components.Schemas.DraftResponse`, `RFIDraftPayload = Components.Schemas.RFIDraftPayload`, `UnresolvedField = Components.Schemas.UnresolvedField`, `JobStatus = Components.Schemas.JobStatus`.
- `struct DraftService { let client: Client; func create(_ draft: LocalDraft) async throws -> DraftResponse; func get(id: UUID, companyID: Int64) async throws -> DraftResponse; func save(id: UUID, companyID: Int64, payload: RFIDraftPayload) async throws -> DraftResponse; enum SubmitStart { case submitted(DraftResponse); case running(jobID: String) }; func submit(id: UUID, companyID: Int64) async throws -> SubmitStart; func job(id: String, companyID: Int64) async throws -> JobStatus; func waitForJob(id: String, draftID: UUID, companyID: Int64, poll: Duration = .seconds(2), timeout: Duration = .seconds(60), clock: any Clock<Duration> = ContinuousClock()) async throws -> DraftResponse }`. `create` sends `CreateDraftRequest{id, feature: .rfi, project_id, transcript, captured_at, location}`. `submit` sends header `Idempotency-Key` = the draft id string; 200 → `.submitted` (fetch the draft), 202 → `.running`. `waitForJob` polls until `succeeded` or `failed`, then returns `get(draft)`; on timeout returns `get(draft)` too. 400 from submit with `code == "bad_request"` throws `APIFailure.validation(message)` (add the case to `APIFailure` if absent, mapping 400).

- [ ] **Step 1: Write the failing tests**: `testCreatePostsTranscriptAndIdempotentID` (recorded body has `id`, `feature == "rfi"`, `transcript`; path `/drafts`), `testSubmitSendsIdempotencyKeyEqualToDraftID`, `testSubmit200IsAlreadySubmitted`, `testWaitForJobPollsUntilSucceeded` (script: job `queued`, job `running`, job `succeeded`, then draft `submitted`; with a test clock, assert three job requests then one draft request), `testWaitForJobTimeoutFallsBackToDraft`, `testCreateOfflineMarksWaiting` is in Task 8 (service only throws `.offline` here: `testCreateOfflineThrowsOffline`).
- [ ] **Step 2: Run** → fails; implement; run → pass.
- [ ] **Step 3: Commit** `feat(ios): draft service with idempotent submit and job polling`.

### Task 5: Review components

**Files:**
- Create: `ios/FieldAssistant/Core/Review/ReviewRow.swift`, `Core/Review/UnresolvedRow.swift`, `Core/Review/PairedRow.swift`, `Core/Review/LookupPickerView.swift`, `Core/Review/TranscriptDisclosure.swift`, `Core/Review/ReviewFooter.swift`
- Test: `ios/FieldAssistantTests/LookupPickerFilterTests.swift`

**Interfaces:**
- `struct ReviewRow<Content: View>: View { init(label: String, flagged: Bool = false, chevron: Bool = false, action: (() -> Void)? = nil, @ViewBuilder content: () -> Content) }`: uppercase caption label, content, optional chevron, 44 pt minimum height, hairline divider.
- `struct UnresolvedRow: View { let label: String; let field: UnresolvedField; let onChoose: (LookupRow) -> Void; let onSomeoneElse: () -> Void }`: reason text (from `field.reason` and `field.message`: `ambiguous` → "More than one match", `not_found` → "Not found in the project", `not_in_lookup` → "Not in the project list", `date_unparseable` → "Could not read the date", `procore_validation` → the message), candidate radio rows showing `label` and `subtitle`, "Someone else…" link (title "Choose another…" for non-people kinds).
- `struct PairedRow<L: View, R: View>: View` two labelled halves split by a vertical divider.
- `struct LookupPickerView: View { let kind: LookupKind; let rows: [LookupRow]; let unavailableReason: String?; @Binding var selection: Int64?; let multi: Bool; let selections: Binding<[Int64]>? }` with a search field; `enum LookupFilter { static func filter(_ rows: [LookupRow], query: String) -> [LookupRow] }` case-insensitive match on label or any `extra` value, empty query returns all; unavailable shows "Not available on this project" and the reason.
- `struct TranscriptDisclosure: View { let transcript: String }` collapsed by default, title "Field notes · original transcript".
- `struct ReviewFooter: View { let unresolvedCount: Int; let isSubmitting: Bool; let onSave: () -> Void; let onSubmit: () -> Void }`: "Save draft" and "Submit to Procore"; submit disabled while `unresolvedCount > 0` or `isSubmitting`; spinner inside the submit button while submitting.

- [ ] **Step 1: Write the failing test** `LookupPickerFilterTests.testFilterMatchesLabelAndExtraCaseInsensitive` and `testEmptyQueryReturnsAll`.
- [ ] **Step 2: Implement** the six views and `LookupFilter`; add a `#Preview` to each view with sample rows.
- [ ] **Step 3: Run** the test class → pass; build succeeds.
- [ ] **Step 4: Commit** `feat(ios): generic review rows, unresolved row, lookup picker and footer`.

### Task 6: RFI review model

**Files:**
- Create: `ios/FieldAssistant/Features/RFI/RFIField.swift`, `Features/RFI/RFIReviewModel.swift`
- Test: `ios/FieldAssistantTests/RFIReviewModelTests.swift`

**Interfaces:**
- `enum RFIField: String, CaseIterable, Sendable { case subject, questionBody = "question_body", assigneeIds = "assignee_ids", rfiManagerId = "rfi_manager_id", locationId = "location_id", specificationSectionId = "specification_section_id", dueDate = "due_date", drawingNumber = "drawing_number", scheduleImpact = "schedule_impact", costImpact = "cost_impact", distributionIds = "distribution_ids", responsibleContractorId = "responsible_contractor_id", costCodeId = "cost_code_id", `private`, draft }` with `var label: String`, `var lookupKind: LookupKind?`, `var isMulti: Bool`, `static let primary: [RFIField]` (the handoff order through `costImpact`) and `static let more: [RFIField]` (the rest); `init?(serverField: String)` (also maps `_form` to nil).
- `@Observable @MainActor final class RFIReviewModel { init(draft: LocalDraft, response: DraftResponse, lookups: LookupStore, service: DraftService, store: DraftStore, submitAsDraftOverride: Bool?); private(set) var payload: RFIDraftPayload; private(set) var unresolved: [UnresolvedField]; var unresolvedCount: Int { unresolved.count }; private(set) var formMessage: String? (from `_form`); var moreExpanded: Bool (initially true when any `more` field is set or flagged); func unresolved(for field: RFIField) -> UnresolvedField?; func choose(_ row: LookupRow, for field: RFIField); func setID(_ id: Int64?, for field: RFIField); func setIDs(_ ids: [Int64], for field: RFIField); func setText(_ text: String, for field: RFIField); func setDate(_ date: Date?); func setImpact(_ impact: Components.Schemas.Impact?, for field: RFIField); func setBool(_ value: Bool, for field: RFIField); func label(forID id: Int64, kind: LookupKind) -> String?; private(set) var isSaving: Bool; private(set) var isSubmitting: Bool; func saveDraft() async -> APIFailure?; enum SubmitOutcome: Equatable { case submitted(number: String?, url: String?); case needsFixes; case failed(APIFailure) }; func submit() async -> SubmitOutcome }`. `choose` sets the ID (append for multi) and removes the field from `unresolved`. `submit`: `saveDraft` first (the server validates again), then `service.submit`; `.running` → `waitForJob`; the resulting `DraftResponse` is applied to the `LocalDraft` and saved through `store`; `status == .submitted` → `DraftFiles.removeAll(for:)` then `.submitted`; `status == .failed` with `unresolved` non-empty → replace `unresolved`, `.needsFixes`; a 400 from submit listing unresolved fields → `.needsFixes` with those fields flagged `procore_validation`-style using the server message; other errors → `.failed`. When `submitAsDraftOverride` is non-nil it overwrites `payload.draft` before every save.

- [ ] **Step 1: Write the failing tests** (StubTransport + in-memory container; a `DraftResponse` fixture with `assignee_ids` unresolved `ambiguous` with candidates 12 and 13, `location_id` unresolved `not_found`, `cost_code_id: 401` set):
  - `testInitialStateCountsUnresolvedAndExpandsMore` (count 2, `moreExpanded == true` because `cost_code_id` is set).
  - `testChoosingCandidateClearsFlagAndSetsID` (choose 12 for `assigneeIds` → payload `assignee_ids == [12]`, count 1).
  - `testSubmitSavesThenSubmitsThenPolls` (script: PATCH 200, POST submit 202, job succeeded, GET draft submitted → `.submitted(number: "214", ...)`, local draft status `.submitted`, request order PATCH, POST, GET job, GET draft).
  - `testSubmit422ReflagsRows` (PATCH 200, submit 202, job failed, GET draft `status failed` with unresolved `due_date procore_validation "can't be blank"` → `.needsFixes`, `unresolved(for: .dueDate)?.message == "can't be blank"`, local status `.failed`).
  - `testSubmitWith400UnresolvedShowsFields`.
  - `testSubmitAsDraftOverrideAppliedOnSave` (override false → PATCH body `payload.draft == false`).
- [ ] **Step 2: Run** → fails; implement `RFIField` and `RFIReviewModel`.
- [ ] **Step 3: Run** → pass.
- [ ] **Step 4: Commit** `feat(ios): RFI review model with candidate resolution, save and submit flow`.

### Task 7: Screens: Home, Capture, Review, Result, Settings

**Files:**
- Create: `ios/FieldAssistant/Features/RFI/CaptureView.swift`, `Features/RFI/RFIReviewView.swift`, `Features/RFI/RFIResultView.swift`, `Features/RFI/RecentDraftsView.swift`, `ios/FieldAssistant/App/SettingsView.swift`, `App/Settings.swift`
- Modify: `ios/FieldAssistant/App/HomeView.swift`, `App/AppState.swift` (`DraftStore`, `DraftService`, `Settings`)

**Interfaces:**
- `@Observable @MainActor final class Settings { init(defaults: UserDefaults = .standard); var submitAsDraft: Bool? (nil until touched; stored under key "submitAsDraft") }`.
- `AppState` gains `let drafts: DraftStore`, `var draftService: DraftService { DraftService(client: client) }`, `let settings: Settings`.
- `HomeView`: title is the project name with a "Change project" control (existing); body is a `List` with: a record button section (large circular `Button` "Record an RFI" with `mic.fill`, subtitle "Tap, talk, review, send"), a "Recent" section (`RecentDraftsView`: rows show `subject ?? "RFI draft"`, `capturedAt` relative, and a status chip from `LocalDraftStatus.label`; tapping a `waiting` draft sends it for extraction then opens Review; `draft`/`failed` open Review; `submitted` opens Result), and a "Procore RFIs" link to the existing `RFIListView`. Toolbar: the existing account menu gains "Settings" pushing `SettingsView`. A `waiting`-drafts banner "No signal. Recording works; sending waits." shows when `appState.phase == .offline` or any draft is `waiting` and the network is unreachable.
- Navigation: `NavigationStack(path:)` on `HomeView` with `enum Route: Hashable { case capture(UUID), review(UUID), result(UUID) }`.
- `CaptureView(model: CaptureModel)`: top bar "Cancel" and "Recording m:ss" (or "Stopped"), the transcript in a scrolling `Text` at `.title3`, photo strip (`PhotosPicker` and a camera `Button` using `UIImagePickerController` wrapped in `UIViewControllerRepresentable`), bottom row: Stop (a 76 pt circular button) and "Build RFI" (disabled until `canBuild`). `#if DEBUG` a "Type instead" button opens a sheet with a `TextEditor` and "Use this" calling `setTypedTranscript` `#endif`. Starts recording in `.task` on appear. "Build RFI" inserts the `LocalDraft`, calls `DraftService.create`; on success applies the response and pushes `.review`; on `APIFailure.offline` leaves status `waiting` and pops to Home.
- `RFIReviewView(model: RFIReviewModel)`: title "Review RFI" with a chip "N fields need you" when `unresolvedCount > 0`; rows in `RFIField.primary` order using `ReviewRow`/`UnresolvedRow`/`PairedRow` (due date and drawing; schedule and cost impact); `questionBody` is a `TextEditor`; ID rows show `label(forID:)` and push `LookupPickerView` bound to the field; impacts use a `Picker` with "Not stated", "Yes, known", "Yes, unknown", "No", "TBD" and a value field; the "More" `DisclosureGroup` holds `RFIField.more`; photos as 64 pt thumbnails from `DraftFiles`; `TranscriptDisclosure`; `ReviewFooter` in `safeAreaInset(edge: .bottom)`. Submit outcome `.submitted` pushes `.result`; `.needsFixes` scrolls to the first flagged row and shows `formMessage` if any; `.failed` shows an alert with the failure's message.
- `RFIResultView(draft: LocalDraft)`: "Created in Procore", "RFI <number>" (or "RFI created" when the number is nil), subject, "Open in Procore" (`Link` to `resultURL`, hidden when nil), "New RFI" (pops to Home and pushes `.capture`), "Done" (pops to Home).
- `SettingsView`: sections "Submitting" (`Toggle` "Submit as Procore draft" bound to `settings.submitAsDraft ?? true`), "Project" ("Refresh people and locations" calls `lookups.prefetch` with `force`, shows a spinner; "Change project" clears `selectedProjectID`), "Account" (user name, "Sign out"), footer with `AppConfig.environment`, version and build from `Bundle.main`.

- [ ] **Step 1: Implement** the screens and the `AppState` additions. No new unit tests for views; the models are tested in Tasks 3, 4 and 6.
- [ ] **Step 2: Build and run in the Simulator on Local** against `make run` (backend plan Task 11): sign in, tap Record, use "Type instead" with the twisted-column transcript, Build RFI, see the review screen with Ben West resolved (seed the local sandbox project or use the fake data path described in backend Task 12), Submit, see the result. Fix anything the walk-through shows.
- [ ] **Step 3: Run** the full test suite → pass.
- [ ] **Step 4: Commit** `feat(ios): home record button, capture, review, result and settings screens`.

### Task 8: Waiting-draft resend and submit reconciliation

**Files:**
- Create: `ios/FieldAssistant/Core/Drafts/DraftSync.swift`, `ios/FieldAssistant/Core/Network/Reachability.swift`
- Test: `ios/FieldAssistantTests/DraftSyncTests.swift`
- Modify: `ios/FieldAssistant/App/FieldAssistantApp.swift` (scene phase hook), `App/AppState.swift`

**Interfaces:**
- `protocol ReachabilityProviding: Sendable { var isReachable: Bool { get }; var updates: AsyncStream<Bool> { get } }`; `final class Reachability: ReachabilityProviding` over `NWPathMonitor`.
- `@MainActor final class DraftSync { init(store: DraftStore, service: @escaping () -> DraftService, reachability: ReachabilityProviding); func start(); func stop(); func runOnce() async }`. `runOnce`: for every `waiting` draft, `service.create` → apply the response (status `draft`); `.offline` stops the pass; other failures leave the draft `waiting` and record `errorMessage`. For every `submitting` draft, `service.get` → apply (reconciling to `submitted` or `failed`; still `submitting` is left alone). `start` runs `runOnce` immediately, on every `updates` value of `true`, and is called again by the app when `scenePhase` becomes `.active`.

- [ ] **Step 1: Write the failing tests**: `testWaitingDraftsResendWhenReachable` (fake reachability flips to true → one `POST /drafts` per waiting draft, statuses become `draft`), `testOfflineFailureStopsPassAndKeepsWaiting`, `testSubmittingDraftReconcilesOnLaunch` (a `submitting` draft, GET returns `submitted` → local `.submitted`, files removed), `testCreateOfflineMarksWaiting` (CaptureView path is covered by the model: `DraftSync` is not involved; assert the draft inserted by Task 7's flow stays `waiting` when `create` throws `.offline`, using `DraftService` with `FailingTransport`).
- [ ] **Step 2: Run** → fails; implement; run → pass.
- [ ] **Step 3: Wire** `DraftSync` into `AppState` (started after sign-in, stopped on sign-out) and the scene phase hook in `FieldAssistantApp`.
- [ ] **Step 4: Commit** `feat(ios): resend waiting drafts on connectivity and reconcile submitting drafts on launch`.

### Task 9: Dev verification on Simulator and iPhone, docs

**Files:**
- Modify: `CLAUDE.md` (Code map rows for `Core/Capture`, `Core/Drafts`, `Core/Lookups`, `Core/Review`; Status line), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 2 result), `ios/project.yml` (Drew: `DEVELOPMENT_TEAM`)

- [ ] **Step 1: Simulator against dev.** Build the Dev configuration (`xcodebuild build -configuration Dev ...`), install on the booted iPhone 17 with `xcrun simctl install` and launch `com.monroeresidential.fieldassistant.dev`; open it through Device Hub (`/Applications/Xcode.app/Contents/Applications/DeviceHub.app`). Sign in to the sandbox, pick the verification project, Record, "Type instead" with the twisted-column transcript, Build RFI, resolve any flagged field, Submit, Result shows the number. Confirm the RFI in the sandbox project and the audit rows on the droplet (`ssh fieldassistant-droplet 'docker compose -f /opt/fieldassistant/deploy/docker-compose.yml exec -T postgres-dev psql -U fa fieldassistant -c "select kind, created_at from audit_events order by id desc limit 5"'`).
- [ ] **Step 2 (Drew): Team ID.** Set `DEVELOPMENT_TEAM` in `ios/project.yml` (the commented placeholder), run `make ios-project`, open the project in Xcode, select the `Dev` configuration in the scheme, and run on your iPhone.
- [ ] **Step 3: iPhone dictation.** Record the twisted-column RFI by voice with two photos, Build RFI, review, Submit. Then: airplane mode, record another, Build RFI → "Waiting" with the banner; airplane mode off → the draft is sent and opens for review.
- [ ] **Step 4: Record results** (RFI numbers, any prompt tuning done) in the roadmap's milestone 2 row and update `CLAUDE.md`; commit `docs: milestone 2 verified on dev from the iPhone` and open the `develop` → `main` promotion only when Drew says so (prod has no Procore app yet).
