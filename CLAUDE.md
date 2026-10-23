# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

Phase 1 is implemented on branch `phase-1`: Go backend plus iOS sign-in, company/project picker and RFI list. Next: Phase 1 sandbox verification, then milestone 2 (RFI extraction) per `docs/superpowers/plans/2026-10-22-roadmap.md`.

Governing documents:

- `procore-field-assistant-spec.md`: the full project spec and target architecture.
- `docs/superpowers/specs/2026-10-22-v1-decisions.md`: v1 amendments for a single company with tens of users. Where it conflicts with the spec, it wins.

Implementation plans live in `docs/superpowers/plans/`.

## Commands

```
make db-up / make db-down      # local Postgres in Docker (api/docker-compose.yml)
make run                       # API server; needs api/.env (copy api/.env.example)
make test                      # go test ./... (DB tests skip without TEST_DATABASE_URL)
make generate                  # oapi-codegen + sqlc; commit the output
make sync-contract             # copy api/openapi.yaml into the iOS package
make check-contract            # fail if the iOS copy is stale (CI runs this)
make ios-project               # xcodegen + install ios/Package.resolved
```

DB-backed Go tests (needs `make db-up`):

```
cd api && TEST_DATABASE_URL='postgres://fa:fa@localhost:5432/fieldassistant_test?sslmode=disable' go test ./...
```

Single Go test: `cd api && go test ./internal/<pkg>/ -run <Name>`

iOS tests (run `make ios-project` first; CI uses `iPhone 16`). Do not add `-quiet` if you want the `** TEST SUCCEEDED **` line; the exit code is the pass signal either way:

```
cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile
```

Single iOS test class: add `-only-testing:FieldAssistantTests/<Class>`.

Fastlane (from `ios/`, via `gem install fastlane`, `brew install fastlane` or `bundle exec`):

- `fastlane test`: regenerates the project and runs the unit tests on iPhone 17.
- `fastlane beta`: builds the Prod configuration and uploads to TestFlight. Needs `ASC_KEY_ID`, `ASC_ISSUER_ID` and `ASC_KEY_CONTENT` (base64 App Store Connect key) in the environment, and the Team ID set in `ios/project.yml`.

Deploy: see `deploy/README.md` (droplet setup, secrets, backups) and `.github/workflows/deploy-api.yml`.

## Code map

| Path | Role |
| --- | --- |
| `api/openapi.yaml` | The contract. Source of the Go server interface and the Swift client. |
| `api/internal/config` | Environment-driven config loading and validation. |
| `api/internal/httpapi` | chi router, generated server glue, handlers, middleware, error mapping. |
| `api/internal/auth` | Procore OAuth broker, session JWTs, refresh-token vault, company cache. |
| `api/internal/procore` | Procore HTTP client (rate limits, pagination, company header) and OAuth calls. |
| `api/internal/store` | Postgres access: migrations, sqlc queries and generated code in `db/`. |
| `api/internal/store/storetest` | Shared test helper: migrates and truncates the test database. |
| `ios/FieldAssistant/App` | App entry, root view, app state, build-time config. |
| `ios/FieldAssistant/Core/Auth` | Sign-in flow, Keychain session store, token refresh. |
| `ios/FieldAssistant/Core/Network` | API client wrapper, auth middleware, failure mapping. |
| `ios/FieldAssistant/Core/Scope` | Company and project selection and its persistence. |
| `ios/FieldAssistant/Features/RFI` | RFI list model and view. |
| `ios/Packages/FieldAssistantAPI` | Generated Swift client package; holds a synced copy of the contract. |
| `deploy/` | Droplet Compose file, Caddyfile, backup script, runbook. |
| `.github/workflows/` | `api.yml` and `ios.yml` (CI), `deploy-api.yml` (droplet deploy). |

## Conventions

- Contract changes bump `info.version` in `api/openapi.yaml` and require `make generate` and `make sync-contract` in the same commit.
- Generated code is committed.
- Migration `000001` may be edited until the first deploy. After editing, recreate local DBs: `docker compose -f api/docker-compose.yml down -v && make db-up`.
- DB tests serialize through an advisory lock in `storetest`; do not bypass it.
- The iOS `.xcodeproj` is generated and never committed. Swift package pins live in `ios/Package.resolved`.
- Error bodies are `{code,message}` with a fixed code set: `bad_request`, `unauthorized`, `reauth_required`, `forbidden_scope`, `procore_permission_denied`, `procore_unavailable`, `internal`.
- On iOS only `AuthRefreshError.rejected` signs the user out; other refresh failures keep the session.

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

Decided: the production domain is `fieldassistant.tools` (API at `api.fieldassistant.tools`). Still unresolved: Spanish dictation, the bundle identifier and Apple Team ID (`com.monroeresidential.fieldassistant` is a placeholder), and who pilots the first TestFlight build. Do not silently resolve these in code; surface them.
