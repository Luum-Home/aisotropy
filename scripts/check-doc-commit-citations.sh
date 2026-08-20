#!/usr/bin/env bash
# check-doc-commit-citations.sh — do the commit hashes cited in the docs still resolve?
#
# The docs cite commits by short hash ("landed in `abc1234`", "board @ `abc1234`").
# A history rewrite invalidates every one of them at once, silently: the prose keeps
# reading like evidence long after the evidence stopped existing. This is the gate.
#
#   exit 0  no findings
#   exit 1  findings (a cited hash resolves to nothing and is not declared)
#   exit 2  error / cannot evaluate
#
# Read-only. No network. No mutation of repo or index state.
#
# ─────────────────────────────────────────────────────────────────────────────
# THE TRAP THIS SCRIPT EXISTS TO AVOID, AND STILL CANNOT FULLY ESCAPE
#
# `git cat-file -e <sha>^{commit}` answers "is this object present", NOT "is this
# commit reachable". After a rewrite the old objects linger unreferenced until gc
# prunes them, so a WORKING repo answers YES for hashes that are already dead for
# anybody who clones. Measured here on 2026-08-03: 20 of 51 citations looked valid
# in the working repo, 3 were actually valid in a clean clone of the same history.
#
# So this script uses REACHABILITY (`git merge-base --is-ancestor`), not presence.
# That is correct in a working repo and in a clone alike. Run with --strict-clone
# to additionally refuse to run anywhere that still holds unreachable objects from
# a recent rewrite — use it in CI, where the checkout is fresh anyway.
# SECOND TRAP, same family: `git grep` searches TRACKED files only. A citation in
# a doc you just wrote and have not committed is invisible to it, so the gate goes
# green at exactly the moment the mistake is being made. Every grep below passes
# --untracked. (This bit twice in one day: an acceptance criterion in ADR-0080 read
# 0 for the same reason, because the ADR asserting it was itself still untracked.)
# ─────────────────────────────────────────────────────────────────────────────

set -uo pipefail

MANIFEST="manifests/retired-commit-citations.yaml"
PATHS=(docs README.md CONTRIBUTING.md AGENTS.md)
MODE="check"
STRICT_CLONE=0

for arg in "$@"; do
  case "$arg" in
    --report)       MODE="report" ;;
    --strict-clone) STRICT_CLONE=1 ;;
    -h|--help)
      sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

command -v git >/dev/null 2>&1 || { echo "git not found" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repository" >&2; exit 2; }
[ -f "$MANIFEST" ] || { echo "missing $MANIFEST (run from the repo root)" >&2; exit 2; }

# Declared identifiers: `retired:` (commits that are gone) and `external:` (commits
# in other repositories — correct citations that cannot resolve here by design).
declared=$(grep -oE '^  - id: [0-9a-f]{7,40}' "$MANIFEST" | awk '{print $3}' | sort -u)
[ -n "$declared" ] || { echo "no declared ids parsed from $MANIFEST — format changed?" >&2; exit 2; }

if [ "$STRICT_CLONE" -eq 1 ]; then
  # Unreachable-but-present objects mean a rewrite happened here and gc has not run.
  # Findings would still be correct (we test reachability) but the repo is not what
  # a contributor would clone, so refuse rather than report a maybe.
  if [ -n "$(git fsck --unreachable --no-progress --connectivity-only 2>/dev/null | grep -m1 'unreachable commit')" ]; then
    echo "BLOCKED: repo holds unreachable commits from a rewrite; run 'git gc --prune=now' or use a fresh clone" >&2
    exit 2
  fi
fi

head_sha=$(git rev-parse HEAD) || exit 2

# The scope must be real before the absence of hits can mean anything. `git
# grep` answers rc=1 (no match) for a pathspec that does not exist — it does
# NOT error — so a renamed doc directory silently shrinks the search and the
# gate still exits 0. A check that examines zero files always passes, and that
# is not a clean result: it is an unasked question.
missing_paths=""
for _p in "${PATHS[@]}"; do
  [ -e "$_p" ] || missing_paths="${missing_paths}${_p} "
done
if [ -n "$missing_paths" ]; then
  echo "BLOCKED: declared scope does not exist: ${missing_paths}— the citation check would" >&2
  echo "         have silently examined less than it claims. Fix PATHS in $0." >&2
  exit 2
fi

cited=$(git grep --untracked -hIoE -e '`[0-9a-f]{7,12}`' -- "${PATHS[@]}" 2>/dev/null | tr -d '`' | sort -u)
[ -n "$cited" ] || { echo "no commit-shaped citations found in ${PATHS[*]} (scope verified present)"; exit 0; }

total=0; live=0; findings=0; declared_hits=0
finding_list=""

for sha in $cited; do
  total=$((total + 1))
  # Reachable from HEAD? (presence alone is not enough — see the header)
  if git rev-parse -q --verify "${sha}^{commit}" >/dev/null 2>&1 \
     && git merge-base --is-ancestor "$sha" "$head_sha" 2>/dev/null; then
    live=$((live + 1))
    if printf '%s\n' "$declared" | grep -qx -- "$sha"; then
      echo "STALE-DECLARATION  $sha is declared in $MANIFEST but resolves again — remove the entry"
      findings=$((findings + 1))
      finding_list="${finding_list}${sha} "
    fi
    continue
  fi
  if printf '%s\n' "$declared" | grep -qx -- "$sha"; then
    declared_hits=$((declared_hits + 1))
    [ "$MODE" = "report" ] && printf '  declared  %s  (%s occurrences)\n' \
      "$sha" "$(git grep --untracked -hoI "\`$sha\`" -- "${PATHS[@]}" 2>/dev/null | wc -l | tr -d ' ')"
    continue
  fi
  findings=$((findings + 1))
  finding_list="${finding_list}${sha} "
  where=$(git grep --untracked -nI "\`$sha\`" -- "${PATHS[@]}" 2>/dev/null | head -1 | cut -c1-120)
  echo "DEAD-CITATION      $sha  ->  ${where:-<not located>}"
done

echo
echo "  citations found:  $total"
echo "  resolve from HEAD:$live"
echo "  declared dead:    $declared_hits  ($MANIFEST)"
echo "  findings:         $findings"

if [ "$findings" -eq 0 ]; then
  echo "VERDICT: clean — every cited hash either resolves or is declared."
  exit 0
fi

cat <<EOF

VERDICT: $findings finding(s): $finding_list

A hash that neither resolves nor is declared is a claim pointing at nothing.
Fix it, in this order:
  1. If a rewrite just ran, repoint from its map: .git/filter-repo/commit-map
     (old<space>new, full hashes) — that is a repair, not a declaration.
  2. If the commit is genuinely gone, add it under 'retired:' in $MANIFEST,
     WITH the context line, so the provenance of the claim survives the hash.
  3. If it names a commit in another repository, add it under 'external:'
     with the command that resolves it there.
Do NOT declare a hash you have just broken yourself.
EOF
exit 1
