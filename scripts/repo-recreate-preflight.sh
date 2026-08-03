#!/usr/bin/env bash
# repo-recreate-preflight.sh — what would be destroyed by deleting and recreating
# the GitHub repository?
#
# Recreating the repo under the same owner/name is the only self-service way to
# purge unreachable objects left behind by a history rewrite (GitHub serves them
# by SHA to anyone who can read the repo; only Support runs gc otherwise). The
# git history survives — it is re-pushed. Everything GitHub stores ABOUT the repo
# does not. This enumerates that, so the decision is made against a list rather
# than an impression.
#
#   exit 0  nothing would be lost — safe to recreate
#   exit 1  something would be lost — read the list first
#   exit 2  CANNOT EVALUATE — see below. Never treat as a pass.
#
# Read-only: every call is a GET.
#
# Usage:
#   repo-recreate-preflight.sh [owner/repo] [--as <gh-account>]
#
# --as switches the active gh account for this run and switches BACK on exit,
# via trap, on every path: success, findings, refusal, error, Ctrl-C. `gh auth
# switch` writes to a GLOBAL config file, not per-terminal, so a bare switch
# leaves every other shell and every concurrent agent session on the wrong
# account — and an `&&` chain that dies in the middle never switches back.
#
# ─────────────────────────────────────────────────────────────────────────────
# WHY exit 2 EXISTS, AND WHY IT IS THE MOST IMPORTANT PART OF THIS SCRIPT
#
# GitHub answers 404 — not 403 — for a private repository you cannot see. So an
# unauthenticated or wrong-account run reports "0 secrets, 0 webhooks, 0
# collaborators" and looks exactly like a clean bill of health. Acting on that
# would delete a repo whose secrets were never enumerated, and CI would break
# after the recreation with no trace of why.
#
# This happened twice while writing this, with two different accounts on one
# machine. So the script probes access FIRST and refuses to report anything if
# the repo is not visible. A guard that cannot evaluate must never absolve.
# ─────────────────────────────────────────────────────────────────────────────

set -uo pipefail

REPO="Luum-Home/aisotropy"
AS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --as) AS="${2:-}"; [ -n "$AS" ] || { echo "--as needs an account" >&2; exit 2; }; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) REPO="$1"; shift ;;
  esac
done

command -v gh >/dev/null 2>&1 || { echo "gh not found" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "jq not found" >&2; exit 2; }

who=$(gh api user --jq .login 2>/dev/null) || { echo "gh is not authenticated" >&2; exit 2; }

# Switch account for this run, and guarantee the restore. The trap fires on
# EXIT, which covers every exit path below including the refusal and Ctrl-C.
restore_account() { gh auth switch -u "$1" >/dev/null 2>&1 || \
  echo "WARNING: could not switch back to '$1' — run: gh auth switch -u $1" >&2; }
if [ -n "$AS" ] && [ "$AS" != "$who" ]; then
  original="$who"
  gh auth switch -u "$AS" >/dev/null 2>&1 || { echo "cannot switch to '$AS' (is it logged in? gh auth status)" >&2; exit 2; }
  trap 'restore_account "$original"' EXIT
  who=$(gh api user --jq .login 2>/dev/null) || { echo "switched to '$AS' but the API refuses it" >&2; exit 2; }
  echo "note:    switched to '$who' for this run; '$original' restored on exit"
fi

# Access probe. Must come before every count below.
if ! gh api "repos/$REPO" >/dev/null 2>&1; then
  cat >&2 <<EOF
BLOCKED: cannot read $REPO as '$who'.

GitHub returns 404 (not 403) for a private repo you cannot see, so every count
below would read 0 and look clean. Refusing to report.

  gh auth status                       # which accounts are configured
  gh auth switch -u <account>          # one with access to $REPO
  $0 $REPO

Remember to switch back afterwards if you use another account for other work.
EOF
  exit 2
fi

echo "repo:    $REPO"
echo "as:      $who"
echo "visible: yes"
echo

