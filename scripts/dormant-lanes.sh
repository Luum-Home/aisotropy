#!/usr/bin/env bash
# dormant-lanes.sh — the ONE operator-runnable lane that brings the repo's
# dormant paths to life locally and proves they work.
#
# WHAT WAS ACTUALLY MISSING (verified 2026-07-27, docs/06-Daily/reports/
# gap-claims-verification-2026-07-27.md — do not trust older "no worker / no
# test" framings):
#   - Both Rust workers ARE in-repo and tracked (crates/sleep-cycle,
#     crates/bitrate-scorer), and BOTH Go-side subprocess contracts are fully
#     unit-tested with executable stubs. Nothing was missing there.
#   - Real-service tests against rqlite and Qdrant ARE written — four files,
#     all `//go:build manual`.
#   - The gap was the LANE: `git grep -l "tags manual" -- scripts/ .github/`
#     returned NOTHING. No script and no workflow ever built a Rust worker or
#     invoked a single `-tags manual` test. The code was live; the button did
#     not exist. This script is that button.
#
# THREE STAGES, each independently selectable:
#   workers   cargo-build both Rust workers, export AISOTROPY_SLEEPCYCLE_BIN /
#             AISOTROPY_BITRATE_BIN, run the Go-side contract tests, then the
#             `-tags manual` live tests against the REAL binaries.
#   services  bring up an ISOLATED rqlite + Qdrant stack (deploy/dormant-lane/),
#             wait for readiness, run the `-tags manual` real-service tests, and
#             tear the stack back down. The machine is left as it was found.
#   status    (DEFAULT) report what is present vs missing and what each stage
#             would cost — WITHOUT starting, building, pulling or provisioning
#             anything.
#
# CONSENT AND CAPACITY ARE SEPARATE (ADR-0071's lesson, applied):
#   - Default invocation is `status`. Doing anything requires naming a stage.
#   - Provisioning (cargo build, docker compose up) additionally requires an
#     explicit --yes. A resource verdict says the machine CAN; only the operator
#     says it SHOULD.
#   - Every heavy step runs under scripts/with-heavy-lane.sh --need-mb N (the
#     cross-session mutex + a DECLARED RAM budget), never bare.
#
# USAGE
#   scripts/dormant-lanes.sh                              # status (safe, spends nothing)
#   scripts/dormant-lanes.sh --stage workers --yes        # build + test the Rust workers
#   scripts/dormant-lanes.sh --stage services --yes       # real rqlite + Qdrant lane
#   scripts/dormant-lanes.sh --stage all --yes            # both
#   LANE_RQLITE_PORT=24001 scripts/dormant-lanes.sh --stage services --yes
#
# EXIT CODES (repo guard convention)
#   0  clean — everything selected ran and passed, or SKIPPED with an explicit reason
#   1  findings — a stage ran and failed, or (status) a prerequisite is missing
#   2  usage/environment error
#   3  BLOCKED — resource-guard refused; nothing was built or started
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || {
  echo "dormant-lanes: not in a git repo" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# Declared RAM budgets (--need-mb). These are the contract, not decoration:
# resource-guard refuses the step outright when the machine cannot supply the
# number, so an UNDER-declared budget is the dangerous direction (it buys a
# green light the machine cannot honour) and an over-declared one only costs a
# false refusal.
#
# MEASURED, not guessed (2026-07-27, first full execution of this lane on a
# 24 GB M-series host, both crates COLD — no target/ dir for sleep-cycle):
#   peak RSS summed over all cargo+rustc processes, sampled every 2s across the
#   whole workers stage = 1924 MB, with CARGO_BUILD_JOBS=4.
#   sleep-cycle (DataFusion 43, ~330 crates) built in 3m08s; bitrate-scorer
#   (candle + tokenizers) in 40.7s.
# The sample is STAGE-level, so it cannot be attributed to one crate — both
# cargo budgets therefore declare the same measured peak + ~33% headroom rather
# than pretending to a per-crate number nobody measured.
#
# All cargo figures assume the job cap set below: peak build RAM scales with
# concurrent rustc/codegen units, so bounding the jobs is what makes a declared
# budget meaningful at all. Raise CARGO_BUILD_JOBS and you must raise these.
# ---------------------------------------------------------------------------
NEED_MB_CARGO_SLEEPCYCLE="${NEED_MB_CARGO_SLEEPCYCLE:-2560}"
NEED_MB_CARGO_BITRATE="${NEED_MB_CARGO_BITRATE:-2560}"
# Go contract tests for the two worker packages: small packages, but `go test`
# builds the toolchain's own cache entries; 1 GB is generous headroom.
NEED_MB_GO_CONTRACT="${NEED_MB_GO_CONTRACT:-1024}"
# Two containers: Qdrant idles ~300-400 MB RSS, rqlite ~60-100 MB, plus the
# container engine's per-container overhead. NOT the engine VM itself — that is
# already running if the daemon answered.
NEED_MB_SERVICES="${NEED_MB_SERVICES:-1024}"
# The manual test binaries themselves are small clients (measured: both live
# suites finished in <2s wall clock with no visible memory pressure).
NEED_MB_GO_MANUAL="${NEED_MB_GO_MANUAL:-512}"

# Bound cargo's parallelism so the declared budgets above stay true.
export CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-4}"

