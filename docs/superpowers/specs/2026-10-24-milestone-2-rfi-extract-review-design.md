# Milestone 2 design: RFI extract and review

Oct 24, 2026. Amends nothing; builds on `procore-field-assistant-spec.md` (Feature framework, Feature 1, Backend spec, LLM design) as amended by `docs/superpowers/specs/2026-10-22-v1-decisions.md`. Where this document is more specific than the spec, this document wins for milestone 2.

## Intent

A superintendent standing in front of a problem dictates an RFI into the phone, takes a couple of photos, reviews a prefilled draft, fixes what the app flagged, and taps submit. The RFI appears in Procore assigned to the right person. The worked example that shaped this design: at 401 W Ontario, a twisted wood column below the new air handler is temporarily held by a steel plate and threaded bolts; the superintendent wants Ben West, the architect, to review it and decide whether to add support or upgrade the plate.

Milestone 1 proved sign-in, scope selection and reading RFIs against the live dev environment. Milestone 2 adds the first write path and the whole Capture → Extract → Review → Submit loop that every later feature reuses.

**Exit criterion (from the roadmap):** a dictated RFI becomes a reviewable draft with resolved IDs and posts to the Procore sandbox without attachments.

## Goals

- Dictate on the phone with a live on-device transcript; no audio leaves the device.
- One extraction call turns the transcript into an RFI draft whose every ID comes from the project's lookup data, with ambiguous fields flagged and candidates offered.
- Review on the phone: fix flagged fields with pickers, edit any field, see the original transcript, submit.
- Submit posts to Procore exactly once per draft, with Procore's configurable validations on, and shows the resulting RFI number and link.
- Every draft leaves an audit trail: transcript, model output, user edits, final payload, Procore request and response, timings, model version.
- The pipeline (lookups, drafts, review, submit, audit) is feature-agnostic so Observation in milestone 7 adds only a feature package and a review screen.
- A ten-case eval set, starting with the 401 W Ontario column, gates prompt and model changes.

## Non-goals (deferred, not rejected)

