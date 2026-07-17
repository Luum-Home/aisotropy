#!/usr/bin/env bash
# Automated credential/env tests for the Pydantic AI live-arena adapter.
#
# Three PASS/FAIL assertions, per the delivery brief:
#   (a) native adapter reads OPENAI_* from the environment, and a real `qa`
#       smoke call succeeds through run.sh (1 real agnes call).
#   (b) the built Docker image has NO key literal baked in: neither as a
#       default env var (`docker run --rm <img> env`) nor anywhere in the
#       image's build history (`docker history --no-trunc`).
#   (c) the key value never appears in any adapter source file, in run.sh /
#       run-docker.sh, or in the stdout/stderr captured from the real run in
#       (a) -- i.e. it is truly never logged.
#
# The actual key value is loaded into a local variable to grep FOR, but is
# never echoed, printed, or written to any file by this script.
#
# Usage: bash test-env.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
IMAGE_TAG="${IMAGE_TAG:-pydantic-ai-live-arena:latest}"

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

echo "=== (a) native: env vars present + qa smoke works ==="

if [[ -n "${OPENAI_API_KEY:-}" && -n "${OPENAI_BASE_URL:-}" && -n "${OPENAI_MODEL:-}" ]]; then
  pass "(a1) OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL are all present in the environment"
else
  fail "(a1) one or more of OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL is missing"
fi

QA_STDOUT_FILE="$(mktemp)"
QA_STDERR_FILE="$(mktemp)"
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Reply with exactly the word: OK"}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$QA_STDOUT_FILE" 2>"$QA_STDERR_FILE"
QA_EXIT=$?

if [[ "$QA_EXIT" -eq 0 ]] && python3 -c "
import json, sys
with open('$QA_STDOUT_FILE') as f:
    obj = json.load(f)
sys.exit(0 if obj.get('error') == '' and obj.get('reply') else 1)
"; then
  pass "(a2) native qa smoke call succeeded (non-empty reply, error=\"\")"
else
  fail "(a2) native qa smoke call did not succeed (exit=$QA_EXIT) -- see $QA_STDOUT_FILE / $QA_STDERR_FILE"
fi

echo
echo "=== (b) Docker image has NO key literal ==="

if ! docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "PROGRESS: building $IMAGE_TAG for the env test..." >&2
  docker build -t "$IMAGE_TAG" "$SCRIPT_DIR" >&2
fi

RUNTIME_ENV_KEY_COUNT="$(docker run --rm "$IMAGE_TAG" env 2>/dev/null | grep -c '^OPENAI_API_KEY=' || true)"
if [[ "$RUNTIME_ENV_KEY_COUNT" -eq 0 ]]; then
  pass "(b1) 'docker run --rm $IMAGE_TAG env' (no -e flags) shows 0 OPENAI_API_KEY entries"
else
  fail "(b1) image bakes a default OPENAI_API_KEY env var ($RUNTIME_ENV_KEY_COUNT match(es))"
fi

# Grep the full, untruncated build history for the literal key value -- proves
# it was never passed as a build-time ARG/ENV that got written into a layer.
HISTORY_KEY_COUNT="$(docker history --no-trunc "$IMAGE_TAG" 2>/dev/null | grep -F -c -- "$OPENAI_API_KEY" || true)"
if [[ "$HISTORY_KEY_COUNT" -eq 0 ]]; then
  pass "(b2) 'docker history --no-trunc $IMAGE_TAG' contains 0 occurrences of the key value"
else
  fail "(b2) the key value appears in the image's build history ($HISTORY_KEY_COUNT match(es))"
fi

echo
echo "=== (c) key never logged ==="

SOURCE_FILES=("$SCRIPT_DIR"/*.py "$SCRIPT_DIR"/run.sh "$SCRIPT_DIR"/run-docker.sh "$SCRIPT_DIR"/Dockerfile)
SOURCE_KEY_COUNT=0
for f in "${SOURCE_FILES[@]}"; do
  [[ -f "$f" ]] || continue
  c="$(grep -F -c -- "$OPENAI_API_KEY" "$f" 2>/dev/null || true)"
  SOURCE_KEY_COUNT=$((SOURCE_KEY_COUNT + c))
done
if [[ "$SOURCE_KEY_COUNT" -eq 0 ]]; then
  pass "(c1) 0 occurrences of the key value in adapter source / run scripts / Dockerfile"
else
  fail "(c1) the key value literally appears in adapter source ($SOURCE_KEY_COUNT match(es))"
fi

RUN_OUTPUT_KEY_COUNT=0
for f in "$QA_STDOUT_FILE" "$QA_STDERR_FILE"; do
  c="$(grep -F -c -- "$OPENAI_API_KEY" "$f" 2>/dev/null || true)"
  RUN_OUTPUT_KEY_COUNT=$((RUN_OUTPUT_KEY_COUNT + c))
done
if [[ "$RUN_OUTPUT_KEY_COUNT" -eq 0 ]]; then
  pass "(c2) 0 occurrences of the key value in the real qa smoke call's stdout+stderr"
else
  fail "(c2) the key value appeared in the smoke call's captured output ($RUN_OUTPUT_KEY_COUNT match(es))"
fi

rm -f "$QA_STDOUT_FILE" "$QA_STDERR_FILE"

echo
echo "=== RESULT: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
