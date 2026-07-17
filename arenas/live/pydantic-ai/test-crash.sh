#!/usr/bin/env bash
# Automated PASS/FAIL assertions for the `crash_recovery` mode: does
# Pydantic AI's persisted `message_history` survive a HARD `kill -9` mid
# conversation, or only a clean process exit?
#
# Scenario:
#   (1) "process A" runs a 2-turn conversation on one session_id via
#       mode=crash_recovery/turns=[...], IN-PROCESS, checkpointing to REAL
#       rqlite (http://localhost:4001) after every turn. Turn 1 states a
#       fact. `run.sh` execs python directly (no subshell), so $! is the
#       actual interpreter PID -- SIGKILL lands on the process itself.
#   (2) This script watches process A's stderr for the turn-1 checkpoint
#       PROGRESS line, then immediately `kill -9`s it -- turn 2's real model
#       call is in flight (real agnes latency is multi-second, see README's
#       evidence numbers), so turn 2 never reaches its own checkpoint.
#   (3) A FRESH process B (mode=crash_recovery/query=...) loads whatever
#       actually persisted in rqlite for that session_id and asks a question
#       that can only be answered by recalling turn 1's fact.
#
# Real agnes calls: turn 1 (completes) + turn 2 (started, killed in flight)
# + process B's recovery turn = 3 calls per run.
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

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "FATAL: no .env found at $ENV_FILE" >&2
  exit 1
fi

# Unique suffix per run so repeated executions don't collide on session_id
# rows a prior run left behind in the shared rqlite store.
RUN_ID="$(date +%s)-$$"
SESSION_ID="crash-test-${RUN_ID}"
CODENAME="Obsidian-Falcon-${RUN_ID}"
FACT="The backup recovery codename for run ${RUN_ID} is ${CODENAME}."

echo "=== (0) sanity: rqlite is reachable at the configured RQLITE_URL ==="
RQLITE_URL_EFFECTIVE="${RQLITE_URL:-http://localhost:4001}"
if curl -s -m 3 -o /dev/null -w '%{http_code}' "$RQLITE_URL_EFFECTIVE/status" | grep -q '^200$'; then
  pass "(0) rqlite reachable at $RQLITE_URL_EFFECTIVE"
else
  fail "(0) rqlite NOT reachable at $RQLITE_URL_EFFECTIVE -- aborting"
  echo "=== RESULT: $PASS passed, $FAIL failed ==="
  exit 1
fi

echo
echo "=== (1) process A: launch 2-turn conversation in background, checkpointing after each turn ==="

PRECRASH_ERR="$(mktemp)"
PRECRASH_PAYLOAD="$(mktemp)"
python3 -c "
import json
print(json.dumps({
    'mode': 'crash_recovery',
    'session_id': '$SESSION_ID',
    'turns': [
        'Remember this fact for later: $FACT',
        'Now, separately, tell me a two-sentence fun fact about maglev trains.',
    ],
}))
" > "$PRECRASH_PAYLOAD"

bash "$SCRIPT_DIR/run.sh" < "$PRECRASH_PAYLOAD" > /dev/null 2> "$PRECRASH_ERR" &
PRECRASH_PID=$!
echo "process A pid=$PRECRASH_PID (watching $PRECRASH_ERR for the turn-1 checkpoint)"

TURN1_CHECKPOINT="checkpoint: turn 1/2 persisted"
TURN2_CHECKPOINT="checkpoint: turn 2/2 persisted"
WAITED=0
MAX_WAIT_TICKS=300  # 300 * 0.1s = 30s ceiling
while ! grep -qF "$TURN1_CHECKPOINT" "$PRECRASH_ERR" 2>/dev/null; do
  if ! kill -0 "$PRECRASH_PID" 2>/dev/null; then
    fail "(1) process A exited before the turn-1 checkpoint ever appeared -- see $PRECRASH_ERR"
    cat "$PRECRASH_ERR" >&2
    rm -f "$PRECRASH_ERR" "$PRECRASH_PAYLOAD"
    echo "=== RESULT: $PASS passed, $FAIL failed ==="
    exit 1
  fi
  sleep 0.1
  WAITED=$((WAITED + 1))
  if [[ "$WAITED" -ge "$MAX_WAIT_TICKS" ]]; then
    fail "(1) turn-1 checkpoint never appeared within 30s -- see $PRECRASH_ERR"
    kill -9 "$PRECRASH_PID" 2>/dev/null
    wait "$PRECRASH_PID" 2>/dev/null
    rm -f "$PRECRASH_ERR" "$PRECRASH_PAYLOAD"
    echo "=== RESULT: $PASS passed, $FAIL failed ==="
    exit 1
  fi
