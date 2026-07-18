#!/usr/bin/env bash
# Live arena runner for the Spring AI (Java) adapter.
#
# Sources the repo-root .env (for OPENAI_API_KEY / OPENAI_BASE_URL /
# OPENAI_MODEL), exports the shared backend-tier endpoints (EMBED_URL /
# QDRANT_URL / RQLITE_URL) if unset, builds the uber-jar once (fast no-op if
# already built), then pipes one JSON object from stdin through the adapter and
# emits exactly one JSON object on stdout. All diagnostics go to stderr.
#
# Usage:
#   echo '{"system":"...","messages":[{"role":"user","content":"..."}]}' \
#     | bash arenas/live/spring-ai/run.sh
#
# The API key is never printed or logged by this script or the adapter.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Repo root is four levels up: arenas/live/spring-ai -> arenas/live -> arenas -> repo.
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "[spring-ai/run.sh] warning: ${ENV_FILE} not found; relying on inherited env" >&2
fi

# Shared backend-tier defaults (identical to the sibling adapters). The runner
# overrides QDRANT_COLLECTION/ARENA_COLLECTION per framework; these are only
# the standalone-run fallbacks.
export EMBED_URL="${EMBED_URL:-http://127.0.0.1:8899}"
export QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"
export RQLITE_URL="${RQLITE_URL:-http://localhost:4001}"

JAR="${SCRIPT_DIR}/target/spring-ai-live-arena.jar"

# Build once. Prefer the Maven wrapper if present; else system mvn. Output goes
# to stderr so stdout stays JSON-only.
if [[ ! -f "${JAR}" ]]; then
  if [[ -x "${SCRIPT_DIR}/mvnw" ]]; then
    ( cd "${SCRIPT_DIR}" && ./mvnw -q -DskipTests package ) 1>&2
  elif command -v mvn >/dev/null 2>&1; then
    ( cd "${SCRIPT_DIR}" && mvn -q -DskipTests package ) 1>&2
  else
    echo "[spring-ai/run.sh] error: no Maven (mvnw or mvn) found to build ${JAR}" >&2
    exit 1
  fi
fi

exec java -jar "${JAR}"
