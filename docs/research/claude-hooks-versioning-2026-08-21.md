---
type: research
title: "Why one agent hook travels with the repo and the rest of .claude/ does not"
description: Investigation into whether the agent-governing hooks under .claude/ should travel with the repo. Finds exactly one project-authored hook that is genuinely protective and measurably works, one that is inert and wired to nothing, and no tracked substitute for either. Recommends versioning that single hook through the ADR-0043 installer pattern and explicitly recommends against un-ignoring .claude/, which is the operator's local agent config. Also records two method corrections, the load-bearing one being a sweep that reported clean fourteen times without opening a file.
tags: [research, hooks, agents, gitignore, supply-chain, adr-0043, tooling]
timestamp: 2026-08-21T00:00:00Z
---

# Why one agent hook travels with the repo and the rest of `.claude/` does not

**Base commit:** `1ec089c`. **Method:** read-only inspection of the main checkout, analysis
and changes in an isolated worktree. No builds or test suites were run (machine was at
`resource-guard.sh` rc=1 DEGRADED, ~114 MB free).

**Scope note (same day, later).** This report originally surveyed the whole of `.claude/`, most
of which was third-party agent tooling that is no longer part of the repo's picture. It was
trimmed to what it still has to support: the evidence for why
`scripts/agent-hook-block-destructive-bash.sh` exists and is tracked. Findings were removed, not
rewritten; the numbers that remain were re-derived against the tree of 2026-08-21 unless marked
otherwise.

**Verdict, up front:** version **one** file — `block-destructive-bash.sh` — through the
installer pattern the repo already uses for git hooks (ADR-0043). Do **not** un-ignore
`.claude/`: it is the operator's local agent config, not a repo artifact. Do **not** version
`block-prod-urls.sh` or `settings.json`. The reasons are below, each with the command that
produced it.

---

## 0. Two corrections to the premises this investigation started from

Both were handed to me as fact. Both are wrong, and the second one nearly produced a false
"all clean" in the section that matters most.

### 0.1 An automatic guidance note described a mechanism this repo does not have

A note injected into the session asserted that `.claude/settings.json` is *generated* here, that
a canonical registry file holds the hook inventory, and that the fix is to re-run a
profile-applying script. None of those existed:

```bash
for f in scripts/_lib/settings-driver-claude-code.sh scripts/apply-efficiency-profile.sh; do
  [ -e "$f" ] && echo "EXISTS $f" || echo "ABSENT $f"
done
git ls-files .claude | wc -l
```

```
ABSENT scripts/_lib/settings-driver-claude-code.sh
ABSENT scripts/apply-efficiency-profile.sh
0
```

**Treated as refuted.** The consequence that *is* load-bearing: `.claude/settings.json` is
hand-maintained and untracked, so the whole chain — the wiring *and* the hooks it points at —
exists only on the operator's disk.

The general lesson: an automatic note is a hypothesis about the repo, not a fact about it, and
it costs one command to check.

### 0.2 A sweep printed `clean` fourteen times without opening a file

`grep` in this session is not the system binary. It is a shell function that execs `ugrep` with
`--ignore-files`, which means **recursive greps honour `.gitignore`** — and `.gitignore:13` is
`.claude/`. So a repo-root sweep reports nothing for the exact files being audited:

```bash
declare -f grep | grep -o -- '--ignore-files'          # → --ignore-files
grep -rl 'block-destructive-bash' .                    # → (nothing)
grep -rl --no-ignore-files 'block-destructive-bash' .  # → .claude/hooks/… , .claude/settings.json
```

This is not academic. My first sensitive-data sweep printed `clean` for all 14 patterns. A
positive control showed why:

```bash
wc -c .claude/hooks/block-destructive-bash.sh   # → open: No such file or directory
```

The sweep had been passed a relative path from the wrong working directory and **never opened
the files**. Fourteen lines of `clean` meant "I did not look." Every sweep below therefore
carries a positive control (a pattern that must hit) and a byte count proving the file opened.

**A verification tool that could not look must never absolve** — the same rule
`prune-guard.sh` learned about nested modules. This is the most reusable finding in this report.

---

## 1. Inventory: what is actually there

### 1.1 Two project-authored agent hooks, neither tracked

`.claude/hooks/` holds two project-authored hooks, `block-destructive-bash.sh` and
`block-prod-urls.sh`. Neither travels:

