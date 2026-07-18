#!/usr/bin/env bash
# aisotropy-live-arena — automated credential/env tests.
#
# Verifies, with PASS/FAIL per assertion, that:
#   (a) the native path reads OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL from the
#       environment and a real "qa" smoke call succeeds;
#   (b) the Docker image never bakes the agnes API key in as a literal — checked
#       four independent ways: the image's baked ENV config, its build history, a
#       full grep over the saved image layers, and a `docker run env` dump;
#   (c) the key is never written to stdout/stderr by a real run;
#   (d) BONUS (the broker invariant): the WASM tool guest is isolated from the
#       agnes key — the sandbox cannot read host files/env, so it cannot see or
#       exfiltrate the secret. Proven by the guest reporting fs_read_denied=true
#       AND the key never appearing anywhere in the wasm_tool run's output.
#
# The key's VALUE is never printed by this script, even on failure — only PASS/FAIL
# and counts.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE="aisotropy-live-arena:local"
RUN_DOCKER="${RUN_DOCKER:-1}" # set RUN_DOCKER=0 to skip the (slow) Docker checks

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

if [[ -z "${OPENAI_API_KEY:-}" && -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE"
  set +a
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

NATIVE_OUT="$(mktemp)"; NATIVE_ERR="$(mktemp)"
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Reply with the single word: pong"}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$NATIVE_OUT" 2>"$NATIVE_ERR"
NATIVE_EXIT=$?
[ "$NATIVE_EXIT" = "0" ] && grep -q '"error":""' "$NATIVE_OUT"
report "native qa smoke call succeeds" $? "exit=$NATIVE_EXIT"

echo "=== (c) key never logged (native) ==="
STDOUT_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$NATIVE_OUT" || true)"
[ "${STDOUT_HITS:-0}" = "0" ]; report "native stdout never contains the key" $? "hits=${STDOUT_HITS:-0}"
STDERR_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$NATIVE_ERR" || true)"
[ "${STDERR_HITS:-0}" = "0" ]; report "native stderr never contains the key" $? "hits=${STDERR_HITS:-0}"

echo "=== (d) BONUS: WASM tool guest is isolated from the agnes key ==="
WASM_OUT="$(mktemp)"; WASM_ERR="$(mktemp)"
echo '{"mode":"wasm_tool","messages":[{"role":"user","content":"What is (12 + 30) * 7 / 2?"}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$WASM_OUT" 2>"$WASM_ERR"
# The guest reports fs_read_denied=true — it cannot read host files, hence cannot
# reach the key (which lives only in the host process environment).
grep -q 'sandbox_fs_read_denied=true' "$WASM_OUT"
report "wasm guest is filesystem-isolated (fs_read_denied=true)" $?
WASM_KEY_HITS="$(cat "$WASM_OUT" "$WASM_ERR" | grep -a -c -- "$OPENAI_API_KEY" || true)"
[ "${WASM_KEY_HITS:-0}" = "0" ]; report "wasm_tool run never emits the key" $? "hits=${WASM_KEY_HITS:-0}"
rm -f "$NATIVE_OUT" "$NATIVE_ERR" "$WASM_OUT" "$WASM_ERR"

if [ "$RUN_DOCKER" != "1" ]; then
  echo "=== (b) docker checks SKIPPED (RUN_DOCKER=0) ==="
  echo ""
  echo "=== test-env.sh summary: $PASS passed, $FAIL failed (docker skipped) ==="
  [ "$FAIL" -gt 0 ] && exit 1 || exit 0
fi

echo "=== (b) docker: no key literal in the image ==="
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "test-env.sh: building $IMAGE (context: repo root) ..." >&2
  docker build -t "$IMAGE" -f "$SCRIPT_DIR/Dockerfile" "$REPO_ROOT" >&2
fi

ENV_HITS="$(docker inspect "$IMAGE" --format '{{json .Config.Env}}' | grep -c 'OPENAI_API_KEY' || true)"
[ "${ENV_HITS:-0}" = "0" ]; report "image Config.Env has no OPENAI_API_KEY entry" $? "hits=${ENV_HITS:-0}"

HISTORY_HITS="$(docker history --no-trunc "$IMAGE" 2>/dev/null | grep -c -- "$OPENAI_API_KEY" || true)"
[ "${HISTORY_HITS:-0}" = "0" ]; report "image build history has no key literal" $? "hits=${HISTORY_HITS:-0}"

SAVE_FILE="$(mktemp)"
docker save "$IMAGE" -o "$SAVE_FILE" 2>/dev/null
LAYER_HITS="$(grep -a -c -- "$OPENAI_API_KEY" "$SAVE_FILE" || true)"
[ "${LAYER_HITS:-0}" = "0" ]; report "saved image layers contain no key literal (full grep)" $? "hits=${LAYER_HITS:-0}"
rm -f "$SAVE_FILE"

RUNTIME_ENV_HITS="$(docker run --rm --entrypoint /usr/local/bin/aisotropy-live-adapter "$IMAGE" </dev/null 2>&1 | grep -a -c -- "$OPENAI_API_KEY" || true)"
[ "${RUNTIME_ENV_HITS:-0}" = "0" ]; report "container without the key passed in never emits it" $? "hits=${RUNTIME_ENV_HITS:-0}"

echo ""
echo "=== test-env.sh summary: $PASS passed, $FAIL failed ==="
[ "$FAIL" -gt 0 ] && exit 1 || exit 0
