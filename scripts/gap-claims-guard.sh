#!/usr/bin/env bash
# gap-claims-guard.sh — the evidence gate for docs/architecture/capabilities-and-gaps.md.
#
# WHY THIS EXISTS: on 2026-07-27 that document was used to dispatch build work and TWO
# dispatches were burned building things that already existed — the runtime config file
# (shipped 10 days earlier, bfeca15/ADR-0040) and the Kubernetes Helm chart (shipped as
# 3d39c44, also BEFORE the doc's own timestamp). A read-only re-verification of the 12
# dispatched claims found 5 wrong or materially overstated
# (docs/06-Daily/reports/gap-claims-verification-2026-07-27.md). The document reads as
# authoritative and rots silently: nothing re-derives it.
#
# This guard re-derives the VOLATILE, MECHANICALLY CHECKABLE subset of its claims FROM THE
# CODE and fails when the document disagrees with reality. It is the capabilities-doc twin
# of scripts/prune-guard.sh: a claim about the codebase must be backed by executable
# evidence, never by a doc read (.claude/rules/prune-evidence-gate.md).
#
# It does NOT parse the document's prose. Claims live in a machine-readable registry —
# manifests/capability-claims.yaml — where each entry is {id, claim, requires, check,
# expect}. The doc rows reference claim ids; the guard asserts every registered id is
# referenced (registry <-> doc linkage), so a claim cannot be added without the doc
# pointing at it, nor a doc row deleted while the claim is still registered.
#
# FAIL-CLOSED: sibling discipline of prune-guard.sh / k8s-chart-guard.sh —
# A GUARD THAT CANNOT EVALUATE MUST NEVER ABSOLVE. A missing required path, a missing
# YAML reader, a command that cannot run, or a check that prints nothing is reported as
# BLOCKED and exits 2. It is never silently counted as a pass. (prune-guard.sh once
# returned green for live code because it could not see inside a nested module; that
# false green is the failure this rule exists to prevent.)
#
# Read-only. Offline. No network, no docker, no cluster, no model call, no build. ~1s.
#
# USAGE:
#   scripts/gap-claims-guard.sh                      # verify, summary output
#   scripts/gap-claims-guard.sh --explain            # + per-claim command, got vs expected
#   scripts/gap-claims-guard.sh --manifest PATH      # verify against an alternate registry
#
# EXIT CODES (repo guard convention):
#   0  OK       — every claim re-derived and matches the registry
#   1  FINDINGS — at least one claim DRIFTED (the document is now wrong; fix the doc or
#                 the registry, and check whether work is being dispatched off a stale row)
#   2  ERROR    — cannot evaluate: usage error, no YAML reader, missing required path,
#                 unrunnable check. Dominates FINDINGS: a partially-evaluated run must not
#                 report a complete verdict.
set -uo pipefail

EXPLAIN=false
MANIFEST_ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --explain|-v) EXPLAIN=true; shift ;;
    --manifest) MANIFEST_ARG="${2:-}"; shift 2 || { echo "--manifest needs a path" >&2; exit 2; } ;;
    -h|--help) sed -n '28,40p' "$0"; exit 0 ;;
    *) echo "usage: scripts/gap-claims-guard.sh [--explain] [--manifest PATH]" >&2; exit 2 ;;
  esac
done

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || {
  echo "gap-claims-guard: not in a git repo" >&2; exit 2; }

MANIFEST="${MANIFEST_ARG:-${GAP_CLAIMS_MANIFEST:-manifests/capability-claims.yaml}}"
[ -f "$MANIFEST" ] || { echo "gap-claims-guard: registry not found: $MANIFEST" >&2; exit 2; }

DOC="docs/architecture/capabilities-and-gaps.md"

# --- 0. FAIL-CLOSED: do we have a YAML reader at all? -----------------------
# Without one, ZERO claims can be evaluated. That is not a pass.
US=$'\x1f'   # field separator
RS=$'\x1e'   # record separator

