#!/usr/bin/env bash
# pre-publication-sweep.sh — what would this repository disclose if published?
#
#   exit 0  clean
#   exit 1  findings
#   exit 2  CANNOT EVALUATE — never treat as a pass
#
# Read-only. No network. No mutation of repo or index state.
#
# ─────────────────────────────────────────────────────────────────────────────
# FOUR TRAPS THIS SWEEP EXISTS TO AVOID. Each one produced a false clean bill of
# health on this repo on 2026-08-03, in the same afternoon.
#
# 1. HEAD IS NOT HISTORY. A string added in one commit and removed in another is
#    absent from the working tree and present forever in the history a clone
#    receives. Grepping HEAD answers a different question than the one publishing
#    asks. Every content check below runs `git log -G` (pickaxe) across all refs.
#
# 2. `git grep` SEARCHES TRACKED FILES ONLY. A file you just wrote and have not
#    committed is invisible to it, so the gate goes green at exactly the moment
#    the mistake is being made. HEAD checks pass --untracked.
#
# 3. NAMES ARE NOT CONTENTS. `git ls-files | grep <term>` searches FILE NAMES.
#    Reporting that as "nothing of that in the repo" is a claim far wider than
#    the evidence — and it is how four references to a third party survived an
#    audit here.
#
# 4. A PATTERN LIST IS ONLY AS GOOD AS WHAT YOU THOUGHT OF. This script cannot
#    know the names of your clients, colleagues or internal hosts. It refuses to
#    report "clean" unless you either supply them or explicitly acknowledge that
#    you are skipping that class. See --names / --no-names.
#
# AND ONE DESIGN CONSTRAINT WORTH STATING: this script never contains the private
# names it searches for. Hardcoding "the client we must not mention" into a
# tracked file would republish exactly what the sweep exists to catch. Private
# terms live in a gitignored file, one per line.
# ─────────────────────────────────────────────────────────────────────────────

set -uo pipefail

NAMES_FILE=".private-sweep-names"
SKIP_NAMES=0
EXCLUDE=(':!*package-lock.json' ':!*pnpm-lock.yaml' ':!*yarn.lock' ':!go.sum' ':!*.svg'
         # This file. A scanner that scans its own rule list always flags itself:
         # the ACCEPTED entry '/home/user/\.rustup' matched the '/home/[a-z]' check
         # on the first real run. Excluding the definition file is what every
         # secret-scanner does; it is narrow, it is stated here, and it means a
         # secret pasted INTO this script would not be caught by this script.
         ':!scripts/pre-publication-sweep.sh')

while [ $# -gt 0 ]; do
  case "$1" in
    --names) NAMES_FILE="${2:-}"; [ -n "$NAMES_FILE" ] || { echo "--names needs a path" >&2; exit 2; }; shift 2 ;;
    --no-names) SKIP_NAMES=1; shift ;;
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repository" >&2; exit 2; }

findings=0
cannot_evaluate=0
report() { printf '  %-14s %-36s %s\n' "$1" "$2" "$3"; }

# ACCEPTED — specific matched CONTENT known to be harmless, each with its reason.
# Scoped to the literal string, never to the pattern: suppressing "home path"
# wholesale would hide a real one tomorrow, which is how a gate becomes a
# rubber stamp. A new occurrence outside this list still fires.
ACCEPTED=(
  '/Users/runner/'      # GitHub Actions runner path baked into an upstream wheel,
                        # quoted in docs/decisions/0069 and the voicebox spike as
                        # the BUG being described. Not a local home directory.
  '/home/user/\.rustup' # test fixture in bench/wasm-vs-docker/guests_test.go
  '03251193000f4bd3b042892be858ee50e8b3719f2b08e5833ac4353724632430'
                        # sha256 checksum of the "reborrow" crate (Cargo.lock in
                        # crates/bitrate-scorer, crates/embed, crates/guardrail-model).
                        # A hex digest, not a card number — it happens to contain a
                        # 13-digit run starting with 4, the exact shape the
                        # credit-card-ish check looks for. Surfaced once that check's
                        # broken \b anchors were fixed to actually anchor (see the
                        # commit that fixed this file's credit-card-ish pattern).
)
accepted_filter() {   # stdin: matching lines -> stdout: lines NOT accepted
  local f='cat' a
  for a in "${ACCEPTED[@]}"; do f="$f | grep -vE '$a'"; done
  eval "$f"
}