# ---------------------------------------------------------------------------
# Lane-owned service coordinates. Deliberately NOT the reference stack's
# 4001/6333/6334: an operator or a concurrent agent session may own those, and
# this lane must never adopt, disturb, or tear down a stack it did not create.
# ---------------------------------------------------------------------------
LANE_PROJECT="${LANE_PROJECT:-aisotropy-dormant-lane}"
LANE_COMPOSE="deploy/dormant-lane/docker-compose.yml"
export LANE_RQLITE_PORT="${LANE_RQLITE_PORT:-14001}"
export LANE_QDRANT_HTTP_PORT="${LANE_QDRANT_HTTP_PORT:-16333}"
export LANE_QDRANT_GRPC_PORT="${LANE_QDRANT_GRPC_PORT:-16334}"
# LOCAL-ONLY dev credentials, identical in posture to the reference stack. The
# rqlite password is paired with deploy/rqlite/users.json — change both together.
export QDRANT_API_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}"
LANE_RQLITE_PASSWORD="${RQLITE_PASSWORD:-local-dev-rqlite-pw-CHANGE-ME}"
READY_TIMEOUT_S="${READY_TIMEOUT_S:-120}"

SLEEPCYCLE_BIN="crates/sleep-cycle/target/release/sleep-cycle"
BITRATE_BIN="crates/bitrate-scorer/target/release/bitrate-scorer"

STAGE=""
YES=0
findings=0

while [ $# -gt 0 ]; do
  case "$1" in
    --status) STAGE="status"; shift ;;
    --stage) STAGE="${2:-}"; shift 2 || { echo "dormant-lanes: --stage requires a value" >&2; exit 2; } ;;
    --yes | -y) YES=1; shift ;;
    -h | --help) sed -n '2,60p' "$0"; exit 0 ;;
    *) echo "dormant-lanes: unknown argument: $1" >&2; exit 2 ;;
  esac
done
STAGE="${STAGE:-status}"
case "$STAGE" in
  status | workers | services | all) ;;
  *) echo "dormant-lanes: --stage must be one of: workers services all (or --status)" >&2; exit 2 ;;
