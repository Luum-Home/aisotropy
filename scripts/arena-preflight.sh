#!/usr/bin/env bash
# Arena preflight — can a real board run right now, and if not, WHY NOT?
#
# WHY THIS EXISTS, and why the answer is not "just curl the health endpoints".
#
# The runner already has a service gate (arenas/live/runner/main.go) that
# classifies every probe into four states instead of a boolean, because
# ABSENCE HAS MANY CAUSES AND PRESENCE HAS ONE. That gate is good and it works.
# It is also invisible until you launch a run, so an operator (or an agent)
# checking "is the infra up?" beforehand reaches for curl, and reaches for the
# WRONG curl:
#
#   curl -s -o /dev/null -w '%{http_code}' http://localhost:6333/healthz   -> 200
#
# That 200 is true and useless. Qdrant's /healthz needs no auth while every
# other endpoint does, so the probe cannot tell "up and usable" from "up and
# refusing everything with 401 Must provide an API key" — the sentence lives in
# the BODY that -o /dev/null throws away. This exact mistake has now been made
# three times in this repository (2026-07-28 twice, 2026-08-14 once), and the
# third time was by an agent that had the lesson available in a Go comment and
# never read it.
#
# So this script probes what a RUN actually needs — an authenticated operation,
# not a liveness ping — and prints the discriminating detail every time.
#
# Exit codes:  0 ready · 1 something is genuinely not ready · 2 cannot evaluate

set -uo pipefail

QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"
RQLITE_HOST="${RQLITE_HOST:-http://localhost:4001}"
EMBED_URL="${AISOTROPY_EMBED_URL:-http://127.0.0.1:8899}"
TIMEOUT="${ARENA_PREFLIGHT_TIMEOUT:-6}"

for req in curl python3; do
  command -v "$req" >/dev/null 2>&1 || { echo "arena-preflight: missing $req" >&2; exit 2; }
done

fail=0
note() { printf '  %-9s %-11s %s\n' "$1" "$2" "$3"; }

# classify NAME URL [extra curl args...] — prints status + body, never a bare bool.
classify() {
  name=$1; url=$2; shift 2
  body_file=$(mktemp) || { echo "arena-preflight: mktemp failed" >&2; exit 2; }
  code=$(curl -s -o "$body_file" -w '%{http_code}' --max-time "$TIMEOUT" "$@" "$url" 2>/dev/null)
  rc=$?
  body=$(head -c 160 "$body_file" | tr '\n' ' ')
  rm -f "$body_file"
  if [ "$rc" -eq 7 ]; then
    note "$name" "REFUSED" "nothing is listening on $url — this IS proof of absence"
    fail=1; return
  fi
  if [ "$rc" -eq 28 ]; then
    # A timeout is a fact about THIS machine's load, not about the service.
    note "$name" "INCONCLUSIVE" "timed out after ${TIMEOUT}s — NOT proof of absence; check load"
    fail=1; return
  fi
  if [ "$rc" -ne 0 ]; then
    note "$name" "INCONCLUSIVE" "transport error (curl rc=$rc)"
    fail=1; return
  fi
  case "$code" in
    2*) note "$name" "OK" "HTTP $code" ;;
    401|403)
      note "$name" "AUTH" "HTTP $code — $body"
      note "" "" "the service is UP and refusing you; set the credential, do not restart it"
      fail=1 ;;
    *) note "$name" "BAD" "HTTP $code — $body"; fail=1 ;;
  esac
}

echo "=== arena preflight ==="

# 1. Qdrant — an AUTHENTICATED operation, not /healthz. /healthz answers without
#    a key, which is precisely why it cannot answer the question being asked.
if [ -n "${QDRANT_API_KEY:-}" ]; then
  classify qdrant "$QDRANT_URL/collections" -H "api-key: ${QDRANT_API_KEY}"
else
  classify qdrant "$QDRANT_URL/collections"
  note "" "" "QDRANT_API_KEY is unset — the reference compose REQUIRES one"
fi

