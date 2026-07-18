#!/usr/bin/env bash
# Build (if needed) and run the containerized Mastra arena adapter.
#
# Sources the repo-root .env for the agnes-2.0-flash OPENAI_* wiring, same as
# native run.sh, but passes it to the container as RUNTIME env (`docker run -e`)
# -- never baked into the image (see Dockerfile). Rewrites the local-infra host
# URLs (embed server, Qdrant, rqlite) from `localhost`/`127.0.0.1` to
# `host.docker.internal` so the containerized adapter can reach the SAME real
# services the native adapter does, unless the caller already overrode them.
#
# The Mastra `memory` mode's on-disk libsql store defaults to a container-local
# /tmp path (AISOTROPY_MASTRA_LIBSQL); that is deliberate -- cross-process libsql
# recall inside the SAME container works, but a container and the host do NOT share
# the file unless you mount one. The rqlite-backed modes (memory_scoped,
# isolation_leak, crash_recovery) DO share state across host+container because they
# hit the real networked rqlite service.
#
# Usage:
#   echo '{"mode":"qa", ...}' | bash arenas/live/mastra/run-docker.sh
#
# Env overrides (all optional, forwarded into the container):
#   EMBED_URL, QDRANT_URL, QDRANT_COLLECTION, RQLITE_URL, AISOTROPY_MASTRA_LIBSQL
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
IMAGE_TAG="aisotropy-arena-mastra:local"

# Capture any explicit override the CALLER set before invoking this script --
# these win outright over whatever .env says (explicit caller intent).
PRE_EMBED_URL="${EMBED_URL:-}"
PRE_QDRANT_URL="${QDRANT_URL:-}"
PRE_RQLITE_URL="${RQLITE_URL:-}"

set -a
# shellcheck disable=SC1091
source "$REPO_ROOT/.env"
set +a

# .env is written for NATIVE use, so EMBED_URL/QDRANT_URL/RQLITE_URL there
# typically point at localhost/127.0.0.1 -- which does not resolve to the host
# from inside the container's own network namespace. Rewrite any such host
# segment to host.docker.internal so the container reaches the SAME real
# services the native adapter does, without requiring a second config file.
dockerize_url() {
  local value="$1"
  local fallback="$2"
  if [ -z "$value" ]; then
    printf '%s' "$fallback"
    return
  fi
  printf '%s' "$value" | sed -E 's#(://)(localhost|127\.0\.0\.1)([:/]|$)#\1host.docker.internal\3#'
}

if [ -n "$PRE_EMBED_URL" ]; then EMBED_URL="$PRE_EMBED_URL"; else
  EMBED_URL="$(dockerize_url "${EMBED_URL:-}" "http://host.docker.internal:8899")"
fi
if [ -n "$PRE_QDRANT_URL" ]; then QDRANT_URL="$PRE_QDRANT_URL"; else
  QDRANT_URL="$(dockerize_url "${QDRANT_URL:-}" "http://host.docker.internal:6333")"
fi
if [ -n "$PRE_RQLITE_URL" ]; then RQLITE_URL="$PRE_RQLITE_URL"; else
  RQLITE_URL="$(dockerize_url "${RQLITE_URL:-}" "http://host.docker.internal:4001")"
fi
QDRANT_COLLECTION="${QDRANT_COLLECTION:-arena_rag}"
MASTRA_LIBSQL="${AISOTROPY_MASTRA_LIBSQL:-file:/tmp/mastra_arena_memory.db}"

# Always build (not "only if no cached image exists"): docker's own layer
# cache already makes a no-op rebuild cheap when index.mjs/lib/ are
# unchanged, but a tag-existence check alone left a stale image running old
# adapter code indefinitely once *any* image with this tag had been built
# once -- source changes must always be picked up.
echo "PROGRESS: building $IMAGE_TAG" >&2
docker build -t "$IMAGE_TAG" "$SCRIPT_DIR" >&2

# Explicit `-e VAR="$VAR"` (not bare `-e VAR`) so this does not depend on shell
# export state -- EMBED_URL/QDRANT_URL/RQLITE_URL above are plain local variables
# (deliberately reassigned after `set +a`), not exported ones.
exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e OPENAI_BASE_URL="$OPENAI_BASE_URL" \
  -e OPENAI_MODEL="$OPENAI_MODEL" \
  -e EMBED_URL="$EMBED_URL" \
  -e QDRANT_URL="$QDRANT_URL" \
  -e QDRANT_COLLECTION="$QDRANT_COLLECTION" \
  -e RQLITE_URL="$RQLITE_URL" \
  -e AISOTROPY_MASTRA_LIBSQL="$MASTRA_LIBSQL" \
  -e ARENA_STUB="${ARENA_STUB:-}" \
  "$IMAGE_TAG"
