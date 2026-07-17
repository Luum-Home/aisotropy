#!/usr/bin/env bash
# Automated scoped-memory isolation tests for the LangGraph live-arena
# adapter's two new modes: memory_scoped and isolation_leak.
#
# Assertions (each reported PASS/FAIL independently):
#   (1) same-scope recall hit    -> memory_scoped: a fact written under
#       (tenant_id, user_id, product_id) IS recalled under the SAME scope.
#   (2) cross-tenant recall miss -> memory_scoped: the SAME fact is NOT
#       recalled under a DIFFERENT tenant_id (same user_id/product_id).
#   (3) zero leak (real agnes call) -> isolation_leak: a secret written by
#       tenant A does not reach a real agnes turn answered as tenant B,
#       neither at the scoped-store layer nor in the model's reply.
#
# Also exercises the memory_scoped "op":"write"/"recall" reproducibility
# path across THREE separate run.sh processes sharing the same on-disk
# store file (informational, not a separate PASS/FAIL line — folded into
# assertion (1)/(2) evidence).
#
# Never prints the OPENAI_API_KEY. Uses jq to parse JSON; never greps for
# the key literal in this script (that is test-env.sh's job).
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

STORE_DB="${SCRIPT_DIR}/.data/scoped_store.sqlite"
rm -f "${STORE_DB}"

echo "=== (1)+(2) memory_scoped: full 3-step scenario in one invocation ==="
SCENARIO_OUT="$(echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","fact_key":"renewal_date","fact":"The plan renews on the 3rd of every month."}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
echo "    ${SCENARIO_OUT}"

SAME_SCOPE_HIT="$(echo "${SCENARIO_OUT}" | jq -r '.scoped_recall.same_scope_hit')"
CROSS_TENANT_MISS="$(echo "${SCENARIO_OUT}" | jq -r '.scoped_recall.cross_tenant_miss')"
ERR="$(echo "${SCENARIO_OUT}" | jq -r '.error')"

if [[ -n "${ERR}" ]]; then
  echo "FAIL (1): memory_scoped returned an error: ${ERR}"
  echo "FAIL (2): memory_scoped returned an error: ${ERR}"
  FAIL=1
else
  if [[ "${SAME_SCOPE_HIT}" == "true" ]]; then
    echo "PASS (1): same-scope recall hit (same_scope_hit=true)"
  else
    echo "FAIL (1): same-scope recall did NOT hit (same_scope_hit=${SAME_SCOPE_HIT})"
    FAIL=1
  fi

  if [[ "${CROSS_TENANT_MISS}" == "true" ]]; then
    echo "PASS (2): cross-tenant recall missed (cross_tenant_miss=true)"
  else
    echo "FAIL (2): cross-tenant recall did NOT miss (cross_tenant_miss=${CROSS_TENANT_MISS})"
    FAIL=1
  fi
fi

echo
echo "=== (1)+(2) reproducibility: same scenario across THREE separate run.sh processes ==="
rm -f "${STORE_DB}"
WRITE_OUT="$(echo '{"mode":"memory_scoped","op":"write","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","fact_key":"note","fact":"Renewal is March 3rd."}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
SAME_OUT="$(echo '{"mode":"memory_scoped","op":"recall","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","fact_key":"note"}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
CROSS_OUT="$(echo '{"mode":"memory_scoped","op":"recall","tenant_id":"tenant-b","user_id":"user-x","product_id":"product-p","fact_key":"note"}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
echo "    write:  ${WRITE_OUT}"
echo "    recall (same tenant, proc 2):  ${SAME_OUT}"
echo "    recall (cross tenant, proc 3): ${CROSS_OUT}"

CROSS_PROC_SAME_HIT="$(echo "${SAME_OUT}" | jq -r '.scoped_recall.same_scope_hit')"
CROSS_PROC_CROSS_HIT="$(echo "${CROSS_OUT}" | jq -r '.scoped_recall.same_scope_hit')"
if [[ "${CROSS_PROC_SAME_HIT}" == "true" && "${CROSS_PROC_CROSS_HIT}" == "false" ]]; then
  echo "PASS (1)/(2) cross-process: same-tenant hit + cross-tenant miss reproduced across 3 processes"
else
  echo "FAIL (1)/(2) cross-process: expected same-tenant hit=true (got ${CROSS_PROC_SAME_HIT}), cross-tenant hit=false (got ${CROSS_PROC_CROSS_HIT})"
  FAIL=1
fi

echo
echo "=== (3) isolation_leak: real agnes turn, zero leak ==="
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "FAIL (3): OPENAI_API_KEY is not set; cannot run the real agnes turn"
  FAIL=1
else
  LEAK_OUT="$(echo '{"mode":"isolation_leak","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","secret_key":"wifi_password","secret_value":"Xk9-Nebula-Vault-42"}' \
    | bash "${SCRIPT_DIR}/run.sh" 2>/dev/null)"
  echo "    ${LEAK_OUT}"

  LEAK_DETECTED="$(echo "${LEAK_OUT}" | jq -r '.leak_detected')"
  LEAK_ERR="$(echo "${LEAK_OUT}" | jq -r '.error')"

  if [[ -n "${LEAK_ERR}" ]]; then
    echo "FAIL (3): isolation_leak returned an error: ${LEAK_ERR}"
    FAIL=1
  elif [[ "${LEAK_DETECTED}" == "false" ]]; then
    echo "PASS (3): zero leak (leak_detected=false) — secret never reached tenant B's store recall or reply"
  else
    echo "FAIL (3): LEAK DETECTED (leak_detected=${LEAK_DETECTED}) — cross-tenant secret exposure"
    FAIL=1
  fi
fi

echo
if [[ "${FAIL}" -eq 0 ]]; then
  echo "RESULT: ALL PASS"
  exit 0
else
  echo "RESULT: SOME FAIL"
  exit 1
fi
