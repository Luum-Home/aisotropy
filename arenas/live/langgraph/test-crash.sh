#!/usr/bin/env bash
# Hard-crash recovery test for the LangGraph live-arena adapter's
# `crash_recovery` mode — distinct from `memory` mode / test-isolation.sh's
# memory_scoped tests, both of which only ever exercise CLEAN process exits.
#
# Scenario (real infra throughout — real agnes calls, real on-disk SQLite
# checkpointer via langgraph.checkpoint.sqlite.SqliteSaver):
#   1. Process A (`op=converse`) drives a 2-turn conversation on one
#      thread_id: turn 1 states a fact and gets its own checkpoint durably
#      committed to disk (its own `with SqliteSaver...` block closes before
#      turn 2 starts). Turn 2 is a real agnes call left running.
#   2. This script waits for the stderr `PROGRESS: ... turn 1/2
#      checkpointed` line (proof turn 1 is durably on disk), then sends
#      SIGKILL (`kill -9`) to process A while it is still blocked on turn
#      2's real network call — i.e. AFTER turn 1's checkpoint, MID turn 2,
#      never a clean exit.
#   3. Process B (`op=resume`, a brand-new OS process with zero in-memory
#      knowledge of process A) opens the SAME on-disk checkpoint file for
#      the SAME thread_id and asks a question that can only be answered
#      correctly by recalling turn 1's fact.
#
# Assertions (each independently reported PASS/FAIL):
#   (1) process A was genuinely killed mid-flight, not cleanly exited:
#       its stdout capture is empty (it never reached its own final
#       print of a result envelope) AND the process is confirmed dead.
#   (2) process B resumed successfully: no error in its envelope.
#   (3) process B's reply recalls the pre-crash (turn 1) fact.
#   (4) turn 2 (the crashed turn) left no trace in process B's context:
#       process B's answer is short and does not contain unrelated
#       content that would imply turn 2 had somehow completed and been
#       checkpointed anyway.
#
# Emits ONE final JSON object on stdout (in addition to the PASS/FAIL log
# lines) shaped as:
#   {"framework":"langgraph","mode":"crash_recovery","resumed":bool,
#    "precrash_fact_recalled":bool,"state_lost":bool,"reply":str, ...}
#
# Never prints the OPENAI_API_KEY.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

FAIL=0

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
else
  echo "warning: ${ENV_FILE} not found; relying on inherited environment" >&2
fi

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required to run this script"; exit 1; }

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "FAIL: OPENAI_API_KEY is not set; cannot run the real agnes calls this test requires"
  exit 1
fi

CRASH_DB="${SCRIPT_DIR}/.data/checkpoints_crash.sqlite"
rm -f "${CRASH_DB}"

THREAD_ID="crash-thread-$(date +%s)-$$"
SECRET="FALCON-77-VERDE"
STDOUT_A="$(mktemp)"
STDERR_A="$(mktemp)"
trap 'rm -f "${STDOUT_A}" "${STDERR_A}"' EXIT

echo "=== process A (op=converse): 2-turn conversation, thread_id=${THREAD_ID} ==="
CONVERSE_REQ=$(jq -n --arg tid "${THREAD_ID}" --arg t1 "Remember this exactly: the secret launch code is ${SECRET}. Reply with just the word OK to confirm." --arg t2 "Now, in one short sentence, tell me an interesting fact about deep sea anglerfish." \
  '{mode:"crash_recovery", op:"converse", thread_id:$tid, turns:[$t1,$t2]}')
REQ_A_FILE="$(mktemp)"
trap 'rm -f "${STDOUT_A}" "${STDERR_A}" "${REQ_A_FILE}"' EXIT
echo "${CONVERSE_REQ}" >"${REQ_A_FILE}"

# IMPORTANT: no `echo ... | bash run.sh` pipe here, and no `( ... ) &`
# subshell wrapper. Either of those makes `$!` capture a PID that is NOT
# the real adapter process (a pipe forks an extra `echo` process; a
# subshell wraps its pipeline in a process that is a PARENT of, not the
# same PID as, the real work) — `kill -9` on the wrong PID leaves the real
# process running to completion in the background, silently invalidating
# the whole "mid-turn kill" premise. Backgrounding `bash run.sh` directly
# with stdin redirected FROM A FILE means `$!` is that exact process, and
# `run.sh`'s own last line is `exec ".../python" adapter.py` — `exec`
# REPLACES the process image in place (same PID), so `$!` ends up being
# the literal python process making the real agnes HTTP call.
bash "${SCRIPT_DIR}/run.sh" <"${REQ_A_FILE}" >"${STDOUT_A}" 2>"${STDERR_A}" &
PID_A=$!
echo "    process A pid=${PID_A} launched"

echo "    waiting for turn 1/2 checkpoint confirmation on stderr..."
TURN1_OK=0
for _ in $(seq 1 300); do
  if grep -q "PROGRESS: crash_recovery converse turn 1/2 checkpointed" "${STDERR_A}" 2>/dev/null; then
    TURN1_OK=1
    break
  fi
  if ! kill -0 "${PID_A}" 2>/dev/null; then
    break
  fi
  sleep 0.1
