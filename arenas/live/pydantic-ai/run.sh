#!/usr/bin/env bash
# LIVE arena adapter runner for Pydantic AI.
#
# Reads one JSON request from stdin, writes one JSON result to stdout.
# Sources the repo-root .env for OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL
# (agnes-2.0-flash, OpenAI-compatible) and activates this arena's own venv.
#
# Usage:
#   echo '{"system":"...","messages":[{"role":"user","content":"..."}]}' | bash run.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "PROGRESS: warning: no .env found at $ENV_FILE — relying on already-exported environment" >&2
fi

if [[ ! -d "$SCRIPT_DIR/.venv" ]]; then
  echo "PROGRESS: error: venv not found at $SCRIPT_DIR/.venv — run: python3.13 -m venv .venv && .venv/bin/pip install -r requirements.txt" >&2
  exit 1
fi

# shellcheck disable=SC1091
source "$SCRIPT_DIR/.venv/bin/activate"

exec python "$SCRIPT_DIR/adapter.py"
