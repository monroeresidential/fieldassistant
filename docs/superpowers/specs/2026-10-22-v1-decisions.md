# Field Assistant v1 decisions (amends procore-field-assistant-spec.md)

Oct 22, 2026

The project spec describes a product that could go to the Procore Marketplace.
v1 is for Monroe Residential / 3F Construction only: one company, tens of users.
This document records what changes for v1. Where it conflicts with the spec, this
document wins. Everything here is reversible later; the spec remains the target
architecture.

## Repository: one repo, three folders

Decided: a single repo (this one), not three.

```
fieldassistant/
  api/     Go backend; own go.mod; api/openapi.yaml is the contract
  ios/     Xcode project + FieldAssistantAPI Swift package generated from the contract
  web/     Vite + React + TypeScript; client generated from the contract
  docs/    specs and plans
```

Why: one builder, one contract that changes weekly at first. A monorepo lets the
contract and all clients change in one commit with no tag pinning. The spec's
objections (Go tooling vs Xcode, Xcode Cloud) do not apply when the Go module
lives in `api/` and each CI job targets its folder with path filters.

Consequences:
- Clients consume `api/openapi.yaml` by relative path, not by pinned release tag.
- No "support two minor contract versions." Breaking changes ship with a new
  TestFlight build; users are told to update.
- Split into separate repos only if separate teams ever need separate cadences.

## Sequencing: phone is the product, website is phase 2 (Drew's decision)

The spec builds the web app from phase 2 alongside iOS. Decided for v1: iOS and
backend through the site pilot; the web app is its own later phase, starting
after the Jan 8, 2027 gate. Reason: superintendents in the field are the users;
a second client while the contract is still moving doubles sync work.

## Trimmed for v1 (keep the column, skip the machinery)

| Spec item | v1 |
| --- | --- |
| OpenTelemetry traces, Prometheus metrics | slog structured logs; uptime check on `/healthz`; alert on job failures from logs |
| Envelope encryption, KMS, quarterly key rotation | AES-GCM with one key from the env file; key rotation is a manual migration if ever needed |
| `doppler` / `sops` secrets | `.env` on the droplet, excluded from git |
| dev / staging / prod | dev (local Docker + Procore sandbox) and prod only |
| Certificate pinning in iOS | skip |
| Cloudflare WAF rate limiting, Cloudflare Access | skip; Cloudflare DNS + proxy only |
| `GET /admin/audit` CSV export | skip; a SQL query on request |
| 50-transcript eval set per feature | 10 to start, grown from audit-log corrections |
| Multi-tenant | keep `company_id` on every table; build no tenant admin, billing, or Marketplace surface |
| Managed Postgres | decided: Postgres in Docker Compose on the droplet with nightly `pg_dump` to Spaces for the pilot; move to Managed Postgres before any second company |

## Unchanged and non-negotiable

- Only the backend holds Procore credentials and the LLM key; clients hold a session token.
- Every Procore ID comes from a lookup; the backend validates all IDs after extraction.
- Submit is idempotent on the draft UUID.
- Review is mandatory; nothing posts without a tap.
- Every draft writes an audit row with transcript, LLM output, user diff, payload, Procore response, model version.
- Model IDs and prompts live in config and versioned files; prompt changes must pass the eval set.

## Still open (from the spec)

- Spanish dictation for v1 crews.
- App name and bundle identifier.
- Who pilots the first TestFlight build and on which project.
- "Dictate and fire" for trusted users (deferred past v1).
