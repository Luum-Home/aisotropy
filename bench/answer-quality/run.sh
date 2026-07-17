#!/usr/bin/env bash
# run.sh — turnkey launcher for the aisotropy end-to-end answer-quality eval.
#
# The operator exports THEIR OWN provider key into the environment; this script
# reads it from the environment and NEVER logs or prints it. With no key set,
# the eval runs on the deterministic stub and clearly labels the output
# "STUB RUN — proves wiring, NOT real answer quality".
#
# Provider selection is PROVIDER-AGNOSTIC (ADR-0009):
#   OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub
#
# Examples:
#   # Real run on OpenRouter, 20 questions from the bundled generic smoke set:
#   export OPENROUTER_API_KEY=sk-...        # your key — never committed, never logged
#   export OPENROUTER_MODEL=qwen/qwen3.7-plus
#   ./bench/answer-quality/run.sh -n 20
#
#   # Real run on YOUR OWN domain Q&A (the ONLY measure that reflects production):
#   ./bench/answer-quality/run.sh -data ./my-domain.json -n 50
#
#   # Judge with a stronger/independent model than the one serving:
#   export AISOTROPY_JUDGE_MODEL=anthropic/claude-3.7-sonnet
#   ./bench/answer-quality/run.sh -n 20
#
#   # Optional cost estimate in USD (provider-specific prices, per 1M tokens):
#   export AISOTROPY_EVAL_IN_PRICE_PER_1M=0.20 AISOTROPY_EVAL_OUT_PRICE_PER_1M=0.80
#   ./bench/answer-quality/run.sh -n 20
#
# All flags are passed straight through to `go run ./bench/answer-quality`.
set -euo pipefail

# Resolve the repo root from this script's location so it runs from any cwd.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

# Report which provider WILL be used, WITHOUT ever printing the key value.
detect_provider() {
  if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then echo "openrouter (OPENROUTER_API_KEY set)";
  elif [[ -n "${OPENAI_API_KEY:-}" ]];   then echo "openai (OPENAI_API_KEY set)";
  elif [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then echo "anthropic (ANTHROPIC_API_KEY set)";
  else echo "stub (no provider key set — STUB run, not real quality)"; fi
}

echo "answer-quality eval — provider: $(detect_provider)"
echo "answer-quality eval — key is read from env and never logged."
echo

exec go run ./bench/answer-quality "$@"
