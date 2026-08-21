---
type: research
title: "Agent hooks under .claude/ — should they be versioned?"
description: Investigation into whether the agent-governing hooks under .claude/hooks/ should travel with the repo. Finds that 37 of 38 registered hooks are the agent tooling tooling that .gitignore excludes for a stated LICENSE reason, that exactly one hook is project-authored and genuinely protective, and that one is inert. Recommends versioning that single hook via the ADR-0043 installer pattern, and explicitly recommends against un-ignoring .claude/.
tags: [research, hooks, agents, gitignore, supply-chain, adr-0043, tooling]
timestamp: 2026-08-21T00:00:00Z
---

# Agent hooks under `.claude/` — should they be versioned?

**Base commit:** `8de9ae1`. **Method:** read-only inspection of the main checkout, analysis
and changes in an isolated worktree. No builds or test suites were run (machine was at
`resource-guard.sh` rc=1 DEGRADED, ~114 MB free).

**Verdict, up front:** version **one** file — `block-destructive-bash.sh` — through the
installer pattern the repo already uses for git hooks (ADR-0043). Do **not** un-ignore
`.claude/`, and do **not** version `block-prod-urls.sh` or `settings.json`. The reasons are
below, each with the command that produced it.

---

## 0. Two corrections to the premises this investigation started from

Both were handed to me as fact. Both are wrong, and the second one nearly produced a false
"all clean" in the section that matters most.

### 0.1 An automatic note described a different project's mechanism

A hook-injected "KNOWN TRAPS" note asserted that `.claude/settings.json` is *generated* here,
that the canonical hook registry is `agent-tooling.yaml > harness.hooks`, and that the fix is
`bash scripts/apply-efficiency-profile.sh standard`.

```bash
for f in scripts/_lib/settings-driver-claude-code.sh scripts/apply-efficiency-profile.sh \
         agent-tooling.yaml .claude/settings.json; do
  [ -e "$f" ] && printf 'EXISTS %-52s tracked=%s\n' "$f" \
      "$(git ls-files --error-unmatch "$f" >/dev/null 2>&1 && echo 1 || echo 0)" \
    || printf 'ABSENT %-52s\n' "$f"
done
grep -n -E '^\s*harness:' agent-tooling.yaml || echo "(no 'harness:' key)"
```

```
ABSENT scripts/_lib/settings-driver-claude-code.sh
ABSENT scripts/apply-efficiency-profile.sh
EXISTS agent-tooling.yaml                          tracked=0
EXISTS .claude/settings.json                      tracked=0
(no 'harness:' key)
```

Neither script exists; `agent-tooling.yaml` has no `harness:` section. **Treated as refuted.**
The consequence that *is* load-bearing: `.claude/settings.json` is hand-maintained and
untracked, so the whole chain — the wiring *and* the hooks it points at — exists only on the
operator's disk.

### 0.2 `grep -r` in this repo silently skips the directory under investigation

`grep` here is not the system binary. It is a shell function the harness installs:

```bash
declare -f grep | grep -o -- '--ignore-files'
```

```
--ignore-files
```

It execs `ugrep` with `--ignore-files` unconditionally, which means **recursive greps honour
`.gitignore`** — and `.gitignore:8` is `.claude/`. So a repo-root sweep reports nothing for the
exact files being audited:

```bash
grep -rl 'block-destructive-bash' .                    # → (nothing)
grep -rl --no-ignore-files 'block-destructive-bash' .  # → .claude/hooks/... , .claude/settings.json
```

This is not academic. My first sensitive-data sweep printed `clean` for all 14 patterns. A
positive control showed why:

```bash
wc -c .claude/hooks/block-destructive-bash.sh   # → open: No such file or directory
```

The sweep had been passed a relative path from the wrong working directory and **never opened
the files**. Fourteen lines of `clean` meant "I did not look." Every sweep below therefore
carries a positive control (a pattern that must hit) and a byte count proving the file opened.
A verification tool that could not look must never absolve — the same rule
`prune-guard.sh` learned about nested modules.

---

## 1. Inventory: what is actually there

### 1.1 The premise "it's 2 hooks" is true of `.claude/hooks/`, and misleading about the repo

```bash
find . -name .git -prune -o -path ./external -prune -o -type d -name hooks -print
```

