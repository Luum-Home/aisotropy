#!/usr/bin/env bash
# Containerized runner for the Pydantic AI exhaustive multi-mode live-arena
# adapter. Builds the image on first use, then runs it with stdin/stdout
# passed through -- same contract as run.sh, just inside a container.
#
# The agnes credential is sourced from the repo-root .env and passed at
# RUNTIME ONLY via `docker run -e VAR` (no `=value`, so the value is taken
# from this shell's exported environment by the docker CLI itself, never
# embedded in the `docker run` argv or in any image layer). It is never
# echoed by this script.
#
# EMBED_URL / QDRANT_URL / RQLITE_URL default to the host's localhost
# services reached via `host.docker.internal` (Docker Desktop resolves this
# natively on macOS/Windows; `--add-host` below makes it work on Linux too).
# Override with EMBED_URL_DOCKER / QDRANT_URL_DOCKER / RQLITE_URL_DOCKER if
# your local services run on non-default ports.
#
# Usage:
#   echo '{"mode":"qa","messages":[{"role":"user","content":"hi"}]}' | bash run-docker.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE_TAG="${IMAGE_TAG:-pydantic-ai-live-arena:latest}"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "PROGRESS: warning: no .env found at $ENV_FILE — relying on already-exported environment" >&2
fi

: "${OPENAI_API_KEY:?OPENAI_API_KEY must be set (source repo-root .env or export it)}"
: "${OPENAI_BASE_URL:?OPENAI_BASE_URL must be set (source repo-root .env or export it)}"
: "${OPENAI_MODEL:?OPENAI_MODEL must be set (source repo-root .env or export it)}"

EMBED_URL_DOCKER="${EMBED_URL_DOCKER:-http://host.docker.internal:8899}"
QDRANT_URL_DOCKER="${QDRANT_URL_DOCKER:-http://host.docker.internal:6333}"
RQLITE_URL_DOCKER="${RQLITE_URL_DOCKER:-http://host.docker.internal:4001}"

if ! docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "PROGRESS: building $IMAGE_TAG (first use)..." >&2
  docker build -t "$IMAGE_TAG" "$SCRIPT_DIR" >&2
fi

exec docker run --rm -i \
  --add-host=host.docker.internal:host-gateway \
  -e OPENAI_API_KEY \
  -e OPENAI_BASE_URL \
  -e OPENAI_MODEL \
  -e EMBED_URL="$EMBED_URL_DOCKER" \
  -e QDRANT_URL="$QDRANT_URL_DOCKER" \
  -e RQLITE_URL="$RQLITE_URL_DOCKER" \
  "$IMAGE_TAG"
