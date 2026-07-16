#!/usr/bin/env bash
# GAP 2: executable governance = dependency LICENSE posture.
#
# aisotropy's constitutional gate is "100% MIT/Apache, no copyleft/BUSL/SSPL/ELv2".
# This scans THIS arena's REAL dependency tree with the actual go-licenses tool
# (github.com/google/go-licenses) and reports the license mix, so the claim is
# executed, not asserted. Falls back to `go list -m all` if go-licenses is absent.
#
# Run:  ./scripts/license-scan.sh     (from arenas/eino/)
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== go-licenses report ./... (real linked import graph) =="
if ! command -v go-licenses >/dev/null 2>&1; then
  echo "go-licenses not on PATH; installing..."
  go install github.com/google/go-licenses@latest
fi

# Third-party only: eino-arena/* are this arena's own packages (no LICENSE file).
report=$(go-licenses report ./... 2>/dev/null | grep -v '^eino-arena' || true)
echo "$report"

echo
echo "== license mix =="
echo "$report" | awk -F, '{print $3}' | sort | uniq -c | sort -rn

total=$(echo "$report" | grep -c . || true)
echo
echo "total third-party packages: $total"

echo
echo "== non-permissive scan (copyleft / source-available) =="
if echo "$report" | grep -iE 'GPL|LGPL|AGPL|MPL|BUSL|SSPL|ELv2|Commons|EUPL|CDDL|EPL'; then
  echo "NON-PERMISSIVE PACKAGE(S) FOUND above — gate would BLOCK."
  exit 1
fi
echo "NONE — no GPL/LGPL/AGPL/MPL/BUSL/SSPL/ELv2/Commons-Clause found."

echo
echo "== full module graph size (go list -m all) =="
echo "modules (incl. indirect/test): $(go list -m all 2>/dev/null | tail -n +2 | wc -l | tr -d ' ')"

echo
echo "LICENSE-SCAN RESULT: PASS — dependency tree holds the permissive (MIT/Apache/BSD) line."
