# Milestone 4 iOS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The phone opens any existing local store without crashing, tells a user before recording when Procore will refuse, treats a Procore sign-out as one sign-in prompt that keeps field work, closes the hardening items a pilot user would hit, shows Procore's required-field notes on Review, and ships distinct TestFlight builds.

**Architecture:** A `VersionedSchema` and migration plan wrap the five SwiftData models, and `AppContainer` returns a recoverable failure the app shows instead of `fatalError`. `AppState` caches companies and projects per session and holds the features state per project from the 1.7.0 `getProjectFeatures` call; Home reads availability from it. `reauth_required` from any call routes through one `AppState.handleReauthRequired()` that ends the session with a reason the sign-in screen shows. Review's locked-save path stops instead of continuing to submit, and unknown Procore field errors render as "Procore requires" notes. Fastlane stamps build numbers.

**Tech Stack:** Swift 6 strict concurrency, SwiftUI, SwiftData (`VersionedSchema`, `SchemaMigrationPlan`), swift-openapi-generator client, Fastlane, XCTest with `StubTransport`.

**Spec:** `docs/superpowers/specs/2026-10-31-milestone-4-hardening-testflight-design.md` (sections 2, 3, 4, 5, 6). Backend plan `2026-10-31-milestone-4-backend.md` must be merged and the contract synced before Task 2. Runbook: `2026-10-31-milestone-4-pilot-runbook.md`.

## Global Constraints

- Contract 1.7.0 client: `getProjectFeatures` → `FeaturesResponse {features: [{feature, available, reason?}], fetched_at}`; `procore_permission_denied` messages now carry Procore's reason.
- Copy, exact: sign-in screen note after a Procore sign-out "Procore signed you out. Sign in again and your drafts will send."; Home's unavailable RFI card kicker "Not available" with Procore's reason beneath; record-button hint when unavailable "RFIs are not available on this project"; Review note for a Procore field with no row "Procore requires <field>: <message>"; store-open failure screen title "Local data couldn't be opened", body "This phone's saved drafts and lookups could not be read. Reset local data to continue; drafts not yet sent will be lost.", button "Reset local data"; locked-save stop message "This draft changed on the server. Review it, then submit again."
- Design-system tokens and `Theme.Text` only; existing strings, accessibility labels and identifiers stay unless named here.
- Behaviour that must not change: the submit runner's guarantees, `AuthRefreshError.rejected` as the refresh sign-out trigger, `DraftSync` triggers.
- Build numbers: `CURRENT_PROJECT_VERSION` set by Fastlane to `yyyyMMddHHmm` UTC before archiving; `MARKETING_VERSION` `1.0` in `project.yml`; Info.plist keys become `$(MARKETING_VERSION)` and `$(CURRENT_PROJECT_VERSION)`.
- Every existing test stays green; the iOS suite runs with `cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile` after `make ios-project`.

## Review Focus

