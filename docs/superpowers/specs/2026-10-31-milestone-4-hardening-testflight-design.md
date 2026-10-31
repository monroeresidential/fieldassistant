# Milestone 4: a real build in four real hands

Date: 2026-10-31. Status: draft for Drew's review. Written from a short question round with Drew on 2026-10-31 plus the roadmap and spec; every choice that is not his is marked "(my call)".

## Intent

Milestones 1 to 3 built the loop: dictate, extract, review, submit, with photos and without signal. It has only ever run against the Procore sandbox, from Drew's phone, with a dev backend. Milestone 4 turns that into a pilot: four named people on one real project, against the real Monroe Residential company in Procore, for two weeks of daily use, with Drew able to see each week what happened. The roadmap's exit criterion for the pilot that follows is "no duplicate submissions, under 20% of drafts needing more than two corrections"; this milestone has to make that measurable and give the app the manners it needs in front of people who are not its author.

## Decided by Drew (2026-10-31)

- The pilot is Drew, Mike Obloy, Chris Lance and Owen Burg, on 118 W Illinois, recording real RFIs into the real Procore company from day one. No sandbox.
- Everyone has at least Standard on the project's RFI tool; Drew sets that up in Procore.
- English only for the pilot. Spanish dictation stays open for later.
- Refresh tokens get a short grace window rather than strict revocation.
- Weekly pilot review is a summary I pull from the database and bring to Drew; nothing in the app, no export endpoint.
- No production Procore app exists yet; creating it is the first step and Drew does it with me driving.
- Mike, Chris and Owen become RFI Managers on the project, so RFIs they submit can open directly and the app's existing rule (a person's first five RFIs land as Procore drafts, the rest open) holds for everyone. Nobody is left on Standard-only permissions in the pilot.
- Apple's Beta App Review gets a restricted test user and a test project in the real company, so the reviewer can sign in and complete a submit without touching 118 W Illinois.

## Goals

- A Prod build of the app, talking to `api.fieldassistant.tools`, installed on the four phones through TestFlight, signed in to the real company, with the RFI tool available on 118 W Illinois.
- When Procore refuses something, the person sees Procore's reason in plain words on the screen where it matters: no access before recording, not after; a required field Procore insists on flagged on Review, not a failed submit.
- A dead session costs one sign-in and nothing else: queued and waiting drafts resume afterwards, and a flaky connection during a token refresh does not sign anyone out.
- The hardening items parked since milestone 1 that a pilot user would hit are closed; the ones they would not are left.
- Each Monday of the pilot, a one-page summary of the week: drafts, submits, failures, corrections per draft, duplicates (expected zero), extraction times, photos pushed, and the transcripts that needed the most fixing, ready to seed the eval set.

## Non-goals

- New features (observations, punch items, web) and Spanish dictation.
- An admin or export HTTP endpoint (v1 decision stands); the weekly summary is a command run on the droplet.
- Crash-reporting SDKs. TestFlight and Xcode Organizer crash logs are enough for four users (my call).
- Changing the extraction prompt or model. Misses found in the pilot become eval cases first; tuning is a milestone of its own.
- App Store release. TestFlight external testing is the distribution for the pilot.

## Anti-goals

- Two RFIs from one tap. Nothing here touches the submit path's guarantees.
- A sign-out that loses field work, or a sign-out caused by a bad connection rather than by Procore.
- A silent refusal: every Procore "no" reaches the screen with Procore's words.
- A prod deploy that nobody approved, or a dev change that reaches prod by accident. `main` deploys prod only through the `production` environment's approval, as today.
- A pilot week that cannot be reconstructed: every number in the summary comes from audit rows that already exist.

## Constraints

