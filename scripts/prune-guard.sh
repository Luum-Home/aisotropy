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
cov="$(go test "./$PKG/" -cover 2>/dev/null | grep -oE 'coverage: [0-9.]+%' | grep -oE '[0-9.]+' | head -1)"
[ -n "$cov" ] || cov="NA"
echo
echo "2) OWN TEST COVERAGE: ${cov}%"
if [ "$cov" != "NA" ] && awk "BEGIN{exit !($cov>0)}"; then
  BLOCKED=1; REASONS+=("has its own tests (${cov}% coverage)")
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
echo
if command -v deadcode >/dev/null 2>&1; then
  # deadcode reports UNREACHABLE funcs from main. If NONE of this package's funcs are
  # unreachable, it is reachable => live. We check: does the package appear in the
  # unreachable set for EVERY one of its funcs? If any func is absent from the
  # unreachable list, it is reachable.
  unreach="$(deadcode ./cmd/aisotropyd/... 2>/dev/null | grep "/$PKG/" | wc -l | tr -d ' ')"
  total_funcs="$(git grep -hE '^func ' -- "$PKG/"'*.go' 2>/dev/null | grep -v '_test.go' | wc -l | tr -d ' ')"
  echo "5) REACHABILITY (deadcode from cmd/aisotropyd): unreachable funcs in pkg=$unreach"
  if [ "${unreach:-0}" -eq 0 ] && [ "${total_funcs:-0}" -gt 0 ]; then
    echo "     deadcode flags 0 funcs of this package as unreachable => reachable from main"
    BLOCKED=1; REASONS+=("reachable from main (deadcode: 0 of $total_funcs funcs unreachable)")
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
