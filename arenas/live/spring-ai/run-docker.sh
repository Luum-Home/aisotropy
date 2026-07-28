#!/usr/bin/env bash
# Dockerized live-arena runner for the Spring AI adapter.
#
# Builds the image (if needed) and runs it with stdin/stdout piped through,
# exactly like run.sh, but inside a container reaching:
#   - the real public agnes endpoint (OPENAI_BASE_URL, already a public URL);
#   - the real EMBED_URL / QDRANT_URL / RQLITE_URL services on the HOST, via
#     host.docker.internal.
#
# The API key and every other credential are passed as RUNTIME env vars
# (`docker run -e`) sourced fresh from the repo-root .env — NEVER baked into
# the image (see Dockerfile).
#
# Usage:
#   echo '{"mode":"qa","messages":[{"role":"user","content":"..."}]}' \
#     | bash arenas/live/spring-ai/run-docker.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"
# The docker path carried the SAME defects the native path did, for the same
# reason: nothing ever ran it. Fixed here, all found on the first real attempt —
#   * OPENAI_MODEL defaulted to a vendor model id (the fourth surviving instance
#     of that default, after Adapter.java, genkit-go/main.go and provider_client)
#   * QDRANT_API_KEY was never forwarded, so every containerised retrieval failed
#     Unauthenticated
#   * RQLITE_URL carried no credential, so memory modes got HTTP 401
#   * ARENA_STUB_MODEL was not forwarded, so the integration lane could not reach
#     a container at all
IMAGE="${SPRING_AI_LIVE_ARENA_IMAGE:-spring-ai-live-arena:latest}"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "[spring-ai/run-docker.sh] warning: ${ENV_FILE} not found; relying on inherited env" >&2
fi

if [[ "${ARENA_STUB:-}" != "1" && -z "${OPENAI_API_KEY:-}" ]]; then
  echo "[spring-ai/run-docker.sh] error: OPENAI_API_KEY is not set (or set ARENA_STUB=1)" >&2
  exit 1
fi

# Build once (Docker layer-caches the Maven deps on unchanged pom).
docker build -q -t "${IMAGE}" "${SCRIPT_DIR}" 1>&2

exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY="${OPENAI_API_KEY:-}" \
  -e OPENAI_BASE_URL="${OPENAI_BASE_URL:?set OPENAI_BASE_URL — no vendor default: a hardcoded fallback silently sends prompts to one provider (see docs/architecture/model-provider.md)}" \
  -e OPENAI_MODEL="${OPENAI_MODEL:?set OPENAI_MODEL — no vendor default: a fallback model id labels every number with a model that was never called}" \
  -e EMBED_URL="${EMBED_URL:-http://host.docker.internal:8899}" \
  -e QDRANT_URL="${QDRANT_URL:-http://host.docker.internal:6333}" \
  -e RQLITE_URL="${RQLITE_URL:-http://aisotropy:local-dev-rqlite-pw-CHANGE-ME@host.docker.internal:4001}" \
  -e QDRANT_API_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}" \
  -e QDRANT_COLLECTION="${QDRANT_COLLECTION:-}" \
  -e ARENA_COLLECTION="${ARENA_COLLECTION:-}" \
  -e ARENA_STUB="${ARENA_STUB:-}" \
  -e ARENA_STUB_MODEL="${ARENA_STUB_MODEL:-}" \
  "${IMAGE}"
