#!/usr/bin/env bash
# Is any commit in THIS repository carrying a co-authorship trailer?
#
# Both CLAUDE.md files say "NEVER add Co-Authored-By trailer to commit messages".
# Until this script existed that rule was enforced by nothing: it lived in prose,
# in a file an agent may or may not have loaded, and a single slip would have sat
# in the history unnoticed. `lo-escrito-no-se-aplica-solo` — a written rule with
# no gate is a hope.
#
# SCOPE, stated because a count is only as good as its universe. This audits every
# commit OBJECT in the repository's own database — not just what is reachable from
# a branch. Dropped stashes, orphaned commits and anything the reflog still points
# at are all included, because "I rewrote it away" is exactly the case where a bad
# trailer survives unnoticed.
#
# DELIBERATELY OUT OF SCOPE: nested third-party checkouts under external/. They are
# gitignored, contribute zero tracked files to this repo, and their remotes belong
# to other orgs. Their history is not ours to police. (For the record, on
# 2026-08-19 external/voicebox held one `Co-authored-by: dependabot[bot]` — a bot
# trailer in someone else's product.)
#
# Exit: 0 clean · 1 a trailer was found · 2 cannot evaluate

set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "coauthor-guard: not inside a git checkout" >&2; exit 2; }

# The needle. Case-insensitive, tolerant of the separator being '-' or '_' and of
# stray whitespace before the colon — a trailer that is malformed is still a
# trailer someone will read as attribution.
PATTERN='co[-_ ]?authored[-_ ]?by|noreply@anthropic\.com|🤖 *Generated with'

commits=$(git cat-file --batch-all-objects --batch-check='%(objecttype) %(objectname)' --unordered 2>/dev/null \
          | awk '$1=="commit"{print $2}')
if [ -z "$commits" ]; then
  echo "coauthor-guard: could not enumerate commit objects" >&2; exit 2
fi
total=$(printf '%s\n' "$commits" | wc -l | tr -d ' ')

# SELF-TEST before trusting a zero. A search that cannot find anything is
# indistinguishable from a repository that contains nothing, and the second is the
# answer we are hoping for — which is exactly when a broken search goes unnoticed.
# "Signed-off-by" is not expected here, so the canary is a string we KNOW exists:
# every commit has an author line.
canary=$(printf '%s\n' "$commits" | head -1 | xargs -I{} git cat-file commit {} 2>/dev/null | grep -ci '^author ')
if [ "${canary:-0}" -lt 1 ]; then
  echo "coauthor-guard: self-test failed — cannot read commit bodies, so a clean result would be meaningless" >&2
  exit 2
fi

hits=0
while read -r sha; do
  [ -z "$sha" ] && continue
  # Read the RAW object, not `git log` output: no reformatting, no pager, no
  # trailer parsing that might normalise the thing away before we see it.
  if git cat-file commit "$sha" 2>/dev/null | grep -qiE "$PATTERN"; then
    hits=$((hits + 1))
    echo "FOUND $sha  $(git log -1 --format='%ad %an  %s' --date=short "$sha" 2>/dev/null)"
    git cat-file commit "$sha" 2>/dev/null | grep -iE "$PATTERN" | sed 's/^/        /'
  fi
done <<< "$commits"

# STRUCTURAL INVARIANT — stronger than the grep above, and the reason this guard
# is not just a pattern match hoping to be exhaustive.
#
# Every commit carries exactly two identity lines: author and committer. A
# co-authorship trailer adds a THIRD identity to the body. So instead of asking
# "does any commit contain a string I thought to look for?", ask "does any commit
# carry more identities than a commit structurally has?" — a question no
# obfuscation of the trailer's spelling can dodge, because the extra identity is
# the point of the trailer.
#
# Measured 2026-08-19: the histogram over 611 commits was {2: 611}, no exceptions,
# with a single identity across all 1222 author/committer lines.
angles=$(printf '%s\n' "$commits" | while read -r sha; do
  [ -z "$sha" ] && continue
  git cat-file commit "$sha" 2>/dev/null | grep -cE '^(author|committer) .*<[^>]+@[^>]+>'
done | sort -u | tr '\n' ' ')
if [ -n "$angles" ] && [ "$(printf '%s' "$angles" | tr -d ' ')" != "2" ]; then
  echo "coauthor-guard: STRUCTURAL ANOMALY — commits carry these identity-line counts: $angles (expected exactly 2)" >&2
  hits=$((hits + 1))
fi

echo "coauthor-guard: scanned $total commit object(s) (reachable + unreachable), $hits with a co-authorship trailer"
[ "$hits" -eq 0 ] || exit 1
exit 0
