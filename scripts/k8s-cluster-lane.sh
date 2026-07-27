#!/usr/bin/env bash
# k8s-cluster-lane.sh — the OPT-IN, resource-guarded REAL-CLUSTER lane.
#
# This is the expensive twin of scripts/k8s-chart-guard.sh. The guard proves the
# chart is CORRECT (renders, probes point at real endpoints, no secret leaks) in
# ~2 seconds with no cluster. This lane proves the chart actually BOOTS: it
# creates a throwaway Kind/k3d cluster, builds and side-loads the Distroless
# image, installs the chart, and curls the real /healthz + /health endpoints.
#
# IT IS OFF BY DEFAULT AND MUST STAY THAT WAY. Two independent gates:
#
#   1. AISOTROPY_K8S_CLUSTER_LANE=1 must be set explicitly. Without it this
#      script exits 0 immediately with a skip reason, so wiring it into a CI
#      step or a pre-commit chain costs nothing and starts nothing.
#   2. scripts/resource-guard.sh must say the machine can take it. A Kind node
#      plus a Go image build on a laptop already running concurrent agent
#      sessions is exactly the workload that OOM-killed test workers on
#      2026-07-23 (swap 81-94%, ~280 MB free RAM). BLOCKED (guard exit 3) is a
#      hard refusal here — no override. DEGRADED (exit 1) needs a second,
#      explicit opt-in.
#   3. An explicit --yes (or AISOTROPY_K8S_CLUSTER_LANE_YES=1). Non-interactive
#      callers that do not pass it get a printed plan and exit 0 without
#      spending anything.
#
# WHY GATE 3 EXISTS (an incident, not a hypothetical): while this script was
# being written, gate 2 was probed twice a few minutes apart. Between the two
# probes the machine's memory pressure eased and resource-guard flipped
# DEGRADED -> OK, so the "this should refuse" test instead sailed through the
# gate and began creating a Kind cluster and a Docker build — unattended and
# unintended. resource-guard behaved correctly; the lesson is that a resource
# verdict is a SNAPSHOT and must not, on its own, authorise an expensive
# irreversible action (the verify-before-execute discipline applied to this
# script itself). Consent is now separate from capacity: the machine says
# whether it CAN, a human says whether it SHOULD.
#
# WHAT IT COSTS (budget before you run it):
#   - Kind/k3d control-plane node container ......... ~0.8-1.5 GB resident
#   - `docker build` of the Go binary (peak, transient) ~2-4 GB
#   - aisotropyd pod ................................ 256 Mi limit (chart default)
#   - + rqlite 256 Mi and Qdrant 512 Mi with TIERS=networked
#   - your container engine's own VM (Docker Desktop / OrbStack / Colima) on top
#   Rule of thumb: do not start this with less than ~4 GB genuinely free.
#   The resulting image is small (55.7 MB, measured when the chart landed in
#   commit 3d39c44) — it is the BUILD and the node, not the image, that cost.
#   NOTE: these are budgeting estimates from the component defaults, not
#   measurements taken by this script. It has not been executed on this machine.
#
# USAGE:
#   scripts/k8s-cluster-lane.sh                      # skips (lane off) — safe
#   AISOTROPY_K8S_CLUSTER_LANE=1 scripts/k8s-cluster-lane.sh          # prints the plan, spends nothing
#   AISOTROPY_K8S_CLUSTER_LANE=1 scripts/k8s-cluster-lane.sh --yes    # actually runs
#   AISOTROPY_K8S_CLUSTER_LANE=1 TIERS=networked scripts/k8s-cluster-lane.sh --yes
#   AISOTROPY_K8S_CLUSTER_LANE=1 KEEP_CLUSTER=1 scripts/k8s-cluster-lane.sh --yes
#
# EXIT CODES (repo guard convention, extended with resource-guard's 3):
#   0  OK / SKIPPED-with-reason (lane off, or a prerequisite tool is absent)
#   1  FINDINGS — the cluster came up but the deployment did not become healthy
#   2  ERROR    — usage or environment error
#   3  BLOCKED  — resource-guard refused; nothing was created
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || {
  echo "k8s-cluster-lane: not in a git repo" >&2; exit 2; }

NAMESPACE="${NAMESPACE:-aisotropy}"
CLUSTER="${CLUSTER:-aisotropy}"
TIERS="${TIERS:-minimal}"
KEEP_CLUSTER="${KEEP_CLUSTER:-0}"

YES="${AISOTROPY_K8S_CLUSTER_LANE_YES:-0}"
case "${1:-}" in
  --yes|-y) YES=1 ;;
  --explain) ;;
  "") ;;
  *) echo "usage: scripts/k8s-cluster-lane.sh [--yes]" >&2; exit 2 ;;