esac

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m  OK\033[0m %s\n' "$*"; }
bad()  { printf '\033[1;31m  FAIL\033[0m %s\n' "$*"; findings=1; }
skip() { printf '  SKIPPED (not a pass): %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

# heavy <need_mb> <label> -- <cmd...>
# Every expensive step goes through here. No bare cargo/docker/go anywhere below.
heavy() {
  local need="$1" label="$2"
  shift 2
  log "$label  (declared need: ${need} MB)"
  scripts/with-heavy-lane.sh --need-mb "$need" "$@"
  local rc=$?
  if [ "$rc" -eq 3 ]; then
    echo
    echo "BLOCKED: resource-guard refused '$label' (needs ${need} MB) or the heavy-lane"
    echo "lock timed out. Nothing was built or started by this step."
    return 3
  fi
  return "$rc"
}

port_busy() { nc -z localhost "$1" >/dev/null 2>&1; }

# ===========================================================================
# STATUS — pure inspection. Runs no build, starts no container, pulls nothing.
# ===========================================================================
status_report() {
  local missing=0
  log "dormant-lanes status — inspection only, nothing will be built or started"
  echo

  echo "TOOLING"
  if have cargo; then ok "cargo present: $(cargo --version 2>/dev/null)"; else skip "cargo ABSENT — the 'workers' stage cannot run"; missing=1; fi
  if have go; then ok "go present: $(go version 2>/dev/null)"; else skip "go ABSENT — no stage can run"; missing=1; fi
  if have docker; then
    ok "docker present: $(docker --version 2>/dev/null)"
    if docker info >/dev/null 2>&1; then ok "docker daemon responding"; else skip "docker daemon NOT responding — the 'services' stage cannot run"; missing=1; fi
  else
    skip "docker ABSENT — the 'services' stage cannot run"; missing=1
  fi
  have nc || skip "nc absent — port pre-checks will be skipped (not fatal)"
  echo

  echo "RUST WORKERS (stage: workers)"
  if [ -x "$SLEEPCYCLE_BIN" ]; then ok "sleep-cycle built: $SLEEPCYCLE_BIN ($(du -h "$SLEEPCYCLE_BIN" | cut -f1))"
  else skip "sleep-cycle NOT built — full DataFusion 43 release build (measured cold: 3m08s, ~836 MB of target/, <=${NEED_MB_CARGO_SLEEPCYCLE} MB declared)"; missing=1; fi
  if [ -x "$BITRATE_BIN" ]; then ok "bitrate-scorer built: $BITRATE_BIN ($(du -h "$BITRATE_BIN" | cut -f1))"
  else skip "bitrate-scorer NOT built — candle release build (measured cold: 41s, ~778 MB of target/, <=${NEED_MB_CARGO_BITRATE} MB declared)"; missing=1; fi
  local tgt
  tgt="$(du -sh crates/sleep-cycle/target crates/bitrate-scorer/target 2>/dev/null | awk '{print $1}' | paste -sd'+' - || true)"
  [ -n "${tgt:-}" ] && echo "  DISK: cargo target dirs currently $tgt (a cold build of both lands ~1.6 GB)"
  if [ -n "${AISOTROPY_BITRATE_MODEL_DIR:-}" ] && [ -f "${AISOTROPY_BITRATE_MODEL_DIR}/model.safetensors" ]; then
    ok "bitrate model dir usable: $AISOTROPY_BITRATE_MODEL_DIR"
  else
    skip "AISOTROPY_BITRATE_MODEL_DIR unset or has no model.safetensors — the bitrate LIVE test will SKIP (the Rust scorer needs converted GPT-2 weights; see ADR-0015)"
  fi
  echo

  echo "REAL SERVICES (stage: services) — lane project '$LANE_PROJECT'"
  if have docker && docker info >/dev/null 2>&1; then
    for img in qdrant/qdrant:v1.12.4 rqlite/rqlite:8.36.3; do
      if docker image inspect "$img" >/dev/null 2>&1; then ok "image present: $img"
      else skip "image NOT pulled: $img — the stage will download it"; missing=1; fi
    done
    local running
    running="$(docker compose -p "$LANE_PROJECT" ps -q 2>/dev/null | wc -l | tr -d ' ')"
    if [ "${running:-0}" -gt 0 ]; then
      skip "the lane's OWN stack is already up ($running containers) — a leftover from an interrupted run; 'docker compose -p $LANE_PROJECT down -v' clears it"
      missing=1
    else
      ok "lane stack not running (clean slate)"
    fi
    local foreign
    foreign="$(docker ps --format '{{.Names}}' 2>/dev/null | grep -Ec 'qdrant|rqlite' || true)"
    [ "${foreign:-0}" -gt 0 ] && echo "  NOTE: $foreign qdrant/rqlite container(s) are running OUTSIDE this lane's project."
    echo "        The lane uses its own ports and project and will not touch them."
  else
    skip "docker unavailable — cannot inspect images or containers"
  fi
  if have nc; then
    for p in "$LANE_RQLITE_PORT" "$LANE_QDRANT_HTTP_PORT" "$LANE_QDRANT_GRPC_PORT"; do
      if port_busy "$p"; then bad "lane port $p is OCCUPIED — set LANE_*_PORT to free ports"; else ok "lane port $p free"; fi
    done
  fi
  echo

  echo "MACHINE"
  bash scripts/resource-guard.sh --explain
  local g=$?
  case "$g" in
    0) ok "resource-guard: OK" ;;
    1) skip "resource-guard: DEGRADED — heavy steps will run serially with capped threads" ;;
    *) skip "resource-guard: BLOCKED — heavy steps would refuse right now" ;;
  esac
  echo

  cat <<EOF
