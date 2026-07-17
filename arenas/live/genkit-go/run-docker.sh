#!/usr/bin/env bash
# Dockerized live arena runner for the exhaustive genkit-go adapter.
#
# Builds the image (if needed) and runs it with stdin/stdout piped through,
# exactly like run.sh, but inside a container reaching:
#   - the real public agnes endpoint (OPENAI_BASE_URL, already a public URL —
#     no host-network trick needed for it);
#   - the real EMBED_URL / QDRANT_URL / RQLITE_URL services running on the
#     HOST machine, via host.docker.internal.
#
# The API key and every other credential are passed as RUNTIME env vars
# (`docker run -e`) sourced fresh from the repo-root .env on every invocation
# of this script — NEVER baked into the image (see Dockerfile and
# test-env.sh, which asserts this with `docker run --rm <img> env`).
#
# Usage:
#   echo '{"mode":"qa","messages":[{"role":"user","content":"..."}]}' \
#     | bash arenas/live/genkit-go/run-docker.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"
IMAGE="${GENKIT_GO_LIVE_ARENA_IMAGE:-genkit-go-live-arena:exhaustive}"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "[genkit-go/run-docker.sh] warning: ${ENV_FILE} not found; relying on inherited env" >&2
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "[genkit-go/run-docker.sh] error: OPENAI_API_KEY is not set (repo-root .env missing or incomplete)" >&2
  exit 1
fi

# Build once (Docker layer-caches the rest on unchanged source), then run.
docker build -q -t "${IMAGE}" "${SCRIPT_DIR}" 1>&2

# host.docker.internal reaches the host's localhost-bound services
# (EMBED_URL/QDRANT_URL/RQLITE_URL default to 127.0.0.1/localhost, which
# means "the container itself" from inside a container). --add-host with
# host-gateway makes host.docker.internal resolve on Linux Docker Engine too,
# not just Docker Desktop (mac/Windows), where it already works natively.
exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY="${OPENAI_API_KEY}" \
  -e OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://apihub.agnes-ai.com/v1}" \
  -e OPENAI_MODEL="${OPENAI_MODEL:-agnes-2.0-flash}" \
  -e EMBED_URL="${EMBED_URL:-http://host.docker.internal:8899}" \
  -e QDRANT_URL="${QDRANT_URL:-http://host.docker.internal:6333}" \
  -e RQLITE_URL="${RQLITE_URL:-http://host.docker.internal:4001}" \
  "${IMAGE}"
