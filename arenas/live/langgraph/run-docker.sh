#!/usr/bin/env bash
# Builds the LangGraph live-arena adapter image and runs it in a container
# against the REAL agnes endpoint (public URL, reachable from inside the
# container same as from the host) and the REAL host-run embed
# server/Qdrant via host.docker.internal.
#
# Usage:
#   echo '{"mode":"qa","messages":[{"role":"user","content":"..."}]}' | bash run-docker.sh
#
# The API key is sourced from the repo-root .env and passed to the
# container ONLY as a runtime `-e` environment variable — it is never baked
# into the image, never a build ARG, and never echoed/logged by this
# script. See Dockerfile's header comment and test-env.sh for the
# automated proof.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"
IMAGE="aisotropy-arena-langgraph:latest"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "warning: ${ENV_FILE} not found; relying on inherited environment" >&2
fi

: "${OPENAI_API_KEY:?OPENAI_API_KEY must be set (source repo-root .env or export it)}"
: "${OPENAI_BASE_URL:?OPENAI_BASE_URL must be set (source repo-root .env or export it)}"
OPENAI_MODEL="${OPENAI_MODEL:-agnes-2.0-flash}"
EMBED_URL="${EMBED_URL:-http://host.docker.internal:8899}"
QDRANT_URL="${QDRANT_URL:-http://host.docker.internal:6333}"

echo "PROGRESS: docker build -t ${IMAGE} ${SCRIPT_DIR}" >&2
docker build -t "${IMAGE}" "${SCRIPT_DIR}" >&2

echo "PROGRESS: docker run ${IMAGE} (stdin piped through, stdout is the JSON envelope)" >&2
exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY \
  -e OPENAI_BASE_URL \
  -e OPENAI_MODEL \
  -e EMBED_URL="${EMBED_URL}" \
  -e QDRANT_URL="${QDRANT_URL}" \
  "${IMAGE}"
