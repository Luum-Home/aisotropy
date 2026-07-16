#!/usr/bin/env bash
# run-overhead-bench.sh — the reproducible runner behind arenas/BENCHMARKS.md.
#
# It measures aisotropy's OWN framework/runtime overhead with the model held
# constant as the keyless stub, so every number is the framework's cost, never a
# model's performance (which is not a framework property — see BENCHMARKS.md
# "Why the model is held constant"). It runs entirely offline: no ANTHROPIC_API_KEY,
# no network, no provider.
#
# What it runs (all in the main Go module github.com/Luum-Home/aisotropy):
#   1. CLASS 1 — micro-benchmarks over the hot paths a turn touches:
#        internal/tools    scope-gated tool selection + prompt-fragment render
#        internal/memory   key scoping/validation + working-memory read/write
#   2. CLASS 1 — the end-to-end in-process turn (aisotropy.BenchmarkTurn): one
#      full App.Turn with the zero-latency stub.
#   3. CLASS 2b (effectiveness, no model) — the token-counting cost-governance
#      proofs (quota pre-spend gate + response cache) as PASS/FAIL tests, plus the
#      bitrate measurement-path harness. These emit t.Log lines with the token
#      deltas; run with -v to see them.
#
# Usage:
#   arenas/scripts/run-overhead-bench.sh            # default 1s/benchmark
#   BENCHTIME=3s arenas/scripts/run-overhead-bench.sh
#   arenas/scripts/run-overhead-bench.sh -v          # also show the proof logs
#
# NOTE ON CROSS-FRAMEWORK NUMBERS: this script measures aisotropy only. A full
# cross-framework wall-clock table requires each arena's runtime installed and
# its stub-backed path driven under an identical load generator — the method is
# documented in BENCHMARKS.md "Cross-framework method (not yet executed)". This
# script deliberately does NOT fabricate the other frameworks' numbers.

set -euo pipefail

# Resolve the repo root (this script lives at arenas/scripts/).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

BENCHTIME="${BENCHTIME:-1s}"
VERBOSE=""
if [[ "${1:-}" == "-v" ]]; then
  VERBOSE="-v"
fi

echo "# aisotropy framework-overhead benchmarks (model held constant: keyless stub)"
echo "# repo:      ${REPO_ROOT}"
echo "# go:        $(go version)"
echo "# benchtime: ${BENCHTIME}"
echo "# date:      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

echo "== CLASS 1: hot-path micro-benchmarks (tools + memory) =="
go test -run=X -bench=. -benchmem -benchtime="${BENCHTIME}" \
  ./internal/tools/ ./internal/memory/ | grep -E '^(Benchmark|ok|PASS|FAIL|goos|goarch|cpu|pkg)'
echo

echo "== CLASS 1: end-to-end in-process turn (aisotropy.BenchmarkTurn) =="
go test -run=X -bench=BenchmarkTurn -benchmem -benchtime="${BENCHTIME}" \
  ./aisotropy/ 2>/dev/null | grep -E '^(Benchmark|ok|PASS|FAIL|goos|goarch|cpu|pkg)'
echo

echo "== CLASS 2b: cost-governance effectiveness proofs (no model, token-counting stub) =="
go test ${VERBOSE} \
  -run 'TestQuotaGatePreSpendReducesTokenSpend|TestResponseCacheReducesTokenSpend' \
  ./aisotropy/ 2>/dev/null | grep -E '(PASS|FAIL|ok|QUOTA GATE|RESPONSE CACHE|---)'
echo

echo "== CLASS 2a plumbing: bitrate measurement-path harness (no model) =="
go test ${VERBOSE} -run TestBitrateMeasurementPathNoModel \
  ./internal/bitrate/ 2>/dev/null | grep -E '(PASS|FAIL|ok|lift=|---)'
echo

echo "# Done. See arenas/BENCHMARKS.md for interpretation and the cross-framework method."
