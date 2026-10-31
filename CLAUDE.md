# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

Phase 1 is implemented and verified end to end against the Procore sandbox on 2026-10-23 (see git history): Go backend plus iOS sign-in, company/project picker and RFI list. Milestone 2 is implemented and merged to `develop` (backend plus iOS capture, review and submit), and the first dictated RFI was submitted from a phone on 2026-10-24. The project-sync backend (per-user RFI mirror, contract 1.5.0) is merged to `develop` and verified against the sandbox on 2026-10-25 (delta filter, Show bodies, submit path); the phone side (`docs/superpowers/plans/2026-10-25-project-sync-ios.md`, contract 1.5.1) is merged to `develop` (PR #12) and passed the device check on Drew's iPhone on 2026-10-26. The design pass (roadmap 2b, `docs/superpowers/plans/2026-10-26-design-pass.md`) is implemented on branch `design-pass` as of 2026-10-29, with the device check pending. Milestone 3's attachments and queued submit are implemented: the backend (contract 1.6.0: draft attachments in the object store, pushed to Procore by the submit job via Direct File Upload; PR #15) is merged to `develop` and deployed to dev, and the phone side (`docs/superpowers/plans/2026-10-30-milestone-3-attachments-ios.md`: photo pipeline, queued submit, background tasks, Settings photo size) is merged to `develop` (PR #16) and deployed to dev as of 2026-10-31, with the device check pending (list in the roadmap). Milestone 4's backend (spec `docs/superpowers/specs/2026-10-31-milestone-4-hardening-testflight-design.md`, plan `docs/superpowers/plans/2026-10-31-milestone-4-backend.md`, contract 1.7.0: features endpoint, 400 and 422 field errors, `rfi_managers` lookup, 120 s refresh grace window, `pilotreport`) is implemented on branch `m4-hardening-testflight` as of 2026-10-31. Milestone 4 on the phone (`docs/superpowers/plans/2026-10-31-milestone-4-ios.md`: versioned schema, features on Home, one prompt for a Procore sign-out, scope cache, capture and draft fixes, date-stamped build numbers) is implemented on branch `m4-ios` as of 2026-10-31, with the device check pending (runbook section D). Next: the pilot runbook (`docs/superpowers/plans/2026-10-31-milestone-4-pilot-runbook.md`) sections A-E.

Governing documents:

- `procore-field-assistant-spec.md`: the full project spec and target architecture.
- `docs/superpowers/specs/2026-10-22-v1-decisions.md`: v1 amendments for a single company with tens of users. Where it conflicts with the spec, it wins.

Implementation plans live in `docs/superpowers/plans/`.

## Commands

```
make db-up / make db-down      # local Postgres and MinIO (object store) in Docker (api/docker-compose.yml)
make run                       # API server; needs api/.env (copy api/.env.example)
make test                      # go test ./... (DB tests skip without TEST_DATABASE_URL)
make evals                     # live RFI extraction evals; needs ANTHROPIC_API_KEY, LLM_EXTRACT_MODEL and TEST_DATABASE_URL
make generate                  # oapi-codegen + sqlc; commit the output
make sync-contract             # copy api/openapi.yaml into the iOS package
make check-contract            # fail if the iOS copy is stale (CI runs this)
make pilot-report ARGS=...     # weekly pilot summary from the prod database over SSH, as Markdown (deploy/README.md)
make ios-project               # xcodegen + install ios/Package.resolved
```

DB-backed Go tests (needs `make db-up`):

```
cd api && TEST_DATABASE_URL='postgres://fa:fa@localhost:5432/fieldassistant_test?sslmode=disable' go test ./...
```

Single Go test: `cd api && go test ./internal/<pkg>/ -run <Name>`

iOS tests (run `make ios-project` first; CI uses Xcode 26.6 on `macos-26` with `iPhone 17`). Do not add `-quiet` if you want the `** TEST SUCCEEDED **` line; the exit code is the pass signal either way:

```
cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile
```

Single iOS test class: add `-only-testing:FieldAssistantTests/<Class>`.

Fastlane (from `ios/`, via `gem install fastlane`, `brew install fastlane` or `bundle exec`):

- `fastlane test`: regenerates the project and runs the unit tests on iPhone 17.
- `fastlane beta_dev` / `fastlane beta_prod`: build the Dev or Prod configuration and upload to TestFlight. Need `ASC_KEY_ID`, `ASC_ISSUER_ID` and `ASC_KEY_CONTENT` (base64 App Store Connect key) in the environment, and the Apple Team ID, which is set as `DEVELOPMENT_TEAM` on the app target in `ios/project.yml`.
- iOS configurations (`ios/Config/*.xcconfig`): `Local` talks to `localhost:8080` and is what the scheme runs and tests; `Dev` talks to `api-dev.fieldassistant.tools`, shows a DEV badge, and uses the `.dev` bundle id and `fieldassistant-dev` URL scheme so it installs beside Prod; `Prod` talks to `api.fieldassistant.tools`.

Deploy: see `deploy/README.md` (droplet setup, secrets, backups) and `.github/workflows/deploy-api.yml`.

## Code map

| Path | Role |
| --- | --- |
| `api/openapi.yaml` | The contract. Source of the Go server interface and the Swift client. |
| `api/internal/config` | Environment-driven config loading and validation. |
| `api/internal/httpapi` | chi router, generated server glue, handlers, middleware, error mapping. |
| `api/internal/auth` | Procore OAuth broker, session JWTs, refresh-token vault, company cache. |
| `api/internal/procore` | Procore HTTP client (rate limits, pagination, company header) and OAuth calls. |
| `api/internal/llm` | Anthropic client behind an `Extractor` interface, with a scripted fake in `llmtest`. |
| `api/internal/pipeline` | Draft pipeline: lookup cache, extraction, ID validation, draft save and revalidate, submit, audit, features probe. |
| `api/internal/features/rfi` | RFI feature module: schema, versioned `prompt.md`, extraction tools and eval set. |
| `api/internal/jobs` | River client, the idempotent submit worker and the records keep-warm job. |
| `api/internal/records` | Per-user mirror of a project's Procore records (RFIs first): delta pulls by updated_at, six-hourly full sweeps, cursor-paged deltas for the phone, keep-warm job. |
| `api/internal/blob` | Object store behind an interface: S3/Spaces in dev and prod, MinIO locally, memory in tests. |
| `api/internal/store` | Postgres access: migrations, sqlc queries and generated code in `db/`. |
| `api/internal/store/storetest` | Shared test helper: migrates and truncates the test database. |
| `api/internal/report` | Weekly pilot summary built from drafts, audit events, attachments and the mirror; read-only. |
| `api/cmd/pilotreport` | The command that prints the summary as Markdown; `/pilotreport` in the API image. |
| `ios/FieldAssistant/App` | App entry, root view, Home and Settings screens, Home sync header, app state, build-time config. `AppSchema.swift` holds the versioned SwiftData schema and migration plan. |
| `ios/FieldAssistant/Core/Design` | Design tokens, Public Sans type scale, logo assets and the shared components every screen is built from. |
| `ios/FieldAssistant/Core/Auth` | Sign-in flow, Keychain session store, token refresh. |
| `ios/FieldAssistant/Core/Network` | API client wrapper, auth middleware, failure mapping. |
| `ios/FieldAssistant/Core/Scope` | Company and project selection and its persistence. `ScopeCache.swift` caches the companies and projects lists for the session; `FeaturesService.swift` fetches the project's feature states. |
| `ios/FieldAssistant/Core/Capture` | Capture model, on-device speech transcriber, one-shot location, photo saving. Photos go through an ImageIO pipeline at capture: downscale to 2,500 px on the long edge and EXIF strip (orientation and GPS kept). |
| `ios/FieldAssistant/Core/Drafts` | Local draft store, submit and resend sync, reconcile on launch. `SubmitRunner` is the one pass that saves, uploads photos, submits and polls; `BackgroundSync` runs it from the iOS refresh and processing tasks. |
| `ios/FieldAssistant/Core/Lookups` | Lookup lists fetched from the backend and cached locally for review pickers. |
| `ios/FieldAssistant/Core/Records` | Per-user mirror of the project's Procore records on the phone: SwiftData models, delta sync driver, local queries. |
| `ios/FieldAssistant/Core/Review` | Review screen rows, lookup picker, unresolved-field rows, footer. |
| `ios/FieldAssistant/Features/RFI` | Local RFI list and detail from the project mirror, attachment viewer, capture, review and result screens (`ResultSummary` behind Result), Drafts list; review model. |
| `ios/Packages/FieldAssistantAPI` | Generated Swift client package; holds a synced copy of the contract. |
| `deploy/` | Droplet Compose file, Caddyfile, backup script, runbook. |
| `.github/workflows/` | `api.yml` and `ios.yml` (CI), `deploy-api.yml` (droplet deploy). |

## Conventions

- Branches: work on `develop` (or a feature branch merged into it); `main` changes only via pull request from `develop`. Pushes to `develop` deploy dev (`api-dev.fieldassistant.tools`); merges to `main` deploy prod, and each prod deploy pauses at the `production` environment until Drew approves it in the Actions UI.
- After each `develop` → `main` merge, bring `develop` up to date (`git checkout develop && git merge main && git push`, or the PR's Update branch button); otherwise the next promotion PR shows as out of date because `main` requires branches to be current.
- Contract changes bump `info.version` in `api/openapi.yaml` and require `make generate` and `make sync-contract` in the same commit.
- Generated code is committed.
- Migration `000001` may be edited until the first deploy. After editing, recreate local DBs: `docker compose -f api/docker-compose.yml down -v && make db-up`.
- DB tests serialize through an advisory lock in `storetest`; do not bypass it.
- The iOS `.xcodeproj` is generated and never committed. Swift package pins live in `ios/Package.resolved`.
- Error bodies are `{code,message}` with a fixed code set: `bad_request`, `unauthorized`, `reauth_required`, `forbidden_scope`, `procore_permission_denied`, `not_found`, `procore_unavailable`, `internal`. `not_found` is emitted by the RFI attachment proxy for an RFI or attachment that is not there.
- The phone reads RFIs from the local mirror (`getRecords`, `syncProject`); `listRFIs` was removed in contract 1.5.1. `getRFIAttachment` streams the file with Procore's real `Content-Type` (declared `*/*` in the contract).
- Attachment-level codes `procore_refused`, `expired` and `size_mismatch` (the stored object's size differs from its record) live on `DraftResponse.attachments[].error`; the draft-level failure is an `unresolved[]` entry with field `attachments` and reason `procore_validation`. Neither is an HTTP error-body code.
- `procore_validation` is a draft-level error code carried in `DraftResponse.error` and `JobStatus.error`, not an HTTP error-body code.
- `GET /projects/{projectId}/features` returns a `FeatureState` per feature (`available`, optional `reason`), probed by Procore reads or the last create outcome as `FEATURES_PROBE` selects (`rfi_managers`, the default, or `create_outcome`). `SyncProject` invalidates the cached answer.
- Lookup kind `rfi_managers` (Procore's `potential_rfi_managers`) backs `rfi_manager_id` defaults and validation; it is not an extractor tool, so the prompt and tool set stay as they were.
- Procore error bodies are parsed on HTTP 400 as well as 422, so field errors such as `rfi_manager_id invalid` land on draft fields. A `*ValidationError` on a read is `procore_unavailable` (502), not a draft failure.
- Refresh grace window: presenting the immediately previous refresh token within `RefreshGrace` (120 s) of rotation returns the current pair without rotating again (the server keeps it encrypted in `sessions.refresh_enc`). A lost rotation race (two refreshes with the same token at once) is treated as a replay and gets the current pair. Past the window, or with any older token, the session is revoked.
- `procore_permission_denied` responses carry Procore's reason in `message`, so the phone can show why access was refused.
- On iOS only `AuthRefreshError.rejected` signs the user out; other refresh failures keep the session.
- `submitting` plus `submitPending` is the queued intent on the phone; reconcile skips queued drafts; the flag clears when the server owns the draft (200, 202 or a locked 400), on a definitive refusal, on a local photo failure, or when the user taps Stop sending.
- A `reauth_required` 401 that survives the refresh retry ends the session once through `AppState.handleReauthRequired()`; the sign-in screen then says "Procore signed you out. Sign in again and your drafts will send." Queued drafts stay queued across it and send after sign-in.
- Build numbers: `MARKETING_VERSION` is `1.0` in `ios/project.yml`; Fastlane stamps `CURRENT_PROJECT_VERSION` as `yyyyMMddHHmm` UTC through `xcargs` for each TestFlight build (`project.yml` keeps `1`). Info.plist reads both, and `project.yml`'s `info.properties` is the source (xcodegen rewrites Info.plist).
- `AppState.preview` is not `#if DEBUG`: `#Preview` blocks compile into the Prod (Release) build.

## Repository layout

One repo, three folders. The spec's three-repo split was rejected for v1 (see the decisions doc).

```
api/     Go backend (own go.mod). api/openapi.yaml is the contract.
ios/     Xcode project + FieldAssistantAPI Swift package generated from the contract.
web/     Vite + React + TypeScript (phase 2, after the phone pilot). Client generated from the contract.
docs/    specs and plans
```

| Folder | Stack | Deploy |
| --- | --- | --- |
| `api/` | Go 1.23+, `chi`, `oapi-codegen`, `sqlc`, `golang-migrate`, River job queue, Postgres 16, S3-compatible storage (DO Spaces), Claude Go SDK | Docker Compose on a DigitalOcean droplet behind Caddy |
| `ios/` | Swift 6 / SwiftUI, iOS 17+, `swift-openapi-generator`, SwiftData, Fastlane | TestFlight |
| `web/` | Vite + React + TypeScript, TanStack Query, `openapi-typescript` + `openapi-fetch`, Playwright | Cloudflare Pages |

**The OpenAPI contract is the only coupling.** `api/openapi.yaml` generates the Go server interface and both clients, consumed by relative path within the repo. Contract and clients change in the same commit. Breaking changes ship with a new app build; there is no multi-version support in v1.

**Sequencing:** phone is the product. Backend + iOS ship through the site pilot first; the web app is phase 2 and starts after the Jan 8, 2027 feature gate.

## Core architecture

Every feature runs the same four-step loop: **Capture** (on-device voice, photos, GPS) → **Extract** (backend sends transcript to an LLM that fills the Procore schema) → **Review** (user corrects flagged fields; nothing submits without a tap) → **Submit** (idempotent job uploads attachments, posts to Procore, writes audit row).

The pipeline code is written once. A feature module supplies only: a Procore schema + lookup list, an extraction prompt + tool set, a submit job (backend), a capture config and a review screen (clients). Adding a feature is `internal/features/<name>` in Go, `Features/<Name>` in Swift, `src/features/<name>` in TS, plus one OpenAPI schema block. Auth, capture, review, lookups, attachments and audit code must not change per feature.

Feature order: RFI → Observation → Punch item (batch mode, desktop-first) → Daily log → Photo tagging → Submittal → Read/ask.

## Invariants to preserve

- **Only the backend talks to Procore or the LLM.** The iOS and web clients hold no Procore token, no `client_secret`, no LLM key. iOS stores a session JWT in Keychain; web uses an `HttpOnly` cookie.
- **Never submit an invented ID.** Every person, location, spec section, cost code, vendor or drawing ID must come from a `list_*` tool result backed by the lookup cache. The backend validates every ID after extraction and demotes unmatched IDs to `unresolved[]` regardless of model output.
- **Submits are idempotent.** The draft UUID is the `Idempotency-Key`; a retried submit must find the stored Procore ID and not post twice.
- **Review is mandatory in v1.** No fully automatic submission.
- **Audit everything.** Each draft stores transcript, LLM output, user diff, final payload, Procore request/response, timings and `model_version`.
- **Model IDs live in config**, never hard-coded, and are recorded per draft.
- **Prompts are versioned files** (`features/<name>/prompt.md`); a prompt or model change must pass the feature's eval set (field exact-match, zero ID hallucination after validation, unresolved precision, p95 latency).
- **`company_id` on every table** and `Procore-Company-Id` on every Procore request, but no other multi-tenant machinery in v1.
- **Procore client rules:** honor rate-limit headers and back off on 429; paginate list calls fully before caching; sandbox base URL switchable per environment; automated tests run against the Procore sandbox.
- **RFI submit** posts with `run_configurable_validations=true` and maps Procore 422 field errors back onto draft fields; photos are downscaled to ≤ 2,500 px long edge and EXIF-stripped except orientation and GPS.

## v1 simplifications

Decided in the decisions doc: no OpenTelemetry or Prometheus (slog + `/healthz`), one AES key from `.env` instead of envelope encryption, dev + prod only, no cert pinning, no admin export endpoint, Postgres in Docker Compose on the droplet with nightly dumps, ten-case eval sets to start. Do not reintroduce these without asking.

## Open decisions

Decided: the production domain is `fieldassistant.tools` (API at `api.fieldassistant.tools`). The bundle identifiers (`tools.fieldassistant.app`, `tools.fieldassistant.app.dev`) and Apple Team ID (`PR6KG6PZJN`) are decided (2026-10-24). Still unresolved: Spanish dictation and who pilots the first TestFlight build. Do not silently resolve these in code; surface them.
