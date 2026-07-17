#!/usr/bin/env bash
# eino-live-arena — uniform LIVE arena adapter runner for CloudWeGo Eino.
#
# Sources the repo-root .env (for OPENAI_API_KEY / OPENAI_BASE_URL /
# OPENAI_MODEL), builds the adapter if needed, and runs it: reads one JSON
# request object from stdin, writes one JSON response object to stdout. All
# logs/diagnostics go to stderr — stdout carries only the JSON response.
#
# Usage:
#   echo '{"system":"...","messages":[{"role":"user","content":"..."}]}' \
#     | bash arenas/live/eino/run.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "eino-live-arena: warning: $ENV_FILE not found — relying on already-exported env vars" >&2
fi

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [ -z "${!var:-}" ]; then
    echo "eino-live-arena: required env var $var is not set" >&2
    exit 1
  fi
done

cd "$SCRIPT_DIR"
exec go run .