# 2. rqlite — a real query, for the same reason: /status is served to the
#    anonymous user so the healthcheck works, while /db/* needs credentials.
if [ -n "${RQLITE_URL:-}" ]; then
  # Reuse the caller's credentialed URL verbatim; never reconstruct or log it.
  q=$(curl -s --max-time "$TIMEOUT" -o /dev/null -w '%{http_code}' \
        "${RQLITE_URL%/}/db/query?level=weak" -H 'content-type: application/json' \
        -d '[["SELECT 1"]]' 2>/dev/null)
  rc=$?
  if [ "$rc" -ne 0 ]; then note rqlite "INCONCLUSIVE" "curl rc=$rc against RQLITE_URL"; fail=1
  elif [ "$q" = "200" ]; then note rqlite "OK" "authenticated SELECT 1 succeeded"
  else note rqlite "BAD" "HTTP $q on an authenticated query"; fail=1; fi
else
  classify rqlite "$RQLITE_HOST/status"
  note "" "" "RQLITE_URL unset — only the anonymous status surface was probed, NOT /db"
  fail=1
fi

# 3. Embedder — must return a real vector. A 200 on /health proves the process
#    booted, not that the model loaded.
ebody=$(curl -s --max-time 25 "$EMBED_URL/embeddings" -H 'content-type: application/json' \
          -d '{"model":"multilingual-e5-small-query","input":"preflight"}' 2>/dev/null)
if [ -z "$ebody" ]; then
  note embed "REFUSED/INCONCLUSIVE" "no response from $EMBED_URL — start arenas/live/_shared/start-embed-server.sh"
  fail=1
else
  dim=$(printf '%s' "$ebody" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin); print(len(d["data"][0]["embedding"]))
except Exception: print(0)' 2>/dev/null)
  if [ "${dim:-0}" -gt 0 ]; then note embed "OK" "returned a ${dim}-dim vector"
  else note embed "BAD" "answered but produced no vector"; fail=1; fi
fi

# 4. Model quota — one minimal call. Cheaper than discovering it mid-board.
if [ -n "${OPENAI_API_KEY:-}" ] && [ -n "${OPENAI_BASE_URL:-}" ]; then
  mcode=$(curl -s -o /dev/null -w '%{http_code}' --max-time 45 \
    -X POST "${OPENAI_BASE_URL}/chat/completions" \
    -H "Authorization: Bearer $OPENAI_API_KEY" -H 'content-type: application/json' \
    -d "{\"model\":\"${OPENAI_MODEL:-}\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"Reply OK\"}]}" 2>/dev/null)
  case "$mcode" in
    200) note model "OK" "provider answered 200 (quota available)" ;;
    429) note model "QUOTA" "HTTP 429 — exhausted or rate-limited; do NOT launch a sweep"; fail=1 ;;
    *)   note model "BAD" "HTTP $mcode"; fail=1 ;;
  esac
else
  note model "SKIPPED" "OPENAI_API_KEY / OPENAI_BASE_URL not in the environment"
fi

# 5. Machine — DEGRADED is not BLOCKED, and the distinction was itself missed
#    once by reading only the exit code (swap is a LAGGING indicator; the script
#    labels it "informative" for that reason).
if [ -x scripts/resource-guard.sh ]; then
  rg_out=$(bash scripts/resource-guard.sh 2>&1); rg=$?
  case "$rg" in
    0) note machine "OK" "$rg_out" ;;
    1) note machine "DEGRADED" "$rg_out"
       note "" "" "tight, NOT blocked — run sequentially (-concurrency 1)" ;;
    3) note machine "BLOCKED" "$rg_out"; fail=1 ;;
    *) note machine "UNKNOWN" "resource-guard exit $rg"; fail=1 ;;
  esac
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "VERDICT: ready — a real board can run."
  exit 0
fi
echo "VERDICT: NOT ready. Read the line above that is not OK; it names the cause."
echo "Reminder: AUTH and INCONCLUSIVE are not 'down'. Fix the credential or the load,"
echo "do not restart a service that is answering."
exit 1
