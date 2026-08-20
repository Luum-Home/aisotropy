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
# WHAT COUNTS AS A TRAILER IS GIT'S QUESTION, NOT OURS.
#
# Two false positives taught this. The first version matched the pattern anywhere
# and flagged the commit that INTRODUCED the guard, whose message discusses the
# rule. Anchoring to line start fixed that and immediately flagged the NEXT
# commit, whose message contains an indented worked example that is, read
# literally, a syntactically valid trailer.
#
# Both were me reinventing a rule git already implements: a trailer lives in the
# final block of the message, and `%(trailers)` returns exactly those. Verified —
# on the commit with the mid-message example it returns nothing; on the vendored
# dependabot commit it returns both Signed-off-by and Co-authored-by. So ask git.
#
# A guard that fires on any commit merely DISCUSSING the rule teaches everyone to
# pass SKIP_COAUTHOR_GUARD=1, and a check everyone skips is not running — the
# cheap green reached by false positive rather than by deliberate loosening.
KEY_PATTERN='^[[:space:]]*co[-_ ]?authored[-_ ]?by[[:space:]]*:|noreply@anthropic\.com'

# THE STRUCTURAL RULE — the second question, and the reason this guard is more
# than a pattern match hoping to be exhaustive.
#
# A commit carries exactly two identities in its HEADERS: author and committer. A
# co-authorship trailer adds a THIRD, down in the trailer block. So alongside
# "does a trailer use a spelling I thought of?", ask "does any trailer carry an
# identity at all?" — a question no re-spelling of the key can dodge, because the
# extra identity is the entire point of the trailer. `Assisted-by: X <y@z>` walks
# straight past KEY_PATTERN and not past this.
#
# NOTE THIS ASKS THE TRAILER BLOCK, NOT THE COMMIT HEADERS. The previous version
# of this rule counted `^(author|committer) .*<...@...>` lines in `git cat-file
# commit` output and called any count but 2 an anomaly — and that check could
# never fire. Those two headers are the only lines in a commit object beginning
# with `author `/`committer `, so the count is 2 for every commit that has ever
# existed; `Co-authored-by:` lives in the MESSAGE and is not counted. Control,
# on a real commit that does carry the trailer:
#
#     $ git -C external/voicebox cat-file commit c323fa72 \
#         | grep -cE '^(author|committer) .*<[^>]+@[^>]+>'
#     2
#
# Two, not three. Its alarm was wired to nothing, and the comment above it sold
# it as the STRONGER of the two defenses — a suppressor suppressing nothing,
# handing out a feeling of coverage where there was none.
#
# SCOPE, because this edge is real: the rule flags ANY identity-bearing trailer,
# so a DCO `Signed-off-by:` would trip it. Measured 2026-08-20 across 617 commit
# objects: zero identity-bearing trailers, the only keys in use being
# Verified/Verify/Ref/Docs/Remaining, none of which carry an address. There is
# deliberately no allowlist — adopting sign-off is a policy change that belongs
# written down here, not absorbed by a hole opened in advance for it.
IDENTITY_TRAILER='^[[:space:]]*[A-Za-z][A-Za-z0-9-]*[[:space:]]*:.*<[^>]+@[^>]+>'

commits=$(git cat-file --batch-all-objects --batch-check='%(objecttype) %(objectname)' --unordered 2>/dev/null \
          | awk '$1=="commit"{print $2}')
if [ -z "$commits" ]; then
  echo "coauthor-guard: could not enumerate commit objects" >&2; exit 2
fi
total=$(printf '%s\n' "$commits" | wc -l | tr -d ' ')

# ONE PASS, and both questions are put to the same mechanism: git's own parse of
# the trailer block. That is deliberate — the canary below then covers the exact
# expansion the verdict rests on. (The self-test this replaces grepped `^author `
# out of `git cat-file`, which after this change decides nothing at all.)
hits=0
with_trailers=0
while read -r sha; do
  [ -z "$sha" ] && continue
  trailers=$(git log -1 --format='%(trailers:only,valueonly=false)' "$sha" 2>/dev/null)

  # SELF-TEST, part 1 — a git too old to know this placeholder hands it back
  # verbatim instead of failing. Every commit would then look trailer-free and
  # this script would certify a history it never read.
  case "$trailers" in
    *'%(trailers'*)
      echo "coauthor-guard: self-test failed — this git did not expand %(trailers), so a clean result would be meaningless" >&2
      exit 2 ;;
  esac

  case "$trailers" in
    *[![:space:]]*) with_trailers=$((with_trailers + 1)) ;;
  esac

  key_hit=$(printf '%s\n' "$trailers" | grep -iE "$KEY_PATTERN")
  id_hit=$(printf '%s\n' "$trailers" | grep -E "$IDENTITY_TRAILER")
  if [ -n "$key_hit$id_hit" ]; then
    hits=$((hits + 1))
    echo "FOUND $sha  $(git log -1 --format='%ad %an  %s' --date=short "$sha" 2>/dev/null)"
    { [ -n "$key_hit" ] && printf '%s\n' "$key_hit"
      [ -n "$id_hit" ]  && printf '%s\n' "$id_hit"
      true; } | sort -u | sed 's/^/        /'
    if [ -z "$key_hit" ]; then
      echo "        ^ no known co-authorship spelling — caught by the identity-trailer rule alone"
    fi
  fi
done <<< "$commits"

# SELF-TEST, part 2 — the canary on the mechanism that decides the verdict.
#
# Everything above rests on %(trailers). Had that expansion come back empty for
# every commit, the loop would have found nothing and this script would print a
# confident zero. "This history carries no trailers" and "the parse I depend on
# returned nothing" arrive as the same silence, and only one of them means clean:
# absence has many causes, presence has one. So refuse to certify a history in
# which git reported no trailer of ANY kind — say "cannot evaluate" out loud
# rather than let absence of evidence be read as evidence of absence.
if [ "$with_trailers" -eq 0 ]; then
  echo "coauthor-guard: self-test failed — %(trailers) returned nothing for all $total commit object(s)" >&2
  echo "  Cannot tell a history with no trailers at all from a trailer parse that is not working." >&2
  exit 2
fi

echo "coauthor-guard: scanned $total commit object(s) (reachable + unreachable), $with_trailers carrying a trailer block, $hits with a co-authorship trailer"
[ "$hits" -eq 0 ] || exit 1
exit 0