# hist <pattern> — commits that ever ADDED OR REMOVED the pattern, anywhere in history.
# `git log -G` is not grep-family: it exits 0 whether or not it finds a commit, so a
# non-zero exit here means the command itself did not run (bad pattern, corrupt repo,
# ...) — never "found nothing". Thanks to `set -o pipefail` above, that exit status
# survives through the trailing `| wc -l | tr -d ' '` and becomes this function's own.
hist() { git log --all --oneline -G"$1" -- . "${EXCLUDE[@]}" 2>/dev/null | wc -l | tr -d ' '; }
# head_ <pattern> — files matching now, including files not yet committed.
# `git grep -l` IS grep-family: 0 = matched, 1 = ran fine and matched nothing (not a
# failure), 2+ = a real error (bad pattern, unreadable path, ...). Only 2+ means the
# probe did not run — see the rc handling in check() below.
# `-e "$1"` (not a bare "$1") because a pattern that itself starts with "-" (the PEM
# check below: '-----BEGIN ...') is otherwise misparsed by git-grep as another option
# instead of the search pattern — which used to fail exactly the way an unreadable
# license file used to in scripts/license-audit.sh: silently, as "0 matches".
head_() { git grep --untracked -lIE -e "$1" -- . "${EXCLUDE[@]}" 2>/dev/null | wc -l | tr -d ' '; }

# ── The \b trap: why no pattern below uses \b ─────────────────────────────────
# \b DOES NOT WORK in either engine check() drives, and it fails in the worst
# way available: silently, as a permanent "clean".
#
# Measured on git 2.50.1 (Apple Git-155), against a repo holding "alpha cat omega"
# in one commit and "alpha abcat omega" in another:
#
#   git grep -lIE '\bcat'                  -> matches ONLY "abcat"  (== pattern 'bcat')
#   git log --all -G '\bcat'               -> matches ONLY "abcat"  (== pattern 'bcat')
#   both engines, '(^|[^A-Za-z0-9_])cat'   -> matches ONLY " cat "  (correct)
#
# So \b does not merely fail to anchor. The backslash is dropped and \b decays to
# a LITERAL 'b' — strictly WORSE than omitting it, because it then REQUIRES a
# stray 'b' exactly where the token must begin or end, which no real credential
# has. Every check spelled '\bPREFIX…\b' consequently matched nothing, ever, in
# either engine, and reported "clean" for it: no error, no COULD_NOT_EVAL, just a
# green line meaning "I looked" when nothing had been looked for. That is trap §1-4
# of this header turned inward on the checks themselves. All seven \b checks were
# confirmed to miss their own canonical positive fixture before this was fixed.
#
# WBL/WBR are the portable substitute, verified in BOTH engines. One deliberate
# difference from a true \b: they CONSUME the boundary character rather than
# asserting it zero-width. That can only merge two matches sharing one separator
# on the same line — check() counts matching LINES and commits, so it cannot care.
WBL='(^|[^A-Za-z0-9_])'
WBR='([^A-Za-z0-9_]|$)'

