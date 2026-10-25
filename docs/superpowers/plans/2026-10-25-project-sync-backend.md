# Project Sync (backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The backend keeps a per-user mirror of each project's RFIs, pulled incrementally from Procore, and serves the phone a cursor-based delta plus a forced-sync endpoint and an attachment proxy.

**Architecture:** A new `api/internal/records` package owns the mirror: `Mirror.Sync` pulls from Procore with the user's own token (delta by `updated_at`, periodic full sweep for deletions and bodies), `Mirror.Delta` pages changes by a global `change_seq`. Procore enforces visibility because every pull is per user. HTTP handlers in `httpapi` map the stored Procore body to the typed `RFIRecord` projection. A River periodic job keeps recently used (user, project) pairs warm.

**Tech Stack:** Go 1.23, chi, oapi-codegen (strict server), sqlc, pgx, golang-migrate, River, Postgres 16.

**Spec:** `docs/superpowers/specs/2026-10-25-project-sync-design.md` (approved 2026-10-25). Phone side: `2026-10-25-project-sync-ios.md`, written beside this plan.

## Global Constraints

- Only the backend talks to Procore. Every Procore request carries `Procore-Company-Id` and the requesting user's token (`auth.TokenSource.AccessToken(ctx, userID)`).
- Every new table has `company_id`. Mirror rows belong to one user (`user_id`) and are never read for another user; they cascade-delete with the user row.
- Contract changes bump `info.version` in `api/openapi.yaml` (this plan: `1.4.2` → `1.5.0`) and run `make generate` and `make sync-contract` in the same commit. Generated code is committed.
- Timings from the spec: freshness window 60 s, delta overlap 5 min, full sweep at most every 6 h (always on a forced full sync), keep-warm every 15 min for pairs requested in the last 24 h, delta page limit 500.
- Procore list calls paginate fully (`listAll`), honor 429 (`do` already retries). Procore's `filters[updated_at]` takes an ISO 8601 range written `<start>...<end>` (three dots); the implementer of Task 3 verifies this against the sandbox before relying on it and records the exact format in the client's doc comment.
- A sweep is a full list (every body), not an id-only list: it refreshes bodies whose `updated_at` did not change (answers and attachments) and detects deletions. No `serializer_view` dependency.
- Error bodies keep the fixed code set; a Procore 403 on a pull is reported in the response body's `pull.status = "failed"` with `pull.code = "procore_permission_denied"`, not as an HTTP error, so the phone keeps showing what it has.
- The existing `GET /projects/{projectId}/rfis` stays in the contract, marked deprecated, until the iOS plan's last task removes it (the phone still calls it until then).
- The submit worker and the draft pipeline are not modified.
- No new infrastructure, metrics or env vars beyond what this plan names (none).

## Review Focus

1. A pull that fails mid-pagination (page 3 of 5 returns 502) must not advance `high_water`; the next pull starts from the old mark. Test in Task 4.
2. Two phones of the same user syncing the same project at once must cause one Procore pull, not two; the second waits and gets the mirror. Test in Task 4 (advisory lock).
3. A user without RFI permission (Procore 403 on the list) gets `pull.status = "failed"`, `pull.code = "procore_permission_denied"` and an empty page with HTTP 200, never a 500 or a 403 body. Test in Task 5.
4. A record whose body did not change keeps its `change_seq`, so a sweep over 2,000 unchanged RFIs sends the phone nothing. Test in Task 2 and Task 4.
5. A deleted RFI is reported in `deleted_ids` exactly once per cursor position and never comes back as a record. Test in Task 4.

---

## File structure

