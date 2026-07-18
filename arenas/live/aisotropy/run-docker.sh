#!/usr/bin/env bash
# aisotropy-live-arena — containerized runner.
#
# Builds the image (if not already built or if --build is passed) and runs it with
# stdin piped through, exactly like run.sh's native path. The agnes API key is
# passed as a RUNTIME environment variable only — it is never baked into the image
# (see Dockerfile / test-env.sh). The real backend tiers (e5 embed server, Qdrant,
# rqlite) are reached at host.docker.internal so the container uses the SAME real
# infra the native path does; every host is overridable for a differently-wired CI.
#
# The Docker BUILD CONTEXT is the REPO ROOT because the adapter's go.mod replaces
# github.com/Luum-Home/aisotropy with ../../.. (the repo's own source).
#
# Usage:
#   echo '{"mode":"qa", ...}' | bash arenas/live/aisotropy/run-docker.sh
#   bash arenas/live/aisotropy/run-docker.sh --build   # force a rebuild first
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE="aisotropy-live-arena:local"

if [[ -z "${OPENAI_API_KEY:-}" && -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
fi

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [ -z "${!var:-}" ]; then
    echo "aisotropy-live-arena: required env var $var is not set" >&2
    exit 1
  fi
done

FORCE_BUILD=0
if [ "${1:-}" = "--build" ]; then
  FORCE_BUILD=1
fi

if [ "$FORCE_BUILD" = "1" ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "aisotropy-live-arena: building $IMAGE (context: repo root) ..." >&2
  docker build -t "$IMAGE" -f "$SCRIPT_DIR/Dockerfile" "$REPO_ROOT" >&2
fi

# host.docker.internal resolves on Docker Desktop and, via --add-host, on Linux.
# The agnes key is passed by NAME only (-e OPENAI_API_KEY) so its value is taken
# from the runtime environment, never written into the image or the command line.
#
# The backend HOSTS are forced to host.docker.internal — NOT inherited from .env
# (which points them at localhost, unreachable from inside the container). A
# CONTAINER-scoped override knob is provided for a differently-wired CI. The
# non-host knobs (model, dim, collection) DO inherit sane defaults.
docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY \
  -e OPENAI_BASE_URL \
  -e OPENAI_MODEL \
  -e AISOTROPY_EMBED_URL="${DOCKER_EMBED_URL:-http://host.docker.internal:8899}" \
  -e AISOTROPY_EMBED_MODEL="${AISOTROPY_EMBED_MODEL:-multilingual-e5-small-query}" \
  -e AISOTROPY_EMBED_DIM="${AISOTROPY_EMBED_DIM:-384}" \
  -e QDRANT_HOST="${DOCKER_QDRANT_HOST:-host.docker.internal}" \
  -e QDRANT_PORT="${QDRANT_PORT:-6334}" \
  -e QDRANT_COLLECTION="${QDRANT_COLLECTION:-aisotropy_arena}" \
  -e QDRANT_REST_URL="${DOCKER_QDRANT_REST_URL:-http://host.docker.internal:6333}" \
  -e RQLITE_URL="${DOCKER_RQLITE_URL:-http://host.docker.internal:4001/?disableClusterDiscovery=true}" \
  -e AISOTROPY_GUARDRAILS_BLOCK_INJECTION="${AISOTROPY_GUARDRAILS_BLOCK_INJECTION:-false}" \
  "$IMAGE"
