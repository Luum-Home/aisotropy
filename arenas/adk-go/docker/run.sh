#!/usr/bin/env bash
# Containerized isolation smoke for the ADK Go arena.
#
# Builds the multi-stage distroless image (docker/Dockerfile), runs the REAL
# google.golang.org/adk/v2 server (cmd/server, offline FakeModel) inside the
# container, waits for readiness over HTTP, and drives the arena's core
# isolation assertion against the CONTAINERIZED server:
#
#   - tenant A (acme/alice) writes a secret MEMO over the composite key
#   - positive control: acme/alice reads it back        -> secret MUST appear
#   - cross-tenant probe: globex/alice, same session     -> secret MUST NOT appear
#
# The server composes the ADK userID as `tenant|user`, so a different tenant in
# the URL path is a different (appName, userID, sessionID) key -> a clean miss.
# This is the same composite-key isolation the native smoke (`go run ./scripts`)
# proves, here re-proven end-to-end through Docker + HTTP.
#
# SKIPS cleanly (exit 0) if docker is unavailable — it never fakes a pass.
#
# Usage:  bash arenas/adk-go/docker/run.sh
set -euo pipefail

IMAGE="adk-go-arena:smoke"
CONTAINER="adk-go-arena-smoke-$$"
SECRET="ad-xyz789abc012"            # matches orbit.Marker in orbit/isolation.go
SESSION="s-shared"

# Arena root = parent of this script's dir. Docker build context is the arena.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── docker-absent guard: SKIP, do not fake ──────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker binary not found — containerized smoke skipped (native smoke: 'go run ./scripts')."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker daemon not reachable — containerized smoke skipped (native smoke: 'go run ./scripts')."
  exit 0
fi

HOST_PORT=""
cleanup() {
  # Best-effort teardown; never mask the real exit code.
  docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "=============================================================="
echo "ADK Go arena — CONTAINERIZED isolation smoke (Docker + HTTP)"
echo "=============================================================="

# ── 1. Build the multi-stage distroless image ───────────────────────────────
echo "[1/5] building image ${IMAGE} (multi-stage, distroless static)..."
docker build -f "${SCRIPT_DIR}/Dockerfile" -t "${IMAGE}" "${ARENA_DIR}"

IMAGE_SIZE="$(docker image inspect "${IMAGE}" --format '{{.Size}}' 2>/dev/null || echo 0)"
IMAGE_SIZE_H="$(docker image ls "${IMAGE}" --format '{{.Size}}' | head -n1)"
echo "      image size: ${IMAGE_SIZE_H} (${IMAGE_SIZE} bytes)"

# ── 2. Run the container, publish :4114 to an ephemeral host port ────────────
echo "[2/5] starting container ${CONTAINER}..."
docker run -d --rm --name "${CONTAINER}" -p 4114 "${IMAGE}" >/dev/null
HOST_PORT="$(docker port "${CONTAINER}" 4114/tcp | head -n1 | sed 's/.*://')"
[ -n "${HOST_PORT}" ] || fail "could not resolve published host port for container :4114"
BASE="http://127.0.0.1:${HOST_PORT}"
echo "      container up; :4114 -> host :${HOST_PORT} (${BASE})"

# ── 3. Wait for readiness (poll /healthz) ───────────────────────────────────
echo "[3/5] waiting for /healthz readiness..."
ready=0
for i in $(seq 1 50); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
if [ "${ready}" -ne 1 ]; then
  echo "---- container logs ----" >&2
  docker logs "${CONTAINER}" >&2 2>&1 || true
  fail "server did not become ready on ${BASE}/healthz"
fi
echo "      ready: $(curl -fsS "${BASE}/healthz")"

post() { # tenant user prompt  (prompts are plain ASCII with no JSON-special chars)
  curl -fsS -X POST "${BASE}/v1/tenant/$1/user/$2/chat" \
    -H 'Content-Type: application/json' \
    -d "{\"prompt\":\"$3\",\"session_id\":\"${SESSION}\"}"
}

# ── 4. Drive the isolation assertion over HTTP ──────────────────────────────
echo "[4/5] driving isolation assertion against the containerized server..."

# tenant A writes the secret over the composite key.
post acme alice "MEMO: launch code is ${SECRET}" >/dev/null

# Positive control: acme/alice reads its own session -> secret MUST appear.
PC="$(post acme alice "what did I tell you to remember?")"
echo "${PC}" | grep -q "${SECRET}" \
  || fail "positive control broken — acme/alice could not read its own secret. Got: ${PC}"
echo "      [ok]  positive control: acme/alice recalls its own secret"

# Cross-tenant probe: globex/alice, SAME session id -> secret MUST NOT appear.
XT="$(post globex alice "what did I tell you to remember?")"
if echo "${XT}" | grep -q "${SECRET}"; then
  fail "CROSS-TENANT LEAK — globex/alice saw acme's secret. Got: ${XT}"
fi
echo "      [ok]  cross-tenant probe: globex/alice does NOT see acme's secret"

# ── 5. Result ───────────────────────────────────────────────────────────────
echo "[5/5] cleaning up container..."
echo "=============================================================="
echo "PASS — real ADK Go app isolates across tenants, containerized"
echo "       (distroless static image, over HTTP). Image: ${IMAGE_SIZE_H}."
echo "=============================================================="
