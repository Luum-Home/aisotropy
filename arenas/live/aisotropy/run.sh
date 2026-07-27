#!/usr/bin/env bash
#
# LIVE arena adapter runner for aisotropy (the reference adapter), EXHAUSTIVE
# multi-mode edition.
#
# Usage:
#   echo '{"mode":"qa","system":"...","messages":[{"role":"user","content":"..."}]}' \
#     | bash run.sh
#
# Sources the repo-root .env for OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL
# (only if OPENAI_API_KEY is not already in the environment), wires aisotropy's
# REAL backend tiers via env (e5 embed server, real Qdrant, real rqlite, on-disk
# BadgerDB), builds the WASM guest + adapter once, and pipes stdin through
# unchanged. Emits exactly one JSON object on stdout; all logs go to stderr. The
# API key is never printed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env"

if [[ -z "${OPENAI_API_KEY:-}" && -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${ENV_FILE}"
  set +a
fi

# ---- aisotropy REAL backend tiers (all env-configurable; defaults target the
# local live-arena infra). The Docker runner overrides the hosts with
# host.docker.internal. Nothing here is a secret: the credentials below are the
# reference stack's local-dev placeholders, identical to the defaults in
# docker-compose.yml and deploy/rqlite/users.json, and they only ever reach a
# localhost container. The CHANGE-ME suffix is the contract.
#
# The rqlite URL carries BOTH a credential and ?disableClusterDiscovery=true:
#   - credential — the reference rqlite runs with `-auth`, so an anonymous write
#     is a 401. This lane defaulted to an anonymous URL long after auth landed,
#     which made every aisotropy run fail before reaching the model.
#   - disableClusterDiscovery — gorqlite would otherwise ask the node for peers
#     and then use the address the node ADVERTISES, which is its container
#     hostname (compose deliberately omits -http-adv-addr; see the comment
#     there). That name is DNS-dead on the host where this script runs. A
#     single-node store has no peers worth discovering.
# The runner injects the same values (frameworkInfraEnv); these defaults are what
# make a DIRECT invocation of this script work too — which is how the breakage
# above was found, one smoke call before a ~1,100-call board. ----
export AISOTROPY_EMBED_URL="${AISOTROPY_EMBED_URL:-http://127.0.0.1:8899}"
export AISOTROPY_EMBED_MODEL="${AISOTROPY_EMBED_MODEL:-multilingual-e5-small-query}"
export AISOTROPY_EMBED_DIM="${AISOTROPY_EMBED_DIM:-384}"
export QDRANT_HOST="${QDRANT_HOST:-localhost}"
export QDRANT_PORT="${QDRANT_PORT:-6334}"
export QDRANT_COLLECTION="${QDRANT_COLLECTION:-aisotropy_arena}"
export QDRANT_REST_URL="${QDRANT_REST_URL:-http://localhost:6333}"
export QDRANT_API_KEY="${QDRANT_API_KEY:-local-dev-qdrant-key-CHANGE-ME}"
export RQLITE_URL="${RQLITE_URL:-http://aisotropy:local-dev-rqlite-pw-CHANGE-ME@localhost:4001/?disableClusterDiscovery=true}"
# On-disk BadgerDB working memory. STABLE by default so the two `memory`-mode
# process invocations (same session_id) share the same store and recall works.
export AISOTROPY_DATA_DIR="${AISOTROPY_DATA_DIR:-${SCRIPT_DIR}/.aisotropy-data}"
export AISOTROPY_ARENA_CORPUS_DIR="${AISOTROPY_ARENA_CORPUS_DIR:-${REPO_ROOT}/arenas/live/_shared/corpus}"
# Benchmark harness sends only legitimate prompts; disable injection blocking so a
# heuristic false-positive never turns into a refused turn (a quality artifact
# unrelated to the framework under test). Redaction + detection still run.
export AISOTROPY_GUARDRAILS_BLOCK_INJECTION="${AISOTROPY_GUARDRAILS_BLOCK_INJECTION:-false}"

BIN="${SCRIPT_DIR}/adapter-bin"
# Rebuild if the binary is missing or any source file (or the guest) is newer.
if [[ ! -x "${BIN}" ]] || \
   [[ -n "$(find "${SCRIPT_DIR}" -maxdepth 2 -name '*.go' -newer "${BIN}" 2>/dev/null)" ]]; then
  bash "${SCRIPT_DIR}/build.sh" "${BIN}" >&2
fi

exec "${BIN}"