check() { # label pattern [note]
  local h t live h_rc t_rc live_raw live_rc
  h=$(hist "$2"); h_rc=$?
  t=$(head_ "$2"); t_rc=$?
  # A probe that did not run must never be read as "found nothing" — collapsing the
  # two is exactly the false-clean-bill-of-health class this script's own header (§1-4)
  # exists to catch, applied to itself. See .claude/rules/absence-has-many-causes.md.
  if [ "$h_rc" -ne 0 ] || [ "$t_rc" -gt 1 ]; then
    report "COULD_NOT_EVAL" "$1" "git log rc=$h_rc git grep rc=$t_rc — probe did not run, NOT the same as 0 matches"
    cannot_evaluate=$((cannot_evaluate + 1))
    return
  fi
  if [ "$h" = "0" ] && [ "$t" = "0" ]; then
    report "clean" "$1" ""; return
  fi
  # Something matched. Count only the occurrences that are NOT on the ACCEPTED
  # list, so a documented false positive stops crying wolf without blinding the
  # check to a new, undocumented one. Same rc discipline as above: this is a second,
  # independent git grep call, and a broken run of IT must not silently read as
  # "everything matched was accepted" (live=0 by virtue of empty input, not review).
  live_raw="$(git grep --untracked -hIE -e "$2" -- . "${EXCLUDE[@]}" 2>&1)"
  live_rc=$?
  if [ "$live_rc" -gt 1 ]; then
    report "COULD_NOT_EVAL" "$1" "git grep (detail pass) rc=$live_rc — could not classify the $t match(es) against ACCEPTED"
    cannot_evaluate=$((cannot_evaluate + 1))
    return
  fi
  live=$(printf '%s\n' "$live_raw" | accepted_filter | grep -c . )
  if [ "${live:-0}" = "0" ] && [ "$t" != "0" ]; then
    report "accepted" "$1" "all $t occurrence(s) on the ACCEPTED list"; return
  fi
  report "FINDING" "$1" "history:$h  now:$t  unaccepted:${live:-?}  ${3:-}"
  findings=$((findings + 1))
}

echo "=== A. Identity and local paths ==="
check "home path"        '/Users/[a-z]'
check "home path (unix)" '/home/[a-z]'
check "windows path"     'C:\\\\Users'

echo
echo "=== B. Credentials ==="
# The repo's own secret-redactor DEFINES these prefixes as regexes, and its tests
# carry obviously-fake fixtures. Those are the feature, not a leak. They are
# reported so a human classifies them; they are not auto-suppressed, because an
# allowlist that hides a real key is worse than a gate that cries wolf.
check "AWS key id"       "${WBL}AKIA[0-9A-Z]{16}${WBR}"   "check: redactor regex vs literal"
check "GitHub PAT"       "${WBL}ghp_[A-Za-z0-9]{36}${WBR}" "check: redactor regex vs literal"
check "Slack token"      "${WBL}xox[baprs]-[A-Za-z0-9-]{10,}"
check "Google API key"   "${WBL}AIza[0-9A-Za-z_-]{35}${WBR}"
check "Anthropic key"    'sk-ant-[A-Za-z0-9_-]{50,}'
check "OpenAI key"       "${WBL}sk-[A-Za-z0-9]{40,}${WBR}"
check "PEM private key"  '-----BEGIN [A-Z ]*PRIVATE KEY-----'
check "JWT (3 segments)" "${WBL}eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\."

echo
echo "=== C. Personal data ==="
check "AR phone"         '\+54[ -]?9?[ -]?[0-9]{2,4}[ -]?[0-9]{4}'
check "IBAN-ish"         "${WBL}[A-Z]{2}[0-9]{2}[A-Z0-9]{12,}${WBR}"
# Digit boundaries, not WBL/WBR, and not \b. See "The \b trap" above for why \b is
# absent everywhere in this file; the correction to that note is that `git log -G`
# does not match \b to "nothing at all" — it decays it to a literal 'b' exactly as
# `git grep -E` does, which is why '\bthe\b' scored 0 (it was searching for "btheb").
# This check uses [^0-9] rather than WBL/WBR's [^A-Za-z0-9_] deliberately: a letter
# MUST count as a boundary here, so that a card-shaped digit run is still found when
# it abuts a letter, while a longer digit run (a SHA, a checksum, a timestamp) that
# merely CONTAINS 13 or 16 digits is correctly rejected.
check "credit-card-ish"  '(^|[^0-9])(4[0-9]{12}|5[1-5][0-9]{14})([^0-9]|$)'

