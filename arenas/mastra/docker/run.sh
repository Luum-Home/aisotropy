#!/usr/bin/env bash
# Mastra arena — CONTAINERIZED isolation smoke.
#
# Builds a minimal node image running the arena's server (mock model, no API key,
# real @mastra/memory), boots it, waits for readiness, and drives the arena's
# isolation assertion against the CONTAINERIZED server over HTTP:
#
#   secret-A stored as tenant A  ->  tenant A recall MUST see it (positive control)
#                                ->  a cross-tenant recall MUST NOT see it (isolation)
#
# This is the HTTP-served analogue of scripts/smoke.js / product_isolation_demo.ts:
# the real framework's thread<->resource ownership guard refuses the cross-tenant
# read, so tenant B cannot observe tenant A's secret.
#
# SKIPS CLEANLY (exit 0, no fake pass) if docker is unavailable.
# Closed-loop: build -> boot -> assert -> clean up. Exit 0 = pass or clean skip.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="mastra-arena-smoke"
CONTAINER="mastra-arena-smoke-run"
HOST_PORT="${HOST_PORT:-4111}"
BASE="http://localhost:${HOST_PORT}"

log()  { printf '%s\n' "$*"; }
line() { printf '%s\n' "------------------------------------------------------------"; }

# ── docker presence gate — skip cleanly, never fake ──────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  log "SKIP: docker not found on PATH — containerized smoke skipped (not a failure)."
  log "      Run the native (non-container) smoke instead: (cd .. && npm run smoke)"
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  log "SKIP: docker is installed but the daemon is not reachable — skipping (not a failure)."
  log "      Start Docker and re-run, or use the native smoke: (cd .. && npm run smoke)"
  exit 0
fi

# ── cleanup on any exit ──────────────────────────────────────────────────────
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Start from a clean slate (a stale container from a prior aborted run).
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

line
log "Mastra arena — CONTAINERIZED isolation smoke"
line

# ── build ────────────────────────────────────────────────────────────────────
log "[1/5] docker build (context: $HERE) ..."
build_start=$(date +%s)
docker build -t "$IMAGE" "$HERE"
build_end=$(date +%s)
log "      build ok in $((build_end - build_start))s"

# ── run ──────────────────────────────────────────────────────────────────────
log "[2/5] docker run -p ${HOST_PORT}:4111 ..."
docker run -d --name "$CONTAINER" -p "${HOST_PORT}:4111" "$IMAGE" >/dev/null

# ── wait for readiness ───────────────────────────────────────────────────────
log "[3/5] waiting for /healthz ..."
ready=""
for i in $(seq 1 40); do
  if curl -fs "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  # Fail fast if the container died during boot.
  if ! docker ps --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
    log "FAIL: container exited during boot. Logs:"
    docker logs "$CONTAINER" 2>&1 | sed 's/^/      /'
    exit 1
  fi
  sleep 0.5
done
if [ -z "$ready" ]; then
  log "FAIL: server did not become ready within timeout. Logs:"
  docker logs "$CONTAINER" 2>&1 | sed 's/^/      /'
  exit 1
fi
log "      ready: $(curl -fs "${BASE}/healthz")"

# ── drive the smoke over HTTP against the container ──────────────────────────
log "[4/5] driving isolation assertion against the containerized server ..."

fail=0
assert() { # name  condition(0/1)  detail
  if [ "$2" = "1" ]; then log "  PASS  $1  $3"; else log "  FAIL  $1  $3"; fail=1; fi
}

# mock-model sanity: the minimal app serves a model response with no key.
CHAT="$(curl -fs -X POST "${BASE}/chat" -H 'Content-Type: application/json' -d '{"prompt":"hello"}')"
case "$CHAT" in *'"text"'*) chat_ok=1;; *) chat_ok=0;; esac
assert "mock_model.chat" "$chat_ok" "$CHAT"

# secret-A stored as tenant A.
SECRET="secretA_$(head -c8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
REM="$(curl -fs -X POST "${BASE}/remember" -H 'Content-Type: application/json' \
        -d "{\"tenant\":\"tenantA\",\"thread\":\"shared\",\"secret\":\"${SECRET}\"}")"
case "$REM" in *'"ok":true'*) rem_ok=1;; *) rem_ok=0;; esac
assert "remember.tenantA" "$rem_ok" "$REM"

# positive control: tenant A recall MUST see secret-A.
POS="$(curl -fs "${BASE}/recall?tenant=tenantA&thread=shared")"
case "$POS" in *"$SECRET"*) pos_ok=1;; *) pos_ok=0;; esac
assert "positive_control.tenantA_sees_own_secret" "$pos_ok" "$POS"

# ISOLATION: a cross-tenant recall MUST NOT see secret-A.
CROSS="$(curl -fs "${BASE}/recall?tenant=tenantB&thread=shared")"
case "$CROSS" in *"$SECRET"*) cross_leaked=1;; *) cross_leaked=0;; esac
if [ "$cross_leaked" = "1" ]; then
  assert "isolation.cross_tenant_read" 0 "LEAK: tenantB saw tenantA's secret -> $CROSS"
else
  assert "isolation.cross_tenant_read" 1 "tenantB did NOT see the secret -> $CROSS"
fi

# ── verdict ──────────────────────────────────────────────────────────────────
log "[5/5] verdict"
line
if [ "$fail" = "0" ]; then
  log "PASS — containerized Mastra arena served the mock model AND enforced tenant"
  log "       isolation over HTTP: tenant A read its own secret; the cross-tenant"
  log "       read was refused by Mastra's thread<->resource ownership guard."
  line
  exit 0
else
  log "FAIL — see the assertion lines above. Container logs:"
  docker logs "$CONTAINER" 2>&1 | sed 's/^/      /'
  line
  exit 1
fi
