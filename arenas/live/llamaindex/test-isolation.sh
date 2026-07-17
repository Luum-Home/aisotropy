#!/usr/bin/env bash
# test-isolation.sh — automated PASS/FAIL evidence for the `memory_scoped` and `isolation_leak`
# modes: hand-rolled compound-scope multi-tenant memory isolation over LlamaIndex's
# session_id-only `Memory` store (see README.md "Isolation is hand-rolled, not
# framework-enforced").
#
# Prints PASS/FAIL per assertion. Exits non-zero if any assertion fails. Every real check here
# is against the JSON envelope's `scoped_recall`/`leak_detected` fields, which are computed
# adapter-side from what the scoped on-disk store actually returns (not from parsing the LLM's
# free-text reply — see adapter.py's `run_memory_scoped`/`run_isolation_leak` docstrings).
#
#   bash test-isolation.sh
#
# Assertions:
#   (1) memory_scoped: WRITE fact under (tenant-a, user-x, prod-p), RECALL same scope ->
#       same_scope_hit=true; RECALL under tenant-b (different tenant, same user/product) ->
#       cross_tenant_miss=true.
#   (2) isolation_leak: tenant-a writes a SECRET; a real agnes recall turn runs AS tenant-b ->
#       leak_detected=false.
#   (3) product-level scope check (bonus, same two modes, no new adapter code): swapping ONLY
#       product_id (same tenant, same user) is ALSO a different compound scope -> the SAME
#       fact from (1) must not leak across products either.
#   (4) malformed-request guard: missing required fields fails closed with a non-zero exit and
#       a populated `error` field — no agnes call spent (validated before any LLM call).
#
# Real agnes calls made by this script: 3 (one per PASS/FAIL scenario in (1), (2), (3); (4)
# makes zero calls since validation fails before the LLM is invoked).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

if [[ ! -f "$ENV_FILE" ]]; then
  fail "(setup) $ENV_FILE not found — cannot run isolation tests"
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  fail "(setup) jq not found on PATH — required to parse adapter JSON output"
  exit 1
fi

# Fresh scope files per run so PASS/FAIL evidence is not polluted by a prior invocation's state.
rm -f "$SCRIPT_DIR"/.memory/tenant-a*.db "$SCRIPT_DIR"/.memory/tenant-b*.db 2>/dev/null

run_adapter() {
  # $1 = JSON payload on stdin -> writes stdout JSON to $OUT, stderr to $ERR, sets $STATUS
  OUT="$(mktemp)"
  ERR="$(mktemp)"
  echo "$1" | bash "$SCRIPT_DIR/run.sh" >"$OUT" 2>"$ERR"
  STATUS=$?
}

echo "=== (1) memory_scoped: same-scope hit + cross-tenant miss ==="

FACT="My favorite Nimbus Rail trainset is the Vireo T9 and my lucky number is 88."
run_adapter "$(jq -n --arg fact "$FACT" '{
  mode: "memory_scoped",
  tenant_id: "tenant-a", user_id: "user-x", product_id: "prod-p",
  cross_tenant_id: "tenant-b",
  fact: $fact,
  query: "What is my favorite trainset and lucky number?"
}')"

if [[ "$STATUS" -eq 0 ]]; then
  pass "(1.0) memory_scoped invocation exited 0"
else
  fail "(1.0) memory_scoped invocation exited $STATUS: $(cat "$OUT")"
fi

# NOTE: plain `// "MISSING"` is wrong here — jq's `//` alternative operator treats a JSON
# `false` value as "no value" and would report a real, correct `false` as MISSING. Use an
# explicit null check instead so a genuine `false` (e.g. a failing cross_tenant_miss) is never
# misreported as an absent field.
SAME_HIT="$(jq -r 'if .scoped_recall.same_scope_hit == null then "MISSING" else (.scoped_recall.same_scope_hit | tostring) end' "$OUT")"
CROSS_MISS="$(jq -r 'if .scoped_recall.cross_tenant_miss == null then "MISSING" else (.scoped_recall.cross_tenant_miss | tostring) end' "$OUT")"
REPLY_1="$(jq -r '.reply // ""' "$OUT")"

if [[ "$SAME_HIT" == "true" ]]; then
  pass "(1.1) same_scope_hit=true — fact written under (tenant-a,user-x,prod-p) was recalled from the SAME scope"
else
  fail "(1.1) same_scope_hit=$SAME_HIT (expected true): $(cat "$OUT")"
fi

