#!/usr/bin/env bash
# owasp-llm-scan.sh — the one command to re-verify aisotropy's DETERMINISTIC,
# OFFLINE OWASP security coverage (executable-evidence norm).
#
# It runs the entire OWASP deterministic suite (Go test funcs named TestOWASP_*)
# with a hermetic, offline environment — stub LLM, in-process stores, no OTLP
# endpoint — so there are ZERO real model calls and ZERO network I/O. It prints a
# per-category PASS/FAIL summary table and surfaces any FINDING (a security test
# skipped with t.Skip("FINDING: ...") because it uncovered a real vulnerability).
#
# It COMPLEMENTS (does not replace) the live arena's real-model safeguard probes
# (arenas/live, -modes safeguard). See docs/security/owasp-llm-coverage.md and
# docs/security/owasp-agentic-coverage.md.
#
# Exit codes:
#   0  all categories passed and no FINDING present  (clean)
#   1  a security test failed, or a FINDING is present
#   2  error (build failure, tooling missing, bad invocation)
#
# Usage:  bash scripts/owasp-llm-scan.sh [-v]
#   -v   verbose: stream full `go test` output per category

set -u
cd "$(dirname "$0")/.." || { echo "cannot cd to repo root" >&2; exit 2; }

VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

command -v go >/dev/null 2>&1 || { echo "ERROR: go toolchain not found" >&2; exit 2; }

# --- Hermetic offline env: force the keyless stub + in-process backends -------
# Unset every provider key and networked-store/telemetry endpoint so a run can
# never make a real model call or reach rqlite/Qdrant/an OTLP collector.
unset ANTHROPIC_API_KEY OPENAI_API_KEY OPENROUTER_API_KEY \
      RQLITE_URL QDRANT_HOST QDRANT_PORT QDRANT_COLLECTION \
      OTEL_EXPORTER_OTLP_ENDPOINT OTEL_EXPORTER_OTLP_TRACES_ENDPOINT \
      AISOTROPY_OTEL_CAPTURE_CONTENT 2>/dev/null || true
export GOFLAGS="-count=1"   # never serve a cached PASS

# --- Category registry: code | title | run-pattern | packages ---------------
# Each row is one OWASP category mapped to its deterministic test(s). No blank
# cells: categories with no unit test (LLM03/LLM09) are reported N/A here and
# documented with their covered-by/reason in the coverage doc.
ROWS=(
  "LLM01|Prompt Injection|TestOWASP_LLM01|./internal/guardrails/ ./internal/api/"
  "LLM02|Sensitive Information Disclosure|TestOWASP_LLM02|./internal/llm/ ./internal/o11y/"
  "LLM04|Data and Model Poisoning|TestOWASP_LLM04|./internal/guardrails/ ./internal/api/"
  "LLM05|Improper Output Handling|TestOWASP_LLM05|./internal/guardrails/"
  "LLM06|Excessive Agency|TestOWASP_LLM06|./internal/tools/"
  "LLM07|System Prompt Leakage|TestOWASP_LLM07|./internal/guardrails/ ./internal/llm/ ./internal/api/"
  "LLM08|Vector and Embedding Weaknesses|TestOWASP_LLM08|./internal/semantic/"
  "LLM10|Unbounded Consumption|TestOWASP_LLM10|./internal/memory/"
  "ASI02|Agentic: Tool Misuse / Privilege Abuse|TestOWASP_ASI02|./internal/agentloop/"
  "ASI05|Agentic: Unexpected Code Execution|TestOWASP_ASI05|./internal/sandbox/"
)

NA_ROWS=(
  "LLM03|Supply Chain"
  "LLM09|Misinformation"
)

echo "=================================================================="
echo " OWASP deterministic offline security scan — aisotropy"
echo " (stub LLM, in-process stores, zero real calls, zero network)"
echo "=================================================================="
printf "%-7s %-42s %-8s\n" "CODE" "CATEGORY" "RESULT"
echo "------------------------------------------------------------------"

overall=0
fail_count=0
pass_count=0

for row in "${ROWS[@]}"; do
  IFS='|' read -r code title pattern pkgs <<< "$row"
  # shellcheck disable=SC2086
  if [ "$VERBOSE" -eq 1 ]; then
    echo ">>> $code $title"
    if go test -run "$pattern" $pkgs; then res="PASS"; else res="FAIL"; fi
  else
    out=$(go test -run "$pattern" $pkgs 2>&1)
    rc=$?
    if [ $rc -eq 0 ]; then res="PASS"; else res="FAIL"; fi
  fi
  if [ "$res" = "PASS" ]; then
    pass_count=$((pass_count+1))
  else
    fail_count=$((fail_count+1)); overall=1
    [ "$VERBOSE" -eq 0 ] && printf '%s\n' "$out" | tail -20
  fi
  printf "%-7s %-42s %-8s\n" "$code" "$title" "$res"
done

echo "------------------------------------------------------------------"
for row in "${NA_ROWS[@]}"; do
  IFS='|' read -r code title <<< "$row"
  printf "%-7s %-42s %-8s\n" "$code" "$title (see coverage doc)" "N/A"
done
echo "------------------------------------------------------------------"

# --- FINDING detection: a security test that uncovered a real vulnerability --
# Convention: such a test is skipped with t.Skip("FINDING: <desc> ...").
findings=$(grep -rns 't.Skip("FINDING' internal/ --include='owasp_*_test.go' 2>/dev/null || true)
if [ -n "$findings" ]; then
  echo "SECURITY FINDINGS PRESENT (see docs/security/owasp-llm-coverage.md):"
  printf '%s\n' "$findings"
  overall=1
else
  echo "FINDINGS: none (no vulnerability markers in the deterministic suite)"
fi

echo "------------------------------------------------------------------"
echo "Summary: ${pass_count} passed, ${fail_count} failed."
if [ "$overall" -eq 0 ]; then
  echo "RESULT: CLEAN (exit 0)"
else
  echo "RESULT: ATTENTION REQUIRED (exit 1)"
fi
exit "$overall"