1. A phone whose store was written by the milestone 3 build (all current fields) opens under the new `VersionedSchema` with no data loss. Test in Task 1 (`testCurrentStoreOpensUnderVersionedSchema`): write rows with the plain `Schema`, reopen with the versioned container, read them back.
2. A corrupt store file: the app shows the reset screen, resetting deletes the store files and the next launch opens clean. Test in Task 1 (`testResetLocalDataRemovesStoreFiles`).
3. A `reauth_required` 401 arriving mid-queue from a background pass: the draft stays queued, the session ends once, the sign-in screen shows the note, and signing in sends the draft. Test in Task 3 (`testReauthFromBackgroundPassKeepsDraftAndShowsNote`).
4. Switching projects three times in ten seconds: one projects fetch per company per session, not three. Test in Task 4 (`testProjectListFetchedOncePerCompanyPerSession`).
5. Taking a photo mid-dictation: the recording continues and the transcript keeps growing after the camera closes. Test in Task 4 (`testCameraCoverDoesNotStopRecording`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `ios/FieldAssistant/App/AppSchema.swift` | `AppSchemaV1: VersionedSchema`, `AppMigrationPlan`. |
| `ios/FieldAssistant/App/AppContainer.swift` | `makeModelContainer() -> Result`, `resetLocalData()`. |
| `ios/FieldAssistant/App/StoreRecoveryView.swift` | The reset screen. |
| `ios/FieldAssistant/App/FieldAssistantApp.swift` | Uses the result; no `fatalError`. |
| `ios/FieldAssistant/Core/Scope/FeaturesService.swift`, `App/AppState.swift` | Features fetch and per-project state; scope cache; reauth handling. |
| `ios/FieldAssistant/App/HomeView.swift` | Availability on the RFI card and record button. |
| `ios/FieldAssistant/Core/Auth/SignInView.swift`, `App/RootView.swift` | Sign-out reason note. |
| `ios/FieldAssistant/Core/Network/AuthMiddleware.swift` or `APIFailure` call sites | `reauth_required` routing. |
| `ios/FieldAssistant/Core/Scope/ProjectPickerView.swift` | Uses the scope cache; "Last used" from the selection. |
| `ios/FieldAssistant/Features/RFI/RFIReviewModel.swift`, `RFIReviewView.swift` | Locked-save stop; "Procore requires" notes. |
| `ios/FieldAssistant/Features/RFI/CaptureView.swift`, `Core/Capture/CaptureModel.swift`, `Core/Drafts/DraftStore.swift` | Mic across the camera cover; photo cleanup on rollback. |
| `ios/fastlane/Fastfile`, `ios/project.yml`, `ios/FieldAssistant/Resources/Info.plist` | Build numbers. |
| Tests | `AppSchemaTests`, `AppStateTests`, `HomeViewTests`, `RFIReviewModelTests`, `CaptureModelTests`, `DraftStoreTests`, `ScopeCacheTests`. |

---

### Task 1: Versioned schema and a recoverable store open

**Files:**
- Create: `ios/FieldAssistant/App/AppSchema.swift`, `ios/FieldAssistant/App/StoreRecoveryView.swift`, `ios/FieldAssistantTests/AppSchemaTests.swift`
- Modify: `ios/FieldAssistant/App/AppContainer.swift`, `ios/FieldAssistant/App/FieldAssistantApp.swift:10-17`

**Interfaces:**
```swift
enum AppSchemaV1: VersionedSchema {
    static let versionIdentifier = Schema.Version(1, 0, 0)
    static let models: [any PersistentModel.Type] = [CachedLookup.self, LocalDraft.self, LocalPhoto.self, CachedRecord.self, ProjectSyncState.self]
}
enum AppMigrationPlan: SchemaMigrationPlan { static let schemas: [any VersionedSchema.Type] = [AppSchemaV1.self]; static let stages: [MigrationStage] = [] }
enum AppContainer {
    enum OpenFailure: Error { case open(Error) }
    static func makeModelContainer(inMemory: Bool = false) -> Result<ModelContainer, OpenFailure>
    static func storeURLs() -> [URL]            // the default store file and its -wal/-shm siblings
    static func resetLocalData() throws        // removes storeURLs(); DraftFiles root too
}
```
`FieldAssistantApp.init` keeps a `Result`; on `.failure` the scene shows `StoreRecoveryView(error:onReset:)` whose button calls `resetLocalData()` then `makeModelContainer()` again and swaps in the app on success (the view is owned by a small `@Observable` `AppLaunch` state so the swap re-renders). The reset screen uses the design system (`Kicker`, `Theme.Text.body`, `PrimaryButtonStyle`).

- [ ] **Step 1: Write the failing tests** in `AppSchemaTests.swift`: `testCurrentStoreOpensUnderVersionedSchema` (create a container with the plain `Schema([...])` at a temp URL, insert one `LocalDraft` with two photos and one `CachedLookup`, close; open the same URL through `AppMigrationPlan` + `AppSchemaV1`; fetch and compare), `testVersionedSchemaListsEveryModel` (the five types, in a stable order), `testResetLocalDataRemovesStoreFiles` (write garbage to the store URL; `makeModelContainer()` returns `.failure`; `resetLocalData()`; `makeModelContainer()` returns `.success`), `testMakeModelContainerInMemorySucceeds`.
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/AppSchemaTests` — expected: compile failure.
- [ ] **Step 3: Implement**; keep `inMemory: true` for tests through the same path; replace the `fatalError`.
- [ ] **Step 4: Run** the class, then the full suite — expected: PASS. Launch the Local build on the simulator with an existing store from the current build (run the current build first) and confirm it opens with its drafts.
- [ ] **Step 5: Commit** `feat(ios): versioned SwiftData schema and a recoverable store open`

---

### Task 2: Features on Home

**Files:**
- Create: `ios/FieldAssistant/Core/Scope/FeaturesService.swift`, `ios/FieldAssistantTests/FeaturesServiceTests.swift`
- Modify: `ios/FieldAssistant/App/AppState.swift` (`features: [Int64: FeatureAvailability]`, `loadFeatures(projectID:)` on selection, on `becameActive`, after Sync), `ios/FieldAssistant/App/HomeView.swift:185-219` (`FeatureCard` unavailable state, record button), `ios/FieldAssistantTests/HomeViewTests.swift`, `AppStateTests.swift`
- Requires: contract 1.7.0 synced into `ios/Packages/FieldAssistantAPI`.

**Interfaces:**
```swift
struct FeatureAvailability: Equatable, Sendable { let available: Bool; let reason: String? }
struct FeaturesService: Sendable {
    init(client: Client)
    func features(projectID: Int64, companyID: Int64, noCache: Bool = false) async throws -> [String: FeatureAvailability]   // keyed by feature name
}
// AppState
var rfiAvailability: FeatureAvailability   // .init(available: true, reason: nil) until a fetch says otherwise; offline keeps the last value
func loadFeatures(force: Bool = false) async
// HomeView
static func recordHint(_ a: FeatureAvailability) -> String   // pure: existing hint when available, "RFIs are not available on this project" when not
```
Home: the RFI `FeatureCard` shows kicker "Not available" and `reason` in `meta12` beneath when unavailable (tokens: muted text, dashed hairline like the "Coming" cards); the `RecordButton` gets `isEnabled: appState.rfiAvailability.available` and the hint above. A `procore_permission_denied` on the features call itself (project not visible) leaves availability as is and lets the existing No access path handle it.
Also in this task, because it rides on the same contract bump: Review's RFI manager picker and its lookup prefetch use the new lookup kind `rfi_managers` (the backend now sources `rfi_manager_id` candidates from Procore's potential RFI managers); the field-to-kind mapping on the phone changes for `rfi_manager_id` only, with a test in `LookupServiceTests` or `RFIReviewModelTests` that the picker requests `rfi_managers`.

- [ ] **Step 1: Write the failing tests**: `FeaturesServiceTests.testFeaturesDecodeAndKeyByName`, `testFeaturesNoCacheSendsHeader`; `AppStateTests.testSelectingProjectLoadsFeatures`, `testFeaturesOfflineKeepsLastValue`, `testSyncRefetchesFeatures`; `HomeViewTests.testRecordHintFollowsAvailability`.
- [ ] **Step 2: Run** the three classes — expected: compile failure.
- [ ] **Step 3: Implement**; take light/dark screenshots of Home with RFI unavailable to `~/Desktop/fieldassistant-m4/`.
- [ ] **Step 4: Run** the classes and the full suite — expected: PASS.
- [ ] **Step 5: Commit** `feat(ios): Home shows whether RFIs are available on the project, with Procore's reason`

---

### Task 3: One sign-in prompt for a Procore sign-out

**Files:**
- Modify: `ios/FieldAssistant/App/AppState.swift` (`enum SignOutReason { case user, refreshRejected, procoreSignedOut }`, `signOutReason: SignOutReason?`, `handleReauthRequired()`, `endSession(reason:)`), `ios/FieldAssistant/Core/Network/AuthMiddleware.swift:60-67` (after the one retry, a 401 whose body code is `reauth_required` calls a new `onReauthRequired` hook once), `ios/FieldAssistant/App/RootView.swift`, `ios/FieldAssistant/Core/Auth/SignInView.swift` (`note: String?` parameter shown above the sign-in button in accent), `ios/FieldAssistant/Core/Network/APIFailureView.swift` (the `.reauthRequired` case routes to `appState.handleReauthRequired()` instead of showing in place), `ios/FieldAssistantTests/AppStateTests.swift`, `AuthMiddlewareTests.swift`, `DraftSyncTests.swift`

**Interfaces:**
```swift
// AppState
enum SignOutReason: Equatable { case user, refreshRejected, procoreSignedOut }
private(set) var signOutReason: SignOutReason?
func handleReauthRequired()        // idempotent per session epoch: endSession(reason: .procoreSignedOut)
static func signInNote(for reason: SignOutReason?) -> String?   // pure: .procoreSignedOut and .refreshRejected → the spec's sentence; .user/nil → nil
// AuthMiddleware
init(..., onSignedOut: @escaping @Sendable () -> Void, onReauthRequired: @escaping @Sendable () -> Void)
```
The middleware fires `onReauthRequired` at most once per session (same `hasSignaledSignOut` guard) when the retried response is 401 with body `{"code":"reauth_required"}`; `AppState` wires it to `handleReauthRequired()`. `endSession(reason:)` records the reason before setting `.signedOut`; `signIn()` success clears it. The in-place "Session expired" UI in `APIFailureView` and the picker alert go away for `.reauthRequired` (the session ends instead); Capture's nil-owner message stays.

- [ ] **Step 1: Write the failing tests**: `AppStateTests.testReauthRequiredEndsSessionWithNote` (reason recorded, `signInNote` equals the sentence), `testSignInClearsSignOutReason`, `testReauthFromBackgroundPassKeepsDraftAndShowsNote` (a queued draft, the PATCH answers 401 `reauth_required` → session ended once, draft still `submitPending`, note present; then a successful sign-in runs a pass and the draft submits); `AuthMiddlewareTests.testReauthRequired401SignalsOnce`; `SignInView` pure test for `signInNote`.
- [ ] **Step 2: Run** the classes — expected: compile failure.
- [ ] **Step 3: Implement**; screenshot the sign-in screen with the note (light/dark).
- [ ] **Step 4: Run** the classes and the full suite — expected: PASS.
- [ ] **Step 5: Commit** `feat(ios): a Procore sign-out is one sign-in prompt that keeps queued drafts`

---

### Task 4: Hardening group

**Files:**
- Create: `ios/FieldAssistant/Core/Scope/ScopeCache.swift`, `ios/FieldAssistantTests/ScopeCacheTests.swift`
- Modify: `ios/FieldAssistant/App/AppState.swift` (owns `ScopeCache`), `ios/FieldAssistant/Core/Scope/ProjectPickerView.swift:121-131,202-204,255-263,286-289` (cache; "Last used" from `appState.selectedProjectID` when set, else the `/me` value), `ios/FieldAssistant/App/HomeView.swift:345-351` (`loadTitles` from the cache), `ios/FieldAssistant/Features/RFI/CaptureView.swift:131,136-141` (mic across the camera cover), `ios/FieldAssistant/Core/Capture/CaptureModel.swift:193-206` and `Core/Drafts/DraftStore.swift:16-25` (photo cleanup on rollback), `ios/FieldAssistant/Features/RFI/RFIReviewModel.swift:330-364` (locked-save stop), tests in `RFIReviewModelTests`, `CaptureModelTests`, `DraftStoreTests`, `AppStateTests`

**Interfaces:**
```swift
@MainActor @Observable final class ScopeCache {
    func companies(force: Bool = false) async throws -> [Components.Schemas.Company]
    func projects(companyID: Int64, force: Bool = false) async throws -> [Components.Schemas.Project]
    func clear()                                        // on sign-out and session generation change
}
// CaptureView: `.onDisappear { if model.state == .recording && !showsCamera { model.stop() } }` plus the camera's onDismiss leaves the mic alone.
// CaptureModel.makeDraft(...) hands LocalPhoto instances to the draft; DraftStore.insert on rollback detaches them: `for p in draft.photos { p.draft = nil }` and CaptureModel keeps them so a second Build works.
// RFIReviewModel.persist(): on the locked 400 → refreshLocked() returns `.locked(status:)`; submit() then returns `.pending(message: Self.lockedStopMessage)` without queuing; take(response, sent:) is used so unsaved edits survive the refresh.
```
Picker and Home read the cache; pull-to-refresh passes `force: true`; a company change does not refetch companies.

- [ ] **Step 1: Write the failing tests**: `ScopeCacheTests.testProjectListFetchedOncePerCompanyPerSession` (three `projects(companyID:)` calls → one request; `force` → a second), `testClearDropsCache`; `AppStateTests.testSignOutClearsScopeCache`; `CaptureModelTests.testCameraCoverDoesNotStopRecording` (view-model level: `showsCamera` true → the disappear path leaves `state == .recording`); `DraftStoreTests.testRolledBackInsertDetachesPhotos` (force a save failure, then a second insert of a new draft with the same photos succeeds); `RFIReviewModelTests.testLockedSaveWithFailedServerDraftStopsAndKeepsEdits` (PATCH 400 locked, GET returns `failed`: outcome `.pending(lockedStopMessage)`, no POST, unsaved edit kept), and the existing `testLockedSubmitReportsServerStatusWithoutPosting` still green; `ProjectPicker` pure test `lastUsedProjectID(selected:meLast:)`.
- [ ] **Step 2: Run** the classes — expected: compile failure / FAIL.
- [ ] **Step 3: Implement**.
- [ ] **Step 4: Run** the classes and the full suite — expected: PASS.
- [ ] **Step 5: Commit** `fix(ios): scope cache, mic across the camera, photo cleanup on rollback, locked-save stop, Last used`

---

### Task 5: "Procore requires" notes on Review

**Files:**
- Modify: `ios/FieldAssistant/Features/RFI/RFIReviewModel.swift:65,93-94,379-380,399-424` (`procoreNotes: [ProcoreNote]` from `_form` entries whose message is prefixed `"<key>: "`), `ios/FieldAssistant/Features/RFI/RFIReviewView.swift:280-285,355-361` (render notes as "Procore requires <field>: <message>" lines under the form message), `ios/FieldAssistantTests/RFIReviewModelTests.swift`

**Interfaces:**
```swift
struct ProcoreNote: Equatable { let field: String; let message: String }
static func procoreNotes(from unresolved: [UnresolvedField]) -> [ProcoreNote]   // pure: `_form` entries with a "key: " prefix become notes; a `_form` entry without a prefix stays the formMessage
static func fieldLabel(_ key: String) -> String   // "custom_field_123" → "Custom field 123"; known keys use RFIField.label
```

- [ ] **Step 1: Write the failing tests**: `testProcoreNotesFromPrefixedFormEntries`, `testUnprefixedFormEntryStaysFormMessage`, `testFieldLabelForCustomAndKnownKeys`, and an update to `testSubmit400FormAndUnknownNamesDoNotAddRows` asserting the note appears.
- [ ] **Step 2: Run** `RFIReviewModelTests` — expected: compile failure.
- [ ] **Step 3: Implement**; screenshot Review with a note.
- [ ] **Step 4: Run** the class and the full suite — expected: PASS.
- [ ] **Step 5: Commit** `feat(ios): Review shows Procore's required-field notes for fields without a row`

---

### Task 6: Build numbers and docs

**Files:**
- Modify: `ios/fastlane/Fastfile` (`build_and_upload` sets `increment_build_number(build_number: Time.now.utc.strftime("%Y%m%d%H%M"))` or passes `CURRENT_PROJECT_VERSION` via `xcargs`; a `changelog` from the last commit subject), `ios/project.yml` (`MARKETING_VERSION: "1.0"`, `CURRENT_PROJECT_VERSION: "1"` in base settings), `ios/FieldAssistant/Resources/Info.plist` (`CFBundleShortVersionString` → `$(MARKETING_VERSION)`, `CFBundleVersion` → `$(CURRENT_PROJECT_VERSION)`), `ios/FieldAssistantTests/SettingsViewTests.swift` (a test that the footer reads both keys), `CLAUDE.md` (status; code map rows for `App/AppSchema`, `Core/Scope/ScopeCache`, `FeaturesService`; conventions: reauth routing, build numbers), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 4 row: iOS implemented, date; the hardening follow-ups closed; the device-check list from the runbook referenced)

- [ ] **Step 1: Write the failing test** (`testSettingsFooterReadsVersionAndBuild`), run it, implement, run the class.
- [ ] **Step 2: `make ios-project`**, build the Prod configuration locally (`xcodebuild -scheme FieldAssistant -configuration Prod -sdk iphonesimulator build ...`) and confirm the built Info.plist carries the resolved values.
- [ ] **Step 3: Docs**; full suite once — expected: PASS.
- [ ] **Step 4: Commit** `feat(ios): date-stamped build numbers; docs for milestone 4 on the phone`