esac

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
skip() { printf 'SKIPPED (not a pass): %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- Gate 1: the lane is off unless explicitly switched on ------------------
if [ "${AISOTROPY_K8S_CLUSTER_LANE:-0}" != "1" ]; then
  echo "k8s-cluster-lane: OFF by default."
  skip "AISOTROPY_K8S_CLUSTER_LANE is not set to 1 — no cluster was created."
  echo
  echo "  The cheap lane covers chart correctness with no cluster at all:"
  echo "      scripts/k8s-chart-guard.sh"
  echo "  Enable this lane deliberately, when the machine is idle:"
  echo "      AISOTROPY_K8S_CLUSTER_LANE=1 scripts/k8s-cluster-lane.sh"
  exit 0
fi

# --- Gate 2: tools -------------------------------------------------------- -
missing=()
have docker  || missing+=("docker (image build + node runtime)")
have kubectl || missing+=("kubectl")
have helm    || missing+=("helm")
if ! have kind && ! have k3d; then missing+=("kind or k3d"); fi
if [ "${#missing[@]}" -gt 0 ]; then
  echo "k8s-cluster-lane: prerequisites absent."
  for m in "${missing[@]}"; do skip "missing: $m"; done
  echo "  Nothing was created. The offline lane (scripts/k8s-chart-guard.sh) needs only helm."
  exit 0
fi

# --- Gate 3: the machine ----------------------------------------------------
log "resource-guard pre-flight (a guard that cannot evaluate must never absolve)"
bash scripts/resource-guard.sh --explain
guard=$?
case "$guard" in
  0) log "resource-guard: OK — proceeding." ;;
  1)
    if [ "${AISOTROPY_K8S_CLUSTER_LANE_DEGRADED_OK:-0}" = "1" ]; then
      log "resource-guard: DEGRADED, but AISOTROPY_K8S_CLUSTER_LANE_DEGRADED_OK=1 — proceeding at your risk."
    else
      echo
      echo "REFUSED: resource-guard reports DEGRADED (tight). A Kind node plus a Go"
      echo "image build on a machine already under pressure is how test workers get"
      echo "OOM-killed. Nothing was created."
      echo "  Wait for the machine to settle, or override deliberately with:"
      echo "      AISOTROPY_K8S_CLUSTER_LANE_DEGRADED_OK=1"
      exit 3
    fi
    ;;
  *)
    echo
    echo "REFUSED: resource-guard reports BLOCKED (saturated, or could not measure)."
    echo "This is a HARD refusal with no override — starting a cluster now would"
    echo "take the machine down. Nothing was created."
    exit 3
    ;;
esac

# --- Gate 4: explicit consent (capacity != permission) ----------------------
if [ "$YES" != "1" ]; then
  cat <<EOF

PLAN (nothing has been created — this is a dry run):
  1. create a $( have kind && echo kind || echo k3d ) cluster named '$CLUSTER'
  2. docker build the Distroless image from ./Dockerfile
     NOTE: the build context is currently $(du -sh . 2>/dev/null | cut -f1) on disk. Compiled
     binaries at the repo root (aisotropyd, bitrate-harness, token-economy) are
     NOT excluded by .dockerignore, so this step transfers multiple GB and is
     the single most expensive part of this lane.
  3. side-load the image, helm install into namespace '$NAMESPACE' (TIERS=$TIERS)
  4. port-forward and curl /healthz + /health
  5. delete the cluster (unless KEEP_CLUSTER=1)

The resource gate above reports capacity, not permission — passing it is never
on its own an authorisation to spend. Re-run with --yes to actually spend it:

    AISOTROPY_K8S_CLUSTER_LANE=1 scripts/k8s-cluster-lane.sh --yes

EOF
  skip "no --yes given — plan printed, nothing created."
  exit 0
fi

# --- Bring-up (delegates to the existing one-command script) ----------------
teardown() {
  if [ "$KEEP_CLUSTER" = "1" ]; then
    log "KEEP_CLUSTER=1 — leaving cluster '$CLUSTER' up. Tear down with: deploy/local/down.sh"
  else
    log "tearing down cluster '$CLUSTER'"
    CLUSTER="$CLUSTER" bash deploy/local/down.sh || true
  fi
}
trap teardown EXIT

log "bringing up (TIERS=$TIERS) via deploy/local/up.sh"
if ! CLUSTER="$CLUSTER" NAMESPACE="$NAMESPACE" TIERS="$TIERS" INSTALLER=helm \
     bash deploy/local/up.sh; then
  echo "RESULT: 🔴 FINDINGS — bring-up failed."
  exit 1
fi

# --- Smoke: the REAL endpoints the chart's probes point at ------------------
log "smoke-testing /healthz + /health through a port-forward"
kubectl -n "$NAMESPACE" port-forward svc/aisotropy 18080:8080 >/dev/null 2>&1 &
PF_PID=$!
trap '{ kill "$PF_PID" 2>/dev/null; teardown; }' EXIT
for _ in $(seq 1 20); do
  curl -fsS localhost:18080/healthz >/dev/null 2>&1 && break
  sleep 1
done

FINDINGS=0
hz="$(curl -fsS localhost:18080/healthz 2>&1)" || hz="<unreachable>"
if printf '%s' "$hz" | grep -q '"status":"ok"'; then
  echo "  PASS  GET /healthz -> $hz"
else
  echo "  FAIL  GET /healthz -> $hz"; FINDINGS=1
fi
h="$(curl -fsS localhost:18080/health 2>&1)" || h="<unreachable or 503>"
if printf '%s' "$h" | grep -q '"status":"ok"'; then
  echo "  PASS  GET /health -> $h"
else
  echo "  FAIL  GET /health -> $h"; FINDINGS=1
fi

echo
if [ "$FINDINGS" -ne 0 ]; then
  echo "RESULT: 🔴 FINDINGS — cluster came up but the app is not healthy."
  exit 1
fi
echo "RESULT: 🟢 OK — chart installs on a real cluster and serves /healthz + /health."
exit 0
