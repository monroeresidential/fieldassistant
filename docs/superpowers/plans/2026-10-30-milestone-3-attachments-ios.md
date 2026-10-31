# Milestone 3 Attachments and Queued Submit: iOS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Photos are final at capture (2,500 px, EXIF stripped except orientation and GPS), travel to the backend with the draft's manifest, and Submit becomes an intent the phone keeps and completes on its own when signal returns, with every failure visible on the Drafts list and Review.

**Architecture:** `PhotoSaver` writes the finished JPEG through ImageIO off the main actor. `DraftService` learns the 1.6.0 contract (manifest on create, `PUT`/`DELETE` attachment). A new `SubmitRunner` in `Core/Drafts` does the ordered work for one draft (save the payload, upload pending photos, post submit, poll) and is shared by Review and `DraftSync`; a `submitPending` flag on `LocalDraft` is the queued intent, and reconcile skips drafts that still carry it. `BackgroundSync` registers the two iOS background tasks. Review, Drafts and Recent show queued and failed states; Settings gains the Photo size row.

**Tech Stack:** Swift 6 strict concurrency, SwiftUI, SwiftData, ImageIO, BackgroundTasks, swift-openapi-generator client (`FieldAssistantAPI` package), XCTest with `StubTransport`.

**Spec:** `docs/superpowers/specs/2026-10-29-milestone-3-attachments-offline-design.md` (sections "Photos on the phone", "Phone: Submit is an intent the phone keeps", "Settings"). Backend plan: `2026-10-30-milestone-3-attachments-backend.md` (contract 1.6.0 must be merged and synced into `ios/Packages/FieldAssistantAPI` before Task 2).

## Global Constraints

- Photos: long edge at most `2500` px, JPEG quality `0.85`, metadata kept: `kCGImagePropertyOrientation` and `kCGImagePropertyGPSDictionary` only. At most `10` photos per draft. File names stay `<photoUUID>.jpg` in `DraftFiles.directory(for: draftID)`.
- The phone never talks to Procore; uploads go to `PUT /drafts/{id}/attachments/{id}` with `Content-Type: image/jpeg` and `X-Company-Id`. The idempotency key for submit stays the lowercase draft id.
- Runner order is fixed: save payload (if dirty) → upload each pending photo, oldest first, one at a time → `POST /submit` → poll up to 60 s. A transport failure anywhere leaves `submitPending` set and stops; a definitive refusal clears it and persists `failed` with the server's message.
- `submitting` keeps its "Queued" label (design D9). New copy, exact: Drafts/Recent third line `"Queued · waiting for signal"` (pending, offline), `"Queued · sending"` (pass running), `"Queued"` (pending, online, idle); Review pending note `"Queued. It sends on its own when you have signal."`; failed photo tile title `"Couldn't send"`, action `"Remove"`; Stop link `"Stop sending"`, its already-sent message `"Already sent"`.
- Background task identifiers: `$(PRODUCT_BUNDLE_IDENTIFIER).draftsync.refresh` (`BGAppRefreshTask`) and `$(PRODUCT_BUNDLE_IDENTIFIER).draftsync.processing` (`BGProcessingTask`, `requiresNetworkConnectivity = true`, `requiresExternalPower = false`). `UIBackgroundModes: [fetch, processing]`.
- Strings, accessibility labels and identifiers that exist today stay unless this plan lists the new text. Design-system tokens and `Theme.Text` only; the failed tile reuses the flagged-row tokens (`Theme.Color.flaggedBackground`, accent text).
- Tests: `cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile` after `make ios-project`; `-only-testing:FieldAssistantTests/<Class>` while iterating. All 223 existing tests stay green.

## Review Focus