COST OF EACH STAGE (declared --need-mb, enforced by resource-guard)
  workers   cargo sleep-cycle ${NEED_MB_CARGO_SLEEPCYCLE} MB | cargo bitrate ${NEED_MB_CARGO_BITRATE} MB | go contract ${NEED_MB_GO_CONTRACT} MB | go manual ${NEED_MB_GO_MANUAL} MB
            (cargo jobs capped at CARGO_BUILD_JOBS=${CARGO_BUILD_JOBS}; the budgets assume that cap)
            measured cold on a 24 GB host: ~4 min wall clock, 1924 MB peak cargo+rustc RSS, ~1.6 GB disk
  services  containers ${NEED_MB_SERVICES} MB | go manual ${NEED_MB_GO_MANUAL} MB
            brings up qdrant + rqlite on ports ${LANE_QDRANT_HTTP_PORT}/${LANE_QDRANT_GRPC_PORT}/${LANE_RQLITE_PORT},
            ephemeral storage, and tears them down again (no named volumes, nothing left behind)

TO ACTUALLY RUN (consent is separate from capacity):
  scripts/dormant-lanes.sh --stage workers --yes
  scripts/dormant-lanes.sh --stage services --yes
EOF
  return "$missing"
}

# ===========================================================================
# STAGE: workers — build the Rust workers, then exercise them for real.
# ===========================================================================
stage_workers() {
  log "STAGE workers — Rust sleep-cycle + bitrate-scorer"
  if ! have cargo; then
    skip "cargo is not installed — cannot build the Rust workers. Nothing was done."
    return 0
  fi
  if [ "$YES" != "1" ]; then
    cat <<EOF

PLAN (nothing has been built — pass --yes to run):
  1. cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml     [${NEED_MB_CARGO_SLEEPCYCLE} MB]
  2. cargo build --release --manifest-path crates/bitrate-scorer/Cargo.toml  [${NEED_MB_CARGO_BITRATE} MB]
  3. go test ./internal/sleepcycle/ ./internal/bitrate/                      [${NEED_MB_GO_CONTRACT} MB]
     with AISOTROPY_SLEEPCYCLE_BIN / AISOTROPY_BITRATE_BIN exported at the built paths
  4. go test -tags manual -run 'TestWorkerLive...' ./internal/sleepcycle/    [${NEED_MB_GO_MANUAL} MB]
  5. go test -tags manual -run TestScorerLive ./internal/bitrate/            [${NEED_MB_GO_MANUAL} MB]
     (SKIPS unless AISOTROPY_BITRATE_MODEL_DIR points at converted GPT-2 weights)
EOF
    return 0
  fi

  heavy "$NEED_MB_CARGO_SLEEPCYCLE" "cargo build --release: sleep-cycle (DataFusion)" \
    cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml
  case $? in
    0) ok "sleep-cycle built: $SLEEPCYCLE_BIN" ;;
    3) return 3 ;;
    *) bad "cargo build sleep-cycle failed"; return 1 ;;
  esac

  heavy "$NEED_MB_CARGO_BITRATE" "cargo build --release: bitrate-scorer (candle)" \
    cargo build --release --manifest-path crates/bitrate-scorer/Cargo.toml
  case $? in
    0) ok "bitrate-scorer built: $BITRATE_BIN" ;;
    3) return 3 ;;
    *) bad "cargo build bitrate-scorer failed"; return 1 ;;
  esac

  # The whole point of the stage: the env vars that gate these paths at runtime
  # now point at real, just-built binaries.
  export AISOTROPY_SLEEPCYCLE_BIN="$PWD/$SLEEPCYCLE_BIN"
  export AISOTROPY_BITRATE_BIN="$PWD/$BITRATE_BIN"
  ok "AISOTROPY_SLEEPCYCLE_BIN=$AISOTROPY_SLEEPCYCLE_BIN"
  ok "AISOTROPY_BITRATE_BIN=$AISOTROPY_BITRATE_BIN"

  heavy "$NEED_MB_GO_CONTRACT" "Go-side subprocess contract tests" \
    go test -count=1 ./internal/sleepcycle/ ./internal/bitrate/
  case $? in
    0) ok "contract tests pass (internal/sleepcycle, internal/bitrate)" ;;
    3) return 3 ;;
    *) bad "contract tests failed" ;;
  esac

  heavy "$NEED_MB_GO_MANUAL" "LIVE sleep-cycle worker (-tags manual, real Rust binary)" \
    go test -tags manual -count=1 -v -run 'TestWorkerLive' ./internal/sleepcycle/
  case $? in
    0) ok "live sleep-cycle tests pass against the real DataFusion worker" ;;
    3) return 3 ;;
    *) bad "live sleep-cycle tests failed" ;;
  esac

  if [ -n "${AISOTROPY_BITRATE_MODEL_DIR:-}" ] && [ -f "${AISOTROPY_BITRATE_MODEL_DIR}/model.safetensors" ]; then
    heavy "$NEED_MB_GO_MANUAL" "LIVE bitrate scorer (-tags manual, real Rust binary + model)" \
      go test -tags manual -count=1 -v -run 'TestScorerLive' ./internal/bitrate/
    case $? in
      0) ok "live bitrate scorer test passes" ;;
      3) return 3 ;;
      *) bad "live bitrate scorer test failed" ;;
    esac
  else
    skip "live bitrate scorer: AISOTROPY_BITRATE_MODEL_DIR unset (or missing model.safetensors). The Rust scorer needs converted GPT-2 weights — one-time offline conversion, see ADR-0015. The binary IS built and the Go contract IS covered above; only the weights-dependent run is absent."
  fi
  return 0
}