| Path | Responsibility |
| --- | --- |
| `api/openapi.yaml` | Contract 1.5.0: records delta, forced sync, attachment proxy, `RFIRecord` and friends. |
| `api/internal/store/migrations/000003_records.{up,down}.sql` | `user_records`, `user_sync_state`, `records_change_seq`. |
| `api/internal/store/queries/records.sql` | sqlc queries for upsert, soft-delete, delta, sync state, warm targets. |
| `api/internal/procore/records.go` | `RawRecord`, `ListRFIsRaw`, `GetRFIRaw`, `FetchURL`. |
| `api/internal/procore/types.go` | `RFIDetail` (every field the projection needs). |
| `api/internal/records/records.go` | `Kind`, `Fetcher`, `Mirror`, `Sync`, `Delta`, `WarmAll`, constants. |
| `api/internal/records/rfi.go` | `RFIFetcher` adapting the Procore client. |
| `api/internal/records/recordstest/fake_fetcher.go` | Scripted fetcher for tests. |
| `api/internal/httpapi/record_handlers.go` | `GetRecords`, `SyncProject`, `GetRFIAttachment`. |
| `api/internal/httpapi/rfi_record.go` | `procore.RFIDetail` → generated `RFIRecord`. |
| `api/internal/jobs/warm.go` | `WarmRecordsArgs`, `WarmRecordsWorker`, periodic job registration. |
| `api/cmd/api/main.go` | Wiring. |
| `CLAUDE.md`, `docs/superpowers/plans/2026-10-22-roadmap.md` | Code map and status. |

---

### Task 1: Contract 1.5.0

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `api/internal/httpapi/gen.go` (generated), `ios/Packages/FieldAssistantAPI/Sources/FieldAssistantAPI/openapi.yaml` (synced)
- Create: `api/internal/httpapi/record_handlers.go` (stubs)
- Test: `make check-contract`, `cd api && go build ./... && go test ./internal/httpapi/`

**Interfaces:**
- Produces: generated request/response types `GetRecordsRequestObject`, `GetRecords200JSONResponse`, `SyncProjectRequestObject`, `SyncProject200JSONResponse`, `GetRFIAttachmentRequestObject`, `GetRFIAttachment200ApplicationoctetStreamResponse`, and schemas `RecordPage`, `RecordEnvelope`, `PullStatus`, `SyncRequest`, `SyncResult`, `RFIRecord`, `RFIQuestion`, `RFIAnswer`, `RFIAttachment`, `Ref`, `ImpactRef`.

- [ ] **Step 1: Add the paths and schemas**

Bump `info.version` to `1.5.0`. Mark `getLookup`'s sibling `listRFIs` with `deprecated: true` and a description "Replaced by getRecords; removed when the phone stops calling it." Add:

```yaml
/projects/{projectId}/records/{kind}:
  get:
    operationId: getRecords
    description: >-
      A page of the project's records of one kind as the signed-in user may
      see them in Procore, changed since the cursor. The backend pulls from
      Procore first when its copy is older than 60 seconds; Cache-Control:
      no-cache forces the pull. The first call sends since=0.
    parameters: projectId (path int64), kind (path, enum [rfi]), X-Company-Id (header int64, required),
                since (query int64, default 0), limit (query int32, default 500, max 500),
                Cache-Control (header string, optional)
    responses: 200 RecordPage; 401, 403, default Error
/projects/{projectId}/sync:
  post:
    operationId: syncProject
    requestBody: SyncRequest {kinds: [string enum rfi] (required, min 1), full: boolean (default false)}
    responses: 200 SyncResult {pulls: [PullStatus]}; 401, 403, default Error
/projects/{projectId}/records/rfi/{rfiId}/attachments/{attachmentId}:
  get:
    operationId: getRFIAttachment
    parameters: projectId, rfiId, attachmentId (path int64), X-Company-Id
    responses: 200 application/octet-stream (binary) with headers Content-Disposition, Content-Type;
               401, 403, 404 (not_found: attachment not on this RFI), 502, default Error
```

Schemas (all fields required unless marked `?`):

