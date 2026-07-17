#!/usr/bin/env bash
#
# Docker smoke test for the Pydantic AI arena — deployment realism.
#
# Builds a minimal image of the orbit app (FastAPI/uvicorn on :4110, keyless
# TestModel stub), runs it as a CONTAINER, waits for HTTP readiness, then drives
# the arena's isolation/smoke assertion against the CONTAINERIZED server over
# real HTTP:
#
#   1. Tenant A writes a unique secret marker (POST /v1/tenant/A/.../chat).
#   2. POSITIVE CONTROL: Tenant A reads it back over HTTP — the marker appears.
#   3. CROSS-TENANT: Tenant B reads over HTTP — the marker must NOT appear.
#
# Isolation here is the app's `get_store()` keying on (tenant_id, user_id)
# derived from the URL PATH (FastAPI routing), so tenant B gets a separate
# store and never sees tenant A's slice. This complements the offline
# scripts/smoke.py (which shows the framework ships NO isolation LAYER — that is
# a DIY-dict property); this HTTP smoke proves the containerized app enforces
# path-scoped separation over the wire.
#
# Exit codes:
#   0  = PASS, or SKIP (docker unavailable — never a fake pass)
#   1  = FAIL (build/boot/assertion failed)
#
# Env overrides:
#   HOST_PORT=4110     host port to publish (container is always :4110)
#   KEEP_IMAGE=1       keep the built image after run (default: remove it)
#   READY_TIMEOUT=60   seconds to wait for HTTP readiness
#
set -u

# ── Locate paths (script lives in <arena>/docker/) ───────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

IMAGE_TAG="pydantic-ai-arena:smoke"
CONTAINER_NAME="pydantic-ai-arena-smoke-$$"
HOST_PORT="${HOST_PORT:-4110}"
READY_TIMEOUT="${READY_TIMEOUT:-60}"
BASE_URL="http://127.0.0.1:${HOST_PORT}"

say()  { printf '%s\n' "$*"; }
pass() { printf '  [PASS] %s\n' "$*"; }
fail() { printf '  [FAIL] %s\n' "$*"; }

# ── SKIP cleanly if docker is unavailable (never fake a pass) ────────────────
if ! command -v docker >/dev/null 2>&1; then
  say "SKIP: docker binary not found on PATH — cannot run the containerized smoke."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  say "SKIP: docker daemon not reachable (\`docker info\` failed) — cannot run the containerized smoke."
  exit 0
fi

# ── Cleanup on any exit ──────────────────────────────────────────────────────
cleanup() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  if [ "${KEEP_IMAGE:-0}" != "1" ]; then
    docker rmi "${IMAGE_TAG}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

say "=========================================================="
say "Pydantic AI Arena — Docker Smoke (deployment realism)"
say "=========================================================="
say "Arena dir : ${ARENA_DIR}"
say "Image     : ${IMAGE_TAG}"
say "Publish   : host ${HOST_PORT} -> container 4110"
say ""

# ── 1. Build ─────────────────────────────────────────────────────────────────
say "[1/4] Building image (first build pulls python:3.13-slim + pydantic-ai; can take a few minutes)..."
if ! docker build -f "${SCRIPT_DIR}/Dockerfile" -t "${IMAGE_TAG}" "${ARENA_DIR}"; then
  fail "docker build failed"
  exit 1
fi
pass "image built"

# ── 2. Run ───────────────────────────────────────────────────────────────────
say "[2/4] Starting container (keyless TestModel stub — no API key)..."
docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
if ! docker run -d --name "${CONTAINER_NAME}" -p "${HOST_PORT}:4110" "${IMAGE_TAG}" >/dev/null; then
  fail "docker run failed"
  exit 1
fi
pass "container started (${CONTAINER_NAME})"

# ── 3. Wait for HTTP readiness ───────────────────────────────────────────────
say "[3/4] Waiting for HTTP readiness at ${BASE_URL}/healthz (timeout ${READY_TIMEOUT}s)..."
ready=0
for _ in $(seq 1 "${READY_TIMEOUT}"); do
  if ! docker ps -q --filter "name=${CONTAINER_NAME}" | grep -q .; then
    fail "container exited before becoming ready — logs:"
    docker logs "${CONTAINER_NAME}" 2>&1 | sed 's/^/    /'
    exit 1
  fi
  if curl -fsS "${BASE_URL}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ "${ready}" != "1" ]; then
  fail "server did not become ready in ${READY_TIMEOUT}s — logs:"
  docker logs "${CONTAINER_NAME}" 2>&1 | sed 's/^/    /'
  exit 1
fi
pass "healthz ok: $(curl -fsS "${BASE_URL}/healthz")"

# ── 4. Drive the isolation assertion over real HTTP ──────────────────────────
say "[4/4] Driving cross-tenant isolation assertion over HTTP..."
MARKER="pmalpha_$(date +%s)_${RANDOM}"
say "  seeding tenantA marker: ${MARKER}"

post_chat() { # tenant user prompt  -> echoes reply body
  local tenant="$1" user="$2" prompt="$3"
  curl -fsS -X POST \
    "${BASE_URL}/v1/tenant/${tenant}/user/${user}/chat" \
    -H 'Content-Type: application/json' \
    -d "{\"prompt\":\"${prompt}\",\"session_id\":\"s1\"}"
}

# Write secret as tenant A.
if ! write_reply="$(post_chat acme alice "store secret ${MARKER} for later")"; then
  fail "tenantA write request failed (HTTP error)"; exit 1
fi
case "${write_reply}" in
  *"${MARKER}"*) pass "tenantA write accepted; server echoed the stored marker" ;;
  *) fail "tenantA write reply did not echo the marker: ${write_reply}"; exit 1 ;;
esac

# Positive control: tenant A reads its own slice back over HTTP.
if ! a_reply="$(post_chat acme alice "what did I say earlier")"; then
  fail "tenantA read request failed (HTTP error)"; exit 1
fi
case "${a_reply}" in
  *"${MARKER}"*) pass "positive control: tenantA read its own marker over HTTP" ;;
  *) fail "positive control failed: tenantA did NOT see its own marker: ${a_reply}"; exit 1 ;;
esac

# Cross-tenant read: tenant B must NOT see tenant A's marker.
if ! b_reply="$(post_chat evilcorp bob "what did I say earlier")"; then
  fail "tenantB read request failed (HTTP error)"; exit 1
fi
case "${b_reply}" in
  *"${MARKER}"*)
    fail "ISOLATION BREACH: tenantB saw tenantA's marker over HTTP: ${b_reply}"
    exit 1 ;;
  *)
    pass "cross-tenant: tenantB did NOT see tenantA's marker (path-scoped store held)" ;;
esac

say ""
say "----------------------------------------------------------"
say "Result: PASS"
say "  The containerized orbit app booted, served HTTP, and enforced"
say "  path-scoped tenant separation: tenantA's secret was readable by"
say "  tenantA and invisible to tenantB over the wire. Cleaning up."
say "----------------------------------------------------------"
exit 0
