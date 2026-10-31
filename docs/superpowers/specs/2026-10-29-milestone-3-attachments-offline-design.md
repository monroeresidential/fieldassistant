# Photos to Procore and submit without signal (milestone 3, remainder)

Date: 2026-10-29. Status: draft for Drew's review. Written from the spec and the milestone 3 row without a question round (Drew opted out on 2026-10-29); every choice that is not the spec's is marked "(my call)".

## Intent

Milestone 3 has two exit criteria left: photos taken on the phone reach the RFI in Procore through Direct File Upload, and an RFI submitted with no signal lands in Procore exactly once when signal returns. Today photos stay on the phone and are deleted when the RFI is submitted, and Submit needs signal at the moment of the tap: with none at all the tap fails with an error and the draft stays a draft; if the link drops between saving the payload and posting the submit, the phone marks the draft as sending, never sends it, and the next reconcile quietly turns it back into a draft after 90 seconds. Both are things a superintendent in a basement will hit in the first week of the pilot.

This design adds the photo path end to end, turns Submit into a queued intent the phone keeps until the server has accepted it, and closes the Settings "Photo size" row the design pass left open.

## Goals

- A photo taken or picked during capture ends up attached to the RFI's question in Procore, downscaled to at most 2,500 px on the long edge, with every EXIF tag stripped except orientation and GPS.
- Submit works with no signal. The phone shows the draft as Queued, sends it as soon as there is a connection, and the RFI is created once. In the foreground the send starts within seconds of signal returning; with the app in the background it runs when iOS gives the app a background slot.
- A draft never produces two RFIs, however many times the phone retries, crashes, or is relaunched. The server's existing idempotency (draft UUID as the key, claim rows, lost-response recovery) stays the single guarantee; the phone only has to retry.
- The draft's photos are never silently dropped. If a photo cannot be sent, the user sees it on the review screen and chooses to remove it or keep trying.
- The pieces are generic: observations and punch items reuse the attachment table, endpoints, Procore push and the phone's queue without changes.

## Non-goals

- Uploading audio. Audio stays on the phone until the draft is submitted, then is deleted, as decided for milestone 2.
- Video, PDFs or anything but JPEG photos. The phone converts every picked image (HEIC included) to JPEG before upload.
- Adding or removing photos after capture, other than removing one that failed to send.
- Background URLSession transfers that continue after the app is suspended. The queue runs in the foreground and in a `BGProcessingTask`; a background URLSession is listed under "Later".
- The web client. It will call the same endpoints later; nothing here is phone-only by design.
- Showing uploaded photos from Procore on the phone's RFI detail beyond what the mirror already does (attachment metadata and the download proxy).

## Anti-goals

- Two RFIs from one tap. This is the one failure the pilot cannot survive.
- A Queued draft that is actually dead: the Drafts list and Review must say when a draft is waiting for signal, when it is sending, and when it has failed and needs the user.
- Photos reaching Procore at full resolution with the phone's location history inside them, or arriving with a lost orientation.
- The backend holding photos forever: objects are deleted once Procore has them and swept by a bucket lifecycle rule if a draft is abandoned.
- A change to the submit path's existing guarantees. The claim, revalidate, attempt-marker and `findCreated` logic are untouched; attachments are pushed before the existing flow and the RFI body gains one field.

## Constraints

- Only the backend talks to Procore (spec invariant). The phone uploads photos to our API; the backend pushes them to Procore with the submitting user's token.
- Procore Direct File Upload, non-segmented flow (files under 5 MB): `POST /rest/v1.1/projects/{project_id}/uploads` with `{response_filename, response_content_type}` returns `{uuid, url, fields}`; the file is then sent as a multipart form `POST` to `url` with every `fields` entry plus the file under the key `file`, last, with no Procore token. The upload must start within one hour of creation, is owned by the authenticated user (only that user can use it), and is deleted by Procore if not associated with a resource within one week. `POST /rest/v1.0/projects/{id}/rfis` accepts `rfi.question.upload_ids: [uuid]` (strings, not integers). The `url` and `fields` vary by company and over time and must never be hard-coded.
- The RFI create keeps `run_configurable_validations=true` and the 422/400 field mapping.
- `company_id` on every table and `Procore-Company-Id` on every Procore call.
- Contract changes bump `info.version`, regenerate both clients and ship with a new app build. Old drafts on a phone that were built before this release have no attachment manifest on the server; they submit without photos, as today.
- v1 decisions hold: one company, tens of users, slog only, DigitalOcean Spaces as the S3-compatible store (already decided in the spec; the bucket exists for backups), no new infrastructure beyond a Spaces access key for the API and a MinIO container for local development.
- iOS 17, Swift 6 strict concurrency; SwiftData for the local draft store; the phone's API client is the generated swift-openapi client over `URLSessionTransport` with a 60 s request timeout and a 1 MiB replayable-body limit in the auth middleware.

