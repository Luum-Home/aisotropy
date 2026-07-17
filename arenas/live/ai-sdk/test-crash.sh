#!/usr/bin/env bash
# Crash-recovery test for the AI SDK live arena adapter's `crash_recovery` mode.
#
# Proves (or disproves) that the adapter survives a HARD kill -9 mid-conversation
# and resumes from real, networked rqlite -- a distinct claim from `memory` mode's
# two-invocation flow, which only ever proves persistence across a CLEAN process
# exit (that process always runs to completion and exits 0 before the next
# invocation starts). Nothing about `memory` mode proves survival of a crash.
#
# Scenario:
#   1. Process A ("role":"run") runs a 2-turn conversation in ONE `node index.mjs`
#      process. Turn 1 states a fact and gets checkpointed to rqlite (one atomic
#      2-row INSERT). Turn 2 starts a real agnes call.
#   2. This script polls process A's stderr PROGRESS markers and SIGKILLs it the
#      instant turn 2's agnes call has STARTED but before it can return -- so
#      turn 2 is never checkpointed.
#   3. Independent evidence is pulled straight from rqlite (not from the adapter's
#      own claims): exactly turn 1's 2 rows exist, and turn 2's topic is absent.
#   4. A FRESH `node index.mjs` process ("role":"resume", new PID) loads whatever
#      rqlite has for the same session_id and continues the conversation, proving
#      it can recall the turn-1 fact purely from the recovered rqlite history.
#
# Usage: bash arenas/live/ai-sdk/test-crash.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

PASS=0
FAIL=0
report() {
  local name="$1"
  local ok="$2"
  local detail="$3"
  if [ "$ok" = "0" ]; then
    echo "PASS  $name -- $detail"
    PASS=$((PASS + 1))
  else
    echo "FAIL  $name -- $detail"
    FAIL=$((FAIL + 1))
  fi
}

set -a
# shellcheck disable=SC1091
source "$REPO_ROOT/.env"
set +a

if [ -z "${OPENAI_API_KEY:-}" ] || [ -z "${OPENAI_MODEL:-}" ]; then
  echo "FATAL: OPENAI_API_KEY / OPENAI_MODEL not set after sourcing $REPO_ROOT/.env" >&2
  exit 2
fi

RQLITE_URL="${RQLITE_URL:-http://localhost:4001}"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

SESSION_ID="crash-test-$(date +%s)-$$"
FACT_TOKEN="Zebra-9142"
FACT_SENTENCE="The secret vault code is ${FACT_TOKEN}."

# --- Preflight: rqlite must be reachable (real infra, never mocked) ---------
RQLITE_STATUS="$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$RQLITE_URL/status" || echo "000")"
if [ "$RQLITE_STATUS" != "200" ]; then
  echo "FATAL: rqlite not reachable at $RQLITE_URL (status=$RQLITE_STATUS) -- this test requires REAL rqlite" >&2
  exit 2
fi

# --- Step 1: launch process A (role=run) in the background ------------------
INPUT_A="$WORKDIR/input-a.json"
cat > "$INPUT_A" <<EOF
{"mode":"crash_recovery","role":"run","session_id":"${SESSION_ID}","turns":["Please remember this important fact and just acknowledge it in one short sentence, nothing else: ${FACT_SENTENCE}","Now, separately, tell me one fun fact about dolphins in one short sentence."]}
EOF

OUT_A="$WORKDIR/out-a.stdout"
ERR_A="$WORKDIR/out-a.stderr"
node "$SCRIPT_DIR/index.mjs" < "$INPUT_A" > "$OUT_A" 2> "$ERR_A" &
PID_A=$!

# --- Step 2: wait for turn 1's checkpoint, then for turn 2 to START, then KILL
TIMEOUT_TICKS=600 # 60s at 0.1s/tick
waited=0
while ! grep -q "turn 1/2 committed" "$ERR_A" 2>/dev/null; do
  if ! kill -0 "$PID_A" 2>/dev/null; then break; fi
  sleep 0.1
  waited=$((waited + 1))
  if [ "$waited" -ge "$TIMEOUT_TICKS" ]; then break; fi
done
TURN1_COMMITTED_OK=1
grep -q "turn 1/2 committed" "$ERR_A" 2>/dev/null && TURN1_COMMITTED_OK=0
report "turn1-checkpoint-committed" "$TURN1_COMMITTED_OK" \
  "process A stderr shows 'turn 1/2 committed' before it is killed (see $ERR_A)"

waited=0
while ! grep -q "turn 2/2 calling agnes" "$ERR_A" 2>/dev/null; do
  if ! kill -0 "$PID_A" 2>/dev/null; then break; fi
  sleep 0.05
  waited=$((waited + 1))
  if [ "$waited" -ge $((TIMEOUT_TICKS * 2)) ]; then break; fi
done

# Small grace so the in-flight fetch() to agnes is actually on the wire, not
# merely about to be issued -- still comfortably before a real network round
# trip to agnes returns (observed 1-7s in this adapter's README).
sleep 0.15

