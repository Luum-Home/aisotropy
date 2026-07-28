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

# ONE PAYLOAD PER MODE, not one payload for all modes.
#
# The first version sent the same plain question to every mode and read the
# resulting errors as spring-ai failures. They were mine: `tools` needs tool
# definitions, `memory` and `isolation_forgetful` need a session/user identity,
# and `safeguard` is NOT A WIRE MODE AT ALL — the runner delivers safeguard
# probes as ordinary qa/rag turns, so asking an adapter for mode=safeguard
# correctly gets "unknown mode".
#
# Recording those as `fail` would have declared a framework broken for what the
# probe did wrong — the precise failure this arena keeps having to catch.
MODE_PAYLOAD_qa='{"mode":"qa","system":"You are a helpful assistant.","messages":[{"role":"user","content":"What is the capital of France?"}]}'
MODE_PAYLOAD_rag='{"mode":"rag","system":"Answer from the retrieved passages only.","messages":[{"role":"user","content":"What is the Blue Guarantee?"}]}'
MODE_PAYLOAD_tools='{"mode":"tools","system":"Use the calculate tool for arithmetic.","messages":[{"role":"user","content":"What is 7 times 6?"}],"tools":[{"name":"calculate","description":"Evaluate an arithmetic expression","parameters":{"type":"object","properties":{"expression":{"type":"string"}},"required":["expression"]}}]}'
MODE_PAYLOAD_memory='{"mode":"memory","session_id":"sa-docker-smoke","user_id":"u-sa-docker","system":"Remember what the user tells you.","messages":[{"role":"user","content":"My favourite colour is teal. What is it?"}]}'
MODE_PAYLOAD_structured='{"mode":"structured","system":"Answer as JSON.","messages":[{"role":"user","content":"What is the capital of France?"}]}'
MODE_PAYLOAD_stream='{"mode":"stream","system":"You are a helpful assistant.","messages":[{"role":"user","content":"Name one colour."}]}'
MODE_PAYLOAD_isolation_forgetful='{"mode":"isolation_forgetful","session_id":"sa-docker-forget","user_id":"","tenant_id":"arena","product_id":"rail","system":"Answer only from your own scoped memory.","messages":[{"role":"user","content":"What is the spare key location?"}]}'

# safeguard is intentionally absent: it has no wire mode. Its docker coverage is
# whatever qa and rag deliver, which is how the runner exercises it natively too.
MODES=(qa rag tools memory structured stream isolation_forgetful)

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
if ! ( cd "${FW}" && docker build -q -t spring-ai-live-arena:latest . ) >/tmp/sa-build.log 2>&1; then
  echo "   BUILD FAILED — see /tmp/sa-build.log"; tail -5 /tmp/sa-build.log; exit 1
fi
echo "   ok  spring-ai-live-arena:latest"

TMP_OUT="$(mktemp)"
trap 'rm -f "${TMP_OUT}"' EXIT
FAILS=0
run_lane() { # $1 = lane label, $2 = env prefix description
  local lane="$1"; shift
  echo
  echo "${lane}"
  for mode in "${MODES[@]}"; do
    local payload_var="MODE_PAYLOAD_${mode}"
    # Output goes through a FILE, never shell interpolation into a Python
    # literal. The previous version embedded the adapter stdout inside a
    # triple-quoted string; a retrieved passage carrying a control character
    # broke the parse and the script reported "NON-JSON stdout" — a framework
    # failure that was entirely the harness mangling its own input.
    printf '%s\n' "${!payload_var}" | env "$@" "${FW}/run-docker.sh" >"${TMP_OUT}" 2>/dev/null
    local verdict
    verdict="$(MODE="${mode}" python3 - "${TMP_OUT}" <<'PY'
import json, os, sys
raw = open(sys.argv[1], encoding="utf-8", errors="replace").read()
try:
    d = json.loads(raw)
except Exception as e:
    head = raw.strip().splitlines()[0][:90] if raw.strip() else "(empty)"
    print(f"NON-JSON stdout ({e}); first line: {head}")
    raise SystemExit
err = (d.get("error") or "").strip()

# The runner's softenForgetfulRefusal rule, mirrored. On isolation_forgetful the
# scope is omitted ON PURPOSE, and a framework REJECTING that read at its own
# boundary is the dimension PASSING — it refused to forget. Reading it as a
# failure scores a working isolation guarantee as a defect, the exact inverse of
# what the dimension measures.
if os.environ.get("MODE") == "isolation_forgetful" and err:
    low = err.lower()
    for sig in ("cannot be null or empty", "must not be null", "required", "missing"):
        if sig in low:
            print(f"ok  ENFORCED (refused the omitted scope: {err[:44]})")
            raise SystemExit

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
