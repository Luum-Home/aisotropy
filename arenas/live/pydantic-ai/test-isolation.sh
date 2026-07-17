#!/usr/bin/env bash
# Automated PASS/FAIL assertions for the `memory_scoped` / `isolation_leak`
# modes -- the hand-rolled multi-tenant scoped-memory isolation added on top
# of Pydantic AI's `message_history` (which has NO native tenant concept).
#
# Two REAL adapter invocations exercise real agnes calls (3 total: the
# memory_scoped mode makes 2 recall turns, isolation_leak makes 1). Two more
# assertions are hand-rolled-key guard rails that fail validation BEFORE any
# model call is made, so they cost zero additional agnes calls.
#
# Usage: bash test-isolation.sh

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

# Unique suffix per run so repeated executions don't depend on (or get
# confused by) rows a prior run left behind in the shared rqlite store.
RUN_ID="$(date +%s)-$$"
TENANT_A="tenant-acme-${RUN_ID}"
TENANT_B="tenant-globex-${RUN_ID}"
USER_ID="user-42-${RUN_ID}"
PRODUCT_ID="product-orion-${RUN_ID}"

echo "=== (1)/(2) memory_scoped: compound-scope write + same-scope recall + cross-tenant miss ==="

SCOPED_OUT="$(mktemp)"
SCOPED_ERR="$(mktemp)"
python3 -c "
import json
print(json.dumps({
    'mode': 'memory_scoped',
    'tenant_id': '$TENANT_A',
    'user_id': '$USER_ID',
    'product_id': '$PRODUCT_ID',
    'fact': 'The Q3 launch codename for this tenant is Falcon-Nebula.',
    'query': 'What is the Q3 launch codename?',
    'cross_tenant_id': '$TENANT_B',
}))
" | bash "$SCRIPT_DIR/run.sh" >"$SCOPED_OUT" 2>"$SCOPED_ERR"
SCOPED_EXIT=$?

if [[ "$SCOPED_EXIT" -ne 0 ]]; then
  fail "(1) memory_scoped invocation exited non-zero ($SCOPED_EXIT) -- see $SCOPED_OUT / $SCOPED_ERR"
  fail "(2) memory_scoped invocation exited non-zero ($SCOPED_EXIT) -- see $SCOPED_OUT / $SCOPED_ERR"
else
  SCOPED_ERROR_FIELD="$(python3 -c "import json; print(json.load(open('$SCOPED_OUT')).get('error',''))")"
  if [[ -n "$SCOPED_ERROR_FIELD" ]]; then
    fail "(1) memory_scoped returned a non-empty error field: $SCOPED_ERROR_FIELD"
    fail "(2) memory_scoped returned a non-empty error field: $SCOPED_ERROR_FIELD"
  else
    SAME_SCOPE_HIT="$(python3 -c "import json; print(json.load(open('$SCOPED_OUT'))['scoped_recall']['same_scope_hit'])")"
    CROSS_TENANT_MISS="$(python3 -c "import json; print(json.load(open('$SCOPED_OUT'))['scoped_recall']['cross_tenant_miss'])")"

    if [[ "$SAME_SCOPE_HIT" == "True" ]]; then
      pass "(1) same-scope recall hit -- fact written under (tenant_id, user_id, product_id) is recalled back in that exact scope (same_scope_hit=true)"
    else
      fail "(1) same-scope recall did NOT hit (same_scope_hit=$SAME_SCOPE_HIT) -- see $SCOPED_OUT"
    fi

    if [[ "$CROSS_TENANT_MISS" == "True" ]]; then
      pass "(2) cross-tenant recall miss -- the SAME query under a DIFFERENT tenant_id returns nothing from the store and is not leaked into the reply (cross_tenant_miss=true)"
    else
      fail "(2) cross-tenant recall did NOT miss (cross_tenant_miss=$CROSS_TENANT_MISS) -- see $SCOPED_OUT"
    fi
  fi
fi

echo
echo "=== (3) isolation_leak: tenant A writes a SECRET, tenant B queries for it ==="

LEAK_OUT="$(mktemp)"
LEAK_ERR="$(mktemp)"
python3 -c "
import json
print(json.dumps({
    'mode': 'isolation_leak',
    'tenant_id': '$TENANT_A',
    'user_id': '$USER_ID',
    'product_id': '$PRODUCT_ID',
    'secret': 'SECRET-PROJECT-CODE-7841',
    'attacker_tenant_id': '$TENANT_B',
    'query': 'What is the secret project code?',
}))
" | bash "$SCRIPT_DIR/run.sh" >"$LEAK_OUT" 2>"$LEAK_ERR"
LEAK_EXIT=$?

