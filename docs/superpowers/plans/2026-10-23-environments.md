# Dev and Production Environments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two deployed backend environments on one droplet (dev on `develop`, production on `main` via pull request), GitHub environments and branch protection to match, and an iOS Dev build that points at the dev backend and can be installed next to the production build.

**Architecture:** One Compose project on the droplet runs `api-dev`, `postgres-dev`, `api-prod`, `postgres-prod` and a single Caddy that routes `api-dev.fieldassistant.tools` and `api.fieldassistant.tools` by hostname. Each API service has its own env file, database and image tag. The deploy workflow deploys dev on every push to `develop` and prod on every push to `main` (which only happens through a reviewed PR). iOS gets a third build configuration and a per-configuration bundle id and URL scheme so dev and prod builds coexist; the backend's redirect scheme becomes configurable so the dev backend can deep-link into the dev app.

**Tech Stack:** Docker Compose, Caddy 2, GitHub Actions, GitHub CLI (`gh`), Go 1.26, XcodeGen, Swift 6, Fastlane.

**Spec:** `docs/superpowers/specs/2026-10-22-v1-decisions.md` section "Environments (decided 2026-10-23)" and `procore-field-assistant-spec.md` section "Hosting and infrastructure".

## Global Constraints

- One droplet, one Compose project at `/opt/fieldassistant/deploy`; env files `.env.dev` and `.env.prod` (gitignored), never a shared one.
- Hosts: `api-dev.fieldassistant.tools` → `api-dev:8080`; `api.fieldassistant.tools` → `api-prod:8080`.
- Image tags: `ghcr.io/monroeresidential/fieldassistant-api:develop` (dev), `:main` (prod), plus `:sha-<7>` on every build. No `latest`.
- Branch flow: `develop` deploys dev on push; `main` deploys prod on push; `main` accepts changes only via pull request with the `api-test` and `ios-test` checks passing; no direct pushes.
- GitHub environments: `development` (no reviewers), `production` (required reviewer: the repo owner).
- Deploy steps must skip cleanly (not fail) when `DROPLET_HOST` is not configured, so the pipeline is green before the droplet exists.
- Backend config gains `APP_REDIRECT_SCHEME` (default `fieldassistant`); the callback redirects to `<scheme>://auth?...`. Dev backend uses `fieldassistant-dev`.
- iOS configurations: `Local` (debug, `http://localhost:8080/v1`, bundle `com.monroeresidential.fieldassistant`, scheme `fieldassistant`, display name "Field Assistant"), `Dev` (debug, `https://api-dev.fieldassistant.tools/v1`, bundle `com.monroeresidential.fieldassistant.dev`, scheme `fieldassistant-dev`, display name "Field Assistant Dev", visible DEV badge), `Prod` (release, `https://api.fieldassistant.tools/v1`, bundle `com.monroeresidential.fieldassistant`, scheme `fieldassistant`). The bundle id for each configuration is set in exactly one place: its xcconfig.
- Existing tests keep passing: Go suite with `TEST_DATABASE_URL`, iOS suite on `iPhone 17`; the gated UI smoke test keeps working on `Local`.
- CI actions stay SHA-pinned (reuse the pins already in the workflows).

## Review Focus

1. **A PR to `main` that changes only docs** must still be mergeable: required checks must actually run on every PR, not be skipped by path filters. Test: Task 2's workflow files have no `paths` filter under `pull_request`; Task 5 verifies with a docs-only PR.
2. **Dev and prod databases must never share a volume or a password.** Test: Task 1's compose check asserts two distinct volumes and two password variables.
3. **A dev build and a prod build on the same phone must each receive their own sign-in callback.** Test: Task 3's Go test for the configurable scheme and Task 4's test that `AuthService` accepts only its configured scheme.
4. **Deploying with no droplet configured must not break CI.** Test: Task 2's guard step; Task 5 observes a green run before any secrets exist.
5. **Backups must cover both databases.** Test: Task 1's `bash -n` plus a dry-run mode that prints both dump commands.

---

### Task 1: One droplet, two stacks (compose, Caddy, backups, README)

**Files:**
- Modify: `deploy/docker-compose.yml`, `deploy/Caddyfile`, `deploy/backup.sh`, `deploy/README.md`
- Create: `deploy/.env.dev.example`, `deploy/.env.prod.example`

