#!/usr/bin/env bash
# test-all.sh — every test lane this repository owns, in one command.
#
# WHY THIS EXISTS
#
# Nothing here was unrunnable. Every lane below already worked; what was missing
# was anything that ran them TOGETHER, and the cost of that gap was paid several
# times in one day:
#
#   - `go test ./...` does not descend into nested modules, so a stale go.mod in
#     arenas/live/aisotropy silently invalidated a 58-run diagnostic — the runs
#     had been measuring a build that no longer compiled.
#   - The same blind spot let a grpc CVE bump break 13 nested modules with a
#     green root suite.
#   - Two lanes (`//go:build manual`, `//go:build keybroker`) are excluded from
#     the default build by design and therefore never ran at all.
#   - A four-day-old spring-ai jar kept running old code because nothing checked
#     freshness.
#
# A green root suite says almost nothing about a repository with 20+ modules,
# two build tags, a Rust crate and six standalone guards.
#
# THE RULE THIS SCRIPT ENFORCES ON ITSELF: a layer that could not run is NOT
# green. SKIPPED is reported separately from PASS, always with its reason, and
# the final verdict names the skips. "Nothing found" is not "nothing to find" —
# the same discipline the prune guard learned when it absolved a package inside
# a module it could not analyse.
#
# Usage: test-all.sh [--quick] [--with-guards]
#   --quick        root module + nested modules only (no tags, no Rust, no guards)
#   --with-guards  also run the standalone guards (determinism, OWASP, golden,
#                  supply-chain, arena integration)
#
# Exit codes: 0 all executed layers passed · 1 a layer failed · 2 usage error.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}" || exit 2

QUICK=0
GUARDS=0
for arg in "$@"; do
  case "${arg}" in
    --quick) QUICK=1 ;;
    --with-guards) GUARDS=1 ;;
    *) echo "unknown flag: ${arg}" >&2; exit 2 ;;
  esac
done

PASSED=(); FAILED=(); SKIPPED=()
pass()  { PASSED+=("$1");  printf '   \033[32mPASS\033[0m     %s\n' "$1"; }
fail()  { FAILED+=("$1");  printf '   \033[31mFAIL\033[0m     %s\n' "$1"; }
skip()  { SKIPPED+=("$1"); printf '   \033[33mSKIP\033[0m     %s\n' "$1"; }

hdr() { printf '\n%s\n' "$1"; }

echo "==================================================================="
echo " test-all: every lane this repository owns"
echo "==================================================================="

# ---- layer 1: root module ---------------------------------------------------
hdr "1) root module"
if ! command -v go >/dev/null 2>&1; then
  skip "go test ./... — 'go' not on PATH"
else
  if go test ./... -count=1 >/tmp/test-all-root.log 2>&1; then
    pass "go test ./...  ($(grep -c '^ok' /tmp/test-all-root.log) package(s) ok)"
  else
    fail "go test ./...  — see /tmp/test-all-root.log"
    grep -E '^(---|FAIL|\s+.*_test\.go:)' /tmp/test-all-root.log | head -8 | sed 's/^/       /'
  fi
fi

# ---- layer 2: build-tagged lanes -------------------------------------------
# Excluded from the default build ON PURPOSE, which is exactly why nothing ever
# ran them. They self-skip when their prerequisites (a provider key, a scorer
# binary, a model dir) are absent, so running them is always safe: they either
# execute or skip with a stated reason.
if [[ ${QUICK} -eq 0 ]]; then
  hdr "2) build-tagged lanes (never covered by 'go test ./...')"
  for tag in manual keybroker; do
    # -json, not plain output: `go test` prints "--- SKIP" only in verbose mode,
    # so counting skips from the default output always yields 0 — a number that
    # looks measured and is not. These lanes self-skip when a provider key or a
    # scorer binary is absent, and how many DID skip is the single most useful
    # fact about the run: it separates "ran and passed" from "was not exercised".
    if go test -tags "${tag}" -json ./... -count=1 >"/tmp/test-all-${tag}.log" 2>&1; then
      # A pass event WITHOUT a "Test" field is the package verdict; with one it
      # is an individual test. Conflating them reports thousands of "packages",
      # which is a number that reads as precision and is simply the wrong unit.
      ran=$(grep '"Action":"pass"' "/tmp/test-all-${tag}.log" | grep -vc '"Test":')
      skipped=$(grep -c '"Action":"skip".*"Test":' "/tmp/test-all-${tag}.log")
      note="${ran} package(s) ok"
      if [[ "${skipped}" -gt 0 ]]; then
        note="${note}, ${skipped} test(s) SELF-SKIPPED (prerequisite absent — not exercised)"
      fi
      pass "-tags ${tag}  (${note})"
    else
      fail "-tags ${tag}  — see /tmp/test-all-${tag}.log"
      grep -E '^(---|FAIL)' "/tmp/test-all-${tag}.log" | head -6 | sed 's/^/       /'
    fi
  done
