#!/usr/bin/env bash
# run-docker.sh — build (if needed) and run the LlamaIndex live-arena adapter in a container.
#
# Contract: identical to run.sh — reads ONE JSON request on stdin, writes ONE JSON response
# on stdout.
#
#   echo '{"mode":"qa","system":"...","messages":[...]}' | bash run-docker.sh
#
# Networking:
#   - agnes-2.0-flash (OPENAI_BASE_URL) is reached over its public URL — no override needed.
#   - EMBED_URL / QDRANT_URL are overridden to host.docker.internal so the container reaches
#     the host-run e5 embed server (127.0.0.1:8899) and Qdrant (localhost:6333). Override
#     EMBED_URL/QDRANT_URL in the calling shell before invoking this script to point elsewhere.
#
# The agnes API key is sourced from the repo-root .env at RUN time and passed as a `docker
# run -e` runtime env var — it is never baked into the image (see Dockerfile, test-env.sh
# assertion (b)) and never echoed/logged by this script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE_TAG="aisotropy-arena-llamaindex:latest"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "run-docker.sh: WARNING: $ENV_FILE not found — relying on already-exported env vars." >&2
fi

arena_stub="${ARENA_STUB:-}"
case "${arena_stub,,}" in
  1|true|yes) arena_stub_on=1 ;;
  *) arena_stub_on=0 ;;
esac

if [[ "$arena_stub_on" -eq 0 ]]; then
  missing=()
  for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
    if [[ -z "${!var:-}" ]]; then
      missing+=("$var")
    fi
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "run-docker.sh: FATAL: missing required env var(s): ${missing[*]}" >&2
    exit 1
  fi
else
  # ARENA_STUB=1: every backend (agnes, e5, Qdrant) is swapped for an in-process LlamaIndex
  # mock (see README.md "Offline stub toggle") -- OPENAI_*/EMBED_URL/QDRANT_URL are irrelevant
  # and this precondition must not block an offline run (known issue task_bf1b16f2).
  echo "run-docker.sh: ARENA_STUB=1 -- skipping OPENAI_* required-key precondition (offline run)" >&2
fi

echo "run-docker.sh: building image ${IMAGE_TAG}..." >&2
docker build --quiet -t "$IMAGE_TAG" "$SCRIPT_DIR" >&2

# Bind-mount the SAME host .memory/ directory run.sh's native form persists to (gitignored,
# see .gitignore) at the container's default LLAMAINDEX_MEMORY_DIR (/app/.memory — SCRIPT_DIR
# is /app inside the image, adapter.py's own default). Without this, each `docker run --rm`
# is a throwaway filesystem: a `memory`/`memory_scoped`/`isolation_leak`/`crash_recovery`
# invocation in one container can never be recalled by a SEPARATE container later, which is
# exactly what cross-invocation persistence testing requires (empirically verified: memory
# mode's second-turn recall silently failed under docker before this mount was added).
mkdir -p "$SCRIPT_DIR/.memory"

exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY \
  -e OPENAI_BASE_URL \
  -e OPENAI_MODEL \
  -e EMBED_URL="${EMBED_URL:-http://host.docker.internal:8899}" \
  -e QDRANT_URL="${QDRANT_URL:-http://host.docker.internal:6333}" \
  -e ARENA_STUB="${ARENA_STUB:-}" \
  -v "$SCRIPT_DIR/.memory:/app/.memory" \
  "$IMAGE_TAG"
