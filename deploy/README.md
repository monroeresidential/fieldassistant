# Droplet deploy

The droplet runs `api`, `postgres` and `caddy` through Docker Compose.
Caddy serves `https://api.fieldassistant.tools` and proxies to the API on port 8080.

## One-time setup

1. Create the droplet and point a DNS A record for `api.fieldassistant.tools` at it.
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
6. In `docker-compose.yml`, replace `OWNER` in the image name with the GitHub owner.
7. Write `/opt/fieldassistant/deploy/.env` with the API variables from
   `api/.env.example` plus the deploy-only ones below. `DATABASE_URL` must point
   at the compose service, for example
   `postgres://fa:<POSTGRES_PASSWORD>@postgres:5432/fieldassistant?sslmode=disable`.

   ```
   DOMAIN=fieldassistant.tools
   POSTGRES_PASSWORD=<strong password>
   SPACES_BUCKET=<bucket name>
   ```
8. If the image is private, log in once: `docker login ghcr.io`.
9. Start everything: `cd /opt/fieldassistant/deploy && docker compose up -d`
10. Check `https://api.fieldassistant.tools/healthz`.

The API applies database migrations itself on startup.

## Releases

Pushing a tag like `v0.1.0` runs `.github/workflows/deploy-api.yml`. It builds
and pushes the image to GHCR, then SSHes to the droplet and runs
`docker compose pull api && docker compose up -d api`.

GitHub secrets required: `DROPLET_HOST`, `DROPLET_SSH_KEY`. The deploy job uses the `production` environment, so create it in repository settings (optionally with required reviewers). Deploys run only after the test job passes.

## Backups

`backup.sh` dumps the database, gzips it to `backups/fa-YYYY-MM-DD.sql.gz`,
uploads it to the Spaces bucket and deletes local dumps older than 30 days.
Add this crontab line (`crontab -e`):

```
0 3 * * * /opt/fieldassistant/deploy/backup.sh >> /var/log/fa-backup.log 2>&1
```
