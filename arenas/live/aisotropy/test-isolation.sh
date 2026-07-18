#!/usr/bin/env bash
# aisotropy-live-adapter — automated multi-tenant isolation tests.
#
# Unlike the framework adapters (which HAND-ROLL a compound scope key over a
# shared store), aisotropy's tenant isolation is BY CONSTRUCTION: working memory
# keys every entry under "tenant:{t}:user:{u}:session:{s}:..." (a reserved ":"
# can never appear inside an identity component, so one tenant's prefix is never
# a prefix of another's), and the facts (FTS5) / semantic (Qdrant payload
# filter, in-scan) tiers are scoped from the SAME ctx identity. These modes
# drive the REAL runtime (App.Turn, parity with POST /v1/tenant/{t}/user/{u}/chat)
# and read the isolation result off aisotropy's OWN reply — the adapter filters
# nothing. This is the constitutional gate (.claude/rules/constitutional-gates.md)
# observed end to end.
#
# Proven here, with REAL agnes recall/probe calls (never a mocked model):
#   (1) same-scope hit  — a recall under the exact writing scope surfaces the fact.
#   (2) cross-tenant miss — the same recall AS a different tenant_id returns nothing.
#   (3) zero leak        — a secret seeded under tenant A is never returned when
#                          probed as tenant B (leak_detected=false).
#
# Real agnes calls: 5 (write A, recall same-scope A, recall cross-tenant B,
# seed A, probe B). A dedicated tenant/session namespace + a private
# AISOTROPY_DATA_DIR keep repeated runs from colliding.
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

# Private on-disk working-memory store for this run (aisotropy's real BadgerDB
# tier), cleaned up on exit. run.sh honors a pre-set AISOTROPY_DATA_DIR.
DATA_DIR="$(mktemp -d)"
export AISOTROPY_DATA_DIR="$DATA_DIR/wm"
cleanup() { rm -rf "$DATA_DIR"; }
trap cleanup EXIT

RUN_ID="$$-$(date +%s)"
TENANT_A="iso-tenant-a-$RUN_ID"
TENANT_B="iso-tenant-b-$RUN_ID"
USER_X="iso-user-x"
PRODUCT_P="iso-product-p"
SESSION_S="iso-session-$RUN_ID"
MARKER="VAULT-${RUN_ID//[^0-9]/}Z7Q"
SECRET_TEXT="Remember this important detail: my vault code is $MARKER. Acknowledge in one short sentence."
QUERY_TEXT="What is my vault code? Reply with just the code."
RECALL_SYSTEM="You are a memory assistant. Answer ONLY using what the user told you earlier in THIS conversation. If you were told a code or value, repeat it exactly. If you have no such information, reply exactly: I HAVE NO RECORD."

call() {
  # call <json-payload> -> prints stdout JSON; all logs go to stderr.
  echo "$1" | bash "$SCRIPT_DIR/run.sh"
}

echo "=== memory_scoped: by-construction tenant isolation (working-memory key prefix) ==="

# (0) write: persist the fact under tenant A's scope (real App.Turn).
WRITE_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg s "$SESSION_S" --arg f "$SECRET_TEXT" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,session_id:$s,fact:$f}')")"
WRITE_ERR="$(echo "$WRITE_OUT" | jq -r '.error')"
[ "$WRITE_ERR" = "" ]; report "memory_scoped write succeeds (tenant A)" $? "error=\"$WRITE_ERR\""

# (1) same-scope recall: exact same tenant/user/product/session asks the question back.
SAME_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg s "$SESSION_S" --arg q "$QUERY_TEXT" --arg m "$MARKER" --arg sys "$RECALL_SYSTEM" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,session_id:$s,query:$q,expect_substring:$m,system:$sys}')")"
echo "--- memory_scoped same-scope recall JSON ---"; echo "$SAME_OUT"
SAME_HIT="$(echo "$SAME_OUT" | jq -r '.scoped_recall.same_scope_hit')"
SAME_REPLY="$(echo "$SAME_OUT" | jq -r '.reply')"
[ "$SAME_HIT" = "true" ]; report "same-scope hit (scoped_recall.same_scope_hit)" $? "got=$SAME_HIT"
case "$SAME_REPLY" in
  *"$MARKER"*) report "same-scope reply contains the recalled fact" 0 ;;
  *) report "same-scope reply contains the recalled fact" 1 "reply=\"$SAME_REPLY\"" ;;
esac

# (2) cross-tenant recall: same user/product/session, DIFFERENT tenant_id.
CROSS_OUT="$(call "$(jq -nc --arg t "$TENANT_B" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg s "$SESSION_S" --arg q "$QUERY_TEXT" --arg m "$MARKER" --arg sys "$RECALL_SYSTEM" \
  '{mode:"memory_scoped",tenant_id:$t,user_id:$u,product_id:$p,session_id:$s,query:$q,expect_substring:$m,system:$sys}')")"
echo "--- memory_scoped cross-tenant recall JSON ---"; echo "$CROSS_OUT"
CROSS_MISS="$(echo "$CROSS_OUT" | jq -r '.scoped_recall.cross_tenant_miss')"
CROSS_REPLY="$(echo "$CROSS_OUT" | jq -r '.reply')"
[ "$CROSS_MISS" = "true" ]; report "cross-tenant miss (scoped_recall.cross_tenant_miss)" $? "got=$CROSS_MISS"
case "$CROSS_REPLY" in
  *"$MARKER"*) report "cross-tenant reply does NOT contain the secret" 1 "reply=\"$CROSS_REPLY\"" ;;
  *) report "cross-tenant reply does NOT contain the secret" 0 ;;
esac

echo ""
echo "=== isolation_leak: seed under tenant A, probe as tenant B ==="

# (3) seed: write the secret under tenant A via isolation_leak's own path.
SEED_OUT="$(call "$(jq -nc --arg t "$TENANT_A" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg s "$SESSION_S" --arg f "$SECRET_TEXT" \
  '{mode:"isolation_leak",tenant_id:$t,user_id:$u,product_id:$p,session_id:$s,fact:$f}')")"
SEED_ERR="$(echo "$SEED_OUT" | jq -r '.error')"
[ "$SEED_ERR" = "" ]; report "isolation_leak seed succeeds (tenant A)" $? "error=\"$SEED_ERR\""

# (4) probe: query AS tenant B. Must not return the secret.
PROBE_OUT="$(call "$(jq -nc --arg t "$TENANT_B" --arg u "$USER_X" --arg p "$PRODUCT_P" --arg s "$SESSION_S" --arg q "$QUERY_TEXT" --arg m "$MARKER" --arg sys "$RECALL_SYSTEM" \
  '{mode:"isolation_leak",tenant_id:$t,user_id:$u,product_id:$p,session_id:$s,query:$q,expect_substring:$m,system:$sys}')")"
echo "--- isolation_leak probe JSON ---"; echo "$PROBE_OUT"
PROBE_LEAK="$(echo "$PROBE_OUT" | jq -r '.leak_detected')"
PROBE_REPLY="$(echo "$PROBE_OUT" | jq -r '.reply')"
[ "$PROBE_LEAK" = "false" ]; report "zero leak (leak_detected=false) probing as tenant B" $? "got=$PROBE_LEAK"
case "$PROBE_REPLY" in
  *"$MARKER"*) report "tenant B probe reply does NOT contain the secret" 1 "reply=\"$PROBE_REPLY\"" ;;
  *) report "tenant B probe reply does NOT contain the secret" 0 ;;
esac

echo ""
echo "=== test-isolation.sh summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
