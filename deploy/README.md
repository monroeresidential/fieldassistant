# Droplet deploy

One droplet runs two stacks through Docker Compose, behind a single Caddy:

| Stack | Host | API image tag | Postgres | Procore credentials |
| --- | --- | --- | --- | --- |
| dev | `https://api-dev.fieldassistant.tools` | `develop` | `postgres-dev`, volume `pgdata_dev` | sandbox |
| prod | `https://api.fieldassistant.tools` | `main` | `postgres-prod`, volume `pgdata_prod` | production |

Caddy proxies each host to its own API on port 8080. The two stacks share nothing
but the droplet and Caddy: separate databases, separate passwords, separate secrets.

## One-time setup

1. Create the droplet and point two DNS A records at it:
   `api-dev.fieldassistant.tools` and `api.fieldassistant.tools`.
2. Install Docker and the Compose plugin:
   `curl -fsSL https://get.docker.com | sh`
3. Install `s3cmd` (`apt install s3cmd`). It is configured in step 4, after the
   `deploy-prod` user exists.
4. Create one SSH user per environment, so a push to `develop` cannot run
   anything on the production stack. Both users are in the `docker` group and
   each has its own keypair (`ssh-keygen -t ed25519`, one per environment):
   ```
   groupadd fa-deploy
   for e in dev prod; do
     adduser --disabled-password --gecos "" deploy-$e
     usermod -aG docker,fa-deploy deploy-$e
     mkdir -p /home/deploy-$e/.ssh && chmod 700 /home/deploy-$e/.ssh
   done
   chown -R deploy-dev:deploy-dev /home/deploy-dev/.ssh
   chown -R deploy-prod:deploy-prod /home/deploy-prod/.ssh
   mkdir -p /opt/fieldassistant && chown root:fa-deploy /opt/fieldassistant && chmod 750 /opt/fieldassistant
   ```
   Put each public key in that user's `authorized_keys`, locked to the forced
   command (`chmod 600` the file). The key can run nothing else:
   ```
   # /home/deploy-dev/.ssh/authorized_keys
   command="/opt/fieldassistant/deploy/deploy-env.sh dev",no-pty,no-port-forwarding,no-agent-forwarding,no-X11-forwarding ssh-ed25519 AAAA...
   # /home/deploy-prod/.ssh/authorized_keys
   command="/opt/fieldassistant/deploy/deploy-env.sh prod",no-pty,no-port-forwarding,no-agent-forwarding,no-X11-forwarding ssh-ed25519 AAAA...
   ```
   Run the remaining steps as root. After step 6, set ownership so `deploy/` is
   owned by root and readable by both users, along with the three env files (Compose reads every service's `env_file`, so both users need all of them):
   ```
   cd /opt/fieldassistant/deploy
   chown -R root:fa-deploy . && chmod 750 . && chmod 750 deploy-env.sh backup.sh
   chown root:fa-deploy .env .env.dev .env.prod
   chmod 640 .env .env.dev .env.prod
   mkdir -p backups && chown deploy-prod:fa-deploy backups && chmod 750 backups
   sudo -u deploy-prod s3cmd --configure
   ```
   Create `backups/` before installing the backup cron. Run `s3cmd --configure`
   as `deploy-prod` with the Spaces access key, secret key and endpoint (for
   example `nyc3.digitaloceanspaces.com`), so the config lands in that user's
   home and the cron job can read it.
   On a single droplet the two deploy users are both in the docker group, which
   is root-equivalent, so file permissions are not a security boundary between
   dev and prod. The boundary is the forced-command SSH key: a leaked dev key can
   only run the dev deploy script. If real isolation is ever required, move dev
   to its own droplet.
   The forced command ignores the workflow's `script:` block; the key can only
   run `deploy-env.sh` with its own argument.
   The private keys go in GitHub **environment** secrets named `DROPLET_SSH_KEY`:
   the `development` environment holds the deploy-dev key and the `production`
   environment holds the deploy-prod key. `DROPLET_HOST` is a repository-level secret.
5. Copy this `deploy/` directory to `/opt/fieldassistant/deploy`
   (for example `git clone` the repo to `/opt/fieldassistant` or `scp -r deploy`).
6. Write three env files in `/opt/fieldassistant/deploy`, starting from the
   `*.example` files. Use plain `KEY=value` lines: no quotes around values and
   Unix line endings (no CRLF), or the values reach the server with stray
   characters. None of these files is committed.
   - `.env` (from `.env.example`): read by Compose only, not by the API. Holds
     `DOMAIN` (`fieldassistant.tools`), `POSTGRES_DEV_PASSWORD`,
     `POSTGRES_PROD_PASSWORD` and `SPACES_BUCKET`. Use two different passwords.
   - `.env.dev` (from `.env.dev.example`): the dev API. Put the **sandbox**
     Procore client id and secret here. `DATABASE_URL` points at `postgres-dev`
     and uses `POSTGRES_DEV_PASSWORD`. The redirect URL and `PUBLIC_BASE_URL` use
     `api-dev.fieldassistant.tools`, and `APP_REDIRECT_SCHEME` is `fieldassistant-dev`.
   - `.env.prod` (from `.env.prod.example`): the prod API. Put the **production**
     Procore client id and secret here. `DATABASE_URL` points at `postgres-prod`
     and uses `POSTGRES_PROD_PASSWORD`. The redirect URL and `PUBLIC_BASE_URL` use
     `api.fieldassistant.tools`, and `APP_REDIRECT_SCHEME` is `fieldassistant`.

   Each `PROCORE_REDIRECT_URL` must match the redirect URI registered with the
   matching Procore app exactly. Generate `TOKEN_VAULT_KEY` and `SESSION_SECRET`
   separately for each environment. If a database password has special characters
   (`@ : / ? # %` and the like), URL-encode it in `DATABASE_URL` (for example `@`
   becomes `%40`); the password in `.env` stays raw.
7. If the image is private, log in once: `docker login ghcr.io`.
8. Start everything: `cd /opt/fieldassistant/deploy && docker compose up -d`
9. Check `https://api-dev.fieldassistant.tools/healthz` and
   `https://api.fieldassistant.tools/healthz`.

The API applies database migrations itself on startup, in each stack's own database.

To pin a different image for a stack, set `API_DEV_IMAGE` or `API_PROD_IMAGE` in `.env`.

## Releases

GitHub secrets required: `DROPLET_HOST` (repository) and `DROPLET_SSH_KEY` (one
per environment, see setup step 4). Deploys to the same ref are serialized.

The deploy workflow (`.github/workflows/deploy-api.yml`) runs on every push to
`develop` and `main`. It runs the API tests, then builds the image and pushes it
to `ghcr.io/monroeresidential/fieldassistant-api` tagged with the branch name
(`develop` or `main`) and `sha-<7 chars>`. A push to `develop` then updates
`api-dev` and a push to `main` updates `api-prod`. On the droplet each update is
`docker compose pull <service> && docker compose up -d <service>`. Tags do not
trigger deploys.

Create the `development` and `production` GitHub environments in repository
settings (optionally with required reviewers on `production`). If the
`DROPLET_HOST` secret is empty the deploy job prints a notice and skips the SSH
step, so the run stays green before the droplet exists.

## Backups

`backup.sh [dev|prod] [--dry-run]` dumps each selected database from its own
container (no argument backs up both), gzips it to `backups/<env>/fa-YYYY-MM-DD.sql.gz`,
uploads it to `s3://$SPACES_BUCKET/<env>/` and deletes local dumps older than
30 days per environment. `--dry-run` prints the commands without running them.
Add this crontab line as `deploy-prod` (`sudo -u deploy-prod crontab -e`):

```
0 3 * * * /opt/fieldassistant/deploy/backup.sh >> /var/log/fa-backup.log 2>&1
```

## Connecting the app to a Procore company

Procore returns `403 {"errors":["App is not connected to this company."]}` until a
company admin installs the app in that company. In Procore: Company Tools → Admin →
App Management → Install App → Install Custom App, and paste the App Version Key from
the Developer Portal (Manage Manifests → the promoted version). Do this once per company,
with the sandbox key for the sandbox company and the production key for production.
