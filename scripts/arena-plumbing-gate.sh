#!/usr/bin/env bash
# Regression gate for the arena's PLUMBING dimensions — no model, no spend.
#
# WHAT THIS IS FOR, AND WHAT IT DELIBERATELY IS NOT
# -------------------------------------------------
# The plumbing dimensions (memory, memory_scoped, isolation_leak,
# crash_recovery) measure whether a store persisted a turn, whether a compound
# scope kept tenants apart, whether state survived kill -9. The model is a
# passenger: hand it the same prompt and it contributes nothing to the answer
# beyond restating a fact.
#
# So these can run on the INTEGRATION lane (ARENA_STUB_MODEL=1: real stores,
# mock model) — deterministic, free, and immune to a provider outage. That also
# removes error the real model introduces in BOTH directions on a dimension it
# does not measure: it can fail to repeat a fact it WAS given (a false negative
# blaming the store) or produce a plausible answer without one (a false positive
# crediting a store that failed).
#
# THIS DOES NOT REPLACE THE REAL-LANE BOARD, and the distinction is the whole
# reason it is a separate script. The mock proves the pipes are connected. It
# cannot evaluate:
#   * hydration COST — a framework that replays 50 turns and fills the context
#     window passes the echo identically to one that sends three;
#   * whether what came back is USABLE — a framework can dump raw JSON into the
#     prompt; the echo repeats it happily, a real model may choke;
#   * aisotropy's actual claim, which is not "the store works" but "tiered
#     hydration produces surgical context" — a property only a reader can judge,
#     and an echo does not read, it repeats.
# The published board keeps measuring those with the real model. This gate
# catches a BROKEN store immediately and for free; the board says how WELL.
#
# THE CEILING IS DERIVED FROM THE SUITE, not chosen. Every memory item carries a
# LongMemEval-aligned category and an `answer_source`; the baseline is simply the
# count of `retrieved` items. Today that is 6 of 8, because mem-03-deadline
# (temporal-reasoning) and mem-h05-derived (multi-hop-derivation) have answers
# that appear nowhere in the conversation. A score ABOVE the baseline is not a
# better result, it is a bug report — see
# docs/architecture/arena-integration-lane-ceiling.md and
# docs/research/arena-prior-art-sweep-2026-07-28.md.
#
# EXIT CODES (executable-evidence convention)
#   0  every adapter met its baseline
#   1  at least one regressed (or exceeded it, which means a dirty store)
#   2  error (cannot evaluate — never reported as a pass)
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
LIVE="$REPO/arenas/live"
RUNNER_BIN="${RUNNER_BIN:-/tmp/arena-plumbing-runner}"
OUT="${OUT:-${TMPDIR:-/tmp}/arena-plumbing-gate.md}"

# Baseline DERIVED from the suite, never hardcoded.
#
# Each memory item carries `answer_source` (LongMemEval-aligned taxonomy, see
# docs/research/arena-prior-art-sweep-2026-07-28.md):
#   retrieved — the answer text is present in the conversation; an echo proves
#               the store returned it
#   computed  — the answer appears NOWHERE and must be derived (temporal
#               reasoning, arithmetic); no echo can produce it, and one that
#               did would be replaying a stored answer from an earlier run
#
# So the ceiling is a PROPERTY OF THE DATA, not a magic number someone tuned.
# Add a retrieved item and the baseline rises on its own; add a computed one and
# it does not. That is what stops this constant from becoming the "cheap green"
# gates-sin-trampa warns about: there is no number here to quietly raise.
read -r BASELINE_MEMORY EXPECTED_TOTAL < <(
  python3 - "$LIVE/tasks/suite.jsonl" <<'PYEOF'
import json, sys
retrieved = total = 0
for line in open(sys.argv[1]):
    d = json.loads(line)
    if d.get("kind") != "multi_turn_memory":
        continue
    total += 1
    if d.get("answer_source", "retrieved") == "retrieved":
        retrieved += 1
print(retrieved, total)
PYEOF
) || { echo "ERROR: could not derive the baseline from the suite — cannot evaluate" >&2; exit 2; }

if [ -z "${BASELINE_MEMORY:-}" ] || [ -z "${EXPECTED_TOTAL:-}" ]; then
  echo "ERROR: empty baseline derived from the suite — cannot evaluate" >&2
  exit 2