- v1 decisions hold: one company, tens of users, slog only, dev and prod stacks on one droplet, Postgres in Compose with nightly dumps, no admin export endpoint, no cert pinning.
- Prod deploys go `develop → main` by pull request, pause for Drew's approval, and need `.env.prod` complete on the droplet (it already has the object store; it still needs the production Procore client id and secret). The prod stack is stopped today.
- Procore: a production OAuth app is created in the Procore developer portal under Drew's login; its redirect URL is `https://api.fieldassistant.tools/v1/auth/callback` (already in `.env.prod.example`); a Company Admin installs it in the company (the deploy README's "Connecting the app to a Procore company" section). Production API base `https://api.procore.com`, login `https://login.procore.com`.
- TestFlight: Fastlane `beta_dev` and `beta_prod` exist and need `ASC_KEY_ID`, `ASC_ISSUER_ID` and `ASC_KEY_CONTENT`; GitHub has no App Store Connect secrets today, and Drew's Mac has the `asc` CLI signed in. Bundle ids `tools.fieldassistant.app` (Prod) and `.dev` (Dev); Team `PR6KG6PZJN`. A build for people outside the App Store Connect team goes through an external TestFlight group and one Beta App Review.
- Contract changes bump `info.version` and regenerate both clients in one commit.
- The phone already keeps its session on any refresh failure other than an outright rejection; the backend's `Sessions.Refresh` rotates the refresh token on every use and treats reuse of a rotated-away token as theft, revoking the whole session. That is the strict behaviour Drew chose against.

## How it works

### 1. Production Procore app and the prod stack

Order of operations, done with Drew at the keyboard for the portal steps:

1. In the Procore developer portal, create production credentials for the Field Assistant app (the sandbox app already exists): redirect URI `https://api.fieldassistant.tools/v1/auth/callback`, OAuth type confidential (the backend holds the secret), the same scopes the sandbox app uses.
2. A Company Admin installs the app in the Monroe Residential company (App Management → custom install), per the README. Until this happens every project call answers 403 "App is not connected to this company", which the app shows as No access.
3. Add `PROCORE_CLIENT_ID` and `PROCORE_CLIENT_SECRET` to `.env.prod` on the droplet (Drew types them; I verify presence and length only, as with the Spaces key).
4. Open the promotion PR `develop → main`, approve the `production` deploy, confirm `https://api.fieldassistant.tools/healthz`, and sign in once from the Dev build pointed at prod or from the first Prod TestFlight build.
5. Bring `develop` up to date with `main` afterwards (repo convention).

Nothing in the code changes for this; it is a runbook task with a checklist in the plan and a new README subsection "Production Procore app" where the current one covers only the company install.

### 2. TestFlight to four phones

- App Store Connect gets two app records if they do not exist yet, one per bundle id. The Prod app is what the pilot runs; the Dev app stays Drew's.
- Fastlane runs from Drew's Mac for the pilot (my call: the three `ASC_*` values live in his shell, not in GitHub, until there is a reason to build in CI; the `.p8` key never enters the repo). `beta_prod` uploads a build with a changelog line per build.
- An external TestFlight group "118 W Illinois" with the four testers' Apple IDs; the first build goes through Beta App Review (usually under a day), later builds do not. The review submission carries the test user's Procore login and the test project's name (both set up by Drew in the real company before the first upload; the test user has the same RFI Manager permission on the test project only), with notes telling the reviewer to dictate an RFI and submit it there. Testers install TestFlight, accept the invite, install the app, sign in with Procore.
- Build numbers are new work: nothing sets `CURRENT_PROJECT_VERSION` today, so the Fastlane lanes set the build number from a UTC date stamp (`yyyyMMddHHmm`) before archiving, which keeps every upload distinct. The Settings footer already shows version, build, environment and host, which is what a tester reads out when something is wrong.

### 3. Permission errors (spec acceptance: "a user with Read-only on the RFI tool is told so before recording, not after")