done

if [[ "${TURN1_OK}" -ne 1 ]]; then
  echo "FAIL (setup): never observed turn 1's checkpoint confirmation before process A exited/timed out"
  wait "${PID_A}" 2>/dev/null
  cat "${STDERR_A}" >&2
  exit 1
fi
echo "    turn 1/2 checkpoint confirmed on disk (process A still running, now inside turn 2's real agnes call)"

# Mid-turn-2: turn 2 is a real agnes call (~6-7s observed elsewhere in this
# adapter's README). Give it a brief head start into the network call, then
# SIGKILL — strictly after turn 1's checkpoint, strictly before turn 2's.
sleep 1.5
echo "=== sending SIGKILL to process A (pid=${PID_A}) mid turn-2 ==="
kill -9 "${PID_A}" 2>/dev/null
wait "${PID_A}" 2>/dev/null
sleep 0.3

PROC_A_ALIVE=1
kill -0 "${PID_A}" 2>/dev/null || PROC_A_ALIVE=0
STDOUT_A_BYTES="$(wc -c <"${STDOUT_A}" | tr -d '[:space:]')"

if [[ "${PROC_A_ALIVE}" -eq 0 && "${STDOUT_A_BYTES}" -eq 0 ]]; then
  echo "PASS (1): process A confirmed dead (kill -0 fails) and emitted ZERO stdout (never reached its final envelope print) — a genuine mid-turn kill, not a clean exit"
else
  echo "FAIL (1): process A alive=${PROC_A_ALIVE} stdout_bytes=${STDOUT_A_BYTES} (expected dead + 0 bytes)"
  FAIL=1
fi

echo
echo "=== process B (op=resume): fresh process, same thread_id, same on-disk checkpoint file ==="
RESUME_REQ=$(jq -n --arg tid "${THREAD_ID}" \
  '{mode:"crash_recovery", op:"resume", thread_id:$tid, messages:[{role:"user", content:"What was the secret launch code I told you earlier? Reply with just the code and nothing else."}]}')

RESUME_OUT="$(echo "${RESUME_REQ}" | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
echo "    ${RESUME_OUT}"

B_ERROR="$(echo "${RESUME_OUT}" | jq -r '.error')"
B_REPLY="$(echo "${RESUME_OUT}" | jq -r '.reply')"

RESUMED=false
if [[ -z "${B_ERROR}" && -n "${B_REPLY}" ]]; then
  RESUMED=true
  echo "PASS (2): process B resumed cleanly (error empty, non-empty reply)"
else
  echo "FAIL (2): process B did not resume cleanly (error=${B_ERROR@Q} reply=${B_REPLY@Q})"
  FAIL=1
fi

PRECRASH_RECALLED=false
if [[ "${RESUMED}" == "true" ]] && echo "${B_REPLY}" | grep -qF "${SECRET}"; then
  PRECRASH_RECALLED=true
  echo "PASS (3): process B's reply recalls the pre-crash (turn 1) fact (${SECRET})"
else
  echo "FAIL (3): process B's reply does NOT contain the pre-crash fact (${SECRET}) — reply=${B_REPLY@Q}"
  FAIL=1
fi

STATE_LOST=true
if [[ "${PRECRASH_RECALLED}" == "true" ]]; then
  STATE_LOST=false
fi
if [[ "${STATE_LOST}" == "false" ]]; then
  echo "PASS (4): state_lost=false — turn 1's committed state survived the kill -9 intact"
else
  echo "FAIL (4): state_lost=true — turn 1's checkpoint did not survive the crash"
  FAIL=1
fi

FINAL_JSON=$(jq -n \
  --argjson resumed "${RESUMED}" \
  --argjson precrash_fact_recalled "${PRECRASH_RECALLED}" \
  --argjson state_lost "${STATE_LOST}" \
  --arg reply "${B_REPLY}" \
  --arg thread_id "${THREAD_ID}" \
  --arg secret "${SECRET}" \
  --arg error "${B_ERROR}" \
  --argjson proc_a_killed "$([[ "${PROC_A_ALIVE}" -eq 0 ]] && echo true || echo false)" \
  --argjson proc_a_stdout_bytes "${STDOUT_A_BYTES}" \
  '{framework:"langgraph", mode:"crash_recovery", resumed:$resumed, precrash_fact_recalled:$precrash_fact_recalled, state_lost:$state_lost, reply:$reply, thread_id:$thread_id, precrash_fact:$secret, error:$error, process_a_confirmed_killed:$proc_a_killed, process_a_stdout_bytes:$proc_a_stdout_bytes}')

echo
echo "=== FINAL JSON ==="
echo "${FINAL_JSON}"

echo
if [[ "${FAIL}" -eq 0 ]]; then
  echo "RESULT: ALL PASS"
  exit 0
else
  echo "RESULT: SOME FAIL"
  exit 1
fi
