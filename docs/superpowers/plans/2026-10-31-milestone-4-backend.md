# Milestone 4 Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The API tells the phone which features a user can use on a project with Procore's reason, flags Procore's field errors on HTTP 400 as well as 422 without truncating them, sources RFI manager candidates from Procore's own list, survives a flaky refresh without signing anyone out, and prints the weekly pilot summary from the audit trail.

**Architecture:** A `features` probe sits beside the lookup cache and answers `GET /projects/{id}/features` (contract 1.7.0) from a per-user, per-project probe cached with the lookup TTL. The Procore client reads error bodies in full before parsing and produces `*ValidationError` on 400 too. A new lookup kind `rfi_managers` wraps `potential_rfi_managers`. `Sessions` gains a vault, a rotation timestamp and an encrypted copy of the current refresh token so a replay inside the grace window gets the same pair. `cmd/pilotreport` is a second binary in the API image.

**Tech Stack:** Go 1.26, chi + oapi-codegen strict server, sqlc + pgx, golang-migrate (next `000005`), AES-GCM vault, Procore REST v1.0.

**Spec:** `docs/superpowers/specs/2026-10-31-milestone-4-hardening-testflight-design.md` (sections 3, 4, 5 backend half, 7). Companion plans: `2026-10-31-milestone-4-ios.md`, `2026-10-31-milestone-4-pilot-runbook.md`.

## Global Constraints

- Only the backend talks to Procore; every project call carries `Procore-Company-Id`; `company_id` on every table.
- Contract `1.7.0`; `make generate` + `make sync-contract` in the same commit; `make check-contract` green; generated code committed. No new HTTP error code: the fixed set stays (`bad_request, unauthorized, reauth_required, forbidden_scope, procore_permission_denied, not_found, procore_unavailable, internal`).
- `procore_permission_denied` responses carry Procore's own text in `message` when the client had a body (first 200 runes, trimmed), else today's "Procore permission denied".
- Grace window `120 s` (`auth.RefreshGrace = 120 * time.Second`), applying to the immediately previous refresh token only; a replay returns the exact current pair; a lost compare-and-swap race is a replay, not theft; anything older is theft as today.
- Error-body handling: read up to `64 KiB` (`maxErrorBodyParse = 64 << 10`) for parsing; store/log at most 200 runes as today.
- Lookup kind `rfi_managers` → `GET /rest/v1.0/projects/{id}/rfis/potential_rfi_managers` (rows `{id, name, login}`), cached like the other kinds; the features probe for `rfi` is that same call by default (`FEATURES_PROBE=rfi_managers`), with `FEATURES_PROBE=create_outcome` as the fallback strategy (availability follows the last RFI create outcome for that user and project within the TTL).
- `pilotreport` is read-only, takes `--from`/`--to` (RFC 3339 dates, default last seven days ending now), prints Markdown to stdout, exit 0; a correction is a field whose final payload value differs from `llm_output`'s (including filled-in empties), counted per field; duplicates are more than one `submitted` event per draft, any `attempt` marker whose outcome is unknown at `--to`, or a mirror RFI matching a submitted draft's subject and creator with a different id.
- Every existing test stays green; DB tests through `storetest`; `go vet` clean.

## Review Focus