## How it works

### Photos on the phone: final at capture

A photo is finished the moment it is saved into the draft folder. `PhotoSaver` gets the original bytes (the library picker already loads `Data`; the camera path takes the captured image plus its `mediaMetadata`) and writes `<photoUUID>.jpg` through ImageIO: a thumbnail at most 2,500 px on the long edge without applying the orientation transform, written as JPEG at quality 0.85 with exactly two metadata entries carried over, the orientation tag and the GPS dictionary. Everything else (camera model, lens, software, thumbnails, maker notes) is gone. The work runs off the main actor, which also closes the "JPEG encoding off the main actor" item from milestone 2.

GPS (my call): a library photo keeps the GPS it had. A camera photo has none, because `UIImagePickerController` never adds it; the saver writes the draft's one-shot fix (already captured for the draft) into the GPS dictionary when there is one, so site photos carry where they were taken. If Drew would rather not geotag camera photos, this is one line.

Limits (my call): at most 10 photos per draft, enforced on the capture screen with the existing "Photos · N" count; a photo that fails to decode is reported as today.

`LocalPhoto` gains `bytes` and `uploadState` (`pending`, `stored`, `failed`) plus a `failureMessage`. The draft's manifest is fixed once Build is tapped.

### Contract (1.6.0)

- `CreateDraftRequest.attachments` (optional, max 10): `[{id: uuid, filename, content_type: "image/jpeg", bytes}]`. The phone sends every photo's id, name and size when it builds the draft. The server stores the manifest as `pending` rows.
- `DraftResponse.attachments`: `[{id, filename, bytes, state: pending | stored | uploaded | failed, procore_upload_id?, error?: {code, message}}]`, so the phone and later the web app see what the server has. `failed` means Procore or the store refused this photo for good (see the submit job); the only way out is `DELETE`.
- `PUT /drafts/{draftId}/attachments/{attachmentId}`: body `image/jpeg`, header `X-Company-Id`, at most 20 MiB. Stores the bytes and returns 200 with the attachment. A repeat PUT of a `stored` or `uploaded` attachment returns 200 without rewriting: the attachment id is the idempotency key. 404 for an id not in the draft's manifest; 400 `bad_request` with today's "draft is submitted or being submitted" message while the draft is `submitting` or `submitted` (the same answer the PATCH gives); 400 `bad_request` when the body is not a JPEG or exceeds the limit, with a message the phone can show.
- `DELETE /drafts/{draftId}/attachments/{attachmentId}`: removes the attachment from the manifest (and its object) while the draft is editable; 204. This is how a photo that cannot be sent is dropped. A draft that is `failed` because of a photo is editable, so the user can remove the photo and submit again.
- `POST /drafts/{draftId}/submit`: unchanged shape. New rule: 400 `bad_request` with the message "N photos have not finished uploading" when any manifest row is still `pending` or `failed`. The phone does not submit while a photo is pending or failed, so this is a guard on the server's invariant that the job never runs with missing files, not a path the user sees.

### Backend: store, push, attach

**Table** `attachments`: `id uuid primary key, company_id, draft_id (fk drafts, cascade), filename, content_type, bytes, object_key, state (pending | stored | uploaded | failed), procore_upload_id, uploaded_at, error_code, error_message, created_at, updated_at`, indexed by `draft_id`. Rows are created from the manifest at `POST /drafts`; `PUT` moves a row to `stored` and sets `object_key`; the submit job moves it to `uploaded`, or to `failed` when Procore or the store refuses it for good.

