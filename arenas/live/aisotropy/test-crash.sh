#!/usr/bin/env bash
# aisotropy-live-adapter — crash_recovery scenario: does aisotropy's on-disk
# BadgerDB working memory (AISOTROPY_DATA_DIR) survive a HARD kill -9
# mid-conversation and resume, distinct from `memory` mode's clean-exit proof?
#
# Two REAL, separate OS processes against the real agnes-2.0-flash model:
#
#   process A (producer): a 2-turn conversation on one (tenant,user,session).
#     App.Turn persists each user turn to the on-disk store as part of the turn
#     (chatPersistUserTurn, BEFORE the model call). Turn 1 states a secret fact.
#     This script waits for the STDERR line proving turn 1 is committed AND turn
#     2's real model call is in-flight, then sends SIGKILL (-9) directly to the
#     process (a standalone binary, not `go run`, so the signal lands on the
#     process actually holding the BadgerDB handle).
#
#   process B (resumer): a completely fresh process (fresh PID, fresh Go
#     runtime, opened after process A is confirmed dead) that re-opens the SAME
#     AISOTROPY_DATA_DIR (BadgerDB replays its value log) and asks a question
#     only answerable by recalling the turn-1 fact.
#
# PASS requires the resumer JSON to report resumed=true,
# precrash_fact_recalled=true, state_lost=false.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

PASS=0
FAIL=0
report() {
  local name="$1" ok="$2" detail="${3:-}"
  if [ "$ok" = "0" ]; then
    echo "PASS: $name${detail:+ ($detail)}"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name${detail:+ ($detail)}"
    FAIL=$((FAIL + 1))
  fi
}

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "test-crash.sh: $ENV_FILE not found — cannot run the crash scenario" >&2
  exit 1
fi

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [ -z "${!var:-}" ]; then
    echo "test-crash.sh: required env var $var is not set" >&2
    exit 1
  fi
done

# aisotropy REAL backend tiers (mirrors run.sh) so the crashed + resumed
# processes wire the same runtime a normal run does. AISOTROPY_DATA_DIR is a
# private temp dir for this run — the on-disk store under test.
DB_DIR="$(mktemp -d)"
export AISOTROPY_EMBED_URL="${AISOTROPY_EMBED_URL:-http://127.0.0.1:8899}"
export AISOTROPY_EMBED_MODEL="${AISOTROPY_EMBED_MODEL:-multilingual-e5-small-query}"
export AISOTROPY_EMBED_DIM="${AISOTROPY_EMBED_DIM:-384}"
export QDRANT_HOST="${QDRANT_HOST:-localhost}"
export QDRANT_PORT="${QDRANT_PORT:-6334}"
export QDRANT_COLLECTION="${QDRANT_COLLECTION:-aisotropy_arena}"
export QDRANT_REST_URL="${QDRANT_REST_URL:-http://localhost:6333}"
export RQLITE_URL="${RQLITE_URL:-http://localhost:4001}"
export AISOTROPY_GUARDRAILS_BLOCK_INJECTION="${AISOTROPY_GUARDRAILS_BLOCK_INJECTION:-false}"
export AISOTROPY_DATA_DIR="$DB_DIR/wm"

SESSION_ID="crash-test-$(date +%s)-$$"
TENANT="crash-tenant-$$"
USER="crash-user"
BIN="$DB_DIR/aisotropy-crash-arena-bin"
A_STDOUT="$DB_DIR/a.stdout.json"
A_STDERR="$DB_DIR/a.stderr.log"
B_STDOUT="$DB_DIR/b.stdout.json"
B_STDERR="$DB_DIR/b.stderr.log"

cleanup() { rm -rf "$DB_DIR"; }
trap cleanup EXIT

echo "=== crash_recovery scenario: session=$SESSION_ID data_dir=$AISOTROPY_DATA_DIR ==="

# Build a real standalone binary (build.sh compiles calc.wasm + the adapter) so
# SIGKILL is guaranteed to land on the process actually holding the BadgerDB
# handle and the in-flight agnes HTTP call — not a `go run` parent.
echo "--- building aisotropy-crash-arena binary ---"
bash "$SCRIPT_DIR/build.sh" "$BIN" >&2
BUILD_EXIT=$?
[ "$BUILD_EXIT" = "0" ] && [ -x "$BIN" ]
report "binary builds" $? "exit=$BUILD_EXIT"
if [ "$BUILD_EXIT" != "0" ]; then
  echo "" && echo "=== test-crash.sh summary: $PASS passed, $FAIL failed ===" && exit 1