- Photo upload to Procore (milestone 3). Photos are captured and kept on the draft; the RFI posts without them.
- Background submit queue with retry while the app is closed (milestone 3). In milestone 2, submit needs connectivity at tap time; a failed submit stays a draft and can be retried, idempotently.
- Permission probing before recording (`/projects/{id}/features`, milestone 4). A user without RFI create permission finds out at submit, as a clear error.
- The visual design pass. The handoff in `docs/design/2026-10-23-ios-handoff/` is final design, implemented as its own pass after milestone 2 (Drew's decision, 2026-10-24). Milestone 2 screens use plain native SwiftUI with the handoff's structure, copy and states, not its styling.
- Spanish dictation. Speech recognition is English only; the prompt is written language-neutral so Spanish is a locale switch later.
- Web client, photo captions by vision model, "dictate and fire".

## Anti-goals (failure even if it works)

- An ID in the submitted payload that did not come from a lookup result. The backend demotes any such ID to unresolved regardless of the model.
- Anything posted to Procore without the user tapping Submit on the review screen.
- Two Procore RFIs from one draft, under any retry or network failure.
- A due date, cost value or impact status the user never said. Empty beats invented.
- Rewriting the user's question into a different question. The body may be cleaned up, never changed in meaning; the original transcript is always kept and shown.

## Constraints (from CLAUDE.md and the decisions doc)

- Only the backend talks to Procore or Claude. iOS holds a session JWT only.
- `company_id` on every new table; `Procore-Company-Id` on every Procore request.
- Model IDs in config, recorded per draft. Prompts are versioned files.
- Error bodies use the fixed code set; `not_found` becomes live in this milestone for drafts and jobs.
- Contract changes bump `info.version`, regenerate both clients and sync in the same commit. Generated code is committed.
- Migration `000002` is new; `000001` is now deployed and frozen.
- No OpenTelemetry or Prometheus; slog only.
- Dev environment (`api-dev.fieldassistant.tools`, sandbox Procore) is live and deploys from `develop`; the iOS Dev build signs in against it. Integration verification happens there.

## Decided: backend

### Packages

| Package | Role |
| --- | --- |
| `internal/llm` | Claude client wrapper (official Go SDK), tool-use loop, JSON-schema validation of the final output, prompt registry that loads `prompt.md` files and reports their version (content hash). No feature knowledge. |
| `internal/pipeline` | Lookup cache (fetch, store, serve with ETag), draft lifecycle (create, extract, save edits, read), ID validation against the cache, audit writes, submit orchestration via the job queue. Feature-agnostic; features register with it. |
| `internal/features/rfi` | `schema.go` (draft payload struct and JSON schema), `prompt.md`, `extract.go` (tool set binding and post-processing), `submit.go` (Procore POST, 422 mapping), `evals/` (ten cases). |
| `internal/jobs` | River setup and the submit worker. Workers run in the API binary. |
| `internal/procore` | Grows typed list calls for the six lookup kinds, plus `CreateRFI`. |

A feature is a Go value implementing one interface: name, lookup kinds it uses, prompt file, output schema, extract post-processing, submit. The pipeline never imports a feature package; `cmd/api` wires them.

### Lookups

Six kinds: `users`, `locations`, `spec_sections`, `cost_codes`, `vendors`, `drawings`. Each row is `{id, label, extra}` where `extra` carries what the model and the pickers need to disambiguate (users: role and company; locations: full path; spec sections: number; drawings: number and discipline).

- Fetched from Procore per project with full pagination, stored in `lookup_cache` as one row per `(company_id, project_id, kind)` with a body, an ETag (hash of the body) and `fetched_at`.
- Server cache lifetime ten minutes. A stale row is refreshed on read; a Procore failure on refresh serves the stale copy with a log line rather than an error.
- `GET /projects/{id}/lookups/{kind}` honours `If-None-Match` with a 304.
- A draft extraction reads from the cache only, never from Procore directly, so tool calls stay sub-100 ms. The first extraction on a cold project warms all six kinds first; the app also prefetches on project select, so this is rare.
- A kind Procore refuses (403 because the tool is off on the project or the user lacks it) is cached as empty with `unavailable: true` and Procore's reason, for the same ten minutes. The lookup response carries that marker so the picker can say "not available on this project" instead of showing an empty list. Extraction proceeds without that kind's tool; the matching payload field stays empty and is not flagged, except `drawing_number`, which accepts a spoken literal. Extraction never fails because a lookup kind is missing. (My call.)

### Extraction

One Claude call per draft using the Sonnet-class model named in config (`LLM_EXTRACT_MODEL`), with:

- System prompt: `features/rfi/prompt.md`, with the house style rules from the spec's LLM design section.
- Project context: project name, timezone, today's date in that timezone, the user's name (the dictating user is the default RFI manager).
- The transcript.
- Six read-only tools, one per lookup kind, each taking an optional `query` and returning up to 25 matching rows from the cache. Matching is case-insensitive substring over label and `extra`, with the full list returned when `query` is empty and the kind has at most 25 rows. The builder may improve matching (token overlap, initials) if the eval set shows the need.
- A required final tool, `submit_draft`, whose input schema is the RFI draft payload plus `unresolved[]` and `confidence{}`. Using a tool for the final answer gives schema-enforced output without a second parse.

Budget: under 8 seconds and at most 6 lookup tool calls; the loop stops at the cap and whatever is still unresolved is flagged.

### Validation after extraction (the rule that matters most)

For every ID field in the model's output, the pipeline checks the ID exists in the cached lookup of the right kind for that project. Any ID that does not is removed from the payload and the field is added to `unresolved` with reason `not_in_lookup` and no candidates. Any `unresolved` candidate ID that is not in the cache is dropped. This runs regardless of what the model claimed. `confidence` values are passed through but never used for validation.

Date fields are checked to be real dates not before today in project timezone; a bad one becomes unresolved with reason `date_unparseable`.

### Draft payload (RFI)

The payload mirrors the Procore RFI create body, in the backend's own names, with the spec's field mapping. Fields: `subject`, `question_body`, `assignee_ids`, `rfi_manager_id`, `distribution_ids`, `responsible_contractor_id`, `location_id`, `specification_section_id`, `drawing_number`, `cost_code_id`, `due_date`, `schedule_impact {status, value}`, `cost_impact {status, value}`, `private`, `draft`.

`question_body` is the cleaned question in the speaker's words, followed by a blank line and `Field notes:` with the verbatim transcript. The app shows the transcript separately too; the duplication in Procore is intentional so the architect sees the original wording.

Defaults the model does not set: `rfi_manager_id` = the dictating user's Procore user ID when they are in the project directory, else unresolved; `private` = false; `schedule_impact` and `cost_impact` are omitted entirely when the user said nothing about them (no `tbd` default; the review screen shows them as "Not stated" and the user can set one).

`draft` (submit to Procore as a draft RFI): the backend sets the default at extraction as true while the user has fewer than five `submitted` drafts, counted from the `drafts` table, then false. The phone holds a "Submit as Procore draft" setting in its own storage (per phone, which is per user for this pilot); once the user touches it, the phone overwrites `draft` in every payload it saves. No column on `users` and no settings endpoint in this milestone. (My call; the spec's "new users default to true", made concrete.)

### Draft lifecycle and storage

Table `drafts`: `id` (client UUID), `company_id`, `project_id`, `user_id`, `feature`, `status`, `transcript`, `llm_output jsonb`, `user_payload jsonb`, `unresolved jsonb`, `confidence jsonb`, `model_version`, `prompt_version`, `procore_record_id`, `procore_record_number`, `procore_url`, `extract_ms`, `created_at`, `updated_at`.

Status values: `extracted` (model output stored, awaiting review), `reviewed` (user edits saved), `submitting`, `submitted`, `failed`. There is no server-side "waiting" status; a draft the phone could not send for extraction exists only on the phone.

Table `audit_events`: `id`, `company_id`, `draft_id`, `kind` (`extracted`, `edited`, `submitted`, `failed`), `payload jsonb`, `procore_request jsonb`, `procore_response jsonb`, `duration_ms`, `created_at`. `extracted` stores the full model conversation (tool calls and results included), `edited` stores the diff between `llm_output` and `user_payload`, `submitted` and `failed` store the exact Procore request body and response.

Table `lookup_cache` as above. River creates its own tables via its migration.

### Submit

`POST /drafts/{id}/submit` with `Idempotency-Key: <draft id>`:

1. If the draft is already `submitted`, return 200 with the stored result; nothing is posted again.
2. If `submitting`, return 202 with the existing job ID.
3. Otherwise validate the user payload once more (same ID validation as after extraction; any unresolved field is a 400 with the field names), mark `submitting`, enqueue the River job with the draft ID as its unique key, return 202 with the job ID.

The submit worker posts `POST /rest/v1.0/projects/{id}/rfis?run_configurable_validations=true` with the company header, using the user's Procore token (refreshed if needed; a revoked token fails the job with `reauth_required`). On 201 it stores the RFI ID, number and web URL, marks `submitted`, writes the audit row. On 422 it parses Procore's field errors, maps them back to draft field names, stores them on the draft as `unresolved` with reason `procore_validation` and the server message, marks the draft `failed`, writes the audit row. On any other failure after a successful POST was possibly received (timeout after send), the worker lists the project's RFIs filtered by subject and created time before retrying, so a lost response cannot cause a duplicate. The job retries transient failures three times with backoff; a final failure marks the draft `failed` with a reason the app can show.

`GET /jobs/{id}` returns `{status, draft_id, result?, error?}`. The app polls every two seconds for up to sixty seconds, then falls back to `GET /drafts/{id}`.

### Contract additions (version 1.4.0)

| Method and path | Purpose |
| --- | --- |
| `GET /projects/{id}/lookups/{kind}` | Lookup rows with ETag; 304 on match |
| `POST /drafts` | `{id, feature, project_id, transcript, captured_at, location?}` → `DraftResponse`. `id` is the client UUID; a repeat with the same ID returns the existing draft rather than extracting twice. |
| `GET /drafts/{id}` | Current state including unresolved and result |
| `PATCH /drafts/{id}` | Save the user's payload; records the `edited` audit event |
| `POST /drafts/{id}/submit` | As above |
| `GET /jobs/{id}` | Submit status |
| `GET /drafts?project_id=` | Recent drafts for the home list, newest first, limit 50 |

`DraftResponse` carries `{id, feature, status, payload, unresolved[], confidence{}, transcript, model_version, result?}` where `unresolved[]` items are `{field, reason, message?, candidates[]: {id, label, extra}}`. The payload schema is the RFI draft payload; later features add their own payload schema under a `oneOf` keyed by `feature`.

All draft and job routes require the session and scope the draft to the caller's user ID; another user's draft is `not_found`.

### Configuration

New: `ANTHROPIC_API_KEY`, `LLM_EXTRACT_MODEL` (no default; the deploy env files name the model explicitly), `LLM_TIMEOUT_SECONDS` (default 20), `PROCORE_WEB_URL` (`https://sandbox.procore.com` in dev and local, `https://app.procore.com` in prod) for the "Open in Procore" link. `.env.example` and the deploy examples gain all four.

Sequencing: the API refuses to start without the required new variables, so before the config change merges to `develop`, `.env.dev` on the droplet must gain `ANTHROPIC_API_KEY`, `LLM_EXTRACT_MODEL` and `PROCORE_WEB_URL`, and the repository needs an `ANTHROPIC_API_KEY` secret for the eval job. The plan puts these as Drew steps ahead of that merge.

Other builder defaults I accept: `POST /drafts` runs extraction synchronously with in-process single-flight on the draft ID; draft and lookup routes take the company from the existing `X-Company-Id` header; the project timezone comes from Procore's project record (a Rails zone name mapped to IANA); "Refresh people and locations" sends `Cache-Control: no-cache` to bypass the server cache; `procore_record_number` is nullable since Procore may leave draft RFIs unnumbered.

### Eval set

`internal/features/rfi/evals/` holds ten cases, each a transcript, a lookup fixture (a small fake project directory, locations, spec sections, drawings) and the expected payload and unresolved fields. Case one is the 401 W Ontario column: expected assignee resolves to the single Ben West, location resolves to the lookup row for the air handler's location if the fixture has one, subject mentions the column, no due date, no cost value, schedule and cost impact absent. Other cases cover: two people with the same first name, a relative due date, a spoken drawing number, a cost code never mentioned, a spec division by name, a location not in the tree, an assignee not in the directory, a stated schedule impact, and a transcript with filler and restarts.

The eval runs as a Go test tagged `evals` that calls the real model and is skipped without the API key. It reports field exact-match, ID hallucination count (must be zero after validation), unresolved precision and p95 latency. CI runs it on changes to `prompt.md`, `extract.go` or the model config.

## Decided: iPhone

### Modules

| Module | Role |
| --- | --- |
| `Core/Capture` | Audio session, `SFSpeechRecognizer` with on-device recognition required, live partial results, photo capture via camera and library, one `CoreLocation` fix per capture. Produces a local draft record. |
| `Core/Drafts` | SwiftData models for drafts, their photos and their status on the phone; the list the home screen shows. |
| `Core/Lookups` | Fetch and cache the six lookup kinds per project with the ETag; prefetch on project select; serve pickers offline from the last copy. |
| `Core/Review` | Generic review form pieces: labelled row, unresolved row with candidate radios and "someone else" picker, paired row, transcript disclosure, sticky footer. Bound to a feature's field list. |
| `Features/RFI` | RFI capture config (voice, photos, location), the RFI review screen built from the review pieces, result screen. The existing RFI list stays. |

### Screens and flow (handoff structure, native styling)

Home gains a record button and a recent-drafts list with status chips (Draft, Submitted, Failed, Waiting). Record pushes Capture, which starts recording immediately: live transcript, photo strip, stop button, then "Build RFI". Build posts the transcript; with no network the draft is saved as Waiting and the user returns home with a banner. Review shows the fields in the handoff's order with flagged rows first-class: reason text, candidates, picker. Save draft stores edits locally and on the server. Submit shows progress in the footer, then Result with the RFI number, "Open in Procore" and "New RFI". A 422 returns to Review with the rows flagged and Procore's message as the reason.

Every payload field is reviewable. The handoff's rows come first (subject, question, assignee, RFI manager, location, spec section, due and drawing, schedule and cost impact, photos, field notes). The fields the handoff omits (distribution, responsible contractor, cost code, private, submit as draft) follow in a "More" group that is expanded when any of them is set or flagged and collapsed otherwise. Nothing the model filled can reach Procore without a row the user could see. (My call.)

In Local and Dev builds only, the Capture screen has a "Type instead" control that accepts a pasted transcript. It exists so the Simulator, UI tests and anyone without a microphone can exercise Build RFI → Review → Submit; Prod builds do not compile it in. (My call.)

Phone-side draft statuses: `waiting` (recorded, not yet extracted), `draft` (extracted, under review), `submitting`, `submitted`, `failed`. A Waiting draft is sent for extraction when the user opens it with connectivity, or automatically when the app comes to the foreground with connectivity.

Audio: kept on the phone until the draft is submitted, then deleted. No upload in any milestone.

### Settings

Adds "Submit as Procore draft" (overrides the first-five default once touched) and "Refresh people and locations" (refetches lookups). Photo size and keep-audio toggles wait for milestone 3.

## Decided: verification and Drew's steps

- Dictation is verified on a physical iPhone running the Dev configuration, installed from Xcode. Drew adds the Apple Team ID to `ios/project.yml` (the commented placeholder) before integration testing; the bundle identifier stays the placeholder for this milestone. The Simulator path uses the "Type instead" control.
- Extraction uses a key from Drew's Anthropic Console account. Drew creates a key for this project and places it in `.env.dev` on the droplet and in the repository's `ANTHROPIC_API_KEY` secret, before the config change merges to `develop`.
- Milestone 2 exit is demonstrated on the dev environment: the 401 W Ontario column RFI dictated on the phone, reviewed, and visible in the sandbox project's RFI list.

## Decided: project housekeeping

- The design handoff zip moves to `docs/design/2026-10-23-ios-handoff/` (unzipped, committed; the zip is deleted). The roadmap gains a "design pass" line after milestone 2, scheduled by Drew.
- Sandbox data: for evals and demo, the sandbox project used for verification should have a few directory entries and locations that resemble a real job (an architect named Ben West, a second person sharing a first name with someone else, a location tree with a mechanical room). Drew sets this up in the sandbox; the plan includes a step to confirm it before integration testing.
- The milestone 1 follow-up to cache the project list is not part of this milestone.

## Left to the builder

- Exact prompt wording, within the house style rules; the eval set is the acceptance test.
- Fuzzy matching details inside the lookup tools.
- The precise JSON schema for `submit_draft` and how `confidence` is represented.
- 422 field-name mapping table from Procore error keys to draft fields.
- SwiftData schema details, photo storage location on the phone, image size limits before milestone 3's downscaling.
- Polling and reachability implementation on iOS.
- Whether River runs in the same process with its own connection pool or shares the pgx pool (same binary either way).
- Test strategy per package, following the existing patterns (fake Procore server, `storetest`, generated client in Swift tests).
