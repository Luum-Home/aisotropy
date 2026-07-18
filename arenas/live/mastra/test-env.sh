#!/usr/bin/env bash
# Automated credential/env tests for the Mastra live arena adapter.
#
# Runs three independent assertions and reports PASS/FAIL for each. NEVER
# prints the OPENAI_API_KEY value itself -- only counts/booleans derived from
# grepping for it. Exits 0 iff every assertion passed, non-zero otherwise.
#
# Usage: bash arenas/live/mastra/test-env.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
IMAGE_TAG="aisotropy-arena-mastra:test-env"

PASS=0
FAIL=0

report() {
  local name="$1"
  local ok="$2"
  local detail="$3"
  if [ "$ok" = "0" ]; then
    echo "PASS  $name -- $detail"
    PASS=$((PASS + 1))
  else
    echo "FAIL  $name -- $detail"
    FAIL=$((FAIL + 1))
  fi
}

set -a
# shellcheck disable=SC1091
source "$REPO_ROOT/.env"
set +a

if [ -z "${OPENAI_API_KEY:-}" ] || [ -z "${OPENAI_MODEL:-}" ]; then
  echo "FATAL: OPENAI_API_KEY / OPENAI_MODEL not set after sourcing $REPO_ROOT/.env" >&2
  exit 2
fi

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# --- Assertion (a): native adapter reads env vars + a real qa smoke works ----
NATIVE_OUT_FILE="$WORKDIR/native.stdout"
NATIVE_ERR_FILE="$WORKDIR/native.stderr"
echo '{"mode":"qa","system":"You are a concise, accurate assistant. Answer in one short sentence.","messages":[{"role":"user","content":"What is the chemical symbol for gold?"}]}' \
  | bash "$SCRIPT_DIR/run.sh" >"$NATIVE_OUT_FILE" 2>"$NATIVE_ERR_FILE"

NATIVE_OK=1
if [ -s "$NATIVE_OUT_FILE" ]; then
  NATIVE_ERROR_FIELD="$(node -e "try{const o=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));process.stdout.write(o.error||'')}catch(e){process.stdout.write('PARSE_FAILED')}" "$NATIVE_OUT_FILE")"
  NATIVE_REPLY_LEN="$(node -e "try{const o=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));process.stdout.write(String((o.reply||'').length))}catch(e){process.stdout.write('0')}" "$NATIVE_OUT_FILE")"
  if [ -z "$NATIVE_ERROR_FIELD" ] && [ "$NATIVE_REPLY_LEN" -gt 0 ]; then
    NATIVE_OK=0
  fi
fi
report "native-env-and-qa-smoke" "$NATIVE_OK" "stdout parsed, error field empty, reply non-empty (see $NATIVE_OUT_FILE)"

# --- Assertion (b): the Docker image bakes in NO key literal ----------------
docker build -t "$IMAGE_TAG" "$SCRIPT_DIR" >"$WORKDIR/docker-build.log" 2>&1
BUILD_STATUS=$?
report "docker-image-builds" "$BUILD_STATUS" "docker build exit code (log: $WORKDIR/docker-build.log)"

# (b.1) `docker run --rm img env` must show ZERO credential env vars -- the
# Dockerfile declares no ENV/ARG for them, so an env dump with no -e flags
# must come back empty for every one of these names.
ENV_DUMP="$WORKDIR/image-env-dump.txt"
docker run --rm "$IMAGE_TAG" env >"$ENV_DUMP" 2>&1
BAKED_VAR_COUNT="$(grep -cE '^(OPENAI_API_KEY|OPENAI_BASE_URL|OPENAI_MODEL)=' "$ENV_DUMP" || true)"
BAKED_VAR_COUNT="${BAKED_VAR_COUNT:-0}"
if [ "$BAKED_VAR_COUNT" -eq 0 ]; then ENV_OK=0; else ENV_OK=1; fi
report "docker-image-env-has-no-credential-vars" "$ENV_OK" "\`docker run --rm img env\` credential-var count = $BAKED_VAR_COUNT (want 0)"

# (b.2) no image layer contains the literal key value as a substring. Export
# the image, decompress every layer tar, and grep (count only, never print).
LAYER_MATCH_COUNT=0
IMAGE_TAR="$WORKDIR/image.tar"
docker save "$IMAGE_TAG" -o "$IMAGE_TAR" 2>>"$WORKDIR/docker-build.log"
EXTRACT_DIR="$WORKDIR/extracted"
mkdir -p "$EXTRACT_DIR"
tar -xf "$IMAGE_TAR" -C "$EXTRACT_DIR" 2>>"$WORKDIR/docker-build.log"
while IFS= read -r -d '' layer_tar; do
  count="$(tar -xO -f "$layer_tar" 2>/dev/null | grep -a -c -F -- "$OPENAI_API_KEY" || true)"
  count="${count:-0}"
  LAYER_MATCH_COUNT=$((LAYER_MATCH_COUNT + count))
done < <(find "$EXTRACT_DIR" -type f -name "*.tar" -print0)
if [ "$LAYER_MATCH_COUNT" -eq 0 ]; then LAYER_OK=0; else LAYER_OK=1; fi
report "docker-image-layers-have-no-key-literal" "$LAYER_OK" "key-literal match count across all layer tars = $LAYER_MATCH_COUNT (want 0)"

# --- Assertion (c): the key is never logged (native or container stderr/stdout) ---
DOCKER_OUT_FILE="$WORKDIR/docker.stdout"
DOCKER_ERR_FILE="$WORKDIR/docker.stderr"
echo '{"mode":"qa","system":"You are a concise, accurate assistant. Answer in one short sentence.","messages":[{"role":"user","content":"How many continents are there on Earth?"}]}' \
  | bash "$SCRIPT_DIR/run-docker.sh" >"$DOCKER_OUT_FILE" 2>"$DOCKER_ERR_FILE"

KEY_LOG_COUNT=0
for f in "$NATIVE_OUT_FILE" "$NATIVE_ERR_FILE" "$DOCKER_OUT_FILE" "$DOCKER_ERR_FILE" "$ENV_DUMP" "$WORKDIR/docker-build.log"; do
  [ -f "$f" ] || continue
  c="$(grep -a -c -F -- "$OPENAI_API_KEY" "$f" || true)"
  c="${c:-0}"
  KEY_LOG_COUNT=$((KEY_LOG_COUNT + c))
done
if [ "$KEY_LOG_COUNT" -eq 0 ]; then LOG_OK=0; else LOG_OK=1; fi
report "key-never-logged" "$LOG_OK" "key-literal match count across native+docker stdout/stderr/build logs = $KEY_LOG_COUNT (want 0)"

# Docker qa smoke sanity (feeds assertion c's evidence, reported separately for clarity)
DOCKER_OK=1
if [ -s "$DOCKER_OUT_FILE" ]; then
  DOCKER_ERROR_FIELD="$(node -e "try{const o=JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'));process.stdout.write(o.error||'')}catch(e){process.stdout.write('PARSE_FAILED')}" "$DOCKER_OUT_FILE")"
  if [ -z "$DOCKER_ERROR_FIELD" ]; then
    DOCKER_OK=0
  fi
fi
report "docker-qa-smoke" "$DOCKER_OK" "containerized qa run's error field empty (see $DOCKER_OUT_FILE)"

echo ""
echo "SUMMARY: $PASS passed, $FAIL failed"
if [ "$FAIL" -eq 0 ]; then
  exit 0
else
  exit 1
fi
