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
)
accepted_filter() {   # stdin: matching lines -> stdout: lines NOT accepted
  local f='cat' a
  for a in "${ACCEPTED[@]}"; do f="$f | grep -vE '$a'"; done
  eval "$f"
}

# hist <pattern> — commits that ever ADDED OR REMOVED the pattern, anywhere in history
hist() { git log --all --oneline -G"$1" -- . "${EXCLUDE[@]}" 2>/dev/null | wc -l | tr -d ' '; }
# head_ <pattern> — files matching now, including files not yet committed
head_() { git grep --untracked -lIE "$1" -- . "${EXCLUDE[@]}" 2>/dev/null | wc -l | tr -d ' '; }

check() { # label pattern [note]
  local h t live
  h=$(hist "$2"); t=$(head_ "$2")
  if [ "$h" = "0" ] && [ "$t" = "0" ]; then
    report "clean" "$1" ""; return
  fi
  # Something matched. Count only the occurrences that are NOT on the ACCEPTED
  # list, so a documented false positive stops crying wolf without blinding the
  # check to a new, undocumented one.
  live=$(git grep --untracked -hIE "$2" -- . "${EXCLUDE[@]}" 2>/dev/null | accepted_filter | grep -c . )
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
check "AWS key id"       '\bAKIA[0-9A-Z]{16}\b'   "check: redactor regex vs literal"
check "GitHub PAT"       '\bghp_[A-Za-z0-9]{36}\b' "check: redactor regex vs literal"
check "Slack token"      '\bxox[baprs]-[A-Za-z0-9-]{10,}'
check "Google API key"   '\bAIza[0-9A-Za-z_-]{35}\b'
check "Anthropic key"    'sk-ant-[A-Za-z0-9_-]{50,}'
check "OpenAI key"       '\bsk-[A-Za-z0-9]{40,}\b'
check "PEM private key"  '-----BEGIN [A-Z ]*PRIVATE KEY-----'
check "JWT (3 segments)" '\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.'

echo
echo "=== C. Personal data ==="
check "AR phone"         '\+54[ -]?9?[ -]?[0-9]{2,4}[ -]?[0-9]{4}'
check "IBAN-ish"         '\b[A-Z]{2}[0-9]{2}[A-Z0-9]{12,}\b'
check "credit-card-ish"  '\b(?:4[0-9]{12}|5[1-5][0-9]{14})\b'

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
