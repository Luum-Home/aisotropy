#!/usr/bin/env bash
# Eino arena — CONTAINERIZED smoke test (additive; native tests stay in ./scripts + ./orbit).
#
# Proves the framework's minimal app works packaged as a production Go image:
#   1. builds arenas/eino/docker/Dockerfile (multi-stage -> distroless static),
#   2. runs the container publishing :4113,
#   3. waits for GET /healthz,
#   4. drives the arena's tenant-isolation assertion against the CONTAINERIZED
#      server over HTTP:
#        - seed secret-A as tenant A (path tenant_id=acme), read it back  -> MUST see it
#        - cross-tenant read: same session_id as tenant B (globex)         -> MUST NOT see it
#      (the server composes the working-memory key from the path tenant_id, so
#       distinct tenants stay isolated — the "correct path" in the arena README),
#   5. asserts, prints the image size, and cleans up (container always; image
#      unless KEEP_IMAGE=1).
#
# SKIPS cleanly (exit 0) if docker is unavailable — it never fakes a pass.
#
# Usage:  bash arenas/eino/docker/run.sh
# Env:    HOST_PORT=4113  IMAGE=eino-arena-smoke:local  KEEP_IMAGE=0
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"   # module root = build context
IMAGE="${IMAGE:-eino-arena-smoke:local}"
CONTAINER="eino-arena-smoke-$$"
HOST_PORT="${HOST_PORT:-4113}"
KEEP_IMAGE="${KEEP_IMAGE:-0}"
BASE="http://127.0.0.1:${HOST_PORT}"

# ---- graceful skip if docker is absent / daemon down ----
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker binary not found — containerized smoke skipped (native tests: 'go test ./...')."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker daemon not reachable — containerized smoke skipped (native tests: 'go test ./...')."
  exit 0
fi

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  if [ "$KEEP_IMAGE" != "1" ]; then
    docker image rm "$IMAGE" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== [1/5] build image ($IMAGE) from docker/Dockerfile =="
docker build -f "$SCRIPT_DIR/Dockerfile" -t "$IMAGE" "$ARENA_DIR"

echo "== [2/5] run container (publish ${HOST_PORT}->4113) =="
docker run -d --name "$CONTAINER" -p "${HOST_PORT}:4113" "$IMAGE" >/dev/null

echo "== [3/5] wait for readiness (GET /healthz) =="
ready=""
for i in $(seq 1 30); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  # surface an early container crash instead of waiting the full window
  if [ -z "$(docker ps -q -f name="$CONTAINER")" ]; then
    docker logs "$CONTAINER" 2>&1 | tail -20 || true
    fail "container exited before becoming ready"
  fi
  sleep 1
done
[ -n "$ready" ] || { docker logs "$CONTAINER" 2>&1 | tail -20 || true; fail "server not ready after 30s"; }
echo "   healthz: $(curl -fsS "${BASE}/healthz")"

echo "== [4/5] drive isolation smoke over HTTP (secret-A as tenant A; cross-tenant read must miss) =="
SECRET="secret-A-$$-$(date +%s)"
SESSION="shared-session"

# Seed: tenant A (acme) stores the secret as a working-memory note.
seed=$(curl -fsS -X POST "${BASE}/v1/tenant/acme/user/alice/chat" \
  -H 'Content-Type: application/json' \
  -d "{\"prompt\":\"remember my secret\",\"session_id\":\"${SESSION}\",\"note\":\"${SECRET}\"}")
echo "   seed(acme): $seed"

# Read back as tenant A — MUST surface the secret (proves working memory is live in-container).
readA=$(curl -fsS -X POST "${BASE}/v1/tenant/acme/user/alice/chat" \
  -H 'Content-Type: application/json' \
  -d "{\"prompt\":\"what do you know about me?\",\"session_id\":\"${SESSION}\"}")
echo "   read(acme): $readA"
case "$readA" in
  *"$SECRET"*) : ;;  # expected
  *) fail "tenant A did not see its own secret in-container (working memory broken)" ;;
esac

# Cross-tenant read as tenant B (globex), SAME session_id — MUST NOT surface the secret.
readB=$(curl -fsS -X POST "${BASE}/v1/tenant/globex/user/alice/chat" \
  -H 'Content-Type: application/json' \
  -d "{\"prompt\":\"what do you know about me?\",\"session_id\":\"${SESSION}\"}")
echo "   read(globex): $readB"
case "$readB" in
  *"$SECRET"*) fail "ISOLATION LEAK: tenant B read tenant A's secret across the container HTTP boundary" ;;
  *) : ;;  # expected: miss
esac

echo "== [5/5] result =="
SIZE=$(docker image inspect "$IMAGE" --format '{{.Size}}' 2>/dev/null || echo 0)
SIZE_H=$(docker image ls "$IMAGE" --format '{{.Size}}' 2>/dev/null | head -1)
echo "   image: $IMAGE  size: ${SIZE_H:-${SIZE} bytes}"
echo "PASS: containerized Eino server booted, served /healthz, and enforced tenant isolation over HTTP."
