#!/usr/bin/env bash
# install-hooks.sh — wire the local supply-chain gate into git.
#
# WHY THIS EXISTS: git hooks live in .git/hooks/ and are NOT tracked by git, so
# the committed, reviewable artifact is this installer plus the scripts it points
# at. Run it once after cloning to activate the local supply-chain gate: the repo
# runs govulncheck + dependency scanning locally before every push instead of
# spending extra paid GitHub Actions minutes on those steps.
#
# INSTALLS:
#   .git/hooks/pre-push -> scripts/supply-chain-guard.sh   (symlink; runs govulncheck)
#
# The hook is a symlink to the repo script, so re-running this installer is
# idempotent and the hook always reflects the committed script. An existing,
# unrelated pre-push hook is backed up rather than clobbered.
#
# USAGE:   scripts/install-hooks.sh
# EXIT:    0 = installed (or already installed)
#          2 = usage / environment error
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "not in a git repo" >&2; exit 2; }

REPO_ROOT="$(pwd)"
HOOK_DIR="$(git rev-parse --git-path hooks)"
case "$HOOK_DIR" in /*) : ;; *) HOOK_DIR="$REPO_ROOT/$HOOK_DIR" ;; esac
mkdir -p "$HOOK_DIR"

GATE="$REPO_ROOT/scripts/supply-chain-guard.sh"
[ -f "$GATE" ] || { echo "missing $GATE" >&2; exit 2; }
chmod +x "$GATE" 2>/dev/null || true

LINK="$HOOK_DIR/pre-push"

# Absolute symlink: robust across worktrees and hook-dir relocation. Hooks are
# not committed, so a machine-absolute target is fine (re-created per clone).
if [ -L "$LINK" ] && [ "$(readlink "$LINK")" = "$GATE" ]; then
  echo "install-hooks: pre-push already points at scripts/supply-chain-guard.sh — nothing to do."
  exit 0
fi

if [ -e "$LINK" ] && [ ! -L "$LINK" ]; then
  backup="$LINK.bak.$(date +%Y%m%d%H%M%S)"
  mv "$LINK" "$backup"
  echo "install-hooks: existing pre-push hook backed up to $backup"
fi

ln -sf "$GATE" "$LINK"
echo "install-hooks: installed pre-push -> scripts/supply-chain-guard.sh"
echo "               (bypass once with: SKIP_SUPPLY_CHAIN_GUARD=1 git push … or git push --no-verify)"
exit 0