emit_records() {
  if command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
    python3 - "$MANIFEST" <<'PY'
import sys, yaml
US, RS = '\x1f', '\x1e'
try:
    doc = yaml.safe_load(open(sys.argv[1]))
except Exception as e:                       # malformed registry => cannot evaluate
    sys.stderr.write("registry parse error: %s\n" % e); sys.exit(2)
claims = (doc or {}).get('claims') or []
if not claims:
    sys.stderr.write("registry declares no claims\n"); sys.exit(2)
for c in claims:
    exp = c.get('expect') or {}
    row = [
        str(c.get('id', '')),
        ' '.join(c.get('requires') or []),
        str(c.get('check', '')),
        str(exp.get('op', '')),
        str(exp.get('value', '')),
        ' '.join(str(c.get('claim', '')).split()),
    ]
    sys.stdout.write(US.join(row) + RS)
PY
    return $?
  fi
  if command -v yq >/dev/null 2>&1 && command -v jq >/dev/null 2>&1; then
    yq -o=json '.' "$MANIFEST" 2>/dev/null | jq -j --arg us "$US" --arg rs "$RS" '
      (.claims // []) as $c
      | if ($c|length) == 0 then error("registry declares no claims") else empty end,
        ($c[] | [ (.id//""), ((.requires//[])|join(" ")), (.check//""),
                  (.expect.op//""), ((.expect.value // "")|tostring),
                  ((.claim//"")|gsub("\\s+";" ")) ] | join($us) + $rs)'
    return $?
  fi
  return 127
}

RECORDS="$(emit_records)"; reader_rc=$?
if [ "$reader_rc" -eq 127 ]; then
  echo "==================================================================="
  echo " gap-claims-guard: CANNOT EVALUATE"
  echo "==================================================================="
  echo " No YAML reader available. Need ONE of:"
  echo "   - python3 with PyYAML   (python3 -c 'import yaml')"
  echo "   - yq + jq"
  echo " ZERO claims were evaluated. This is NOT a pass."
  echo "==================================================================="
  exit 2
fi
if [ "$reader_rc" -ne 0 ] || [ -z "$RECORDS" ]; then
  echo "gap-claims-guard: CANNOT EVALUATE — registry $MANIFEST is unreadable or empty" >&2
  exit 2
fi

echo "==================================================================="
echo " gap-claims-guard: are the volatile claims in"
echo "   $DOC"
echo " still true of the code at HEAD?"
echo " registry: $MANIFEST"
echo "==================================================================="
echo

N_OK=0; N_FAIL=0; N_BLOCKED=0
declare -a FAILS=() BLOCKS=() IDS=()

compare() { # compare <op> <got> <expected> -> 0 match, 1 mismatch, 2 uncomparable
  local op="$1" got="$2" want="$3"
  case "$op" in
    eq|ne|ge|le)
      case "$got$want" in *[!0-9]*) return 2 ;; esac
      [ -n "$got" ] && [ -n "$want" ] || return 2
      case "$op" in
        eq) [ "$got" -eq "$want" ] ;;
        ne) [ "$got" -ne "$want" ] ;;
        ge) [ "$got" -ge "$want" ] ;;
        le) [ "$got" -le "$want" ] ;;
      esac ;;
    contains)      case "$got" in *"$want"*) true ;; *) false ;; esac ;;
    not_contains)  case "$got" in *"$want"*) false ;; *) true ;; esac ;;
    matches)       printf '%s' "$got" | grep -qE "$want" ;;
    *) return 2 ;;
  esac
}