```yaml
PullStatus: {kind: string, status: enum [fresh, pulled, failed], pulled_at?: date-time, code?: string, message?: string}
RecordPage: {records: [RecordEnvelope], deleted_ids: [int64], next_since: int64, has_more: boolean, synced_at?: date-time, pull: PullStatus}
RecordEnvelope: {id: int64, kind: string, updated_at: date-time, change_seq: int64, rfi?: RFIRecord}
Ref: {id: int64, name: string}
ImpactRef: {status?: string, value?: string}            # Procore cost_impact / schedule_impact {status, value}
RFIAttachment: {id: int64, name: string, filename?: string, url: string}   # url is the app's proxy path
RFIAnswer: {id: int64, answer_date?: date-time, plain_text_body: string, rich_text_body?: string, created_by?: Ref, attachments: [RFIAttachment]}
RFIQuestion: {id: int64, question_date?: date-time, plain_text_body: string, rich_text_body?: string, created_by?: Ref, attachments: [RFIAttachment], answers: [RFIAnswer]}
RFIRecord:
  id, number: string, full_number?: string, prefix?: string, subject, status, translated_status?: string,
  draft: boolean, private: boolean, due_date?: date, initiated_at?: date-time, time_resolved?: date-time,
  created_at: date-time, updated_at: date-time, created_by?: Ref, assignee?: Ref, assignees: [Ref],
  ball_in_court?: Ref, ball_in_courts: [Ref], ball_in_court_role?: string, rfi_manager?: Ref,
  received_from?: Ref, responsible_contractor?: Ref, distribution_list: [Ref], location?: Ref,
  specification_section?: Ref, cost_code?: Ref, cost_impact?: ImpactRef, schedule_impact?: ImpactRef,
  drawing_number?: string, drawing_ids: [int64], reference?: string, revision?: string,
  current_revision: boolean, has_revisions: boolean, questions: [RFIQuestion],
  custom_fields?: object (additionalProperties true), link?: string
```

Note `RFIAttachment.url` is the backend proxy path (`/v1/projects/{p}/records/rfi/{r}/attachments/{a}`), never Procore's expiring URL. `Ref.name` for a specification section is Procore's `label`, falling back to `number + " " + description`; for a cost code it is `full_code + " " + name`; for a person it is `name`; for a location it is `name`. That mapping lives in Task 5.

- [ ] **Step 2: Generate and sync**

Run: `make generate && make sync-contract && make check-contract`
Expected: `contract in sync`; `git status` shows `gen.go` and the iOS copy changed.

- [ ] **Step 3: Stub the three handlers**

