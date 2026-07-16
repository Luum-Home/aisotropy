#!/usr/bin/env bash
# Executable governance: scan THIS arena's real dependency tree and assert
# aisotropy's constitutional license gate — "no strong-copyleft (GPL/AGPL/LGPL)
# and no source-available (BUSL/SSPL/ELv2/Commons-Clause/FSL)".
#
# Reports the REAL pip-licenses output (no guessing) and fails (exit 1) if any
# hard-blocker license appears in the langchain/langgraph dependency tree.
# Weak-copyleft MPL-2.0 is reported as a CAUTION per aisotropy's own
# license-policy.md (⚠️ CAUTION, not a BLOCKER) — surfaced, not fatal.
#
# OFFLINE. Reads only the installed venv metadata.
set -euo pipefail

cd "$(dirname "$0")/.."
PLIC=".venv/bin/pip-licenses"
PY=".venv/bin/python"

if [[ ! -x "$PLIC" ]]; then
  echo "ERROR: pip-licenses not installed in venv. Run: uv pip install pip-licenses" >&2
  exit 2
fi

echo "======================================================================"
echo "LangGraph arena — dependency LICENSE posture (constitutional gate)"
echo "======================================================================"
echo
echo "--- Full license table (real pip-licenses output, ordered by license) ---"
"$PLIC" --python "$PY" --format=plain --order=license

echo
echo "--- Summary (count by license) ---"
"$PLIC" --python "$PY" --summary

TOTAL=$("$PLIC" --python "$PY" --format=plain | tail -n +2 | grep -c .)
echo
echo "Total packages scanned: $TOTAL"

# Hard blockers per constitutional-gates.md + license-policy.md.
BLOCKERS='GPL|AGPL|LGPL|BUSL|Business Source|SSPL|Server Side Public|Elastic License|ELv2|Commons.?Clause|Functional Source|FSL'
echo
echo "--- Hard-blocker check (GPL/AGPL/LGPL/BUSL/SSPL/ELv2/Commons-Clause/FSL) ---"
HITS=$("$PLIC" --python "$PY" --format=plain --order=license | tail -n +2 | grep -iE "$BLOCKERS" || true)
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
MPL=$("$PLIC" --python "$PY" --format=plain --order=license | tail -n +2 | grep -iE 'MPL|Mozilla Public' || true)
if [[ -n "$MPL" ]]; then
  echo "[CAUTION] MPL-2.0 (weak/file-level copyleft) present — allowed with caution per"
  echo "          aisotropy license-policy.md (⚠️ CAUTION, not a BLOCKER):"
  echo "$MPL"
else
  echo "None."
fi

echo
echo "Result: PASS — no hard-blocker licenses; MPL-2.0 flagged as caution only."
exit 0