```bash
git check-ignore -v .claude/hooks/block-destructive-bash.sh
```

```
.gitignore:13:.claude/	.claude/hooks/block-destructive-bash.sh
```

`.codex/hooks/` carries a byte-identical twin of each. Since this report's change that identity
is **structural rather than coincidental**: the installer symlinks both harness directories at
the same canonical file (`scripts/install-hooks.sh:101-103`), so they cannot drift.

Git hooks are a separate, already-solved case: `.git/hooks/` holds two symlinks into `scripts/`
per ADR-0043.

`.claude/settings.json` registers the wiring. It has exactly two top-level keys, `hooks` and
`permissions`, the latter being 10 `Read(...)` deny rules for `.env`, `*.key`, `*.pem`,
`.ssh/**` and similar. **`block-prod-urls.sh` is registered nowhere.** It is an orphan.

### 1.2 What each hook does — measured, not read

Behaviour matrix, re-run on 2026-08-21 against the **tracked canonical script**:

```bash
G=scripts/agent-hook-block-destructive-bash.sh
printf '{"tool_input":{"command":"%s"}}' "$CMD" | bash "$G" >/dev/null 2>&1; echo $?
```

| input | rc | meaning |
|---|---:|---|
| a recursive force-delete of a dotdir under `$HOME` | 2 | BLOCK — the motivating incident |
| the same, targeting another user's `Documents` | 2 | BLOCK — home path |
| the same, targeting a system path under `/etc` | 2 | BLOCK — system path |
| the same, targeting `./build` | 0 | allow — in-repo |
| the same, targeting a path under `/tmp` | 0 | allow — temp |
| `ls -la` | 0 | allow |

**3/3 dangerous blocked, 3/3 legitimate allowed.** `block-destructive-bash.sh` is real, working
protection. (The inputs are described rather than quoted here for the reason §1.3 explains: a
document containing them as literal text is itself blocked when written from a shell.)

`block-prod-urls.sh`, measured the same way on the report date, returned **rc=0 on a production
URL**. It ships with every pattern commented out and returns before reading stdin; its own
header calls the patterns placeholders to "fill in … then wire this into settings.json … if
desired." Neither was ever done. It is protection that exists only as a filename — the same
failure `1ec089c` fixed elsewhere in this repo.

### 1.3 The known false positive, now pinned to a precise shape

The guard matches on command **text**, so a command that merely *contains* a destructive string
as data can be blocked. This is documented behaviour — the header calls the script "a heuristic
defense-in-depth guard, not a sandbox" — but it fires often enough to be worth pinning down. It
has now blocked several of this investigation's own commands, including the heredoc that would
have written this very section: the paragraph's example strings were read as the command's own
intent.

The mechanism is narrower than "it matches text". `CMD_START` in the script treats `|` as a
shell command separator, so a `|` **inside a quoted `sed` substitution expression** puts the
following words at what the guard reads as a command position. Whitespace-splitting the same
expression then manufactures a path-shaped token that resolves outside the repo:

```bash
# blocked (rc=2) — reason: "targets a path OUTSIDE the repo: /old/cache|make"
sed -i '' 's|<recursive-force-delete> /old/cache|make clean|' README.md

# identical intent, '/' delimiter — allowed (rc=0)
sed -i '' 's/<recursive-force-delete> \/old\/cache/make clean/' README.md

# the verb quoted as a search string — allowed (rc=0)
grep -n '<recursive-force-delete> /var/cache' README.md
```

So the trigger is the **delimiter choice**, not the mention of the verb: `|` re-enters the
command grammar, `/` does not. Worth knowing before anyone widens the patterns, and worth
knowing when the guard blocks a command that is obviously inert — the reason string naming a
nonsense path like `/old/cache|make` is the tell.

---

## 2. Do the hooks contain anything that must not travel?

Sweep with controls proving it read the bytes. Re-run 2026-08-21 against the tracked copy:

```bash
A=scripts/agent-hook-block-destructive-bash.sh
wc -c "$A"                                   # 5056            ← file opened
grep -cE 'block-destructive' "$A"            # 3               ← control hits
grep -c  'zzz-nonexistent-zzz' "$A"          # 0               ← control misses
# The operator-specific tokens (given name, surname, mail handle, client and
# org names) are NOT inlined here on purpose: this report is tracked, and
# writing them down would put in the repo the very strings the sweep exists to
# keep out. They live in .private-sweep-names (gitignored) and in the check
# list of scripts/pre-publication-sweep.sh, which is where a reader should look.

# Structural patterns, safe to write down because they name a shape, not a person:
for pat in '/Users/' '/home/[a-z]' '(TOKEN|SECRET|PASSWORD|API_?KEY)' \
           'sk-[A-Za-z0-9]{8}' 'AKIA[0-9A-Z]{16}' 'xox[baprs]-' '-----BEGIN' \
           '[0-9]{1,3}(\.[0-9]{1,3}){3}' '\.internal|\.corp'; do
  printf '%-38s %s\n' "$pat" "$(grep -cEi -e "$pat" "$A")"
done
```

**Result: clean.** Every structural pattern returned 0 except
`(TOKEN|SECRET|PASSWORD|API_?KEY)` → 2, which resolves to the English word *token* in two
comments about path parsing (lines 75-76) — a false positive. No absolute machine paths, no
usernames, no credentials, no internal hostnames, no IPs.

The full environment surface the script reads:

```bash
grep -ohE '\$\{?[A-Za-z_][A-Za-z0-9_]*\}?' "$A" | sort -u
```

```
$HOME  $PWD}  $input  $rc  $reason  $repo  ${CLAUDE_PROJECT_DIR  ${HOME}
```

Everything machine-specific is resolved **at runtime** — `git rev-parse --show-toplevel` for the
repo, `$HOME` for the home directory, `${CLAUDE_PROJECT_DIR:-$PWD}` for the project. **No
parameterization work was required**; the file was already portable as written.

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

### 3.2 But `.claude/` stays ignored, deliberately

`.claude/` is the **operator's own local agent config**: rules, instructions, hooks and a
hand-maintained `settings.json`, varying by machine, by harness and by personal preference. It
describes how someone drives their tools, not how the published runtime behaves, so it stays out
of the repo's history — and `.codex/`, the projection of the same content, with it.

That exclusion is aimed at configuration, and `block-destructive-bash.sh` is **collateral**: it
is not configuration, it is a project-authored guard whose header documents an aisotropy-specific
incident. Keeping it out of a clone protects nothing and costs the clone a real defense. Hence
the split this report recommends — the guard travels through `scripts/`, the config does not
travel at all.

---

## 4. The real risk of not versioning

Distinguishing "protection absent" from "protection provided elsewhere", as asked.

```bash
for p in .claude .codex; do printf '%-10s tracked: %s\n' "$p" "$(git ls-files "$p" | wc -l)"; done
git grep -lE 'tool_input|PreToolUse' -- scripts/
```

```
.claude    tracked: 0
.codex     tracked: 0
```

Before this report's change, that second command returned nothing — **no tracked script
implemented the agent-hook protocol at all.** It now returns the canonical guard and its
installer. The five tracked scripts that match a recursive force-delete all use it in their own
cleanup traps (an `EXIT` trap removing a scratch dir, and similar), not as guards.

**So a fresh clone received:** no agent hooks, no `settings.json`, therefore no
`block-destructive-bash` guard *and* none of the 10 `Read()` deny rules protecting `.env`,
`*.key`, `*.pem` and `.ssh/**` from being read by an agent.

**Is it provided elsewhere?** Partly, and the honest answer is conditional:

- Claude Code's built-in permission prompting asks before running Bash commands. In a default
  session that is a real, harness-provided substitute — a human sees the command first.
- **This very session runs in bypass-permissions mode**, in which those prompts do not fire. In
  that mode `block-destructive-bash.sh` is the only thing standing between a sub-agent and the
  out-of-scope home-directory destroy its header cites as the motivating incident.

**Not evaluated:** I did not empirically test harness behaviour across permission modes; that
claim rests on the mode declared in this session's own system prompt, not on an experiment I
ran. What I *can* state from evidence is that the repo contributed zero protection to a clone,
and that the protection that exists on this machine works.

The risk is therefore narrow but real, and it is the same shape as the `calc.wasm` fix in
`350a837`: **a protection that exists only on the machine where it already was.** The severity
is bounded by the harness's default prompting; it is unbounded in bypass mode.

---

## 5. Recommendation

