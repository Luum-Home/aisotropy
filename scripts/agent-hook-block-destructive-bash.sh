#!/usr/bin/env bash
# agent-hook-block-destructive-bash.sh — PreToolUse guard (matcher: Bash).
#
# CANONICAL COPY. The runtime location an agent harness reads is
# .claude/hooks/block-destructive-bash.sh (and its .codex/ projection), and
# .claude/ is gitignored (.gitignore:13) so nothing under it travels with a
# clone. Same shape as the git hooks: per ADR-0043 the committed, reviewable
# artifact is this script plus scripts/install-hooks.sh, which symlinks it into
# the untracked runtime location. Edit THIS file; never the symlink.
#
# Wiring the harness to call it is a separate, manual step -- the installer
# prints the settings.json snippet rather than editing that file. settings.json
# is the operator's own local agent config: untracked, hand-maintained, and
# specific to the machine and harness it runs on. An installer has no business
# rewriting a file the repo neither tracks nor owns.
#
# Denies a destructive Bash command whose target resolves OUTSIDE the repo and
# outside the session/system temp. Motivated by a sub-agent that ran
# `rm -rf ~/.goenv` — an out-of-scope, home-directory destroy with no gate.
#
# Covered verbs (as COMMANDS, not flags): rm -r/-f, rmdir, find … -delete,
# dd of=, mkfs*, shred. Deliberately NOT covered (too many legit uses / false
# positives): plain redirects `>`/`>>`, mv/cp. In-repo deletions are ALLOWED
# (agents clean build dirs constantly); so are /tmp, /private/tmp, /var/folders,
# and /dev/null-style sinks. This is a heuristic defense-in-depth guard, not a
# sandbox: it stops the common dangerous cases, not every possible obfuscation.
#
# Protocol: exit 2 = BLOCK (stderr is shown to the agent); exit 0 = allow.
set -uo pipefail
input="$(cat)"
repo="$(git rev-parse --show-toplevel 2>/dev/null || echo "${CLAUDE_PROJECT_DIR:-$PWD}")"

reason="$(REPO="$repo" HOMEDIR="$HOME" INPUT="$input" python3 <<'PY'
import json, os, re, sys

inp  = os.environ.get("INPUT", "")
repo = os.path.realpath(os.environ.get("REPO", "") or ".")
home = os.environ.get("HOMEDIR", "") or ""
try:
    cmd = json.loads(inp).get("tool_input", {}).get("command", "") or ""
except Exception:
    cmd = ""
if not cmd:
    sys.exit(0)

# --- a destructive verb, at a COMMAND position (not a flag like --rm) ? ---
CMD_START = r'(?:^|[\n;&|(){}`]|\bsudo\s+|\bxargs\s+(?:-\S+\s+)*)\s*'
rm_at_cmd = re.search(CMD_START + r'rm\b', cmd) and not re.search(r'\bgit\s+rm\b', cmd)
rm_force  = rm_at_cmd and re.search(r'\brm\b\s+(?:-\S*[rf]\S*|--recursive|--force|-[a-zA-Z]*[rf])', cmd)
other = (re.search(CMD_START + r'rmdir\b', cmd)
         or re.search(r'\bfind\b[^\n]*\s-delete\b', cmd)
         or re.search(r'\bdd\b[^\n]*\bof=', cmd)
         or re.search(r'\bmkfs\S*\b', cmd)
         or re.search(CMD_START + r'shred\b', cmd))
if not (rm_force or other):
    sys.exit(0)

ALLOW_SINKS = {"/dev/null", "/dev/zero", "/dev/stdout", "/dev/stderr", "/dev/tty"}
SYS = ("/etc", "/usr", "/bin", "/sbin", "/var", "/opt", "/System", "/Library",
       "/Applications", "/private/etc", "/dev", "/boot", "/lib", "/root", "/cores")

def expand(raw):
    t = raw.strip().strip('"').strip("'")
    t = t.replace("${HOME}", home).replace("$HOME", home)
    if t == "~":
        t = home
    elif t.startswith("~/"):
        t = home + t[1:]
    return t

danger = []
for raw in re.split(r'\s+', cmd):
    if not raw or raw.startswith("-"):
        continue
    # only path-looking tokens rooted at /, ~, or $HOME are candidates;
    # a relative token resolves under the CWD (the repo) -> allowed.
    if not (raw.startswith("/") or raw.startswith("~") or "$HOME" in raw or "${HOME}" in raw):
        continue
    t = expand(raw)
    base = t.rstrip("*").rstrip("/") or "/"
    if base in ALLOW_SINKS or base.startswith("/dev/fd"):
        continue
    rp = os.path.normpath(base)
    # in-repo? (the repo lives under $HOME, so check it FIRST) -> allow
    if rp == repo or rp.startswith(repo + os.sep):
        continue
    # session / system temp -> allow
    if rp.startswith("/private/tmp/") or rp.startswith("/tmp/") or rp.startswith("/var/folders/"):
        continue
    # bare root
    if rp in ("/", ""):
        danger.append(t); continue
    # home root or a home dotdir/anything under $HOME (e.g. ~/.goenv, ~/.ssh) -> block
    if home and (rp == home or rp.startswith(home + os.sep)):
        danger.append(t); continue
    # a system directory -> block
    if any(rp == s or rp.startswith(s + os.sep) for s in SYS):
        danger.append(t); continue
    # any other absolute path outside the repo -> block
    if os.path.isabs(rp):
        danger.append(t); continue

if danger:
    print("targets a path OUTSIDE the repo: " + ", ".join(danger[:5]))
    sys.exit(1)
sys.exit(0)
PY
)"
rc=$?

if [ "$rc" -ne 0 ] && [ -n "$reason" ]; then
  echo "BLOCKED by block-destructive-bash: this command $reason" >&2
  echo "Repo scope: $repo. In-repo deletions, /tmp and /dev/null are allowed." >&2
  echo "If this is genuinely needed, run it yourself outside the agent, or re-scope it inside the repo." >&2
  exit 2
fi
exit 0