echo
echo "=== D. Private names (clients, colleagues, internal hosts) ==="
if [ "$SKIP_NAMES" -eq 1 ]; then
  echo "  SKIPPED by --no-names. This class is the one a generic pattern list"
  echo "  cannot cover, so the verdict below is PARTIAL, not clean."
elif [ ! -f "$NAMES_FILE" ]; then
  cat >&2 <<EOF
BLOCKED: no private-name list at '$NAMES_FILE'.

The patterns above cannot know your clients, colleagues or internal hostnames —
the exact class that survived an audit here. Create the file (one term per line,
'#' comments allowed, KEEP IT GITIGNORED) or pass --no-names to acknowledge that
you are publishing without checking it.
EOF
  exit 2
else
  if ! git check-ignore -q "$NAMES_FILE" 2>/dev/null; then
    echo "BLOCKED: '$NAMES_FILE' is NOT gitignored. It would publish the very terms" >&2
    echo "you are trying to keep out. Add it to .gitignore and re-run." >&2
    exit 2
  fi
  n=0
  while IFS= read -r term; do
    case "$term" in ''|\#*) continue ;; esac
    n=$((n + 1))
    check "name: $term" "$term"
  done < "$NAMES_FILE"
  [ "$n" -gt 0 ] || echo "  (list is empty — nothing checked)"
fi

echo
echo "=== E. Binary blobs ever committed (text greps skip these) ==="
big=$(git rev-list --objects --all 2>/dev/null \
      | git cat-file --batch-check='%(objecttype) %(objectname) %(objectsize) %(rest)' 2>/dev/null \
      | awk '$1=="blob" && $3>102400 {printf "  %6.1f MB  %s\n", $3/1048576, $4}' | sort -rn -u | head -15)
if [ -n "$big" ]; then
  echo "$big"
  echo "  ^ classify by hand: an image carries EXIF, a PDF carries author metadata,"
  echo "    an archive carries whatever was in it. Text files here are fine."
else
  echo "  none over 100 KB"
fi

echo
if [ "$cannot_evaluate" -gt 0 ]; then
  echo "VERDICT: COULD NOT EVALUATE — $cannot_evaluate probe(s) did not run (see the"
  echo "  COULD_NOT_EVAL line(s) above). Per the header contract this is exit 2, and an"
  echo "  exit-2 run is NEVER a pass: a broken probe reads identically to '0 matches' on"
  echo "  its own output, so nothing above it can be trusted as clean — fix the probe"
  echo "  (or the git state it depends on) and re-run before treating this as done."
  exit 2
fi
if [ "$findings" -eq 0 ] && [ "$SKIP_NAMES" -eq 0 ]; then
  echo "VERDICT: clean against the patterns checked."
  echo "That is not the same as 'safe to publish'. A sweep certifies its own list"
  echo "and nothing else — read §4 of the header before treating this as done."
  exit 0
fi
if [ "$SKIP_NAMES" -eq 1 ] && [ "$findings" -eq 0 ]; then
  echo "VERDICT: PARTIAL — generic patterns clean, private names not checked."
  exit 1
fi
echo "VERDICT: $findings finding(s). Each needs classifying, not suppressing:"
echo "  real leak      -> remove from HEAD, then from history (see"
echo "                    docs/operations/purge-rewritten-history.md), then re-run"
echo "  by design      -> e.g. a secret-redactor's own regexes; leave it, and say"
echo "                    so where a reader will look, not only in your head"
echo "  false positive -> e.g. 255.255.255.255 matching a national-ID shape"
exit 1
