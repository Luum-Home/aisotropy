#!/usr/bin/env bash
# run.sh — the Letta arena's OFFLINE smoke, run inside a clean Docker container.
#
# Builds the image (real letta-client==1.12.1 installed from requirements.txt, NO
# LLM key, NO Letta server), then runs the arena's adversarial isolation assertion
# INSIDE the container and asserts it passes. Cleans up the image on exit.
#
# HONESTY — read this before trusting the result:
#   This containerizes the arena's OFFLINE plane, which is a *labelled behavioral
#   simulation* of the Letta server (real SDK Pydantic types + the real, documented
#   server filter contract; a deterministic responder in place of the LLM). It is
#   NOT a live Letta server. Letta OSS is a server: agents + memory blocks +
#   archival passages are durable Postgres/SQLite objects and an agent turn calls
#   an LLM provider. A live-server containerized smoke additionally needs a running
#   `letta server` + Postgres + a provider key — out of scope for an offline,
#   keyless run and deliberately NOT faked. See ../README.md.
#
#   What this DOES prove: the arena runs end-to-end in a bare Python 3.13 container
#   (deps install cleanly, real Letta types import, the isolation matrix executes),
#   i.e. it is self-contained and reproducible, not host-venv-dependent.
#
# Docker-gated (matches this repo's honesty gate): if the Docker daemon is absent,
# print SKIP and exit 0 — CI never fails merely because Docker is missing.

set -euo pipefail

ARENA_DIR="$(cd "$(dirname "$0")/.." && pwd)"   # arenas/letta (build context root)
IMAGE="letta-arena:smoke-$$"                    # unique tag; removed on exit

cleanup() {
  docker image rm -f "$IMAGE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== Letta arena — offline smoke in Docker =="

if ! docker version >/dev/null 2>&1; then
  echo "SKIP: docker daemon unavailable (honesty gate) — nothing proven, exit 0."
  exit 0
fi

echo
echo "-- build: $IMAGE (context=$ARENA_DIR, real letta-client==1.12.1, no key/server) --"
docker build -f "$ARENA_DIR/docker/Dockerfile" -t "$IMAGE" "$ARENA_DIR"

# ── Assertion 1: the offline adversarial isolation smoke (scripts/smoke.py) ──
# Contract: exit 0 = harness healthy (positive control held) and the org-wide
# under-scoping leak was demonstrated on the real SDK types. No network/key/server.
echo
echo "-- run: scripts/smoke.py inside the container (offline simulation plane) --"
SMOKE_OUT="$(docker run --rm --network=none "$IMAGE" python scripts/smoke.py)"
echo "$SMOKE_OUT"
if ! printf '%s\n' "$SMOKE_OUT" | grep -q "Positive control: acme saw its own marker"; then
  echo "FAIL: positive control did not hold inside the container." >&2
  exit 1
fi

# ── Assertion 2: the isolation assertion as an executable pytest ──
# tests/test_isolation.py encodes the thesis (correct scope clean; every leaky
# primitive IS a cross-read) as assertions against the REAL Letta types.
echo
echo "-- run: pytest tests/test_isolation.py inside the container --"
PYTEST_OUT="$(docker run --rm --network=none "$IMAGE" \
  python -m pytest tests/test_isolation.py -q)"
echo "$PYTEST_OUT"
if ! printf '%s\n' "$PYTEST_OUT" | grep -Eq "[1-9][0-9]* passed"; then
  echo "FAIL: isolation pytest did not report a green run inside the container." >&2
  exit 1
fi
if printf '%s\n' "$PYTEST_OUT" | grep -Eq "[1-9][0-9]* failed|[1-9][0-9]* error"; then
  echo "FAIL: isolation pytest reported failures/errors inside the container." >&2
  exit 1
fi

echo
echo "PASS: the Letta arena's OFFLINE isolation smoke ran green in a clean container."
echo "      (labelled server-plane simulation — NOT a live Letta server; a live"
echo "       containerized smoke needs \`letta server\` + Postgres + a provider key.)"