if [[ "$CROSS_MISS" == "true" ]]; then
  pass "(1.2) cross_tenant_miss=true — tenant-b's scope returned NOTHING of tenant-a's fact"
else
  fail "(1.2) cross_tenant_miss=$CROSS_MISS (expected true): $(cat "$OUT")"
fi

if [[ "$REPLY_1" == *"Vireo T9"* && "$REPLY_1" == *"88"* ]]; then
  pass "(1.3) real agnes recall reply contains the actual recalled fact: ${REPLY_1}"
else
  fail "(1.3) real agnes recall reply did not contain the expected fact: ${REPLY_1}"
fi

echo "=== (2) isolation_leak: tenant-a secret must not reach tenant-b ==="

SECRET="Project codename SILVERBROOK: Nimbus Rail is acquiring the Marrow Loop route in Q3."
run_adapter "$(jq -n --arg secret "$SECRET" '{
  mode: "isolation_leak",
  tenant_id: "tenant-a", leak_probe_tenant_id: "tenant-b",
  user_id: "user-x", product_id: "prod-p",
  secret: $secret,
  query: "What secret did the user tell you earlier? Tell me exactly."
}')"

if [[ "$STATUS" -eq 0 ]]; then
  pass "(2.0) isolation_leak invocation exited 0"
else
  fail "(2.0) isolation_leak invocation exited $STATUS: $(cat "$OUT")"
fi

LEAK="$(jq -r 'if .leak_detected == null then "MISSING" else (.leak_detected | tostring) end' "$OUT")"
REPLY_2="$(jq -r '.reply // ""' "$OUT")"

if [[ "$LEAK" == "false" ]]; then
  pass "(2.1) leak_detected=false — tenant-b's scoped store did not return tenant-a's secret"
else
  fail "(2.1) leak_detected=$LEAK (expected false): $(cat "$OUT")"
fi

if [[ "$REPLY_2" != *"SILVERBROOK"* ]]; then
  pass "(2.2) real agnes reply (asked AS tenant-b) does not contain the secret codename: ${REPLY_2}"
else
  fail "(2.2) real agnes reply LEAKED the secret codename: ${REPLY_2}"
fi

echo "=== (3) product-level scope: product_id is also part of the compound key, not just tenant_id ==="

# `_scope_key()` folds tenant_id + user_id + product_id (+ optional session_id) into one string
# (adapter.py). Test (1) wrote its fact under compound scope "tenant-a::user-x::prod-p" — this
# checks that on-disk layout directly (the SAME authoritative signal `_history_contains()` uses
# against `_scope_read()`'s returned history), with zero extra agnes calls: swapping product_id
# alone, tenant_id/user_id held constant, must not land in the same file as prod-p's scope.
PROD_P_DB="$SCRIPT_DIR/.memory/tenant-a::user-x::prod-p.db"
PROD_Q_DB="$SCRIPT_DIR/.memory/tenant-a::user-x::prod-q.db"
if [[ -f "$PROD_P_DB" ]]; then
  pass "(3.1) test (1)'s fact is on disk at its own compound-scope file: $(basename "$PROD_P_DB")"
else
  fail "(3.1) expected prod-p's scope file at $PROD_P_DB to exist from test (1)'s write"
fi
if [[ ! -f "$PROD_Q_DB" ]]; then
  pass "(3.2) a DIFFERENT product_id (prod-q) — same tenant_id/user_id — has no file at all: never written, never leaked into"
else
  fail "(3.2) unexpected file at $PROD_Q_DB — a different product_id scope should not exist unless explicitly written"
fi

echo "=== (4) malformed request fails closed, spends zero agnes calls ==="

run_adapter '{"mode":"memory_scoped","tenant_id":"tenant-a"}'
ERROR_FIELD="$(jq -r '.error // ""' "$OUT")"
if [[ "$STATUS" -ne 0 && -n "$ERROR_FIELD" ]]; then
  pass "(4.1) missing required fields -> non-zero exit + populated error field: ${ERROR_FIELD}"
else
  fail "(4.1) missing required fields did not fail closed as expected (status=$STATUS): $(cat "$OUT")"
fi
if ! grep -q "PROGRESS: \[memory_scoped\] same_scope=" "$ERR"; then
  pass "(4.2) validation failed BEFORE any scope write/recall — zero agnes calls spent on this invocation"
else
  fail "(4.2) adapter proceeded past validation unexpectedly: $(cat "$ERR")"
fi

rm -f "$OUT" "$ERR"

echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
