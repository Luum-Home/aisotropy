#!/usr/bin/env bash
# Automated PASS/FAIL test for the genkit-go live arena adapter's
# crash_recovery mode — proves REAL crash durability, not just clean-exit
# persistence, against a real rqlite node (RQLITE_URL, default
# http://localhost:4001) and real agnes calls.
#
# Sequence:
#   (1) process A, turn 1: states a fact, runs to completion, CHECKPOINTS to
#       rqlite normally.
#   (2) process A, turn 2: launched in the background, then SIGKILLed
#       (kill -9) the moment its stderr shows it has entered genkit.Generate
#       — i.e. strictly AFTER the turn-1 checkpoint, strictly BEFORE turn 2's
#       own checkpoint could ever happen. This is a real hard-crash mid-turn,
#       not a simulated one.
#   (3) process B: a completely FRESH process invocation, same session_id,
#       asks for the turn-1 fact back. It can only answer correctly by having
#       re-hydrated the turn-1 checkpoint from rqlite — its own input never
#       mentions the fact.
#
# Prints PASS/FAIL per assertion and exits non-zero if any assertion failed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

FAILED=0
pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAILED=1; }

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "FAIL: repo-root .env not found at ${ENV_FILE}"
  exit 1
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "FAIL: OPENAI_API_KEY not set after sourcing .env — cannot run tests"
  exit 1
fi
KEY="${OPENAI_API_KEY}"

if ! command -v jq >/dev/null 2>&1; then
  echo "FAIL: jq is required to parse adapter output and is not on PATH"
  exit 1
fi

RQLITE_URL_CHECK="${RQLITE_URL:-http://localhost:4001}"
if ! curl -s -m 3 -o /dev/null -w '' "${RQLITE_URL_CHECK}/status"; then
  echo "FAIL: rqlite is not reachable at ${RQLITE_URL_CHECK} — crash_recovery mode requires a real, running rqlite node"
  exit 1
fi

# Build once up front (fast no-op if already built) so the background turn-2
# invocation below doesn't spend its uncommitted-window budget compiling.
( cd "${SCRIPT_DIR}" && go build -o ./genkit-go-live-arena . ) 1>&2

RUN_ID="$(date +%s)-$$"
SESSION_ID="crash-test-${RUN_ID}"
FACT_CODE="DELTA-${RUN_ID}"

TURN1_STDERR="/tmp/genkit-go-test-crash-turn1-${RUN_ID}.stderr"
TURN2_STDOUT="/tmp/genkit-go-test-crash-turn2-${RUN_ID}.stdout"
TURN2_STDERR="/tmp/genkit-go-test-crash-turn2-${RUN_ID}.stderr"
TURN3_STDERR="/tmp/genkit-go-test-crash-turn3-${RUN_ID}.stderr"
cleanup() { rm -f "${TURN1_STDERR}" "${TURN2_STDOUT}" "${TURN2_STDERR}" "${TURN3_STDERR}"; }
trap cleanup EXIT

# --------------------------------------------------------------------------
# (1) Turn 1 — states the fact, runs to completion, checkpoints normally.
# --------------------------------------------------------------------------
echo "--- (1) turn 1: state the fact, run to completion, checkpoint ---"

TURN1_OUT="$(echo "{\"mode\":\"crash_recovery\",\"session_id\":\"${SESSION_ID}\",\"messages\":[{\"role\":\"user\",\"content\":\"Remember this secret launch code: ${FACT_CODE}. Reply with just: Understood, I will remember ${FACT_CODE}.\"}]}" \
  | bash "${SCRIPT_DIR}/run.sh" 2>"${TURN1_STDERR}")"
TURN1_STATUS=$?

if [[ ${TURN1_STATUS} -eq 0 ]] && echo "${TURN1_OUT}" | jq -e '.error == ""' >/dev/null 2>&1; then
  pass "turn 1: smoke call succeeded (error field empty)"
else
  fail "turn 1: smoke call failed (status=${TURN1_STATUS}, output=${TURN1_OUT})"
