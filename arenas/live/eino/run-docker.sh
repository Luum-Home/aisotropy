#!/usr/bin/env bash
# eino-live-arena — containerized runner.
#
# Builds the image (if not already built or if --build is passed) and runs
# it with stdin piped through, exactly like run.sh's native path. The agnes
# API key is passed as a RUNTIME environment variable only — it is never
# baked into the image (see Dockerfile). EMBED_URL and QDRANT_URL default to
# host.docker.internal so the container reaches the REAL e5 embed server and
# REAL Qdrant already running on the host.
#
# Usage:
#   echo '{"mode":"qa", ...}' | bash arenas/live/eino/run-docker.sh
#   bash arenas/live/eino/run-docker.sh --build   # force a rebuild first
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE="eino-live-arena:local"

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "eino-live-arena: warning: $ENV_FILE not found — relying on already-exported env vars" >&2
fi

if [ "${ARENA_STUB:-}" != "1" ]; then
  for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
    if [ -z "${!var:-}" ]; then
      echo "eino-live-arena: required env var $var is not set" >&2
      exit 1
    fi
  done
fi

FORCE_BUILD=0
if [ "${1:-}" = "--build" ]; then
  FORCE_BUILD=1
fi

if [ "$FORCE_BUILD" = "1" ] || ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "eino-live-arena: building $IMAGE ..." >&2
  docker build -t "$IMAGE" "$SCRIPT_DIR" >&2
fi

# host.docker.internal resolves on Docker Desktop (macOS/Windows) and on
# Linux via --add-host below; EMBED_URL/QDRANT_URL stay overridable so a CI
# runner with different networking can point elsewhere.
docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY \
  -e OPENAI_BASE_URL \
  -e OPENAI_MODEL \
  -e ARENA_STUB="${ARENA_STUB:-}" \
  -e EMBED_URL="${EMBED_URL:-http://host.docker.internal:8899}" \
  -e QDRANT_URL="${QDRANT_URL:-http://host.docker.internal:6333}" \
  -e QDRANT_GRPC_PORT="${QDRANT_GRPC_PORT:-6334}" \
  -e QDRANT_COLLECTION="${QDRANT_COLLECTION:-arena_rag}" \
  "$IMAGE"
