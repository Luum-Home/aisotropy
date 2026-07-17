#!/usr/bin/env bash
# test-crash.sh — proves (or disproves) whether the LlamaIndex live-arena adapter's on-disk
# Memory store survives a HARD crash (`kill -9`) mid-conversation, as distinct from the
# `memory` mode's already-verified clean-process-exit recall (README.md "Acceptance evidence
# #5").
#
# Scenario:
#   1. process A (role=process_a) runs a 2-turn conversation on a fresh session_id, persisting
#      each turn to the REAL on-disk SQLite store `memory` mode uses (`llama_index.core.memory.
#      Memory`, `Memory.aput()` -> `await session.commit()`). Turn 1 states a fact.
#   2. The instant turn 1's commit is durable, process A logs `CHECKPOINT_TURN_1_COMMITTED` to
#      stderr and enters a deterministic `inject_delay_ms` sleep (a documented test hook, see
#      adapter.py `run_crash_recovery` docstring) BEFORE starting turn 2 — this script watches
#      stderr for that line, then sends a REAL `kill -9` partway through the sleep window, i.e.
#      strictly after turn 1 is durable and strictly before turn 2 could possibly commit.
#   3. A FRESH process B (role=process_b, separate PID, separate Python interpreter) reopens the
#      SAME on-disk db and must recall the turn-1 fact through a real, live agnes continuation
#      turn.
#
# PASS requires ALL of:
#   - process A produced NO stdout (a true kill -9 gives no chance to flush/emit anything —
#     any stdout at all would mean the kill landed too late, i.e. a harness timing failure)
#   - process A's exit reflects a REAL SIGKILL (wait status 137 = 128+9)
#   - process B's JSON: resumed=true, precrash_fact_recalled=true, state_lost=false,
#     turn2_committed=false (the crashed turn correctly left NO partial trace)
#
# Usage: bash test-crash.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

PASS=0
FAIL=0
pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

if [[ ! -f "$ENV_FILE" ]]; then
  fail "(setup) $ENV_FILE not found — cannot run crash-recovery test"
  echo "" && echo "=== Summary: $PASS passed, $FAIL failed ===" && exit 1
fi
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a
for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [[ -z "${!var:-}" ]]; then
    fail "(setup) $var not set in $ENV_FILE"
    echo "" && echo "=== Summary: $PASS passed, $FAIL failed ===" && exit 1
  fi
done

if [[ ! -x "$SCRIPT_DIR/.venv/bin/python3" ]]; then
  fail "(setup) venv missing at $SCRIPT_DIR/.venv — run the one-time setup in README.md"
  echo "" && echo "=== Summary: $PASS passed, $FAIL failed ===" && exit 1
fi

LOG_DIR="$SCRIPT_DIR/.memory/crash-test-logs"
mkdir -p "$LOG_DIR"
SESSION_ID="crash-demo-$(date +%s)-$$"
DB_PATH="$SCRIPT_DIR/.memory/${SESSION_ID}.db"
A_OUT="$LOG_DIR/${SESSION_ID}.process_a.stdout.json"
A_ERR="$LOG_DIR/${SESSION_ID}.process_a.stderr.log"
B_OUT="$LOG_DIR/${SESSION_ID}.process_b.stdout.json"
B_ERR="$LOG_DIR/${SESSION_ID}.process_b.stderr.log"
: >"$A_OUT"; : >"$A_ERR"; : >"$B_OUT"; : >"$B_ERR"

FACT_MARKER="Vireo T7 lucky number 47"
TURN2_MARKER="BLUE-FALCON-9142-NEVER-PERSISTED"
INJECT_DELAY_MS=6000

echo "=== crash_recovery: session_id=${SESSION_ID} ==="
echo "1) launching process_a (2 turns; turn 1 states the fact, turn 2 carries a marker that must NOT survive the crash)"

PAYLOAD_A=$(cat <<JSON
{
  "mode": "crash_recovery",
  "role": "process_a",
  "session_id": "${SESSION_ID}",
  "crash_after_turn": 1,
  "inject_delay_ms": ${INJECT_DELAY_MS},
  "messages": [
    {"role": "user", "content": "Remember this fact for later: my favorite trainset is the ${FACT_MARKER}."},
    {"role": "user", "content": "This is turn two. The marker phrase is ${TURN2_MARKER}. Say OK."}
  ]
}
JSON
)

echo "$PAYLOAD_A" | bash "$SCRIPT_DIR/run.sh" >"$A_OUT" 2>"$A_ERR" &
A_PID=$!
echo "   process_a pid=${A_PID}"

echo "2) waiting for CHECKPOINT_TURN_1_COMMITTED on process_a's stderr (turn 1 durably committed)..."
CHECKPOINT_SEEN=0
for _ in $(seq 1 300); do  # up to 60s (300 * 0.2s) — turn 1 is one real agnes call, ~7s typical
  if grep -q "CHECKPOINT_TURN_1_COMMITTED" "$A_ERR" 2>/dev/null; then
    CHECKPOINT_SEEN=1
    break
  fi
  if ! kill -0 "$A_PID" 2>/dev/null; then
    break  # process exited on its own (error) before checkpointing — stop polling
  fi
  sleep 0.2
done

if [[ "$CHECKPOINT_SEEN" -eq 1 ]]; then
  pass "(1) process_a durably committed turn 1 before any kill (CHECKPOINT_TURN_1_COMMITTED observed)"