fi

TURN1_CHECKPOINTED="$(echo "${TURN1_OUT}" | jq -r 'if .checkpointed == null then "null" else (.checkpointed|tostring) end')"
if [[ "${TURN1_CHECKPOINTED}" == "true" ]]; then
  pass "turn 1: checkpointed=true (turn 1 durably written before turn 2 begins)"
else
  fail "turn 1: checkpointed=${TURN1_CHECKPOINTED} (expected true)"
fi

TURN1_RESUMED="$(echo "${TURN1_OUT}" | jq -r 'if .resumed == null then "null" else (.resumed|tostring) end')"
if [[ "${TURN1_RESUMED}" == "false" ]]; then
  pass "turn 1: resumed=false (fresh session, nothing to resume yet)"
else
  fail "turn 1: resumed=${TURN1_RESUMED} (expected false — this session_id is unique to this run)"
fi

# --------------------------------------------------------------------------
# (2) Turn 2 — launched, then SIGKILLed strictly mid-flight (after entering
#     genkit.Generate, before any checkpoint write for this turn).
# --------------------------------------------------------------------------
echo "--- (2) turn 2: launch, wait for mid-flight marker, SIGKILL ---"

echo "{\"mode\":\"crash_recovery\",\"session_id\":\"${SESSION_ID}\",\"messages\":[{\"role\":\"user\",\"content\":\"Now tell me a short story about a train crossing the Alps.\"}]}" \
  | bash "${SCRIPT_DIR}/run.sh" >"${TURN2_STDOUT}" 2>"${TURN2_STDERR}" &
TURN2_PID=$!

# Bounded poll (up to 10s) for the exact uncommitted window: the
# "entering genkit.Generate" marker is written to stderr AFTER turn 2's load
# from rqlite (read-only) and BEFORE its checkpoint write — see
# crash_recovery.go. Killing here guarantees turn 2 never reaches rqlite.
WAITED_MS=0
while ! grep -q "entering genkit.Generate" "${TURN2_STDERR}" 2>/dev/null; do
  if ! kill -0 "${TURN2_PID}" 2>/dev/null; then
    break
  fi
  sleep 0.2
  WAITED_MS=$((WAITED_MS + 200))
  if [[ ${WAITED_MS} -ge 10000 ]]; then
    break
  fi
done

if grep -q "entering genkit.Generate" "${TURN2_STDERR}" 2>/dev/null && kill -0 "${TURN2_PID}" 2>/dev/null; then
  pass "turn 2: caught mid-flight (uncommitted — inside genkit.Generate, before any checkpoint write)"
  kill -9 "${TURN2_PID}" 2>/dev/null
  wait "${TURN2_PID}" 2>/dev/null
  TURN2_WAIT_STATUS=$?
  if [[ ${TURN2_WAIT_STATUS} -ne 0 ]]; then
    pass "turn 2: process did not exit cleanly after SIGKILL (wait status=${TURN2_WAIT_STATUS}, expected non-zero)"
  else
    fail "turn 2: process reported a clean (zero) exit status after SIGKILL — expected non-zero"
  fi
else
  fail "turn 2: never observed the mid-flight marker before timing out or exiting on its own — cannot prove a genuine hard-crash mid-turn"
  kill -9 "${TURN2_PID}" 2>/dev/null
  wait "${TURN2_PID}" 2>/dev/null || true
fi

if [[ ! -s "${TURN2_STDOUT}" ]]; then
  pass "turn 2: stdout is empty (the process never reached its final JSON emit — proves it died mid-flight, not after success)"
else
  fail "turn 2: stdout is non-empty (${TURN2_STDOUT} has content) — the process may have completed before being killed"
fi

if echo "$(cat "${TURN2_STDOUT}" 2>/dev/null)$(cat "${TURN2_STDERR}" 2>/dev/null)" | grep -qF "${KEY}"; then
  fail "turn 2: stdout/stderr contains the literal API key"
