#!/usr/bin/env bash
# Forced command for the deploy-dev and deploy-prod SSH keys.
# Usage: deploy-env.sh dev|prod
set -euo pipefail

case "${1:-}" in
  dev|prod) ;;
  *) echo "usage: $0 dev|prod" >&2; exit 2 ;;
esac

cd /opt/fieldassistant/deploy
docker compose pull "api-$1"
docker compose up -d "api-$1"
