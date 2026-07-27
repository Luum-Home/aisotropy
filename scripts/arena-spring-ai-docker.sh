#!/usr/bin/env bash
# arena-spring-ai-docker.sh — close spring-ai's 17 pending docker cells.
#
# WHY THESE CELLS ARE THE LAST HOLE IN THE MATRIX
#
# 840 declared cells across nine adapters: 410 pass, 382 infeasible (each with a
# written reason), 31 fail — and 17 pending, ALL of them spring-ai in docker
# form. native and wasm have none. So this script is the entire remaining gap
# between the arena and the no-blank-cells law.
#
# The flag that marks spring-ai a `candidate` exists BECAUSE of these cells, and
# it stays until they actually run. Clearing it by editing matrix.json would turn
# an honest declaration of incomplete coverage into a false claim of complete
# coverage — the exact failure mode this arena has spent the most effort not
# committing.
#
# COST, split by lane, because half of it is free:
#
#   stub/docker  8 cells   ZERO model calls (mocked model, in-memory stores)
#   real/docker  9 cells   ~95 real calls   (the provider is actually exercised)
#
# Usage: arena-spring-ai-docker.sh [--stub-only] [--real]
#   default      builds the image and runs the free stub cells
#   --real       ALSO runs the 9 real-lane cells (~95 model calls — a spend)
#   --stub-only  never touches the real lane, even by accident
#
# Exit codes: 0 ok · 1 a cell failed · 2 could not run.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FW="${ROOT}/arenas/live/spring-ai"
DO_REAL=0
for a in "$@"; do
  case "$a" in
    --real) DO_REAL=1 ;;
    --stub-only) DO_REAL=0 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

MODES=(qa rag tools memory structured stream safeguard isolation_forgetful)

if pgrep -f 'aisotropy-live-runner' >/dev/null 2>&1; then
  echo "ERROR: a board is running. Building a JVM image and driving containers" >&2
  echo "alongside it contends for both the provider's rate limit and memory —" >&2
  echo "and on a machine already deep in swap that is how a 3-hour board dies." >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "ERROR: docker unavailable — these cells are docker-FORM by definition." >&2
  exit 2
fi

echo "==================================================================="
echo " spring-ai docker cells — the last 17 pending in an 840-cell matrix"
echo "==================================================================="
echo
echo "0) image"
if ! ( cd "${FW}" && docker build -q -t arena-spring-ai:local . ) >/tmp/sa-build.log 2>&1; then
  echo "   BUILD FAILED — see /tmp/sa-build.log"; tail -5 /tmp/sa-build.log; exit 1
fi
echo "   ok  arena-spring-ai:local"

FAILS=0
run_lane() { # $1 = lane label, $2 = env prefix description
  local lane="$1"; shift
  echo
  echo "${lane}"
  for mode in "${MODES[@]}"; do
    local out
    out="$(printf '{"mode":"%s","system":"You are a helpful assistant.","messages":[{"role":"user","content":"What is the Blue Guarantee?"}]}\n' "${mode}" \
      | env "$@" "${FW}/run-docker.sh" 2>/dev/null)"
    local verdict
    verdict="$(python3 - <<PY
import json
raw = """${out}"""
try:
    d = json.loads(raw)
except Exception as e:
    print(f"NON-JSON stdout ({e})"); raise SystemExit
err = (d.get("error") or "").strip()
print(f"error: {err[:70]}" if err else f"ok  model={str(d.get('model'))[:26]}")
PY
)"
    case "${verdict}" in
      ok*) printf '   ok       %-22s %s\n' "${mode}" "${verdict#ok  }" ;;
      *)   printf '   FAIL     %-22s %s\n' "${mode}" "${verdict}"; FAILS=$((FAILS+1)) ;;
    esac
  done
}

# The stub lane first, always: it is free, and if the container itself is broken
# it fails here before a single billable call is made.
run_lane "1) stub/docker — 8 cells, ZERO model calls" ARENA_STUB=1

if [[ ${DO_REAL} -eq 1 ]]; then
  run_lane "2) real/docker — 9 cells, ~95 real model calls" \
    QDRANT_API_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}"
else
  echo
  echo "2) real/docker — SKIPPED (pass --real; ~95 model calls)"
fi

echo
echo "==================================================================="
if [[ ${FAILS} -gt 0 ]]; then
  echo " ${FAILS} cell(s) failed — declare them 'fail' in matrix.json, not 'pass'."
  echo " A cell that ran and failed is a RESULT; leaving it pending would be the blank."
  echo "==================================================================="
  exit 1
fi
echo " every executed cell ran. Update arenas/live/spring-ai/matrix.json from"
echo " these outcomes DELIBERATELY (scripts/arena-manifest-drift.py reports the"
echo " gaps); the candidate flag comes off only once no cell is pending."
echo "==================================================================="
