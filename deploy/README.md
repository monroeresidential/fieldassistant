# Droplet deploy

One droplet runs two stacks through Docker Compose, behind a single Caddy:

| Stack | Host | Branch | Postgres | Procore credentials |
| --- | --- | --- | --- | --- |
| dev | `https://api-dev.fieldassistant.tools` | `develop` | `postgres-dev`, volume `pgdata_dev` | sandbox |
| prod | `https://api.fieldassistant.tools` | `main` | `postgres-prod`, volume `pgdata_prod` | production |

Caddy proxies each host to its own API on port 8080. The two stacks share nothing
but the droplet and Caddy: separate databases, separate passwords, separate secrets.

## One-time setup

Do the steps in order. Run them as root on the droplet, except step 8, which is in GitHub.

1. Create the droplet and point two DNS A records at it:
   `api-dev.fieldassistant.tools` and `api.fieldassistant.tools`.
2. Install Docker, the Compose plugin and `s3cmd`:
   ```
   curl -fsSL https://get.docker.com | sh
   apt install -y s3cmd
   ```
3. Create one SSH user per environment, so a push to `develop` cannot run
   anything on the production stack. Both users are in the `docker` group and in
   a shared `fa-deploy` group, and each has its own keypair.

   **Generate the two keypairs on your own machine first** (not on the droplet).
   These are deploy keys used only by GitHub Actions; keep them separate from the
   root key you use to administer the droplet:
   ```
   ssh-keygen -t ed25519 -N '' -C fieldassistant-deploy-dev  -f ~/.ssh/fieldassistant-deploy-dev
   ssh-keygen -t ed25519 -N '' -C fieldassistant-deploy-prod -f ~/.ssh/fieldassistant-deploy-prod
   ```
   Each command produces a private key (`~/.ssh/fieldassistant-deploy-<env>`, goes
   into the GitHub environment secret in step 8, never into the repo) and a public
   key (`~/.ssh/fieldassistant-deploy-<env>.pub`, goes into that user's
   `authorized_keys` below). Print the public keys so you can paste them:
   ```
   cat ~/.ssh/fieldassistant-deploy-dev.pub ~/.ssh/fieldassistant-deploy-prod.pub
   ```

   Now, on the droplet as root, create the users:
   ```
   groupadd fa-deploy
   for e in dev prod; do
     adduser --disabled-password --gecos "" deploy-$e
     usermod -aG docker,fa-deploy deploy-$e
     mkdir -p /home/deploy-$e/.ssh && chmod 700 /home/deploy-$e/.ssh
   done
   ```
   Write each public key to that user's `authorized_keys`, locked to the forced
   command, so the key can run nothing else. Replace `ssh-ed25519 AAAA...` with
   that environment's public key:
   ```
   install -m 600 -o deploy-dev -g deploy-dev /dev/stdin /home/deploy-dev/.ssh/authorized_keys <<< 'command="/opt/fieldassistant/deploy/deploy-env.sh dev",restrict ssh-ed25519 AAAA...'
   install -m 600 -o deploy-prod -g deploy-prod /dev/stdin /home/deploy-prod/.ssh/authorized_keys <<< 'command="/opt/fieldassistant/deploy/deploy-env.sh prod",restrict ssh-ed25519 AAAA...'
   ```
   Then fix ownership and permissions:
   ```
   for e in dev prod; do
     chmod 600 /home/deploy-$e/.ssh/authorized_keys
     chown -R deploy-$e:deploy-$e /home/deploy-$e/.ssh
   done
   ```
   `restrict` turns off port, agent and X11 forwarding, the PTY and `~/.ssh/rc`.
   The forced command never runs what the client sends; the key can only run
   `deploy-env.sh` with its own argument. The workflow sends the image digest as
   the command, and the script reads it from `SSH_ORIGINAL_COMMAND`.

   On a single droplet the two deploy users are both in the docker group, which
   is root-equivalent, so file permissions are not a security boundary between
   dev and prod. The boundary is the forced-command SSH key: a leaked dev key can
   only run the dev deploy script. If real isolation is ever required, move dev
   to its own droplet.
4. Create the parent directory, then copy this `deploy/` directory to
   `/opt/fieldassistant/deploy` (for example `git clone` the repo to
   `/opt/fieldassistant`, or from your machine `scp -r deploy root@<droplet>:/opt/fieldassistant/`):
   ```
   mkdir -p /opt/fieldassistant
   ```
5. Write three env files in `/opt/fieldassistant/deploy`, starting from the
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
6. Set ownership and permissions. The directory is owned by root and readable by
   both deploy users. Compose reads every service's `env_file`, so both users
   need all three env files. `.env` is group-writable because `deploy-env.sh`
   records the deployed image digest in it. `backups/` belongs to `deploy-prod`,
   which runs the backup cron (`backup.sh` keeps it at mode 700):
   ```
   chown root:fa-deploy /opt/fieldassistant
   chmod 750 /opt/fieldassistant
   cd /opt/fieldassistant/deploy
   chown -R root:fa-deploy .
   chmod 750 . deploy-env.sh backup.sh
   chown root:fa-deploy .env .env.dev .env.prod
   chmod 660 .env
   chmod 640 .env.dev .env.prod
   mkdir -p backups
   chown deploy-prod:fa-deploy backups
   chmod 700 backups
   ```
7. Configure `s3cmd` as `deploy-prod`, with the Spaces access key, secret key
   and endpoint (for example `nyc3.digitaloceanspaces.com`), so the config lands
   in that user's home where the backup cron can read it:
   `sudo -H -u deploy-prod s3cmd --configure`
