#!/usr/bin/env bash
# eino-live-arena — automated multi-tenant scoped-memory isolation tests.
#
# Eino has NO native multi-tenant memory isolation primitive; memory_scoped
# and isolation_leak (mode_scoped_memory.go) hand-roll a compound scope key
# (tenant_id, user_id, product_id[, session_id]) over the same kind of real
# on-disk SQLite store `memory` mode uses. This script proves, with REAL
# agnes recall/probe calls (never a mocked model), that the hand-rolled key
# actually isolates tenants:
#
#   (1) same-scope hit      — a recall issued for the exact scope a fact was
#                              written under returns that fact.
#   (2) cross-tenant miss    — the same recall, issued as a DIFFERENT
#                              tenant_id (same user/product), returns nothing.
#   (3) zero leak            — isolation_leak: a secret seeded under tenant A
#                              is never returned when probed as tenant B.
#
# Real agnes calls made by this script: 3 (recall same-scope, recall
# cross-tenant, isolation_leak probe). The two "write"/"seed" calls are pure
# store operations and make no LLM call — see mode_scoped_memory.go.
#
# Uses a dedicated tenant/session namespace (test-isolation-<random>) so
# repeated runs never collide with each other or with manual smoke-test data
# left in .data/memory.db.
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

if ! command -v jq >/dev/null 2>&1; then
  echo "test-isolation.sh: jq is required but not installed" >&2
  exit 1
fi

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "test-isolation.sh: $ENV_FILE not found — cannot run isolation tests" >&2
  exit 1
fi

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [ -z "${!var:-}" ]; then
    echo "test-isolation.sh: required env var $var is not set" >&2
    exit 1
  fi
done

RUN_ID="$$-$(date +%s)"
TENANT_A="test-isolation-tenant-a-$RUN_ID"
TENANT_B="test-isolation-tenant-b-$RUN_ID"
USER_X="test-isolation-user-x"
PRODUCT_P="test-isolation-product-p"
SECRET_TEXT="ISOLATION-TEST-SECRET-$RUN_ID: the vault combination is 7-31-94."
QUERY_TEXT="What is the vault combination?"

call() {
  # call <json-payload>  -> prints stdout JSON, all logs to stderr (bash's own stderr)
  echo "$1" | bash "$SCRIPT_DIR/run.sh"
}

echo "=== memory_scoped: compound-scope isolation ==="

# (0) write: seed a fact under tenant A's scope. Pure store op, no agnes call.
WRITE_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg f "$SECRET_TEXT" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,fact:$f}')")"
WRITE_ERR="$(echo "$WRITE_OUT" | jq -r '.error')"
[ "$WRITE_ERR" = "" ]; report "memory_scoped write succeeds (tenant A)" $? "error=\"$WRITE_ERR\""

# (1) same-scope recall: exact same tenant/user/product asks the question back. Real agnes call.
SAME_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg q "$QUERY_TEXT" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,query:$q}')")"
SAME_HIT="$(echo "$SAME_OUT" | jq -r '.scoped_recall.same_scope_hit')"
SAME_REPLY="$(echo "$SAME_OUT" | jq -r '.reply')"
[ "$SAME_HIT" = "true" ]; report "same-scope hit (scoped_recall.same_scope_hit)" $? "got=$SAME_HIT"
case "$SAME_REPLY" in
  *"7-31-94"*) report "same-scope reply contains the recalled fact" 0 ;;
  *) report "same-scope reply contains the recalled fact" 1 "reply=\"$SAME_REPLY\"" ;;
esac

# (2) cross-tenant recall: same user/product, DIFFERENT tenant_id. Real agnes call.
CROSS_OUT="$(call "$(jq -nc --arg t "$TENANT_B" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg q "$QUERY_TEXT" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,query:$q}')")"
CROSS_MISS="$(echo "$CROSS_OUT" | jq -r '.scoped_recall.cross_tenant_miss')"
CROSS_REPLY="$(echo "$CROSS_OUT" | jq -r '.reply')"
[ "$CROSS_MISS" = "true" ]; report "cross-tenant miss (scoped_recall.cross_tenant_miss)" $? "got=$CROSS_MISS"
case "$CROSS_REPLY" in
  *"7-31-94"*) report "cross-tenant reply does NOT contain the secret" 1 "reply=\"$CROSS_REPLY\"" ;;
  *) report "cross-tenant reply does NOT contain the secret" 0 ;;
esac

echo ""
echo "=== isolation_leak: seed under tenant A, probe as tenant B ==="

# (3) seed: write the secret under tenant A via isolation_leak's own path. Pure store op.
SEED_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg f "$SECRET_TEXT" \
  '{mode:"isolation_leak",tenant_id:$t,user_id:$u,product_id:$p,fact:$f}')")"
SEED_ERR="$(echo "$SEED_OUT" | jq -r '.error')"
[ "$SEED_ERR" = "" ]; report "isolation_leak seed succeeds (tenant A)" $? "error=\"$SEED_ERR\""

# (4) probe: query AS tenant B. Real agnes call. Must not return the secret.
PROBE_OUT="$(call "$(jq -nc --arg t "$TENANT_B" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg q "$QUERY_TEXT" \
  '{mode:"isolation_leak",tenant_id:$t,user_id:$u,product_id:$p,query:$q}')")"
PROBE_LEAK="$(echo "$PROBE_OUT" | jq -r '.leak_detected')"
PROBE_REPLY="$(echo "$PROBE_OUT" | jq -r '.reply')"
[ "$PROBE_LEAK" = "false" ]; report "zero leak (leak_detected=false) probing as tenant B" $? "got=$PROBE_LEAK"
case "$PROBE_REPLY" in
  *"7-31-94"*) report "tenant B probe reply does NOT contain the secret" 1 "reply=\"$PROBE_REPLY\"" ;;
  *) report "tenant B probe reply does NOT contain the secret" 0 ;;
esac

echo ""
echo "=== direct store check: scoped_facts on disk holds only tenant A rows for this run ==="

DB_PATH="${EINO_ARENA_MEMORY_DB:-$SCRIPT_DIR/.data/memory.db}"
if command -v sqlite3 >/dev/null 2>&1 && [ -f "$DB_PATH" ]; then
  LEAK_ROWS="$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM scoped_facts WHERE tenant_id = '$TENANT_B' AND fact LIKE '%$RUN_ID%';" 2>/dev/null || echo "err")"
  [ "$LEAK_ROWS" = "0" ]; report "on-disk store has zero rows for tenant B from this run" $? "rows=$LEAK_ROWS"
else
  echo "SKIP: on-disk store check (sqlite3 CLI or $DB_PATH not available)"
fi

echo ""
echo "=== test-isolation.sh summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
