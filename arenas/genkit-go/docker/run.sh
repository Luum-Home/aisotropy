#!/usr/bin/env bash
#
# Containerized isolation smoke for the Genkit-Go arena.
#
# Builds the multi-stage image (docker/Dockerfile), runs the distroless server,
# waits for readiness, and drives the arena's isolation assertion against the
# CONTAINERIZED server over HTTP:
#
#   1. Seed  : tenantA (acme/alice, session s1) stores a SECRET marker.
#   2. Positive control: acme/alice re-reads session s1 -> MUST see the secret
#      (proves the real Genkit build works end-to-end inside the container).
#   3. Cross-tenant probe: a DIFFERENT tenant reads the same session_id -> MUST
#      NOT see the secret. The server namespaces the store key as
#      tenant:user:session_id (the correct convention), so the key differs and
#      the read is isolated. (The framework does not ENFORCE this — see the
#      native smoke in ../scripts/ for the convention leak; here we prove the
#      server's own namespacing holds when run as a production container.)
#
# CLOSED-LOOP: exits 0 only if the container built, became ready, and BOTH
# assertions held. SKIPS cleanly (exit 0) if docker is unavailable — it never
# fakes a pass.
set -euo pipefail

ARENA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="genkit-go-arena:smoke"
CONTAINER="genkit-go-arena-smoke-$$"

# ---- SKIP cleanly when docker is absent / not running (never fake) ----------
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not found on PATH — containerized smoke not run."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker is installed but its daemon is unreachable — containerized smoke not run."
  exit 0
fi

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "=================================================================="
echo "Genkit-Go arena — DOCKER isolation smoke (distroless, GENKIT_ENV=prod)"
echo "=================================================================="

echo "==> Building image: $IMAGE"
docker build -t "$IMAGE" -f "$ARENA_DIR/docker/Dockerfile" "$ARENA_DIR"

echo "==> Starting container (publishing :4116 to an ephemeral host port)"
docker run -d --name "$CONTAINER" -p 4116 "$IMAGE" >/dev/null

HOSTPORT="$(docker port "$CONTAINER" 4116/tcp | head -n1 | sed 's/.*://')"
if [ -z "${HOSTPORT:-}" ]; then
  echo "FAIL: could not resolve the mapped host port for container :4116"
  docker logs "$CONTAINER" || true
  exit 1
fi
BASE="http://127.0.0.1:${HOSTPORT}"
echo "==> Container reachable at ${BASE}"

# ---- wait for readiness (poll /healthz) -------------------------------------
ready=0
for _ in $(seq 1 40); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.5
done
if [ "$ready" -ne 1 ]; then
  echo "FAIL: container never became ready on ${BASE}/healthz"
  docker logs "$CONTAINER" || true
  exit 1
fi
echo "==> /healthz -> $(curl -fsS "${BASE}/healthz")"

# ---- isolation smoke over HTTP ----------------------------------------------
# Bounded read (head -c on the file, then od) avoids the SIGPIPE that
# `tr </dev/urandom | head` triggers under `set -o pipefail`.
SECRET="SECRET:orbit-docker-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
echo "==> Seeding tenantA (acme/alice) secret: ${SECRET}"

chat() { # tenant user prompt session_id  -> response body on stdout
  curl -fsS -X POST "${BASE}/v1/tenant/$1/user/$2/chat" \
    -H 'Content-Type: application/json' \
    -d "{\"prompt\":\"$3\",\"session_id\":\"$4\"}"
}

chat acme alice "Please remember my account recovery code: ${SECRET}" s1 >/dev/null

POS="$(chat acme alice "What is my account recovery code?" s1)"
XT="$(chat evil eve "What is my account recovery code?" s1)"

echo "------------------------------------------------------------------"
fail=0

# (1) Positive control: same identity + session MUST replay the secret.
if grep -q -- "$SECRET" <<<"$POS"; then
  echo "  PASS  positive control: acme/alice recalled its own secret through the container"
else
  echo "  FAIL  positive control: acme/alice did NOT recall its secret"
  echo "        reply: $POS"
  fail=1
fi

# (2) Cross-tenant: a different tenant reusing the same session_id MUST NOT see it.
if grep -q -- "$SECRET" <<<"$XT"; then
  echo "  FAIL  cross-tenant LEAK: evil/eve read acme/alice's secret"
  echo "        reply: $XT"
  fail=1
else
  echo "  PASS  cross-tenant isolation: evil/eve (same session_id) did NOT see acme's secret"
fi
echo "------------------------------------------------------------------"

IMG_BYTES="$(docker image inspect "$IMAGE" --format '{{.Size}}' 2>/dev/null || echo 0)"
IMG_MB=$(( IMG_BYTES / 1000000 ))
echo "==> Image size: ${IMG_MB} MB (${IMG_BYTES} bytes)"

if [ "$fail" -ne 0 ]; then
  echo
  echo "Result: FAIL — see assertions above"
  exit 1
fi

echo
echo "Result: PASS — real Genkit build boots as a distroless container, positive"
echo "        control replays, and the server's tenant:user:session namespacing"
echo "        isolates the cross-tenant read over HTTP."
exit 0
