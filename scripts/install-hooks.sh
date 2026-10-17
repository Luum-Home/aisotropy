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
#   .git/hooks/pre-push   <- scripts/pre-push-hook.sh        (dispatcher copy:
#                            govulncheck + the deterministic OWASP LLM/Agentic suite)
#   .git/hooks/pre-commit -> scripts/license-gate-precommit.sh (symlink; conditional
#                            license gate — runs license-audit.sh --root-only ONLY when
#                            the staged changes touch a dependency/image manifest)
#   .claude/hooks/ + .codex/hooks/block-destructive-bash.sh
#                         -> scripts/agent-hook-block-destructive-bash.sh (symlink;
#                            PreToolUse guard that denies a destructive Bash command
#                            targeting a path outside the repo and outside /tmp)
#
# AGENT HOOKS vs GIT HOOKS: both runtime locations are untracked (.git/hooks/ is
# never tracked; .claude/ and .codex/ are gitignored at .gitignore:13,14), so the
# same rule applies — the committed artifact is the script plus this installer.
# The agent hook additionally needs a settings.json entry to be CALLED; this
# installer prints that snippet instead of editing settings.json, because that
# file is the operator's own local agent config — untracked, hand-maintained,
# machine- and harness-specific — and not this installer's to rewrite.
#
# The pre-push dispatcher selects the active worktree's gate. Other hooks remain
# symlinks. Re-running this installer is idempotent; replaced hooks are backed up.
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

# install_hook <hook-name> <target-script> <bypass-hint>
# Other hooks retain their absolute symlink targets (re-created per clone).
# Pre-push uses the worktree-aware dispatcher installed separately below.
# An existing, unrelated hook of the same name is backed up rather than clobbered.
install_hook() {
  hook_name="$1"; gate="$2"; bypass="$3"
  [ -f "$gate" ] || { echo "missing $gate" >&2; exit 2; }
  chmod +x "$gate" 2>/dev/null || true
  link="$HOOK_DIR/$hook_name"

  if [ -L "$link" ] && [ "$(readlink "$link")" = "$gate" ]; then
    echo "install-hooks: $hook_name already points at ${gate#"$REPO_ROOT/"} — nothing to do."
    return 0
  fi

  if [ -e "$link" ] && [ ! -L "$link" ]; then
    backup="$link.bak.$(date +%Y%m%d%H%M%S)"
    mv "$link" "$backup"
    echo "install-hooks: existing $hook_name hook backed up to $backup"
  fi

  ln -sf "$gate" "$link"
  echo "install-hooks: installed $hook_name -> ${gate#"$REPO_ROOT/"}"
  echo "               (bypass once with: $bypass)"
}

# A shared absolute symlink would keep running the installing checkout's gate
# even when a different worktree contains the fix being pushed.
dispatcher="$REPO_ROOT/scripts/pre-push-hook.sh"
[ -f "$dispatcher" ] || { echo "missing $dispatcher" >&2; exit 2; }
if [ ! -L "$HOOK_DIR/pre-push" ] && cmp -s "$dispatcher" "$HOOK_DIR/pre-push"; then
  echo "install-hooks: pre-push dispatcher already installed."
else
  if [ -e "$HOOK_DIR/pre-push" ] || [ -L "$HOOK_DIR/pre-push" ]; then
    backup="$HOOK_DIR/pre-push.bak.$(date +%Y%m%d%H%M%S)"
    mv "$HOOK_DIR/pre-push" "$backup"
    echo "install-hooks: existing pre-push hook backed up to $backup"
  fi
  cp "$dispatcher" "$HOOK_DIR/pre-push"
  chmod +x "$HOOK_DIR/pre-push"
  echo "install-hooks: installed pre-push dispatcher for the active worktree."
fi
install_hook pre-commit "$REPO_ROOT/scripts/license-gate-precommit.sh" \
  "SKIP_LICENSE_AUDIT=1 git commit … or git commit --no-verify"

# --- agent (harness) hooks -------------------------------------------------
# install_agent_hook <harness-dir> <hook-basename> <target-script>
# Same symlink discipline as install_hook, but the runtime dir is a gitignored
# harness directory rather than .git/hooks. Missing dirs are created; an
# existing regular file is backed up rather than clobbered.
install_agent_hook() {
  hdir="$1"; hook_name="$2"; gate="$3"
  [ -f "$gate" ] || { echo "missing $gate" >&2; exit 2; }
  chmod +x "$gate" 2>/dev/null || true
  mkdir -p "$REPO_ROOT/$hdir"
  link="$REPO_ROOT/$hdir/$hook_name"

  if [ -L "$link" ] && [ "$(readlink "$link")" = "$gate" ]; then
    echo "install-hooks: $hdir/$hook_name already points at ${gate#"$REPO_ROOT/"} — nothing to do."
    return 0
  fi
  if [ -e "$link" ] && [ ! -L "$link" ]; then
    backup="$link.bak.$(date +%Y%m%d%H%M%S)"
    mv "$link" "$backup"
    echo "install-hooks: existing $hdir/$hook_name backed up to $backup"
  fi
  ln -sf "$gate" "$link"
  echo "install-hooks: installed $hdir/$hook_name -> ${gate#"$REPO_ROOT/"}"
}

AGENT_GATE="$REPO_ROOT/scripts/agent-hook-block-destructive-bash.sh"
for hdir in .claude/hooks .codex/hooks; do
  install_agent_hook "$hdir" block-destructive-bash.sh "$AGENT_GATE"
done

# The symlink alone does nothing until the harness is told to call it. Print the
# wiring rather than editing settings.json (see header).
if [ -f "$REPO_ROOT/.claude/settings.json" ] \
   && grep -q 'block-destructive-bash' "$REPO_ROOT/.claude/settings.json" 2>/dev/null; then
  echo "install-hooks: .claude/settings.json already wires block-destructive-bash.sh."
else
  cat <<'WIRING'
install-hooks: NOT WIRED — the agent hook is installed but nothing calls it yet.
               Add this entry under "hooks" > "PreToolUse" in .claude/settings.json:

  { "matcher": "Bash",
    "hooks": [ { "type": "command",
                 "command": "bash \"${CLAUDE_PROJECT_DIR:-$PWD}/.claude/hooks/block-destructive-bash.sh\"" } ] }

               Verify afterwards with:
                 printf '{"tool_input":{"command":"rm -rf ~/.goenv"}}' \
                   | bash scripts/agent-hook-block-destructive-bash.sh; echo "rc=$? (expect 2)"
WIRING
fi
exit 0