# ===========================================================================
# STAGE: services — real rqlite + Qdrant, then the -tags manual tests.
# ===========================================================================
# shellcheck disable=SC2329  # invoked indirectly, via the EXIT/INT/TERM trap below
lane_down() {
  # Teardown is unconditional-on-exit and scoped to the lane's OWN project.
  # -v removes the project's volumes; since the lane declares none, this is
  # belt-and-braces against a future edit adding one.
  log "tearing the lane stack down (leaving the machine as found)"
  docker compose -p "$LANE_PROJECT" -f "$LANE_COMPOSE" down -v --remove-orphans >/dev/null 2>&1
  local left
  left="$(docker compose -p "$LANE_PROJECT" ps -q 2>/dev/null | wc -l | tr -d ' ')"
  if [ "${left:-0}" -eq 0 ]; then ok "lane stack down — 0 containers remain in project '$LANE_PROJECT'"
  else bad "teardown incomplete: $left container(s) still in project '$LANE_PROJECT'"; fi
}

wait_ready() {
  local label="$1" url="$2" hdr="${3:-}" waited=0
  log "waiting for $label readiness (bounded at ${READY_TIMEOUT_S}s)"
  while [ "$waited" -lt "$READY_TIMEOUT_S" ]; do
    if [ -n "$hdr" ]; then
      curl -sf -o /dev/null --max-time 5 -H "$hdr" "$url" && { ok "$label ready after ${waited}s"; return 0; }
    else
      curl -sf -o /dev/null --max-time 5 "$url" && { ok "$label ready after ${waited}s"; return 0; }
    fi
    sleep 3
    waited=$((waited + 3))
  done
  bad "$label did NOT become ready within ${READY_TIMEOUT_S}s ($url)"
  return 1
}

