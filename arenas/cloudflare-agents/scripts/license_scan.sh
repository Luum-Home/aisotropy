#!/usr/bin/env bash
# Executable governance: scan the REAL installed dependency tree of this arena
# with pnpm's own license reader and classify permissive / weak-copyleft /
# hard-blocker. aisotropy's constitutional gate: "100% MIT/Apache, no
# copyleft/BUSL without an ADR". This reports what the `agents` SDK tree actually
# holds — not a guess. Exits non-zero if any HARD-BLOCKER license ever enters the
# tree (GPL/AGPL/SSPL/BUSL/ELv2/Commons-Clause/FSL), so it is CI-runnable.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== FULL TREE (prod runtime + dev tooling) =="
pnpm licenses list --json 2>/dev/null | node scripts/classify-licenses.mjs

echo ""
echo "== PROD-ONLY TREE (what actually ships to the edge: agents + transitive) =="
pnpm licenses list --prod --json 2>/dev/null | node scripts/classify-licenses.mjs
