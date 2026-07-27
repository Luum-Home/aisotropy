#!/usr/bin/env bash
# arena-integration-gate.sh — every adapter, real stores, ZERO model calls.
#
# WHY THIS EXISTS
#
# Four defect classes reached a published board, or nearly did, and every one of
# them was invisible to the existing gates because the free lane (ARENA_STUB=1)
# fakes the stores as well as the model:
#
#   1. LANE LEAK — an adapter reporting the REAL model id on a lane that promised
#      no model call. aisotropy read OPENAI_MODEL before checking the lane;
#      spring-ai did the same AND still carried a hardcoded `agnes-2.0-flash`
#      default, which would have labelled numbers with a model never called.
#   2. STORE SCHEMA — spring-ai's RAG returned zero documents on every cell of
#      every board ("— (19 err)") because the seeder wrote integer point ids and
#      the body under `text`; Spring AI needs UUID ids and `doc_content`. The
#      arena reported its own encoding as a framework's capability.
#   3. AUTH — the seeder itself sent no api-key after Qdrant was hardened, and
#      its error path crashed on the non-JSON 401 body instead of naming it.
#   4. STDOUT POLLUTION — io.qdrant logs a WARN to stdout, in front of the
#      adapter's JSON, breaking the one-object contract. `2>/dev/null` cannot fix
#      what was never on stderr.
#
# Each was found by hand, after a board had already spent. This gate turns all
# four into an executable check that costs nothing: the integration lane
# (ARENA_STUB_MODEL=1) runs every adapter against the REAL embed server, Qdrant
# and rqlite with a mocked model, so infrastructure is provable without a
# provider, a key, or a quota.
#
# Exit codes: 0 clean · 1 findings · 2 usage/environment error.
#
# Read-only with respect to the repo; it starts no services and writes nothing
# outside a scratch dir it removes.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LIVE="${REPO_ROOT}/arenas/live"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}"' EXIT

QDRANT_REST="${QDRANT_REST_URL:-http://localhost:6333}"
QDRANT_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}"
EMBED_URL="${AISOTROPY_EMBED_URL:-http://127.0.0.1:8899}"

# The model id an adapter must NEVER report on this lane. Read from the ambient
# env exactly as run.sh would see it, because that is the value that leaks.
REAL_MODEL="${OPENAI_MODEL:-}"
if [[ -z "${REAL_MODEL}" && -f "${REPO_ROOT}/.env" ]]; then
  REAL_MODEL="$(grep -E '^OPENAI_MODEL=' "${REPO_ROOT}/.env" 2>/dev/null | tail -1 | cut -d= -f2- | tr -d '"'"'"' ')"
fi

FINDINGS=0
finding() { printf '   \033[31mFINDING\033[0m  %s\n' "$*"; FINDINGS=$((FINDINGS + 1)); }
ok()      { printf '   ok       %s\n' "$*"; }

echo "==================================================================="
echo " arena-integration-gate: real stores, mocked model, zero model calls"
echo "==================================================================="
echo

# ---- 0) preconditions ------------------------------------------------------
# A gate that cannot reach the stores must FAIL, never pass quietly: "nothing
# found" is not the same as "nothing to find" (the lesson prune-guard learned
# when it green-lit a package inside a nested module it could not analyse).
echo "0) stores reachable"
for probe in "${EMBED_URL}/health|embed" "${QDRANT_REST}/collections|qdrant"; do
  url="${probe%%|*}"; name="${probe##*|}"
  code="$(curl -s -o /dev/null -m 5 -w '%{http_code}' -H "api-key: ${QDRANT_KEY}" "${url}" 2>/dev/null)"
  if [[ "${code}" != "200" ]]; then
    echo "   ERROR: ${name} not reachable at ${url} (HTTP ${code:-none})."
    echo "   This gate proves the REAL infrastructure works; it cannot run without it."
    echo "   Start the stack (docker compose up -d, arenas/live/_shared/start-embed-server.sh) and retry."
    exit 2
  fi
  ok "${name} reachable"
done
echo

