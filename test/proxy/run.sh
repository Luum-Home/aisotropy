#!/usr/bin/env bash
# Reproducible end-to-end proof that aisotropy's SSE stream survives a reverse
# proxy through BOTH Caddy and nginx, with a buffering-nginx CONTROL to prove the
# assertion has teeth. Self-contained: spins up docker compose, asserts per-chunk
# timing, tears everything down. SKIPS cleanly if docker is unavailable.
#
# Usage:  test/proxy/run.sh
set -euo pipefail

cd "$(dirname "$0")"

if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not found on PATH — nothing to run." >&2
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker daemon not available — nothing to run." >&2
  exit 0
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "SKIP: docker compose plugin not available — nothing to run." >&2
  exit 0
fi

# The Go test (build tag `proxy`) owns compose up/down and the timing assertions.
# -count=1 defeats the test cache so the rig actually runs every invocation.
exec go test -tags proxy -run TestSSESurvivesReverseProxies -v -count=1 -timeout 300s .