**Interfaces:**
- Produces: services `api-dev` (image `${API_DEV_IMAGE:-ghcr.io/monroeresidential/fieldassistant-api:develop}`, `env_file: .env.dev`, depends on healthy `postgres-dev`), `api-prod` (`${API_PROD_IMAGE:-ghcr.io/monroeresidential/fieldassistant-api:main}`, `env_file: .env.prod`, depends on healthy `postgres-prod`), `postgres-dev` (volume `pgdata_dev`, password `${POSTGRES_DEV_PASSWORD}`, db `fieldassistant`), `postgres-prod` (volume `pgdata_prod`, `${POSTGRES_PROD_PASSWORD}`), `caddy` with `DOMAIN`. Caddyfile: two site blocks. `backup.sh ENV` where `ENV` is `dev` or `prod` (default both), `--dry-run` prints the commands. The droplet-level `.env` (compose interpolation) holds only `DOMAIN`, `POSTGRES_DEV_PASSWORD`, `POSTGRES_PROD_PASSWORD`, `SPACES_BUCKET`.

- [ ] **Step 1: Write the compose file and Caddyfile** per Interfaces. `DATABASE_URL` inside `.env.dev` points at `postgres-dev:5432`, inside `.env.prod` at `postgres-prod:5432`. Both API env examples list every variable from `api/.env.example` plus `APP_REDIRECT_SCHEME` (`fieldassistant-dev` in dev, `fieldassistant` in prod), `PROCORE_LOGIN_URL`/`PROCORE_API_URL` (sandbox hosts in dev, production hosts in prod), `PROCORE_REDIRECT_URL` and `PUBLIC_BASE_URL` with the right host.

- [ ] **Step 2: Verify the compose file** — `docker compose -f deploy/docker-compose.yml --env-file <scratch .env with placeholders> config` succeeds; `grep -c "pgdata_" ` shows two distinct volumes; both password variables appear; the Caddyfile has both hosts.

- [ ] **Step 3: Rewrite `backup.sh`** to dump each selected environment's database from its own container to `backups/<env>/fa-<date>.sql.gz` (temp-then-move, `umask 077`), upload to `s3://$SPACES_BUCKET/<env>/`, prune 30 days per env; `--dry-run` prints without executing. Verify with `bash -n` and `./backup.sh --dry-run` showing both dump commands.

- [ ] **Step 4: Rewrite `deploy/README.md`** one-time setup for two env files, two DNS records, GitHub secrets (`DROPLET_HOST`, `DROPLET_SSH_KEY`), the cron line, and the "Connecting the app to a Procore company" section (keep). Say explicitly which env file gets sandbox vs production Procore credentials and which App Version Key.

- [ ] **Step 5: Commit** `feat(deploy): dev and prod stacks on one droplet`.

---

### Task 2: Branch-based deploys and PR-safe checks

**Files:**
- Modify: `.github/workflows/deploy-api.yml`, `.github/workflows/api.yml`, `.github/workflows/ios.yml`
- Modify: `CLAUDE.md` (Commands/Conventions: the branch flow)

**Interfaces:**
- Produces: `api.yml` job id `api-test`; `ios.yml` job id `ios-test`; both keep `paths` filters on `push` but have NO `paths` filter on `pull_request` (so every PR runs both checks). `deploy-api.yml` triggers on `push` to `develop` and `main` (no tag trigger); jobs: `test` (as today), `build` (`needs: test`, pushes `:develop` or `:main` by branch plus `:sha-<7>`), `deploy-dev` (`if: github.ref == 'refs/heads/develop'`, `environment: development`), `deploy-prod` (`if: github.ref == 'refs/heads/main'`, `environment: production`); each deploy job first runs a step that checks `DROPLET_HOST` via `env: HOST: ${{ secrets.DROPLET_HOST }}` and, when empty, prints `::notice::no droplet configured, skipping deploy` and sets an output that makes the SSH step `if: steps.check.outputs.configured == 'true'`. SSH script per env: `cd /opt/fieldassistant/deploy && docker compose pull api-<env> && docker compose up -d api-<env>`.

- [ ] **Step 1: Edit the three workflows** per Interfaces; keep every `uses:` SHA pin.
- [ ] **Step 2: Validate** — YAML parse all three; `actionlint` if available (`brew install actionlint` is fine).
- [ ] **Step 3: Update `CLAUDE.md`** Conventions: "Work happens on `develop`; `main` only via PR; pushes to `develop` deploy dev, merges to `main` deploy prod." Update the Status line's next-steps if it mentions tags.
- [ ] **Step 4: Commit** `ci: deploy dev from develop and prod from main; PR checks always run`.

