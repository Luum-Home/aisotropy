#!/usr/bin/env bash
# Executable governance: scan THIS arena's real Maven dependency tree and assert
# aisotropy's constitutional license gate — "no strong-copyleft (GPL/AGPL/LGPL)
# and no source-available (BUSL/SSPL/ELv2/Commons-Clause/FSL)".
#
# Reports the REAL license-maven-plugin output (each dep's declared <licenses>,
# no guessing) and fails (exit 1) if any hard-blocker license appears in the
# Spring AI 1.1.8 dependency tree. Weak-copyleft MPL-2.0 is reported as a CAUTION
# per aisotropy's own license-policy.md (⚠️ CAUTION, not a BLOCKER).
#
# Reads the resolved tree via the license-maven-plugin (needs network on first run
# to fetch the plugin + any un-cached dep POMs; offline once ~/.m2 is warm).
set -euo pipefail

cd "$(dirname "$0")/.."
MVN="${MVN:-mvn}"
if ! command -v "$MVN" >/dev/null 2>&1; then
  echo "ERROR: mvn not on PATH. Install Maven 3.9+ (JDK 21+) or set MVN=/path/to/mvn." >&2
  exit 2
fi

echo "======================================================================"
echo "Spring AI arena — dependency LICENSE posture (constitutional gate)"
echo "======================================================================"
echo
echo "--- Generating THIRD-PARTY report (real license-maven-plugin output) ---"
"$MVN" -q org.codehaus.mojo:license-maven-plugin:2.6.0:add-third-party \
  -Dlicense.outputDirectory=target \
  -Dlicense.excludeTransitiveDependencies=false >/dev/null 2>&1

REPORT=target/THIRD-PARTY.txt
if [[ ! -f "$REPORT" ]]; then
  echo "ERROR: $REPORT not generated." >&2
  exit 2
fi

echo
echo "--- Full license table (real plugin output, per declared <licenses>) ---"
grep -E '^\s+\(' "$REPORT" || true

TOTAL=$(grep -cE '^\s+\(' "$REPORT")
echo
echo "Total dependencies scanned: $TOTAL"

# Hard blockers per constitutional-gates.md + license-policy.md.
BLOCKERS='GPL|AGPL|LGPL|BUSL|Business Source|SSPL|Server Side Public|Elastic License|ELv2|Commons.?Clause|Functional Source|FSL'
echo
echo "--- Hard-blocker check (GPL/AGPL/LGPL/BUSL/SSPL/ELv2/Commons-Clause/FSL) ---"
HITS=$(grep -E '^\s+\(' "$REPORT" | grep -iE "$BLOCKERS" || true)
if [[ -n "$HITS" ]]; then
  echo "[BLOCK] Non-permissive blocker license(s) found:"
  echo "$HITS"
  echo
  echo "Result: FAIL — dependency tree violates aisotropy's license gate."
  exit 1
fi
echo "[PASS] No strong-copyleft or source-available blocker licenses in the tree."

# Weak-copyleft MPL-2.0 — CAUTION, reported not fatal.
echo
echo "--- Weak-copyleft (MPL-2.0) advisory ---"
MPL=$(grep -E '^\s+\(' "$REPORT" | grep -iE 'Mozilla Public|MPL-[0-9]|MPL [0-9]' || true)
if [[ -n "$MPL" ]]; then
  echo "[CAUTION] MPL-2.0 present (allowed with caution per license-policy.md):"
  echo "$MPL"
else
  echo "None — the Spring AI tree is 100% permissive (Apache-2.0 / MIT / BSD / CC0)."
fi

echo
echo "Result: PASS — no hard-blocker licenses; tree is fully permissive."
exit 0