else
  fail "(1) process_a never reached CHECKPOINT_TURN_1_COMMITTED — see $A_ERR"
  echo "--- process_a stderr tail ---"; tail -20 "$A_ERR"
  kill -9 "$A_PID" 2>/dev/null
  wait "$A_PID" 2>/dev/null
  echo "" && echo "=== Summary: $PASS passed, $FAIL failed ===" && exit 1
fi

echo "3) inside the ${INJECT_DELAY_MS}ms crash-injection window now — sending SIGKILL to process_a (pid=${A_PID})"
sleep 1  # comfortably inside the 6s window, well before turn 2 could start any I/O
kill -9 "$A_PID" 2>/dev/null
wait "$A_PID" 2>/dev/null
A_WAIT_STATUS=$?

if [[ "$A_WAIT_STATUS" -eq 137 ]]; then
  pass "(2) process_a's wait status is 137 (128+SIGKILL) — a real kill -9 landed"
else
  fail "(2) process_a's wait status was ${A_WAIT_STATUS}, expected 137 (128+SIGKILL)"
fi

if [[ ! -s "$A_OUT" ]]; then
  pass "(3) process_a produced ZERO stdout — proves the kill landed before any envelope was emitted (a true hard crash, not a graceful error exit)"
else
  fail "(3) process_a produced stdout despite being killed — kill landed too late: $(cat "$A_OUT")"
fi

echo "4) launching FRESH process_b to resume session ${SESSION_ID}"

PAYLOAD_B=$(cat <<JSON
{
  "mode": "crash_recovery",
  "role": "process_b",
  "session_id": "${SESSION_ID}",
  "expected_fact_substring": "${FACT_MARKER}",
  "turn2_marker_substring": "${TURN2_MARKER}",
  "query": "What did I tell you to remember earlier? Repeat the exact fact back to me."
}
JSON
)

echo "$PAYLOAD_B" | bash "$SCRIPT_DIR/run.sh" >"$B_OUT" 2>"$B_ERR"
B_STATUS=$?

if [[ "$B_STATUS" -ne 0 ]] || ! "$SCRIPT_DIR/.venv/bin/python3" -c "import json,sys; json.load(open('$B_OUT'))" 2>/dev/null; then
  fail "(4) process_b did not exit 0 with valid JSON on stdout (status=$B_STATUS) — see $B_OUT / $B_ERR"
  echo "--- process_b stderr tail ---"; tail -20 "$B_ERR"
  echo "" && echo "=== Summary: $PASS passed, $FAIL failed ===" && exit 1
fi
pass "(4) process_b exited 0 with valid JSON on stdout"

PYOUT=$("$SCRIPT_DIR/.venv/bin/python3" - "$B_OUT" <<'PYSCRIPT'
import json, sys
d = json.load(open(sys.argv[1]))
reply = (d.get("reply") or "").replace("\n", " ")[:80]
print(d.get("resumed"), d.get("precrash_fact_recalled"), d.get("state_lost"), d.get("turn2_committed"), reply)
PYSCRIPT
)
read -r RESUMED PRECRASH_RECALLED STATE_LOST TURN2_COMMITTED REPLY_SNIPPET <<< "$PYOUT"

echo "   process_b: resumed=${RESUMED} precrash_fact_recalled=${PRECRASH_RECALLED} state_lost=${STATE_LOST} turn2_committed=${TURN2_COMMITTED}"
echo "   process_b reply: ${REPLY_SNIPPET}"

[[ "$RESUMED" == "True" ]] && pass "(5) resumed=true — process_b successfully loaded pre-crash session state from disk" \
  || fail "(5) resumed=${RESUMED}, expected true"

[[ "$PRECRASH_RECALLED" == "True" ]] && pass "(6) precrash_fact_recalled=true — turn-1 fact present in BOTH the recovered on-disk history AND a live agnes reply" \
  || fail "(6) precrash_fact_recalled=${PRECRASH_RECALLED}, expected true"

[[ "$STATE_LOST" == "False" ]] && pass "(7) state_lost=false — the durably-committed turn-1 fact was NOT lost" \
  || fail "(7) state_lost=${STATE_LOST}, expected false"

[[ "$TURN2_COMMITTED" == "False" ]] && pass "(8) turn2_committed=false — the crashed turn left no partial trace on disk (correct crash-safety, not corruption)" \
  || fail "(8) turn2_committed=${TURN2_COMMITTED}, expected false (crash window did not land before turn 2's commit)"

if command -v sqlite3 >/dev/null 2>&1 && [[ -f "$DB_PATH" ]]; then
  # SQLAlchemyChatStore's table schema is (id, key, timestamp, role, status, data) — the
  # partition column is named "key", not "session_id" (llama_index/core/storage/chat_store/
  # sql.py::_setup_tables). Memory.from_defaults(session_id=...) passes that value through as
  # the store's "key".
  ROW_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM llama_index_memory WHERE key='${SESSION_ID}';" 2>/dev/null || echo "ERR")
  echo "   independent check: sqlite3 row count for this session_id = ${ROW_COUNT}"
  # Expect exactly 4 rows: turn-1 user + turn-1 assistant + process_b's continuation user + reply.
  # Turn 2 (user+assistant) must be ABSENT -> 4, not 6.
  if [[ "$ROW_COUNT" == "4" ]]; then
    pass "(9) independent sqlite3 query confirms exactly 4 rows (turn 1 + process_b's continuation only, turn 2 absent)"
  else
    fail "(9) independent sqlite3 query found ${ROW_COUNT} rows, expected 4"
  fi
else
  echo "   (sqlite3 CLI not available or db file missing — skipping independent row-count check, non-fatal)"
fi

echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