STILL_ALIVE_OK=1
if kill -0 "$PID_A" 2>/dev/null; then STILL_ALIVE_OK=0; fi
kill -9 "$PID_A" 2>/dev/null
wait "$PID_A" 2>/dev/null
report "sigkill-delivered-mid-turn2" "$STILL_ALIVE_OK" \
  "process A (pid $PID_A) was still alive and mid-turn-2 (not yet exited) at the moment kill -9 was sent"

# --- Step 3: independent evidence straight from rqlite (not the adapter) ----
node_query() {
  # $1 = extra WHERE clause SQL fragment (with a single %s), $2... = params
  node -e "
    const [sql, ...params] = process.argv.slice(1);
    process.stdout.write(JSON.stringify([[sql, ...params]]));
  " "$@"
}

ROW_COUNT_BODY="$(node_query "SELECT COUNT(*) AS n FROM ai_sdk_arena_crash_recovery WHERE session_id = ?" "$SESSION_ID")"
RQLITE_ROWS_JSON="$WORKDIR/rqlite-rows.json"
curl -s -X POST "$RQLITE_URL/db/query?level=strong" -H 'content-type: application/json' -d "$ROW_COUNT_BODY" > "$RQLITE_ROWS_JSON"
ROW_COUNT="$(node -e "try{const d=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));process.stdout.write(String(d.results[0].values[0][0]))}catch(e){process.stdout.write('-1')}" "$RQLITE_ROWS_JSON")"
ROW_COUNT_OK=1
[ "$ROW_COUNT" = "2" ] && ROW_COUNT_OK=0
report "rqlite-has-exactly-turn1-checkpoint" "$ROW_COUNT_OK" \
  "raw rqlite row count for session = $ROW_COUNT (want 2: turn 1's user+assistant pair only -- proves turn 2 never committed)"

DOLPHIN_BODY="$(node_query "SELECT COUNT(*) AS n FROM ai_sdk_arena_crash_recovery WHERE session_id = ? AND content LIKE '%dolphin%'" "$SESSION_ID")"
DOLPHIN_JSON="$WORKDIR/rqlite-dolphin.json"
curl -s -X POST "$RQLITE_URL/db/query?level=strong" -H 'content-type: application/json' -d "$DOLPHIN_BODY" > "$DOLPHIN_JSON"
DOLPHIN_COUNT="$(node -e "try{const d=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));process.stdout.write(String(d.results[0].values[0][0]))}catch(e){process.stdout.write('-1')}" "$DOLPHIN_JSON")"
DOLPHIN_OK=1
[ "$DOLPHIN_COUNT" = "0" ] && DOLPHIN_OK=0
report "turn2-in-flight-data-absent" "$DOLPHIN_OK" \
  "rows mentioning turn 2's topic ('dolphin') for this session = $DOLPHIN_COUNT (want 0: the in-flight turn was truly never persisted, not a lucky timing race)"

# --- Step 4: process B (role=resume), a FRESH process, same session_id ------
INPUT_B="$WORKDIR/input-b.json"
cat > "$INPUT_B" <<EOF
{"mode":"crash_recovery","role":"resume","session_id":"${SESSION_ID}","continuation":"What was the secret vault code I told you earlier? Reply with just the code, nothing else.","expected_fact":"${FACT_TOKEN}"}
EOF

OUT_B="$WORKDIR/out-b.stdout"
ERR_B="$WORKDIR/out-b.stderr"
node "$SCRIPT_DIR/index.mjs" < "$INPUT_B" > "$OUT_B" 2> "$ERR_B"

echo ""
echo "process B (role=resume) JSON output:"
cat "$OUT_B"
echo ""

get_field() {
  node -e "try{const o=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));const v=o[process.argv[2]];process.stdout.write(typeof v==='string'?v:JSON.stringify(v))}catch(e){process.stdout.write('PARSE_FAILED')}" "$OUT_B" "$1"
}

B_ERROR="$(get_field error)"
B_RESUMED="$(get_field resumed)"
B_STATE_LOST="$(get_field state_lost)"
B_FACT_RECALLED="$(get_field precrash_fact_recalled)"

ERROR_OK=1
[ -z "$B_ERROR" ] && ERROR_OK=0
report "process-b-no-error" "$ERROR_OK" "error field = '$B_ERROR' (want empty)"

RESUMED_OK=1
[ "$B_RESUMED" = "true" ] && RESUMED_OK=0
report "resumed-true" "$RESUMED_OK" \
  "resumed = $B_RESUMED (want true: a FRESH process loaded rqlite state after process A was killed)"

STATE_LOST_OK=1
[ "$B_STATE_LOST" = "false" ] && STATE_LOST_OK=0
report "state-lost-false" "$STATE_LOST_OK" \
  "state_lost = $B_STATE_LOST (want false: the checkpointed turn-1 fact was NOT lost -- only the never-committed in-flight turn 2 was discarded)"

RECALLED_OK=1
[ "$B_FACT_RECALLED" = "true" ] && RECALLED_OK=0
report "precrash-fact-recalled-true" "$RECALLED_OK" \
  "precrash_fact_recalled = $B_FACT_RECALLED (want true: agnes actually used the recovered fact in a real completion, not just 'it's in the DB')"

echo ""
echo "SUMMARY: $PASS passed, $FAIL failed"
if [ "$FAIL" -eq 0 ]; then
  exit 0
else
  exit 1
fi
