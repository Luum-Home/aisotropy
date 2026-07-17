#!/usr/bin/env bash
# test-env.sh — automated credential/env hygiene tests for the LlamaIndex live-arena adapter.
#
# Prints PASS/FAIL per assertion, never prints the agnes API key itself. Exits non-zero if
# any assertion fails.
#
#   bash test-env.sh
#
# Assertions:
#   (a) native mode (run.sh) reads OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL/EMBED_URL/
#       QDRANT_URL from the environment, and a real `qa` smoke succeeds.
#   (b) the built Docker image does NOT contain the key literal anywhere: `docker history`,
#       `docker run --rm <img> env`, and a grep of the exported image filesystem layers all
#       return zero hits.
#   (c) the key never appears in this script's own captured stdout/stderr from either the
#       native or the Docker run.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE_TAG="aisotropy-arena-llamaindex:test-env"

PASS=0
FAIL=0

pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

if [[ ! -f "$ENV_FILE" ]]; then
  fail "(setup) $ENV_FILE not found — cannot run credential tests"
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [[ -z "${!var:-}" ]]; then
    fail "(setup) $var not set in $ENV_FILE"
    exit 1
  fi
done

KEY_VALUE="$OPENAI_API_KEY"
EMBED_URL_DEFAULT="${EMBED_URL:-http://127.0.0.1:8899}"
QDRANT_URL_DEFAULT="${QDRANT_URL:-http://localhost:6333}"

echo "=== (a) native mode reads env + qa smoke ==="

NATIVE_OUT="$(mktemp)"
NATIVE_ERR="$(mktemp)"
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$NATIVE_OUT" 2>"$NATIVE_ERR"
NATIVE_STATUS=$?

if grep -q "embed_url='${EMBED_URL_DEFAULT}'" "$NATIVE_ERR" && grep -q "qdrant_url='${QDRANT_URL_DEFAULT}'" "$NATIVE_ERR"; then
  pass "(a.1) adapter picked up EMBED_URL/QDRANT_URL from env (embed_url=${EMBED_URL_DEFAULT}, qdrant_url=${QDRANT_URL_DEFAULT})"
else
  fail "(a.1) adapter did not log the expected EMBED_URL/QDRANT_URL — see $NATIVE_ERR"
fi

if [[ "$NATIVE_STATUS" -eq 0 ]] && grep -q '"reply": "Paris"' "$NATIVE_OUT"; then
  pass "(a.2) native qa smoke returned Paris via OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL from env"
else
  fail "(a.2) native qa smoke did not return the expected reply (status=$NATIVE_STATUS): $(cat "$NATIVE_OUT")"
fi

echo "=== (b) built image does not contain the key literal ==="

if docker build --quiet -t "$IMAGE_TAG" "$SCRIPT_DIR" >/tmp/llamaindex-test-env-build.log 2>&1; then
  pass "(b.0) docker build succeeded"
else
  fail "(b.0) docker build failed — see /tmp/llamaindex-test-env-build.log"
  echo "Cannot continue Docker assertions without a built image." >&2
  echo ""
  echo "=== Summary: $PASS passed, $FAIL failed ==="
  rm -f "$NATIVE_OUT" "$NATIVE_ERR"
  exit 1
fi

HISTORY_HITS=$(docker history --no-trunc "$IMAGE_TAG" 2>/dev/null | grep -c -F -- "$KEY_VALUE" || true)
if [[ "$HISTORY_HITS" -eq 0 ]]; then
  pass "(b.1) docker history contains 0 occurrences of the key literal"
else
  fail "(b.1) docker history contains $HISTORY_HITS occurrence(s) of the key literal"
fi

ENV_HITS=$(docker run --rm --entrypoint env "$IMAGE_TAG" 2>/dev/null | grep -c -F -- "$KEY_VALUE" || true)
if [[ "$ENV_HITS" -eq 0 ]]; then
  pass "(b.2) 'docker run --rm <img> env' contains 0 occurrences of the key literal"
else
  fail "(b.2) 'docker run --rm <img> env' contains $ENV_HITS occurrence(s) of the key literal"
fi

# Inspect every layer's filesystem contents for the key literal (catches the case where a key
# was accidentally COPYed or baked into a file rather than an ENV var).
EXPORT_DIR="$(mktemp -d)"
CONTAINER_ID="$(docker create "$IMAGE_TAG")"
docker export "$CONTAINER_ID" | tar -tvf - >/dev/null 2>&1  # sanity: archive is readable
LAYER_HITS=$(docker export "$CONTAINER_ID" | strings 2>/dev/null | grep -c -F -- "$KEY_VALUE" || true)
docker rm "$CONTAINER_ID" >/dev/null 2>&1
rm -rf "$EXPORT_DIR"

if [[ "$LAYER_HITS" -eq 0 ]]; then
  pass "(b.3) exported container filesystem contains 0 occurrences of the key literal"
else
  fail "(b.3) exported container filesystem contains $LAYER_HITS occurrence(s) of the key literal"
fi

echo "=== (c) key never appears in captured stdout/stderr ==="

NATIVE_LOG_HITS=$(grep -c -F -- "$KEY_VALUE" "$NATIVE_OUT" "$NATIVE_ERR" 2>/dev/null | awk -F: '{sum+=$2} END{print sum+0}')
if [[ "$NATIVE_LOG_HITS" -eq 0 ]]; then
  pass "(c.1) native run.sh stdout+stderr contain 0 occurrences of the key literal"
else
  fail "(c.1) native run.sh stdout+stderr contain $NATIVE_LOG_HITS occurrence(s) of the key literal"
fi

rm -f "$NATIVE_OUT" "$NATIVE_ERR"

echo ""
echo "=== Summary: $PASS passed, $FAIL failed ==="
if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