done
pass "(1) process A checkpointed turn 1 (fact persisted to rqlite) before any kill was sent"

echo
echo "=== (2) SIGKILL process A immediately -- turn 2's real model call is in flight, never checkpoints ==="
kill -9 "$PRECRASH_PID" 2>/dev/null
wait "$PRECRASH_PID" 2>/dev/null
KILL_WAIT_STATUS=$?
if kill -0 "$PRECRASH_PID" 2>/dev/null; then
  fail "(2) process A ($PRECRASH_PID) is still alive after kill -9"
else
  pass "(2) process A ($PRECRASH_PID) is confirmed dead after kill -9 (wait exit status=$KILL_WAIT_STATUS, expected non-zero/killed)"
fi

if grep -qF "$TURN2_CHECKPOINT" "$PRECRASH_ERR" 2>/dev/null; then
  echo "NOTE: turn 2 checkpointed before the kill signal landed (timing race) -- recovery check below still valid either way"
else
  pass "(2a) turn 2 never checkpointed -- confirms the kill landed mid-turn-2, before its persist"
fi

echo
echo "=== (3) FRESH process B: load whatever survived, continue, must recall the turn-1 fact ==="

RECOVER_OUT="$(mktemp)"
RECOVER_ERR="$(mktemp)"
RECOVER_PAYLOAD="$(mktemp)"
python3 -c "
import json
print(json.dumps({
    'mode': 'crash_recovery',
    'session_id': '$SESSION_ID',
    'query': 'What backup recovery codename did I ask you to remember earlier in this conversation? Reply with just the codename.',
    'expect_fact': '$CODENAME',
}))
" > "$RECOVER_PAYLOAD"

bash "$SCRIPT_DIR/run.sh" < "$RECOVER_PAYLOAD" > "$RECOVER_OUT" 2> "$RECOVER_ERR"
RECOVER_EXIT=$?

if [[ "$RECOVER_EXIT" -ne 0 ]]; then
  fail "(3) process B (recovery) exited non-zero ($RECOVER_EXIT) -- see $RECOVER_OUT / $RECOVER_ERR"
else
  RECOVER_ERROR_FIELD="$(python3 -c "import json; print(json.load(open('$RECOVER_OUT')).get('error',''))" 2>/dev/null)"
  if [[ -n "$RECOVER_ERROR_FIELD" ]]; then
    fail "(3) process B returned a non-empty error field: $RECOVER_ERROR_FIELD -- see $RECOVER_OUT"
  else
    RESUMED="$(python3 -c "import json; print(json.load(open('$RECOVER_OUT'))['resumed'])")"
    FACT_RECALLED="$(python3 -c "import json; print(json.load(open('$RECOVER_OUT'))['precrash_fact_recalled'])")"
    STATE_LOST="$(python3 -c "import json; print(json.load(open('$RECOVER_OUT'))['state_lost'])")"
    REPLY="$(python3 -c "import json; print(json.load(open('$RECOVER_OUT'))['reply'])")"

    echo "process B reply: $REPLY"

    if [[ "$RESUMED" == "True" ]]; then
      pass "(3a) resumed=true -- fresh process B found and loaded process A's pre-crash persisted history"
    else
      fail "(3a) resumed=false -- process B found NO persisted history for session_id=$SESSION_ID -- see $RECOVER_OUT"
    fi

    if [[ "$STATE_LOST" == "False" ]]; then
      pass "(3b) state_lost=false -- the turn-1 checkpoint survived the SIGKILL"
    else
      fail "(3b) state_lost=true -- pre-crash state did NOT survive the SIGKILL -- see $RECOVER_OUT"
    fi

    if [[ "$FACT_RECALLED" == "True" ]]; then
      pass "(3c) precrash_fact_recalled=true -- process B's reply recalls the fact stated in turn 1, before the crash"
    else
      fail "(3c) precrash_fact_recalled=false -- process B's reply does NOT contain the turn-1 fact -- see $RECOVER_OUT"
    fi
  fi
fi

echo
echo "=== JSON result (process B / recovery) ==="
cat "$RECOVER_OUT" 2>/dev/null
echo

rm -f "$PRECRASH_ERR" "$PRECRASH_PAYLOAD" "$RECOVER_OUT" "$RECOVER_ERR" "$RECOVER_PAYLOAD"

echo
echo "=== RESULT: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
