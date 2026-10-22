# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Current state

Planning stage (Oct 2026). No application code yet. Two documents govern the work:

- `procore-field-assistant-spec.md`: the full project spec and target architecture.
- `docs/superpowers/specs/2026-10-22-v1-decisions.md`: v1 amendments for a single company with tens of users. Where it conflicts with the spec, it wins.

Implementation plans live in `docs/superpowers/plans/`. Update this file with real build, test and codegen commands as each folder gains code.

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

Still unresolved: Spanish dictation, app name and bundle identifier, who pilots the first TestFlight build. Do not silently resolve these in code; surface them.