stage_services() {
  log "STAGE services — real rqlite + Qdrant, isolated project '$LANE_PROJECT'"
  if ! have docker; then skip "docker is not installed — nothing was started."; return 0; fi
  if ! docker info >/dev/null 2>&1; then skip "docker daemon is not responding — nothing was started."; return 0; fi
  if ! have curl; then skip "curl is not installed — readiness cannot be probed; refusing to start containers blind."; return 0; fi
  if [ ! -f "$LANE_COMPOSE" ]; then echo "dormant-lanes: missing $LANE_COMPOSE" >&2; return 2; fi

  # Verify-before-execute: re-check ports at execution time, not at plan time.
  if have nc; then
    for p in "$LANE_RQLITE_PORT" "$LANE_QDRANT_HTTP_PORT" "$LANE_QDRANT_GRPC_PORT"; do
      if port_busy "$p"; then
        bad "lane port $p is occupied — refusing to start (it may belong to another session). Set LANE_RQLITE_PORT / LANE_QDRANT_HTTP_PORT / LANE_QDRANT_GRPC_PORT to free ports."
        return 1
      fi
    done
  fi

  if [ "$YES" != "1" ]; then
    cat <<EOF

PLAN (nothing has been started — pass --yes to run):
  1. docker compose -p $LANE_PROJECT -f $LANE_COMPOSE up -d      [${NEED_MB_SERVICES} MB]
     qdrant  -> localhost:${LANE_QDRANT_HTTP_PORT} (REST) / ${LANE_QDRANT_GRPC_PORT} (gRPC)
     rqlite  -> localhost:${LANE_RQLITE_PORT} (HTTP, -auth, node-id pinned, ephemeral storage)
  2. wait for /readyz on both (bounded at ${READY_TIMEOUT_S}s)
  3. go test -tags manual -run 'TestRqliteLive|TestRqliteRebootIdempotent' ./internal/facts/   [${NEED_MB_GO_MANUAL} MB]
  4. go test -tags manual -run 'TestQdrantLive|TestQdrantLiveNewPaths'     ./internal/semantic/ [${NEED_MB_GO_MANUAL} MB]
  5. docker compose -p $LANE_PROJECT down -v  (always, even on failure/interrupt)
EOF
    return 0
  fi

  # Guarded, declared-budget provisioning. Note `up` itself is the heavy step.
  trap 'lane_down' EXIT INT TERM
  heavy "$NEED_MB_SERVICES" "docker compose up: qdrant + rqlite (lane project)" \
    docker compose -p "$LANE_PROJECT" -f "$LANE_COMPOSE" up -d
  case $? in
    0) ok "lane stack started" ;;
    3) trap - EXIT INT TERM; return 3 ;;
    *) bad "docker compose up failed"; return 1 ;;
  esac

  local ready=0
  wait_ready "rqlite" "http://localhost:${LANE_RQLITE_PORT}/readyz" || ready=1
  wait_ready "qdrant" "http://localhost:${LANE_QDRANT_HTTP_PORT}/readyz" "api-key: ${QDRANT_API_KEY}" || ready=1
  if [ "$ready" -ne 0 ]; then
    echo "  service logs (tail):"
    docker compose -p "$LANE_PROJECT" -f "$LANE_COMPOSE" logs --tail 20 2>&1 | sed 's/^/    /'
    return 1
  fi

  heavy "$NEED_MB_GO_MANUAL" "LIVE facts tier against real rqlite (-tags manual)" \
    env RQLITE_URL="http://aisotropy:${LANE_RQLITE_PASSWORD}@localhost:${LANE_RQLITE_PORT}" \
    go test -tags manual -count=1 -v -run 'TestRqliteLive|TestRqliteRebootIdempotent' ./internal/facts/
  case $? in
    0) ok "rqlite live tests pass (Store contract + idempotent-migration regression)" ;;
    3) return 3 ;;
    *) bad "rqlite live tests failed" ;;
  esac

  heavy "$NEED_MB_GO_MANUAL" "LIVE semantic tier against real Qdrant (-tags manual)" \
    env QDRANT_HOST=localhost QDRANT_PORT="${LANE_QDRANT_GRPC_PORT}" \
    QDRANT_API_KEY="${QDRANT_API_KEY}" QDRANT_COLLECTION=aisotropy_dormant_lane \
    go test -tags manual -count=1 -v -run 'TestQdrantLive|TestQdrantLiveNewPaths' ./internal/semantic/
  case $? in
    0) ok "qdrant live tests pass (gRPC Store contract + delete/prune/scroll paths)" ;;
    3) return 3 ;;
    *) bad "qdrant live tests failed" ;;
  esac

  # Tear down INSIDE the stage, not only from the trap: an incomplete teardown
  # is itself a finding ("leave the machine as found" is an acceptance
  # criterion, not a courtesy), and a trap that fires after the verdict is
  # printed can no longer influence the exit code. The trap stays armed for the
  # abnormal paths (early return, failure, Ctrl-C) and is disarmed here so the
  # stack is not torn down twice.
  trap - EXIT INT TERM
  lane_down
  return 0
}

# ===========================================================================
main() {
  local rc=0
  case "$STAGE" in
    status)
      status_report || rc=1
      exit "$rc"
      ;;
    workers)
      stage_workers; rc=$?
      ;;
    services)
      stage_services; rc=$?
      ;;
    all)
      stage_workers; rc=$?
      [ "$rc" -eq 3 ] && exit 3
      stage_services; local rc2=$?
      [ "$rc2" -eq 3 ] && exit 3
      [ "$rc2" -ne 0 ] && rc="$rc2"
      ;;
  esac
  [ "$rc" -eq 3 ] && exit 3
  [ "$rc" -eq 2 ] && exit 2
  echo
  if [ "$findings" -ne 0 ] || [ "$rc" -ne 0 ]; then
    echo "dormant-lanes: FINDINGS — see the FAIL lines above."
    exit 1
  fi
  echo "dormant-lanes: clean."
  exit 0
}
main