In `api/internal/httpapi/record_handlers.go` implement `GetRecords`, `SyncProject` and `GetRFIAttachment` on `*Server`, each returning `nil, &APIError{Status: 500, Code: "internal", Message: "not implemented"}` (use the package's existing error constructor if one fits). This keeps `var _ StrictServerInterface = (*Server)(nil)` compiling.

- [ ] **Step 4: Build and test**

Run: `cd api && go build ./... && go test ./internal/httpapi/`
Expected: builds; existing tests pass.

- [ ] **Step 5: Commit**

```bash
git add api/openapi.yaml api/internal/httpapi/gen.go api/internal/httpapi/record_handlers.go ios/Packages/FieldAssistantAPI/Sources/FieldAssistantAPI/openapi.yaml
git commit -m "feat(contract): 1.5.0 project records delta, forced sync and RFI attachment proxy"
```

---

### Task 2: Mirror tables and queries

**Files:**
- Create: `api/internal/store/migrations/000003_records.up.sql`, `000003_records.down.sql`
- Create: `api/internal/store/queries/records.sql`
- Modify: `api/internal/store/db/*` (generated by `make generate`)
- Test: `api/internal/store/records_test.go`

**Interfaces:**
- Produces (sqlc, package `db`): `UpsertRecord(ctx, UpsertRecordParams{CompanyID, UserID, ProjectID, Kind, ProcoreID, ProcoreUpdatedAt, Body, BodyHash}) (int64 change_seq, error)`; `SoftDeleteMissingRecords(ctx, SoftDeleteMissingRecordsParams{UserID, ProjectID, Kind, KeepIDs []int64, DeletedAt}) error`; `ListRecordDelta(ctx, ListRecordDeltaParams{UserID, ProjectID, Kind, Since int64, Limit int32}) ([]UserRecord, error)` ordered by `change_seq`, including soft-deleted rows; `GetSyncState(ctx, GetSyncStateParams{UserID, ProjectID, Kind}) (UserSyncState, error)`; `UpsertSyncState(ctx, UpsertSyncStateParams{...all columns})`; `ListWarmTargets(ctx, requestedAfter time.Time) ([]UserSyncState, error)`; `DeleteUserRecords(ctx, DeleteUserRecordsParams{UserID, ProjectID, Kind}) error`.

- [ ] **Step 1: Write the migration**

```sql
create sequence records_change_seq;
create table user_records (
  company_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  project_id bigint not null,
  kind text not null,
  procore_id bigint not null,
  procore_updated_at timestamptz not null,
  body jsonb not null,
  body_hash text not null,
  deleted_at timestamptz,
  change_seq bigint not null,
  synced_at timestamptz not null default now(),
  primary key (user_id, project_id, kind, procore_id)
);
create index user_records_delta on user_records (user_id, project_id, kind, change_seq);
create table user_sync_state (
  company_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  project_id bigint not null,
  kind text not null,
  high_water timestamptz,
  last_pull_at timestamptz,
  last_sweep_at timestamptz,
  last_requested_at timestamptz not null default now(),
  last_error text,
  primary key (user_id, project_id, kind)
);
```

Down drops both tables and the sequence.

- [ ] **Step 2: Write the queries**

`UpsertRecord` assigns `nextval('records_change_seq')` only when the row is new, its `body_hash` differs, or it was soft-deleted (resurrection); otherwise it keeps `change_seq` and updates `synced_at` only. Use `on conflict ... do update set ... change_seq = case when user_records.body_hash <> excluded.body_hash or user_records.deleted_at is not null then nextval('records_change_seq') else user_records.change_seq end, deleted_at = null` and `returning change_seq`. `SoftDeleteMissingRecords` sets `deleted_at` and a new `change_seq` on rows of the (user, project, kind) whose `procore_id <> all(@keep_ids)` and `deleted_at is null`. `ListRecordDelta` selects rows with `change_seq > @since` ordered by `change_seq` limited to `@limit`.

- [ ] **Step 3: Generate**

Run: `make generate`
Expected: `api/internal/store/db/records.sql.go` and `models.go` gain the types above.

- [ ] **Step 4: Write the failing store test**

`api/internal/store/records_test.go` using `storetest`:

```go
func TestUpsertRecord_ChangeSeqOnlyMovesOnChange(t *testing.T)  // upsert A(hash h1) → seq s1; upsert same hash → seq s1; new hash → seq > s1
func TestSoftDeleteMissing_ReportsOnceInDelta(t *testing.T)     // A,B upserted; SoftDeleteMissing(keep [A]) → delta since 0 has B with deleted_at set and change_seq > both; delta since B.change_seq is empty
func TestListRecordDelta_IsPerUser(t *testing.T)                 // user1 rows never appear in user2's delta
```

- [ ] **Step 5: Run to verify fail, implement any query fix, run to verify pass**

Run: `cd api && TEST_DATABASE_URL='postgres://fa:fa@localhost:5432/fieldassistant_test?sslmode=disable' go test ./internal/store/ -run 'Record|SoftDelete'`
Expected: PASS after the queries are right.

- [ ] **Step 6: Commit**

```bash
git add api/internal/store
git commit -m "feat(store): per-user record mirror tables and delta queries"
```

---

### Task 3: Procore client: raw RFI list, show and file fetch

**Files:**
- Create: `api/internal/procore/records.go`
- Modify: `api/internal/procore/types.go` (add `RFIDetail` and nested types)
- Test: `api/internal/procore/records_test.go`

**Interfaces:**
- Produces: `type RawRecord struct { ID int64; UpdatedAt time.Time; Body json.RawMessage }`; `func (c *Client) ListRFIsRaw(ctx, accessToken string, companyID, projectID int64, updatedSince *time.Time) ([]RawRecord, error)`; `func (c *Client) GetRFIRaw(ctx, accessToken string, companyID, projectID, rfiID int64) (json.RawMessage, error)`; `func (c *Client) FetchURL(ctx context.Context, rawURL string) (*http.Response, error)` (GET with the client's http.Client, no Procore headers, caller closes the body); `type RFIDetail struct` with json tags for every `RFIRecord` field in Task 1 (`Questions []RFIQuestionDetail`, each with `Attachments []RFIAttachmentDetail` and `Answers []RFIAnswerDetail`; people and lookups as `*Ref{ID, Name}` plus `Label`, `Number`, `Description`, `FullCode` where Procore has them; `CostImpact`, `ScheduleImpact` as `*Impact{Status, Value string}`; `CustomFields map[string]any`).

- [ ] **Step 1: Write the failing tests**

With an `httptest.Server` playing Procore:

```go
func TestListRFIsRaw_SendsUpdatedAtRangeAndPaginates(t *testing.T)
// updatedSince = 2026-10-20T10:00:00Z; the fake asserts query filters[updated_at] == "2026-10-20T10:00:00Z...<now>" where <now> parses as RFC3339 and is within 5 s of time.Now(), per_page == 100, serves 2 pages; result has 150 RawRecords with UpdatedAt parsed and Body equal to each item's raw JSON.
func TestListRFIsRaw_NilSinceOmitsFilter(t *testing.T)
func TestGetRFIRaw_ReturnsBody(t *testing.T)
func TestRFIDetail_DecodesDocsSample(t *testing.T)
// decode testdata/rfi_show.json (the implementer writes this fixture from Procore's Show RFI sample, with questions, attachments and answers) and assert Subject, Number, Questions[0].Attachments[0].ID, Questions[0].Answers[0].PlainTextBody, SpecificationSection.Label, CostCode.FullCode, CostImpact.Status.
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd api && go test ./internal/procore/ -run 'RFIsRaw|RFIRaw|RFIDetail'`
Expected: FAIL, undefined symbols.

- [ ] **Step 3: Implement**

`ListRFIsRaw` uses `listAll[json.RawMessage]` on `projectPath(projectID, "/rfis")` with `filters[updated_at]` set to `since.UTC().Format(time.RFC3339) + "..." + now.UTC().Format(time.RFC3339)` when `updatedSince != nil`, then decodes `{id, updated_at}` from each item. Before writing the format, verify it against the sandbox with one curl (project 376187, company 4292442, any valid token) and record in the doc comment what the sandbox accepted; if Procore rejects three dots, use what it accepts and update the test's assertion. `GetRFIRaw` is `do` with GET on `/rfis/{id}` into `json.RawMessage`.

- [ ] **Step 4: Run to verify pass**

Run: same command. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add api/internal/procore
git commit -m "feat(procore): raw RFI list by updated_at, show, and file fetch"
```

---

### Task 4: The mirror

**Files:**
- Create: `api/internal/records/records.go`, `api/internal/records/rfi.go`, `api/internal/records/recordstest/fake_fetcher.go`
- Test: `api/internal/records/records_test.go` (needs `TEST_DATABASE_URL`)

**Interfaces:**
- Consumes: Task 2 queries; `pipeline.AccessTokenSource` (reuse the interface by importing `pipeline`, or redeclare an identical `TokenSource` interface locally to avoid the dependency; the implementer picks and says which).
- Produces:

```go
type Kind string
const KindRFI Kind = "rfi"
func ParseKind(s string) (Kind, bool)

type Fetcher interface {
    ListSince(ctx context.Context, token string, companyID, projectID int64, since *time.Time) ([]procore.RawRecord, error)
}
type RFIFetcher struct{ PC *procore.Client }   // ListSince → PC.ListRFIsRaw

const (
    FreshnessWindow = 60 * time.Second
    Overlap         = 5 * time.Minute
    SweepInterval   = 6 * time.Hour
    WarmWindow      = 24 * time.Hour
    MaxPageLimit    = 500
)

type SyncOpts struct{ Force, Full bool }
type PullStatus struct {
    Kind     Kind
    Status   string     // "fresh", "pulled", "failed"
    PulledAt *time.Time
    Code     string     // error code on failed: procore_permission_denied, procore_unavailable, reauth_required, internal
    Message  string
}
type Record struct {
    ID int64; Kind Kind; UpdatedAt time.Time; ChangeSeq int64; Body json.RawMessage; Deleted bool
}
type DeltaPage struct {
    Records   []Record   // Deleted == false
    DeletedIDs []int64
    NextSince int64
    HasMore   bool
    SyncedAt  *time.Time
}

func NewMirror(st *store.Store, fetchers map[Kind]Fetcher, tokens TokenSource, now func() time.Time, logger *slog.Logger) *Mirror
func (m *Mirror) Sync(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind Kind, opts SyncOpts) PullStatus
func (m *Mirror) Delta(ctx context.Context, userID uuid.UUID, projectID int64, kind Kind, since int64, limit int) (DeltaPage, error)
func (m *Mirror) WarmAll(ctx context.Context) error
```

Behaviour of `Sync`: take `pg_advisory_xact_lock(hashtext(user||project||kind))` in a transaction for the whole pull so a concurrent call for the same key waits and then sees `last_pull_at` fresh. Update `last_requested_at` always. If `!opts.Force` and `last_pull_at` is within `FreshnessWindow`, return `fresh`. Decide full: `opts.Full`, or `last_sweep_at` nil, or older than `SweepInterval`, or `high_water` nil. Fetch with `since = high_water - Overlap` (nil when full). Upsert every record (`body_hash` = hex SHA-256 of the body); on a full pull soft-delete rows not in the fetched id set and set `last_sweep_at`. Set `high_water` to the max `updated_at` seen (keep the old one when the fetch returned nothing), `last_pull_at = now`, clear `last_error`. On any fetch error: no row changes, `last_error` set, status `failed` with the code mapped as `httpapi` maps Procore errors (403 → `procore_permission_denied`, 401/refresh rejected → `reauth_required`, 5xx/timeouts → `procore_unavailable`), and the lock released.

`Delta` reads `ListRecordDelta(since, limit+1)`, splits live rows into `Records` and soft-deleted into `DeletedIDs`, sets `HasMore` when limit+1 rows came back, `NextSince` = last returned row's `change_seq` (or `since` when empty), `SyncedAt` = `last_pull_at`.

`WarmAll` lists sync states with `last_requested_at` after `now - WarmWindow` and calls `Sync(opts{})` for each with a 30 s per-pair timeout, logging failures and continuing.

- [ ] **Step 1: Write the fake fetcher**

`recordstest.FakeFetcher` with `Script []func(since *time.Time) ([]procore.RawRecord, error)` consumed in order, `Calls []*time.Time` recorded, and a `Block chan struct{}` that, when non-nil, makes `ListSince` wait before returning (for the concurrency test).

- [ ] **Step 2: Write the failing tests**

```go
func TestSync_FirstPullIsFullAndSetsHighWater(t *testing.T)        // since nil; 3 records; high_water = max updated_at; last_sweep_at set; status pulled
func TestSync_WithinFreshnessWindowIsFresh(t *testing.T)            // second call 10 s later: status fresh, fetcher called once
func TestSync_ForceBypassesWindow(t *testing.T)
func TestSync_DeltaUsesHighWaterMinusOverlap(t *testing.T)          // clock +2h; fetcher.Calls[1] == high_water - 5m
func TestSync_FetchErrorKeepsHighWater(t *testing.T)                // script[1] returns 502 APIError; status failed code procore_unavailable; high_water unchanged; last_error set
func TestSync_FullSweepSoftDeletesMissing(t *testing.T)             // records A,B; full with only A → Delta reports DeletedIDs [B]
func TestSync_UnchangedBodiesDoNotMoveTheCursor(t *testing.T)       // full sweep returning identical bodies → Delta since previous NextSince is empty
func TestSync_PermissionDeniedIsFailedNotError(t *testing.T)        // 403 → status failed, code procore_permission_denied
func TestSync_ConcurrentSameKeyPullsOnce(t *testing.T)              // two goroutines, fetcher blocks; release; fetcher called once; both statuses in {pulled, fresh}
func TestDelta_PagesByChangeSeq(t *testing.T)                        // 7 records, limit 3 → three pages, HasMore true,true,false, NextSince chain
func TestWarmAll_SyncsRecentPairsOnly(t *testing.T)                  // one state requested 1h ago, one 30h ago → fetcher called for the first only
```

- [ ] **Step 3: Run to verify fail**

Run: `cd api && TEST_DATABASE_URL=... go test ./internal/records/`
Expected: FAIL to compile.

- [ ] **Step 4: Implement `records.go`, `rfi.go`**

- [ ] **Step 5: Run to verify pass**

Expected: PASS, including the concurrency test run with `-race`.

- [ ] **Step 6: Commit**

```bash
git add api/internal/records
git commit -m "feat(records): per-user Procore record mirror with delta pulls, sweeps and warm-up"
```

---

### Task 5: Handlers and the RFI projection

**Files:**
- Modify: `api/internal/httpapi/record_handlers.go` (replace stubs), `api/internal/httpapi/router.go` (add `Records *records.Mirror` to `Deps`)
- Create: `api/internal/httpapi/rfi_record.go`
- Modify: `api/internal/httpapi/testserver_test.go` (fake Procore serves `GET /rest/v1.0/projects/{id}/rfis` with `filters[updated_at]` honoured on a per-test slice of RFI JSON, `GET .../rfis/{id}`, and a file URL `/files/{name}` returning bytes with a content type)
- Test: `api/internal/httpapi/record_handlers_test.go`

**Interfaces:**
- Consumes: Task 4 `Mirror`; Task 3 `RFIDetail`, `GetRFIRaw`, `FetchURL`.
- Produces: `func rfiRecord(projectID int64, d procore.RFIDetail) RFIRecord` (the projection; attachment URLs rewritten to the proxy path), `func pullStatus(p records.PullStatus) PullStatus`.

Handler behaviour:
- `GetRecords`: `requireVisible(company)`; parse kind (400 `bad_request` on unknown); `force` from `Cache-Control: no-cache`; `limit` clamped to 1..500; `status := Records.Sync(...)`; `page, err := Records.Delta(...)`; map records whose kind is rfi by decoding `Body` into `RFIDetail` and calling `rfiRecord`; a body that fails to decode is logged and skipped (never a 500 for the whole page).
- `SyncProject`: `requireVisible`; for each kind in the body `Sync(Force: true, Full: body.Full)`; 400 on an unknown kind or empty list.
- `GetRFIAttachment`: `requireVisible`; token; `GetRFIRaw` (403/404 from Procore map through `procoreFailure`); find the attachment id across questions and answers, else 404 `not_found` "attachment is not on this RFI"; `FetchURL`; copy status 200 body through with Procore's `Content-Type` and a `Content-Disposition: attachment; filename="<filename>"`; a non-200 upstream is 502 `procore_unavailable`.

- [ ] **Step 1: Write the failing handler tests**

```go
func TestGetRecords_FirstPageAndCursor(t *testing.T)              // fake has 2 RFIs; since=0 → 2 records with rfi projection (subject, number, assignees names, questions[0].attachments[0].url == "/v1/projects/376187/records/rfi/<id>/attachments/<aid>"), pull.status pulled; call again with next_since → empty, pull.status fresh
func TestGetRecords_NoCacheForcesPull(t *testing.T)               // second call with Cache-Control: no-cache → fake rfis requests == 2
func TestGetRecords_PermissionDeniedIsSoft(t *testing.T)          // fake answers 403 on /rfis → HTTP 200, pull.status failed, pull.code procore_permission_denied, records empty
func TestGetRecords_OtherUserSeesOwnMirrorOnly(t *testing.T)      // sign in as two Procore users; fake returns different RFIs per token; each user's page has only their own
func TestSyncProject_FullSweepDeletes(t *testing.T)               // after first page, remove one RFI from the fake; POST sync {kinds:[rfi], full:true}; GET since=cursor → deleted_ids has it
func TestGetRFIAttachment_StreamsAndRejectsForeign(t *testing.T)  // known attachment → 200 with bytes, Content-Type image/jpeg, Content-Disposition filename; unknown id → 404 not_found
func TestGetRecords_UnknownKindIs400(t *testing.T)
```

- [ ] **Step 2: Run to verify fail**

Run: `cd api && TEST_DATABASE_URL=... go test ./internal/httpapi/ -run 'Records|SyncProject|RFIAttachment'`

- [ ] **Step 3: Implement the handlers, projection and fake routes**

`startTestServer` builds the `Mirror` with `records.RFIFetcher{PC: pc}` and adds it to `Deps`.

- [ ] **Step 4: Run to verify pass**

Expected: PASS; the whole `httpapi` package still green.

- [ ] **Step 5: Commit**

```bash
git add api/internal/httpapi
git commit -m "feat(api): records delta, forced sync and RFI attachment proxy over the per-user mirror"
```

---

### Task 6: Keep-warm job and wiring

**Files:**
- Create: `api/internal/jobs/warm.go`
- Modify: `api/internal/jobs/jobs.go` (`NewClient(pool, s Submitter, w Warmer, logger)`; `riverConfig` gains `PeriodicJobs`), `api/internal/jobs/jobs_test.go` (constructor calls), `api/cmd/api/main.go`
- Test: `api/internal/jobs/warm_test.go`

**Interfaces:**
- Produces: `type Warmer interface { WarmAll(ctx context.Context) error }`; `type WarmRecordsArgs struct{}` with `Kind() == "records_warm"`; `WarmRecordsWorker{Warmer}`; `const WarmInterval = 15 * time.Minute`; periodic job `river.NewPeriodicJob(river.PeriodicInterval(WarmInterval), func() (river.JobArgs, *river.InsertOpts) { return WarmRecordsArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateScheduled, rivertype.JobStateRetryable}}} }, &river.PeriodicJobOpts{RunOnStart: false})`; worker `Timeout()` 10 min.

- [ ] **Step 1: Write the failing tests**

```go
func TestRiverConfig_HasWarmPeriodicJob(t *testing.T)   // cfg.PeriodicJobs has one job; constructing it yields WarmRecordsArgs
func TestWarmRecordsWorker_CallsWarmAll(t *testing.T)    // fake Warmer records one call
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass**

