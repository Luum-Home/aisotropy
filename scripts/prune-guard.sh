#!/usr/bin/env bash
# prune-guard.sh — the evidence gate for any "delete this package as dead code" claim.
#
# WHY THIS EXISTS: a consolidation ("poda") proposal once recommended DELETING
# internal/routing as "dead code, nothing imports it" — copied verbatim from a stale
# doc. The truth: 10 importers, 100% test coverage, publicly re-exported, isolation-
# critical. Deleting it would have broken the build and removed a constitutional
# isolation seam. The lesson: a "dead code" claim must be backed by EXECUTABLE
# EVIDENCE (import graph + tests + reachability), never asserted from a doc read.
#
# This script produces that evidence for a package and AUTO-BLOCKS the prune if any
# signal says the code is live. No delete/demote recommendation should ship without
# attaching its output (see .claude/rules/prune-evidence-gate.md).
#
# USAGE:   scripts/prune-guard.sh <pkg-dir>        e.g. scripts/prune-guard.sh internal/routing
# EXIT:    0 = no live-signal found (a prune MAY be considered — verify further)
#          3 = BLOCKED (live code: imported / tested / wired / public / reachable)
#          2 = usage / environment error
set -uo pipefail

PKG="${1:-}"
if [ -z "$PKG" ]; then
  echo "usage: scripts/prune-guard.sh <pkg-dir e.g. internal/routing>" >&2
  exit 2
fi
cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || { echo "not in a git repo" >&2; exit 2; }
PKG="${PKG#./}"; PKG="${PKG%/}"
if [ ! -d "$PKG" ]; then echo "no such package dir: $PKG" >&2; exit 2; fi

MODULE="$(go list -m 2>/dev/null | head -1)"
[ -n "$MODULE" ] || MODULE="github.com/Luum-Home/aisotropy"
IMPORT_PATH="$MODULE/$PKG"

# --- 0. FAIL-CLOSED: is the target inside a NESTED Go module? ---
# Every check below assumes the target belongs to the ROOT module: the import
# path is built as "$MODULE/$PKG", `go test ./$PKG/` is run from the root, and
# reachability is analysed from ./cmd/aisotropyd/... . None of that can see into
# a nested module (this repo has 20+: arenas/live/*, examples/*, bench/*,
# external/gollem). Run against one, every probe silently finds nothing and the
# guard used to report 🟢 "no live-signal" — a FALSE GREEN authorising the
# deletion of live code. Observed: `prune-guard.sh arenas/live/aisotropy`
# returned green for a package called unconditionally from its own main().
# A guard that cannot evaluate must BLOCK, never absolve.
nested_mod=""
probe="$PKG"
while [ -n "$probe" ] && [ "$probe" != "." ]; do
  if [ -f "$probe/go.mod" ]; then nested_mod="$probe"; break; fi
  probe="$(dirname "$probe")"
done
if [ -n "$nested_mod" ]; then
  echo "==================================================================="
  echo " prune-guard: CANNOT EVALUATE  $PKG"
  echo "==================================================================="
  echo "0) NESTED MODULE: $nested_mod/go.mod owns this path — it is NOT part of"
  echo "   the root module ($MODULE)."
  echo "   Every check in this guard resolves against the root module, so all of"
  echo "   them would silently find nothing here and report a FALSE GREEN."
  echo ""
  echo " VERDICT: 🚫 BLOCKED — out of this guard's reach, not proven dead."
  echo " Apply the five criteria MANUALLY from inside that module:"
  echo "   cd $nested_mod"
  echo "   1. importers:    git grep -n '<symbol>' -- '*.go'"
  echo "   2. coverage:     go test ./... -cover"
  echo "   3. wiring:       grep the module's own composition root (main.go)"
  echo "   4. re-export:    is it exported and referenced outside its package?"
  echo "   5. reachability: deadcode ./... (from that module's own main)"
  echo " Record each answer with its evidence before deleting anything."
  echo "==================================================================="
  exit 3
fi

echo "==================================================================="
echo " prune-guard: is  $PKG  safe to delete as dead code?"
echo " import path: $IMPORT_PATH"
echo "==================================================================="

BLOCKED=0
declare -a REASONS=()

# --- 1. Importers OUTSIDE the package (tests INCLUDED: a test that exercises it means it is live) ---
importers="$(git grep -l "\"$IMPORT_PATH\"" -- '*.go' 2>/dev/null | grep -v "^$PKG/" || true)"
n_importers="$(printf '%s' "$importers" | grep -c . || true)"
echo
echo "1) IMPORTERS outside the package: $n_importers"
if [ "$n_importers" -gt 0 ]; then
  printf '     %s\n' $importers
  BLOCKED=1; REASONS+=("imported by $n_importers file(s) outside the package")
fi

# --- 2. Test coverage (its own tests) ---
# "NA" used to mean two different things: "ran fine, package has no tests" (0%,
# legitimately non-blocking) and "go test could not even run" (build failure —
# an ABSENCE OF SIGNAL, not a measurement of 0%). Capture go test's own exit
# code so the two are never conflated (.claude/rules/absence-has-many-causes.md).
go_test_out="$(go test "./$PKG/" -cover 2>&1)"
go_test_rc=$?
cov="$(printf '%s\n' "$go_test_out" | grep -oE 'coverage: [0-9.]+%' | grep -oE '[0-9.]+' | head -1)"
echo
if [ -n "$cov" ]; then
  echo "2) OWN TEST COVERAGE: ${cov}%"
  if awk "BEGIN{exit !($cov>0)}"; then
    BLOCKED=1; REASONS+=("has its own tests (${cov}% coverage)")
  fi