- Backend: `GET /projects/{projectId}/features` (contract 1.7.0) returns, for the signed-in user on that project, each feature with `available: true | false` and, when false, Procore's reason. For `rfi` the probe must tell Read-only from Standard, and listing RFIs does not (Read-only can list and view). The probe is therefore a project-level call that Read-only cannot make and Standard can; which call that is gets settled empirically against a Read-only login on the sandbox project before the endpoint is built (Drew creates the login; the plan's first backend task is that experiment, and `potential_rfi_managers` is the first candidate). If no read-only-safe call distinguishes the two, the endpoint reports `available` from the last create outcome the backend saw for that user and project (a 403 on create marks the feature unavailable until Sync), and the screen says so honestly. This is a new probe beside the lookup probes, not a reuse of `ProbeKind`, which only knows lookup kinds; it shares the lookup cache's lifetime and is refreshed on the project's Sync.
- Backend: every `procore_permission_denied` response carries Procore's own message in `message` (today a generic text), so the phone can show it.
- Phone: Home's feature grid shows RFI as "Selected" only when available, otherwise a greyed card with Procore's reason beneath, and the record button is disabled with the same reason as its accessibility hint. The "No access" screen (project level) shows Procore's reason instead of the generic admin copy. Existing strings stay where no reason is available.
- Backend: Procore's `errors` map is parsed on HTTP 400 as well as 422, so a 400 "rfi_manager_id invalid" flags the RFI manager row instead of failing the draft with a `_form` message; and RFI manager candidates come from the project's `potential_rfi_managers` where Procore exposes it, falling back to the users lookup (both items parked since milestone 2).

### 4. Token revocation and the grace window (spec: "a failed refresh marks the user reauth_required; the app shows one sign-in prompt and resumes queued work after")

- Backend: the sessions table already keeps the previous refresh token's hash (`prev_refresh_hash`, rotated on every use); what is new is the rotation time and, because the server stores only hashes, an encrypted copy of the current refresh token (the vault's AES key, as for Procore tokens) so a grace replay can hand back the same pair. Rule: a refresh presenting the previous token within the grace window returns the current pair, unchanged, without revoking; a lost compare-and-swap race between two simultaneous refreshes from the same phone is treated the same way instead of deleting the session; after the window, or for any older token, it is theft as today. Grace window `120 s` (my call: long enough for a basement retry, short enough that a stolen token is useless by the time anyone notices). Reissue behaves the same.
- Backend: sign-out already revokes the Procore refresh token best effort before deleting the session; this milestone adds the test that pins it, nothing more.
- Phone: `AuthRefreshError.rejected` is the only sign-out trigger today, and a rejected refresh during a queued run keeps the draft queued. A Procore-side revocation, though, never reaches the phone as a rejected refresh: it arrives as `reauth_required` on an ordinary call, which today shows an in-place "Session expired" message wherever it happens (Home, picker, Capture, sync). This milestone unifies the two: `reauth_required` ends the session the same way a rejected refresh does and lands on the sign-in screen with the one prompt "Procore signed you out. Sign in again and your drafts will send." (my call); the in-place messages stay only for permission and transport failures. Signing in resumes waiting and queued drafts (already true); a test covers both kinds after re-sign-in.

### 5. Configurable validations (spec acceptance: "Procore's configurable required fields trigger a field-level error on the review screen")

Already built in milestone 2: the submit posts with `run_configurable_validations=true` and maps 422 field errors onto Review rows. What remains: a 422 on a field Review has no row for (a project custom field, or `question.attachments`) is shown today as the generic `_form` message; it becomes a named note "Procore requires <field label>" with Procore's text, and the first run against the real project's required-field configuration is a device-check item, since the sandbox project has none.

### 6. Hardening items from the roadmap

Closed in this milestone (a pilot user would hit them):

- Project list cached per company for the session; refreshed on pull-to-refresh or company change (today three fetches in ten seconds while switching projects).
- SwiftData `VersionedSchema` for the local store and a non-fatal open: a store that cannot open shows a message and offers to reset local data instead of crashing at launch. Every TestFlight build from here on changes the schema; this is the gate for shipping them.
- A save refused as locked while the server says `failed` must not replace unsaved edits, and the same tap must not submit; stop after the refresh instead.
- A rolled-back draft insert leaves `LocalPhoto` instances behind in `CaptureModel`; clean them up.
- Taking a photo while recording stops the microphone (the camera cover triggers `onDisappear`); keep the recording running across the cover.
- The sign-in screen's "Last used" company goes stale after a same-session project change.

Left where they are (nobody in the pilot would notice): decoded-rows cache in `CachedLookup`, the N+1 attachment queries on the drafts list, the design-pass polish list, the backend's deferred minors.

### 7. The weekly pilot summary

