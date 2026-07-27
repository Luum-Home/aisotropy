#!/usr/bin/env bash
# with-heavy-lane.sh — run a heavy command under (a) the resource guard and
# (b) a cross-session mutex, at low scheduling priority.
#
# Why: several agent sessions run concurrently on this machine; two full
# `go test ./...` sweeps or a voice-model inference + a docker build at the
# same time is what saturates swap and gets workers OOM-killed. This wrapper
# serializes the heavy lanes across sessions with a plain mkdir lock (works on
# macOS, no flock dependency) and refuses to start when resource-guard says
# BLOCKED.
#
# Usage:
#   scripts/with-heavy-lane.sh go test ./... -count=1
#   scripts/with-heavy-lane.sh pytest examples/voice-pipecat/tests/
#   HEAVY_LANE_WAIT_S=600 scripts/with-heavy-lane.sh <cmd>   # wait up to 10min for the lock
#
# Exit codes: the wrapped command's exit code; 3 if blocked by the guard or
# the lock wait timed out (guard convention: 3 = BLOCKED).

set -u

LOCK_DIR="${TMPDIR:-/tmp}/aisotropy-heavy-lane.lock"
WAIT_S="${HEAVY_LANE_WAIT_S:-300}"
POLL_S=5

NEED_MB=""
while [ $# -gt 0 ]; do
  case "$1" in
    --need-mb) NEED_MB="${2:-}"; shift 2 || { echo "--need-mb requires a value" >&2; exit 2; } ;;
    --) shift; break ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) break ;;
  esac
done

if [ $# -eq 0 ]; then
  echo "usage: $0 [--need-mb N] <command> [args...]" >&2
  exit 2
fi

here="$(cd "$(dirname "$0")" && pwd)"
guard_args="--explain"
[ -n "$NEED_MB" ] && guard_args="$guard_args --need-mb $NEED_MB"

# 1) Resource pre-flight (reject before spending).
# shellcheck disable=SC2086 # guard_args is a controlled, space-separated flag list
"$here/resource-guard.sh" $guard_args
guard=$?
if [ "$guard" -eq 3 ]; then
  echo "with-heavy-lane: BLOCKED by resource-guard — not starting: $*" >&2
  exit 3
fi

# 2) Cross-session mutex: mkdir is atomic; stale locks (dead pid) are reclaimed.
waited=0
while ! mkdir "$LOCK_DIR" 2>/dev/null; do
  owner="$(cat "$LOCK_DIR/pid" 2>/dev/null || echo '?')"
  if [ "$owner" != "?" ] && ! kill -0 "$owner" 2>/dev/null; then
    echo "with-heavy-lane: reclaiming stale lock (dead pid $owner)" >&2
    rm -rf "$LOCK_DIR"
    continue
  fi
  if [ "$waited" -ge "$WAIT_S" ]; then
    echo "with-heavy-lane: BLOCKED — lock held by pid $owner after ${WAIT_S}s: $LOCK_DIR" >&2
    exit 3
  fi
  [ "$waited" -eq 0 ] && echo "with-heavy-lane: waiting for lock (held by pid $owner)..." >&2
  sleep "$POLL_S"; waited=$((waited + POLL_S))
done
echo "$$" > "$LOCK_DIR/pid"
trap 'rm -rf "$LOCK_DIR"' EXIT INT TERM

# 2b) RE-CHECK after acquiring the lock. The pre-flight verdict above may be
# minutes old — we may have waited the full HEAVY_LANE_WAIT_S for another
# session, and the machine it hands back is not the machine we measured. A
# stale OK is exactly how the ADR-0071 incident happened: the guard flipped
# between two probes and a lane that should have refused started a Kind
# cluster and a 3.5 GB build. A resource verdict is a snapshot; re-take it
# immediately before spending.
if [ "$waited" -gt 0 ]; then
  echo "with-heavy-lane: re-checking resources after ${waited}s lock wait (the pre-flight verdict is stale)" >&2
  # shellcheck disable=SC2086
  "$here/resource-guard.sh" $guard_args
  guard=$?
  if [ "$guard" -eq 3 ]; then
    echo "with-heavy-lane: BLOCKED on re-check — the machine changed while we waited; not starting: $*" >&2
    exit 3
  fi
fi

# 3) Run degraded-aware: low priority always; capped threads when tight.
if [ "$guard" -eq 1 ]; then
  export OMP_NUM_THREADS="${OMP_NUM_THREADS:-2}"
  export GOMAXPROCS="${GOMAXPROCS:-4}"
  echo "with-heavy-lane: DEGRADED — running with OMP_NUM_THREADS=$OMP_NUM_THREADS GOMAXPROCS=$GOMAXPROCS nice 19" >&2
fi
nice -n 19 "$@"