elif [ "$go_test_rc" -ne 0 ]; then
  echo "2) OWN TEST COVERAGE: COULD NOT EVALUATE — go test exited $go_test_rc"
  echo "     (build/test failure, not a measured 0% — fail-closed, not absolved)"
  BLOCKED=1; REASONS+=("could not evaluate own test coverage — go test failed (exit $go_test_rc)")
else
  echo "2) OWN TEST COVERAGE: 0% (go test ran, no test files in this package)"
fi

# --- 3. Wired in the composition root ---
in_root="$(printf '%s\n' $importers | grep -E '^(aisotropy/app\.go|aisotropy/aisotropy\.go|cmd/aisotropyd/)' || true)"
echo
echo "3) WIRED IN COMPOSITION ROOT (app.go / cmd/aisotropyd): $([ -n "$in_root" ] && echo YES || echo no)"
if [ -n "$in_root" ]; then
  printf '     %s\n' $in_root
  BLOCKED=1; REASONS+=("wired in the composition root")
fi

# --- 4. Publicly re-exported on the aisotropy/ façade ---
public="$(printf '%s\n' $importers | grep -E '^aisotropy/(extend|knowledge|app|runtask|runtasks|sandbox|config|knowledge)\.go' || true)"
echo
echo "4) RE-EXPORTED ON THE PUBLIC FAÇADE (aisotropy/): $([ -n "$public" ] && echo YES || echo no)"
if [ -n "$public" ]; then
  printf '     %s\n' $public
  BLOCKED=1; REASONS+=("re-exported on the public aisotropy/ façade")
fi

# --- 5. Reachability via the authoritative Go analyzer (golang.org/x/tools/cmd/deadcode) ---
# D4: `unreach="$(deadcode ... 2>/dev/null | grep ... | wc -l)"` used to fold deadcode's
# exit status into a pipeline and throw it away with `2>/dev/null`. If deadcode failed
# to run at all (not installed is handled separately below, but also: panic, build
# error, wrong module, wrong Go version, OOM under this machine's own resource
# pressure...) stdout is simply EMPTY, so `grep | wc -l` silently yields 0 — and the
# script printed "deadcode flags 0 funcs ... => reachable from main", asserting a
# measurement that never happened. Absence of output is not the same as a measured
# zero (.claude/rules/absence-has-many-causes.md, .claude/rules/prune-evidence-gate.md
# "a green from a tool that could not run its checks is absence of evidence, not
# evidence of absence"). Fix: capture deadcode's OWN exit code before piping its
# stdout through grep/wc, and never claim "0 unreachable" unless deadcode actually
# completed. A failed run still BLOCKS (fail-closed) — it just says so honestly
# instead of inventing a reachability finding.
echo
if command -v deadcode >/dev/null 2>&1; then
  # deadcode reports UNREACHABLE funcs from main. If NONE of this package's funcs are
  # unreachable, it is reachable => live. We check: does the package appear in the
  # unreachable set for EVERY one of its funcs? If any func is absent from the
  # unreachable list, it is reachable.
  deadcode_stdout="$(deadcode ./cmd/aisotropyd/... 2>/dev/null)"
  deadcode_rc=$?
  if [ "$deadcode_rc" -ne 0 ]; then
    echo "5) REACHABILITY (deadcode from cmd/aisotropyd): COULD NOT EVALUATE"
    echo "     deadcode exited with status $deadcode_rc — it did not complete an analysis,"
    echo "     so no reachability signal was measured for $PKG (an empty/failed run is"
    echo "     NOT the same as \"0 funcs unreachable\" — fail-closed, not absolved)."
    BLOCKED=1; REASONS+=("could not evaluate reachability — deadcode exited with status $deadcode_rc")
  else
    unreach="$(printf '%s\n' "$deadcode_stdout" | grep "/$PKG/" | wc -l | tr -d ' ')"
    total_funcs="$(git grep -hE '^func ' -- "$PKG/"'*.go' 2>/dev/null | grep -v '_test.go' | wc -l | tr -d ' ')"
    echo "5) REACHABILITY (deadcode from cmd/aisotropyd): unreachable funcs in pkg=$unreach"
    if [ "${unreach:-0}" -eq 0 ] && [ "${total_funcs:-0}" -gt 0 ]; then
      echo "     deadcode flags 0 funcs of this package as unreachable => reachable from main"
      BLOCKED=1; REASONS+=("reachable from main (deadcode: 0 of $total_funcs funcs unreachable)")
    fi
  fi
else
  echo "5) REACHABILITY: deadcode not installed — install with:"
  echo "     go install golang.org/x/tools/cmd/deadcode@latest"
  echo "     (skipped; the import/coverage/wiring signals above are already decisive)"
fi

# --- VERDICT ---
echo
echo "==================================================================="
if [ "$BLOCKED" -eq 1 ]; then
  echo " VERDICT: 🔴 BLOCKED — $PKG is LIVE code, NOT dead. Do NOT delete."
  echo " Live signals:"
  printf '   - %s\n' "${REASONS[@]}"
  echo "==================================================================="
  exit 3
else
  echo " VERDICT: 🟢 no live-signal found for $PKG."
  echo " A prune MAY be considered — but confirm intent and run the full suite"
  echo " ( go build ./...  &&  go test ./... ) after removal before committing."
  echo "==================================================================="
  exit 0
fi
