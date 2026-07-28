#!/usr/bin/env bash
# wait-for-board.sh — block until the arena runner exits. Safely.
#
# THE BUG THIS REPLACES
#
# The obvious one-liner is:
#
#     while pgrep -f 'aisotropy-live-runner' >/dev/null; do sleep 60; done
#
# and it never terminates. `pgrep -f` matches against full command lines, and the
# watcher's OWN command line contains the pattern — it is right there in the
# while condition. So the watcher finds itself, forever. Three chained follow-up
# tasks sat spinning past the end of a completed board because of exactly this,
# and the board's own output said "done" the whole time.
#
# It is the same mistake, in mirror image, as a `pkill -f 'exe/runner'` that
# matched nothing because the compiled binary lives in the go-build cache under a
# different name: both are pattern-matching against process command lines without
# checking what the pattern can and cannot see. One never fires, one never stops.
#
# THE FIXES, both applied:
#   1. Exclude self and our whole process group ($$ and $PPID) from the match.
#   2. Match on the runner's ARGV[0] via `pgrep -f -a` and a check that the line
#      is not a shell — a shell whose arguments merely mention the runner is not
#      the runner.
#   3. A hard timeout. A watcher that cannot decide must give up loudly rather
#      than spin: an unbounded wait is indistinguishable from a hang, and the
#      operator sees "stuck" either way.
#
# Usage: wait-for-board.sh [timeout_seconds]   (default 21600 = 6h)
# Exit codes: 0 board is not running · 1 timed out while it was still running.

set -uo pipefail
TIMEOUT="${1:-21600}"
INTERVAL=30
SELF=$$

board_running() {
  # -a prints the full command line so we can inspect it. A match is only real
  # when the process is NOT this script, NOT its parent, and NOT a shell that
  # merely mentions the runner in its arguments.
  while read -r pid line; do
    [[ "${pid}" == "${SELF}" || "${pid}" == "${PPID}" ]] && continue
    case "${line}" in
      *"/bin/sh"*|*"/bin/bash"*|*"/bin/zsh"*|*"pgrep"*|*"wait-for-board"*) continue ;;
    esac
    return 0
  done < <(pgrep -f -a 'aisotropy-live-runner' 2>/dev/null)
  return 1
}

waited=0
while board_running; do
  if (( waited >= TIMEOUT )); then
    echo "wait-for-board: TIMED OUT after ${TIMEOUT}s with the runner still alive." >&2
    echo "Not silently continuing: a follow-up that acts on a half-written record" >&2
    echo "stream would describe a run that never finished." >&2
    exit 1
  fi
  sleep "${INTERVAL}"
  waited=$(( waited + INTERVAL ))
done
exit 0
