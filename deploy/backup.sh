#!/usr/bin/env bash
# Nightly Postgres dump per environment, uploaded to a DigitalOcean Spaces
# bucket via s3cmd. Run from cron on the droplet. Requires a one-time
# `s3cmd --configure`.
#
# Usage: backup.sh [dev|prod] [--dry-run]
#   No environment argument backs up both. --dry-run prints the commands only.
set -euo pipefail
umask 077

cd "$(dirname "$0")"

usage() {
  echo "usage: $0 [dev|prod] [--dry-run]" >&2
  exit 2
}

DRY_RUN=0
ENVS=()
for arg in "$@"; do
  case "${arg}" in
    --dry-run) DRY_RUN=1 ;;
    dev|prod) ENVS+=("${arg}") ;;
    *) usage ;;
  esac
done
if [ "${#ENVS[@]}" -eq 0 ]; then
  ENVS=(dev prod)
fi

# Load SPACES_BUCKET from .env without executing the file.
SPACES_BUCKET=""
if [ -f .env ]; then
  SPACES_BUCKET="$(grep -E '^SPACES_BUCKET=' .env | head -n1 | cut -d= -f2- || true)"
fi
if [ -z "${SPACES_BUCKET}" ]; then
  if [ "${DRY_RUN}" -eq 1 ]; then
    SPACES_BUCKET="<SPACES_BUCKET>"
  else
    echo "SPACES_BUCKET is not set in .env" >&2
    exit 1
  fi
fi

TMP=""
trap 'if [ -n "${TMP}" ]; then rm -f "${TMP}"; fi' EXIT

backup_env() {
  local env="$1"
  local dir="backups/${env}"
  local file="${dir}/fa-$(date +%F).sql.gz"

  if [ "${DRY_RUN}" -eq 1 ]; then
    echo "mkdir -p ${dir} && chmod 700 backups ${dir}"
    echo "docker compose exec -T postgres-${env} pg_dump -U fa fieldassistant | gzip > ${file}.tmp"
    echo "mv ${file}.tmp ${file}"
    echo "s3cmd put ${file} s3://${SPACES_BUCKET}/${env}/$(basename "${file}")"
    echo "find ${dir} -name 'fa-*.sql.gz' -mtime +30 -delete"
    return
  fi

  mkdir -p "${dir}"
  chmod 700 backups "${dir}"
  TMP="${file}.tmp"

  # Dump to a temp file and move into place only on success.
  docker compose exec -T "postgres-${env}" pg_dump -U fa fieldassistant | gzip > "${TMP}"
  mv "${TMP}" "${file}"
  TMP=""
  s3cmd put "${file}" "s3://${SPACES_BUCKET}/${env}/$(basename "${file}")"

  # Keep 30 days of local dumps per environment.
  find "${dir}" -name 'fa-*.sql.gz' -mtime +30 -delete
}

for env in "${ENVS[@]}"; do
  backup_env "${env}"
done
