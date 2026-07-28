#!/usr/bin/env bash
# Does an adapter FAIL LOUDLY when its vector store is unreachable, or does it
# quietly answer from an in-process fallback?
#
# WHY THIS EXISTS
# ---------------
# On 2026-07-28 a board hit `dial tcp 127.0.0.1:6334: connect: connection
# refused` twenty times against arena_rag_genkit_go while five other adapters
# ran their RAG items without a single error. That looked like five adapters
# degrading silently -- a retrieval score computed against nothing, published as
# framework quality.
#
# It was not. Reading the log's TIMELINE showed the outage window contained ONLY
# genkit-go's items; the other five had run earlier, against a healthy Qdrant.
# The "finding" was an artifact of comparing totals instead of windows.
#
# But that only removed the evidence, not the question. "No proof they degrade"
# is not "proof they fail loudly", and the difference decides whether a board
# can be trusted after an infrastructure blip. So this answers it directly
# instead of waiting for the next outage to answer it by accident.
#
# METHOD
# ------
# For each adapter that implements `rag`, run ONE rag turn with the vector store
# pointed at a dead port, and classify what comes back:
#
#   LOUD    non-zero exit, or a JSON `error` field  -> the failure is visible
#   SILENT  a normal reply with no error            -> the failure is invisible
#           and would have been scored as retrieval quality
#
# ARENA_STUB_MODEL=1 selects the integration lane: REAL stores, MOCK model. The
# stores are the subject here, and mocking the model keeps the probe free (no
# provider spend) and independent of which model is configured.
#
# EXIT CODES (executable-evidence convention)
#   0  every adapter fails loudly
#   1  at least one adapter degrades silently
#   2  error (cannot evaluate -- never reported as a pass)
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIVE="$(cd "$HERE/../arenas/live" && pwd)"

# A port nothing listens on. Chosen high and fixed so the probe is reproducible.
DEAD_PORT="${DEAD_PORT:-59998}"

QUESTION='How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund?'
INPUT=$(printf '{"mode":"rag","session_id":"degradation-probe","tenant_id":"probe","user_id":"probe","messages":[{"role":"user","content":"%s"}]}' "$QUESTION")

adapters=()
for d in "$LIVE"/*/run.sh; do
  [ -f "$d" ] || continue
  fw="$(basename "$(dirname "$d")")"
  # Only adapters whose matrix declares a real-lane rag cell that is not
  # infeasible -- probing an adapter that does not do RAG proves nothing.
  m="$LIVE/$fw/matrix.json"
  [ -f "$m" ] || continue
  if python3 -c "
import json,sys
d=json.load(open('$m'))
c=(d.get('dimensions') or {}).get('rag')
sys.exit(0 if c and not str((c.get('real') or {}).get('native','')).startswith('infeasible') else 1)
" 2>/dev/null; then
    adapters+=("$fw")
  fi
done

[ "${#adapters[@]}" -gt 0 ] || { echo "ERROR: no adapters with a live rag cell — cannot evaluate" >&2; exit 2; }

echo "probing ${#adapters[@]} adapter(s) with the vector store pointed at a dead port ($DEAD_PORT)"
echo

silent=0
for fw in "${adapters[@]}"; do
  out=$(cd "$LIVE/$fw" && printf '%s' "$INPUT" | \
    env ARENA_STUB_MODEL=1 \
        QDRANT_HOST=127.0.0.1 \
        QDRANT_PORT="$DEAD_PORT" \
        QDRANT_URL="http://127.0.0.1:$DEAD_PORT" \
        QDRANT_REST_URL="http://127.0.0.1:$DEAD_PORT" \
        ARENA_QDRANT_URL="http://127.0.0.1:$DEAD_PORT" \
        bash ./run.sh 2>/dev/null)
  rc=$?

  reply=$(printf '%s' "$out" | python3 -c "
import json,sys
raw=sys.stdin.read().strip()
# An adapter writes exactly one JSON object to stdout; take the last line that parses.
for line in reversed(raw.splitlines()):
    line=line.strip()
    if not line: continue
    try:
        d=json.loads(line)
    except Exception: continue
    if d.get('error'): print('ERRFIELD'); break
    r=(d.get('reply') or '').strip()
    print('REPLY' if r else 'EMPTY'); break
else:
    print('NOJSON')
" 2>/dev/null)

  if [ "$rc" -ne 0 ] || [ "$reply" = "ERRFIELD" ] || [ "$reply" = "NOJSON" ]; then
    printf '  LOUD    %-12s exit=%s %s — the failure is visible\n' "$fw" "$rc" "$reply"
  else
    printf '  SILENT  %-12s exit=%s %s — answered anyway; this would have scored as retrieval quality\n' "$fw" "$rc" "$reply"
    silent=$((silent + 1))
  fi
done

echo
if [ "$silent" -gt 0 ]; then
  echo "VERDICT: $silent adapter(s) degrade SILENTLY. With the vector store unreachable they"
  echo "still produce a reply, so a board run during an outage publishes a retrieval score"
  echo "measured against a fallback. The board cannot distinguish that from framework"
  echo "quality, and neither can a reader."
  exit 1
fi
echo "VERDICT: every probed adapter fails loudly when its vector store is unreachable."
exit 0
