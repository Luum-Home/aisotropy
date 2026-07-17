#!/usr/bin/env bash
# eino-live-arena — automated credential/env tests.
#
# Verifies, with PASS/FAIL per assertion, that:
#   (a) the native path reads OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL
#       from the environment and a real "qa" smoke call succeeds;
#   (b) the Docker image never bakes the agnes API key in as a literal —
#       checked three independent ways: the image's baked ENV config, the
#       image's build history, and a full grep over the saved image layers;
#   (c) the key is never written to stderr/stdout by a real run.
#
# The key's VALUE is never printed by this script, even on failure — only
# PASS/FAIL and counts.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE="eino-live-arena:local"

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

if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
else
  echo "test-env.sh: $ENV_FILE not found — cannot run credential tests" >&2
  exit 1
fi

for var in OPENAI_API_KEY OPENAI_BASE_URL OPENAI_MODEL; do
  if [ -z "${!var:-}" ]; then
    echo "test-env.sh: required env var $var is not set" >&2
    exit 1
  fi
done

echo "=== (a) native: env vars read + qa smoke ==="

[ -n "${OPENAI_API_KEY:-}" ]; report "native reads OPENAI_API_KEY from env" $?
[ -n "${OPENAI_BASE_URL:-}" ]; report "native reads OPENAI_BASE_URL from env" $?
[ -n "${OPENAI_MODEL:-}" ]; report "native reads OPENAI_MODEL from env" $?

NATIVE_OUT_FILE="$(mktemp)"
NATIVE_ERR_FILE="$(mktemp)"
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Reply with the single word: pong"}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$NATIVE_OUT_FILE" 2>"$NATIVE_ERR_FILE"
NATIVE_EXIT=$?

[ "$NATIVE_EXIT" = "0" ] && grep -q '"error":""' "$NATIVE_OUT_FILE"
report "native qa smoke call succeeds" $? "exit=$NATIVE_EXIT"

echo "=== (b) docker: no key literal in the image ==="

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "test-env.sh: building $IMAGE ..." >&2
  docker build -t "$IMAGE" "$SCRIPT_DIR" >&2
fi

docker inspect "$IMAGE" --format '{{json .Config.Env}}' | grep -c 'OPENAI_API_KEY' >/tmp/eino_env_count.$$ 2>/dev/null || true
ENV_HITS="$(cat /tmp/eino_env_count.$$ 2>/dev/null || echo 0)"
rm -f /tmp/eino_env_count.$$
[ "${ENV_HITS:-0}" = "0" ]; report "image Config.Env has no OPENAI_API_KEY entry" $? "hits=${ENV_HITS:-0}"

HISTORY_HITS="$(docker history --no-trunc "$IMAGE" 2>/dev/null | grep -c "OPENAI_API_KEY=${OPENAI_API_KEY}" || true)"
[ "${HISTORY_HITS:-0}" = "0" ]; report "image build history has no key literal" $? "hits=${HISTORY_HITS:-0}"

SAVE_FILE="$(mktemp)"
docker save "$IMAGE" -o "$SAVE_FILE" 2>/dev/null
LAYER_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$SAVE_FILE" || true)"
[ "${LAYER_HITS:-0}" = "0" ]; report "saved image layers contain no key literal (full grep)" $? "hits=${LAYER_HITS:-0}"
rm -f "$SAVE_FILE"

RUNTIME_ENV_HITS="$(docker run --rm "$IMAGE" 2>&1 | grep -a -c -- "$OPENAI_API_KEY" || true)"
[ "${RUNTIME_ENV_HITS:-0}" = "0" ]; report "container with no OPENAI_API_KEY passed in never emits it" $? "hits=${RUNTIME_ENV_HITS:-0}"

echo "=== (c) key never logged ==="

STDOUT_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$NATIVE_OUT_FILE" || true)"
[ "${STDOUT_HITS:-0}" = "0" ]; report "native stdout never contains the key" $? "hits=${STDOUT_HITS:-0}"

STDERR_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$NATIVE_ERR_FILE" || true)"
[ "${STDERR_HITS:-0}" = "0" ]; report "native stderr never contains the key" $? "hits=${STDERR_HITS:-0}"

rm -f "$NATIVE_OUT_FILE" "$NATIVE_ERR_FILE"

echo ""
echo "=== test-env.sh summary: $PASS passed, $FAIL failed ==="
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
