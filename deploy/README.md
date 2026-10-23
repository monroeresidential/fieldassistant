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
3. Install `s3cmd` and run `s3cmd --configure` once with the Spaces access key,
   secret key and endpoint (for example `nyc3.digitaloceanspaces.com`).
4. Create the non-root deploy user the workflow SSHes in as:
   ```
   adduser --disabled-password --gecos "" deploy
   usermod -aG docker deploy
   mkdir -p /home/deploy/.ssh && chmod 700 /home/deploy/.ssh
   # put the deploy public key in /home/deploy/.ssh/authorized_keys
   chmod 600 /home/deploy/.ssh/authorized_keys
   chown -R deploy:deploy /home/deploy/.ssh
   mkdir -p /opt/fieldassistant && chown -R deploy /opt/fieldassistant
   ```
   Run the remaining steps as `deploy`. The private key goes in the `DROPLET_SSH_KEY` secret.
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

GitHub secrets required: `DROPLET_HOST`, `DROPLET_SSH_KEY`.

The deploy workflow (`.github/workflows/deploy-api.yml`) is updated separately to
match this layout: the `develop` image goes to `api-dev` and the `main` image goes
to `api-prod`. On the droplet each update is
`docker compose pull <service> && docker compose up -d <service>`, with
`<service>` being `api-dev` or `api-prod`. Create the `production` GitHub
environment in repository settings (optionally with required reviewers) for the
prod deploy. Deploys run only after the test job passes.

## Backups

`backup.sh [dev|prod] [--dry-run]` dumps each selected database from its own
container (no argument backs up both), gzips it to `backups/<env>/fa-YYYY-MM-DD.sql.gz`,
uploads it to `s3://$SPACES_BUCKET/<env>/` and deletes local dumps older than
30 days per environment. `--dry-run` prints the commands without running them.
Add this crontab line (`crontab -e`):

```
0 3 * * * /opt/fieldassistant/deploy/backup.sh >> /var/log/fa-backup.log 2>&1
```

## Connecting the app to a Procore company

Procore returns `403 {"errors":["App is not connected to this company."]}` until a
company admin installs the app in that company. In Procore: Company Tools → Admin →
App Management → Install App → Install Custom App, and paste the App Version Key from
the Developer Portal (Manage Manifests → the promoted version). Do this once per company,
with the sandbox key for the sandbox company and the production key for production.