| Location | Files | Tracked | What it is |
|---|---:|---:|---|
| `.claude/hooks/` | 2 | 0 | project-authored agent hooks |
| `.codex/hooks/` | 2 | 0 | byte-identical projection of the above |
| `.agent-tooling/hooks/cos/` | 43 `.sh` + `_lib/` | 0 | the agent tooling tooling |
| `.git/hooks/` | 2 symlinks | n/a | git hooks → `scripts/` (ADR-0043) |
| `.agent-tooling/checkpoints/*/files/.codex/hooks/` | 30 dirs | 0 | snapshots, not live |

The `.codex/` twins are byte-identical, exactly as `.gitignore:10-15` claims:

```bash
shasum -a 256 .claude/hooks/*.sh .codex/hooks/*.sh
```

```
921b1492…845  .claude/hooks/block-destructive-bash.sh
684457d4…32e  .claude/hooks/block-prod-urls.sh
921b1492…845  .codex/hooks/block-destructive-bash.sh
684457d4…32e  .codex/hooks/block-prod-urls.sh
```

For scale, the shell-script universe (the number I was warned not to eyeball):

```bash
find . -name .git -prune -o -path ./external -prune -o -type f -name '*.sh' -print | wc -l   # 827
# …excluding COS checkpoint snapshots:                                                          196
git ls-files '*.sh' | wc -l                                                                   # 109
```

### 1.2 What invokes what

```bash
python3 -c "…parse .claude/settings.json → hooks…"   # full command in §Appendix
```

**38 hook commands are registered. 37 point at `.agent-tooling/hooks/cos/`. One points at
`.claude/hooks/`.**

```bash
grep -c '\.agent-tooling/hooks' .claude/settings.json   # 37
grep -c '\.claude/hooks'       .claude/settings.json   # 1
```

The single project-authored entry:

```
[PreToolUse] matcher=Bash
    bash "${CLAUDE_PROJECT_DIR:-$PWD}/.claude/hooks/block-destructive-bash.sh"
```

`.claude/settings.local.json` does not exist. `settings.json` has exactly two top-level keys,
`hooks` and `permissions`, the latter being 10 `Read(...)` deny rules for `.env`, `*.key`,
`*.pem`, `.ssh/**` and similar.

**`block-prod-urls.sh` is registered nowhere.** It is an orphan.

### 1.3 What each hook does — measured, not read

Run against copies, in the worktree:

```bash
p(){ printf '{"tool_input":{"command":"%s"}}' "$1" | bash ./block-destructive-bash.sh >/dev/null 2>&1; echo "rc=$?"; }
```

| input | rc | meaning |
|---|---:|---|
| `rm -rf ~/.goenv` | 2 | BLOCK — the motivating incident |
| `rm -rf /Users/PLACEHOLDER/Documents` | 2 | BLOCK — home path |
| `rm -rf /etc/passwd` | 2 | BLOCK — system path |
| `rm -rf ./build` | 0 | allow — in-repo |
| `rm -rf /tmp/scratch` | 0 | allow — temp |
| `ls -la` | 0 | allow |
| `curl https://api.aisotropy.com/prod` → `block-prod-urls.sh` | 0 | **allow — the hook is inert** |

`block-destructive-bash.sh` is real, working protection: 3/3 dangerous blocked, 3/3 legitimate
allowed. `block-prod-urls.sh` ships with every pattern commented out and returns 0 before
reading stdin; its own header says the patterns are placeholders to "fill in … then wire this
into settings.json … if desired." Neither was ever done.

**A known characteristic of the working hook, found by accident:** it matches on command
*text*, so a command that merely *contains* a destructive string as data is blocked too. My own
test invocation was blocked for quoting `rm -rf ~/.goenv` inside a JSON payload. The hook's
header already calls itself "a heuristic defense-in-depth guard, not a sandbox," so this is
documented behaviour, not a defect — but it is worth knowing before anyone widens its patterns.

---

## 2. Do the hooks contain anything that must not travel?

Sweep, with controls proving it read the bytes:

```bash
A=.claude/hooks/block-destructive-bash.sh; B=.claude/hooks/block-prod-urls.sh
wc -c "$A" "$B"                                        # 4234 + 1055 = 5289  ← files opened
grep -hcE 'block-destructive|PROD_PATTERNS' "$A" "$B"  # 2 / 4               ← control hits
grep -hc  'zzz-nonexistent-zzz' "$A" "$B"              # 0 / 0               ← control misses
# The operator-specific tokens (given name, surname, mail handle, client and
# org names) are NOT inlined here on purpose: this report is tracked, and
# writing them down would put in the repo the very strings the sweep exists to
# keep out. They live in .private-sweep-names (gitignored) and in the check
# list of scripts/pre-publication-sweep.sh, which is where a reader should look.
while IFS= read -r pat; do
  grep -nEi -e "$pat" "$A" "$B" || true
done < <(personal_tokens)   # .private-sweep-names + the script's own checks

# Structural patterns, safe to write down because they name a shape, not a person:
for pat in '/Users/' '/home/[a-z]' \
           '(TOKEN|SECRET|PASSWORD|API_?KEY)' 'sk-[A-Za-z0-9]{8}' 'ghp_|gho_|github_pat_' \
           'AKIA[0-9A-Z]{16}' 'Bearer ' 'xox[baprs]-' '-----BEGIN' \
           '[0-9]{1,3}(\.[0-9]{1,3}){3}' '@[a-z0-9.-]+\.(com|io|net|org)' \
           '\.internal|\.corp|\.local\b'; do
  grep -nEi -e "$pat" "$A" "$B" || true
done
```

**Result: clean.** The single hit was the English word "token" in two comments about path
parsing — a false positive. No absolute machine paths, no usernames, no credentials, no
internal hostnames, no IPs.

The full environment surface the two hooks read:

```bash
grep -ohE '\$\{?[A-Za-z_][A-Za-z0-9_]*\}?' "$A" "$B" | sort -u
```

```
$HOME  $PWD}  $input  $pattern  $rc  $reason  $repo  ${CLAUDE_PROJECT_DIR  ${HOME}  ${PROD_PATTERNS
```

Everything machine-specific is resolved **at runtime** — `git rev-parse --show-toplevel` for the
repo, `$HOME` for the home directory, `${CLAUDE_PROJECT_DIR:-$PWD}` for the project. **No
parameterization work is required**; the files are already portable as written.

`.claude/settings.json` is likewise free of personal data and fully parameterized (same sweep,
13966 bytes, control `hooks`→56 hits; only hits were the filename `secret-detector.sh` and the
deny rule `Read(./secrets/**)`).

---

## 3. The repo already has a convention for exactly this

This is the decisive section, and it cuts both ways.

### 3.1 The pattern: canonical script tracked, runtime location untracked, installer bridges

`scripts/install-hooks.sh` is tracked, and its header states the principle outright:

> git hooks live in `.git/hooks/` and are NOT tracked by git, so the committed, reviewable
> artifact is this installer plus the scripts it points at.

It symlinks `scripts/pre-push-gate.sh` → `.git/hooks/pre-push` and
`scripts/license-gate-precommit.sh` → `.git/hooks/pre-commit`, idempotently, backing up any
pre-existing regular file. ADR-0043 (`docs/decisions/0043-local-first-ci-gates-git-hooks.md`)
records the decision and, importantly, **accepts the trade-off**: the gate is opt-in (someone
must run the installer) and bypassable, "a fast first line, not a server-enforced gate."

There is no `.githooks/` and `core.hooksPath` is unset — the symlink-from-`scripts/` approach is
the only convention in play. `scripts/` is flat (no subdirectories) with kebab-case names.

### 3.2 But `.claude/` is ignored deliberately, and for a *license* reason

`.gitignore:1-17` is not an accident. It carries a written rationale:

> the agent tooling — tooling dev-time (FSL-1.1-MIT), NO forma parte del producto. Aisotropy es un
> producto soberano MIT/Apache-2.0: mantenemos el tooling FSL fuera de la historia de git.

and, for the `.codex/`/`.agents/` projections:

> Si el canónico no se trackea, la proyección tampoco: de lo contrario el repo público
> publicaría por la puerta de atrás la config de agentes que el resto del repo deliberadamente
> no publica.

```bash
git check-ignore -v .claude/hooks/block-destructive-bash.sh
```

```
.gitignore:8:.claude/	.claude/hooks/block-destructive-bash.sh
```

**This is the crux of the whole question.** The stated reason for ignoring `.claude/` is that
the agent tooling tooling is FSL-1.1-MIT and must stay out of an MIT/Apache-2.0 product's history.
That reason is sound, and it covers 37 of the 38 registered hooks. It **does not** cover
`block-destructive-bash.sh`, which is not COS tooling — it is project-authored, its header
documents an aisotropy-specific incident, and it appears nowhere in the COS hook set. That file
is **collateral** of a rule aimed at something else:

```bash
ls .agent-tooling/hooks/cos/ | grep -E 'destructive|prod-url'
```

```
(absent — the two .claude/ hooks are not part of the COS hook set)
```

The repo's own license policy (`.claude/rules/cos/license-policy.md`) blocks FSL for product
code. Versioning the COS hooks would violate it. Versioning one MIT-compatible,
project-authored guard does not.

---

## 4. The real risk of not versioning

Distinguishing "protection absent" from "protection provided elsewhere", as asked.

```bash
for p in .claude .codex .agents .agent-tooling agent-tooling.yaml; do
  printf '%-18s tracked: %s\n' "$p" "$(git ls-files "$p" | wc -l)"
done
```

```
.claude            tracked: 0
.codex             tracked: 0
.agents            tracked: 0
.agent-tooling      tracked: 0
agent-tooling.yaml  tracked: 0
```

And nothing tracked substitutes:

```bash
git grep -lE 'tool_input|PreToolUse' -- scripts/
```

```
(none — no tracked script implements the agent-hook protocol)
```

The five tracked scripts that match `rm -rf` all use it in their own cleanup traps
(`trap 'rm -rf "$SCRATCH"' EXIT` and similar), not as guards.

**So a fresh clone receives:** no agent hooks, no `settings.json`, therefore no
`block-destructive-bash` guard *and* none of the 10 `Read()` deny rules protecting `.env`,
`*.key`, `*.pem` and `.ssh/**` from being read by an agent.

**Is it provided elsewhere?** Partly, and the honest answer is conditional:

- Claude Code's built-in permission prompting asks before running Bash commands. In a default
  session that is a real, harness-provided substitute — a human sees the command first.
- **This very session runs in bypass-permissions mode**, in which those prompts do not fire. In
  that mode `block-destructive-bash.sh` is the only thing standing between a sub-agent and
  `rm -rf ~/.goenv` — which is precisely the incident its header cites.

**Not evaluated:** I did not empirically test harness behaviour across permission modes; that
claim rests on the mode declared in this session's own system prompt, not on an experiment I
ran. What I *can* state from evidence is that the repo contributes zero protection to a clone,
and that the protection that exists on this machine works.

The risk is therefore narrow but real, and it is the same shape as the `calc.wasm` fix in
`0806c51`: **a protection that exists only on the machine where it already was.** The severity
is bounded by the harness's default prompting; it is unbounded in bypass mode.

---

## 5. Recommendation

**Version one file, via the existing ADR-0043 pattern. Change nothing else.**

### 5.1 Do: `scripts/agent-hook-block-destructive-bash.sh` + installer support

Rationale, against the alternatives:

| Option | Verdict |
|---|---|
| `!.claude/hooks/` negation in `.gitignore` | **Rejected.** Re-opens a directory whose exclusion has a written license rationale, and invites the next file dropped in `.claude/hooks/` to be published by default. The `.gitignore:10-15` comment warns against exactly this back-door publication. |
| Version `.claude/settings.json` too | **Rejected.** 37 of its 38 entries reference `.agent-tooling/` paths. Committing it publishes the FSL tooling's hook inventory by name and creates dangling references in any clone that does not install COS. |
| Version `block-prod-urls.sh` | **Rejected.** It is inert (rc=0 on a production URL) and wired to nothing. Committing a no-op guard manufactures the illusion of coverage — the failure this repo just fixed in `8de9ae1`. Fill in its patterns and wire it, or delete it; do not version it as-is. |
| **Canonical script in `scripts/`, installer symlinks it in** | **Adopted.** Matches ADR-0043 exactly, leaves `.gitignore` untouched, keeps the license posture intact, and makes the reviewable artifact travel. |

### 5.2 What was changed

- **`scripts/agent-hook-block-destructive-bash.sh`** (new, tracked) — the canonical copy.
  Byte-identical to the live hook at migration time (`sha256 921b1492…845`) apart from a
  prepended provenance header. Verified free of machine-local data by the §2 sweep with controls.
- **`scripts/install-hooks.sh`** (modified) — gains `install_agent_hook`, mirroring the existing
  `install_hook`: creates the harness dir, symlinks the canonical script into
  `.claude/hooks/` and `.codex/hooks/`, is idempotent, and backs up a pre-existing regular file
  rather than clobbering it.