**Version one file, via the existing ADR-0043 pattern. Change nothing else.**

### 5.1 Do: `scripts/agent-hook-block-destructive-bash.sh` + installer support

Rationale, against the alternatives:

| Option | Verdict |
|---|---|
| `!.claude/hooks/` negation in `.gitignore` | **Rejected.** Re-opens a directory that is deliberately excluded as local agent config, and invites the next file dropped in `.claude/hooks/` to be published by default. |
| Version `.claude/settings.json` too | **Rejected.** It is hand-maintained, machine- and harness-specific local config; committing it publishes one operator's setup as if it were the repo's, and its entries reference paths that need not exist in any other clone. |
| Version `block-prod-urls.sh` | **Rejected.** It is inert (rc=0 on a production URL) and wired to nothing. Committing a no-op guard manufactures the illusion of coverage — the failure this repo just fixed in `1ec089c`. Fill in its patterns and wire it, or delete it; do not version it as-is. |
| **Canonical script in `scripts/`, installer symlinks it in** | **Adopted.** Matches ADR-0043 exactly, leaves the `.claude/` exclusion intact, and makes the reviewable artifact travel. |

### 5.2 What was changed

- **`scripts/agent-hook-block-destructive-bash.sh`** (new, tracked) — the canonical copy.
  Byte-identical to the live hook at migration time apart from a prepended provenance header.
  Verified free of machine-local data by the §2 sweep with controls.
- **`scripts/install-hooks.sh`** (modified) — gains `install_agent_hook`, mirroring the existing
  `install_hook`: creates the harness dir, symlinks the canonical script into
  `.claude/hooks/` and `.codex/hooks/`, is idempotent, and backs up a pre-existing regular file
  rather than clobbering it.

Deliberately **not** automated: the installer **prints** the `settings.json` snippet instead of
editing that file. That file is the operator's own local config, hand-maintained and untracked;
a patcher that mangles it would be worse than the gap it closes. Wiring stays a conscious,
one-line manual step, and the installer says so loudly (`NOT WIRED — the agent hook is installed
but nothing calls it yet`).

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
   `PreToolUse`/`Bash` entry the installer prints.
2. **Decide `block-prod-urls.sh`'s fate** — inert and unreferenced since 2026-07-08. Either fill
   in `PROD_PATTERNS` and wire it, or delete it. It currently exists only as a name.
3. **The 10 `Read()` deny rules still do not travel.** They are harness-native, not a hook, so
   this change does not address them. If those matter for a clone, that is a separate decision
   about whether a *minimal* `settings.json` (deny rules plus the one project hook) should be
   tracked.
4. **Worktrees do not get the hook.** `install_agent_hook` writes to the toplevel of whatever
   checkout it runs in, while the harness resolves `${CLAUDE_PROJECT_DIR:-$PWD}`. In a worktree
   session the guard is whatever the *project dir* has, not the worktree. Documented, not fixed.

---

## Appendix — commands used

```bash
# what is tracked, and what the ignore rule is
git ls-files .claude .codex | wc -l
git check-ignore -v .claude/hooks/block-destructive-bash.sh
git ls-files '*.sh' | wc -l                      # 110 tracked shell scripts (2026-08-21)

# behaviour, against the tracked canonical script
printf '{"tool_input":{"command":"%s"}}' "$CMD" \
  | bash scripts/agent-hook-block-destructive-bash.sh; echo $?

# convention already in play
git grep -lE 'tool_input|PreToolUse' -- scripts/
git config --get core.hooksPath                  # (unset)

# the grep caveat that shaped this whole report
declare -f grep | grep -o -- '--ignore-files'
grep -rl 'block-destructive-bash' .                    # nothing
grep -rl --no-ignore-files 'block-destructive-bash' .  # finds it
```

## References

- [ADR-0043 — Local-first CI gates: git hooks over paid GitHub Actions](../decisions/0043-local-first-ci-gates-git-hooks.md)
- `scripts/install-hooks.sh`, `scripts/agent-hook-block-destructive-bash.sh`
- `.gitignore:1-14` (why `.claude/` and `.codex/` are excluded as local agent config)
- `docs/architecture/prune-evidence-gate.md` — "a green from a tool that could not run its checks is not evidence of absence", the rule §0.2 re-learned the hard way
- `1ec089c` — the guard that declared its blind spot where only its author would read it
