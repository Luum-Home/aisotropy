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

# Test the INTERPRETER, not the directory. A venv's bin/python is a symlink to
# the exact minor version that created it; a Homebrew upgrade (3.13 -> 3.14)
# leaves the directory perfectly intact with a dangling symlink, so `-d .venv`
# is true for a venv that cannot run. The failure then surfaced two lines later
# as `python: command not found` (exit 127) — a message that says nothing about
# venvs, on a framework that consequently scored 0/7. Same defect and same fix
# as llamaindex/run.sh (which had the strong test) and the embed server.
if [[ ! -x "$SCRIPT_DIR/.venv/bin/python3" ]]; then
  echo "PROGRESS: error: no usable interpreter at $SCRIPT_DIR/.venv/bin/python3 (missing venv, or its base Python was upgraded away) — run: rm -rf .venv && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt" >&2
  exit 1
fi

# exec the venv interpreter DIRECTLY rather than `source activate && exec python`:
# activate only prepends to PATH, so a broken venv silently falls through to
# whatever `python` the ambient PATH happens to offer — or to nothing at all.
exec "$SCRIPT_DIR/.venv/bin/python3" "$SCRIPT_DIR/adapter.py"
