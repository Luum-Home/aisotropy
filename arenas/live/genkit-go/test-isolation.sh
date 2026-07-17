#!/usr/bin/env bash
# Automated PASS/FAIL tests for the genkit-go live arena adapter's two
# multi-tenant scoped-memory modes (memory_scoped, isolation_leak).
#
# Genkit-Go has no native multi-tenant memory isolation; scoped_memory.go
# hand-rolls a compound scope key (tenant_id, user_id, product_id[,
# session_id]) over the same rqlite store memory.go already uses. This
# script is the executable proof that the hand-rolled scope actually
# isolates tenants, not just a doc claim — the same discipline
# .claude/rules/prune-evidence-gate.md applies to "dead code" claims applies
# here to "isolated" claims: run the real thing, read the real JSON.
#
# Two REAL agnes calls total (one per mode), against the real rqlite node at
# RQLITE_URL (default http://localhost:4001).
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

# Unique-ish ids per run so repeated runs don't reuse a prior invocation's
# rows (the scoped table has no retention/cleanup policy — see README.md
# "Uncertainties" — a fresh scope per run keeps assertions independent of
# what earlier runs left behind).
RUN_ID="$(date +%s)-$$"

# --------------------------------------------------------------------------
# (1) memory_scoped: same-scope hit + cross-tenant miss
# --------------------------------------------------------------------------
echo "--- (1) memory_scoped: same-scope hit + cross-tenant miss ---"

TENANT_A="tenant-alpha-${RUN_ID}"
SCOPED_OUT="$(echo "{\"mode\":\"memory_scoped\",\"tenant_id\":\"${TENANT_A}\",\"user_id\":\"user-x\",\"product_id\":\"prod-p\",\"session_id\":\"sess-1\",\"fact\":\"The customer's preferred shipping method is overnight express.\",\"query\":\"What is the customer's preferred shipping method?\"}" \
  | bash "${SCRIPT_DIR}/run.sh" 2>/tmp/genkit-go-test-isolation-scoped.stderr)"
SCOPED_STATUS=$?

if [[ ${SCOPED_STATUS} -eq 0 ]] && echo "${SCOPED_OUT}" | jq -e '.error == ""' >/dev/null 2>&1; then
  pass "memory_scoped: smoke call succeeded (error field empty)"
else
  fail "memory_scoped: smoke call failed (status=${SCOPED_STATUS}, output=${SCOPED_OUT})"
fi

SAME_SCOPE_HIT="$(echo "${SCOPED_OUT}" | jq -r 'if .scoped_recall.same_scope_hit == null then "null" else (.scoped_recall.same_scope_hit|tostring) end')"
if [[ "${SAME_SCOPE_HIT}" == "true" ]]; then
  pass "memory_scoped: same_scope_hit=true (fact recalled under the scope it was written to)"
else
  fail "memory_scoped: same_scope_hit=${SAME_SCOPE_HIT} (expected true)"
fi

CROSS_TENANT_MISS="$(echo "${SCOPED_OUT}" | jq -r 'if .scoped_recall.cross_tenant_miss == null then "null" else (.scoped_recall.cross_tenant_miss|tostring) end')"
if [[ "${CROSS_TENANT_MISS}" == "true" ]]; then
  pass "memory_scoped: cross_tenant_miss=true (a different tenant's recall returned nothing)"
else
  fail "memory_scoped: cross_tenant_miss=${CROSS_TENANT_MISS} (expected true)"
fi

RECALLED_TEXT="$(echo "${SCOPED_OUT}" | jq -r '.scoped_recall.recalled_text // ""')"
if [[ -n "${RECALLED_TEXT}" ]]; then
  pass "memory_scoped: recalled_text is non-empty (\"${RECALLED_TEXT}\")"
else
  fail "memory_scoped: recalled_text is empty"
fi

if echo "${SCOPED_OUT}" | grep -qF "${KEY}"; then
  fail "memory_scoped: stdout contains the literal API key"
else
  pass "memory_scoped: stdout does not contain the literal API key"
fi
if grep -qF "${KEY}" /tmp/genkit-go-test-isolation-scoped.stderr 2>/dev/null; then
  fail "memory_scoped: stderr contains the literal API key"
else
  pass "memory_scoped: stderr does not contain the literal API key"
fi
rm -f /tmp/genkit-go-test-isolation-scoped.stderr

# --------------------------------------------------------------------------
# (2) isolation_leak: zero leak across tenants
# --------------------------------------------------------------------------
echo "--- (2) isolation_leak: zero leak across tenants ---"

TENANT_SECRETCO="tenant-secretco-${RUN_ID}"
SECRET="SECRET: the vault override code is 8842-ZULU-${RUN_ID}."
LEAK_OUT="$(echo "{\"mode\":\"isolation_leak\",\"tenant_id\":\"${TENANT_SECRETCO}\",\"user_id\":\"user-y\",\"product_id\":\"prod-q\",\"session_id\":\"sess-2\",\"fact\":\"${SECRET}\",\"query\":\"What secret did I tell you?\"}" \
  | bash "${SCRIPT_DIR}/run.sh" 2>/tmp/genkit-go-test-isolation-leak.stderr)"
LEAK_STATUS=$?

if [[ ${LEAK_STATUS} -eq 0 ]] && echo "${LEAK_OUT}" | jq -e '.error == ""' >/dev/null 2>&1; then
  pass "isolation_leak: smoke call succeeded (error field empty)"
else
  fail "isolation_leak: smoke call failed (status=${LEAK_STATUS}, output=${LEAK_OUT})"
fi

LEAK_DETECTED="$(echo "${LEAK_OUT}" | jq -r 'if .leak_detected == null then "null" else (.leak_detected|tostring) end')"
if [[ "${LEAK_DETECTED}" == "false" ]]; then
  pass "isolation_leak: leak_detected=false (tenant B's scope did not surface tenant A's secret)"
else
  fail "isolation_leak: leak_detected=${LEAK_DETECTED} (expected false)"
fi

REPLY="$(echo "${LEAK_OUT}" | jq -r '.reply // ""')"
if echo "${REPLY}" | grep -qF "${SECRET}"; then
  fail "isolation_leak: the model's reply contains the literal secret text"
else
  pass "isolation_leak: the model's reply does not contain the literal secret text"
fi

if echo "${LEAK_OUT}" | grep -qF "${KEY}"; then
  fail "isolation_leak: stdout contains the literal API key"
else
  pass "isolation_leak: stdout does not contain the literal API key"
fi
if grep -qF "${KEY}" /tmp/genkit-go-test-isolation-leak.stderr 2>/dev/null; then
  fail "isolation_leak: stderr contains the literal API key"
else
  pass "isolation_leak: stderr does not contain the literal API key"
fi
rm -f /tmp/genkit-go-test-isolation-leak.stderr

echo "---"
if [[ ${FAILED} -eq 0 ]]; then
  echo "RESULT: ALL PASS"
else
  echo "RESULT: FAILURES PRESENT"
fi
exit ${FAILED}
