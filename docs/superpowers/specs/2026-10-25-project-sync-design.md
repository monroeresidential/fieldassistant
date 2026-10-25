# Project sync: a local, synced copy of a project's Procore records on the phone

Date: 2026-10-25. Status: approved by Drew 2026-10-25 (per-user mirror, no offline attachments). Written from Drew's direction on 2026-10-24 without a question round; every choice not his is marked "(my call)".

## Intent

After the first RFI submitted from the phone, Drew asked for the app to hold the project's RFIs itself instead of linking out to Procore: every field on the phone, synced when the app opens and from a Sync button on the project, and built so everything on a project syncs the same way. The phone becomes where the team looks things up on site, signal or not. Procore stays the system of record; the app never becomes a second place to edit.

## Goals

- Opening the app on a project shows that project's RFI list at once from local data, with no network.
- Every RFI field Procore returns is on the phone, including questions, answers and attachment metadata, so the detail screen never needs a round trip.
- Sync runs when the app comes to the foreground and when the user taps Sync on the project; it pulls only what changed.
- An RFI created or changed in Procore (web, mobile, or by this app's own submit) shows up on the phone after the next sync; a deleted RFI disappears.
- The machinery is per project and per record kind, so observations and punch items plug in with a fetcher and a projection, not a second sync.

## Non-goals

- Editing, answering, closing or reassigning RFIs from the phone. Creation keeps going through drafts. The mirror is read-only.
- Attachments and photos stored offline. Attachment metadata syncs; the file opens through the backend when there is signal.
- Background sync while the app is closed. Foreground open and the Sync button are the triggers.
- Procore webhooks in the first cut (see "Later").
- The web client (phase 2) reads the same endpoints later; nothing here is phone-only by design, but no web work is included.

## Anti-goals

- A user seeing a private RFI they could not see in Procore.
- A stale phone that looks fresh: the screen must always say when it last synced and whether a sync failed.
- A sync that hammers Procore: a phone that opens ten times an hour must not pull from Procore ten times.
- Sync work that touches the capture, review or submit paths. The draft pipeline is untouched.

## Constraints

- Only the backend talks to Procore (spec invariant). The phone syncs from our API.
- `company_id` on every table; `Procore-Company-Id` on every Procore call; the lookup cache's per-user project-access probe applies before any record is served.
- Procore list endpoints: `GET /rest/v1.0/projects/{id}/rfis` supports `filters[updated_at]` (ISO 8601 range), `filters[status]`, `search`, `sort[attribute]`, pagination with `Per-Page`, `Total` and `Link` headers. List items carry the full record including `questions` with `attachments` and `answers`. `deleted` and `deleted_at` appear only on deleted records, so the normal list does not announce deletions.
- Procore webhooks are best-effort notifications that must be paired with a reconciliation sync; they need a public HTTPS endpoint and the Webhooks API permission on the company.
- v1 decisions still hold: one company, tens of users, slog only, no new infrastructure.
- Contract changes bump `info.version`, regenerate both clients and ship with a new app build.

## How it works

### Backend: a mirror per user and project

The backend keeps, in Postgres, a mirror of each synced project's records **as seen by each user**, pulled with that user's own Procore token, and serves the phone from it. Procore applies its own visibility rules (private RFIs, drafts, tool permission) to every pull, so the backend never has to reimplement them, and nothing a user could not see in Procore can reach their phone. The cost is one pull per user instead of one per project; with tens of users that is well inside Procore's limits, and the freshness window below keeps repeat opens cheap. (My call; the alternative, one shared mirror filled by a designated admin or service account, needs a credential the project does not have and still leaves per-user draft and tool-permission rules to us.)

- Table `user_records` (my call on names): `company_id`, `user_id` (our user), `project_id`, `kind` (text, starts with `rfi`), `procore_id`, `procore_updated_at`, `body` (jsonb, the full Procore record as returned), `deleted_at` (nullable), `change_seq` (bigint from one sequence, bumped on every insert, update and soft delete), `synced_at`. Unique on (`company_id`, `user_id`, `project_id`, `kind`, `procore_id`). Index on (`user_id`, `project_id`, `kind`, `change_seq`).
- Table `user_sync_state`: `company_id`, `user_id`, `project_id`, `kind`, `high_water` (the newest `updated_at` seen), `last_pull_at`, `last_full_sweep_at`, `last_error`.
- A user's rows live as long as their user row (they cascade with it) and are never read for another user. The phone clears its own copy on sign-out.
- **Delta pull** from Procore: list with `filters[updated_at]` from `high_water` minus a five-minute overlap (my call; clock skew and late commits) to now, fully paginated, upserted into `project_records`. A row whose body is unchanged keeps its `change_seq`.
- **Full sweep** for deletions, lost visibility and quiet changes: list every record the user can see for the kind (full bodies, fully paginated), upsert them, and soft-delete the user's mirror rows whose id is missing. That covers RFIs deleted in Procore, RFIs the user can no longer see (turned private, removed from distribution), and answers or attachments that did not move the RFI's `updated_at`. Runs on a manual full Sync and at most once every six hours otherwise (my call; a full list of two thousand RFIs is about twenty Procore pages).
- **Freshness window**: a pull triggered by a phone is skipped when `last_pull_at` is under 60 seconds old (my call); the phone still gets its delta from the mirror. A manual Sync forces the pull.
- **Own submits**: the submit worker is not changed. When the phone returns from the Result screen to Home it requests a forced delta pull (bypassing the freshness window); the delta's five-minute overlap catches the RFI Procore just created. (My call, so the idempotent submit job and its tests stay as they are.)
- **Keep-warm job**: a River periodic job every 15 minutes pulls deltas for each (user, project) synced in the last 24 hours (my call) using that user's refresh token, so the open-app sync is usually a delta of nothing. A user whose refresh fails is skipped until their next sign-in.
- **Pulls that fail** with 401 or 403 record `last_error` and leave the mirror as it was; the phone shows "Sync failed, showing what you have".

### Visibility

Procore decides. Every pull uses the requesting user's token, so private RFIs, draft RFIs and the RFI tool permission are enforced by Procore on the way in, and the full sweep removes from a user's mirror anything they have stopped being able to see. The backend adds only what it already does for lookups: the project-access probe and the per-kind probe (a user with no RFI tool access gets `unavailable`, not an empty list). No per-record permission logic lives in our code.

### Contract

- `GET /v1/projects/{id}/records/{kind}?since={change_seq}&limit=500` returns `{records: [RecordEnvelope], deleted_ids: [int64], next_since: int64, has_more: bool, synced_at: date-time, pull: {status: "fresh"|"pulled"|"failed", error?: string}}`. The first call sends `since=0`. `RecordEnvelope` is `{id, kind, updated_at, change_seq, body}` where `body` is the typed projection for the kind.
- `POST /v1/projects/{id}/sync` with `{kinds: ["rfi"], full: bool}` forces a pull (and a sweep when `full`) and returns the same `pull` status. The Sync button calls this, then pages the delta.
- `RFIRecord` (the projection, every field, named in the contract): id, number, full_number, prefix, subject, status, translated_status, draft, private, due_date, initiated_at, time_resolved, created_at, updated_at, created_by, assignee, assignees, ball_in_court, ball_in_courts, ball_in_court_role, rfi_manager, received_from, responsible_contractor, distribution_list, location, specification_section, cost_code, cost_impact, schedule_impact, drawing_number, drawing_ids, reference, revision, current_revision, has_revisions, questions (each: id, question_date, plain_text_body, rich_text_body, created_by, attachments (id, name, filename, url), answers (id, answer_date, plain_text_body, rich_text_body, created_by, attachments)), custom_fields (pass-through object), link. People and lookups are `{id, name}` objects.
- `GET /v1/projects/{id}/attachments/{attachment_id}` streams the attachment through the backend with the user's token (my call: Procore attachment URLs expire, so the phone never stores them).
- `GET /v1/projects/{id}/rfis` and `RFISummary` are removed. Breaking; ships with the app build.

### Phone

- SwiftData `CachedRecord` (my call on names): `key` (`"<projectID>/<kind>/<procoreID>"`, unique), `projectID`, `kind`, `procoreID`, `updatedAt`, `changeSeq`, `bodyData`, plus the columns the list sorts and searches on without decoding: `number`, `subject`, `status`, `ballInCourt`, `dueDate`. `ProjectSyncState` per project and kind: `nextSince`, `lastSyncedAt`, `lastError`.
- `RecordSync` (in `Core/Records`, my call) runs for the selected project: on scene active, on project selection, on pull-to-refresh and on the Sync button. It pages the delta until `has_more` is false, applies inserts, updates and deletes in one transaction per page, then updates `nextSince`. A cursor of 0 after sign-in or a cleared project means a full load. Sync is cancelled on sign-out and project change.
- **Sync button and status** live in the project header on Home: "Synced 2 min ago", a spinner while syncing, "Sync failed, showing what you have" with Retry. The RFI list has pull-to-refresh.
- **RFI list**: local, instant, sorted by number descending, searchable by number and subject, filterable by status (Draft, Open, Closed) and "Ball in my court" (my call on the first filters). Rows: number, subject, status tag, ball in court, due date.
- **RFI detail**: every field in the projection, questions with answers in order, attachments as rows that open through the backend when there is signal (greyed with "Needs signal" otherwise), "Open in Procore" using `link`.
- **Sign-out**: records and sync state are cleared, like lookups. Drafts are untouched, as today.
- **Storage**: the body is stored as JSON data; a project with two thousand RFIs at a few kilobytes each is a few megabytes. No pruning in the first cut.

### Generality

Adding a kind means: a Procore fetcher (`list since`, `list ids`) and a projection type on the backend, one `kind` value in the contract, a projection decode on the phone, and list and detail screens. Sync, storage, cursors, visibility hooks and the Sync button are shared. Observation and punch item are the next kinds (roadmap features 2 and 3).

## What success looks like

- Airplane mode, open the app: the RFI list and any RFI's detail render fully.
- Create an RFI in Procore web, tap Sync on the phone: it appears within a few seconds. Delete it in Procore, tap Sync: it is gone.
- Submit an RFI from the phone: it is in the list after the result screen's return to Home.
- One phone opening the app ten times in a minute causes one Procore pull.
- A private RFI never reaches a phone whose user is not on it, and an RFI that turns private disappears from that phone after the next full sweep (test with the sandbox's Test Architect and API Support accounts).

## Decided by Drew (2026-10-25)

1. Attachments are not stored offline for now; metadata syncs and files open through the backend with signal.
2. A mirror per user is fine; no shared mirror and no designated Procore admin account.

## Left to the builder

- Procore serializer view and page size for the id sweep; whether the sandbox exposes recycled RFIs another way.
- Whether adding an answer or an attachment to a question bumps the RFI's `updated_at` in Procore. The six-hourly full sweep refreshes every body regardless, so the "appears after the next sync" promise holds for answers at least at that cadence; if the sandbox shows answers do bump `updated_at`, they arrive with the next delta.
- Exact SwiftData indexes and whether `@Query` or a fetched snapshot drives the list.
- How the delta endpoint pages (500 per page is a starting point).
- Status tag colours and the detail layout: the design handoff (`docs/design/2026-10-23-ios-handoff/`) has no RFI list or detail screen; the design pass (roadmap 2b) must add them, and the first cut uses the handoff's tokens with plain rows.

## Later

- Procore webhooks for RFIs on the dev and prod companies, delivering to the backend to trigger a delta pull within seconds; the reconciliation sync above is already the fallback Procore asks for.
- Multi-project warm sync (the last few projects, not only the selected one).
- Offline attachments, if answer 2 above says yes.
- Observations and punch items as further kinds.
