#!/usr/bin/env bash
# worktree-audit.sh — answer, executably: "is any worktree, branch or stash
# holding content that main does NOT have?"
#
# Why it exists: agent sessions create linked worktrees under
# .claude/worktrees/ and branches named worktree-agent-*. They accumulate,
# they hold uncommitted files, and some carry multi-MB build artifacts. The
# question before deleting any of them is always the same, and answering it by
# hand is slow and easy to get wrong — a spot-check that reads three files and
# concludes "already in main" is how work gets lost.
#
# The audit is CONTENT-based, not patch-based, because `git cherry` compares
# patch-ids: work that landed on main through a different parent (rebase,
# re-commit, cherry-pick) shows as "not applied" while being byte-identical.
# What matters is whether main has the content, not whether it has the commit.
#
# Read-only: this script never deletes, stashes, or checks anything out.
#
# Usage:
#   scripts/worktree-audit.sh              # audit against main
#   scripts/worktree-audit.sh <ref>        # audit against another ref
#
# Exit codes (repo guard convention):
#   0  clean    — nothing outside <ref> holds unique content; safe to prune
#   1  findings — something holds content <ref> lacks; INSPECT before deleting
#   2  error    — could not evaluate (never treated as clean)

set -u

BASE="${1:-main}"
findings=0

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "worktree-audit: not inside a git repository" >&2
  exit 2
}
git rev-parse --verify "$BASE" >/dev/null 2>&1 || {
  echo "worktree-audit: base ref '$BASE' does not exist" >&2
  exit 2
}

echo "═══ worktree-audit — base: $BASE ($(git rev-parse --short "$BASE"))"
echo

# ── 1. Branches ────────────────────────────────────────────────────────────
# For each branch other than BASE: does it hold FILES that BASE lacks? A file
# whose content merely differs is not a finding by itself — BASE may simply
# have evolved past an old branch — but a file BASE has never seen is.
echo "── branches"
base_files="$(mktemp)"; git ls-tree -r "$BASE" --name-only | sort > "$base_files"
for br in $(git for-each-ref --format='%(refname:short)' refs/heads/ | grep -v "^${BASE}$"); do
  br_files="$(mktemp)"
  git ls-tree -r "$br" --name-only 2>/dev/null | sort > "$br_files"
  only="$(comm -23 "$br_files" "$base_files")"
  ahead="$(git rev-list --count "$BASE".."$br" 2>/dev/null || echo '?')"
  if [ -n "$only" ]; then
    echo "  ⚠  $br ($ahead commits ahead) — files absent from $BASE:"
    printf '       %s\n' $only
    findings=$((findings + 1))
  else
    echo "  ✓  $br ($ahead commits ahead) — every file exists in $BASE"
  fi
  rm -f "$br_files"
done
rm -f "$base_files"
echo

# ── 2. Linked worktrees: uncommitted content ───────────────────────────────
# The real risk. For every modified/untracked file in a linked worktree, count
# lines present there but absent from BASE's version. Zero unique lines means
# BASE is a superset and the worktree copy is stale.
echo "── linked worktrees (uncommitted content)"
main_root="$(git rev-parse --show-toplevel)"
git worktree list --porcelain | awk '/^worktree /{print $2}' | while read -r wt; do
  [ "$wt" = "$main_root" ] && continue
  dirty="$(git -C "$wt" status --porcelain 2>/dev/null | awk '{print $NF}')"
  if [ -z "$dirty" ]; then
    echo "  ✓  ${wt#"$main_root"/} — clean"
    continue
  fi
  wt_unique=0; wt_missing=0
  for f in $dirty; do
    if ! git cat-file -e "$BASE:$f" 2>/dev/null; then
      echo "  ⚠  ${wt#"$main_root"/}: $f — path does not exist in $BASE"
      wt_missing=$((wt_missing + 1)); continue
    fi
    n=$(diff <(git show "$BASE:$f" 2>/dev/null) "$wt/$f" 2>/dev/null | grep -c '^>' || true)
    if [ "${n:-0}" -gt 0 ]; then
      printf "  ⚠  %s: %s — %s line(s) not in %s\n" "${wt#"$main_root"/}" "$f" "$n" "$BASE"
      wt_unique=$((wt_unique + n))
    fi
  done
  if [ "$wt_unique" -eq 0 ] && [ "$wt_missing" -eq 0 ]; then
    echo "  ✓  ${wt#"$main_root"/} — dirty, but $BASE is a superset of every change"
  else
    echo "  ⚠  ${wt#"$main_root"/} — INSPECT: $wt_unique unique line(s), $wt_missing absent path(s)"
    echo "$wt" >> "${TMPDIR:-/tmp}/worktree-audit-findings.$$"
  fi
done
[ -f "${TMPDIR:-/tmp}/worktree-audit-findings.$$" ] && {
  findings=$((findings + $(wc -l < "${TMPDIR:-/tmp}/worktree-audit-findings.$$")))
  rm -f "${TMPDIR:-/tmp}/worktree-audit-findings.$$"
}
echo

# ── 3. Stashes ─────────────────────────────────────────────────────────────
# A stash is by definition content that is committed nowhere. Never auto-judged
# clean: it is always reported for a human to read.
echo "── stashes"
if [ -z "$(git stash list)" ]; then
  echo "  ✓  none"
else
  git stash list | sed 's/^/  ⚠  /'
  echo "  (a stash holds content committed nowhere — review before pruning)"
  findings=$((findings + $(git stash list | wc -l)))
fi
echo

# ── Verdict ────────────────────────────────────────────────────────────────
if [ "$findings" -eq 0 ]; then
  echo "═══ 🟢 CLEAN — nothing outside $BASE holds unique content."
  echo "    Worktrees/branches above can be pruned without losing work."
  exit 0
fi
echo "═══ 🔴 $findings finding(s) — do NOT prune blindly."
echo "    A file absent from $BASE, or unique lines in a dirty worktree, may be"
echo "    real work. Inspect each before deleting. Note the common false"
echo "    positive: a stale branch whose 'unique' lines are simply an OLDER"
echo "    version of a file $BASE has since rewritten — read the lines, don't"
echo "    count them."
exit 1
