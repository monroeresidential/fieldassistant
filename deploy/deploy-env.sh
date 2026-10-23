#!/usr/bin/env bash
# Forced command for the deploy-dev and deploy-prod SSH keys.
# Usage: deploy-env.sh dev|prod
#
# The workflow sends the image digest it built as the SSH command; sshd passes
# it here in SSH_ORIGINAL_COMMAND. The digest is pinned in .env
# (API_DEV_IMAGE / API_PROD_IMAGE), so a mutable tag pushed by another branch
# can never reach this stack.
set -euo pipefail

case "${1:-}" in
  dev|prod) ;;
  *) echo "usage: $0 dev|prod" >&2; exit 2 ;;
esac
ENV_NAME="$1"
SERVICE="api-${ENV_NAME}"
REPO="ghcr.io/monroeresidential/fieldassistant-api"

# Only the first line counts: with script_stop, appleboy/ssh-action appends an
# exit-code check line after the script. Nothing received here is executed.
DIGEST="${SSH_ORIGINAL_COMMAND:-}"
DIGEST="${DIGEST%%$'\n'*}"
DIGEST="${DIGEST%$'\r'}"
if ! [[ "${DIGEST}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "expected an image digest (sha256:<64 hex>) as the SSH command, got: '${DIGEST}'" >&2
  exit 3
fi
IMAGE="${REPO}@${DIGEST}"

case "${ENV_NAME}" in
  dev) VAR=API_DEV_IMAGE ;;
  prod) VAR=API_PROD_IMAGE ;;
esac

cd /opt/fieldassistant/deploy

# Rewrite the image line in .env. Dev and prod deploys can run at the same
# time, so hold a lock on .env. The directory is not writable by the deploy
# users, so build the new contents in a temp file and copy them back over .env
# in place, which keeps its root:fa-deploy 660 ownership and mode.
exec 9<>.env
flock 9
TMP="$(mktemp)"
trap 'rm -f "${TMP}"' EXIT
grep -v -E "^${VAR}=" .env > "${TMP}" || true
echo "${VAR}=${IMAGE}" >> "${TMP}"
cat "${TMP}" > .env
flock -u 9

docker compose pull "${SERVICE}"
docker compose up -d "${SERVICE}"

RUNNING="$(docker inspect --format '{{join .RepoDigests " "}}' "$(docker compose images -q "${SERVICE}")")"
case " ${RUNNING} " in
  *"@${DIGEST} "*) echo "${SERVICE} running ${IMAGE}" ;;
  *) echo "${SERVICE} is not running ${DIGEST} (repo digests: ${RUNNING})" >&2; exit 4 ;;
esac
