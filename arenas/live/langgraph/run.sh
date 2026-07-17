#!/usr/bin/env bash
# Runs the LangGraph live-arena adapter against the real agnes model.
#
# Usage:
#   echo '{"system":"...","messages":[{"role":"user","content":"..."}]}' | bash run.sh
#
# Sources the repo-root .env for OPENAI_API_KEY / OPENAI_BASE_URL /
# OPENAI_MODEL, activates this arena's dedicated venv, and pipes stdin
# through to adapter.py unchanged. Emits exactly one JSON object on stdout.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "warning: ${ENV_FILE} not found; relying on inherited environment" >&2
fi

VENV_DIR="${SCRIPT_DIR}/.venv"
if [[ ! -x "${VENV_DIR}/bin/python" ]]; then
  echo "error: venv not found at ${VENV_DIR} (run: python3 -m venv .venv && .venv/bin/pip install -r requirements.txt)" >&2
  exit 1
fi

exec "${VENV_DIR}/bin/python" "${SCRIPT_DIR}/adapter.py"
