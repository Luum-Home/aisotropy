#!/usr/bin/env bash
# run.sh — LlamaIndex live-arena adapter entrypoint.
#
# Contract: reads ONE JSON request on stdin, writes ONE JSON response on
# stdout (adapter.py's own stderr carries PROGRESS/diagnostic lines — this
# script does not add any stdout noise of its own).
#
#   echo '{"system":"...","messages":[...]}' | bash run.sh
#
# Sources the repo-root .env for OPENAI_API_KEY / OPENAI_BASE_URL /
# OPENAI_MODEL (agnes-2.0-flash, OpenAI-compatible), activates this arena's
# dedicated venv, then runs adapter.py. The key is never echoed or logged.
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
  echo "run.sh: WARNING: $ENV_FILE not found — relying on already-exported env vars." >&2
fi

if [[ ! -x "$SCRIPT_DIR/.venv/bin/python3" ]]; then
  echo "run.sh: FATAL: venv missing at $SCRIPT_DIR/.venv — run:" >&2
  echo "  python3.13 -m venv $SCRIPT_DIR/.venv && $SCRIPT_DIR/.venv/bin/pip install -r $SCRIPT_DIR/requirements.txt" >&2
  exit 1
fi

exec "$SCRIPT_DIR/.venv/bin/python3" "$SCRIPT_DIR/adapter.py"