# One request, capped. We only need "zero or not"; --paginate over a large repo
# is unbounded and turned this into a two-minute hang on the first test run.
# A count of 100 is reported as "100+" rather than pretending to be exact.
count() {
  local sep='?'; case "$1" in *'?'*) sep='&';; esac
  local n
  n=$(gh api "repos/$REPO/$1${sep}per_page=100" --jq 'length' 2>/dev/null) || { echo 0; return; }
  [ "${n:-0}" -ge 100 ] && { echo "100+"; return; }
  echo "${n:-0}"
}

# GitHub's /issues endpoint RETURNS PULL REQUESTS TOO, so a naive count double-
# counts every PR as an issue. Filter them out, or the report says "2 issues, 2
# PRs" for what is really 2 PRs and no issues. (Caught on the first real run.)
prs=$(count "pulls?state=all")
issues_all=$(gh api "repos/$REPO/issues?state=all&per_page=100" \
  --jq '[.[] | select(.pull_request == null)] | length' 2>/dev/null || echo UNKNOWN)
forks=$(gh api "repos/$REPO" --jq '.forks_count')
stars=$(gh api "repos/$REPO" --jq '.stargazers_count')
watchers=$(gh api "repos/$REPO" --jq '.subscribers_count')
wiki=$(gh api "repos/$REPO" --jq '.has_wiki')
releases=$(count releases)
tags=$(count tags)
hooks=$(count hooks)
keys=$(count keys)
collabs=$(count collaborators)
# These three need permissions the repo-read scope does not always carry, and gh
# prints its error JSON to STDOUT — so `|| echo 0` turns a 403 into a reported
# zero. That is the exact failure this script exists to prevent, and the first
# test run reproduced it on the "CI breaks silently" row. Unknown is UNKNOWN.
total_count() {
  local out
  out=$(gh api "repos/$REPO/$1" --jq '.total_count' 2>/dev/null)
  if [ $? -ne 0 ] || ! printf '%s' "$out" | grep -qE '^[0-9]+$'; then
    echo UNKNOWN; return
  fi
  printf '%s' "$out"
}
secrets=$(total_count actions/secrets)
vars=$(total_count actions/variables)
envs=$(total_count environments)
protection=$(gh api "repos/$REPO/branches/main/protection" >/dev/null 2>&1 && echo 1 || echo 0)

lost=0
unknown=0
row() { # name count [note]
  printf '  %-22s %-8s %s\n' "$1" "$2" "${3:-}"
  [ "$2" = "UNKNOWN" ] && { unknown=$((unknown + 1)); return 0; }
  [ "$2" != "0" ] && [ "$2" != "false" ] && lost=$((lost + 1))
  return 0
}

echo "Would NOT survive a delete-and-recreate:"
row "issues (all states)" "$issues_all"
row "pull requests"       "$prs"
row "forks"               "$forks"   "a fork also keeps the old objects alive"
row "stars"               "$stars"
row "watchers"            "$watchers"
row "releases"            "$releases"
row "wiki"                "$wiki"
row "webhooks"            "$hooks"
row "deploy keys"         "$keys"
row "collaborators"       "$collabs" "beyond the owner"
row "Actions secrets"     "$secrets" "CI breaks silently if not re-added"
row "Actions variables"   "$vars"
row "environments"        "$envs"
row "branch protection"   "$protection"
echo
echo "Survives, because it is re-pushed from your clone: all commits, branches,"
echo "tags ($tags in the remote today — re-push with 'git push --tags')."
echo "The URL survives too, if you recreate under the same owner/name — which"
echo "matters here: the Go import path IS the URL."
echo

if [ "$unknown" -gt 0 ]; then
  echo "VERDICT: CANNOT EVALUATE — $unknown row(s) came back UNKNOWN."
  echo "The token lacks the permission to read them, and a permission error is not"
  echo "a zero. Re-run with an account that can read them, or check those settings"
  echo "in the web UI by hand, before deleting anything."
  exit 2
fi
if [ "$lost" -eq 0 ]; then
  echo "VERDICT: nothing would be lost. Recreating is a push plus repo settings."
  exit 0
fi
echo "VERDICT: $lost item(s) above are non-zero. Re-create them by hand afterwards,"
echo "or choose the GitHub Support route instead (keeps everything, costs latency)."
exit 1
