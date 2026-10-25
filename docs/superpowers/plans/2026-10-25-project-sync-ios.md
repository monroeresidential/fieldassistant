# Project Sync (iOS) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The phone holds every RFI of the selected project in SwiftData, syncs deltas from the backend on foreground, project selection, pull-to-refresh and a Sync button, and shows a local RFI list and a full RFI detail that work with no signal.

**Architecture:** `Core/Records` mirrors `Core/Lookups` and `Core/Drafts`: `CachedRecord` and `ProjectSyncState` models, a `RecordService` over the generated client, a `RecordStore` for queries and page application, and a `RecordSync` driver owned by `AppState`. Screens read the store, never the network. The last task removes the old live RFI list and its contract endpoint.

**Tech Stack:** Swift 6, SwiftUI, SwiftData (iOS 17), swift-openapi-generator client, QuickLook for attachments.

**Spec:** `docs/superpowers/specs/2026-10-25-project-sync-design.md`. Backend: `2026-10-25-project-sync-backend.md` (contract 1.5.0 must be merged and deployed to dev before Task 2's device check; unit tests do not need it).

## Global Constraints

- Only the backend talks to Procore; the phone calls `getRecords`, `syncProject` and `getRFIAttachment` only.
- Records and sync state are cleared on sign-out (like lookups); drafts are untouched.
- Sync runs only for the selected project; it is cancelled on project change and in `AppState.endSession` (the session epoch pattern from `loadMe` applies: a page that arrives after sign-out is dropped).
- Every screen shows sync status honestly: "Synced <relative time>", a spinner while syncing, "Sync failed, showing what you have" with Retry; a `pull.status == "failed"` with `code == "procore_permission_denied"` shows "No RFI access on this project" on the list (the backend soft-deletes the user's rows on a 403, so they arrive in `deleted_ids` and the list empties).
- Delta pages are applied in one `ModelContext.save()` per page; no per-row saves.
- Copy and styling use the handoff tokens (`docs/design/2026-10-23-ios-handoff/`) with plain `List` rows; the design pass restyles later.
- The generated Swift type names in this plan (`Components.Schemas.RecordPage`, `.RFIRecord`, `Operations.getRecords`, `Operations.syncProject`, `Operations.getRFIAttachment`) are the generator's defaults for contract 1.5.0; the Task 1 implementer confirms them against `ios/Packages/FieldAssistantAPI` and reports any difference.
- Tests run with `cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile -only-testing:FieldAssistantTests/<Class>` after `make ios-project`.

## Review Focus

1. Airplane mode at launch: the RFI list and a detail render from local rows at once, with "Sync failed" or the last synced time, and no spinner that never ends. Test in Task 3 (`testRunOnceOfflineKeepsRowsAndReportsFailure`).
2. Project switch mid-sync: the old project's remaining pages are cancelled and the new project syncs from its own cursor; no row of project A is ever written under project B. Test in Task 3.
3. Sign-out mid-sync: the in-flight page is dropped and `clearAll` leaves zero rows. Test in Task 3.
4. A record body with unexpected nulls or extra fields decodes (optional fields nil, arrays empty), never failing the whole page. Test in Task 1 (`testDecodeRFIRecordWithSparseBody`).
5. Opening an attachment with no signal shows "Needs signal" instead of a failed download sheet. Test in Task 4 (view model only).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `ios/FieldAssistant/Core/Records/CachedRecord.swift` | `@Model` row: key, project, kind, procore id, sortable columns, body data. |
| `ios/FieldAssistant/Core/Records/ProjectSyncState.swift` | `@Model` per project and kind: cursor, last synced, last error. |
| `ios/FieldAssistant/Core/Records/RecordService.swift` | Client wrapper: `page(...)`, `sync(...)`, `attachmentData(...)`. |
| `ios/FieldAssistant/Core/Records/RecordStore.swift` | Queries and `apply(page:)`, `clearAll()`, `state(projectID:kind:)`. |
| `ios/FieldAssistant/Core/Records/RecordSync.swift` | Drives paging; observable `SyncStatus`. |
| `ios/FieldAssistant/Features/RFI/RFIRecordListView.swift` | Local list with search and filters. |
| `ios/FieldAssistant/Features/RFI/RFIRecordDetailView.swift` | All fields, questions, answers, attachments. |
| `ios/FieldAssistant/Features/RFI/AttachmentViewer.swift` | Download through the backend and QuickLook. |
| `ios/FieldAssistant/App/HomeView.swift`, `AppState.swift`, `AppContainer.swift` | Header sync status and button, sync lifecycle, schema. |
| `ios/FieldAssistantTests/Record*.swift` | Tests per task. |

---

### Task 1: Models, service and decoding

**Files:**
- Create: `Core/Records/CachedRecord.swift`, `Core/Records/ProjectSyncState.swift`, `Core/Records/RecordService.swift`
- Modify: `App/AppContainer.swift` (schema adds the two models)
- Test: `FieldAssistantTests/RecordServiceTests.swift`

**Interfaces:**
- Produces:

```swift
typealias RecordPage = Components.Schemas.RecordPage
typealias RFIRecord = Components.Schemas.RFIRecord
typealias PullStatus = Components.Schemas.PullStatus

enum RecordKind: String { case rfi }

@Model final class CachedRecord {
    @Attribute(.unique) var key: String        // "<projectID>/<kind>/<procoreID>"
    var projectID: Int64; var kind: String; var procoreID: Int64
    var changeSeq: Int64; var updatedAt: Date; var bodyData: Data
    var number: String; var numberSort: Int64   // numeric part of number for sorting, 0 when none
    var subject: String; var status: String; var ballInCourt: String; var dueDate: Date?
    init(projectID:kind:procoreID:changeSeq:updatedAt:bodyData:number:subject:status:ballInCourt:dueDate:)
    var rfi: RFIRecord?                          // decodes bodyData; nil when it fails
    static func key(projectID: Int64, kind: RecordKind, procoreID: Int64) -> String
}

@Model final class ProjectSyncState {
    @Attribute(.unique) var key: String        // "<projectID>/<kind>"
    var projectID: Int64; var kind: String
    var nextSince: Int64; var lastSyncedAt: Date?; var lastError: String?; var lastErrorCode: String?
}

struct RecordService {
    let client: Client
    func page(companyID: Int64, projectID: Int64, kind: RecordKind, since: Int64, force: Bool) async throws -> RecordPage
    func sync(companyID: Int64, projectID: Int64, kinds: [RecordKind], full: Bool) async throws -> [PullStatus]
    func attachmentData(companyID: Int64, projectID: Int64, rfiID: Int64, attachmentID: Int64) async throws -> (Data, filename: String?, contentType: String?)
}
```

`page` maps 400/401/403/502/default to `APIFailure` as `LookupService.fetch` does. `attachmentData` collects the `HTTPBody` up to 50 MB and reads `Content-Disposition` for the filename.

- [ ] **Step 1: Write the failing tests**

```swift
func testPageSendsSinceLimitAndNoCache()        // StubTransport records the request: path /v1/projects/7/records/rfi, query since=42&limit=500, header Cache-Control: no-cache when force
func testPageDecodesRecordsAndDeletedIDs()      // scripted RecordPage JSON with one rfi and deleted_ids [9]; assert fields incl. questions[0].answers[0].plain_text_body and attachments[0].url
func testDecodeRFIRecordWithSparseBody()        // body with only required fields and "assignees": [] → decodes; optional refs nil
func testSyncPostsKindsAndFull()                // body {"kinds":["rfi"],"full":true}
func testAttachmentDataReadsFilename()          // Content-Disposition: attachment; filename="col.jpg" → filename "col.jpg"
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass**

Run: the test command with `-only-testing:FieldAssistantTests/RecordServiceTests`.

- [ ] **Step 3: Commit**

```bash
git commit -am "feat(ios): record models and service for the project mirror"
```

---

### Task 2: Store and page application

**Files:**
- Create: `Core/Records/RecordStore.swift`
- Test: `FieldAssistantTests/RecordStoreTests.swift`

**Interfaces:**
- Produces:

```swift
@MainActor final class RecordStore {
    init(context: ModelContext)
    func state(projectID: Int64, kind: RecordKind) -> ProjectSyncState?          // nil before the first sync
    func apply(_ page: RecordPage, projectID: Int64, kind: RecordKind) throws     // upserts records, deletes deleted_ids, advances nextSince, sets lastSyncedAt from page.synced_at, lastError from page.pull when failed; one save
    func markFailed(projectID: Int64, kind: RecordKind, code: String?, message: String) throws
    func records(projectID: Int64, kind: RecordKind, query: String, filter: RecordFilter) -> [CachedRecord]  // sorted numberSort desc, then number
    func record(projectID: Int64, kind: RecordKind, procoreID: Int64) -> CachedRecord?
    func clearAll() throws
}
enum RecordFilter: Equatable { case all, status(String), ballInCourt(userName: String) }
```

`records(query:)` matches `number` or `subject` case-insensitively; `status(...)` compares lowercased status; `ballInCourt(userName:)` matches `ballInCourt`.

- [ ] **Step 1: Write the failing tests** (in-memory container via `AppContainer.makeModelContainer(inMemory: true)`)

```swift
func testApplyInsertsUpdatesAndDeletes()        // page1: A(seq 1), B(seq 2); page2: A changed (seq 3), deleted_ids [B] → one A with new subject, no B, nextSince 3
func testApplyIsOneSavePerPage()                // 300 records → store.context.hasChanges false after apply; rows == 300
func testRecordsSortedByNumberDescending()      // numbers "12","9","RFI-10","3" → 12, RFI-10, 9, 3
func testQueryMatchesNumberOrSubject()
func testStatusAndBallInCourtFilters()
func testClearAllRemovesRecordsAndState()
func testApplyFailedPullKeepsRowsAndRecordsError()  // pull.status failed code procore_permission_denied with deleted_ids listing the rows → rows removed (they arrive in deleted_ids), state.lastErrorCode set
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass**

- [ ] **Step 3: Commit**

```bash
git commit -am "feat(ios): record store applies delta pages and serves the local list"
```

---

### Task 3: Sync driver and app lifecycle

**Files:**
- Create: `Core/Records/RecordSync.swift`
- Modify: `App/AppState.swift` (`recordSync`, `startRecordSync()`, stop in `endSession`, clear in sign-out alongside `lookups.clearAll()`, run in `becameActive`, restart on project selection, `syncRecords(force:full:)` public entry)
- Test: `FieldAssistantTests/RecordSyncTests.swift`, `FieldAssistantTests/AppStateTests.swift` (additions)

**Interfaces:**
- Produces:

```swift
@MainActor @Observable final class RecordSync {
    enum Status: Equatable { case idle, syncing, synced(Date), failed(code: String?, message: String) }
    private(set) var status: Status
    init(store: RecordStore, service: @escaping () -> RecordService, companyID: Int64, projectID: Int64, kinds: [RecordKind], reachability: any ReachabilityProviding)
    func runOnce(force: Bool = false, full: Bool = false) async   // re-entrant calls coalesce: a second call while running waits for the first and returns
    func stop()                                                    // cancels the running task
}
```

`runOnce`: when `full` or `force`, call `service.sync(kinds:full:)` first, then page `service.page(since: state.nextSince, force: false)` until `has_more == false`, applying each page; the first page of a project with no state uses `since: 0`. On `APIFailure.offline` or any error: `store.markFailed` and `status = .failed`; rows stay. When reachability is off and `force` is false, skip the network and set `.failed(code: "offline", message: "No signal")` only when there is no `lastSyncedAt`; otherwise leave `.synced(lastSyncedAt)`.

`AppState`: `recordSync` starts when `phase == .signedIn` and a project is selected (same place `startDraftSync` and `prefetchLookups` run), with `runOnce()` immediately; `selectProject` stops and restarts it; `becameActive` calls `recordSync?.runOnce()`; `endSession` stops it and sets it nil; sign-out clears records with `RecordStore(context:).clearAll()` next to `lookups.clearAll()`; `func syncRecords(force: Bool, full: Bool) async` forwards to `recordSync`. Returning from the Result screen calls `syncRecords(force: true, full: false)` (Task 4 wires the call).

- [ ] **Step 1: Write the failing tests**

```swift
func testRunOncePagesUntilDone()                       // transport serves 3 pages has_more true,true,false → 7 rows, status synced, state.nextSince == last change_seq
func testRunOnceCoalescesConcurrentCalls()             // gated transport: two runOnce calls → one page request
func testRunOnceOfflineKeepsRowsAndReportsFailure()    // rows present; transport throws URLError(.notConnectedToInternet) → rows unchanged; status failed; lastSyncedAt unchanged
func testForceCallsSyncEndpointFirst()                 // requests[0] is POST /sync, then GET records
func testStopCancelsMidPaging()                        // gate page 2; stop(); release → page 3 never requested; rows from page 1 only
func testProjectSwitchMidSyncWritesNothingUnderNewProject()   // AppStateTests: select project B while A's page is gated; release; records(projectID: B) empty; A rows exist
func testSignOutMidSyncDropsPageAndClears()            // AppStateTests: gate page; signOut; release; zero CachedRecord rows
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass**

Run with `-only-testing:FieldAssistantTests/RecordSyncTests -only-testing:FieldAssistantTests/AppStateTests`.

- [ ] **Step 3: Commit**

```bash
git commit -am "feat(ios): record sync on foreground, project selection and sign-in; cleared on sign-out"
```

---

### Task 4: Screens: header, list, detail, attachments

**Files:**
- Create: `Features/RFI/RFIRecordListView.swift`, `Features/RFI/RFIRecordDetailView.swift`, `Features/RFI/AttachmentViewer.swift`
- Modify: `App/HomeView.swift` (sync header section replaces the "Procore RFIs" row's destination; `Route.rfi(Int64)` added; Result → Home pop triggers `syncRecords(force: true)`), `Features/RFI/RFIResultView.swift` only if the pop needs a hook
- Test: `FieldAssistantTests/AttachmentViewerModelTests.swift`

**Interfaces:**
- Consumes: `RecordStore`, `RecordSync.Status`, `AppState.syncRecords(force:full:)`, `RecordService.attachmentData`.
- Produces: `@Observable final class AttachmentViewerModel { enum State { case idle, needsSignal, loading, ready(URL), failed(String) }; func open(reachable: Bool) async }`.

Screens:
- **Home header** (a `Section` above Record): project name, one line of status from `recordSync?.status` ("Synced 2 min ago" via `RelativeDateTimeFormatter`, "Syncing…", "Sync failed, showing what you have" + Retry button, "No RFI access on this project" for `procore_permission_denied`), and a "Sync" button (disabled while syncing) calling `syncRecords(force: true, full: false)`; a long-press menu item "Full resync" calls `full: true`. Accessibility labels on both.
- **RFI list** (`RFIRecordListView(projectID:)`): `.searchable` over number and subject; a filter menu with All, Open, Closed, Draft, "Ball in my court" (uses the signed-in user's name from `AppState`; the implementer uses whatever property holds the `/me` name); rows: `#number`, subject (2 lines), status tag, ball in court name, due date; `.refreshable { await appState.syncRecords(force: true, full: false) }`; empty states "No RFIs yet" and "No RFI access" and "Not synced yet. Pull to sync." Navigation to `Route.rfi(procoreID)`.
- **RFI detail** (`RFIRecordDetailView(projectID:procoreID:)`): reads the record from the store; sections Overview (number, subject, status, ball in court, due, created by/at, RFI manager, assignees, received from, responsible contractor, distribution), Where (location, spec section, cost code, drawing number), Impacts (cost, schedule), Questions (each question body, date, author; attachments rows; answers in order with their attachments), Field notes if the question body carries a "Field notes:" section (plain text, no parsing), and "Open in Procore" using `link`. Missing optional fields are omitted, not shown as dashes.
- **Attachments** (`AttachmentViewer`): a row tap with no reachability shows an inline "Needs signal" caption; with reachability it downloads via `attachmentData` to a temp file named by the attachment's filename and presents `QLPreviewController` in a sheet; failures show the message inline.

- [ ] **Step 1: Write the failing view-model test**

```swift
func testOpenWithoutSignalNeedsSignal()   // reachable false → state .needsSignal; service not called
func testOpenDownloadsToTempFile()        // scripted bytes → state .ready(url); file exists with filename
```

- [ ] **Step 2: Implement the screens and the model; run the test; build for the simulator**

Run: the test command with `-only-testing:FieldAssistantTests/AttachmentViewerModelTests`, then `xcodebuild build -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile`.

- [ ] **Step 3: Walkthrough on the simulator against api-dev (Dev configuration) or a local backend on contract 1.5.0**

Sign in, select the sandbox project, confirm the header shows "Syncing…" then "Synced just now", the list shows RFIs 182885, 182895, 182897 with status tags, search "column" finds the twisted-column RFI, the detail shows questions and the field notes, "Open in Procore" opens the link. Record the result in the SDD ledger.

- [ ] **Step 4: Commit**

```bash
git commit -am "feat(ios): local RFI list and detail from the project mirror with sync header"
```

---

### Task 5: Remove the live RFI list and its endpoint

**Files:**
- Modify: `api/openapi.yaml` (remove `/projects/{projectId}/rfis` and `RFISummary`; version `1.5.1`), `api/internal/httpapi/gen.go` (generate), `ios/Packages/FieldAssistantAPI/.../openapi.yaml` (sync)
- Delete: `api/internal/httpapi/scope_handlers.go` `ListRFIs` and `rfiSummary` plus their tests; `ios/FieldAssistant/Features/RFI/RFIListModel.swift`, `RFIListView.swift`, `ios/FieldAssistantTests/RFIListModelTests.swift`
- Keep: `procore.Client.ListRFIs` only if something else uses it; otherwise delete it and its test.

- [ ] **Step 1: Edit the contract, generate, sync, delete the code, build both sides**

Run: `make generate && make sync-contract && make check-contract && cd api && go build ./... && go test ./internal/httpapi/ ./internal/procore/`, then the iOS build.

- [ ] **Step 2: Run the full iOS suite**

Run: the test command without `-only-testing`. Expected: `** TEST SUCCEEDED **`.

- [ ] **Step 3: Commit**

```bash
git commit -am "feat(contract): 1.5.1 removes the live RFI list; the phone reads the mirror"
```

---

### Task 6: Docs and device check

**Files:**
- Modify: `CLAUDE.md` (code map: `Core/Records`; status line), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 3 row)

- [ ] **Step 1: Update docs and commit**

```bash
git commit -am "docs: project sync on the phone; code map and roadmap"
```

- [ ] **Step 2: Device check (Drew, Dev build on the iPhone)**

Install the Dev build. Open the app on the sandbox project: header syncs; list shows the RFIs. Airplane mode on, kill and reopen the app: list and a detail render, header says when it last synced. Airplane mode off, tap Sync: header updates. In Procore web, create an RFI, tap Sync: it appears; delete it, long-press Sync → Full resync: it disappears. Submit an RFI from the phone and return Home: it is in the list. Record outcomes in the ledger and the roadmap row.