Deliberately **not** automated: the installer **prints** the `settings.json` snippet instead of
editing that file, because the live `settings.json` also registers 37 COS hooks and a patcher
that mangles it would be worse than the gap it closes. Wiring stays a conscious, one-line manual
step, and the installer says so loudly (`NOT WIRED — the agent hook is installed but nothing
calls it yet`).

### 5.3 Verification performed

In a throwaway git repo (not the worktree, not the main checkout):

| check | result |
|---|---|
| fresh install creates both symlinks | pass |
| second run is idempotent | pass (`already points at … nothing to do`) |
| pre-existing regular file backed up, content preserved | pass (`.bak.20260821110741`, contents intact) |
| guard still functions **through the symlink** | pass — rc=2 out-of-repo, rc=0 in-repo, rc=0 `/tmp` |
| unwired state announced | pass |
| main checkout untouched | pass — `git status` clean, original mtimes, no new symlinks, no `.bak`, no leaked script |

`bash -n` passes on both scripts. **Not run:** `go test ./...`, `gofmt`, or any build — the
machine was DEGRADED (~114 MB free, swap 92%) and this change touches no Go code.

### 5.4 Follow-ups for the operator (not done here)

1. **Wire it after installing** — the symlink is inert until `settings.json` gains the
   `PreToolUse`/`Bash` entry the installer prints. On this machine that entry already exists and
   points at `.claude/hooks/block-destructive-bash.sh`, so running the installer here converts a
   regular file into a symlink at the same path and the wiring keeps working unchanged.
2. **Decide `block-prod-urls.sh`'s fate** — inert and unreferenced since 2026-07-08. Either fill
   in `PROD_PATTERNS` and wire it, or delete it. It currently exists only as a name.
3. **The 10 `Read()` deny rules still do not travel.** They are harness-native, not a hook, so
   this change does not address them. If those matter for a clone, that is a separate decision
   about whether a *minimal* `settings.json` (deny rules + the one project hook, no COS entries)
   should be tracked.
4. **Worktrees do not get the hook.** `install_agent_hook` writes to the toplevel of whatever
   checkout it runs in, while the harness resolves `${CLAUDE_PROJECT_DIR:-$PWD}`. In a worktree
   session the guard is whatever the *project dir* has, not the worktree. Documented, not fixed.

---

## Appendix — commands used

```bash
# hook inventory
find . -name .git -prune -o -path ./external -prune -o -type d -name hooks -print
find . -name .git -prune -o -path ./external -prune -o -type f -name '*.sh' -print | wc -l
git ls-files '*.sh' | wc -l
shasum -a 256 .claude/hooks/*.sh .codex/hooks/*.sh

# what invokes what
python3 -c '
import json; d=json.load(open(".claude/settings.json"))
n=0
for ev,es in d.get("hooks",{}).items():
    for e in es:
        for h in e.get("hooks",[]):
            n+=1; print(f"[{ev}] matcher={e.get(\"matcher\",\"\")}\n    {h.get(\"command\",\"\")}\n")
print("TOTAL:", n)'
grep -c '\.agent-tooling/hooks' .claude/settings.json
grep -c '\.claude/hooks'       .claude/settings.json

# behaviour (on copies)
printf '{"tool_input":{"command":"%s"}}' "$CMD" | bash ./block-destructive-bash.sh; echo $?

# ignore-status and convention
git check-ignore -v .claude/hooks/*.sh .claude/settings.json
git grep -lE 'tool_input|PreToolUse' -- scripts/
git config --get core.hooksPath

# the grep caveat that shaped this whole report
declare -f grep | grep -o -- '--ignore-files'
grep -rl 'block-destructive-bash' .                    # nothing
grep -rl --no-ignore-files 'block-destructive-bash' .  # finds it
```

## References

- [ADR-0043 — Local-first CI gates: git hooks over paid GitHub Actions](../decisions/0043-local-first-ci-gates-git-hooks.md)
- `scripts/install-hooks.sh`, `scripts/agent-hook-block-destructive-bash.sh`
- `.gitignore:1-17` (the license rationale for excluding `.claude/`, `.agent-tooling/`, `.codex/`, `.agents/`)
- `docs/architecture/prune-evidence-gate.md` — "a green from a tool that could not run its checks is not evidence of absence", the rule §0.2 re-learned the hard way
- `8de9ae1` — the guard that declared its blind spot where only its author would read it