1. A HEIC photo from the library with an EXIF orientation of 6 (portrait shot held sideways): the saved JPEG must display upright in Review and in Procore, which means the orientation tag survives and the pixels are not rotated twice. Test in Task 1 (`testSaveKeepsOrientationTagWithoutRotatingPixels`).
2. The app is killed while the runner is between the photo uploads and the submit POST: on relaunch the draft must still be Queued and the runner must resume without re-uploading stored photos. Test in Task 3 (`testRelaunchMidQueueResumesWithoutReupload`).
3. The user removes a failed photo while offline, then signal returns: the runner must send the DELETE before the submit, and the submit must not include the removed photo in any expectation. Test in Task 5 (`testRemovedPhotoDeletedBeforeSubmit`).
4. A queued draft whose server copy was already submitted by an earlier lost POST: the runner's `POST /submit` answers 200 and the phone must treat that as accepted (not a refusal), clearing `submitPending` and deleting files. Test in Task 3 (`testSubmit200AlreadySubmittedIsAccepted`).
5. Signing out with a queued draft: the queue must not run under another user, and signing back in as the owner must resume it. Test in Task 3 (`testQueuedDraftOnlyRunsForItsOwner`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `ios/FieldAssistant/Core/Capture/PhotoSaver.swift` | ImageIO pipeline: downscale, strip, GPS write; `PhotoLimits`. |
| `ios/FieldAssistant/Core/Capture/CaptureModel.swift` | `addPhoto(data:)` / `addPhoto(image:metadata:)`, the 10-photo limit. |
| `ios/FieldAssistant/Features/RFI/CaptureView.swift` | Passes picker `Data` and camera `mediaMetadata`; hides the add tiles at 10. |
| `ios/FieldAssistant/Core/Drafts/LocalPhoto.swift` | `bytes`, `uploadState`, `failureMessage`. |
| `ios/FieldAssistant/Core/Drafts/LocalDraft.swift` | `submitPending`, `pendingPayloadData`, attachment-state apply. |
| `ios/FieldAssistant/Core/Drafts/DraftService.swift` | Manifest on create; `uploadAttachment`, `deleteAttachment`. |
| `ios/FieldAssistant/Core/Drafts/SubmitRunner.swift` | The ordered submit work for one draft. |
| `ios/FieldAssistant/Core/Drafts/DraftSync.swift` | Queued step; reconcile skips pending. |
| `ios/FieldAssistant/Core/Drafts/BackgroundSync.swift` | BG task registration, scheduling helper, handler. |
| `ios/FieldAssistant/Features/RFI/RFIReviewModel.swift` | Submit through the runner; Stop sending; failed photo removal. |
| `ios/FieldAssistant/Features/RFI/RFIReviewView.swift` | Queued read-only state, Stop link, failed tile. |
| `ios/FieldAssistant/Features/RFI/RecentDraftsView.swift`, `DraftsListView.swift` | Third-line copy. |
| `ios/FieldAssistant/App/AppState.swift`, `App/FieldAssistantApp.swift`, `App/SettingsView.swift` | Wiring, background scheduling, Photo size row. |
| `ios/project.yml` | Background modes and permitted identifiers. |
| `ios/FieldAssistantTests/PhotoSaverTests.swift`, `SubmitRunnerTests.swift`, `BackgroundSyncTests.swift`, plus additions to `DraftSyncTests`, `DraftServiceTests`, `RFIReviewModelTests`, `RecentDraftsViewTests`, `SettingsViewTests` | Tests per task. |

---

### Task 1: Photos are final at capture

**Files:**
- Modify: `ios/FieldAssistant/Core/Capture/PhotoSaver.swift`, `Core/Capture/CaptureModel.swift:119-126`, `Features/RFI/CaptureView.swift:456-470,757`, `Core/Drafts/LocalPhoto.swift`
- Create: `ios/FieldAssistantTests/PhotoSaverTests.swift`
- Test: existing `CaptureModelTests`

**Interfaces:**
- Produces:
  ```swift
  enum PhotoLimits { static let longEdge: CGFloat = 2500; static let jpegQuality: CGFloat = 0.85; static let maxPerDraft = 10 }
  enum PhotoSaverError: LocalizedError { case encodingFailed, tooManyPhotos }
  enum PhotoSaver {
      /// Library path: the picker's bytes, with whatever metadata they carry.
      nonisolated static func save(data: Data, draftID: UUID, location: CLLocationCoordinate2D?) throws -> LocalPhoto
      /// Camera path: the captured image and the picker's `mediaMetadata` (orientation lives there).
      nonisolated static func save(image: UIImage, metadata: [CFString: Any]?, draftID: UUID, location: CLLocationCoordinate2D?) throws -> LocalPhoto
      /// Pure: the properties dictionary written to the destination (orientation + GPS only).
      static func outputProperties(orientation: CGImagePropertyOrientation?, gps: [CFString: Any]?, location: CLLocationCoordinate2D?) -> [CFString: Any]
  }
  ```
  `LocalPhoto` gains `var bytes: Int64 = 0`, `var uploadStateRaw: String = "pending"` with `enum PhotoUploadState: String { case pending, stored, failed, removed }` and a typed `uploadState` accessor, `var failureMessage: String?`. `CaptureModel.addPhoto(data:)` and `addPhoto(image:metadata:)` are `async throws` and run the saver on a detached task; both throw `PhotoSaverError.tooManyPhotos` at 10.
- Algorithm the signature does not determine: `CGImageSourceCreateWithData` → read `kCGImagePropertyOrientation` and `kCGImagePropertyGPSDictionary` from the source properties (camera path: from `metadata`); `CGImageSourceCreateThumbnailAtIndex` with `kCGImageSourceCreateThumbnailFromImageAlways: true`, `kCGImageSourceThumbnailMaxPixelSize: 2500`, `kCGImageSourceCreateThumbnailWithTransform: false`; `CGImageDestinationCreateWithURL(..., UTType.jpeg.identifier, 1, nil)`, `CGImageDestinationAddImage(dest, thumb, outputProperties(...) as CFDictionary)` where the properties hold `kCGImageDestinationLossyCompressionQuality: 0.85`, the orientation and the GPS dictionary; a `location` without a GPS dictionary becomes one with `Latitude`, `LatitudeRef`, `Longitude`, `LongitudeRef`. Write atomically (temp file then rename), set `bytes` from the file size.

- [ ] **Step 1: Write the failing tests** in `PhotoSaverTests.swift`. A helper builds a 4000×3000 JPEG in memory via ImageIO with EXIF `Make: "Apple"`, `DateTimeOriginal`, orientation `6`, and a GPS dictionary. Tests: `testSaveDownscalesLongEdgeTo2500` (output pixel width ≤ 2500 and height proportional), `testSaveKeepsOrientationTagWithoutRotatingPixels` (output orientation property == 6 and pixel size is 2500×1875, not 1875×2500), `testSaveKeepsGPSAndDropsOtherEXIF` (GPS dictionary present with the same latitude; `kCGImagePropertyExifDictionary` has no `DateTimeOriginal`; TIFF `Make` absent), `testSaveCameraPhotoWritesDraftLocation` (image path with no GPS and `location = (41.89, -87.63)` → GPS dictionary with `LatitudeRef "N"`, `LongitudeRef "W"`), `testSaveLibraryPhotoWithoutGPSAndNoLocationHasNoGPS`, `testSaveSmallPhotoIsNotUpscaled` (800×600 stays 800×600), `testSaveSetsBytes`, `testOutputPropertiesContainOnlyOrientationGPSAndQuality`. In `CaptureModelTests`: `testEleventhPhotoIsRefused`.
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/PhotoSaverTests` — expected: compile failure (`save(data:...)` missing).
- [ ] **Step 3: Implement** the saver, the `LocalPhoto` fields, the async `addPhoto` variants, and the view changes: `addPicked` passes `data` (no `UIImage(data:)` round trip); `CameraPicker` passes `info[.originalImage]` and `info[.mediaMetadata] as? [CFString: Any]`; both call sites pass `model.coordinate`; the add tiles hide when `photos.count == PhotoLimits.maxPerDraft`. Keep every existing capture string.
- [ ] **Step 4: Run** `PhotoSaverTests` and `CaptureModelTests` — expected: PASS. Build the app; on the simulator add a library photo and confirm the thumbnail is upright.
- [ ] **Step 5: Commit** `feat(ios): photos are final at capture (2,500 px, EXIF stripped except orientation and GPS)`

---

### Task 2: Contract 1.6.0 on the phone: manifest and attachment calls

**Files:**
- Modify: `ios/FieldAssistant/Core/Drafts/DraftService.swift`, `Core/Drafts/LocalDraft.swift` (`apply` maps attachment states), `ios/FieldAssistantTests/DraftServiceTests.swift`, `DraftStoreTests.swift`
- Requires: `ios/Packages/FieldAssistantAPI` regenerated from contract 1.6.0 (`make sync-contract` on the merged backend branch; `make check-contract` green).

**Interfaces:**
- Produces:
  ```swift
  extension DraftService {
      /// PUT the photo file; 200 returns the server's attachment. Throws APIFailure: .validation for 400,
      /// .notFound for 404, .offline / .server for transport and 5xx, as the other calls do.
      func uploadAttachment(draftID: UUID, attachmentID: UUID, companyID: Int64, fileURL: URL) async throws -> Components.Schemas.Attachment
      func deleteAttachment(draftID: UUID, attachmentID: UUID, companyID: Int64) async throws
  }
  ```
  `APIFailure` gains `case notFound(String)` (404 on draft and attachment routes; `userMessage` is the server message). `create(_:)` sends `attachments: draft.photos.sorted(by: createdAt).map { .init(id: $0.id, filename: $0.fileName, content_type: .image_jpeg, bytes: $0.bytes) }` when the draft has photos. `LocalDraft.apply(_:)` sets each matching `LocalPhoto.uploadState` from `response.attachments` (`stored`/`uploaded` → `.stored`, `failed` → `.failed` with `error.message` as `failureMessage`, `pending` → leave as is) and ignores unknown ids.
- The upload body is `HTTPBody(try Data(contentsOf: fileURL))` with content type `image/jpeg` (a `Data`-backed body is replayable through `AuthMiddleware`'s one retry).

- [ ] **Step 1: Write the failing tests** in `DraftServiceTests.swift` with `StubTransport`: `testCreateSendsAttachmentManifestInCapturedOrder` (body JSON has `attachments` with ids, filenames, `image/jpeg`, bytes, in `createdAt` order), `testCreateWithoutPhotosOmitsAttachments`, `testUploadAttachmentPutsJPEGWithCompanyHeader` (method PUT, path `/v1/drafts/<id>/attachments/<id>`, `Content-Type: image/jpeg`, body bytes equal the file, `X-Company-Id`), `testUploadAttachment404ThrowsNotFound`, `testUploadAttachment400ThrowsValidationWithServerMessage`, `testDeleteAttachmentSendsDELETEAnd204Returns`. In `DraftStoreTests`: `testApplyMapsAttachmentStatesOntoPhotos` (two photos; response says one `stored`, one `failed` with a message).
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/DraftServiceTests -only-testing:FieldAssistantTests/DraftStoreTests` — expected: compile failure.
- [ ] **Step 3: Implement** the service methods, the `APIFailure.notFound` case (and its `userMessage`), the manifest on create, and `apply`.
- [ ] **Step 4: Run** the two classes — expected: PASS; full suite green.
- [ ] **Step 5: Commit** `feat(ios): contract 1.6.0: photo manifest on create; upload and delete attachment calls`

---

### Task 3: SubmitRunner and the queued intent

**Files:**
- Create: `ios/FieldAssistant/Core/Drafts/SubmitRunner.swift`, `ios/FieldAssistantTests/SubmitRunnerTests.swift`
- Modify: `Core/Drafts/LocalDraft.swift` (`submitPending`, `pendingPayloadData`), `Core/Drafts/DraftStore.swift` (`draftsPendingSubmit(owner:)`), `Core/Drafts/DraftSync.swift` (queued step, reconcile skip, `isRunning(draftID:)`), `App/AppState.swift` (build the runner, pass it to `DraftSync` and Review), `Features/RFI/RFIReviewModel.swift:181-251` (submit through the runner), `ios/FieldAssistantTests/DraftSyncTests.swift`, `RFIReviewModelTests.swift`

**Interfaces:**
- Produces:
  ```swift
  // LocalDraft
  var submitPending: Bool = false
  /// The reviewed payload still to PATCH (JSON `RFIDraftPayload` via LocalDraft.encoder); nil once saved.
  var pendingPayloadData: Data?

  // DraftStore
  func draftsPendingSubmit(owner: Int64) -> [LocalDraft]   // submitPending == true, oldest updatedAt first

  @MainActor final class SubmitRunner {
      enum Outcome: Equatable {
          case accepted(DraftResponse)          // server answered 200 or 202 (and the poll, if any, finished)
          case stillPending                     // transport failure; flag kept; stop the pass
          case refused(APIFailure)              // definitive; flag cleared; draft persisted failed
          case photoFailed(LocalPhoto)          // a PUT was refused for good; flag cleared; draft failed
      }
      init(store: DraftStore, service: @escaping @MainActor () -> DraftService)
      func run(_ draft: LocalDraft) async -> Outcome
      func isRunning(_ draftID: UUID) -> Bool
  }
  ```
  `run` order: (1) if `pendingPayloadData != nil` → `service.save` with it; success clears it; (2) for each photo with `uploadState == .removed` → `deleteAttachment`, then delete the `LocalPhoto` row and file; (3) for each photo `.pending`, oldest first → `uploadAttachment(fileURL: DraftFiles.photoURL(...))`; 200 → `.stored` with the server's `bytes`; 404 or 400 → `.failed` with the message, then stop with `.photoFailed` (flag cleared, draft `failed`, `errorMessage` = that message); (4) `service.submit`; `.submitted(response)` or `.running(jobID)` + `waitForJob` → `draft.apply`, `submitPending = false`; a `submitted` response deletes files (`DraftFiles.removeAll`); a poll error after 202 still clears the flag (the server owns the draft now) and returns `.accepted` with the draft as it stands; (5) `APIFailure.offline`/`.server` anywhere → `.stillPending`; `.validation`/`.permissionDenied`/`.reauthRequired`/`.notFound` on save or submit → `.refused`: `submitPending = false`, `status = .failed`, `errorMessage = failure.userMessage`, then a best-effort `service.get` whose response is applied (so `unresolved[]` lands in `serverData`). Every state change is followed by `store.save()`.
- `DraftSync.performPass` becomes: `resendWaiting` → `runQueued` (runner for each `draftsPendingSubmit(owner:)`, stop the pass on `.stillPending`) → `reconcileSubmitting`, which now skips drafts with `submitPending == true`. `DraftSync.init` gains `runner: SubmitRunner`.
- `RFIReviewModel.submit()` becomes: `persist()` writes the payload locally to `pendingPayloadData` **before** PATCHing (a failed PATCH leaves it set); then `draft.status = .submitting; draft.submitPending = true; draft.updatedAt = .now; store.save()`; then `runner.run(draft)` and map: `.accepted` → the existing `take(response)` switch; `.stillPending` → `.pending(message: Self.queuedMessage)` where `queuedMessage = "Queued. It sends on its own when you have signal."`; `.refused(f)` → `.needsFixes` when `flagFields(from:)` finds fields, else `.failed(f)`; `.photoFailed` → `.needsFixes` (the failed tile is the fix). `connectionLostMessage` is no longer used by submit (keep the constant if Check still uses it).

- [ ] **Step 1: Write the failing tests** in `SubmitRunnerTests.swift` (StubTransport scripts, in-memory SwiftData container as other draft tests do): `testRunOrderSavePhotosSubmitPoll` (requests in order: PATCH, PUT a, PUT b, POST submit, GET job), `testRunSkipsStoredPhotos`, `testRunStopsOnOfflineAndKeepsPending` (PUT a fails offline → `.stillPending`, flag set, photo a still `.pending`), `testRunRefusal400PersistsFailedWithMessageAndFetchesDraft`, `testRunPhotoPut400MarksPhotoFailedAndStops` (no POST sent), `testRunPhotoPut404MarksPhotoFailed`, `testSubmit200AlreadySubmittedIsAccepted` (POST → 200 with a submitted draft; flag cleared; files removed), `testPollErrorAfter202ClearsPendingAndReturnsAccepted`, `testRelaunchMidQueueResumesWithoutReupload` (first run: PUT a ok, PUT b offline; new runner on a fresh store load: only PUT b, POST, GET). In `DraftSyncTests`: `testQueuedDraftRunsWhenReachable`, `testReconcileSkipsPendingDraft` (server says `reviewed` but the flag is set → status stays `submitting`), `testQueuedDraftOnlyRunsForItsOwner`, `testQueuedRunStopsPassWhenOffline`. In `RFIReviewModelTests`: `testSubmitOfflineQueuesAndReturnsQueuedMessage` (PATCH offline → draft `submitting`, `submitPending`, `pendingPayloadData` set, outcome `.pending(queuedMessage)`), `testSubmitRefusalShowsFixAndPersistsFailed`, `testSubmitAcceptedPathUnchanged` (the existing accepted assertions hold through the runner).
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/SubmitRunnerTests -only-testing:FieldAssistantTests/DraftSyncTests -only-testing:FieldAssistantTests/RFIReviewModelTests` — expected: compile failure.
- [ ] **Step 3: Implement** the model fields, the store query, the runner, the sync changes, the review rewrite, and the `AppState` wiring (`startDraftSync` builds one `SubmitRunner` and hands it to both `DraftSync` and the review screens through `AppState`).
- [ ] **Step 4: Run** the three classes, then the full suite — expected: PASS.
- [ ] **Step 5: Commit** `feat(ios): Submit is a queued intent; one runner saves, uploads photos, submits and polls`

---

### Task 4: Background tasks

**Files:**
- Create: `ios/FieldAssistant/Core/Drafts/BackgroundSync.swift`, `ios/FieldAssistantTests/BackgroundSyncTests.swift`
- Modify: `ios/project.yml` (Info `UIBackgroundModes`, `BGTaskSchedulerPermittedIdentifiers`), `App/FieldAssistantApp.swift` (register at launch; schedule on `.background`), `App/AppState.swift` (`enteredBackground()`, `runBackgroundPass()`)

**Interfaces:**
- Produces:
  ```swift
  @MainActor final class BackgroundSync {
      static let refreshID = "\(Bundle.main.bundleIdentifier!).draftsync.refresh"
      static let processingID = "\(Bundle.main.bundleIdentifier!).draftsync.processing"
      /// Pure: schedule when any draft is waiting or queued.
      static func shouldSchedule(waiting: Int, pendingSubmit: Int) -> Bool
      init(run: @escaping @MainActor () async -> Void)   // one DraftSync pass
      func register()                                      // BGTaskScheduler.shared.register for both ids
      func schedule(waiting: Int, pendingSubmit: Int)      // submits both requests when shouldSchedule; earliestBeginDate 15 s; processing requiresNetworkConnectivity = true
      func cancelAll()
  }
  ```
  The handler for each task: `task.expirationHandler` cancels the pass (`draftSync.stop()` then `start()` semantics: cancel the running pass without recording); run one pass; `setTaskCompleted(success: true)`; reschedule if work remains. `AppState.enteredBackground()` counts `drafts.drafts(withStatus: .waiting, owner:)` and `draftsPendingSubmit(owner:)` and calls `schedule`.
- `project.yml` Info gains `UIBackgroundModes: [fetch, processing]` and `BGTaskSchedulerPermittedIdentifiers: [$(PRODUCT_BUNDLE_IDENTIFIER).draftsync.refresh, $(PRODUCT_BUNDLE_IDENTIFIER).draftsync.processing]`.

- [ ] **Step 1: Write the failing tests** in `BackgroundSyncTests.swift`: `testShouldScheduleOnlyWithWork` (`(0,0) == false`, `(1,0)`, `(0,1)`, `(2,3) == true`), `testIdentifiersUseBundleID` (both end with `.draftsync.refresh` / `.draftsync.processing` and start with the test bundle's host app id).
- [ ] **Step 2: Run** `-only-testing:FieldAssistantTests/BackgroundSyncTests` — expected: compile failure.
- [ ] **Step 3: Implement** the class, the registration in `FieldAssistantApp.init` (before the scene is created, as BackgroundTasks requires), `scenePhase == .background` → `appState.enteredBackground()`, and the `project.yml` entries. `make ios-project` and build.
- [ ] **Step 4: Run** the class and the full suite — expected: PASS. On the simulator with a queued draft, background the app and trigger the task with `e -l objc -- (void)[[BGTaskScheduler sharedScheduler] _simulateLaunchForTaskWithIdentifier:@"tools.fieldassistant.app.draftsync.processing"]` in the Xcode debugger (Local build's bundle id); confirm one pass runs.
- [ ] **Step 5: Commit** `feat(ios): background refresh and processing tasks run the draft queue`

---

### Task 5: Queued and failed states on screen

**Files:**
- Modify: `Features/RFI/RecentDraftsView.swift:77-95` (`DraftRowMeta.line` gains `queued: QueuedState?`), `Features/RFI/DraftsListView.swift` (same line), `Features/RFI/RFIReviewView.swift` (queued read-only state, Stop link, failed photo tile), `Features/RFI/RFIReviewModel.swift` (`stopSending()`, `removePhoto(_:)`, `isQueued`), `Core/Drafts/SubmitRunner.swift` (the `.removed` DELETE step is in Task 3; nothing new), `ios/FieldAssistantTests/RecentDraftsViewTests.swift`, `RFIReviewModelTests.swift`, `SubmitRunnerTests.swift`

**Interfaces:**
- Produces:
  ```swift
  enum QueuedState: Equatable { case waitingForSignal, sending, queued }
  // DraftRowMeta
  static func line(feature: String, capturedAt: Date, photos: Int, queued: QueuedState?, now: Date, calendar: Calendar = .current) -> String
  // → existing text, then " · Queued · waiting for signal" / " · Queued · sending" / " · Queued" when queued != nil (the builder keeps today's separator style)
  // RFIReviewModel
  var isQueued: Bool { draft.submitPending }
  enum StopOutcome: Equatable { case stopped, alreadySent, sending }
  func stopSending() async -> StopOutcome
  func removePhoto(_ photo: LocalPhoto)     // marks .removed locally (file deleted now); the runner sends the DELETE
  ```
  `stopSending`: `.sending` when `runner.isRunning(draft.id)`; with signal, `service.get` first: server `submitting`/`submitted` → apply, `.alreadySent`; otherwise (or offline) → `submitPending = false`, `pendingPayloadData` kept, `status = .draft`, save, `.stopped`. `QueuedState` for a row: `submitPending && !reachable` → `.waitingForSignal`; `runner.isRunning` → `.sending`; else `.queued`.
- Review while `isQueued`: rows disabled, footer shows the Queued tag and the `"Stop sending"` `TextLinkButtonStyle` link; `"Already sent"` shown as the footer note on that outcome. A photo tile with `uploadState == .failed` draws the flagged tokens, `"Couldn't send"`, the `failureMessage` in `meta12`, and a `"Remove"` text link.

- [ ] **Step 1: Write the failing tests**: `RecentDraftsViewTests.testRowMetaQueuedStates` (three strings), `RFIReviewModelTests.testStopSendingWhileRunningIsRefused`, `testStopSendingAsksServerAndReportsAlreadySent`, `testStopSendingOfflineClearsFlagAndUnlocks`, `testRemovePhotoMarksRemovedAndDeletesFile`; `SubmitRunnerTests.testRemovedPhotoDeletedBeforeSubmit` (DELETE then POST; the deleted `LocalPhoto` row is gone after).
- [ ] **Step 2: Run** the three classes — expected: compile failure.
- [ ] **Step 3: Implement** the model methods, the row meta, and the Review UI; take light and dark screenshots of Review queued, Review with a failed photo, and the Drafts list with a queued row to `~/Desktop/fieldassistant-m3/`.
- [ ] **Step 4: Run** the classes and the full suite — expected: PASS.
- [ ] **Step 5: Commit** `feat(ios): queued and failed-photo states in Review, Drafts and Recent; Stop sending`

---

### Task 6: Settings row, docs and the device check

**Files:**
- Modify: `App/SettingsView.swift` (Submitting section gains the row `"Photo size"` / `"Long edge, before upload"` / value `"2,500 px"` formatted from `PhotoLimits.longEdge` with the grouping separator, `meta` style tabular), `ios/FieldAssistantTests/SettingsViewTests.swift` (`testPhotoSizeValueFormatsLongEdge` on a pure `SettingsView.photoSizeValue(_:)` helper → `"2,500 px"` in `en_US`), `CLAUDE.md` (status: milestone 3 attachments and queued submit implemented on `m3-attachments-offline`, device check pending; code map rows for `Core/Capture` and `Core/Drafts` mention the ImageIO pipeline, `SubmitRunner` and `BackgroundSync`; conventions: `submitting` plus `submitPending` is the queued intent and reconcile skips it), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 3 row: implemented dates, the 2b "Photo size" note resolved, the device-check list below)

- [ ] **Step 1: Write the failing test**, run it (`-only-testing:FieldAssistantTests/SettingsViewTests`), expected FAIL (no helper).
- [ ] **Step 2: Implement** the helper and the row; run the class, expected PASS; full suite green.
- [ ] **Step 3: Docs** as listed. The roadmap's device-check list for Drew (Dev build on the iPhone): airplane mode on → dictate with three photos (one from the library, two from the camera) → Submit → Queued; airplane mode off in the foreground → the RFI appears in Procore within 30 s with three upright photos, once; repeat with the app backgrounded and note how long iOS took; remove a failed photo and resubmit (force a failure by uploading a non-JPEG via the debug build or by deleting the file); Settings shows the Photo size row; `exiftool` on a downloaded attachment shows only Orientation and GPS beyond the JPEG basics.
- [ ] **Step 4: Commit** `docs: milestone 3 attachments and queued submit on the phone; Settings photo size`
