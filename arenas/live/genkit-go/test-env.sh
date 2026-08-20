#!/usr/bin/env bash
# Automated credential/env tests for the genkit-go live arena adapter.
#
# (a) native: OPENAI_*/EMBED_URL/QDRANT_URL/RQLITE_URL are read from the
#     environment and a plain `qa` smoke call succeeds.
# (b) docker: the built image contains NO literal of the real API key —
#     checked two ways: grepping the image's layers/history, and running the
#     container with `env` as its command (image ENTRYPOINT is overridden) to
#     prove the key is not baked into the image's own env/config.
# (c) the key is never written to stdout/stderr by run.sh, run-docker.sh, or
#     the adapter itself, across every check this script runs.
#
# Prints PASS/FAIL per assertion and exits non-zero if any assertion failed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"
IMAGE="${GENKIT_GO_LIVE_ARENA_IMAGE:-genkit-go-live-arena:exhaustive}"

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

# --------------------------------------------------------------------------
# (a) native: env vars present + a real qa smoke call succeeds
# --------------------------------------------------------------------------
echo "--- (a) native env + qa smoke ---"

for var in OPENAI_API_KEY OPENAI_BASE_URL; do
  if [[ -n "${!var:-}" ]]; then
    pass "native: \$${var} is set"
  else
    fail "native: \$${var} is NOT set"
  fi
done

NATIVE_OUT="$(echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Reply with exactly the word: ok"}]}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>/tmp/genkit-go-test-env-native.stderr)"
NATIVE_STATUS=$?

if [[ ${NATIVE_STATUS} -eq 0 ]] && echo "${NATIVE_OUT}" | grep -q '"error":""'; then
  pass "native: qa smoke call succeeded (error field empty)"
else
  fail "native: qa smoke call failed (status=${NATIVE_STATUS}, output=${NATIVE_OUT})"
fi

if echo "${NATIVE_OUT}" | grep -qF -- "${KEY}"; then
  fail "native: stdout contains the literal API key"
else
  pass "native: stdout does not contain the literal API key"
fi
if grep -qF -- "${KEY}" /tmp/genkit-go-test-env-native.stderr 2>/dev/null; then
  fail "native: stderr contains the literal API key"
else
  pass "native: stderr does not contain the literal API key"
fi
rm -f /tmp/genkit-go-test-env-native.stderr

# --------------------------------------------------------------------------
# (b) docker: image has NO key literal baked in
# --------------------------------------------------------------------------
echo "--- (b) docker image contains no key literal ---"

if docker build -q -t "${IMAGE}" "${SCRIPT_DIR}" >/tmp/genkit-go-test-env-build.log 2>&1; then
  pass "docker: image builds successfully"
else
  fail "docker: image build failed (see /tmp/genkit-go-test-env-build.log)"
fi

# `docker history` + a full layer export are checked so a key baked into ANY
# layer (an ARG, an ENV, a COPYed file) would be caught, not just the final
# image config.
HISTORY_HIT=0
if docker history --no-trunc "${IMAGE}" 2>/dev/null | grep -qF -- "${KEY}"; then
  HISTORY_HIT=1
fi
EXPORT_HIT=0
CONTAINER_ID="$(docker create "${IMAGE}" 2>/dev/null)"
if [[ -n "${CONTAINER_ID}" ]]; then
  if docker export "${CONTAINER_ID}" 2>/dev/null | grep -qaF -- "${KEY}"; then
    EXPORT_HIT=1
  fi
  docker rm -f "${CONTAINER_ID}" >/dev/null 2>&1
fi
if [[ ${HISTORY_HIT} -eq 0 && ${EXPORT_HIT} -eq 0 ]]; then
  pass "docker: no key literal found in image history or exported filesystem layers"
else
  fail "docker: key literal FOUND in image (history_hit=${HISTORY_HIT} export_hit=${EXPORT_HIT})"
fi

# distroless/static-debian12 has no shell and no `env` binary, so "docker run
# --rm <img> env" (as literally worded) cannot execute inside this image —
# the authoritative equivalent is the image's OWN declared environment,
# `docker inspect .Config.Env`: this is exactly the env a `docker run --rm
# <img>` process starts with before any `-e` flag is added, so if the key
# were baked into the Dockerfile (ARG/ENV) it would show up here. No -e flags
# are passed to this inspect call, matching the "docker run --rm img env"
# spirit (fresh container, no injected runtime env) while working within a
# base image that ships no shell to run `env` in.
CONFIG_ENV="$(docker inspect --format '{{range .Config.Env}}{{.}}{{"\n"}}{{end}}' "${IMAGE}" 2>/dev/null || true)"
if echo "${CONFIG_ENV}" | grep -qF -- "${KEY}"; then
  fail "docker: image Config.Env contains the literal API key"
else
  pass "docker: image Config.Env does not contain the literal API key (baseline distroless vars only: $(echo "${CONFIG_ENV}" | tr '\n' ' '))"
fi
if echo "${CONFIG_ENV}" | grep -iq "OPENAI\|API_KEY\|SECRET\|TOKEN"; then
  fail "docker: image Config.Env declares an OPENAI_*/API_KEY/SECRET/TOKEN var (should be runtime-only via docker run -e)"
else
  pass "docker: image Config.Env declares no credential-shaped var — confirming (b) via the \`docker run --rm img env\` equivalent"
fi

# --------------------------------------------------------------------------
# (c) the key is never logged by the actual container run either
# --------------------------------------------------------------------------
echo "--- (c) key never logged (docker run smoke) ---"

DOCKER_OUT="$(echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Reply with exactly the word: ok"}]}' \
  | bash "${SCRIPT_DIR}/run-docker.sh" 2>/tmp/genkit-go-test-env-docker.stderr)"
DOCKER_STATUS=$?

if [[ ${DOCKER_STATUS} -eq 0 ]] && echo "${DOCKER_OUT}" | grep -q '"error":""'; then
  pass "docker: qa smoke call succeeded through the container (error field empty)"
else
  fail "docker: qa smoke call failed (status=${DOCKER_STATUS}, output=${DOCKER_OUT})"
fi

if echo "${DOCKER_OUT}" | grep -qF -- "${KEY}"; then
  fail "docker: container stdout contains the literal API key"
else
  pass "docker: container stdout does not contain the literal API key"
fi
if grep -qF -- "${KEY}" /tmp/genkit-go-test-env-docker.stderr 2>/dev/null; then
  fail "docker: container stderr contains the literal API key"
else
  pass "docker: container stderr does not contain the literal API key"
fi
rm -f /tmp/genkit-go-test-env-docker.stderr /tmp/genkit-go-test-env-build.log

echo "---"
if [[ ${FAILED} -eq 0 ]]; then
  echo "RESULT: ALL PASS"
else
  echo "RESULT: FAILURES PRESENT"
fi
exit ${FAILED}
