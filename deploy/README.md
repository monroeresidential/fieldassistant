# Droplet deploy

The droplet runs `api`, `postgres` and `caddy` through Docker Compose.
Caddy serves `https://api.<DOMAIN>` and proxies to the API on port 8080.

## One-time setup

1. Create the droplet and point a DNS A record for `api.<DOMAIN>` at it.
2. Install Docker and the Compose plugin:
   `curl -fsSL https://get.docker.com | sh`
3. Install `s3cmd` and run `s3cmd --configure` once with the Spaces access key,
   secret key and endpoint (for example `nyc3.digitaloceanspaces.com`).
4. Copy this `deploy/` directory to `/opt/fieldassistant/deploy`
   (for example `git clone` the repo to `/opt/fieldassistant` or `scp -r deploy`).
5. In `docker-compose.yml`, replace `OWNER` in the image name with the GitHub owner.
6. Write `/opt/fieldassistant/deploy/.env` with the API variables from
   `api/.env.example` plus the deploy-only ones below. `DATABASE_URL` must point
   at the compose service, for example
   `postgres://fa:<POSTGRES_PASSWORD>@postgres:5432/fieldassistant?sslmode=disable`.

   ```
   DOMAIN=example.com
   POSTGRES_PASSWORD=<strong password>
   SPACES_BUCKET=<bucket name>
   ```
7. If the image is private, log in once: `docker login ghcr.io`.
8. Start everything: `cd /opt/fieldassistant/deploy && docker compose up -d`
9. Check `https://api.<DOMAIN>/healthz`.

The API applies database migrations itself on startup.

## Releases

Pushing a tag like `v0.1.0` runs `.github/workflows/deploy-api.yml`. It builds
and pushes the image to GHCR, then SSHes to the droplet and runs
`docker compose pull api && docker compose up -d api`.

GitHub repository secrets required: `DROPLET_HOST`, `DROPLET_SSH_KEY`.

## Backups

`backup.sh` dumps the database, gzips it to `backups/fa-YYYY-MM-DD.sql.gz`,
uploads it to the Spaces bucket and deletes local dumps older than 30 days.
Add this crontab line (`crontab -e`):

```
0 3 * * * /opt/fieldassistant/deploy/backup.sh >> /var/log/fa-backup.log 2>&1
```