# ---- 1) no hardcoded vendor model defaults ---------------------------------
# Static half of finding #1. A fallback model id is provenance fabricated by a
# default: an unconfigured run labels its numbers with a model it never called.
echo "1) no hardcoded vendor model defaults in adapter sources"
VENDOR_PATTERN='agnes-2\.0-flash|laguna-s-2\.1|gpt-4[o0]?|claude-3|gemini-1\.5'
hits="$(cd "${LIVE}" && grep -rInE "(envOr|getenv|environ\.get|process\.env)[^\n]{0,60}OPENAI_MODEL[^\n]{0,40}(${VENDOR_PATTERN})" \
  --include='*.go' --include='*.mjs' --include='*.js' --include='*.py' --include='*.java' . 2>/dev/null \
  | grep -v node_modules | grep -v '/\.venv/' || true)"
if [[ -n "${hits}" ]]; then
  while IFS= read -r line; do finding "vendor default in a model lookup: ${line}"; done <<< "${hits}"
else
  ok "none found"
fi
echo

# ---- 2) every adapter, on the integration lane -----------------------------
echo "2) per-adapter: stdout contract · lane leak · real retrieval"
[[ -n "${REAL_MODEL}" ]] && echo "   (real model id that must NOT appear: ${REAL_MODEL})"

for dir in "${LIVE}"/*/; do
  fw="$(basename "${dir}")"
  case "${fw}" in _shared|runner|tasks|models) continue ;; esac
  [[ -x "${dir}/run.sh" ]] || continue

  coll="arena_rag_${fw//-/_}"
  collvar="QDRANT_COLLECTION"
  [[ "${fw}" == "llamaindex" ]] && collvar="ARENA_COLLECTION"
  # aisotropy self-ingests into its own collection (documented exemption).
  [[ "${fw}" == "aisotropy" ]] && coll="aisotropy_arena"

  out="${SCRATCH}/${fw}.out"
  printf '{"mode":"rag","system":"You are a helpful assistant.","messages":[{"role":"user","content":"What is the Blue Guarantee?"}]}\n' \
    | env -u OPENAI_API_KEY \
          ARENA_STUB_MODEL=1 \
          "${collvar}=${coll}" \
          QDRANT_API_KEY="${QDRANT_KEY}" \
      "${dir}/run.sh" >"${out}" 2>"${SCRATCH}/${fw}.err"
  rc=$?

  if [[ ${rc} -ne 0 ]]; then
    finding "${fw}: adapter exited ${rc} on the integration lane — $(tail -1 "${SCRATCH}/${fw}.err" | cut -c1-120)"
    continue
  fi

  # Finding #4: stdout must be EXACTLY one JSON object. A log line in front of
  # it is a contract violation even when the JSON itself is fine.
  verdict="$(python3 - "${out}" "${REAL_MODEL}" <<'PY'
import json, sys
path, real = sys.argv[1], (sys.argv[2] if len(sys.argv) > 2 else "")
raw = open(path, encoding="utf-8", errors="replace").read()
try:
    doc = json.loads(raw)
except Exception as exc:
    head = raw.strip().splitlines()[0][:100] if raw.strip() else "(empty)"
    print(f"STDOUT|stdout is not exactly one JSON object ({exc}); first line: {head}")
    raise SystemExit
model = str(doc.get("model") or "")
if real and model.strip() == real.strip():
    print(f"LEAK|reported the REAL model {model!r} on a lane that makes no model call")
    raise SystemExit
retrieved = doc.get("retrieved") or []
err = (doc.get("error") or "").strip()
if err:
    print(f"ERROR|{err[:110]}")
    raise SystemExit
if not retrieved:
    print("EMPTY|retrieved 0 documents from the REAL store (seeding schema, credential, or client mismatch)")
    raise SystemExit
top = (retrieved[0] or {}).get("doc_id", "?")
print(f"OK|model={model[:28]} docs={len(retrieved)} top={top}")
PY
)"
  kind="${verdict%%|*}"; detail="${verdict#*|}"
  case "${kind}" in
    OK)     ok "$(printf '%-13s %s' "${fw}" "${detail}")" ;;
    *)      finding "$(printf '%-13s %s' "${fw}" "${detail}")" ;;
  esac
done
echo

# ---- verdict ---------------------------------------------------------------
echo "==================================================================="
if [[ ${FINDINGS} -eq 0 ]]; then
  echo " VERDICT: clean — every adapter reaches the REAL stores with a mocked"
  echo " model, reports an offline model id, and keeps stdout to one JSON object."
  echo "==================================================================="
  exit 0
fi
echo " VERDICT: ${FINDINGS} finding(s). These are INFRASTRUCTURE defects, not"
echo " framework results — shipping a board with any of them attributes our own"
echo " plumbing to somebody else's product."
echo "==================================================================="
exit 1