- A Go command `cmd/pilotreport` run on the droplet against the prod database, read-only, printing Markdown for one week (`--from`, `--to`, default the last seven days): drafts created, built, submitted, failed by error code; corrections per draft; duplicates; extraction p50/p95 from `extract_ms`; photos pushed and photo failures by code; per user, with names from `users`; and the five drafts with the most corrections, each with transcript, extracted payload and final payload side by side, formatted so a reviewer can turn one into an eval case by hand.
- A correction (my call, and the number the pilot's exit criterion is judged on) is a field whose value in the final submitted payload differs from the extraction's value in `drafts.llm_output`, including a field the extraction left empty and the user filled; it is counted per field, not per save, so system-written `edited` events (revalidation, a photo flag cleared, a queued re-save) and repeated saves of the same field do not inflate it. The `edited` audit events stay the evidence trail for how a value got there.
- Duplicates cannot be found by two drafts sharing a `procore_record_id` (a unique index makes that impossible). The report looks for what a duplicate would actually leave behind: more than one `submitted` audit event for a draft, any `attempt` marker whose outcome is still unknown at week's end, and any RFI in the project mirror whose subject and creator match a submitted draft but whose id is not that draft's `procore_record_id`. Expected: none.
- The API image is distroless and holds only `/api`; the Dockerfile also builds `cmd/pilotreport` into the image as `/pilotreport`. `make pilot-report` wraps `docker compose exec api-prod /pilotreport ...` in the deploy README, and I run it each Monday and bring the output to Drew. The command adds no tables.
- An `evals/pilot/` folder in the RFI feature module receives cases seeded from the summary, in the existing eval-set format, reviewed by Drew before they count.

## What success looks like

- Drew, Mike, Chris and Owen each have the Prod app from TestFlight, signed in to the real company, with RFI available on 118 W Illinois, and each has submitted one real RFI with a photo that appeared in Procore once.
- A Read-only user on a test project sees the reason on Home before recording; a required field configured on the real project flags on Review; a revoked Procore token produces one sign-in prompt and the queued draft then sends.
- Pulling the phone's network during a token refresh does not sign anyone out; presenting the previous refresh token twice outside the window does revoke (Go tests on `Sessions`).
- The first weekly summary runs against prod and prints every section with real rows.
- Go and iOS suites green; the device check for this milestone passes on the four phones, not only Drew's.

## My calls (Drew can overrule any of these)

1. No crash-reporting SDK; TestFlight and Organizer logs suffice for four users.
2. TestFlight uploads run from Drew's Mac with the App Store Connect key in his shell, not from CI, for now.
3. External TestFlight group with one Beta App Review, rather than adding the superintendents to the App Store Connect team.
4. The refresh-token grace window is 120 seconds and applies to the immediately previous token only; the server keeps an encrypted copy of the current refresh token so a replay gets the same pair back, and a lost refresh race is treated as a replay, not theft.
5. The features probe is a new project-level probe settled empirically against a Read-only sandbox login, sharing the lookup cache's lifetime; if nothing distinguishes Read-only from Standard, availability follows the last create outcome.
6. The weekly summary is a Go command built into the API image, not a script, so it reuses the store's queries and types.
7. Hardening scope is split as listed: the six items a pilot user would hit are in; the rest stay deferred.
8. `reauth_required` from any call ends the session and lands on the sign-in screen with one prompt, "Procore signed you out. Sign in again and your drafts will send."
9. A correction is a field whose final value differs from the extraction's, including filled-in empties, counted per field from `llm_output` against the final payload.
10. Build numbers are a UTC date stamp set by the Fastlane lanes.

## Left to the builder

- The exact Procore developer-portal screens (they change); the runbook records what was clicked.
- How `potential_rfi_managers` is fetched (per project endpoint vs. per RFI form) and its cache key.
- The `VersionedSchema` migration plan's shape for the existing stores on Drew's phones.
- The grace-window columns on the sessions table and the migration number.
- The Markdown layout of the summary and how the payload diff names fields.

## Later

- Spanish dictation; web app (phase 2); prompt tuning from the seeded eval cases; App Store release; CI-driven TestFlight uploads once the key lives in GitHub secrets.
