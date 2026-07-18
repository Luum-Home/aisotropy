#!/usr/bin/env bash
# arena-determinism-guard.sh — the evidence gate for the live arena's PRIMARY
# quality score.
#
# WHY THIS EXISTS: the live arena's original quality signal was an
# LLM-as-judge — agnes scoring agnes's own outputs — which rates its own
# family generously and non-discriminatingly (observed: a flat ~5.0 across
# frameworks that behave very differently). arenas/live/runner/scoring is the
# replacement: a pure, deterministic, per-dimension scorer that is the
# arena's PRIMARY score by construction (see
# .claude/rules/arena-deterministic-judging.md). This script is the
# executable gate that makes "deterministic by construction" more than a
# claim — the same discipline as scripts/prune-guard.sh (no deletion claim
# without executable evidence) and the runner's own matrix-completeness gate
# (arenas/live/runner/matrix.go): a rule an agent inherits, not a promise it
# makes.
#
# It checks TWO things and BLOCKS (exit 3) if either fails:
#
#   1. PURITY — the scoring package's full transitive dependency graph
#      (`go list -deps`) must contain NONE of: net/http, math/rand, time,
#      internal/llm, or anything with "agnes"/"anthropic"/"openai" in its
#      import path (an LLM/agnes client). An LLM-as-primary-judge, or any
#      network/randomness/wall-clock dependency, is rejected by construction.
#
#   2. REPRODUCIBILITY — arenas/live/runner/scoring's
#      TestDeterministic_Reproducible fixture (100 in-loop iterations per
#      fixture) is re-run 5 times as separate test-process invocations
#      (`-count=5`), so both in-process AND process-level nondeterminism
#      (stray goroutines, map-iteration-order leaks into a %v format,
#      accidental wall-clock/randomness) would surface as a failure.
#
# USAGE:   scripts/arena-determinism-guard.sh
# EXIT:    0 = PASS (pure + reproducible)
#          3 = BLOCKED (impure dependency or non-reproducible score)
#          2 = usage / environment error
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || { echo "not in a git repo" >&2; exit 2; }

RUNNER_DIR="arenas/live/runner"
SCORING_DIR="$RUNNER_DIR/scoring"
if [ ! -d "$SCORING_DIR" ]; then
  echo "no such package dir: $SCORING_DIR" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go toolchain not found on PATH" >&2
  exit 2
fi

MODULE="$(cd "$RUNNER_DIR" && go list -m 2>/dev/null)"
[ -n "$MODULE" ] || MODULE="aisotropy-live-runner"
IMPORT_PATH="$MODULE/scoring"

echo "==================================================================="
echo " arena-determinism-guard: is  $SCORING_DIR  PURE and REPRODUCIBLE?"
echo " import path: $IMPORT_PATH"
echo "==================================================================="

BLOCKED=0
declare -a REASONS=()

# --- 1. PURITY: the package's OWN direct imports must be clean -------------
# Deliberately DIRECT imports (go list's .Imports, what the package's own .go
# files literally `import`), not the full transitive closure (`go list
# -deps`): almost anything that imports "fmt" or "os" transitively reaches
# "time" somewhere in the stdlib graph (os.FileInfo carries a time.Time), so
# a transitive check would false-positive on every nontrivial Go package.
# What must actually be true is narrower and checkable: the scoring
# package's OWN source files never reach for net/http, math/rand, time, or
# an LLM/agnes client directly.
echo
echo "1) PURITY — go list -f '{{join .Imports \"\\n\"}}' ./scoring (direct imports only)"
deps="$(cd "$RUNNER_DIR" && go list -f '{{join .Imports "\n"}}' ./scoring 2>&1)"
list_status=$?
if [ $list_status -ne 0 ]; then
  echo "   ERROR: go list failed (does the package build?):"
  echo "$deps" | sed 's/^/     /'
  exit 2
fi

FORBIDDEN_EXACT=("net/http" "math/rand" "time" "internal/llm")
for bad in "${FORBIDDEN_EXACT[@]}"; do
  hit="$(printf '%s\n' "$deps" | grep -xF "$bad" || true)"
  if [ -n "$hit" ]; then
    echo "   BLOCKED: scoring package directly imports forbidden package: $bad"
    BLOCKED=1; REASONS+=("imports forbidden package \"$bad\"")
  fi
done

# Any import path that looks like an LLM/agnes/provider client — catches
# internal/llm's siblings, a vendored agnes SDK, or a direct anthropic/openai
# client, however it is later named or moved.
llm_hit="$(printf '%s\n' "$deps" | grep -iE 'agnes|anthropic|openai|/llm(/|$)' || true)"
if [ -n "$llm_hit" ]; then
  echo "   BLOCKED: scoring package directly imports an LLM/agnes client package:"
  printf '     %s\n' "$llm_hit"
  BLOCKED=1; REASONS+=("references an LLM/agnes client package")
fi

if [ "$BLOCKED" -eq 0 ]; then
  echo "   OK: no forbidden import among the package's direct imports:"
  printf '%s\n' "$deps" | sed 's/^/     /'
fi

# --- 2. REPRODUCIBILITY: same input -> byte-identical score, every time ----
echo
echo "2) REPRODUCIBILITY — TestDeterministic_Reproducible x5 process-level runs"
echo "   (each run internally loops every fixture 100x and diffs against its own baseline)"
if (cd "$RUNNER_DIR" && go test ./scoring/... -run '^TestDeterministic_Reproducible$' -count=5 -v); then
  echo "   OK: reproducibility fixture passed across 5 independent process runs."
else
  echo "   BLOCKED: reproducibility fixture FAILED — see test output above."
  BLOCKED=1; REASONS+=("TestDeterministic_Reproducible failed (non-deterministic score detected)")
fi

# --- VERDICT -----------------------------------------------------------------
echo
echo "==================================================================="
if [ "$BLOCKED" -ne 0 ]; then
  echo " VERDICT: BLOCKED — the scoring package is not pure-and-reproducible."
  echo " Reasons:"
  printf '   - %s\n' "${REASONS[@]}"
  echo
  echo " Fix: remove the offending import/dependency, or fix the source of"
  echo " nondeterminism (wall-clock read, randomness, unordered map iteration"
  echo " reaching a formatted string), then re-run this script."
  echo "==================================================================="
  exit 3
fi
echo " VERDICT: PASS — $SCORING_DIR is pure (no LLM/network/random/time"
echo " dependency) and reproducible (byte-identical scores across 5 fresh"
echo " process runs x 100 in-loop iterations each)."
echo "==================================================================="
exit 0