while IFS="$US" read -r -d "$RS" id requires check op want claim; do
  [ -n "$id" ] || continue
  IDS+=("$id")

  # (a) required paths — a vanished path means the check is meaningless, not passing
  missing=""
  for p in $requires; do [ -e "$p" ] || missing="$missing $p"; done
  if [ -n "$missing" ]; then
    printf '  BLOCKED  %-36s required path(s) missing:%s\n' "$id" "$missing"
    N_BLOCKED=$((N_BLOCKED+1)); BLOCKS+=("$id: required path(s) missing:$missing")
    continue
  fi

  # (b) run the check
  out="$(bash -c "$check" 2>/dev/null)"; rc=$?
  got="$(printf '%s' "$out" | head -1 | tr -d '[:space:]')"
  if [ "$rc" -eq 127 ]; then
    printf '  BLOCKED  %-36s check command not found (exit 127)\n' "$id"
    N_BLOCKED=$((N_BLOCKED+1)); BLOCKS+=("$id: check command not found")
    continue
  fi
  if [ -z "$got" ]; then
    printf '  BLOCKED  %-36s check printed nothing (exit %s) — nothing to compare\n' "$id" "$rc"
    N_BLOCKED=$((N_BLOCKED+1)); BLOCKS+=("$id: check produced no value")
    continue
  fi

  # (c) compare
  compare "$op" "$got" "$want"; cmp_rc=$?
  if [ "$cmp_rc" -eq 2 ]; then
    printf '  BLOCKED  %-36s uncomparable: op=%s got=%s want=%s\n' "$id" "$op" "$got" "$want"
    N_BLOCKED=$((N_BLOCKED+1)); BLOCKS+=("$id: uncomparable (op=$op got=$got want=$want)")
  elif [ "$cmp_rc" -eq 0 ]; then
    printf '  OK       %-36s %s %s %s\n' "$id" "$got" "$op" "$want"
    N_OK=$((N_OK+1))
  else
    printf '  DRIFTED  %-36s got %s, registry says %s %s\n' "$id" "$got" "$op" "$want"
    N_FAIL=$((N_FAIL+1)); FAILS+=("$id: got $got, expected $op $want — $claim")
  fi
  if [ "$EXPLAIN" = true ]; then
    printf '             claim: %s\n' "$claim"
    printf '             check: %s\n' "$check"
  fi
done <<< "$RECORDS"

# --- registry <-> doc linkage ------------------------------------------------
# Every registered claim id must be referenced by the document. This is the only
# coupling between guard and doc, and it is exact-string, not prose parsing.
echo
if [ ! -f "$DOC" ]; then
  echo "  BLOCKED  registry<->doc linkage             $DOC not found"
  N_BLOCKED=$((N_BLOCKED+1)); BLOCKS+=("linkage: $DOC not found")
else
  unref=""
  for id in "${IDS[@]}"; do
    grep -qF "$id" "$DOC" || unref="$unref $id"
  done
  if [ -n "$unref" ]; then
    echo "  DRIFTED  registry<->doc linkage             ids not referenced by the doc:$unref"
    N_FAIL=$((N_FAIL+1)); FAILS+=("linkage: claim id(s) not referenced by $DOC:$unref")
  else
    printf '  OK       %-36s all %d claim ids referenced by the doc\n' "registry<->doc linkage" "${#IDS[@]}"
    N_OK=$((N_OK+1))
  fi
fi

# --- VERDICT ---------------------------------------------------------------
echo
echo "==================================================================="
echo " checks: $N_OK ok / $N_FAIL drifted / $N_BLOCKED blocked"
if [ "$N_BLOCKED" -gt 0 ]; then
  echo " VERDICT: 🚫 CANNOT EVALUATE — $N_BLOCKED check(s) could not run."
  printf '   - %s\n' "${BLOCKS[@]}"
  [ "$N_FAIL" -gt 0 ] && printf '   (also drifted) %s\n' "${FAILS[@]}"
  echo " A guard that cannot look must never absolve. Fix the check or the path,"
  echo " then re-run. Do NOT read this as a pass."
  echo "==================================================================="
  exit 2
fi
if [ "$N_FAIL" -gt 0 ]; then
  echo " VERDICT: 🔴 DRIFTED — the document no longer matches the code."
  printf '   - %s\n' "${FAILS[@]}"
  echo
  echo " This is the failure that burned two dispatches on 2026-07-27. Before"
  echo " dispatching ANY work off $DOC:"
  echo "   1. re-derive the row from the code (the check command above IS the derivation),"
  echo "   2. fix the doc row AND manifests/capability-claims.yaml together,"
  echo "   3. if the row was used to scope work already in flight, re-scope it."
  echo "==================================================================="
  exit 1
fi
echo " VERDICT: 🟢 every volatile claim re-derived from the code and matches."
echo " NOTE: this covers only the machine-checkable subset. The registry's"
echo " out_of_scope section lists what is deliberately NOT checked here"
echo " (positioning, semantic code properties, paid/slow measured numbers,"
echo " upstream runtime behavior, open decisions) — the guard's silence on"
echo " those rows is not a claim about them."
echo "==================================================================="
exit 0