fi

# --- process A: 2-turn conversation, killed mid-turn-2 ---
echo "--- process A: starting (will be SIGKILLed mid-turn-2) ---"
PRECRASH_FACT="the secret checkpoint code is BLUE-FALCON-77"
REQ_A=$(cat <<JSON
{"mode":"crash_recovery","tenant_id":"$TENANT","user_id":"$USER","session_id":"$SESSION_ID","system":"You are a concise assistant. Keep replies to one short sentence.","crash_turns":["Remember this: $PRECRASH_FACT. Just acknowledge in one short sentence.","Now, separately, name one color of the rainbow. This turn is expected to be interrupted mid-flight."]}
JSON
)

"$BIN" <<<"$REQ_A" >"$A_STDOUT" 2>"$A_STDERR" &
A_PID=$!

# Wait deterministically for the synchronization line that proves turn 1 is
# already committed to disk AND turn 2's real App.Turn (model call) has begun —
# not a blind sleep. Bounded to ~60s (runtime boot happens during turn 1).
WAITED=0
TURN2_STARTED=0
while [ "$WAITED" -lt 600 ]; do
  if grep -q 'turn 2 starting' "$A_STDERR" 2>/dev/null; then
    TURN2_STARTED=1
    break
  fi
  if ! kill -0 "$A_PID" 2>/dev/null; then
    break # process A already exited (e.g. errored) before turn 2 started
  fi
  sleep 0.1
  WAITED=$((WAITED + 1))
done

[ "$TURN2_STARTED" = "1" ]
report "process A reached turn 2 (in-flight) before being killed" $?

# SIGKILL — a real hard crash: no SIGTERM, no ctx cancellation, no defer/Close()
# ever runs in process A.
kill -9 "$A_PID" 2>/dev/null
wait "$A_PID" 2>/dev/null
A_WAIT_EXIT=$?
# A killed-by-signal process reports a non-zero wait status; confirm it did NOT
# exit cleanly with 0 (which would mean it finished turn 2 before we killed it,
# invalidating the "mid-turn" claim).
[ "$A_WAIT_EXIT" != "0" ]
report "process A did not exit cleanly (was actually killed)" $?

TURN2_PERSISTED=0
if grep -q 'turn 2 persisted' "$A_STDERR" 2>/dev/null; then
  TURN2_PERSISTED=1
fi
[ "$TURN2_PERSISTED" = "0" ]
report "turn 2's assistant reply never committed (kill landed before it completed)" $?

echo "--- process A stderr (evidence) ---"
cat "$A_STDERR" >&2

# Give the OS a moment to reap the dead PID and release BadgerDB's directory lock.
sleep 1

# --- process B: fresh process, resumes and recalls ---
echo "--- process B: fresh process, resuming session ---"
REQ_B=$(cat <<JSON
{"mode":"crash_recovery","tenant_id":"$TENANT","user_id":"$USER","session_id":"$SESSION_ID","system":"You are a concise assistant. Keep replies to one short sentence.","messages":[{"role":"user","content":"What is the secret checkpoint code I told you earlier? Reply with just the code."}],"expect_substring":"BLUE-FALCON-77"}
JSON
)

"$BIN" <<<"$REQ_B" >"$B_STDOUT" 2>"$B_STDERR"
B_EXIT=$?

[ "$B_EXIT" = "0" ]
report "process B (resumer) ran successfully" $? "exit=$B_EXIT"

echo "--- process B stdout ---"
cat "$B_STDOUT"

RESUMED=$(grep -o '"resumed":[a-z]*' "$B_STDOUT" | head -1 | cut -d: -f2)
FACT_RECALLED=$(grep -o '"precrash_fact_recalled":[a-z]*' "$B_STDOUT" | head -1 | cut -d: -f2)
STATE_LOST=$(grep -o '"state_lost":[a-z]*' "$B_STDOUT" | head -1 | cut -d: -f2)

[ "$RESUMED" = "true" ]
report "resumed=true" $? "got=${RESUMED:-<missing>}"

[ "$FACT_RECALLED" = "true" ]
report "precrash_fact_recalled=true" $? "got=${FACT_RECALLED:-<missing>}"

[ "$STATE_LOST" = "false" ]
report "state_lost=false" $? "got=${STATE_LOST:-<missing>}"

echo ""
echo "=== test-crash.sh summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