else
  pass "turn 2: stdout/stderr does not contain the literal API key"
fi

# --------------------------------------------------------------------------
# (3) Turn 3 — a FRESH process (process B). Its own input never mentions the
#     fact; it must resume it from rqlite's turn-1 checkpoint alone.
# --------------------------------------------------------------------------
echo "--- (3) turn 3: fresh process, resume, recall the pre-crash fact ---"

TURN3_OUT="$(echo "{\"mode\":\"crash_recovery\",\"session_id\":\"${SESSION_ID}\",\"fact\":\"${FACT_CODE}\",\"messages\":[{\"role\":\"user\",\"content\":\"What secret launch code did I give you earlier? Reply with just the code, nothing else.\"}]}" \
  | bash "${SCRIPT_DIR}/run.sh" 2>"${TURN3_STDERR}")"
TURN3_STATUS=$?

if [[ ${TURN3_STATUS} -eq 0 ]] && echo "${TURN3_OUT}" | jq -e '.error == ""' >/dev/null 2>&1; then
  pass "turn 3: smoke call succeeded (error field empty)"
else
  fail "turn 3: smoke call failed (status=${TURN3_STATUS}, output=${TURN3_OUT})"
fi

RESUMED="$(echo "${TURN3_OUT}" | jq -r 'if .resumed == null then "null" else (.resumed|tostring) end')"
if [[ "${RESUMED}" == "true" ]]; then
  pass "turn 3: resumed=true (loaded checkpointed state left behind by an earlier, now-dead process)"
else
  fail "turn 3: resumed=${RESUMED} (expected true)"
fi

PRIOR_LOADED="$(echo "${TURN3_OUT}" | jq -r '.prior_turns_loaded // "null"')"
if [[ "${PRIOR_LOADED}" == "2" ]]; then
  pass "turn 3: prior_turns_loaded=2 (exactly turn 1's user+assistant rows — turn 2 contributed zero, as expected)"
else
  fail "turn 3: prior_turns_loaded=${PRIOR_LOADED} (expected 2)"
fi

PRECRASH_RECALLED="$(echo "${TURN3_OUT}" | jq -r 'if .precrash_fact_recalled == null then "null" else (.precrash_fact_recalled|tostring) end')"
if [[ "${PRECRASH_RECALLED}" == "true" ]]; then
  pass "turn 3: precrash_fact_recalled=true"
else
  fail "turn 3: precrash_fact_recalled=${PRECRASH_RECALLED} (expected true)"
fi

STATE_LOST="$(echo "${TURN3_OUT}" | jq -r 'if .state_lost == null then "null" else (.state_lost|tostring) end')"
if [[ "${STATE_LOST}" == "false" ]]; then
  pass "turn 3: state_lost=false"
else
  fail "turn 3: state_lost=${STATE_LOST} (expected false)"
fi

REPLY="$(echo "${TURN3_OUT}" | jq -r '.reply // ""')"
if echo "${REPLY}" | grep -qF "${FACT_CODE}"; then
  pass "turn 3: reply text independently contains the fact code (${FACT_CODE}) — cross-checked outside the adapter's own self-report"
else
  fail "turn 3: reply text does NOT contain the fact code (${FACT_CODE}) — reply was: ${REPLY}"
fi

if echo "${TURN3_OUT}" | grep -qF "${KEY}"; then
  fail "turn 3: stdout contains the literal API key"
else
  pass "turn 3: stdout does not contain the literal API key"
fi
if grep -qF "${KEY}" "${TURN3_STDERR}" 2>/dev/null; then
  fail "turn 3: stderr contains the literal API key"
else
  pass "turn 3: stderr does not contain the literal API key"
fi

echo "---"
echo "FINAL_JSON: ${TURN3_OUT}"
echo "---"
if [[ ${FAILED} -eq 0 ]]; then
  echo "RESULT: ALL PASS"
else
  echo "RESULT: FAILURES PRESENT"
fi
exit ${FAILED}
