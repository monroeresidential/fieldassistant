# Milestone 3 Attachments: Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The API accepts a draft's photo manifest and the photos themselves, stores them in an S3-compatible bucket, and the submit job pushes them to Procore's Direct File Upload and creates the RFI with their upload ids, failing loudly when a photo cannot be sent.

**Architecture:** A small `internal/blob` package wraps the object store behind an interface with an S3 implementation (DigitalOcean Spaces in dev and prod, MinIO locally) and an in-memory one for tests. The pipeline gains an `attachments` table and domain (`pending → stored → uploaded | failed`), two endpoints (`PUT` and `DELETE` on a draft's attachment) and a guard on submit. `RunSubmit` gains one step before the attempt marker: push every stored attachment to Procore with the user's token and put the upload ids on the RFI question. Nothing after the marker changes.

**Tech Stack:** Go 1.26, chi + oapi-codegen strict server, sqlc + pgx, River, aws-sdk-go-v2 (S3), golang-migrate, Procore REST v1.0 (RFIs) and v1.1 (uploads).

**Spec:** `docs/superpowers/specs/2026-10-29-milestone-3-attachments-offline-design.md` (sections "Contract (1.6.0)" and "Backend: store, push, attach"). The companion iOS plan is `2026-10-30-milestone-3-attachments-ios.md`.

## Global Constraints

- Only the backend talks to Procore. Uploads are created with the submitting user's access token (`d.tokens.AccessToken`), never a shared one; the storage POST carries no Procore headers or token.
- `company_id` on every table; `Procore-Company-Id` on every Procore call (the client's `do` sets it when `companyID` is non-nil).
- Contract version `1.6.0`; `make generate` and `make sync-contract` run in the same commit as the YAML change; generated code is committed.
- Error bodies stay `{code, message}` with the fixed code set; no new HTTP error code. A locked draft answers `400 bad_request` "draft is submitted or being submitted" (the existing `errDraftLocked`). Attachment-level codes `procore_refused` and `expired` live on the attachment row and in `DraftResponse.attachments[].error`, not in HTTP error bodies.
- Limits: at most `10` attachments per draft; `20 MiB` (`20 << 20` bytes) per `PUT`; content type `image/jpeg` only, and the body must begin with the JPEG marker bytes `FF D8 FF`.
- Object keys: `attachments/<company_id>/<draft_id>/<attachment_id>.jpg`. Objects are deleted best effort after `markSubmitted`; a 30-day bucket lifecycle rule on the `attachments/` prefix sweeps the rest.
- Procore upload ids are reused when `uploaded_at` is within `5 * 24h` of now; older attachments are pushed again.
- River: `JobTimeout = 15 * time.Minute`, `RescueStuckJobsAfter = 20 * time.Minute` (must stay above `JobTimeout`), `MaxAttempts` unchanged at 4. The storage POST uses its own `http.Client{Timeout: 60 * time.Second}`; `CreateUpload` goes through the normal 30 s client.
- Pushes run one at a time in manifest order (`created_at`, then `id`); `question.upload_ids` keeps that order.
- Every existing test stays green; DB tests go through `storetest` and its advisory lock.

## Review Focus

1. A PUT whose body is a JPEG with a `Content-Length` lie or no length at all (a chunked upload from a flaky link): the server must count bytes itself and refuse past 20 MiB without storing a partial object. Test in Task 3 (`TestPutAttachment_OverLimit_400_NothingStored`).
2. A retry after the process died between the storage POST and `SetAttachmentUploaded` (a deploy mid-push): the next attempt must push that photo again, not treat it as uploaded. Test in Task 5 (`TestRunSubmit_PushInterruptedBeforeStore_PushesAgain`).
3. A user whose Procore token was revoked between PUT and submit: the push must fail the draft with `reauth_required` exactly as the RFI POST path does today, and post nothing. Test in Task 5 (`TestRunSubmit_PushReauth_MarksFailed`).
4. A second concurrent PUT of the same attachment while the first is still writing: the second must not corrupt or double-write; the last writer wins and both answer 200. Test in Task 3 (`TestPutAttachment_Concurrent_OneRowOneObject`).
5. A draft whose photo was pushed on attempt 1, whose RFI POST was lost, and which recovers on attempt 2 via `findCreated`: the recovered path must not push again and must still delete the objects. Test in Task 5 (`TestRunSubmit_RecoveredRFI_DeletesObjectsWithoutRepush`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `api/internal/blob/blob.go` | `Store` interface, `ErrNotFound`, `Key(companyID, draftID, attachmentID)`. |
| `api/internal/blob/s3.go` | `S3` implementation over aws-sdk-go-v2 with endpoint override and path style; `EnsureBucket`. |
| `api/internal/blob/memory.go` | `Memory` implementation for tests and `blobtest` helpers. |
| `api/internal/config/config.go` | `ObjectStore` config block. |
| `api/internal/store/migrations/000004_attachments.{up,down}.sql` | The `attachments` table. |
| `api/internal/store/queries/attachments.sql` | sqlc queries for attachments. |
| `api/internal/pipeline/attachments.go` | Attachment types, manifest insert, `StoreAttachment`, `DeleteAttachment`, pending guard. |
| `api/internal/pipeline/submit.go` | `pushAttachments` step, upload ids on the body, object cleanup. |
| `api/internal/pipeline/audit.go` | `AuditUploaded` kind. |
| `api/internal/procore/uploads.go` | `CreateUpload`, `PostUpload`, upload types. |
| `api/internal/pipeline/pipelinetest/procore.go` | Fake gains `CreateUpload` and `PostUpload` with recorded multipart parts. |
| `api/internal/httpapi/attachment_handlers.go` | `PutDraftAttachment`, `DeleteDraftAttachment`. |
| `api/openapi.yaml` + generated `gen.go` + iOS copy | Contract 1.6.0. |
| `api/docker-compose.yml`, `api/.env.example`, `deploy/README.md`, `deploy/.env.dev.example` | MinIO locally, Spaces key and lifecycle in deploy. |
| `CLAUDE.md`, `docs/superpowers/plans/2026-10-22-roadmap.md` | Code map and status. |

---

### Task 1: Object store package and config

**Files:**
- Create: `api/internal/blob/blob.go`, `api/internal/blob/s3.go`, `api/internal/blob/memory.go`, `api/internal/blob/blob_test.go`
- Modify: `api/internal/config/config.go`, `api/internal/config/config_test.go`, `api/.env.example`, `api/docker-compose.yml`, `Makefile` (`db-up` also starts `minio`), `api/go.mod`
- Modify: `api/cmd/api/main.go` (build the store, `EnsureBucket` when configured)

**Interfaces:**
- Produces:
  ```go
  package blob
  var ErrNotFound = errors.New("blob: not found")
  type Store interface {
      Put(ctx context.Context, key, contentType string, size int64, r io.Reader) error
      Get(ctx context.Context, key string) (rc io.ReadCloser, size int64, err error) // ErrNotFound when absent
      Delete(ctx context.Context, key string) error // nil when already absent
  }
  func Key(companyID int64, draftID, attachmentID uuid.UUID) string // "attachments/%d/%s/%s.jpg"
  type S3Config struct{ Endpoint, Region, Bucket, AccessKey, SecretKey string; PathStyle bool }
  func NewS3(cfg S3Config) (*S3, error)
  func (s *S3) EnsureBucket(ctx context.Context) error // creates the bucket when missing
  func NewMemory() *Memory // implements Store; Keys() []string for tests
  ```
- Config: `cfg.ObjectStore` with fields `Endpoint, Region, Bucket, AccessKey, SecretKey string; CreateBucket bool` from `OBJECT_STORE_ENDPOINT`, `OBJECT_STORE_REGION`, `OBJECT_STORE_BUCKET`, `OBJECT_STORE_ACCESS_KEY`, `OBJECT_STORE_SECRET_KEY` (all required) and `OBJECT_STORE_CREATE_BUCKET` (optional, default false; local dev sets `true`).

- [ ] **Step 1: Write the failing tests** in `blob_test.go`: `TestMemory_PutGetDelete` (round trip, `Get` after `Delete` is `ErrNotFound`, `Delete` twice is nil), `TestKey` (`Key(7, d, a) == "attachments/7/<d>/<a>.jpg"`), and `TestS3_RoundTrip` gated on `OBJECT_STORE_ENDPOINT` being set (skips otherwise; against local MinIO it puts, gets and deletes one object). In `config_test.go`, add the five `OBJECT_STORE_*` variables to the valid environment the existing tests build, and `TestConfig_MissingObjectStore_Fails`.
- [ ] **Step 2: Run them**: `cd api && go test ./internal/blob/ ./internal/config/` — expected: compile failure, package `blob` undefined.
- [ ] **Step 3: Implement** `blob.go`, `memory.go` (a mutex-guarded `map[string][]byte` plus content types), `s3.go` using `github.com/aws/aws-sdk-go-v2/service/s3` with `BaseEndpoint`, `UsePathStyle: cfg.PathStyle`, static credentials; `Get` maps `*types.NoSuchKey` to `ErrNotFound`; `Delete` ignores `NoSuchKey`. Add the config block and validation. Add the `minio` service to `api/docker-compose.yml` (`minio/minio:latest`, `server /data --console-address ":9001"`, ports `9000`/`9001`, `MINIO_ROOT_USER=fa`, `MINIO_ROOT_PASSWORD=fafafafa`, volume `miniodata`) and make `db-up` start `postgres minio`. In `.env.example`: endpoint `http://localhost:9000`, region `us-east-1`, bucket `fieldassistant-attachments`, the MinIO root key pair, `OBJECT_STORE_CREATE_BUCKET=true`. In `main.go` build `blob.NewS3` from config, call `EnsureBucket` when `CreateBucket` is set, and keep the handle for Task 2's `NewDrafts`.
- [ ] **Step 4: Run** `make db-up` then `cd api && OBJECT_STORE_ENDPOINT=http://localhost:9000 OBJECT_STORE_REGION=us-east-1 OBJECT_STORE_BUCKET=fieldassistant-attachments OBJECT_STORE_ACCESS_KEY=fa OBJECT_STORE_SECRET_KEY=fafafafa go test ./internal/blob/ ./internal/config/` — expected: PASS including the S3 round trip. `go build ./...` passes.
- [ ] **Step 5: Commit** `feat(api): object store package (S3/Spaces, MinIO locally) and config`

---

### Task 2: Attachments table, queries and pipeline domain

**Files:**
- Create: `api/internal/store/migrations/000004_attachments.up.sql`, `000004_attachments.down.sql`, `api/internal/store/queries/attachments.sql`, `api/internal/pipeline/attachments.go`, `api/internal/pipeline/attachments_test.go`
- Modify: `api/internal/pipeline/drafts.go` (`CreateInput.Attachments`, `Draft.Attachments`, `NewDrafts` gains `blobs blob.Store`, `toDraft` loads attachments), `api/internal/pipeline/submit.go` (pending guard in `Submit`), `api/internal/pipeline/*_test.go` and `pipelinetest` (construct with `blob.NewMemory()`), `api/cmd/api/main.go`
- Generated: `api/internal/store/db/*` via `make generate`

**Interfaces:**
- Produces:
  ```go
  const (
      AttachmentPending  = "pending"
      AttachmentStored   = "stored"
      AttachmentUploaded = "uploaded"
      AttachmentFailed   = "failed"
      MaxAttachments     = 10
      MaxAttachmentBytes = 20 << 20
      AttachmentsField   = "attachments" // unresolved field name for photo failures
  )
  type AttachmentInput struct { ID uuid.UUID; Filename, ContentType string; Bytes int64 }
  type Attachment struct {
      ID, DraftID uuid.UUID
      Filename, ContentType string
      Bytes int64
      ObjectKey, State, ProcoreUploadID string
      UploadedAt time.Time
      Error *DraftError
      CreatedAt time.Time
  }
  var (
      ErrAttachmentInvalid    = errors.New("pipeline: attachment is not a jpeg within the size limit")
      ErrTooManyAttachments   = errors.New("pipeline: more than 10 attachments")
      ErrAttachmentsPending   = errors.New("pipeline: attachments not uploaded")
  )
  type PendingError struct{ N int } // wraps ErrAttachmentsPending; Error() is "1 photo has not finished uploading" or "%d photos have not finished uploading"
  func (d *Drafts) StoreAttachment(ctx context.Context, userID, draftID, attachmentID uuid.UUID, contentType string, body io.Reader) (Attachment, error)
  func (d *Drafts) DeleteAttachment(ctx context.Context, userID, draftID, attachmentID uuid.UUID) error
  ```
  `CreateInput.Attachments []AttachmentInput`; `Draft.Attachments []Attachment` (manifest order). `NewDrafts(st, lookups, extractor, pc, tokens, blobs blob.Store, features, model, llmTimeout, now, logger)`.
- Table `attachments`: `id uuid primary key, company_id bigint not null, draft_id uuid not null references drafts(id) on delete cascade, filename text not null, content_type text not null, bytes bigint not null, object_key text, state text not null default 'pending', procore_upload_id text, uploaded_at timestamptz, error_code text, error_message text, created_at timestamptz not null default now(), updated_at timestamptz not null default now()`; index `attachments_draft_idx (draft_id, created_at, id)`.
- Queries: `InsertAttachment :one`, `ListAttachmentsForDraft :many` (order `created_at, id`), `GetAttachmentForDraft :one`, `SetAttachmentStored :exec` (object_key, bytes, state stored, clears error), `SetAttachmentUploaded :exec`, `SetAttachmentFailed :exec` (state failed, error_code, error_message), `DeleteAttachment :exec`, `CountAttachmentsNotUploadable :one` (state in pending, failed).

- [ ] **Step 1: Write the failing tests** in `attachments_test.go` (DB-backed, `storetest`): `TestCreate_StoresManifestPending` (two inputs → two rows `pending`, `Draft.Attachments` in order), `TestCreate_ElevenAttachments_Rejected` (`ErrTooManyAttachments`, no draft row), `TestStoreAttachment_WritesObjectAndMarksStored` (body = JPEG magic + bytes; row `stored`, `ObjectKey == blob.Key(...)`, `Bytes` = counted length, object present in `Memory`), `TestStoreAttachment_Idempotent` (second call returns the row unchanged and `Memory.Keys()` still has one key), `TestStoreAttachment_NotJPEG_Invalid` (body `PNG...` → `ErrAttachmentInvalid`, no object), `TestStoreAttachment_UnknownID_NotFound`, `TestStoreAttachment_Locked` (draft `submitting` → `ErrDraftLocked`), `TestDeleteAttachment_RemovesRowAndObject`, `TestDeleteAttachment_AllowedWhenDraftFailed`, `TestSubmit_PendingAttachment_Refused` (`errors.Is(err, ErrAttachmentsPending)`, `PendingError{N: 1}`, draft still `reviewed`), `TestSubmit_FailedAttachment_Refused`.
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/pipeline/ -run 'Attachment|Manifest|Pending'` — expected: compile failure.
- [ ] **Step 3: Implement** the migration, queries, `make generate`, `attachments.go` (`StoreAttachment` reads the body with `io.ReadAll(io.LimitReader(body, MaxAttachmentBytes+1))`, fails with `ErrAttachmentInvalid` when the result is longer than the limit or does not start with `FF D8 FF`, and only then calls `blobs.Put` with the exact size, so nothing partial is ever stored and the S3 client gets a known length; locks follow `SaveEdits`: `submitting` and `submitted` are `ErrDraftLocked`), the `Create` manifest insert inside the existing create transaction, `toDraft` loading, and the guard at the top of `Submit` before `MarkDraftSubmitting`. Thread `blob.NewMemory()` through every existing test constructor.
- [ ] **Step 4: Run** the whole package: `cd api && TEST_DATABASE_URL=... go test ./internal/pipeline/ ./internal/store/...` — expected: PASS, all existing submit tests included.
- [ ] **Step 5: Commit** `feat(api): attachments table and pipeline domain; submit refuses pending photos`

---

### Task 3: Contract 1.6.0 and the attachment endpoints

**Files:**
- Modify: `api/openapi.yaml` (`info.version: 1.6.0`), generated `api/internal/httpapi/gen.go`, iOS copy via `make sync-contract`
- Create: `api/internal/httpapi/attachment_handlers.go`, `api/internal/httpapi/attachment_handlers_test.go`
- Modify: `api/internal/httpapi/draft_handlers.go` (`CreateDraft` passes the manifest; `draftResponse` emits `attachments`), `api/internal/httpapi/submit_handlers.go` (map `ErrAttachmentsPending`)

**Interfaces:**
- Contract additions:
  - `AttachmentManifestItem {id: uuid, filename: string (minLength 1, maxLength 255), content_type: enum [image/jpeg], bytes: int64 (minimum 1)}`; `CreateDraftRequest.attachments: array, maxItems 10` (optional).
  - `Attachment {id, filename, bytes, state: enum [pending, stored, uploaded, failed], procore_upload_id?: string, error?: Error}`; `DraftResponse.attachments: array of Attachment` (required; empty array when none).
  - `PUT /drafts/{draftId}/attachments/{attachmentId}` operationId `putDraftAttachment`: params `X-Company-Id`; requestBody required, content `image/jpeg: {type: string, format: binary}`; responses 200 `Attachment`, 400, 401, 403, 404, 502, default.
  - `DELETE /drafts/{draftId}/attachments/{attachmentId}` operationId `deleteDraftAttachment`: 204, 400 (locked), 401, 403, 404, 502, default.
- Handlers: `func (s *Server) PutDraftAttachment(ctx, req PutDraftAttachmentRequestObject) (PutDraftAttachmentResponseObject, error)` (the strict server gives `req.Body io.Reader`); `DeleteDraftAttachment` likewise. `ErrAttachmentInvalid` → `400 bad_request` "photo must be a JPEG under 20 MiB"; `ErrTooManyAttachments` → `400 bad_request` "at most 10 photos"; `PendingError` → `400 bad_request` with its `Error()` text; `ErrNotFound` → the existing `errDraftNotFound` for a draft, and `404 not_found` "attachment is not on this draft" for an attachment.

- [ ] **Step 1: Write the failing tests** in `attachment_handlers_test.go` using the existing `testserver_test.go` harness: `TestCreateDraft_WithManifest_ReturnsPendingAttachments`, `TestPutAttachment_StoresAndReturns200`, `TestPutAttachment_Repeat_200_SameObject`, `TestPutAttachment_NotJPEG_400`, `TestPutAttachment_OverLimit_400_NothingStored` (a body of `20<<20 + 1` bytes with a JPEG header and no `Content-Length`; assert 400 and `Memory.Keys()` empty), `TestPutAttachment_Concurrent_OneRowOneObject` (two goroutines PUT the same id; both 200; one row, one key), `TestPutAttachment_Locked_400`, `TestPutAttachment_OtherUser_404`, `TestDeleteAttachment_204`, `TestSubmit_PendingAttachment_400Message` (one pending photo → `400 bad_request` with message exactly "1 photo has not finished uploading"; two → "2 photos have not finished uploading").
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/httpapi/ -run 'Attachment|Manifest'` — expected: compile failure (no such operations).
- [ ] **Step 3: Implement** the YAML, run `make generate && make sync-contract` (`make check-contract` passes), write the handlers, extend `draftResponse`, map the errors. The PUT handler passes `req.Body` straight to `StoreAttachment` (no buffering); the manifest conversion lives in `CreateDraft`.
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./...` — expected: PASS. `make check-contract` passes.
- [ ] **Step 5: Commit** `feat(contract): 1.6.0 adds draft attachments (manifest, PUT, DELETE)`

---

### Task 4: Procore client: create upload and storage POST

**Files:**
- Create: `api/internal/procore/uploads.go`, `api/internal/procore/uploads_test.go`
- Modify: `api/internal/procore/types.go` (`RFIQuestion.UploadIDs []string`), `api/internal/pipeline/submit.go` (`ProcoreRFIs` interface gains the two methods), `api/internal/pipeline/pipelinetest/procore.go`

**Interfaces:**
- Produces:
  ```go
  type UploadCreate struct {
      ResponseFilename    string `json:"response_filename"`
      ResponseContentType string `json:"response_content_type"`
  }
  type Upload struct {
      UUID   string            `json:"uuid"`
      URL    string            `json:"url"`
      Fields map[string]string `json:"fields"`
  }
  // POST /rest/v1.1/projects/{projectID}/uploads with the company header; 30 s client; existing error mapping.
  func (c *Client) CreateUpload(ctx context.Context, accessToken string, companyID, projectID int64, in UploadCreate) (Upload, error)
  // Multipart form POST to up.URL: every Fields entry, then "file" last (filename, content type image/jpeg).
  // No Authorization or Procore headers; https-only redirects (as FetchURL); 60 s client; any non-2xx is *APIError{Status, Body}.
  func (c *Client) PostUpload(ctx context.Context, up Upload, filename string, size int64, r io.Reader) error
  ```
  `ProcoreRFIs` gains `CreateUpload` and `PostUpload`. `pipelinetest.FakeProcore` gains scripted `Uploads []procore.Upload` (handed out in order), `UploadErr`, `PostErr`, and records each `PostUpload` as `Call{Method: "PostUpload", ...}` plus `Posted []PostedUpload{URL, Fields map[string]string, FieldOrder []string, Filename string, Bytes int, HasAuthHeader bool}`.

- [ ] **Step 1: Write the failing tests** in `uploads_test.go` with `httptest`: `TestCreateUpload_PostsToV11WithCompanyHeader` (path `/rest/v1.1/projects/42/uploads`, header `Procore-Company-Id: 7`, body `{"response_filename":"a.jpg","response_content_type":"image/jpeg"}`, decodes `uuid/url/fields`), `TestPostUpload_MultipartFieldsThenFileLast_NoAuth` (parse with `mime/multipart`; assert every field present, `file` is the last part with the filename and `Content-Type: image/jpeg`, no `Authorization` or `Procore-Company-Id` header), `TestPostUpload_Non2xx_APIError` (403 → `*APIError{Status: 403}`), `TestPostUpload_HTTPRedirect_Refused` (a redirect to `http://` is refused).
- [ ] **Step 2: Run** `cd api && go test ./internal/procore/ -run 'Upload'` — expected: compile failure.
- [ ] **Step 3: Implement** `uploads.go` (an `io.Pipe` with `multipart.Writer` so the object streams; `uploadClient` built once in `NewClient` with `Timeout: 60 * time.Second` and the `CheckRedirect` from `FetchURL` factored into a shared helper), change `UploadIDs` to `[]string`, extend the interface and the fake.
- [ ] **Step 4: Run** `cd api && go test ./internal/procore/ ./internal/pipeline/...` — expected: PASS (`go vet` clean).
- [ ] **Step 5: Commit** `feat(procore): Direct File Upload client (create upload, storage POST); upload ids are strings`

---

### Task 5: Submit job pushes attachments

**Files:**
- Modify: `api/internal/pipeline/submit.go` (`pushAttachments`, body `UploadIDs`, object cleanup, `attemptAudit.UploadIDs`), `api/internal/pipeline/audit.go` (`AuditUploaded = "uploaded"`), `api/internal/jobs/jobs.go` (timeouts), `api/internal/jobs/jobs_test.go`
- Create: `api/internal/pipeline/submit_attachments_test.go`

**Interfaces:**
- Produces:
  ```go
  // pushAttachments pushes every stored attachment of cur to Procore in manifest order and
  // returns the upload ids. A *submitFailure means a definitive photo failure already recorded
  // on the attachment (state failed) and to be applied to the draft by the caller; an error is
  // transient and retried by River.
  func (d *Drafts) pushAttachments(ctx context.Context, token string, cur Draft) (ids []string, fail *submitFailure, err error)
  const UploadReuseWindow = 5 * 24 * time.Hour
  const AttachmentErrProcoreRefused, AttachmentErrExpired = "procore_refused", "expired"
  ```
  Messages (exact): `"Photo %s could not be sent to Procore. Remove it and submit again."` and `"Photo %s expired on the server. Remove it and submit again."`, with `%s` the attachment filename. The failure is `submitFailure{code: CodeProcoreValidation, message: <that text>, invalid: []Unresolved{{Field: AttachmentsField, Reason: ReasonProcoreValidation, Message: <that text>, Candidates: []LookupRow{}}}}`.
  Audit `uploaded` payload: `{attachment_id, procore_upload_id, bytes, duration_ms}`. `attemptAudit` gains `UploadIDs []string json:"upload_ids,omitempty"`.
- Placement in `RunSubmit`: after the token is obtained and the `prior.unknown` recovery check, before `attemptStart`/`writeAttempt`. A recovered RFI (`n == 1` before posting) skips the push and still deletes objects. After a successful `markSubmitted` (both the posted and recovered paths), delete every attachment object with a non-empty `ObjectKey`, logging `Warn` on failure; attachments stay `uploaded` with their ids.
- Classification: `*procore.APIError` with status 4xx (except 408 and 429) from `CreateUpload` or `PostUpload` → `procore_refused`; `blob.ErrNotFound` on `Get` → `expired`; `procore.ErrUnauthorized` or `auth.ErrReauthRequired` → the existing `reauth_required` draft failure (not an attachment failure); `ErrPermissionDenied` → `procore_permission_denied`; anything else → transient error (`auditAttemptFailed(ctx, cur, nil, err, 0, false)` then return err).

- [ ] **Step 1: Write the failing tests** in `submit_attachments_test.go` using `draftsEnv` with two stored attachments: `TestRunSubmit_PushesAttachmentsThenPostsWithUploadIDs` (fake hands out uploads `u1`, `u2`; asserts two `CreateUpload` calls with the user token and project id, two `Posted` entries with `file` last and `HasAuthHeader == false`, `postedRFI().Question.UploadIDs == []string{"u1","u2"}`, rows `uploaded` with ids, two `uploaded` audit rows, `Memory.Keys()` empty after success, `attempt` audit carries `upload_ids`), `TestRunSubmit_ReusesRecentUploadIDs` (rows pre-set `uploaded` 1 h ago → no `CreateUpload` call, ids on the body), `TestRunSubmit_RepushesStaleUploadIDs` (6 days ago → pushed again), `TestRunSubmit_PushRefused_FailsDraftWithoutPosting` (fake `PostErr = &procore.APIError{Status: 403}` → attachment `failed` with `procore_refused`, draft `failed` `procore_validation` with the `attachments` field message naming the filename, `CreateRFI` never called, objects kept), `TestRunSubmit_ObjectMissing_FailsExpired`, `TestRunSubmit_PushTransient_RetriesWithoutUnknownOutcome` (fake `PostErr = procore.ErrUnavailable` → error returned, no `attempt` marker, attachment still `stored`), `TestRunSubmit_PushInterruptedBeforeStore_PushesAgain` (first run's fake records the POST then `SetAttachmentUploaded` is made to fail via a cancelled context; second run pushes that attachment again and completes), `TestRunSubmit_PushReauth_MarksFailed`, `TestRunSubmit_RecoveredRFI_DeletesObjectsWithoutRepush` (prior unknown attempt + `findCreated` match → no push calls, objects deleted). In `jobs_test.go`: `TestRiverConfig_RescueExceedsJobTimeout` (`RescueStuckJobsAfter > JobTimeout`, `JobTimeout == 15m`).
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/pipeline/ -run 'RunSubmit_(Push|Reuses|Repush|Object|Recovered)' ./internal/jobs/ -run RiverConfig` — expected: FAIL (no push, `UploadIDs` empty).
- [ ] **Step 3: Implement** `pushAttachments`, wire it into `RunSubmit`, set `body.Question.UploadIDs`, add the audit kind, the cleanup, and the River constants with their comments updated ("ten photos at up to 90 s each").
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./...` — expected: PASS, every existing submit test included.
- [ ] **Step 5: Commit** `feat(api): submit job pushes photos to Procore Direct File Upload before creating the RFI`

---

### Task 6: Deploy, docs and sandbox check

**Files:**
- Modify: `deploy/README.md` (new "Attachments bucket" subsection under "One-time setup": create the `fieldassistant-attachments` Space, a scoped access key, the five `OBJECT_STORE_*` lines in `.env.dev` and `.env.prod` with endpoint `https://nyc3.digitaloceanspaces.com` and region `nyc3` or whatever the Space's region is, `OBJECT_STORE_CREATE_BUCKET` left unset, and the lifecycle rule applied with `s3cmd setlifecycle` from a `deploy/lifecycle-attachments.xml` that expires the `attachments/` prefix after 30 days), `deploy/.env.dev.example`, `deploy/lifecycle-attachments.xml` (new), `CLAUDE.md` (code map row for `api/internal/blob`; commands note that `make db-up` also starts MinIO; conventions line that attachment-level codes `procore_refused` and `expired` live on `DraftResponse.attachments[].error`; status line says the attachments backend is implemented on branch `m3-attachments-offline`), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 3 row: backend attachments implemented, date, verification result)
- Create: `api/internal/procore/uploads_sandbox_test.go` (gated the same way as `records_sandbox_test.go`; creates an upload in the sandbox project, posts a 1 KB JPEG, asserts 2xx; it does not create an RFI)

- [ ] **Step 1: Run the sandbox test** with the same environment `records_sandbox_test.go` uses: `cd api && go test ./internal/procore/ -run Sandbox_Upload -v` — expected: PASS (a real upload uuid in the log).
- [ ] **Step 2: Write the docs** listed above. The roadmap row records the date and the sandbox upload uuid.
- [ ] **Step 3: Verify** `make check-contract`, `cd api && go vet ./... && go test ./...` (with `TEST_DATABASE_URL`) — expected: PASS.
- [ ] **Step 4: Commit** `docs: attachments bucket, lifecycle rule and status; sandbox upload check`

Dev deploy: pushing the branch to `develop` deploys dev; the droplet needs the `OBJECT_STORE_*` lines in `.env.dev` first (README). The end-to-end check with real photos is the iOS plan's device step.
