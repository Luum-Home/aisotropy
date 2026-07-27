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
# The reference stack requires auth on both tiers (rqlite `-auth`, Qdrant
# QDRANT__SERVICE__API_KEY). These are the same local-dev placeholders the
# compose file itself defaults to; an anonymous URL here is a guaranteed 401.
export QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"
export QDRANT_API_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}"
export RQLITE_URL="${RQLITE_URL:-http://aisotropy:local-dev-rqlite-pw-CHANGE-ME@localhost:4001}"

JAR="${SCRIPT_DIR}/target/spring-ai-live-arena.jar"

# Build when the jar is MISSING **or STALE**. Prefer the Maven wrapper if
# present; else system mvn. Output goes to stderr so stdout stays JSON-only.
#
# The condition used to be `[[ ! -f "$JAR" ]]` — existence only. That is not a
# freshness check: once built, the jar was never rebuilt again, so every edit to
# src/ was silently ignored and the adapter kept running whatever was compiled
# the first time. Observed 2026-07-27: a Qdrant-auth fix edited at 14:08 never
# reached a jar built four days earlier, and spring-ai went on failing RAG for a
# reason that had already been fixed in the source. Same defect shape as testing
# `-d .venv` instead of `-x .venv/bin/python3`: asking "does an artifact exist?"
# when the question is "does it match its inputs?".
needs_build=0
if [[ ! -f "${JAR}" ]]; then
  needs_build=1
elif [[ -n "$(find "${SCRIPT_DIR}/src" "${SCRIPT_DIR}/pom.xml" -newer "${JAR}" -print -quit 2>/dev/null)" ]]; then
  echo "[spring-ai/run.sh] sources newer than ${JAR##*/} — rebuilding" >&2
  needs_build=1
fi
if [[ "${needs_build}" -eq 1 ]]; then
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
