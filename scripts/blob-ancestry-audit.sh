#!/usr/bin/env bash
# blob-ancestry-audit.sh — for every file in <ref>, prove whether its EXACT
# content ever existed in <base>'s history.
#
# The question this answers is the only one that matters before deleting a
# branch, a worktree or a stash:
#
#     "Is this content accounted for, or is it about to be lost?"
#
# Line counting cannot answer it. A stale branch and a branch with real unique
# work both show 'lines base does not have' — the first because base rewrote
# the file since, the second because the work never landed. The distinction is
# not in the count, it is in whether base's HISTORY ever contained this exact
# blob.
#
# Method (exact, not heuristic): for each path, take the blob SHA in <ref> and
# walk every commit in <base> that touched that path, comparing blob SHAs.
#   HEAD      — identical to base's current version. Fully accounted for.
#   ANCESTOR  — not current, but base's history contains this exact blob:
#               the file is an OLD snapshot base has since evolved past.
#               Fully accounted for.
#   UNIQUE    — this blob never existed in base's history for this path.
#               Either genuinely unmerged work, or a variant nobody committed.
#               MUST be read by a human.
#   ABSENT    — the path does not exist in base at all.
#
# Read-only. Never deletes, checks out, or modifies anything.
#
# Usage:
#   scripts/blob-ancestry-audit.sh <ref> [base]        # e.g. a deleted branch sha
#   scripts/blob-ancestry-audit.sh stash@{0} main
#   scripts/blob-ancestry-audit.sh <ref> main --verbose   # list HEAD/ANCESTOR too
#
# Exit codes (repo guard convention):
#   0  every file is HEAD or ANCESTOR — nothing unaccounted for
#   1  at least one UNIQUE or ABSENT path — inspect before deleting
#   2  error / could not evaluate (never treated as clean)

set -u

REF="${1:-}"
BASE="${2:-main}"
VERBOSE=false
[ "${3:-}" = "--verbose" ] && VERBOSE=true

[ -z "$REF" ] && { echo "usage: $0 <ref> [base] [--verbose]" >&2; exit 2; }
cd "$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "not a git repo" >&2; exit 2; }
git rev-parse --verify --quiet "$REF" >/dev/null || { echo "ref '$REF' not found" >&2; exit 2; }
git rev-parse --verify --quiet "$BASE" >/dev/null || { echo "base '$BASE' not found" >&2; exit 2; }

echo "═══ blob-ancestry-audit — ref: $REF   base: $BASE"
echo

n_head=0; n_anc=0; n_uniq=0; n_abs=0
uniq_list=""; abs_list=""

# Only examine paths that actually differ from base — identical paths are
# HEAD by definition and walking history for them is wasted work.
paths="$(git diff --name-only "$BASE" "$REF" 2>/dev/null)"
total="$(printf '%s\n' "$paths" | grep -c . || true)"
echo "paths differing from $BASE: $total"
echo

for p in $paths; do
  ref_blob="$(git rev-parse "$REF:$p" 2>/dev/null)" || { echo "  (skip, unreadable in ref) $p"; continue; }

  if ! git cat-file -e "$BASE:$p" 2>/dev/null; then
    n_abs=$((n_abs + 1)); abs_list="$abs_list$p\n"
    echo "  ABSENT   $p"
    continue
  fi

  base_blob="$(git rev-parse "$BASE:$p" 2>/dev/null)"
  if [ "$ref_blob" = "$base_blob" ]; then
    n_head=$((n_head + 1))
    $VERBOSE && echo "  HEAD     $p"
    continue
  fi

  # Walk base's history for this path, comparing blob SHAs.
  found=""
  for c in $(git rev-list "$BASE" -- "$p" 2>/dev/null); do
    if [ "$(git rev-parse "$c:$p" 2>/dev/null)" = "$ref_blob" ]; then found="$c"; break; fi
  done

  if [ -n "$found" ]; then
    n_anc=$((n_anc + 1))
    $VERBOSE && echo "  ANCESTOR $p  (exact blob at $(git rev-parse --short "$found"))"
  else
    n_uniq=$((n_uniq + 1)); uniq_list="$uniq_list$p\n"
    echo "  UNIQUE   $p  ← blob never in $BASE's history for this path"
  fi
done

echo
echo "── summary"
printf "  HEAD (identical to %s):        %s\n" "$BASE" "$n_head"
printf "  ANCESTOR (old state of %s):    %s\n" "$BASE" "$n_anc"
printf "  UNIQUE (never in history):     %s\n" "$n_uniq"
printf "  ABSENT (path not in %s):       %s\n" "$BASE" "$n_abs"
echo

if [ "$n_uniq" -eq 0 ] && [ "$n_abs" -eq 0 ]; then
  echo "═══ 🟢 ACCOUNTED FOR — every differing file is either $BASE's current"
  echo "    version or an exact earlier state of it. Nothing is lost by deleting $REF."
  exit 0
fi
echo "═══ 🔴 $((n_uniq + n_abs)) path(s) NOT accounted for. Read each before deleting:"
[ -n "$uniq_list" ] && printf "$uniq_list" | sed 's/^/    UNIQUE  /'
[ -n "$abs_list" ]  && printf "$abs_list"  | sed 's/^/    ABSENT  /'
echo
echo "    A UNIQUE blob is not automatically real work — it can be a variant"
echo "    that was never committed anywhere (formatting, a half-edit). But it is"
echo "    the only set a human must actually read."
exit 1