Run: `cd api && TEST_DATABASE_URL=... go test ./internal/jobs/`

- [ ] **Step 3: Wire `main.go`**

`mirror := records.NewMirror(st, map[records.Kind]records.Fetcher{records.KindRFI: records.RFIFetcher{PC: pc}}, tokens, time.Now, logger)`; pass `mirror` to `jobs.NewClient` and `Deps.Records`.

- [ ] **Step 4: Build and run the whole suite**

Run: `cd api && go build ./... && TEST_DATABASE_URL=... go test ./...`
Expected: all packages ok.

- [ ] **Step 5: Commit**

```bash
git add api/internal/jobs api/cmd/api/main.go
git commit -m "feat(jobs): keep recently synced project mirrors warm every 15 minutes"
```

---

### Task 7: Docs and dev verification

**Files:**
- Modify: `CLAUDE.md` (code map row for `api/internal/records`; conventions note that `listRFIs` is deprecated until the iOS switch), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 3 row: backend sync landed, date)

- [ ] **Step 1: Update the docs and commit**

```bash
git commit -am "docs: records mirror in the code map; roadmap notes backend sync"
```

- [ ] **Step 2: Verify on dev (controller, after merge to develop)**

With a dev session token (`scratchpad/devauth*.sh` pattern): `GET /v1/projects/376187/records/rfi?since=0` with `X-Company-Id: 4292442` returns the sandbox RFIs including 182885, 182895, 182897 with questions; create an RFI in the sandbox web UI, `POST /v1/projects/376187/sync {"kinds":["rfi"]}`, GET with the cursor shows it; delete it in the UI, `POST sync {"full":true}`, GET shows it in `deleted_ids`; sign in as a second sandbox user with fewer permissions and confirm their page differs. Record results in the roadmap row.
