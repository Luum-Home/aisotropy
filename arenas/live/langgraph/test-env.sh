#!/usr/bin/env bash
# Automated credential/env tests for the LangGraph live-arena adapter.
#
# Assertions (each reported PASS/FAIL independently):
#   (a) native: run.sh reads OPENAI_* from the sourced repo-root .env and a
#       real qa-mode smoke succeeds end-to-end.
#   (b) docker: the built image contains NO literal copy of the API key —
#       neither in its layer history (it is never a build ARG/ENV) nor in
#       `docker run --rm --entrypoint env <image>` (the key is only ever
#       injected at `docker run` time via `-e`, never baked in).
#   (c) logs: the API key never appears in the native adapter's stdout or
#       stderr from assertion (a).
#
# Never prints the key itself — only reports hit counts (expected: 0).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"
IMAGE="aisotropy-arena-langgraph:test-env"

FAIL=0
TMP_STDERR="$(mktemp)"
TMP_BUILD_LOG="$(mktemp)"
trap 'rm -f "${TMP_STDERR}" "${TMP_BUILD_LOG}"' EXIT

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "FAIL (a): ${ENV_FILE} not found; cannot source OPENAI_* vars"
  exit 1
fi
set -a
# shellcheck disable=SC1090
source "${ENV_FILE}"
set +a

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "FAIL (a): OPENAI_API_KEY is empty after sourcing ${ENV_FILE}"
  FAIL=1
fi
KEY="${OPENAI_API_KEY:-}"

echo "=== (a) native: env-var read + real qa smoke ==="
NATIVE_OUT="$(echo '{"mode":"qa","messages":[{"role":"user","content":"Reply with exactly one word: ack"}]}' \
  | bash "${SCRIPT_DIR}/run.sh" 2>"${TMP_STDERR}")"
NATIVE_STDERR="$(cat "${TMP_STDERR}")"

if echo "${NATIVE_OUT}" | grep -q '"framework": "langgraph"' && echo "${NATIVE_OUT}" | grep -q '"error": ""'; then
  echo "PASS (a): native qa smoke succeeded"
  echo "    ${NATIVE_OUT}"
else
  echo "FAIL (a): native qa smoke did not return a clean envelope"
  echo "    ${NATIVE_OUT}"
  FAIL=1
fi

echo
echo "=== (c) key never appears in native stdout/stderr ==="
if [[ -z "${KEY}" ]]; then
  echo "SKIP (c): no key loaded to search for (assertion (a) already failed on this)"
else
  STDOUT_HITS=0
  STDERR_HITS=0
  echo "${NATIVE_OUT}" | grep -qF "${KEY}" && STDOUT_HITS=1
  echo "${NATIVE_STDERR}" | grep -qF "${KEY}" && STDERR_HITS=1
  if [[ "${STDOUT_HITS}" -eq 0 && "${STDERR_HITS}" -eq 0 ]]; then
    echo "PASS (c): key literal hits in native stdout=0 stderr=0"
  else
    echo "FAIL (c): key literal hits in native stdout=${STDOUT_HITS} stderr=${STDERR_HITS}"
    FAIL=1
  fi
fi

echo
echo "=== (b1) docker image build + layer history contains no key literal ==="
if docker build -t "${IMAGE}" "${SCRIPT_DIR}" >"${TMP_BUILD_LOG}" 2>&1; then
  echo "PASS (b0): docker build succeeded"
else
  echo "FAIL (b0): docker build failed"
  tail -40 "${TMP_BUILD_LOG}"
  FAIL=1
fi

if [[ -n "${KEY}" ]] && docker image inspect "${IMAGE}" >/dev/null 2>&1; then
  HISTORY_HITS="$(docker history --no-trunc "${IMAGE}" 2>/dev/null | grep -cF "${KEY}" || true)"
  if [[ "${HISTORY_HITS}" -eq 0 ]]; then
    echo "PASS (b1): key literal hits in 'docker history' = 0"
  else
    echo "FAIL (b1): key literal hits in 'docker history' = ${HISTORY_HITS}"
    FAIL=1
  fi

  echo
  echo "=== (b2) 'docker run --rm --entrypoint env <image>' contains no key literal ==="
  ENV_DUMP="$(docker run --rm --entrypoint env "${IMAGE}" 2>/dev/null || true)"
  ENV_HITS=0
  if [[ -n "${KEY}" ]]; then
    echo "${ENV_DUMP}" | grep -qF "${KEY}" && ENV_HITS=1
  fi
  if [[ "${ENV_HITS}" -eq 0 ]]; then
    echo "PASS (b2): key literal hits in 'docker run ... env' = 0"
  else
    echo "FAIL (b2): key literal hits in 'docker run ... env' = ${ENV_HITS}"
    FAIL=1
  fi
else
  echo "FAIL (b1/b2): image not available to inspect (build must have failed)"
  FAIL=1
fi

echo
if [[ "${FAIL}" -eq 0 ]]; then
  echo "RESULT: ALL PASS"
  exit 0
else
  echo "RESULT: SOME FAIL"
  exit 1
fi