fi
echo "PROGRESS: baseline derived from the suite: $BASELINE_MEMORY retrieved of $EXPECTED_TOTAL memory items" >&2

MODES="${MODES:-memory}"
FRAMEWORKS="${FRAMEWORKS:-ai-sdk,eino,genkit-go,langgraph,llamaindex,mastra,pydantic-ai,spring-ai,aisotropy}"

echo "PROGRESS: building the runner" >&2
if ! (cd "$LIVE/runner" && go build -o "$RUNNER_BIN" .) 2>&1; then
  echo "ERROR: runner build failed — cannot evaluate" >&2
  exit 2
fi

# -concurrency 1 is not a performance choice. Four adapters hitting one rqlite
# simultaneously produced HTTP 503s and timeouts that render as item ERRORS,
# which read like plumbing failures and are not (2026-07-28). Sequential is the
# difference between measuring the store and measuring the load on it.
echo "PROGRESS: running plumbing dimensions on the integration lane (no model calls)" >&2
ARENA_STUB_MODEL=1 "$RUNNER_BIN" \
  -live "$LIVE" -modes "$MODES" -frameworks "$FRAMEWORKS" \
  -concurrency 1 -retry 3 -results-out "$OUT" >"${OUT}.log" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then
  echo "ERROR: the run itself failed (exit $rc) — cannot evaluate. Tail:" >&2
  tail -12 "${OUT}.log" >&2
  exit 2
fi

echo
printf '%-14s %-8s %s\n' "adapter" "memory" "verdict"
printf '%-14s %-8s %s\n' "--------------" "--------" "----------------------------------------"

fail=0
seen=0
while IFS= read -r line; do
  fw="$(echo "$line" | awk -F'|' '{gsub(/ /,"",$2); print $2}')"
  score="$(echo "$line" | awk -F'|' '{gsub(/ /,"",$4); print $4}')"
  passed="${score%%/*}"
  total="$(echo "${score#*/}" | tr -cd '0-9')"
  case "$passed" in ''|*[!0-9]*) continue ;; esac
  seen=$((seen + 1))

  if [ -n "$total" ] && [ "$total" -ne "$EXPECTED_TOTAL" ]; then
    printf '%-14s %-8s %s\n' "$fw" "$score" "ERRORED ITEMS — infrastructure, not plumbing; re-run when quiet"
    fail=$((fail + 1))
  elif [ "$passed" -lt "$BASELINE_MEMORY" ]; then
    printf '%-14s %-8s %s\n' "$fw" "$score" "REGRESSED below baseline $BASELINE_MEMORY"
    fail=$((fail + 1))
  elif [ "$passed" -gt "$BASELINE_MEMORY" ]; then
    printf '%-14s %-8s %s\n' "$fw" "$score" "ABOVE baseline — an echo cannot do arithmetic; check the store for rows from an earlier run"
    fail=$((fail + 1))
  else
    printf '%-14s %-8s %s\n' "$fw" "$score" "ok"
  fi
# Anchor on the SUMMARY row's exact shape: field 3 is literally "memory" and
# field 4 opens with N/M. Without the second condition this also matches the
# bench table and the per-item detail rows — which mention "memory" in a later
# column — and the gate then parses a latency figure as a score.
#
# That happened on this gate's first real run: both adapters were at 6/8, the
# correct baseline, and it reported "2 of 4 adapter(s) off baseline". A gate
# whose measurement is wrong manufactures failures, which is the one thing worse
# than not gating.
done < <(awk -F'|' '$0 ~ /^\|/ && $3 ~ /^ *memory *$/ && $4 ~ /^ *[0-9]+\/[0-9]+/' "$OUT" 2>/dev/null)

echo
if [ "$seen" -eq 0 ]; then
  echo "ERROR: no memory rows parsed from $OUT — the gate could not evaluate anything," >&2
  echo "       which is absence of evidence, not evidence of absence." >&2
  exit 2
fi
if [ "$fail" -gt 0 ]; then
  echo "VERDICT: $fail of $seen adapter(s) off baseline. Board: $OUT (the published"
  echo "RESULTS.md was not touched). An ABOVE-baseline score is the one to chase first:"
  echo "it means a previous run's rows are still in the store, and the echo replayed them."
  exit 1
fi
echo "VERDICT: all $seen adapter(s) at the $BASELINE_MEMORY/$EXPECTED_TOTAL ceiling — plumbing intact, zero model calls."
exit 0
