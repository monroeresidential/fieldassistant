# Milestone 2 Backend: RFI Extract, Review and Submit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `POST /v1/drafts` turns a transcript into an RFI draft whose every ID comes from cached Procore lookups, `PATCH` saves the user's edits, and `POST /v1/drafts/{id}/submit` posts the RFI to the Procore sandbox exactly once, with an audit trail per draft.

**Architecture:** Three new packages under `api/internal/`: `llm` (Claude tool-use loop and prompt registry, feature-agnostic), `pipeline` (lookup cache, drafts, ID validation, audit, submit orchestration; features register through an interface) and `features/rfi` (schema, `prompt.md`, extraction post-processing, Procore submit, evals). `jobs` wraps River for the submit worker, running in the API binary. The Procore client gains six lookup list calls, project detail and RFI create. Migration `000002` adds `lookup_cache`, `drafts`, `audit_events`; River adds its own tables.

**Tech Stack:** Go 1.26 (`api/go.mod`), `github.com/anthropics/anthropic-sdk-go` v1.84.0, `github.com/riverqueue/river` v0.49.0 with `riverdriver/riverpgxv5`, existing chi + oapi-codegen strict server + sqlc + pgx + golang-migrate; Postgres 16.

**Spec:** `docs/superpowers/specs/2026-10-24-milestone-2-rfi-extract-review-design.md` (sections Decided: backend, Eval set, Decided: verification and Drew's steps), which builds on `procore-field-assistant-spec.md` (Feature 1, Backend spec, LLM design) and `docs/superpowers/specs/2026-10-22-v1-decisions.md`.

## Global Constraints

- Work on branch `milestone-2` from `develop`; merge to `develop` only in Task 12, after Drew's environment steps, because every push to `develop` deploys the dev stack and the new required variables would crash-loop it.
- `company_id` on every new table; `Procore-Company-Id` on every Procore request (the existing `Client.do` adds it when a company ID is passed).
- Model and prompt: `LLM_EXTRACT_MODEL` from config with no default, recorded on every draft as `model_version`; `prompt_version` is the first 12 hex characters of SHA-256 of `prompt.md`.
- Extraction budget: at most 6 lookup tool calls, request timeout `LLM_TIMEOUT_SECONDS` (default 20).
- After extraction, every ID field is validated against `lookup_cache`; unknown IDs are removed and the field is flagged `not_in_lookup`. This runs regardless of model output. The same validation runs again at submit.
- Submit is idempotent on the draft UUID: `Idempotency-Key` must equal the draft ID; a `submitted` draft returns its stored result; the River job's unique key is the draft ID.
- Procore RFI create: `POST /rest/v1.0/projects/{id}/rfis?run_configurable_validations=true`, body `{"rfi": {...}}`; 422 field errors map back to draft fields.
- Error codes stay within `bad_request`, `unauthorized`, `reauth_required`, `forbidden_scope`, `procore_permission_denied`, `not_found`, `procore_unavailable`, `internal`. `not_found` goes live for drafts and jobs that do not exist or belong to another user.
- Contract bump to `1.4.0`; `make generate` and `make sync-contract` in the same commit as the contract change; generated code committed.
- Migration `000001` is frozen (deployed). New DDL is `000002`.
- Lookup kinds, exactly: `users`, `locations`, `spec_sections`, `cost_codes`, `vendors`, `drawings`. Lookup row shape: `{id int64, label string, extra map[string]string}`.
- Impact fields are omitted when not spoken; no `tbd` default. Impact status values accepted: `yes_known`, `yes_unknown`, `no`, `tbd`.
- `question_body` = cleaned question, blank line, `Field notes:`, newline, verbatim transcript.
- `draft` defaults to true while the user has fewer than 5 drafts in status `submitted`; `rfi_manager_id` defaults to the user's Procore user ID when present in the `users` lookup, else unresolved `not_found`; `private` defaults to false.
- Commit after every task with the message given in the task. DB-backed tests need `make db-up` and `TEST_DATABASE_URL='postgres://fa:fa@localhost:5432/fieldassistant_test?sslmode=disable'`.

## Review Focus

1. **The model returns an ID that is not in the lookup** (a hallucinated user ID). The payload must not carry it; the field must be flagged `not_in_lookup`. Test in Task 7 (`TestValidate_UnknownUserID_Demoted`).
2. **The same submit arrives twice** (phone retried after a timeout). Procore must receive one POST and the second call must return the first result. Test in Task 9 (`TestSubmit_Twice_PostsOnce`).
3. **Procore times out after the POST was sent.** The worker must look for an RFI with the same subject created since the attempt before retrying, so a lost response cannot create a duplicate. Test in Task 9 (`TestSubmit_LostResponse_FindsExistingRFI`).
4. **A lookup kind is forbidden on the project** (Drawings tool off). Extraction must proceed without that tool and the lookup endpoint must say `unavailable`. Test in Task 4 (`TestLookups_Forbidden_CachedUnavailable`) and Task 8 (`TestCreateDraft_UnavailableKind_ToolOmitted`).
5. **Procore returns 422 with configurable-field errors.** The draft must return to review with those fields flagged and the server message as the reason, and the draft must not be marked submitted. Test in Task 9 (`TestSubmit_422_FlagsFields`).

---

### Task 1: Config: LLM settings

**Files:**
- Modify: `api/internal/config/config.go`, `api/internal/config/config_test.go`, `api/.env.example`, `deploy/.env.dev.example`, `deploy/.env.prod.example`

**Interfaces:**
- Produces: `Config.LLM LLMConfig` with `APIKey string`, `ExtractModel string`, `Timeout time.Duration`.

- [ ] **Step 1: Write the failing tests** in `config_test.go`, following the existing `TestLoad_*` style with a map-backed `getenv`:
  - `TestLoad_LLMRequired`: with `ANTHROPIC_API_KEY` and `LLM_EXTRACT_MODEL` unset, `Load` errors and the message contains both names.
  - `TestLoad_LLMTimeoutDefault`: both set and `LLM_TIMEOUT_SECONDS` unset → `cfg.LLM.Timeout == 20*time.Second`.
  - `TestLoad_LLMTimeoutInvalid`: `LLM_TIMEOUT_SECONDS=abc` → error mentioning `LLM_TIMEOUT_SECONDS`.
  - Update every existing test's env map to include the two new required variables.
- [ ] **Step 2: Run** `cd api && go test ./internal/config/ -run LLM` → FAIL (field undefined).
- [ ] **Step 3: Implement** `LLMConfig` and the three reads in `Load` (`req` for the two required names; timeout parsed with `strconv.Atoi`, must be > 0).
- [ ] **Step 4: Add the variables** to `api/.env.example` (`ANTHROPIC_API_KEY=`, `LLM_EXTRACT_MODEL=claude-sonnet-5-5`, `LLM_TIMEOUT_SECONDS=20` with one comment line each) and to both deploy examples, with the comment "Generate the key in the Anthropic Console; one key per environment."
- [ ] **Step 5: Run** `go test ./internal/config/` → PASS.
- [ ] **Step 6: Commit** `feat(api): LLM configuration (ANTHROPIC_API_KEY, LLM_EXTRACT_MODEL, LLM_TIMEOUT_SECONDS)`.

### Task 2: Migration 000002 and sqlc queries for lookups, drafts, audit

**Files:**
- Create: `api/internal/store/migrations/000002_drafts.up.sql`, `000002_drafts.down.sql`, `api/internal/store/queries/lookups.sql`, `queries/drafts.sql`, `queries/audit.sql`, `api/internal/store/drafts_test.go`
- Modify: `api/internal/store/storetest/storetest.go` (truncate list), `api/internal/store/db/*` (generated)

**Interfaces:**
- Produces sqlc methods on `db.Queries` (exact names): `UpsertLookup`, `GetLookup`, `InsertDraft`, `GetDraft` (by id), `GetDraftForUser` (id + user_id), `ListDraftsForUserProject` (user_id, project_id, limit), `SetDraftExtracted`, `SetDraftUserPayload`, `SetDraftStatus`, `SetDraftSubmitted`, `SetDraftFailed`, `SetDraftJob` (id, job_id), `CountSubmittedDrafts` (user_id), `InsertAuditEvent`, `ListAuditEvents` (draft_id).

- [ ] **Step 1: Write the DDL** in `000002_drafts.up.sql`:

```sql
create table lookup_cache (
  company_id bigint not null,
  project_id bigint not null,
  kind text not null,
  etag text not null,
  body jsonb not null,
  unavailable boolean not null default false,
  reason text,
  fetched_at timestamptz not null default now(),
  primary key (company_id, project_id, kind)
);
create table drafts (
  id uuid primary key,
  company_id bigint not null,
  project_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  feature text not null,
  status text not null,
  transcript text not null,
  captured_at timestamptz not null,
  llm_output jsonb,
  user_payload jsonb,
  unresolved jsonb not null default '[]'::jsonb,
  confidence jsonb not null default '{}'::jsonb,
  model_version text,
  prompt_version text,
  extract_ms integer,
  procore_record_id bigint,
  procore_record_number text,
  procore_url text,
  error_code text,
  error_message text,
  job_id bigint,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index drafts_user_project_idx on drafts (user_id, project_id, created_at desc);
create table audit_events (
  id bigserial primary key,
  company_id bigint not null,
  draft_id uuid not null references drafts(id) on delete cascade,
  kind text not null,
  payload jsonb,
  procore_request jsonb,
  procore_response jsonb,
  duration_ms integer,
  created_at timestamptz not null default now()
);
create index audit_events_draft_idx on audit_events (draft_id, created_at);
```

The down file drops the three tables in reverse order.

- [ ] **Step 2: Write the queries.** Every `Set*` query also sets `updated_at = now()`. `ListDraftsForUserProject` orders by `created_at desc` with `limit $3`. `CountSubmittedDrafts` counts `status = 'submitted'` for `$1`. `UpsertLookup` is an insert with `on conflict (company_id, project_id, kind) do update` on `etag, body, unavailable, reason, fetched_at`.
- [ ] **Step 3: Write the failing store test** `TestDrafts_RoundTrip` in `drafts_test.go` (uses `storetest.New`): insert a user with `UpsertUserByProcoreID`, insert a draft with status `extracted`, `SetDraftUserPayload`, `SetDraftSubmitted` with id 501, number "214", url; assert `GetDraftForUser` returns status `submitted`, number "214", and that `CountSubmittedDrafts` is 1; assert `GetDraftForUser` with a different user id returns `pgx.ErrNoRows`. Add `TestLookups_Upsert` asserting a second upsert replaces body and etag.
- [ ] **Step 4: Run** `make generate` then the DB-backed test → FAIL until the truncate list includes the new tables (`truncate users, sessions, procore_tokens, auth_states, auth_codes, drafts, audit_events, lookup_cache cascade`).
- [ ] **Step 5: Update `storetest.go`** and run `cd api && TEST_DATABASE_URL=... go test ./internal/store/...` → PASS.
- [ ] **Step 6: Commit** `feat(api): drafts, audit_events and lookup_cache tables with sqlc queries`.

### Task 3: Procore client: lookups, project detail, RFI create

**Files:**
- Modify: `api/internal/procore/client.go`, `api/internal/procore/types.go`, `api/internal/procore/client_test.go`

**Interfaces:**
- Produces on `*Client` (all take `ctx, accessToken string, companyID, projectID int64` first):
  - `ListProjectUsers(...) ([]ProjectUser, error)` → `GET /rest/v1.0/projects/{id}/users`
  - `ListLocations(...) ([]Location, error)` → `GET /rest/v1.0/projects/{id}/locations`
  - `ListSpecSections(...) ([]SpecSection, error)` → `GET /rest/v1.0/projects/{id}/specification_sections`
  - `ListCostCodes(...) ([]CostCode, error)` → `GET /rest/v1.0/cost_codes?project_id={id}`
  - `ListVendors(...) ([]Vendor, error)` → `GET /rest/v1.0/projects/{id}/vendors`
  - `ListDrawings(...) ([]Drawing, error)` → `GET /rest/v1.1/drawings?project_id={id}`
  - `GetProject(...) (ProjectDetail, error)` → `GET /rest/v1.0/projects/{id}`; `ProjectDetail{ID, Name, DisplayName, TimeZone string}` (`time_zone`)
  - `CreateRFI(ctx, accessToken, companyID, projectID, body RFICreate) (RFICreated, error)` → `POST /rest/v1.0/projects/{id}/rfis?run_configurable_validations=true` with JSON `{"rfi": body}`; `RFICreated{ID int64, Number string, Status string, Link string}`; on 422 returns `*ValidationError{Fields map[string][]string}` parsed from `{"errors": {...}}` (string or array values both accepted).
- Types: `ProjectUser{ID, Name, EmailAddress, JobTitle string; Vendor struct{ID int64; Name string}}`, `Location{ID, Name, NodeName string, ParentID *int64}`, `SpecSection{ID, Number, Description, Label string}`, `CostCode{ID, FullCode, Name string}`, `Vendor{ID, Name, TradeName, AbbreviatedName string}`, `Drawing{ID, Number, Title, Discipline string}`, `RFICreate` with JSON tags matching the Procore body (`subject`, `question{body, upload_ids}`, `assignee_ids`, `rfi_manager_id`, `distribution_ids`, `responsible_contractor_id`, `location_id`, `specification_section_id`, `drawing_number`, `cost_code_id`, `due_date`, `schedule_impact{status,value}`, `cost_impact{status,value}`, `private`, `draft`), pointers for optional fields with `omitempty`. `RFI` gains `CreatedAt string` (`created_at`).
- `do` gains a request body parameter: change the signature to `do(ctx, token, companyID, method, path, q, body any, out any)` with `Content-Type: application/json` when body is non-nil; existing callers pass nil.

- [ ] **Step 1: Write the failing tests** in `client_test.go` with the existing `newFakeProcore`:
  - `TestClient_ListProjectUsers_PathAndCompanyHeader`: path `/rest/v1.0/projects/9/users`, header `Procore-Company-Id: 7`, decodes `vendor.name`.
  - `TestClient_ListCostCodes_UsesQueryParam`: path `/rest/v1.0/cost_codes`, query `project_id=9`.
  - `TestClient_ListDrawings_V11`: path `/rest/v1.1/drawings`, query `project_id=9`.
  - `TestClient_GetProject_TimeZone`: `time_zone` decoded.
  - `TestClient_CreateRFI_PostsWrappedBodyWithValidations`: method POST, query `run_configurable_validations=true`, body JSON has top-level `rfi` with `subject` and `question.body`; omitted optional fields absent; 201 decodes `id`, `number` (number given as a JSON number), `link`.
  - `TestClient_CreateRFI_422_IsValidationError`: body `{"errors":{"due_date":["can't be blank"],"rfi_manager_id":"is required"}}` → `errors.As` `*ValidationError` with both keys, each a `[]string`.
  - `TestClient_CreateRFI_403_IsErrPermissionDenied`.
- [ ] **Step 2: Run** `go test ./internal/procore/ -run 'ListProjectUsers|CostCodes|Drawings|GetProject|CreateRFI'` → FAIL.
- [ ] **Step 3: Implement** the types, the body parameter on `do` (encode with `json.Marshal`, set the header), the seven methods, and 422 handling in `decodeResponse` (status 422 → parse errors → `*ValidationError`; falls back to `*APIError` when the body does not parse).
- [ ] **Step 4: Run** `go test ./internal/procore/` → PASS (existing tests included).
- [ ] **Step 5: Commit** `feat(procore): lookup list calls, project detail and RFI create with 422 mapping`.

### Task 4: Lookup cache

**Files:**
- Create: `api/internal/pipeline/lookups.go`, `api/internal/pipeline/lookup_rows.go`, `api/internal/pipeline/lookups_test.go`, `api/internal/pipeline/pipelinetest/fake_procore.go`

**Interfaces:**
- Produces in package `pipeline`:
  - `type LookupKind string` with constants `KindUsers = "users"`, `KindLocations`, `KindSpecSections = "spec_sections"`, `KindCostCodes = "cost_codes"`, `KindVendors`, `KindDrawings`; `var AllKinds = []LookupKind{...}`; `func ParseKind(s string) (LookupKind, bool)`.
  - `type LookupRow struct { ID int64 `json:"id"`; Label string `json:"label"`; Extra map[string]string `json:"extra,omitempty"` }`
  - `type LookupSet struct { Kind LookupKind; Rows []LookupRow; ETag string; Unavailable bool; Reason string; FetchedAt time.Time }` with `func (s LookupSet) Has(id int64) bool` and `func (s LookupSet) Find(id int64) (LookupRow, bool)`.
  - `type ProcoreLookups interface` with the seven read methods from Task 3 (so tests can fake Procore without HTTP); `*procore.Client` satisfies it.
  - `type AccessTokenSource interface { AccessToken(ctx context.Context, userID uuid.UUID) (string, error) }` (satisfied by `*auth.TokenSource`; tests pass a fake).
  - `func NewLookups(st *store.Store, pc ProcoreLookups, tokens AccessTokenSource, ttl time.Duration, now func() time.Time, logger *slog.Logger) *Lookups`
  - `func (l *Lookups) Get(ctx context.Context, userID uuid.UUID, companyID, projectID int64, kind LookupKind, force bool) (LookupSet, error)`: serves the cached row when `now - fetched_at < ttl` and `!force`; otherwise fetches from Procore with the user's token, maps to rows, upserts and returns. On Procore `ErrPermissionDenied` (403) the row is stored with `Unavailable=true`, `Reason` = the Procore message, empty rows. On any other Procore error with a stale row present, returns the stale row and logs `Warn("lookup refresh failed", ...)`; with no row at all, returns the error.
  - `func (l *Lookups) Warm(ctx, userID, companyID, projectID int64, kinds []LookupKind) (map[LookupKind]LookupSet, error)`: `Get` for each kind; errors from one kind do not stop the others; returns the first error after trying all.
  - `func (l *Lookups) Project(ctx, userID, companyID, projectID) (procore.ProjectDetail, error)`: cached in memory for `ttl` keyed by project.
  - `func rowsFor(kind LookupKind, items any) []LookupRow` in `lookup_rows.go`: labels are `users` → `Name` with extra `role`=`JobTitle`, `company`=`Vendor.Name`, `email`; `locations` → `Name` (the full path Procore returns) with extra `node`=`NodeName`; `spec_sections` → `Number + " " + Description` with extra `number`; `cost_codes` → `FullCode + " " + Name` with extra `code`; `vendors` → `Name` with extra `trade`=`TradeName`, `abbr`; `drawings` → `Number + " " + Title` with extra `number`, `discipline`.
  - ETag = `"` + first 16 hex of SHA-256 of the marshalled rows + `"`.
  - `pipelinetest.FakeProcore` implements `ProcoreLookups` plus `CreateRFI` and `ListRFIs` with scripted responses and a recorded call list; used by later tasks too.

- [ ] **Step 1: Write the failing tests** (DB-backed via `storetest.New`; a fake `AccessTokenSource` returning a fixed token):
  - `TestLookups_FetchesThenServesCache`: first `Get` calls Procore once; second within ttl calls zero times; `Advance(ttl+1s)` then `Get` calls again.
  - `TestLookups_Force_BypassesTTL`.
  - `TestLookups_Forbidden_CachedUnavailable`: fake returns `procore.ErrPermissionDenied` → `Unavailable == true`, rows empty, stored row has `unavailable = true`; second `Get` within ttl does not call Procore.
  - `TestLookups_RefreshFailure_ServesStale`: cached row, ttl expired, fake returns `ErrUnavailable` → returns the stale rows, no error.
  - `TestRowsFor_Users_LabelAndExtra`: `ProjectUser{Name:"Ben West", JobTitle:"Architect", Vendor:{Name:"West Studio"}}` → label `Ben West`, extra role/company/email.
  - `TestLookups_ETagStableForSameRows`.
- [ ] **Step 2: Run** `go test ./internal/pipeline/ -run Lookups|RowsFor` → FAIL.
- [ ] **Step 3: Implement** `lookups.go`, `lookup_rows.go`, `pipelinetest/fake_procore.go`.
- [ ] **Step 4: Run** with `TEST_DATABASE_URL` → PASS.
- [ ] **Step 5: Commit** `feat(pipeline): lookup cache with ETag, TTL refresh and unavailable kinds`.

### Task 5: Contract 1.4.0 and the lookups endpoint

**Files:**
- Modify: `api/openapi.yaml` (version `1.4.0`), `api/internal/httpapi/gen.go` output (generated), `api/internal/httpapi/router.go` (Deps gains `Lookups *pipeline.Lookups`, `Drafts *pipeline.Drafts`, `Jobs *jobs.Client`), `api/internal/httpapi/testserver_test.go` (build the new deps with the fake Procore)
- Create: `api/internal/httpapi/lookup_handlers.go`, `api/internal/httpapi/lookup_handlers_test.go`
- Run: `make generate && make sync-contract`

**Interfaces:**
- Produces the contract for every milestone 2 route. Add to `api/openapi.yaml`, all requiring `bearerAuth` and header `X-Company-Id` (int64, required) like `listRFIs`:
  - `GET /projects/{projectId}/lookups/{kind}` `operationId: getLookup`; path `kind` enum of the six kinds; optional header `If-None-Match`; optional header `Cache-Control` (value `no-cache` forces refresh); 200 `LookupResponse` with response header `ETag`; 304 no body; 401/403/502/default `Error`.
  - `POST /drafts` `createDraft` body `CreateDraftRequest` → 200 `DraftResponse` (200, not 201, because a repeat with the same id returns the existing draft); 400/401/403/502/default.
  - `GET /drafts` `listDrafts` query `project_id` (int64, required), `limit` (int, default 50, max 50) → 200 array of `DraftResponse`.
  - `GET /drafts/{draftId}` `getDraft` → 200 `DraftResponse`; 404.
  - `PATCH /drafts/{draftId}` `saveDraft` body `SaveDraftRequest` → 200 `DraftResponse`; 400/404.
  - `POST /drafts/{draftId}/submit` `submitDraft` header `Idempotency-Key` (string, required) → 200 `SubmitResponse` when already submitted, 202 `SubmitResponse` when enqueued; 400 (unresolved fields or key mismatch), 404.
  - `GET /jobs/{jobId}` `getJob` → 200 `JobStatus`; 404.
- Schemas:
  - `LookupRow{id int64, label string, extra object additionalProperties string}` (required id, label)
  - `LookupResponse{kind, rows [LookupRow], unavailable bool, reason string?, fetched_at date-time}`
  - `CreateDraftRequest{id uuid, feature enum[rfi], project_id int64, transcript string (minLength 1), captured_at date-time, location {lat number, lng number}?}`
  - `Impact{status enum[yes_known, yes_unknown, no, tbd], value string?}`
  - `RFIDraftPayload{subject string?, question_body string?, assignee_ids [int64], rfi_manager_id int64?, distribution_ids [int64], responsible_contractor_id int64?, location_id int64?, specification_section_id int64?, drawing_number string?, cost_code_id int64?, due_date date?, schedule_impact Impact?, cost_impact Impact?, private bool, draft bool}` (required: `assignee_ids`, `distribution_ids`, `private`, `draft`)
  - `UnresolvedField{field string, reason enum[ambiguous, not_found, not_in_lookup, date_unparseable, procore_validation], message string?, candidates [LookupRow]}`
  - `DraftResult{procore_id int64, number string?, url string?}`
  - `DraftResponse{id uuid, feature, project_id, status enum[extracted, reviewed, submitting, submitted, failed], payload RFIDraftPayload, unresolved [UnresolvedField], confidence object additionalProperties number, transcript, model_version string?, result DraftResult?, error Error?, created_at, updated_at}`
  - `SaveDraftRequest{payload RFIDraftPayload}`
  - `SubmitResponse{job_id string, draft_id uuid, status enum[submitting, submitted]}`
  - `JobStatus{id string, draft_id uuid, status enum[queued, running, succeeded, failed], result DraftResult?, error Error?}`
- `lookup_handlers.go`: `GetLookup` checks `requireVisible`, parses the kind, calls `Lookups.Get` with `force = (Cache-Control == "no-cache")`, returns 304 when `If-None-Match` equals the set's ETag, else 200 with `ETag` header.

- [ ] **Step 1: Edit `openapi.yaml`** as above; bump version; run `make generate` and `make sync-contract`; `go build ./...` fails on the unimplemented strict methods, which is expected until the handler files exist. Add stub methods returning `errNotImplemented` for the draft and job operations in a new `draft_handlers.go` so the build passes (Task 8 and 9 replace them).
- [ ] **Step 2: Write the failing tests** in `lookup_handlers_test.go` using `newTestServer`, `signIn`, `call`:
  - `TestGetLookup_ReturnsRowsAndETag`: fake Procore `ProjectUsers[9] = [{ID:11, Name:"Ben West", JobTitle:"Architect"}]` → 200, body `rows[0].label == "Ben West"`, `ETag` header non-empty.
  - `TestGetLookup_IfNoneMatch_304`: second call with the ETag → 304, empty body.
  - `TestGetLookup_NoCache_Refetches`: `Cache-Control: no-cache` → fake request count increments.
  - `TestGetLookup_BadKind_400`: `/lookups/people` → 400 `bad_request`.
  - `TestGetLookup_CompanyNotVisible_403`.
  - `TestGetLookup_Forbidden_Unavailable`: fake marks the path forbidden → 200 with `unavailable: true`.
- [ ] **Step 3: Extend `fakeProcore`** in `testserver_test.go` with `ProjectUsers map[int64][]procore.ProjectUser` (and the other five kinds), `ProjectDetails map[int64]procore.ProjectDetail`, handlers for the seven GET paths and `POST .../rfis` (records the body; answers from `RFICreateResponse func(body) (status int, json string)` so Task 9 can script 201/422/timeouts). `newTestServer` constructs `pipeline.NewLookups(st, pc, tokens, 10*time.Minute, clock.now, logger)`.
- [ ] **Step 4: Run** `go test ./internal/httpapi/ -run GetLookup` → FAIL, then implement `GetLookup`, run → PASS; run `make check-contract` → in sync.
- [ ] **Step 5: Commit** `feat(api): contract 1.4.0 (lookups, drafts, submit, jobs) and the lookups endpoint`.

### Task 6: LLM package: Claude tool loop and prompt registry

**Files:**
- Create: `api/internal/llm/llm.go`, `api/internal/llm/client.go`, `api/internal/llm/prompt.go`, `api/internal/llm/client_test.go`, `api/internal/llm/prompt_test.go`, `api/internal/llm/llmtest/scripted.go`
- Modify: `api/go.mod` (add `github.com/anthropics/anthropic-sdk-go v1.84.0`)

**Interfaces:**
- Produces in package `llm`:
  - `type Tool struct { Name, Description string; InputSchema map[string]any }`
  - `type ToolCall struct { ID, Name string; Input json.RawMessage }`
  - `type ToolHandler func(ctx context.Context, call ToolCall) (json.RawMessage, error)`
  - `type Request struct { System string; User string; Tools []Tool; FinalTool string; MaxToolCalls int }`
  - `type Turn struct { Role string; Content json.RawMessage }` (the raw message content as sent or received, for the audit row)
  - `type Result struct { Final json.RawMessage; Turns []Turn; ToolCalls int; Model string; Duration time.Duration; InputTokens, OutputTokens int }`
  - `type Extractor interface { Extract(ctx context.Context, req Request, handle ToolHandler) (Result, error) }`
  - `var ErrNoFinal = errors.New("llm: model stopped without calling the final tool")`, `var ErrToolBudget = errors.New("llm: tool call budget exhausted")`
  - `func NewClient(apiKey, model, baseURL string, timeout time.Duration) *Client` (`baseURL` empty means the SDK default; tests point it at an httptest server) and `func (c *Client) Extract(...)`: loop: send system, user text and tools with `tool_choice: any`; for each `tool_use` block, if its name is `FinalTool` stop and return its input as `Final`; otherwise count it, return `ErrToolBudget` as a stop condition when the count would exceed `MaxToolCalls` (the loop then sends one more turn with `tool_choice` forced to `FinalTool` and no lookup tools, so the model must finish), call `handle`, append a `tool_result`; a handler error becomes a `tool_result` with `is_error: true` and text of the error. `max_tokens` 4096. Stop reason `end_turn` without the final tool → `ErrNoFinal`.
  - `type Prompt struct { Text, Version string }`; `func LoadPrompt(fsys fs.FS, name string) (Prompt, error)` with `Version` = first 12 hex of SHA-256 of the bytes.
  - `llmtest.Scripted` implements `Extractor` with a `Steps []Step` where each step is either `{Tool string, Input string}` (the fake "model" calls that tool and records the handler's result) or `{Final string}`; it records every handler result in `Results []json.RawMessage` so tests can assert what the tools returned. `Model` field is reported in `Result.Model`.

- [ ] **Step 1: Write the failing tests**:
  - `prompt_test.go` `TestLoadPrompt_VersionIsHashPrefix`: `fstest.MapFS` with `prompt.md` = "hello" → `Version == "2cf24dba5fb0"`.
  - `client_test.go` with an `httptest.Server` that speaks the Messages API (`POST /v1/messages`, returns scripted JSON bodies in order and records request bodies):
    - `TestClient_Extract_ToolLoopThenFinal`: responses: (1) `tool_use` `list_users` input `{"query":"ben"}`; (2) `tool_use` `submit_draft` input `{"subject":"x"}`. Assert the handler was called once with name `list_users`, the second request body contains a `tool_result` with the handler's JSON, `Result.Final` equals `{"subject":"x"}`, `ToolCalls == 1`, `Turns` has 4 entries.
    - `TestClient_Extract_ToolBudget_ForcesFinal`: `MaxToolCalls: 1`, responses: `list_users`, `list_users`, then `submit_draft`. Assert the third request's `tool_choice` names `submit_draft` and the only tool offered is `submit_draft`; result ok with `ToolCalls == 1` and handler called once.
    - `TestClient_Extract_EndTurnWithoutFinal_ErrNoFinal`.
    - `TestClient_Extract_HandlerError_IsToolResultError`: handler returns an error → next request has `is_error: true`.
    - `TestClient_Extract_Timeout`: server sleeps past `timeout` → error wraps `context.DeadlineExceeded`.
- [ ] **Step 2: Run** `go test ./internal/llm/` → FAIL.
- [ ] **Step 3: Add the SDK** (`go get github.com/anthropics/anthropic-sdk-go@v1.84.0`) and implement with `anthropic.NewClient(option.WithAPIKey(key), option.WithBaseURL(baseURL))` and `client.Messages.New`. Keep all SDK types inside `client.go`; nothing else imports the SDK.
- [ ] **Step 4: Run** `go test ./internal/llm/` → PASS; `go vet ./...` clean.
- [ ] **Step 5: Commit** `feat(llm): Claude tool-use extraction loop, prompt registry and scripted test extractor`.

### Task 7: Feature interface, RFI schema and prompt, ID validation

**Files:**
- Create: `api/internal/pipeline/feature.go`, `api/internal/pipeline/validate.go`, `api/internal/pipeline/validate_test.go`, `api/internal/features/rfi/rfi.go`, `api/internal/features/rfi/schema.go`, `api/internal/features/rfi/prompt.md`, `api/internal/features/rfi/rfi_test.go`

**Interfaces:**
- Produces in `pipeline`:
  - `type IDField struct { Field string; Kind LookupKind; Multi bool }`
  - `type ExtractContext struct { ProjectName, TimeZone string; Today time.Time; UserName string; UserProcoreID int64; SubmittedCount int }`
  - `type Feature interface { Name() string; LookupKinds() []LookupKind; IDFields() []IDField; DateFields() []string; Prompt() llm.Prompt; OutputSchema() map[string]any; UserMessage(ctx ExtractContext, transcript string) string; ApplyDefaults(ctx ExtractContext, payload map[string]any, lookups map[LookupKind]LookupSet) (map[string]any, []Unresolved) }`
  - `type Unresolved struct { Field string `json:"field"`; Reason string `json:"reason"`; Message string `json:"message,omitempty"`; Candidates []LookupRow `json:"candidates"` }` with reason constants `ReasonAmbiguous`, `ReasonNotFound`, `ReasonNotInLookup`, `ReasonDateUnparseable`, `ReasonProcoreValidation`.
  - `func Validate(f Feature, payload map[string]any, unresolved []Unresolved, lookups map[LookupKind]LookupSet, today time.Time, loc *time.Location) (map[string]any, []Unresolved)`: for each `IDField`, remove IDs not in `lookups[kind]` (single → delete key, multi → filter the array; an emptied multi array stays `[]`) and append `Unresolved{Field, ReasonNotInLookup}` once per field if anything was removed; drop unresolved candidates whose ID is not in the lookup; for each `DateFields` value that is not `YYYY-MM-DD` or is before `today` in `loc`, delete it and flag `ReasonDateUnparseable`; dedupe unresolved by field (first wins). Unresolved fields are never also present in the payload for single-valued fields.
- Produces in `features/rfi`:
  - `func New() *Feature` implementing `pipeline.Feature` with `Name() == "rfi"`, all six kinds, `IDFields` = `assignee_ids/users/multi`, `rfi_manager_id/users`, `distribution_ids/users/multi`, `responsible_contractor_id/vendors`, `location_id/locations`, `specification_section_id/spec_sections`, `cost_code_id/cost_codes`; `DateFields` = `due_date`.
  - `schema.go`: `var OutputSchema map[string]any` = the JSON schema of the `submit_draft` tool input: `payload` (the `RFIDraftPayload` fields, with impact `status` enum and `value` string), `unresolved` (array of `{field, reason enum[ambiguous, not_found], message, candidates[{id, label}]}`), `confidence` (object of number 0 to 1), `required: [payload, unresolved, confidence]`. The model's candidates carry `id` and `label` only; `Validate` replaces them with full rows from the cache.
  - `prompt.md` contents, as sections: role (fill a Procore RFI from a dictated transcript for a construction superintendent); house style (question first, context second, what is blocked third; speaker's words, no hedging, no rewording of meaning; subject at most 100 characters stating the question); the `question_body` rule (`Field notes:` block with the verbatim transcript after a blank line); ID rules (every `*_id` must be an `id` returned by a tool in this conversation; when a name matches more than one row, leave the field empty and add it to `unresolved` with reason `ambiguous` and the candidates; when it matches none, reason `not_found`; search with short queries such as a last name or a trade; stop after 6 lookups); date rules (resolve relative dates against today's date and the project timezone given in the user message; emit `YYYY-MM-DD`; an ambiguous date goes to `unresolved`); impacts (set only when the speaker addressed it; "holding up" means `schedule_impact.status = yes_unknown`; a spoken dollar amount sets `cost_impact {yes_known, value}`; otherwise omit both); never set `cost_code_id`, `due_date`, `private` unless spoken; `drawing_number` accepts a spoken sheet number like "M-301" verbatim; leave `rfi_manager_id` and `draft` empty (the server sets them); finish by calling `submit_draft` exactly once.
  - `UserMessage` renders: project name, timezone, today's date, the user's name and Procore ID, then `Transcript:` and the text.
  - `ApplyDefaults`: `rfi_manager_id` unset → user's Procore ID if `lookups[users].Has(id)` else unresolved `not_found` with message "You are not in this project's directory"; `draft` unset → `SubmittedCount < 5`; `private` unset → false; `assignee_ids`/`distribution_ids` missing → `[]`; compose `question_body` with the `Field notes:` block if the model did not already include it.

- [ ] **Step 1: Write the failing tests**:
  - `validate_test.go`: `TestValidate_UnknownUserID_Demoted` (payload `assignee_ids: [11, 99]`, lookup users has 11 → `[11]` and unresolved `assignee_ids/not_in_lookup`); `TestValidate_SingleUnknownID_RemovedAndFlagged` (`location_id: 5` not in lookup → key gone); `TestValidate_DropsCandidatesNotInLookup`; `TestValidate_DatePastOrMalformed_Flagged` (`due_date: "yesterday"` and a date before today); `TestValidate_KeepsKnownIDs_NoFlag`.
  - `rfi_test.go`: `TestRFI_OutputSchema_IsValidJSONSchema` (marshals; `required` has the three keys); `TestRFI_ApplyDefaults_ManagerAndDraft` (user 42 in lookup, `SubmittedCount 2` → `rfi_manager_id 42`, `draft true`; count 5 → `draft false`); `TestRFI_ApplyDefaults_ManagerNotInDirectory_Unresolved`; `TestRFI_QuestionBody_AppendsFieldNotes` (body "Why?" + transcript "um why" → ends with `Field notes:\num why`); `TestRFI_Prompt_LoadsWithVersion` (`Prompt().Version` has length 12).
- [ ] **Step 2: Run** `go test ./internal/pipeline/ ./internal/features/rfi/` → FAIL.
- [ ] **Step 3: Implement** `feature.go`, `validate.go`, `rfi.go`, `schema.go`, write `prompt.md` (embedded with `//go:embed prompt.md`).
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(rfi): feature interface, RFI output schema and prompt, post-extraction ID validation`.

### Task 8: Draft pipeline and handlers (create, get, save, list)

**Files:**
- Create: `api/internal/pipeline/drafts.go`, `api/internal/pipeline/audit.go`, `api/internal/pipeline/rails.go`, `api/internal/pipeline/drafts_test.go`, `api/internal/httpapi/draft_handlers.go` (replace the Task 5 stubs), `api/internal/httpapi/draft_handlers_test.go`
- Modify: `api/internal/httpapi/router.go` (Deps), `api/internal/httpapi/testserver_test.go` (build `pipeline.NewDrafts` with an `llmtest.Scripted` the test can program)

**Interfaces:**
- Produces in `pipeline`:
  - `type Draft struct { ID uuid.UUID; CompanyID, ProjectID int64; UserID uuid.UUID; Feature, Status, Transcript string; CapturedAt time.Time; LLMOutput, UserPayload json.RawMessage; Unresolved []Unresolved; Confidence map[string]float64; ModelVersion, PromptVersion string; ExtractMS int; Result *Result; Error *DraftError; CreatedAt, UpdatedAt time.Time }`, `type Result struct { ProcoreID int64; Number, URL string }`, `type DraftError struct { Code, Message string }`; status constants `StatusExtracted`, `StatusReviewed`, `StatusSubmitting`, `StatusSubmitted`, `StatusFailed`.
  - `func (d Draft) Payload() json.RawMessage` = `UserPayload` if set else `LLMOutput`.
  - `type CreateInput struct { ID uuid.UUID; UserID uuid.UUID; CompanyID, ProjectID int64; Feature, Transcript string; CapturedAt time.Time }`
  - `func NewDrafts(st *store.Store, lookups *Lookups, extractor llm.Extractor, features map[string]Feature, model string, now func() time.Time, logger *slog.Logger) *Drafts`
  - `func (d *Drafts) Create(ctx, in CreateInput) (Draft, error)`: single-flight on `in.ID` (`golang.org/x/sync/singleflight`, already a dependency); if a draft with that ID exists, return it when it belongs to the user, else `ErrNotFound`; otherwise: warm lookups for the feature's kinds, build the tool set from kinds that are not unavailable, build `ExtractContext` (project detail from `Lookups.Project`, timezone via `rails.go` map, `SubmittedCount` from `CountSubmittedDrafts`), call the extractor with `MaxToolCalls 6` and `FinalTool "submit_draft"`, parse `Final` into `{payload, unresolved, confidence}`, `ApplyDefaults`, `Validate`, insert the draft with `status extracted`, `llm_output` = validated payload, `model_version` = `Result.Model` (falls back to the configured model), `prompt_version`, `extract_ms`; write audit event `extracted` with `payload` = `{turns, tool_calls, input_tokens, output_tokens, raw_final}`.
    Tool handler: `list_<kind>` with input `{query?}` returns up to 25 rows (case-insensitive substring match over label and every extra value; empty query returns the first 25 rows and `total`); result JSON `{rows: [...], total: n}`.
    An extractor error (timeout, `ErrNoFinal`) is returned as `ErrExtractFailed` wrapping the cause; nothing is inserted.
  - `func (d *Drafts) Get(ctx, userID, id) (Draft, error)` (`ErrNotFound` when missing or another user's).
  - `func (d *Drafts) SaveEdits(ctx, userID, id uuid.UUID, payload json.RawMessage) (Draft, error)`: validate IDs again (same `Validate`, today from `now`), store as `user_payload`, status `reviewed` unless `submitted`/`submitting` (then `ErrDraftLocked`), replace `unresolved` with the validation result, audit `edited` with `payload` = `{before, after}` where `before` is the previous `Payload()`.
  - `func (d *Drafts) List(ctx, userID, projectID int64, limit int) ([]Draft, error)`.
  - `var ErrNotFound`, `ErrExtractFailed`, `ErrDraftLocked`.
  - `func RailsZone(name string) *time.Location` in `rails.go`: map of US Rails zone names to IANA (`Eastern Time (US & Canada)` → `America/New_York`, Central → `America/Chicago`, Mountain → `America/Denver`, Arizona → `America/Phoenix`, Pacific → `America/Los_Angeles`, Alaska → `America/Anchorage`, Hawaii → `Pacific/Honolulu`); a name that `time.LoadLocation` accepts directly is used as is; anything else → `time.UTC` with a `Warn` log.
- Handlers in `draft_handlers.go`: `CreateDraft` (requireVisible on `X-Company-Id`; `ErrExtractFailed` → 502 `procore_unavailable` with message "extraction failed"; note the code set has no LLM-specific code, and the design accepts this mapping), `GetDraft`, `SaveDraft`, `ListDrafts`; `ErrNotFound` → 404 `not_found`, `ErrDraftLocked` → 400 `bad_request`. `draftResponse(d Draft) DraftResponse` converter shared with Task 9.

- [ ] **Step 1: Write the failing pipeline tests** in `drafts_test.go` (DB-backed, `llmtest.Scripted`, `pipelinetest.FakeProcore` with users `[{11 Ben West Architect}, {42 Pat Field Superintendent}]` and the dictating user's Procore ID 42):
  - `TestCreate_ExtractsValidatesAndAudits`: scripted: tool `list_users` `{"query":"west"}` then final `{"payload":{"subject":"Twisted column","question_body":"...","assignee_ids":[11],"location_id":77},"unresolved":[],"confidence":{"subject":0.9}}`; location 77 not in lookup. Assert status `extracted`, `assignee_ids [11]`, no `location_id`, unresolved has `location_id/not_in_lookup`, `rfi_manager_id 42`, `draft true`, `model_version` set, `prompt_version` 12 chars, audit has one `extracted` event whose payload has `turns`.
  - `TestCreate_SameID_ReturnsExistingWithoutExtracting`: second `Create` with same ID → extractor called once.
  - `TestCreate_SameID_OtherUser_NotFound`.
  - `TestCreate_UnavailableKind_ToolOmitted`: `drawings` forbidden in fake → the `Request.Tools` recorded by `Scripted` has no `list_drawings`.
  - `TestCreate_ToolHandler_FiltersRows`: scripted `list_users` `{"query":"ben"}` → recorded handler result rows has exactly Ben West.
  - `TestCreate_ExtractorError_NoDraft`: scripted error → `ErrExtractFailed`, `GetDraft` → no rows.
  - `TestSaveEdits_ValidatesAndAudits`: payload with `assignee_ids [99]` → stored `[]`, unresolved flagged, status `reviewed`, audit `edited` has before/after.
  - `TestRailsZone_Central`.
- [ ] **Step 2: Run** → FAIL; implement `drafts.go`, `audit.go`, `rails.go`.
- [ ] **Step 3: Run** pipeline tests → PASS.
- [ ] **Step 4: Write the failing handler tests** in `draft_handlers_test.go`: `TestCreateDraft_ReturnsDraftResponse` (asserts JSON `status == "extracted"`, `payload.assignee_ids == [11]`, `unresolved[0].field`), `TestCreateDraft_CompanyNotVisible_403`, `TestGetDraft_OtherUser_404`, `TestSaveDraft_StoresPayload`, `TestListDrafts_NewestFirst`, `TestCreateDraft_ExtractFails_502`.
- [ ] **Step 5: Implement the handlers** and the test server wiring; run `go test ./internal/httpapi/` → PASS.
- [ ] **Step 6: Commit** `feat(api): draft extraction pipeline with lookup tools, validation, audit; draft endpoints`.

### Task 9: Submit: River worker, submit and job endpoints

**Files:**
- Create: `api/internal/jobs/jobs.go`, `api/internal/jobs/submit.go`, `api/internal/pipeline/submit.go`, `api/internal/pipeline/submit_test.go`, `api/internal/httpapi/submit_handlers.go`, `api/internal/httpapi/submit_handlers_test.go`
- Modify: `api/internal/store/store.go` (`Migrate` also runs River's migrations through `rivermigrate`), `api/internal/store/storetest/storetest.go` (truncate `river_job` too), `api/internal/pipeline/drafts.go` (`Submit`, `RunSubmit`), `api/internal/httpapi/router.go`, `api/go.mod`

**Interfaces:**
- Produces in `jobs`:
  - `type SubmitArgs struct { DraftID uuid.UUID `json:"draft_id"` }` with `Kind() == "draft_submit"`.
  - `type Submitter interface { RunSubmit(ctx context.Context, draftID uuid.UUID) error }` (satisfied by `*pipeline.Drafts`).
  - `func NewClient(pool *pgxpool.Pool, s Submitter, logger *slog.Logger) (*river.Client[pgx.Tx], error)`: one queue `default` with `MaxWorkers 4`; `SubmitWorker` calls `RunSubmit`; retry policy: River default, `MaxAttempts 4`.
  - `func Enqueue(ctx, c *river.Client[pgx.Tx], draftID uuid.UUID) (jobID int64, err error)` with `river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{Available, Scheduled, Running, Retryable}}}`; when River reports the insert was skipped as a duplicate, returns the existing job's ID.
  - `func Status(ctx, c *river.Client[pgx.Tx], jobID int64) (JobState, error)` where `JobState` has `Status` mapped to `queued` (available/scheduled/retryable/pending), `running`, `succeeded` (completed), `failed` (discarded/cancelled) and `DraftID`.
- Produces in `pipeline`:
  - `type SubmitOutcome struct { JobID int64; Draft Draft; AlreadySubmitted bool }`
  - `func (d *Drafts) Submit(ctx, userID, id uuid.UUID, enqueue func(ctx, draftID uuid.UUID) (int64, error)) (SubmitOutcome, error)`: `submitted` → `AlreadySubmitted`; `submitting` → return the job ID stored in `drafts.job_id`; otherwise re-run `Validate` on `Payload()`; any unresolved → `ErrUnresolved{Fields []string}`; set status `submitting`, enqueue, store `job_id`, return.
  - `func (d *Drafts) RunSubmit(ctx, draftID uuid.UUID) error` (the worker body): load the draft; if `submitted` return nil; build `procore.RFICreate` from `Payload()` (`question.body` = `question_body`; `due_date` string; impacts as given); `attemptStart := now()`; call `CreateRFI` with the user's token. On success: `SetDraftSubmitted` (id, number, link), audit `submitted` with request and response, return nil. On `*procore.ValidationError`: map keys (`question.body` and `question` → `question_body`; strip a leading `rfi.`; others unchanged; unknown keys → field `_form`) to `Unresolved{Field, ReasonProcoreValidation, Message = joined messages}`, store them, `SetDraftFailed` with code `procore_validation`, audit `failed`, return nil (not retried). On `auth.ErrReauthRequired` or `procore.ErrPermissionDenied`: `SetDraftFailed` with code `reauth_required` / `procore_permission_denied`, audit `failed`, return nil. On any other error (timeout, 5xx, network): call `ListRFIs`, and if one has `Subject == payload.subject` and `CreatedAt >= attemptStart - 1 minute`, treat it as the created record (store, audit `submitted` with `procore_response = {"recovered": true, ...}`), return nil; otherwise audit `failed` with the error and return the error so River retries. After the last attempt River discards the job; a `river.ErrorHandler` hook marks the draft `failed` with code `procore_unavailable`.
  - `var ErrUnresolved` type with `Fields`.
- `store.Migrate`: after golang-migrate, run `rivermigrate.New(riverpgxv5.New(pool), nil).Migrate(ctx, rivermigrate.DirectionUp, nil)`.
- Handlers: `SubmitDraft` (header `Idempotency-Key` must equal `draftId` else 400 `bad_request` "Idempotency-Key must equal the draft id"; `ErrUnresolved` → 400 with message listing the fields; `AlreadySubmitted` → 200 `{status: submitted}`; else 202 `{status: submitting, job_id}`); `GetJob` (loads River job; its `DraftID` must belong to the user else 404; `result`/`error` from the draft).
- Startup: River client created in `main.go` `buildDeps`, started with `client.Start(ctx)` after the HTTP server is built, stopped on shutdown with `client.Stop`.

- [ ] **Step 1: Add the SDK** `go get github.com/riverqueue/river@v0.49.0 github.com/riverqueue/river/riverdriver/riverpgxv5@v0.49.0`.
- [ ] **Step 2: Write the failing pipeline tests** in `submit_test.go` (DB-backed; `pipelinetest.FakeProcore` scripted `CreateRFI`; River client from `jobs.NewClient` started in the test with `rivertest` or by calling `RunSubmit` directly for the worker-body tests):
  - `TestRunSubmit_PostsAndStoresResult`: 201 `{id 501, number 214, link ...}` → status `submitted`, result stored, audit `submitted` with request body containing `rfi.subject`.
  - `TestSubmit_Twice_PostsOnce`: `Submit` then `RunSubmit`, then `Submit` again → `AlreadySubmitted`, fake `CreateRFI` called once.
  - `TestRunSubmit_422_FlagsFields`: `ValidationError{"due_date": ["can't be blank"], "question.body": ["too short"]}` → status `failed`, unresolved contains `due_date` and `question_body` with reason `procore_validation` and the messages; `CreateRFI` not retried; error nil.
  - `TestRunSubmit_LostResponse_FindsExistingRFI`: `CreateRFI` returns `ErrUnavailable`; `ListRFIs` returns an RFI with the same subject created after `attemptStart` → status `submitted`, no second POST, audit response has `recovered: true`.
  - `TestRunSubmit_LostResponse_NoMatch_ReturnsErrorForRetry`.
  - `TestSubmit_UnresolvedPayload_Rejected`: payload with `assignee_ids []` and unresolved `assignee_ids` → `ErrUnresolved` with `Fields == ["assignee_ids"]`; status unchanged.
  - `TestRunSubmit_Reauth_MarksFailed`.
- [ ] **Step 3: Run** → FAIL; implement `jobs/`, `pipeline/submit.go`, store migration hook.
- [ ] **Step 4: Run** pipeline and store tests with `TEST_DATABASE_URL` → PASS.
- [ ] **Step 5: Write the failing handler tests**: `TestSubmitDraft_Enqueues202ThenJobSucceeds` (test server runs the River client; poll `GET /jobs/{id}` until `succeeded` within 10 s; `GET /drafts/{id}` shows `submitted` with `result.number == "214"`), `TestSubmitDraft_KeyMismatch_400`, `TestSubmitDraft_Unresolved_400`, `TestSubmitDraft_AlreadySubmitted_200`, `TestGetJob_OtherUser_404`.
- [ ] **Step 6: Implement handlers and wiring**; run `go test ./...` with `TEST_DATABASE_URL` → PASS; `go vet ./...` clean.
- [ ] **Step 7: Commit** `feat(api): idempotent RFI submit through River with 422 mapping and lost-response recovery`.

### Task 10: Eval set and CI eval job

**Files:**
- Create: `api/internal/features/rfi/evals/cases/01-twisted-column.json` … `10-filler-and-restarts.json`, `api/internal/features/rfi/evals/evals_test.go` (build tag `evals`), `api/internal/features/rfi/evals/README.md`
- Modify: `.github/workflows/api.yml` (job `api-evals`)

**Interfaces:**
- Case file shape: `{ "name", "transcript", "today": "2026-11-10", "time_zone": "America/Chicago", "user": {"procore_id": 42, "name": "Pat Field"}, "fixture": {"users": [LookupRow...], "locations": [...], "spec_sections": [...], "cost_codes": [...], "vendors": [...], "drawings": [...]}, "expect": {"payload": {...fields that must match exactly...}, "absent": ["due_date", "cost_impact"], "unresolved": ["assignee_ids"] } }`.
- The test builds `pipeline.Drafts` with real `llm.NewClient` (from `ANTHROPIC_API_KEY`, `LLM_EXTRACT_MODEL`), a `Lookups` seeded by writing the fixture straight into `lookup_cache` (so no Procore), runs every case, and reports per case: fields matched, hallucinated IDs after validation (must be 0, fails the test), unresolved precision (flagged fields ∩ expected unresolved / flagged), latency; prints a summary table and p95. Thresholds that fail the test: any hallucination, p95 over 8 s, exact-match rate under 0.8 across all expected fields.
- Cases, with the fixture carrying Ben West (11, Architect), Mike Ross (12, Project Manager), Mike Chen (13, Electrician, vendor Chen Electric), Pat Field (42, Superintendent); locations `Level 2 > Mechanical Room` (201), `Roof > Air Handler Platform` (202), `Level 1 > Lobby` (203); spec sections `23 05 00 Common Work Results for HVAC` (301), `06 10 00 Rough Carpentry` (302); cost codes `01-310 Supervision` (401); vendors `West Studio` (501, Architecture), `Chen Electric` (502); drawings `M-301 Mechanical Plan Level 2` (601), `S-201 Framing Plan` (602):
  1. twisted column (expect assignee 11, location 202 or unresolved not_found is acceptable, absent due_date and both impacts)
  2. "assign to Mike" (expect unresolved `assignee_ids` with two candidates)
  3. "due next Friday" with today a Tuesday (expect `due_date` = that Friday's date)
  4. spoken sheet "M dash 301" (expect `drawing_number` "M-301")
  5. no cost code mentioned, spec words "mechanical" (expect `specification_section_id` 301, absent `cost_code_id`)
  6. location not in tree, "the south stair" (expect absent `location_id` or unresolved not_found, never an ID)
  7. assignee not in directory, "send it to Jorge" (expect unresolved `assignee_ids` not_found, `assignee_ids []`)
  8. "this is holding up rough-in, cost unknown" (expect `schedule_impact.status yes_unknown`, `cost_impact.status yes_unknown`)
  9. "about two thousand dollars extra" (expect `cost_impact {yes_known, value "2000"}`)
  10. filler and restarts, same content as case 1 (expect same payload as case 1)

- [ ] **Step 1: Write the ten case files and `evals_test.go`** as above; `go vet -tags evals ./...` compiles it.
- [ ] **Step 2: Run** `cd api && ANTHROPIC_API_KEY=... LLM_EXTRACT_MODEL=claude-sonnet-5-5 go test -tags evals ./internal/features/rfi/evals/ -v` → the table prints; tune `prompt.md` until thresholds pass (cases 6 and 7 must never produce an ID). Record the final numbers in `evals/README.md`.
- [ ] **Step 3: Add the CI job** `api-evals` to `api.yml`: runs on `push` to `develop`/`main` and on `workflow_dispatch` only when paths `api/internal/features/**`, `api/internal/llm/**`, `api/internal/pipeline/**` changed (use `dorny/paths-filter` pinned by SHA, or a `git diff --name-only` step); needs Postgres like `api-test`; env `ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}`, `LLM_EXTRACT_MODEL: claude-sonnet-5-5`; skips with a notice when the secret is empty (pull requests from forks).
- [ ] **Step 4: Commit** `feat(rfi): ten-case eval set with hallucination, precision and latency thresholds; CI eval job`.

### Task 11: Wire the binary, deploy config and docs

**Files:**
- Modify: `api/cmd/api/main.go`, `deploy/.env.dev.example`, `deploy/.env.prod.example`, `deploy/README.md` (step 5 lists the three LLM variables), `CLAUDE.md` (Status, Code map rows for `llm`, `pipeline`, `features/rfi`, `jobs`; commands for the eval test), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 2 status)

- [ ] **Step 1: `buildDeps`** constructs `llm.NewClient(cfg.LLM.APIKey, cfg.LLM.ExtractModel, "", cfg.LLM.Timeout)`, `pipeline.NewLookups(st, pc, tokens, 10*time.Minute, time.Now, logger)`, `pipeline.NewDrafts(..., map[string]pipeline.Feature{"rfi": rfi.New()}, cfg.LLM.ExtractModel, time.Now, logger)`, `jobs.NewClient(st.Pool, drafts, logger)`; `main` starts the River client before `ListenAndServe` and stops it during shutdown.
- [ ] **Step 2: Verify locally**: `make db-up && make run` with a local `.env` carrying the three LLM variables; `curl localhost:8080/healthz` → `{"status":"ok"}`; server log shows River started.
- [ ] **Step 3: Update the docs** listed above. In `CLAUDE.md` Status: "Milestone 2 backend implemented; iOS capture/review in `2026-10-24-milestone-2-ios.md`."
- [ ] **Step 4: Run** `go vet ./... && TEST_DATABASE_URL=... go test ./...` → PASS; `make check-contract` → in sync.
- [ ] **Step 5: Commit** `feat(api): wire extraction, lookups and submit worker into the server; docs`.

### Task 12: Drew's environment steps, merge and dev verification

**Files:** none in the repo beyond the merge.

- [ ] **Step 1 (Drew): Anthropic key.** Create a key in the Anthropic Console for this project. Append to `/opt/fieldassistant/deploy/.env.dev` on the droplet (`ssh fieldassistant-droplet`): `ANTHROPIC_API_KEY=<key>`, `LLM_EXTRACT_MODEL=claude-sonnet-5-5`, `LLM_TIMEOUT_SECONDS=20`. Add the same three to `.env.prod` with a second key (prod stack stays stopped). Set the GitHub secret: `gh secret set ANTHROPIC_API_KEY --repo monroeresidential/fieldassistant`.
- [ ] **Step 2 (Drew): Sandbox data.** In the sandbox project used for verification, make sure the directory has an architect named Ben West and two people who share a first name, and the location tree has a mechanical room. Note the project ID.
- [ ] **Step 3: Merge** `milestone-2` into `develop` (`git checkout develop && git merge --no-ff milestone-2 && git push`); watch `gh run list --workflow deploy-api.yml --limit 1` until `deploy-dev` succeeds.
- [ ] **Step 4: Verify on dev.** `curl https://api-dev.fieldassistant.tools/healthz` → ok. Then, from the iOS Dev build (milestone 2 iOS plan Task 9) or with a session token obtained by signing in through the browser and exchanging the one-time code with `curl` (see `/auth/exchange` in the contract), call `GET /v1/projects/{id}/lookups/users` with `X-Company-Id` → 200 with the sandbox directory; `POST /v1/drafts` with the twisted-column transcript → `DraftResponse` with `assignee_ids` resolved to Ben West; `POST /v1/drafts/{id}/submit` with `Idempotency-Key` → 202; `GET /v1/jobs/{id}` → `succeeded`; the RFI appears in the sandbox project's RFI list.
- [ ] **Step 5: Record** the draft ID, RFI number and eval numbers in the roadmap's milestone 2 row; commit `docs: milestone 2 backend verified on dev`.
