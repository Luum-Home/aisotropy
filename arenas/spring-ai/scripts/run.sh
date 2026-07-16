#!/usr/bin/env bash
# Build Orbit and run one of its demos against the REAL Spring AI API (no key).
#   ./scripts/run.sh smoke     -> orbit.SmokeMain            (tenant/user isolation)
#   ./scripts/run.sh product   -> orbit.ProductIsolationMain (4th scope axis)
#   ./scripts/run.sh features  -> orbit.FeatureDemoMain      (memory / structured output)
#   ./scripts/run.sh all       -> all three
#
# Needs network on first run (to fetch Spring AI jars into ~/.m2); offline after.
set -euo pipefail
cd "$(dirname "$0")/.."
MVN="${MVN:-mvn}"

command -v "$MVN" >/dev/null 2>&1 || { echo "ERROR: mvn not on PATH (need Maven 3.9+, JDK 21+)." >&2; exit 2; }

"$MVN" -q compile
"$MVN" -q dependency:build-classpath -Dmdep.outputFile=cp.txt >/dev/null
CP="target/classes:$(cat cp.txt)"

run() { echo "############ $1 ############"; java -cp "$CP" "$2" 2>/dev/null; echo "exit=$?"; echo; }

case "${1:-all}" in
  smoke)    run "SMOKE (tenant/user)"        orbit.SmokeMain ;;
  product)  run "PRODUCT ISOLATION (axis 4)" orbit.ProductIsolationMain ;;
  features) run "FEATURE DEMO"               orbit.FeatureDemoMain ;;
  all)
    run "SMOKE (tenant/user)"        orbit.SmokeMain
    run "PRODUCT ISOLATION (axis 4)" orbit.ProductIsolationMain
    run "FEATURE DEMO"               orbit.FeatureDemoMain ;;
  *) echo "usage: $0 {smoke|product|features|all}" >&2; exit 2 ;;
esac
