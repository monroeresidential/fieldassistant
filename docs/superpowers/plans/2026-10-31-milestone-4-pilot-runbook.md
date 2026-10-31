# Milestone 4 Pilot Runbook

A checklist, not an SDD plan: these steps happen in portals, on the droplet and on phones. Owner per step: **Drew** (his logins) or **Claude** (driven from the session, with Drew watching where a secret is typed). Record outcomes in the roadmap's milestone 4 row as they land.

**Spec:** `docs/superpowers/specs/2026-10-31-milestone-4-hardening-testflight-design.md` (sections 1, 2 and "What success looks like"). Code plans: `2026-10-31-milestone-4-backend.md`, `2026-10-31-milestone-4-ios.md`.

## A. Production Procore app and the prod stack

- [ ] **A1 (Drew, Claude driving)** In the Procore developer portal, open the Field Assistant app and create production credentials: redirect URI `https://api.fieldassistant.tools/v1/auth/callback`, confidential OAuth client, the same scopes as the sandbox app. Note the production client id and secret and the production **App Version Key**. Record what was clicked in `deploy/README.md` under a new "Production Procore app" subsection.
- [ ] **A2 (Drew)** As Company Admin in Procore: Company Tools → Admin → App Management → Install App → Install Custom App → paste the production App Version Key (README "Connecting the app to a Procore company"). Until this is done every project call answers 403 "App is not connected to this company".
- [ ] **A3 (Drew types, Claude verifies presence and length only)** On the droplet, add `PROCORE_CLIENT_ID` and `PROCORE_CLIENT_SECRET` to `/opt/fieldassistant/deploy/.env.prod`; confirm `PROCORE_LOGIN_URL=https://login.procore.com`, `PROCORE_API_URL=https://api.procore.com`, the redirect URL, `ANTHROPIC_API_KEY`, `LLM_EXTRACT_MODEL`, the object-store lines and `FEATURES_PROBE` (optional) are all present. Back the file up first, as for `.env.dev`.
- [ ] **A3b (Claude)** Before opening the backend PR to `develop` (so before A4, which needs it merged): trigger the live RFI evals once on the branch head (`gh workflow run api.yml --ref m4-hardening-testflight`) and confirm the `api-evals` job passes; the `develop` push re-runs them. Also run the gated `TestSandbox_PotentialRFIManagers` once with a sandbox access token (see `api/internal/procore/managers_sandbox_test.go`) to confirm Procore's `potential_rfi_managers` answers 200 with id, name and login rows.
- [ ] **A4 (Claude)** Open the promotion PR `develop → main` once the backend plan is merged; **Drew** approves the `production` deploy in the Actions UI; **Claude** confirms `https://api.fieldassistant.tools/healthz` answers 200 and the `api-prod` container is on the new image.
- [ ] **A5 (Claude)** Bring `develop` up to date with `main` (`git checkout develop && git merge main && git push`).
- [ ] **A6 (Drew)** Apply the Spaces lifecycle rule for the attachments bucket per the README (deferred from milestone 3).

## B. Procore users and permissions

- [ ] **B1 (Drew)** On 118 W Illinois: Mike Obloy, Chris Lance and Owen Burg are RFI Managers; Drew keeps his existing role.
- [ ] **B2 (Drew)** A test project in the real company (for example "Field Assistant Test") and a test user with RFI Manager on that project only, with an email Apple's reviewer can sign in with; share the login with Claude for the review notes (the password goes into App Store Connect's review form, never into chat or the repo).
- [ ] **B3 (Drew)** A Read-only login on the **sandbox** project (for the features-probe check in the backend plan's Task 1 and the device check below). Sign in once with it on the Dev build so the dev backend holds its tokens.

## C. App Store Connect and TestFlight

- [ ] **C1 (Drew)** App Store Connect app records exist for `tools.fieldassistant.app` ("Field Assistant") and `tools.fieldassistant.app.dev` ("Field Assistant Dev"), both on team `PR6KG6PZJN`. Create them if missing (bundle ids are already registered).
- [ ] **C2 (Drew)** An App Store Connect API key with the App Manager role; export `ASC_KEY_ID`, `ASC_ISSUER_ID` and `ASC_KEY_CONTENT` (base64 of the `.p8`) in the shell that runs Fastlane. Not committed, not in GitHub.
- [ ] **C3 (Claude, Drew's Mac)** After the iOS plan is merged: `cd ios && bundle exec fastlane beta_prod` (and `beta_dev` for Drew's own Dev build). Confirm the build appears in App Store Connect with the date-stamped build number.
- [ ] **C4 (Drew)** TestFlight external group "118 W Illinois" with the four Apple IDs; add the first Prod build; fill Beta App Review information: the test user's login from B2, the test project's name, and notes: "Sign in with Procore, choose the test project, tap Record, dictate a short RFI, tap Build, review, tap Submit. The RFI appears in Procore on the test project." Submit for review (usually under a day).
- [ ] **C5 (All four)** Install TestFlight, accept the invite, install Field Assistant, sign in with Procore, pick 118 W Illinois. Each submits one real RFI with a photo; **Drew** confirms each appeared once in Procore.

## D. Device check for this milestone (Prod build on the four phones; Dev build where a sandbox login is needed)

- [ ] **D1** The Read-only sandbox login (B3) on the Dev build: Home shows the RFI card "Not available" with Procore's reason before recording; the record button is disabled with the hint.
- [ ] **D2** A required field configured on the real project (Drew configures one on the test project if 118 W Illinois has none): submitting without it flags the row on Review, or shows "Procore requires <field>" for a field without a row.
- [ ] **D3** Revoke the app's access for one tester in Procore (or change that tester's password): the phone shows one sign-in prompt with the note, and a draft queued before the revocation sends after signing back in.
- [ ] **D4** Airplane mode on during a token refresh (lock the phone for 15 minutes, then open the app offline, then restore signal): no sign-out.
- [ ] **D5** Switch projects three times quickly: Home and the picker do not refetch per switch (watch the dev API log on the droplet for `/companies/{id}/projects`).
- [ ] **D6** Take a photo mid-dictation: the recording continues and the transcript keeps growing.
- [ ] **D7** Build on a phone that already had drafts from the milestone 3 build: the store opens with its drafts (versioned schema).
- [ ] **D8** Milestone 3's seven-step device check, still pending from the roadmap's milestone 3 row, including "Submit, then lock the phone at once".

## E. Weekly pilot review

- [ ] **E1 (Claude)** Each Monday during the pilot: `make pilot-report` (backend plan Task 4) for the previous week; bring the Markdown to Drew; file the five worst drafts as candidate eval cases under `api/internal/features/rfi/evals/pilot/` after Drew confirms the expected values.
- [ ] **E2 (Drew)** Gate review on Jan 8, 2027 against the roadmap criterion: no duplicate submissions, under 20% of drafts with more than two corrections.