8. In GitHub, add the secrets. `DROPLET_HOST` is a repository-level secret. The
   private keys go in **environment** secrets named `DROPLET_SSH_KEY`: the
   `development` environment holds the deploy-dev key and the `production`
   environment holds the deploy-prod key. From your own machine with `gh`
   authenticated (the private key files are the ones generated in step 3):
   ```
   gh secret set DROPLET_HOST --repo monroeresidential/fieldassistant --body "<droplet public IP or hostname>"
   gh secret set DROPLET_SSH_KEY --repo monroeresidential/fieldassistant --env development < ~/.ssh/fieldassistant-deploy-dev
   gh secret set DROPLET_SSH_KEY --repo monroeresidential/fieldassistant --env production  < ~/.ssh/fieldassistant-deploy-prod
   ```
   Test each key once from your machine; the forced command should refuse a
   missing digest with `exit 3` rather than give you a shell:
   ```
   ssh -i ~/.ssh/fieldassistant-deploy-dev  deploy-dev@<droplet>  ; echo "exit $?"
   ssh -i ~/.ssh/fieldassistant-deploy-prod deploy-prod@<droplet> ; echo "exit $?"
   ```
9. Start everything. If the image is private, first run `docker login ghcr.io`
   as root and as each deploy user, since Docker credentials are per user. Log in
   with your GitHub username and a personal access token (classic) that has only
   the `read:packages` scope. Root
   pulls the images now; the deploy users pull them on every release:
   ```
   docker login ghcr.io
   sudo -H -u deploy-dev docker login ghcr.io
   sudo -H -u deploy-prod docker login ghcr.io
   ```
   ```
   cd /opt/fieldassistant/deploy && docker compose up -d
   ```
10. Check `https://api-dev.fieldassistant.tools/healthz` and
    `https://api.fieldassistant.tools/healthz`.
11. Install the backup cron as `deploy-prod`; see Backups below.

The API applies database migrations itself on startup, in each stack's own database.

Before the first deploy each stack runs its branch tag (`:develop` or `:main`).
After that it runs the digest pinned in `.env`; see Releases and Rollback below.

### Updating the deploy files

Deploys only change the image. After changing `docker-compose.yml`, the
`Caddyfile` or the scripts, update `/opt/fieldassistant/deploy` as root (`git pull`
in `/opt/fieldassistant`, or copy the files again), reapply the step 6
permissions to any new files, and run `docker compose up -d`. A Caddyfile change
also needs `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`.

## Releases

GitHub secrets required: `DROPLET_HOST` (repository) and `DROPLET_SSH_KEY` (one
per environment, see setup step 8). Deploys to the same ref are serialized.

The deploy workflow (`.github/workflows/deploy-api.yml`) runs on every push to
`develop` and `main`. It runs the API tests, then builds the image and pushes it
to `ghcr.io/monroeresidential/fieldassistant-api` tagged with the branch name
(`develop` or `main`) and `sha-<7 chars>`. A push to `develop` then updates
`api-dev` and a push to `main` updates `api-prod`. Tags do not trigger deploys.

Deploys pin the exact image the run built, not the branch tag, so an image
pushed under `:main` by some other workflow never reaches production. The
workflow sends the image digest (`sha256:...`) over SSH. `deploy-env.sh` checks
its format, writes `API_DEV_IMAGE` or `API_PROD_IMAGE` in `.env` as
`ghcr.io/monroeresidential/fieldassistant-api@sha256:...`, runs
`docker compose pull` and `docker compose up -d` for that one service, and fails
the deploy if the running container is not that digest.

Both GitHub environments exist. `production` requires the owner's approval, so
every merge to `main` waits in the Actions UI until it is approved.
Deployment-branch policies limit `production` to protected branches (`main`) and
`development` to `develop`. If the `DROPLET_HOST` secret is empty the deploy job
prints a notice and skips the SSH step, so the run stays green before the
droplet exists.

### Rollback

As root in `/opt/fieldassistant/deploy`, set the stack's image line in `.env` to
an earlier build, then restart that service:
```
API_PROD_IMAGE=ghcr.io/monroeresidential/fieldassistant-api:sha-<7 chars>
```
```
docker compose up -d api-prod
```
`@sha256:<digest>` works in place of `:sha-<7 chars>`. For dev, set
`API_DEV_IMAGE` and run `docker compose up -d api-dev`. The next deploy to that
stack replaces the line again.

## Backups

`backup.sh [dev|prod] [--dry-run]` dumps each selected database from its own
container (no argument backs up both), gzips it to `backups/<env>/fa-YYYY-MM-DD.sql.gz`,
uploads it to `s3://$SPACES_BUCKET/<env>/` and deletes local dumps older than
30 days per environment. `--dry-run` prints the commands without running them.
Install this crontab line as `deploy-prod` (`sudo -H -u deploy-prod crontab -e`).
First create its log file: `touch /var/log/fa-backup.log && chown deploy-prod /var/log/fa-backup.log`.

```
0 3 * * * /opt/fieldassistant/deploy/backup.sh >> /var/log/fa-backup.log 2>&1
```

## Connecting the app to a Procore company

Procore returns `403 {"errors":["App is not connected to this company."]}` until a
company admin installs the app in that company. In Procore: Company Tools → Admin →
App Management → Install App → Install Custom App, and paste the App Version Key from
the Developer Portal (Manage Manifests → the promoted version). Do this once per company,
with the sandbox key for the sandbox company and the production key for production.
