#!/usr/bin/env bash
# license_scan.sh — RUN-VERIFIED dependency LICENSE posture for the Genkit Go
# arena, checked against aisotropy's constitutional gate: "the stack is
# deliberately 100% MIT/Apache-2.0 ... no copyleft (GPL/AGPL) or
# source-available/BUSL license without an explicit ADR."
#
# Primary tool:  go-licenses report ./...   (github.com/google/go-licenses)
# Fallback:      go list -m all + heuristic classify (if go-licenses absent)
#
# It prints the REAL tool output, tallies the license mix, and NAMES any
# non-permissive package. Exit 0 = permissive line holds (no copyleft /
# restricted / source-available dep). Exit 1 = a non-permissive dep was found.
set -euo pipefail
cd "$(dirname "$0")/.."

export PATH="$PATH:$(go env GOPATH)/bin"

echo "======================================================================"
echo "Genkit Go arena — dependency LICENSE scan (executed governance gate)"
echo "======================================================================"

RAW="$(mktemp)"
trap 'rm -f "$RAW"' EXIT

if command -v go-licenses >/dev/null 2>&1; then
  echo "tool: go-licenses report ./..."
  echo "----------------------------------------------------------------------"
  # go-licenses prints CSV: <module>,<license-url>,<license-id>. The arena's OWN
  # packages (module path genkit-go-arena/...) have no LICENSE file and show as
  # "Unknown"; we drop them and classify only the EXTERNAL dependency tree.
  go-licenses report ./... 2>/dev/null | grep -v '^genkit-go-arena/' | sort -u > "$RAW"
  cat "$RAW"
else
  echo "tool: go-licenses NOT found — FALLBACK: go list -m all (module list only,"
  echo "      license id inferred is best-effort; install go-licenses for authority)"
  echo "----------------------------------------------------------------------"
  go list -m all | tail -n +2 | awk '{print $1",,UNVERIFIED"}' | sort -u > "$RAW"
  cat "$RAW"
fi

echo "----------------------------------------------------------------------"

# Classify by license id (3rd CSV field).
permissive=0; copyleft=0; restricted=0; other=0; total=0
nonperm_names=""
while IFS=',' read -r mod url lic; do
  [ -z "$mod" ] && continue
  total=$((total+1))
  case "$lic" in
    MIT|BSD-2-Clause|BSD-3-Clause|Apache-2.0|ISC|CC0-1.0|Unlicense|0BSD|BSD-3-Clause-Clear)
      permissive=$((permissive+1));;
    GPL-2.0|GPL-3.0|LGPL-2.1|LGPL-3.0|AGPL-3.0|MPL-2.0|EPL-1.0|EPL-2.0|GPL*|LGPL*|AGPL*|MPL*)
      copyleft=$((copyleft+1)); nonperm_names="$nonperm_names\n  [COPYLEFT] $mod -> $lic";;
    BUSL*|SSPL*|Elastic*|ELv2|Commons-Clause|FSL*|BSL*)
      restricted=$((restricted+1)); nonperm_names="$nonperm_names\n  [RESTRICTED] $mod -> $lic";;
    *)
      other=$((other+1)); nonperm_names="$nonperm_names\n  [UNCLASSIFIED] $mod -> $lic";;
  esac
done < "$RAW"

echo "SUMMARY (external dependency tree):"
echo "  total external deps : $total"
echo "  permissive          : $permissive   (MIT / BSD / Apache-2.0 / ISC)"
echo "  copyleft            : $copyleft   (GPL / LGPL / AGPL / MPL)"
echo "  restricted/src-avail: $restricted   (BUSL / SSPL / ELv2 / Commons-Clause / FSL)"
echo "  unclassified/other  : $other"
echo "----------------------------------------------------------------------"
echo "License id breakdown:"
cut -d',' -f3 "$RAW" | sort | uniq -c | sort -rn | sed 's/^/  /'
echo "----------------------------------------------------------------------"

if [ "$copyleft" -eq 0 ] && [ "$restricted" -eq 0 ] && [ "$other" -eq 0 ]; then
  echo "VERDICT: PASS — 100% permissive. The framework's real dep tree HOLDS the"
  echo "         MIT/Apache line aisotropy's constitutional gate requires."
  exit 0
else
  echo "VERDICT: NON-PERMISSIVE DEPENDENCIES FOUND:"
  printf '%b\n' "$nonperm_names"
  exit 1
fi
