#!/usr/bin/env bash
# Live arena runner for the Genkit (Go) adapter.
#
# Sources the repo-root .env (for OPENAI_API_KEY / OPENAI_BASE_URL /
# OPENAI_MODEL), then pipes one JSON object from stdin through the adapter and
# emits exactly one JSON object on stdout. All diagnostics go to stderr.
#
# Usage:
#   echo '{"system":"...","messages":[{"role":"user","content":"..."}]}' \
#     | bash arenas/live/genkit-go/run.sh
#
# The API key is never printed or logged by this script or the adapter.
set -euo pipefail

# Resolve the adapter directory regardless of the caller's CWD.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Repo root is four levels up: arenas/live/genkit-go -> arenas/live -> arenas -> repo.
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

if [[ -f "${ENV_FILE}" ]]; then
  # Export every variable defined in .env into the environment.
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "[genkit-go/run.sh] warning: ${ENV_FILE} not found; relying on inherited env" >&2
fi

# Build once (fast no-op if already built), then run, forwarding stdin/stdout.
( cd "${SCRIPT_DIR}" && go build -o ./genkit-go-live-arena . ) 1>&2
exec "${SCRIPT_DIR}/genkit-go-live-arena"