**Object store**: an `ObjectStore` interface (`Put`, `Get`, `Delete`) with an S3 implementation (aws-sdk-go-v2, endpoint and path style from config) and an in-memory implementation for tests. Keys are `attachments/<company_id>/<draft_id>/<attachment_id>.jpg`. Config adds `OBJECT_STORE_ENDPOINT`, `OBJECT_STORE_REGION`, `OBJECT_STORE_BUCKET`, `OBJECT_STORE_ACCESS_KEY`, `OBJECT_STORE_SECRET_KEY`, all required; local development gets a MinIO service in `api/docker-compose.yml` and matching `.env.example` values; dev and prod get a Spaces key scoped to one bucket (`fieldassistant-attachments`, my call, separate from the backup bucket so the lifecycle rule cannot touch dumps). The deploy README gains the key setup and a lifecycle rule that expires objects under `attachments/` after 30 days.

**Submit job**, one new step at the front of `RunSubmit`, inside the existing attempt: for each `stored` attachment of the draft, in manifest order, create a Procore upload with the user's token (`response_filename` = the photo's filename, `response_content_type` = `image/jpeg`), stream the object from the store into a multipart POST to the returned `url` with the returned `fields` and the file last, and on success set `procore_upload_id` and `uploaded_at` and write an `uploaded` audit row with the attachment id, Procore uuid, bytes and duration. An attachment already `uploaded` less than five days ago is reused (my call: inside Procore's one-week window with margin); older ones are pushed again. The push happens before the `attempt` marker, so a retry never counts as an unknown RFI outcome. Push failures split the way RFI failures do today:

- Transient (5xx, 429 after the client's retries, timeouts, network errors): a retryable error for River, same as a lost RFI response; the draft stays `submitting`, and after River's last attempt it fails with `procore_unavailable` as today.
- Definitive (a 4xx from create-upload or from the storage POST, or the object missing from our store because the draft outlived the lifecycle rule): the attachment is marked `failed` with a code (`procore_refused` or `expired`) and a message, no further attachments are pushed, and the draft is failed with `procore_validation` carrying a field error on `attachments` ("Photo IMG_0012.jpg could not be sent to Procore" or "Photo IMG_0012.jpg expired on the server; remove it and submit again"). The RFI is not posted without the photo. The user removes the photo (DELETE) or, for an expired one, nothing else can be done, and submits again.

The RFI body then carries `question.upload_ids` with every uploaded id. After `markSubmitted`, the job deletes the draft's objects, best effort with a log line; the lifecycle rule catches anything missed.

`procore.RFIQuestion.UploadIDs` becomes `[]string`. The Procore client gains `CreateUpload` (through the normal client, 30 s like every Procore call) and a `PostForm` helper for the storage POST that uses its own `http.Client` with a 60 s timeout, sends no Procore headers and follows the same https-only redirect rule as `FetchURL`. Budget: ten photos at worst 30 s + 60 s each is 15 minutes, so River's `JobTimeout` becomes 15 minutes and `RescueStuckJobsAfter` 20 minutes (it must stay above the job timeout, or a live job can be rescued and run twice; the existing warm-job test pins the same rule). Pushes run one at a time; the builder may run two at once if the budget needs it, keeping the same ordering of `upload_ids`.

**Audit**: new kind `uploaded` per attachment; `submitted` and `failed` rows are unchanged. The `attempt` payload gains `upload_ids`.

### Phone: Submit is an intent the phone keeps

Today Review's Submit does three network things in a row (PATCH the payload, POST submit, poll the job) and gives up on the first failure. The new shape:

1. Tapping Submit saves the payload locally, sets the draft to `submitting` with a new `submitPending = true` flag, and returns to the caller with the draft Queued. Nothing else about the local model changes: `submitting` already reads as "Queued" in the design (D9).
2. A single `SubmitRunner` (used by Review inline and by `DraftSync` in the background) runs the pending work for one draft in order: PATCH the payload if it is dirty, `PUT` each `pending` photo (oldest first, one at a time), then `POST /submit` with the draft id as the idempotency key, then poll the job for up to 60 s as today. When the server answers 200 or 202, `submitPending` becomes false; from then on the existing reconcile owns the draft.
3. Outcomes. A transport failure anywhere leaves `submitPending` set and ends the pass (no signal). A definitive refusal (400 unresolved or validation, 422 field errors, permission denied, reauth) clears the flag and records it on the phone: the local draft becomes `failed` ("Fix") with the server's message, and the server's `DraftResponse` is re-read so Review can flag the fields from its `unresolved[]`. Today a refused submit in Review only reverts the status and keeps the flags in the screen's memory; with no screen open that would turn a Queued draft back into a plain "Draft" with nothing to say why, so the refusal has to be persisted (my call: on the phone, reusing the existing `failed` status and `errorMessage`; the builder checks that the server's submit-time revalidation leaves the blocking `unresolved[]` on the draft row, and persists it there if it does not, so the flags survive a relaunch). A photo `PUT` refused with 400 (not a JPEG, too large) or 404 (no manifest, an old draft) marks that photo `failed` locally with the message; the runner does not submit while any photo is `failed`, clears `submitPending`, and the draft shows Fix with the photo tile explaining itself. A 400 "photos have not finished uploading" from the server cannot happen after this and is treated as a bug: logged, flag cleared, draft shown as failed with the server message.
4. `DraftSync` gains the queued step between resending waiting drafts and reconciling: run the runner for every draft with `submitPending`, oldest first. Reconcile skips drafts whose flag is still set, which removes today's silent revert. Triggers stay as they are (reachability change, foreground, pull to refresh, sign-in), which gives the "within seconds" behaviour in the foreground.
5. Background. A suspended app runs no code, so the reachability listener cannot fire until the app is back in the foreground or iOS gives it a slot. The app registers two tasks under `$(PRODUCT_BUNDLE_IDENTIFIER).draftsync` identifiers (so Dev and Prod differ): a `BGAppRefreshTask` for the frequent short slots and a `BGProcessingTask` with `requiresNetworkConnectivity = true` for the longer ones. Both are scheduled whenever the app goes to the background with any draft `waiting` or `submitPending`; the handler runs one `DraftSync` pass and reschedules if work remains; the expiration handler cancels the pass (a cancelled pass records nothing, as today). The acceptance test splits honestly: in the foreground the RFI lands within 30 seconds of signal returning; with the app in the background it lands when iOS runs a task, verified in development by forcing the task from the debugger, and the roadmap row says so. A background URLSession that keeps uploading while suspended is the next step if the pilot shows the background path is too slow (see "Later").
6. Review of a queued draft opens read-only with the Queued tag and a "Stop sending" text link (my call: without it, a mistake noticed offline is trapped until signal comes back). Stop is offered only while no pass is running for that draft. It first asks the server (GET) when there is signal: if the server already has the draft `submitting` or `submitted`, the phone applies that and says "Already sent"; otherwise it clears `submitPending`, returns the draft to `draft` and unlocks editing. With no signal it clears the flag at once; the one case where the POST reached the server but its answer was lost then shows up as a locked edit on the next save, which refreshes to the server's state. No path produces two RFIs. A photo tile whose upload `failed` shows the message and a Remove action; removal is applied locally at once and the DELETE is sent by the runner before the submit.

Copy (my call, Drew may rename): the Drafts and Recent third line reads "Queued · waiting for signal" when `submitPending` is set and the phone is offline, "Queued · sending" when a pass is running, and "Queued" otherwise; the Review footer's pending note reads "Queued. It sends on its own when you have signal." A failed photo tile reads "Couldn't send" with the reason beneath and "Remove".

Local files: photos stay on the phone until the draft is `submitted` (unchanged), which is now also the point at which the server has pushed them to Procore.

### Settings

The "Photo size" row from handoff 1h is added now that it is true: "Photo size" / "Long edge, before upload" / "2,500 px", the value read from the one `PhotoLimits.longEdge` constant the saver uses.

## What success looks like

- Backend: a Go test drives the submit job with two stored attachments against the fake Procore and the in-memory store and asserts: two uploads created with the user's token, two multipart POSTs with `file` last and no Procore headers, `question.upload_ids` in the RFI body in manifest order, `uploaded` audit rows, objects deleted after success. Further tests pin: a transient push failure retries without an unknown-outcome marker; a definitive push failure marks the attachment `failed`, fails the draft with the `attachments` field error and posts no RFI; a missing object does the same with `expired`; a retry reuses recent upload ids; `PUT` is idempotent, refuses a locked draft and rejects a non-JPEG; submit refuses a draft with a pending or failed attachment; `DELETE` removes the row and object and unlocks a photo-failed draft; the River config test pins rescue above the job timeout. Every existing submit test stays green.
- Sandbox: one RFI created from the dev stack with two photos, visible on the RFI's question in Procore with correct orientation and no EXIF beyond orientation and GPS (checked with `exiftool` on the downloaded attachment); a repeat submit returns the stored result.
- Phone (unit): `PhotoSaver` tests on fixture images pin the long edge, the surviving tags and the dropped ones, and the GPS write for camera photos; `DraftSync` tests pin the queued flow order (PATCH, PUT per photo, POST, poll), the offline stop, the definitive refusal persisting `failed` with the message, the failed-photo stop before submit, the reconcile skip while pending, and a relaunch mid-queue continuing; a pure helper decides whether to schedule the background tasks; Stop sending is refused while a pass runs and defers to the server's state when it has one. All 223 existing tests stay green.
- Phone (device, Drew): airplane mode on, dictate an RFI with three photos, Submit, see Queued; airplane mode off with the app in the foreground: the RFI appears in Procore within 30 seconds with three photos, exactly once; repeat with the app backgrounded and confirm it lands when iOS runs the task (forced from Xcode during development). Settings shows the Photo size row.
- Milestone 3's two remaining exit criteria read as met in the roadmap row.

## Decided by the spec

Direct File Upload for attachments; downscale to 2,500 px and strip EXIF except orientation and GPS; the draft UUID as the idempotency key; Spaces as the object store with a 30-day lifecycle rule; `BGProcessingTask` for the queue; audio never uploaded.

## My calls (Drew can overrule any of these)

1. Photos are downscaled and stripped on the phone at capture, not on the backend at upload. It cuts upload bytes on bad signal and keeps image processing out of Go, where EXIF handling is weak.
2. Camera photos get the draft's one-shot GPS fix written in; library photos keep their own.
3. Photos upload to our API only as part of a Submit (inside the runner), not eagerly when the draft is built. Eager upload would leave objects in Spaces for drafts that are never submitted, and a draft submitted 30 days later would then find its photos swept. The cost is a longer first send on a slow link, which the Queued state covers.
4. The manifest travels with `POST /drafts`; the server refuses a submit with a pending or failed attachment.
5. At most 10 photos per draft; 20 MiB per upload on the server.
6. A separate Spaces bucket for attachments; objects deleted on success plus a 30-day sweep.
7. Procore upload ids are reused for five days, then re-pushed.
8. A photo Procore refuses for good fails the draft with a field error naming the photo; the RFI is never posted without it silently. The user removes the photo and submits again.
9. A refused background submit is persisted on the phone as `failed` ("Fix") with the server's message and flags, not reverted to a plain draft.
10. River job timeout 15 minutes, rescue after 20; the storage POST has its own 60 s client.
11. Both background task types are registered; the 30-second criterion is claimed for the foreground only, and the background path is best effort until the pilot says otherwise.
12. Queued drafts can be stopped from Review (only while no pass is running, after asking the server when there is signal); failed photos can be removed.
13. The copy listed above.

## Left to the builder

- The exact ImageIO calls (thumbnail options, destination properties) and how `mediaMetadata` from the camera picker is read.
- Multipart streaming in Go (pipe plus `multipart.Writer`), the S3 client wiring, and MinIO's compose definition.
- Where `SubmitRunner` lives (`Core/Drafts`), how Review and `DraftSync` share it, and how the running pass is surfaced for the "sending" copy.
- Background task registration placement and the scheduling helper's shape.
- Migration numbering, sqlc queries, generated code, and the `.env` and README edits.
- How the failed-photo tile looks within the design system (reuse the flagged-row tokens).

## Later

- Background URLSession uploads that outlive the app's foreground time.
- Observations and punch items use the same manifest, endpoints and push; only the record create changes.
- A per-draft "sending" progress line on Home when the queue is long.
- Webhooks or push so the phone learns a job finished without polling.
