#!/usr/bin/env bash
# Nightly Postgres dump, uploaded to a DigitalOcean Spaces bucket via s3cmd.
# Run from cron on the droplet. Requires a one-time `s3cmd --configure`.
set -euo pipefail
umask 077

cd "$(dirname "$0")"

# Load SPACES_BUCKET from .env without executing the file.
SPACES_BUCKET="$(grep -E '^SPACES_BUCKET=' .env | head -n1 | cut -d= -f2-)"
if [ -z "${SPACES_BUCKET}" ]; then
  echo "SPACES_BUCKET is not set in .env" >&2
  exit 1
fi

mkdir -p backups
chmod 700 backups
FILE="backups/fa-$(date +%F).sql.gz"
TMP="${FILE}.tmp"
trap 'rm -f "${TMP}"' EXIT

# Dump to a temp file and move into place only on success.
docker compose exec -T postgres pg_dump -U fa fieldassistant | gzip > "${TMP}"
mv "${TMP}" "${FILE}"
s3cmd put "${FILE}" "s3://${SPACES_BUCKET}/$(basename "${FILE}")"

# Keep 30 days of local dumps.
find backups -name 'fa-*.sql.gz' -mtime +30 -delete