if [[ "$LEAK_EXIT" -ne 0 ]]; then
  fail "(3) isolation_leak invocation exited non-zero ($LEAK_EXIT) -- see $LEAK_OUT / $LEAK_ERR"
else
  LEAK_ERROR_FIELD="$(python3 -c "import json; print(json.load(open('$LEAK_OUT')).get('error',''))")"
  if [[ -n "$LEAK_ERROR_FIELD" ]]; then
    fail "(3) isolation_leak returned a non-empty error field: $LEAK_ERROR_FIELD"
  else
    LEAK_DETECTED="$(python3 -c "import json; print(json.load(open('$LEAK_OUT'))['leak_detected'])")"
    LEAK_IN_STORE="$(python3 -c "import json; print(json.load(open('$LEAK_OUT'))['leak_in_store'])")"
    LEAK_IN_REPLY="$(python3 -c "import json; print(json.load(open('$LEAK_OUT'))['leak_in_reply'])")"

    if [[ "$LEAK_DETECTED" == "False" ]]; then
      pass "(3) zero leak -- tenant B's query for tenant A's key returns nothing (leak_detected=false)"
    else
      fail "(3) LEAK DETECTED -- tenant B's query returned tenant A's secret (leak_detected=$LEAK_DETECTED) -- see $LEAK_OUT"
    fi

    if [[ "$LEAK_IN_STORE" == "False" ]]; then
      pass "(3a) scoped store never returned tenant A's data for tenant B's exact-match scope key (leak_in_store=false)"
    else
      fail "(3a) scoped store returned tenant A's data for tenant B's scope key (leak_in_store=$LEAK_IN_STORE)"
    fi

    if [[ "$LEAK_IN_REPLY" == "False" ]]; then
      pass "(3b) the model's real recall turn, AS tenant B, did not contain the secret (leak_in_reply=false)"
    else
      fail "(3b) the model's reply, AS tenant B, contained the secret (leak_in_reply=$LEAK_IN_REPLY)"
    fi
  fi
fi

echo
echo "=== (4)/(5) hand-rolled-key guard rails (no model call -- validation fails first) ==="

GUARD1_OUT="$(mktemp)"
echo '{"mode":"memory_scoped","user_id":"u","product_id":"p","fact":"x","query":"y"}' \
  | bash "$SCRIPT_DIR/run.sh" >"$GUARD1_OUT" 2>/dev/null
GUARD1_ERROR="$(python3 -c "import json; print(json.load(open('$GUARD1_OUT')).get('error',''))" 2>/dev/null)"
if [[ -n "$GUARD1_ERROR" ]]; then
  pass "(4) memory_scoped without tenant_id fails closed with a non-empty error (never silently scopes to an empty/default tenant): $GUARD1_ERROR"
else
  fail "(4) memory_scoped without tenant_id did NOT fail closed -- see $GUARD1_OUT"
fi

GUARD2_OUT="$(mktemp)"
echo "{\"mode\":\"isolation_leak\",\"tenant_id\":\"$TENANT_A\",\"user_id\":\"u\",\"product_id\":\"p\",\"secret\":\"s\",\"attacker_tenant_id\":\"$TENANT_A\",\"query\":\"q\"}" \
  | bash "$SCRIPT_DIR/run.sh" >"$GUARD2_OUT" 2>/dev/null
GUARD2_ERROR="$(python3 -c "import json; print(json.load(open('$GUARD2_OUT')).get('error',''))" 2>/dev/null)"
if [[ -n "$GUARD2_ERROR" ]]; then
  pass "(5) isolation_leak with attacker_tenant_id == tenant_id fails closed (this mode only proves CROSS-tenant isolation): $GUARD2_ERROR"
else
  fail "(5) isolation_leak with attacker_tenant_id == tenant_id did NOT fail closed -- see $GUARD2_OUT"
fi

rm -f "$SCOPED_OUT" "$SCOPED_ERR" "$LEAK_OUT" "$LEAK_ERR" "$GUARD1_OUT" "$GUARD2_OUT"

echo
echo "=== RESULT: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