1. A user who is Read-only on RFIs but on the project: `GET /features` must say `rfi` unavailable with Procore's reason, and the lookup endpoints must still work (users list is readable). Test in Task 1 (`TestFeatures_ReadOnlyUser_RFIUnavailableWithReason`).
2. A 422 whose body is 3 KB (Procore lists every configurable field): fields must map, not fall through to `_form`. Test in Task 2 (`TestClient_CreateRFI_422_LargeBody_IsValidationError`).
3. Two refreshes from one phone three seconds apart, the first response lost: the phone presents the previous token; it must get the current pair and stay signed in. Test in Task 3 (`TestSessions_RefreshReplayWithinGrace_ReturnsSamePair`).
4. The previous token presented 121 seconds after rotation: theft, session deleted. Test in Task 3 (`TestSessions_RefreshReplayAfterGrace_Revokes`).
5. A draft edited four times on one field and once on another: corrections = 2, not 5. Test in Task 4 (`TestPilotReport_CorrectionsCountFieldsNotSaves`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `api/internal/pipeline/features.go` (+ `_test.go`) | `Features` type: per-user probe and cache, `Get(ctx, userID, companyID, projectID)`. |
| `api/internal/httpapi/feature_handlers.go` (+ `_test.go`) | `GetProjectFeatures`. |
| `api/internal/procore/client.go` | Error-body cap, 400 field errors, `ListPotentialRFIManagers`. |
| `api/internal/pipeline/lookups.go`, `features/rfi/rfi.go` | `KindRFIManagers`; RFI manager candidates and default. |
| `api/internal/httpapi/scope_handlers.go` | `mapProcoreErr` carries Procore's message on 403. |
| `api/internal/auth/sessions.go` (+ tests), `store/migrations/000005_session_grace.*`, `queries/sessions.sql` | Grace window. |
| `api/internal/auth/broker_test.go` | Logout revoke test. |
| `api/cmd/pilotreport/main.go`, `api/internal/report/*.go` (+ tests), `store/queries/report.sql` | Weekly summary. |
| `api/Dockerfile`, `Makefile`, `deploy/README.md` | Second binary, `make pilot-report`, runbook pointers. |
| `api/openapi.yaml`, `gen.go`, iOS copy | Contract 1.7.0. |
| `CLAUDE.md`, roadmap | Docs. |

---

### Task 1: Features probe and `GET /projects/{projectId}/features`

**Files:**
- Create: `api/internal/pipeline/features.go`, `api/internal/pipeline/features_test.go`, `api/internal/httpapi/feature_handlers.go`, `api/internal/httpapi/feature_handlers_test.go`
- Modify: `api/openapi.yaml` (`1.7.0`), `api/internal/httpapi/router.go` (`Deps.Features`), `api/internal/httpapi/scope_handlers.go:27-38` (`mapProcoreErr` message), `api/internal/config/config.go` (`FEATURES_PROBE`), `api/cmd/api/main.go`, `api/internal/procore/client.go` (`ListPotentialRFIManagers`), `api/internal/pipeline/pipelinetest/fake_procore.go`

**Interfaces:**
- Contract:
  - `GET /projects/{projectId}/features`, operationId `getProjectFeatures`, header `X-Company-Id`; 200 `FeaturesResponse {features: [FeatureState], fetched_at}`; `FeatureState {feature: enum [rfi], available: bool, reason?: string}`; errors 401, 403, 502, default.
- Go:
  ```go
  package procore
  type RFIManager struct { ID int64 `json:"id"`; Name string `json:"name"`; Login string `json:"login"` }
  func (c *Client) ListPotentialRFIManagers(ctx context.Context, accessToken string, companyID, projectID int64) ([]RFIManager, error) // GET /rest/v1.0/projects/{id}/rfis/potential_rfi_managers, paginated via listAll

  package pipeline
  type FeatureState struct { Feature string; Available bool; Reason string }
  type FeatureProbe string
  const ( ProbeRFIManagers FeatureProbe = "rfi_managers"; ProbeCreateOutcome FeatureProbe = "create_outcome" )
  type Features struct { /* lookups *Lookups, pc ProcoreLookups, tokens AccessTokenSource, probe FeatureProbe, ttl, now, mu, cache map[featureKey]featureEntry */ }
  func NewFeatures(lookups *Lookups, pc ProcoreLookups, tokens AccessTokenSource, probe FeatureProbe, ttl time.Duration, now func() time.Time, logger *slog.Logger) *Features
  func (f *Features) Get(ctx context.Context, userID uuid.UUID, companyID, projectID int64, force bool) ([]FeatureState, time.Time, error)
  // RecordCreateOutcome is called by the submit path (Task 2 wires it) so ProbeCreateOutcome has data.
  func (f *Features) RecordCreateOutcome(userID uuid.UUID, companyID, projectID int64, err error)
  ```
  `ProcoreLookups` gains `ListPotentialRFIManagers`. Project visibility is checked first through `lookups.requireAccess`; `ProbeRFIManagers` calls the list and maps 403 → unavailable with `denialReason`-style text, 404 → unavailable "RFI tool not enabled on this project", success → available; `ProbeCreateOutcome` reads the recorded outcome (none → available). Results cache for `lookupCacheTTL`; `force` (header `Cache-Control: no-cache`) refetches; the project's `POST /projects/{id}/sync` handler calls `Features.Invalidate(userID, companyID, projectID)`.
- `mapProcoreErr`: for `procore.ErrPermissionDenied` wrapping an `*procore.APIError` with a non-empty body, `message` is the body's first 200 runes with surrounding JSON noise stripped (`{"errors":["App is not connected to this company."]}` → `App is not connected to this company.`); otherwise "Procore permission denied". Helper `procoreReason(err error) string` shared with the features probe.

- [ ] **Step 1: Write the failing tests**: `features_test.go` (DB-backed via `storetest`, fake Procore): `TestFeatures_StandardUser_RFIAvailable`, `TestFeatures_ReadOnlyUser_RFIUnavailableWithReason` (fake answers 403 with body `{"errors":["Permission denied"]}` → `Available=false`, `Reason="Permission denied"`), `TestFeatures_ToolOff_404_Unavailable`, `TestFeatures_CachedForTTL_ThenRefetched` (one Procore call within the TTL, a second after), `TestFeatures_ForceRefetches`, `TestFeatures_InvalidateDropsCache`, `TestFeatures_CreateOutcomeProbe_FollowsLastOutcome`; `feature_handlers_test.go`: `TestGetFeatures_200`, `TestGetFeatures_CompanyNotVisible_403`, `TestGetFeatures_NoCache_Refetches`; `scope_handlers_test.go`: `TestProjects_PermissionDenied_CarriesProcoreReason` (403 body → `message` equals the stripped text) and `TestProjects_PermissionDenied_NoBody_GenericMessage`. Client: `TestClient_ListPotentialRFIManagers_PathAndDecode`.
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/pipeline/ -run Features ./internal/httpapi/ -run 'Features|PermissionDenied' ./internal/procore/ -run RFIManagers` — expected: compile failures.
- [ ] **Step 3: Implement** the contract, `make generate && make sync-contract`, the client method, `Features`, the handler, `mapProcoreErr`, config (`FEATURES_PROBE` optional, default `rfi_managers`, validated against the two values), `main.go` wiring, and the fake (`RFIManagers []procore.RFIManager`, `RFIManagersErr error`, recorded `Call{Method: "ListPotentialRFIManagers"}`).
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./... && make check-contract` — expected: PASS.
- [ ] **Step 5: Commit** `feat(api): project features endpoint with Procore's reason; contract 1.7.0`

---

### Task 2: Procore error bodies, 400 field errors, RFI manager candidates

**Files:**
- Modify: `api/internal/procore/client.go` (`decodeResponse`, `maxErrBody` use), `api/internal/procore/client_test.go`, `api/internal/pipeline/submit.go` (classification unchanged for `*ValidationError`; `RecordCreateOutcome` call after `CreateRFI`), `api/internal/pipeline/lookups.go` (`KindRFIManagers`), `api/internal/procore/client.go` (`lookupEndpoint` for the new kind), `api/internal/features/rfi/rfi.go` (`IDFields` for `rfi_manager_id`, `ApplyDefaults`), `api/openapi.yaml` (lookup `kind` enum gains `rfi_managers`; still 1.7.0 within this plan), `api/internal/pipeline/submit_test.go` (new cases), `api/internal/pipeline/lookups_test.go` (`TestLookupKinds_MatchProcoreKinds` extended)

**Interfaces:**
- `decodeResponse` reads the body with `io.LimitReader(resp.Body, maxErrorBodyParse)` (`64 << 10`), parses `errors` on **400 and 422** into `*ValidationError` when the shape matches, and builds `*APIError{Status, Body: first 200 runes}` otherwise; `ErrUnauthorized`/`ErrPermissionDenied` wrapping unchanged.
- `const maxErrorBodyParse = 64 << 10` beside `maxErrBody` (log/store cap stays 200).
- `pipeline.KindRFIManagers LookupKind = "rfi_managers"` appended to `AllKinds`; rows `{ID, Name, Label: Name, Sub: Login}` in the existing `LookupRow` shape.
- `features/rfi`: `IDFields["rfi_manager_id"] = pipeline.KindRFIManagers`; `ApplyDefaults` sets `rfi_manager_id = ctx.UserProcoreID` when that id is in the RFI managers set, else leaves it unresolved with reason `not_found`, message "You are not an RFI manager on this project", candidates = the RFI managers rows; the assignee default logic is unchanged.
- Submit path: after `CreateRFI` returns, `d.features.RecordCreateOutcome(cur.UserID, cur.CompanyID, cur.ProjectID, err)` (nil `features` is tolerated for tests that do not wire it).

- [ ] **Step 1: Write the failing tests**: `client_test.go`: `TestClient_CreateRFI_422_LargeBody_IsValidationError` (3 KB body with 40 fields → every field present), `TestClient_CreateRFI_400_WithErrorsMap_IsValidationError` (`400 {"errors":{"rfi_manager_id":["invalid"]}}`), `TestClient_400_WithoutErrorsMap_IsAPIError`, `TestClient_APIErrorBody_CappedAt200Runes`; `submit_test.go`: `TestRunSubmit_400_WithFieldErrors_FlagsField` (draft fails `procore_validation` with `rfi_manager_id` flagged, not `_form`), existing `TestRunSubmit_400_FailsDefinitively` stays green for a bodyless 400; `TestRunSubmit_RecordsCreateOutcomeForFeatures`; rfi feature tests: `TestApplyDefaults_RFIManagerFromPotentialManagers`, `TestApplyDefaults_UserNotManager_Unresolved`; `lookups_test.go`: the kinds test covers `rfi_managers`; `lookup_handlers_test.go`: `TestGetLookup_RFIManagers_Rows`.
- [ ] **Step 2: Run** the named tests — expected: FAIL.
- [ ] **Step 3: Implement**; regenerate; `make sync-contract`.
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./... && make check-contract` — expected: PASS (eval tests unaffected).
- [ ] **Step 5: Commit** `feat(api): parse Procore field errors on 400 and large bodies; RFI managers from potential_rfi_managers`

---

### Task 3: Refresh-token grace window and the logout revoke test

**Files:**
- Create: `api/internal/store/migrations/000005_session_grace.up.sql`, `.down.sql`, `api/internal/auth/broker_test.go`
- Modify: `api/internal/store/queries/sessions.sql`, `api/internal/auth/sessions.go`, `api/internal/auth/sessions_test.go`, `api/cmd/api/main.go`, `api/internal/httpapi/testserver_test.go` (construct with the vault)

**Interfaces:**
- Migration: `alter table sessions add column rotated_at timestamptz, add column refresh_enc bytea;` down drops both.
- Queries: `RotateSessionRefresh` also sets `prev_refresh_hash = refresh_hash, rotated_at = now(), refresh_enc = $5`; `CreateSession` takes `refresh_enc`; `GetSessionByPrevRefreshHash` unchanged (returns `rotated_at`, `refresh_enc` through `select *`).
- `NewSessions(st *store.Store, secret []byte, vault *Vault, now func() time.Time) *Sessions`; `const RefreshGrace = 120 * time.Second`.
- `Refresh`: unknown hash → `GetSessionByPrevRefreshHash`; if found and `now - rotated_at <= RefreshGrace` and the session is unexpired → decrypt `refresh_enc`, return `pair(userID, sessionID, currentRefresh)` without rotating; else delete and `ErrSessionRevoked` as today. `rotate`'s lost race: re-read the session by id; if its `prev_refresh_hash == sess.RefreshHash` within the grace → return the current pair the same way; else `ErrSessionRevoked`. `Create` stores `refresh_enc`.

- [ ] **Step 1: Write the failing tests** in `sessions_test.go`: `TestSessions_RefreshReplayWithinGrace_ReturnsSamePair` (refresh with pair1 → pair2; advance 3 s; refresh with pair1 again → pair2's refresh token exactly, session alive, access token valid), `TestSessions_RefreshReplayAfterGrace_Revokes` (advance 121 s → `ErrSessionRevoked`, session gone), `TestSessions_ReplayOfOlderToken_Revokes` (pair1 after two rotations → revoked), `TestSessions_RotateLosesRace_WithinGrace_ReturnsCurrentPair` (replaces the expectation of the existing `RotateLosesRace` test, which is renamed), `TestSessions_CreateStoresEncryptedRefresh`. `broker_test.go`: `TestBroker_Logout_RevokesAtProcoreThenDeletesSession`, `TestBroker_Logout_ProcoreRevokeFails_StillDeletesSession`. `httpapi`: `TestAuthRefresh_RotatesAndRejectsReuse` is updated to advance the clock past the grace before the reuse assertion, plus `TestAuthRefresh_ReplayWithinGrace_200`.
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/auth/ ./internal/httpapi/ -run 'Sessions|Broker|AuthRefresh'` — expected: compile failure / FAIL.
- [ ] **Step 3: Implement** the migration, queries, `make generate`, `Sessions` changes, wiring.
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./...` — expected: PASS.
- [ ] **Step 5: Commit** `feat(auth): refresh-token replay within 120 s returns the same pair; logout revoke pinned`

---

### Task 4: `pilotreport`

**Files:**
- Create: `api/cmd/pilotreport/main.go`, `api/internal/report/report.go`, `api/internal/report/corrections.go`, `api/internal/report/markdown.go`, `api/internal/report/report_test.go`, `api/internal/store/queries/report.sql`
- Modify: `api/Dockerfile` (build and copy `/pilotreport`), `Makefile` (`pilot-report`), `deploy/README.md` (new "Weekly pilot summary" subsection)

**Interfaces:**
```go
package report
type Window struct { From, To time.Time }
type Summary struct {
    Window Window
    Drafts struct { Created, Built, Submitted, Failed int; FailedByCode map[string]int }
    Corrections struct { PerDraft map[uuid.UUID]int; DraftsOverTwo int; Histogram map[int]int }
    Duplicates struct { MultipleSubmitted []uuid.UUID; UnknownAttempts []uuid.UUID; MirrorMatches []MirrorMatch }
    Extraction struct { P50, P95 time.Duration; N int }
    Photos struct { Pushed int; FailedByCode map[string]int }
    PerUser []UserRow            // name, created, submitted, failed, corrections
    Worst []WorstDraft           // top 5 by corrections: transcript, extracted, final, fields changed
}
func Build(ctx context.Context, q *db.Queries, w Window) (Summary, error)
func Corrections(llmOutput, final json.RawMessage) (fields []string)   // pure; nested impacts compared as values; nil/"" treated as empty
func Markdown(s Summary) string
```
`cmd/pilotreport`: flags `--from`, `--to` (RFC 3339; default `now-7d .. now`), reads `DATABASE_URL`, prints `Markdown(Build(...))`. Queries in `report.sql`: `ListDraftsInWindow`, `ListAuditEventsForDrafts`, `ListAttachmentsForDrafts`, `ListUsersByIDs`, `ListMirrorRFIsForProjectSubjects` (from `user_records` bodies: id, subject, created_by).
Dockerfile: `go build ... -o /out/pilotreport ./cmd/pilotreport` and `COPY --from=build /out/pilotreport /pilotreport`. `Makefile`: `pilot-report: ssh fieldassistant-droplet 'cd /opt/fieldassistant/deploy && sudo docker compose exec -T api-prod /pilotreport $(ARGS)'`.

- [ ] **Step 1: Write the failing tests** (`report_test.go`, DB-backed with seeded rows): `TestPilotReport_CorrectionsCountFieldsNotSaves` (one field edited four times, another once → 2), `TestPilotReport_CorrectionsIncludeFilledEmpties`, `TestPilotReport_DuplicatesFromMultipleSubmittedEvents`, `TestPilotReport_UnknownAttemptListed`, `TestPilotReport_ExtractionPercentiles`, `TestPilotReport_PhotosByCode`, `TestPilotReport_WorstFiveOrdered`, `TestPilotReport_MarkdownHasEverySection` (every heading present with real rows), `TestPilotReport_WindowBounds` (a draft outside the window excluded).
- [ ] **Step 2: Run** `cd api && TEST_DATABASE_URL=... go test ./internal/report/` — expected: compile failure.
- [ ] **Step 3: Implement**; build both binaries (`go build ./...`); `docker build -f api/Dockerfile api` locally and confirm `/pilotreport --help` runs in the image.
- [ ] **Step 4: Run** `cd api && TEST_DATABASE_URL=... go test ./...` — expected: PASS.
- [ ] **Step 5: Commit** `feat(api): pilotreport prints the weekly pilot summary from the audit trail`

---

### Task 5: Docs

**Files:**
- Modify: `CLAUDE.md` (code map rows `api/internal/report`, `api/cmd/pilotreport`; conventions: `rfi_managers` lookup kind, `GET /projects/{id}/features`, 400 field errors, `FEATURES_PROBE`, the grace window; status: milestone 4 backend implemented on branch, pilot pending), `docs/superpowers/plans/2026-10-22-roadmap.md` (milestone 4 row: backend done, date; follow-ups closed: Procore reason on No access, 400 errors map, `potential_rfi_managers`, grace window decided and built), `deploy/README.md` (the "Weekly pilot summary" subsection from Task 4 cross-linked from the runbook; `FEATURES_PROBE` in the env examples)

- [ ] **Step 1: Write the docs**; `make check-contract`; `cd api && go vet ./... && go test ./...` (with the DB) — expected: PASS.
- [ ] **Step 2: Commit** `docs: milestone 4 backend: features, error parsing, grace window, pilot report`
