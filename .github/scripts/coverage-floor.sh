#!/usr/bin/env bash
#
# coverage-floor.sh — enforce a weighted statement-coverage floor over the
# "promoted" packages only, ignoring thin main packages and demo/bench code.
#
# Why a custom parser instead of `go tool cover -func | tail -1`?
#   `go test ./... -coverprofile` produces ONE profile spanning every package
#   in the root module — including code we deliberately do NOT unit-test
#   (thin CLI mains, benchmark/demo mains, analyzer command wrappers). Letting
#   those drag the number down (or, worse, letting an untested main inflate a
#   naive average) makes the gate lie. So we recompute the total ourselves over
#   an explicit allow-list of package-path prefixes, weighting each block by its
#   statement count (exactly how Go computes the real percentage).
#
# Included in the floor (the code we promote and stand behind):
#   <module>/internal/...      — all business logic
#   <module>/aisotropy/...     — the public embeddable builder package
#   <module>/cmd/aisotropy/    — the one exercised command (CLI turn runner)
#
# Excluded (intentionally not unit-tested; wiring/demo/bench only):
#   cmd/aisotropyd, cmd/bitrate-harness, cmd/embed-bench, cmd/skill-import,
#   cmd/x, tools/analyzers/**/cmd/**, arenas/**, bench/**
#   (arenas/* and examples/* that are SEPARATE modules never appear here at all —
#    the root `go test ./...` does not descend into them.)
#
# Usage:
#   .github/scripts/coverage-floor.sh [coverage_profile] [floor_percent]
# Defaults: coverage.out, 70
#
# Exit 0 if weighted coverage over the included set >= floor, else exit 1.
# Dependency-free: bash + awk + the Go toolchain (`go list -m`).

set -euo pipefail

PROFILE="${1:-coverage.out}"
FLOOR="${2:-70}"

if [[ ! -f "$PROFILE" ]]; then
  echo "coverage-floor: profile not found: $PROFILE" >&2
  exit 2
fi

# Resolve the module path (e.g. github.com/Luum-Home/aisotropy) so the
# allow-list prefixes are not hard-coded. Fall back to parsing go.mod.
MODULE="$(go list -m 2>/dev/null || true)"
if [[ -z "$MODULE" ]]; then
  MODULE="$(awk '/^module /{print $2; exit}' go.mod)"
fi
if [[ -z "$MODULE" ]]; then
  echo "coverage-floor: could not determine module path" >&2
  exit 2
fi

awk -v mod="$MODULE" -v floor="$FLOOR" '
  # Coverage profile line format:
  #   <file>:<startLine.col>,<endLine.col> <numStatements> <count>
  # The first line is a "mode:" header we skip.
  NR == 1 && /^mode:/ { next }
  {
    # file path is everything up to the first ":"
    colon = index($0, ":")
    if (colon == 0) next
    file = substr($0, 1, colon - 1)

    include = 0
    if (index(file, mod "/internal/")     == 1) include = 1
    else if (index(file, mod "/aisotropy/")   == 1) include = 1
    else if (index(file, mod "/cmd/aisotropy/") == 1) include = 1
    if (!include) next

    stmts = $(NF - 1)
    count = $NF
    total += stmts
    if (count + 0 > 0) covered += stmts
  }
  END {
    if (total == 0) {
      print "coverage-floor: no statements matched the included package set" > "/dev/stderr"
      exit 2
    }
    pct = covered / total * 100
    printf "Promoted-set coverage: %.2f%% (%d/%d statements)\n", pct, covered, total
    printf "Floor:                 %.2f%%\n", floor + 0
    if (pct + 0 < floor + 0) {
      printf "FAIL: coverage %.2f%% is below the floor of %.2f%%\n", pct, floor + 0
      exit 1
    }
    printf "PASS: coverage %.2f%% meets the floor of %.2f%%\n", pct, floor + 0
    exit 0
  }
' "$PROFILE"