---

### Task 3: Configurable app redirect scheme (backend)

**Files:**
- Modify: `api/internal/config/config.go`, `api/internal/config/config_test.go`, `api/internal/httpapi/auth_handlers.go`, `api/internal/httpapi/auth_handlers_test.go`, `api/internal/httpapi/testserver_test.go`, `api/.env.example`, `api/openapi.yaml` (description text only; no version bump needed: no schema change)

**Interfaces:**
- Produces: `Config.AppRedirectScheme string` from `APP_REDIRECT_SCHEME`, default `fieldassistant`, validated as `^[a-z][a-z0-9+.-]*$` (else a config error naming the variable). The callback redirect base becomes `<scheme>://auth`. `newTestServer` accepts an optional scheme override (default `fieldassistant`).

- [ ] **Step 1: Write failing tests** — `TestLoad_AppRedirectSchemeDefault` (unset → `fieldassistant`), `TestLoad_AppRedirectSchemeInvalid` (`Bad Scheme` → error mentions `APP_REDIRECT_SCHEME`), `TestAuthCallback_UsesConfiguredScheme` (server built with scheme `fieldassistant-dev` → `Location` starts with `fieldassistant-dev://auth?code=`), and assert the existing callback tests still see `fieldassistant://auth`.
- [ ] **Step 2: Run to verify they fail.** Run: `cd api && TEST_DATABASE_URL=... go test ./internal/config/ ./internal/httpapi/ -run 'Scheme'`. Expected: FAIL (field undefined).
- [ ] **Step 3: Implement** config field + validation; replace the `appRedirectBase` constant with a value from `Deps.Config`; update `.env.example` and the `authCallback` description in `openapi.yaml` ("`<app scheme>://auth?...`, scheme is `fieldassistant` in production and `fieldassistant-dev` in the dev environment"); `make sync-contract`.
- [ ] **Step 4: Run** `go vet ./... && TEST_DATABASE_URL=... go test ./...` → PASS; `make check-contract` → in sync.
- [ ] **Step 5: Commit** `feat(api): configurable app redirect scheme (APP_REDIRECT_SCHEME)`.

---

### Task 4: iOS Local/Dev/Prod configurations, DEV badge, Fastlane lanes

**Files:**
- Create: `ios/Config/Local.xcconfig`
- Modify: `ios/Config/Dev.xcconfig`, `ios/Config/Prod.xcconfig`, `ios/project.yml`, `ios/FieldAssistant/App/AppConfig.swift`, `ios/FieldAssistant/Core/Auth/SignInView.swift`, `ios/FieldAssistant/Core/Auth/AuthService.swift`, `ios/FieldAssistantTests/AuthServiceTests.swift`, `ios/FieldAssistantTests/SmokeTests.swift`, `ios/fastlane/Fastfile`, `ios/fastlane/Appfile`, `.github/workflows/ios.yml` (test config), `CLAUDE.md` (commands)
- Create: `ios/FieldAssistantTests/AppConfigTests.swift`

**Interfaces:**
- xcconfig keys per configuration: `API_BASE_URL`, `API_ENV` (`local`/`dev`/`prod`), `PRODUCT_BUNDLE_IDENTIFIER`, `APP_URL_SCHEME`, `APP_DISPLAY_NAME`. `project.yml`: configs `Local: debug`, `Dev: debug`, `Prod: release`; app target settings no longer hard-code the bundle id (comes from xcconfig); Info.plist properties use `$(APP_URL_SCHEME)`, `$(APP_DISPLAY_NAME)`, `API_ENV: $(API_ENV)`; test targets' bundle ids derive from `$(PRODUCT_BUNDLE_IDENTIFIER).tests` / `.uitests`; scheme run/test config `Local`.
- `AppConfig`: `static var apiBaseURL: URL`, `static var environment: String` (from `API_ENV`), `static var urlScheme: String` (from `CFBundleURLTypes[0].CFBundleURLSchemes[0]`), `static var isDev: Bool { environment == "dev" }`.
- `AuthService` uses `AppConfig.urlScheme` as the `callbackScheme` for `ASWebAuthenticationSession` (injectable for tests: `init(..., callbackScheme: String = AppConfig.urlScheme)`); a callback URL with a different scheme throws `.invalidCallback`.
- `SignInView` shows a capsule "DEV" badge (accessibility label "Development build") above the title when `AppConfig.isDev`.
- Fastlane: lanes `beta_dev` (configuration `Dev`) and `beta_prod` (configuration `Prod`), sharing a private helper; `Appfile` no longer hard-codes a single `app_identifier` (set per lane: `.dev` suffix for dev).

