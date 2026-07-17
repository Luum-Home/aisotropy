#!/usr/bin/env bash
# Containerized Cloudflare Agents arena smoke — CLOSED LOOP.
#
# Builds the image (docker/Dockerfile), boots `wrangler dev` (which embeds real
# `workerd` + Durable Objects) inside the container, then drives the SAME per-DO
# isolation smoke the native `pnpm dev` path proves — but over HTTP against the
# containerized instance — and asserts every response. Cleans up on any exit.
#
# HONEST SCOPE: this containerizes the LOCAL-FIDELITY runtime (workerd/miniflare),
# which is exactly what the native smoke uses. It does NOT — and cannot — reproduce
# Cloudflare's production edge: global DO uniqueness + rented durability live only
# on Cloudflare's platform (README "Sovereignty caveat"). What this proves is that
# the real DO-backed HTTP endpoint + per-object SQLite isolation run reproducibly
# inside a container, not just on the host.
#
# SKIPS cleanly (exit 0) if docker is unavailable — matches the arena's other
# optional tooling conventions.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE="orbit-cf-arena:smoke"
CONTAINER="orbit-cf-arena-smoke-$$"
HOST_PORT="${ORBIT_HOST_PORT:-18787}"
BASE="http://127.0.0.1:${HOST_PORT}"

# ---- SKIP if docker is absent (not a failure) -----------------------------
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker not found on PATH — containerized smoke skipped."
  echo "      (Native path unaffected: run 'pnpm smoke' / 'pnpm dev'.)"
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker present but daemon not reachable — containerized smoke skipped."
  exit 0
fi

# ---- cleanup on ANY exit ---------------------------------------------------
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

FAIL=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1"; FAIL=1; }

# ---- build -----------------------------------------------------------------
echo "==> Building $IMAGE ..."
DOCKER_BUILDKIT=1 docker build -f "$SCRIPT_DIR/Dockerfile" -t "$IMAGE" "$ARENA_DIR"

# ---- run --------------------------------------------------------------------
echo "==> Starting container $CONTAINER on host port $HOST_PORT ..."
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" -p "${HOST_PORT}:8787" "$IMAGE" >/dev/null

# ---- wait for workerd to boot the Worker (/healthz answers) ----------------
echo "==> Waiting for the DO-backed endpoint to come up ..."
READY=0
for i in $(seq 1 40); do
  if [ "$(curl -fsS -o /dev/null -w '%{http_code}' "$BASE/healthz" 2>/dev/null)" = "200" ]; then
    READY=1
    echo "    ready after ${i}s"
    break
  fi
  sleep 1
done
if [ "$READY" != "1" ]; then
  echo "FAIL: endpoint never became ready. Container logs:"
  docker logs "$CONTAINER" 2>&1 | tail -30
  exit 1
fi

# ---- drive the per-DO isolation smoke over HTTP ----------------------------
# Helpers: POST a chat turn and extract a JSON numeric/string field with a
# grep/sed pair (no jq dependency on the host).
post_chat() { # scope_path  json_body  [auth_header]
  local path="$1" body="$2" auth="${3:-}"
  if [ -n "$auth" ]; then
    curl -s -X POST "$BASE$path" -H "Authorization: $auth" \
      -H 'Content-Type: application/json' -d "$body"
  else
    curl -s -X POST "$BASE$path" -H 'Content-Type: application/json' -d "$body"
  fi
}
post_code() { # scope_path  json_body  auth_header
  local path="$1" body="$2" auth="$3"
  curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE$path" \
    -H "Authorization: $auth" -H 'Content-Type: application/json' -d "$body"
}
field_num() { echo "$1" | sed -n 's/.*"'"$2"'":\([0-9][0-9]*\).*/\1/p'; }

echo "==> Driving per-DO isolation smoke against the container ..."

# PART A — per-object working memory persists in THIS DO's SQLite.
r1="$(post_chat /v1/tenant/acme/user/alice/product/productX/chat '{"prompt":"first productX"}')"
[ "$(field_num "$r1" hydrated)" = "0" ] && pass "productX turn 1 -> hydrated:0" \
  || fail "productX turn 1 hydrated expected 0, got: $r1"

r2="$(post_chat /v1/tenant/acme/user/alice/product/productX/chat '{"prompt":"second productX"}')"
[ "$(field_num "$r2" hydrated)" = "2" ] && pass "productX turn 2 -> hydrated:2 (per-object memory persisted)" \
  || fail "productX turn 2 hydrated expected 2, got: $r2"

# PART A — a DIFFERENT product is a DIFFERENT object: its own empty SQLite.
ry="$(post_chat /v1/tenant/acme/user/alice/product/productY/chat '{"prompt":"hi productY"}')"
[ "$(field_num "$ry" hydrated)" = "0" ] && pass "productY (different object) -> hydrated:0 (isolated)" \
  || fail "productY hydrated expected 0, got: $ry"

# PART A — a DIFFERENT tenant is a DIFFERENT object.
rg="$(post_chat /v1/tenant/globex/user/alice/product/productX/chat '{"prompt":"hi globex"}')"
[ "$(field_num "$rg" hydrated)" = "0" ] && pass "globex/productX (different tenant) -> hydrated:0 (isolated)" \
  || fail "globex hydrated expected 0, got: $rg"

# Sanity: productY must not carry productX's identity/text (no cross-object leak).
if echo "$ry" | grep -q '"identity":"acme|alice|productY"' && ! echo "$ry" | grep -q 'productX'; then
  pass "productY response has its own identity, no productX bleed-through"
else
  fail "productY response leaked productX content: $ry"
fi

# PART C — strong must-agree bind: token authorizes productX, caller asks productY.
c403="$(post_code /v1/tenant/acme/user/alice/product/productY/chat '{"prompt":"hi"}' 'Bearer tok:acme|alice|productX')"
[ "$c403" = "403" ] && pass "must-agree bind rejects wrong-product token -> HTTP 403" \
  || fail "expected 403 on scope mismatch, got HTTP $c403"

# PART C — self-consistent verified request resolves to its own object.
c200="$(post_code /v1/tenant/acme/user/alice/product/productX/chat '{"prompt":"hi"}' 'Bearer tok:acme|alice|productX')"
[ "$c200" = "200" ] && pass "self-consistent verified request -> HTTP 200" \
  || fail "expected 200 on matching scope, got HTTP $c200"

# ---- verdict ----------------------------------------------------------------
echo
if [ "$FAIL" = "0" ]; then
  echo "CONTAINERIZED DO SMOKE: PASS — real workerd + Durable Objects + per-object"
  echo "SQLite isolation verified over HTTP against the container."
  exit 0
else
  echo "CONTAINERIZED DO SMOKE: FAIL — see failures above. Container logs:"
  docker logs "$CONTAINER" 2>&1 | tail -30
  exit 1
fi