else
  skip "build-tagged lanes — --quick"
fi

# ---- layer 3: nested modules ------------------------------------------------
# The blind spot that cost the most. Each nested module is its own build+test
# universe and the root suite cannot see into any of them.
hdr "3) nested Go modules"
NESTED=0
while IFS= read -r mod; do
  [[ -z "${mod}" ]] && continue
  NESTED=$((NESTED + 1))
  name="${mod#./}"
  if find "${mod}" -name '*_test.go' -not -path '*/node_modules/*' 2>/dev/null | grep -q .; then
    if (cd "${mod}" && go test ./... -count=1) >"/tmp/test-all-nested.log" 2>&1; then
      pass "${name}  (tests)"
    elif (cd "${mod}" && go test -tags keybroker ./... -count=1) >>"/tmp/test-all-nested.log" 2>&1; then
      pass "${name}  (tests, -tags keybroker)"
    else
      fail "${name}  — see /tmp/test-all-nested.log"
    fi
  else
    # No tests is not a failure, but it IS worth saying: a module that only
    # builds is covered against rot, not against defects.
    if (cd "${mod}" && go build ./...) >/dev/null 2>&1 ||
       (cd "${mod}" && go build -tags keybroker ./...) >/dev/null 2>&1; then
      pass "${name}  (builds; no tests of its own)"
    else
      fail "${name}  — does not build"
    fi
  fi
done < <(find . -name go.mod -maxdepth 4 -not -path './external/*' -not -path '*/node_modules/*' 2>/dev/null \
          | xargs -n1 dirname | grep -v '^\.$' | sort)
echo "   (${NESTED} nested module(s); ./external/* is vendored upstream and excluded)"

# ---- layer 4: Rust ----------------------------------------------------------
if [[ ${QUICK} -eq 0 ]]; then
  hdr "4) Rust crates"
  if ! command -v cargo >/dev/null 2>&1; then
    skip "crates/sleep-cycle — 'cargo' not on PATH"
  elif [[ -f crates/sleep-cycle/Cargo.toml ]]; then
    if (cd crates/sleep-cycle && cargo test --quiet) >/tmp/test-all-rust.log 2>&1; then
      pass "crates/sleep-cycle"
    else
      fail "crates/sleep-cycle — see /tmp/test-all-rust.log"
    fi
  else
    skip "no Rust crates found"
  fi
else
  skip "Rust — --quick"
fi

# ---- layer 5: standalone guards --------------------------------------------
if [[ ${GUARDS} -eq 1 ]]; then
  hdr "5) standalone guards"
  run_guard() { # name, script, extra note for exit 2
    local name="$1" script="$2"
    if [[ ! -f "${script}" ]]; then skip "${name} — ${script} missing"; return; fi
    bash "${script}" >"/tmp/test-all-guard.log" 2>&1
    local rc=$?
    case ${rc} in
      0) pass "${name}" ;;
      2) skip "${name} — could not run (exit 2): $(tail -1 /tmp/test-all-guard.log | cut -c1-70)" ;;
      *) fail "${name} (exit ${rc}) — see /tmp/test-all-guard.log" ;;
    esac
  }
  run_guard "arena determinism"  "scripts/arena-determinism-guard.sh"
  run_guard "arena integration"  "scripts/arena-integration-gate.sh"
  run_guard "OWASP LLM scan"     "scripts/owasp-llm-scan.sh"
  run_guard "supply chain"       "scripts/supply-chain-guard.sh"
else
  skip "standalone guards — pass --with-guards"
fi

# ---- verdict ----------------------------------------------------------------
echo
echo "==================================================================="
printf ' %d passed · %d failed · %d skipped\n' "${#PASSED[@]}" "${#FAILED[@]}" "${#SKIPPED[@]}"
if [[ ${#SKIPPED[@]} -gt 0 ]]; then
  echo
  echo " SKIPPED layers are NOT green — they were never executed:"
  for s in "${SKIPPED[@]}"; do echo "   · ${s}"; done
fi
if [[ ${#FAILED[@]} -gt 0 ]]; then
  echo
  echo " FAILED:"
  for f in "${FAILED[@]}"; do echo "   · ${f}"; done
  echo "==================================================================="
  exit 1
fi
echo
echo " Every EXECUTED layer passed."
echo "==================================================================="
exit 0
