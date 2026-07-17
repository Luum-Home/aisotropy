#!/usr/bin/env bash
# Docker smoke driver for the eve arena — ADDITIVE to the native run.
#
# Closed loop:
#   1. builds the image (real `npm install` + `eve build`),
#   2. runs the container (compiled Nitro server + offline mock model on :4117),
#   3. waits for readiness on the host-published GET /eve/v1/health boundary,
#   4. drives the arena's isolation/product smoke (../scripts/smoke.mjs, via the
#      typed `eve/client`) INSIDE the container against that same server,
#   5. asserts 6/6 checks incl. the (tenant,user,product) partition story:
#        - correct composite key isolates products (positive control + partition),
#        - the naive key that drops the product segment collides (documented DIY
#          footgun) — the collision IS caught and asserted, never faked,
#   6. cleans up (container removed; image removed unless KEEP_IMAGE=1).
#
# SKIPS CLEANLY (exit 0) when Docker is absent/unreachable — never fakes a pass.
#
# Env knobs:
#   HOST_PORT=4117   host port mapped to the container's :4117 (readiness probe)
#   KEEP_IMAGE=1     keep the built image after the run (faster re-runs)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE="eve-arena-smoke:local"
CONTAINER="eve-arena-smoke-$$"
HOST_PORT="${HOST_PORT:-4117}"

log()  { printf '%s\n' "$*"; }
skip() { log "SKIP: $*"; exit 0; }

# --- Docker presence gate: skip cleanly, never fake ------------------------
if ! command -v docker >/dev/null 2>&1; then
  skip "docker CLI not found — Docker smoke skipped (native path in ../README.md is unaffected)."
fi
if ! docker info >/dev/null 2>&1; then
  skip "docker daemon not reachable — Docker smoke skipped (native path unaffected)."
fi

cleanup() {
  local code=$?
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  if [ "${KEEP_IMAGE:-0}" != "1" ]; then
    docker rmi "$IMAGE" >/dev/null 2>&1 || true
  fi
  exit "$code"
}
trap cleanup EXIT INT TERM

# --- 1) Build the image ----------------------------------------------------
log "==> [1/5] docker build ($IMAGE) — real npm install + eve build"
docker build -f "$SCRIPT_DIR/Dockerfile" -t "$IMAGE" "$ARENA_DIR"

# --- 2) Run the container --------------------------------------------------
log "==> [2/5] docker run — booting the compiled server on :4117 (offline mock model)"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" -p "${HOST_PORT}:4117" "$IMAGE" >/dev/null

# --- 3) Wait for readiness on the published boundary -----------------------
log "==> [3/5] waiting for GET http://127.0.0.1:${HOST_PORT}/eve/v1/health"
health_ok=0
for i in $(seq 1 60); do
  if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    log "FAIL: container exited during boot. Last logs:"
    docker logs "$CONTAINER" 2>&1 | tail -30
    exit 1
  fi
  body="$(curl -sf "http://127.0.0.1:${HOST_PORT}/eve/v1/health" 2>/dev/null || true)"
  if printf '%s' "$body" | grep -q '"status":"ready"'; then
    log "    ready after ${i} poll(s): ${body}"
    health_ok=1
    break
  fi
  sleep 1
done
if [ "$health_ok" -ne 1 ]; then
  log "FAIL: server did not become ready within 60s. Last logs:"
  docker logs "$CONTAINER" 2>&1 | tail -30
  exit 1
fi

# The host-published probe (Docker's userland proxy) can report ready a moment
# before the container-internal loopback path the smoke actually uses is serving.
# Gate on that exact in-container path (http://127.0.0.1:4117) to avoid the race.
loop_ok=0
for i in $(seq 1 30); do
  if docker exec "$CONTAINER" node -e \
      "fetch('http://127.0.0.1:4117/eve/v1/health').then(r=>r.ok?process.exit(0):process.exit(1)).catch(()=>process.exit(1))" \
      >/dev/null 2>&1; then
    loop_ok=1
    break
  fi
  sleep 1
done
if [ "$loop_ok" -ne 1 ]; then
  log "FAIL: in-container loopback (127.0.0.1:4117) not serving within 30s. Last logs:"
  docker logs "$CONTAINER" 2>&1 | tail -30
  exit 1
fi

# --- 4) Drive the smoke INSIDE the container against that server -----------
log "==> [4/5] driving ../scripts/smoke.mjs inside the container (eve/client on Node 24)"
set +e
SMOKE_OUT="$(docker exec -e EVE_HOST=http://127.0.0.1:4117 "$CONTAINER" node scripts/smoke.mjs 2>&1)"
SMOKE_CODE=$?
set -e
printf '%s\n' "$SMOKE_OUT"

# --- 5) Assert the closed loop ---------------------------------------------
log "==> [5/5] asserting smoke result"
fail=0
if [ "$SMOKE_CODE" -ne 0 ]; then
  log "ASSERT-FAIL: smoke exited non-zero ($SMOKE_CODE)"; fail=1
fi
if printf '%s' "$SMOKE_OUT" | grep -q '^FAIL'; then
  log "ASSERT-FAIL: smoke printed a FAIL line"; fail=1
fi
# 6 checks total, all PASS (positive control + product-partition + naive collision).
if ! printf '%s' "$SMOKE_OUT" | grep -q 'checks=6'; then
  log "ASSERT-FAIL: expected 'checks=6' not found"; fail=1
fi
# The naive-key product collision MUST be caught (the isolation/product story).
if ! printf '%s' "$SMOKE_OUT" | grep -q 'naive_key_collisions=1'; then
  log "ASSERT-FAIL: expected 'naive_key_collisions=1' (the caught DIY footgun) not found"; fail=1
fi
# The correct composite key MUST isolate products (positive control + partition).
if ! printf '%s' "$SMOKE_OUT" | grep -q '^PASS  correct_key.product_partitioned'; then
  log "ASSERT-FAIL: expected correct-key product partition to PASS"; fail=1
fi
PASS_COUNT="$(printf '%s' "$SMOKE_OUT" | grep -c '^PASS' || true)"

if [ "$fail" -ne 0 ]; then
  log ""
  log "RESULT: DOCKER SMOKE FAILED (${PASS_COUNT}/6 PASS)"
  exit 1
fi

# On-disk footprint (Docker's DISK USAGE), the honest number a puller pays.
IMG_HUMAN="$(docker images "$IMAGE" --format '{{.Size}}' 2>/dev/null | head -1)"
log ""
log "RESULT: DOCKER SMOKE PASSED — ${PASS_COUNT}/6 checks against the containerized server."
log "        image=${IMAGE} on-disk=${IMG_HUMAN:-unknown}  (native run in ../README.md unchanged)."