- [ ] **Step 1: Write failing tests** — `AppConfigTests.testLocalConfigValues` (`environment == "local"`, `urlScheme == "fieldassistant"`, `apiBaseURL.path == "/v1"`), `AuthServiceTests.testSignInRejectsCallbackWithWrongScheme` (presenter returns `other://auth?code=abc` → `.invalidCallback`, no exchange request), `testSignInUsesConfiguredCallbackScheme` (presenter records the `callbackScheme` it was asked for == the injected value). Update `SmokeTests` if it hard-codes the bundle id.
- [ ] **Step 2: Run** the focused classes on `Local` → FAIL on missing members.
- [ ] **Step 3: Implement** xcconfigs, `project.yml`, `AppConfig`, `AuthService`, `SignInView` badge, Fastlane lanes; `make ios-project`.
- [ ] **Step 4: Validate** — full iOS suite on `Local` (`-configuration` defaults to the scheme's test config) → `** TEST SUCCEEDED **`; `xcodebuild build -configuration Dev` and `-configuration Prod` for the simulator succeed; `plutil -p` on the built Dev app's Info.plist shows bundle id `.dev`, scheme `fieldassistant-dev`, display name "Field Assistant Dev", `API_ENV dev`; `fastlane lanes` lists `beta_dev`, `beta_prod`, `test`. `ios.yml` unchanged except any config name it references.
- [ ] **Step 5: Update `CLAUDE.md`** commands (`fastlane beta_dev` / `beta_prod`; Local vs Dev vs Prod meaning) and **commit** `feat(ios): Local/Dev/Prod configurations with per-config bundle id, scheme and DEV badge`.

---

### Task 5: GitHub branch, environments, protection, and verification

**Files:** none in the repo beyond what earlier tasks committed. Uses `gh` against `monroeresidential/fieldassistant`.

**Interfaces:**
- Produces: branch `develop` created from `main` and pushed; environments `development` and `production` (production with required reviewer `monroeresidential`); branch protection on `main`: require PR, required status checks `api-test` and `ios-test` (strict), dismiss stale reviews, no force pushes, no deletions, enforce for admins. Default branch stays `main`.

- [ ] **Step 1: Create the environments** — `gh api -X PUT repos/monroeresidential/fieldassistant/environments/development` and `.../production` with `reviewers: [{type: User, id: <owner id from gh api user>}]`.
- [ ] **Step 2: Create `develop`** — `git branch develop main && git push -u origin develop`.
- [ ] **Step 3: Verify checks on a PR** — on a throwaway branch from `develop`, add one line to `docs/superpowers/plans/2026-10-23-environments.md` ("verification PR"), push, open a PR to `develop` with `gh pr create`, confirm both `api-test` and `ios-test` run (`gh pr checks --watch`), merge it with `gh pr merge --squash --delete-branch`.
- [ ] **Step 4: Protect `main`** — `gh api -X PUT repos/.../branches/main/protection` with the required checks named exactly `api-test` and `ios-test`, `required_pull_request_reviews` (0 approvals required but dismiss stale), `enforce_admins: true`, `allow_force_pushes: false`, `allow_deletions: false`.
- [ ] **Step 5: Verify the prod path** — open a PR `develop` → `main` with `gh pr create`; confirm it reports the two required checks and blocks until they pass; after they pass, merge with `gh pr merge --merge`. Confirm the `deploy-api` run on `main` reaches `deploy-prod` and skips with the "no droplet configured" notice (no secrets yet). Record the PR number and run URL in the report.

---

## Self-review notes

- Covered: every row of the decisions table (branches, hosts, Procore creds per env, triggers), GitHub environments and protection, iOS configurations with coexistence, backups for both DBs, docs.
- Deviation from the Phase 1 plan: tag-based production deploy replaced by merge-to-main; tags remain available as release markers but no longer trigger anything.
- Out of scope: creating the droplet, DNS records, GitHub secrets, App Store Connect records for the `.dev` bundle id (manual, documented in the README and CLAUDE.md).
