#!/usr/bin/env bash
# license-scan.sh — dependency license posture for the ADK Go arena.
#
# aisotropy's constitutional gate: "100% MIT/Apache, no copyleft/BUSL". This
# scans THIS arena's real dependency tree with google/go-licenses and prints the
# permissive vs copyleft vs restricted mix. Reports the REAL tool output — it
# does not guess licenses.
#
#   scripts/license-scan.sh
#
# Requires: go-licenses (go install github.com/google/go-licenses@latest).
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"

if ! command -v go-licenses >/dev/null 2>&1; then
	echo "installing go-licenses..." >&2
	go install github.com/google/go-licenses@latest
fi

csv="$(mktemp)"
# go-licenses emits per-package license type; arena-own packages (adk-go-arena/*)
# have no LICENSE file and show Unknown — they are our code, not deps.
go-licenses report ./... 2>/dev/null | sort -u >"$csv"

third_party="$(grep -v '^adk-go-arena/' "$csv" || true)"
total=$(printf '%s\n' "$third_party" | grep -c ',' || true)

perm=$(printf '%s\n' "$third_party" | grep -cE ',(MIT|BSD-2-Clause|BSD-3-Clause|Apache-2.0|ISC)$' || true)
copyleft=$(printf '%s\n' "$third_party" | grep -cE ',(GPL|LGPL|AGPL|MPL)' || true)
restricted=$(printf '%s\n' "$third_party" | grep -ciE ',(BUSL|SSPL|Elastic|Commons)' || true)

echo "=============================================================="
echo "ADK Go arena — dependency license posture (go-licenses report)"
echo "=============================================================="
echo "third-party modules classified: $total"
echo
echo "license histogram:"
printf '%s\n' "$third_party" | cut -d, -f3 | sort | uniq -c | sort -rn
echo
echo "permissive (MIT/BSD/Apache/ISC): $perm"
echo "copyleft   (GPL/LGPL/AGPL/MPL):  $copyleft"
echo "restricted (BUSL/SSPL/ELv2/CC):  $restricted"
echo
echo "non-permissive packages:"
if printf '%s\n' "$third_party" | grep -vE ',(MIT|BSD-2-Clause|BSD-3-Clause|Apache-2.0|ISC)$' | grep -q ','; then
	printf '%s\n' "$third_party" | grep -vE ',(MIT|BSD-2-Clause|BSD-3-Clause|Apache-2.0|ISC)$'
	echo
	echo "Result: GATE VIOLATION — non-permissive dependency present."
	exit 1
fi
echo "  (none — all permissive)"
echo
echo "Result: PASS — dep tree holds the 100% MIT/Apache/BSD line aisotropy claims."
