#!/usr/bin/env bash
# Containerized run of the Dify arena's OFFLINE, LABELLED SIMULATION.
#
# HONEST SCOPE: this does NOT run real Dify. Dify has no in-process library — it
# is a Flask + Celery + Postgres + Redis multi-container platform, and its own
# modified-Apache-2.0 LICENSE §1a forbids multi-tenant operation without a
# commercial license (see ../README.md). Real Dify is SOURCE-VERIFIED, NOT
# EXECUTED, and this script does NOT stand it up or fake a server.
#
# What this DOES: builds a small Python image (docker/Dockerfile) and runs the
# arena's labelled simulation of Dify's data plane INSIDE the container — Dify's
# real multi-tenant schema transcribed into Dify's OWN ORM (SQLAlchemy 2.0),
# exercised over in-memory SQLite:
#
#   - pytest tests/                    -> 9 isolation invariants (positive control,
#                                         membership bind, the shared-schema leaks)
#   - scripts/smoke.py                 -> adversarial tenant-isolation matrix
#   - scripts/product_isolation_demo.py-> the missing 4th (product) scope axis
#
# It then asserts, on the HOST, that the containerized run passed (exit 0 + the
# expected positive-control / leak / pytest markers appear). No key, no network.
#
# SKIPS cleanly (exit 0) if docker is unavailable — it never fakes a pass.
#
# Usage:  bash arenas/dify/docker/run.sh
set -euo pipefail

IMAGE="dify-arena-sim:smoke"
CONTAINER="dify-arena-sim-smoke-$$"

# Arena root = parent of this script's dir. Docker build context is the arena.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARENA_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── docker-absent guard: SKIP, do not fake ──────────────────────────────────
NATIVE_HINT="native run: 'python scripts/smoke.py && pytest tests/ -q'"
if ! command -v docker >/dev/null 2>&1; then
  echo "SKIP: docker binary not found — containerized simulation skipped (${NATIVE_HINT})."
  exit 0
fi
if ! docker info >/dev/null 2>&1; then
  echo "SKIP: docker daemon not reachable — containerized simulation skipped (${NATIVE_HINT})."
  exit 0
fi

cleanup() {
  # Best-effort teardown; never mask the real exit code.
  docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
  docker image rm -f "${IMAGE}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "=============================================================="
echo "Dify arena — CONTAINERIZED offline SIMULATION (NOT real Dify)"
echo "=============================================================="
echo "NOTE: real Dify is a multi-container platform (Flask+Celery+PG+Redis),"
echo "      source-verified-not-executed, multi-tenant-license-restricted."
echo "      This containerizes the arena's labelled simulation of its data plane."
echo

# ── 1. Build the image (arena root as context) ──────────────────────────────
echo "[1/4] building image ${IMAGE}..."
docker build -f "${SCRIPT_DIR}/Dockerfile" -t "${IMAGE}" "${ARENA_DIR}"

IMAGE_SIZE_H="$(docker image ls "${IMAGE}" --format '{{.Size}}' | head -n1)"
echo "      image size: ${IMAGE_SIZE_H}"

# ── 2. Run the simulation suite INSIDE the container ────────────────────────
# Keyless, no network. Capture combined output so the host can assert on it.
echo "[2/4] running the simulation suite inside the container..."
OUT_FILE="$(mktemp)"
set +e
docker run --rm --name "${CONTAINER}" --network none "${IMAGE}" >"${OUT_FILE}" 2>&1
RC=$?
set -e

echo "---------------------- container output ----------------------"
cat "${OUT_FILE}"
echo "--------------------------------------------------------------"

# ── 3. Assert the containerized run passed ──────────────────────────────────
echo "[3/4] asserting the simulation passed in-container..."
[ "${RC}" -eq 0 ] || { rm -f "${OUT_FILE}"; fail "container exited ${RC} (simulation suite did not pass)"; }

# pytest: all isolation invariants green.
grep -Eq '[0-9]+ passed' "${OUT_FILE}" \
  || { rm -f "${OUT_FILE}"; fail "pytest 'N passed' line not found in container output"; }
# smoke: positive control held (the harness_ok gate).
grep -q "Positive control: acme saw its own marker" "${OUT_FILE}" \
  || { rm -f "${OUT_FILE}"; fail "smoke positive control not observed in container output"; }
# smoke: the shared-schema cross-read finding was reproduced (the labelled leak).
grep -q "shared-schema cross-read reproduced" "${OUT_FILE}" \
  || { rm -f "${OUT_FILE}"; fail "smoke shared-schema finding not reproduced in container output"; }
# product demo: the missing-4th-axis finding held.
grep -q "no product boundary exists to enforce" "${OUT_FILE}" \
  || { rm -f "${OUT_FILE}"; fail "product-axis finding not observed in container output"; }

rm -f "${OUT_FILE}"

# ── 4. Result ───────────────────────────────────────────────────────────────
echo "[4/4] cleaning up image + container..."
echo "=============================================================="
echo "PASS — the Dify arena's OFFLINE SIMULATION runs + passes in a clean,"
echo "       keyless, network-free container (image: ${IMAGE_SIZE_H})."
echo "       This is the labelled simulation of Dify's data plane, NOT real"
echo "       Dify — which is out of scope offline (multi-container platform,"
echo "       source-verified-not-executed, multi-tenant-license-restricted)."
echo "=============================================================="
