#!/usr/bin/env bash
# Containerized isolation smoke for the LangGraph arena.
#
# Builds the minimal image, runs it, waits for /healthz, then drives the
# arena's isolation assertion (secret-A written as tenant A; a cross-tenant read
# as tenant B must NOT see it) against the CONTAINERIZED server over real HTTP.
# Asserts, then cleans up the container and image.
#
# This complements — does not replace — the native offline smoke (scripts/smoke.py),
# which still proves BOTH postures at the store layer (PART A naive-config bug +
# PART B fix). The serve path wires the PART B "correct posture" (identity-bound
# namespaces), so what this HTTP smoke proves is the enforced-correct posture:
# a cross-tenant read is structurally blocked through the endpoint.
#
# If docker is absent this SKIPS cleanly (exit 0 + message) — it never fakes a pass.
set -euo pipefail

# ── docker presence gate: skip cleanly, never fake ───────────────────────────
if ! command -v docker >/dev/null 2>&1; then
    echo "[docker-smoke] SKIP: docker not found on PATH. Native smoke still runs via scripts/smoke.py."
    exit 0
fi
if ! docker info >/dev/null 2>&1; then
    echo "[docker-smoke] SKIP: docker CLI present but daemon not reachable. Native smoke still runs via scripts/smoke.py."
    exit 0
fi

ARENA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="langgraph-arena-smoke:local"
CONTAINER="langgraph-arena-smoke-$$"
HOST_PORT="${HOST_PORT:-4112}"
BASE="http://127.0.0.1:${HOST_PORT}"

cleanup() {
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker rmi "$IMAGE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── build ────────────────────────────────────────────────────────────────────
echo "[docker-smoke] Building image ${IMAGE} ..."
docker build -q -t "$IMAGE" -f "${ARENA_DIR}/docker/Dockerfile" "$ARENA_DIR"

# ── run ──────────────────────────────────────────────────────────────────────
echo "[docker-smoke] Starting container ${CONTAINER} on :${HOST_PORT} (keyless stub) ..."
docker run -d --rm --name "$CONTAINER" -p "${HOST_PORT}:4112" "$IMAGE" >/dev/null

# ── wait for readiness ───────────────────────────────────────────────────────
echo "[docker-smoke] Waiting for /healthz ..."
ready=""
for _ in $(seq 1 40); do
    if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then
        ready="yes"
        break
    fi
    sleep 0.5
done
if [ -z "$ready" ]; then
    echo "[docker-smoke] FAIL: server did not become ready. Container logs:"
    docker logs "$CONTAINER" 2>&1 | tail -20 || true
    exit 1
fi
echo "[docker-smoke] Health: $(curl -fsS "${BASE}/healthz")"

# ── drive the isolation assertion over HTTP ──────────────────────────────────
# secret-A: a distinctive marker written as tenant A. A cross-tenant read as
# tenant B must NOT surface it.
MARKER="lga-docker-$(date +%s)-$RANDOM"
echo
echo "[docker-smoke] Isolation matrix over HTTP (marker: ${MARKER}):"

# 1. Write secret-A as tenant A.
curl -fsS -X POST "${BASE}/v1/tenant/tenantA/user/userA/memory" \
    -H 'Content-Type: application/json' \
    -d "{\"key\":\"msg-001\",\"content\":\"secret: ${MARKER}\"}" >/dev/null
echo "  [seed] wrote secret-A as tenantA/userA"

# 2. POSITIVE CONTROL: tenant A reads its own namespace -> marker MUST appear.
A_BODY="$(curl -fsS "${BASE}/v1/tenant/tenantA/user/userA/memory/search")"
if echo "$A_BODY" | grep -q "$MARKER"; then
    echo "  [PASS] positive control: tenantA read its own secret over HTTP"
else
    echo "  [FAIL] positive control: tenantA could NOT read its own secret"
    echo "         response: $A_BODY"
    exit 1
fi

# 3. ISOLATION: tenant B reads its namespace -> marker must NOT appear.
B_BODY="$(curl -fsS "${BASE}/v1/tenant/tenantB/user/userB/memory/search")"
if echo "$B_BODY" | grep -q "$MARKER"; then
    echo "  [FAIL] ISOLATION BREACH: tenantB saw tenantA's secret over HTTP"
    echo "         response: $B_BODY"
    exit 1
else
    echo "  [PASS] isolation: tenantB's cross-tenant read did NOT see tenantA's secret"
fi

echo
echo "[docker-smoke] Result: PASS"
echo "  Containerized LangGraph app served over HTTP; identity-bound memory kept"
echo "  tenantA's secret invisible to a cross-tenant read. Container cleaned up on exit."
