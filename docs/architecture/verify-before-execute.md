---
type: Process
title: "Verify-before-execute — a plan is a snapshot, the state is the truth"
description: >-
  The concurrency norm this repository learned on 2026-07-23, when several agent sessions worked the
  same checkout at once: between deciding to do a queued item and doing it, re-verify against CURRENT
  state that it is still needed, still yours, and still executable as planned. Covers queued fixes
  already landed by someone else, scarce shared identifiers like ADR numbers, committing under
  concurrent writers, and heavy local lanes on a loaded machine. This is the tracked canonical;
  .claude/rules/verify-before-execute.md is a harness projection of it and is gitignored.
tags: [architecture, process, concurrency, git, agents, contributing]
timestamp: 2026-08-05T00:00:00Z
---

# Verify-before-execute

> **A plan is a snapshot; the state is the truth.** Between deciding to do a queued work item and
> executing it, re-verify against CURRENT state that it is (a) still needed, (b) still yours, and
> (c) still executable as planned. Under concurrent sessions, plans go stale in minutes.

## Where this came from

2026-07-23, several sessions worked this repository simultaneously. Four incidents, one root cause —
acting on a plan snapshot instead of current state:

1. A queued backfill task was executed by an agent — but a concurrent session had **already landed
   the work** mid-task. The agent checked `HEAD` first, found it done, and **verified instead of
   duplicating**. That save is the pattern this norm names, and it is why the norm is framed as a
   habit rather than a prohibition.
2. Two sessions allocated **the same ADR number**, ten minutes apart, because each read "the next
   free number" once and wrote later. The repair cost four commits; a re-check at write time would
   have cost one `ls`.
3. A file staged for commit **vanished from the diff** mid-session — another session had committed it
   with its own context. Blind staging would have produced an empty or wrong commit.
4. Heavy test lanes were about to pile onto a machine at 93% swap. `scripts/resource-guard.sh` now
   answers that question before the spend.

## The checks, by work item

| Work item | The check — seconds, not minutes |
|---|---|
| A queued fix or backlog task | `git log --oneline -10` plus a `git grep` for the landed symptom. If someone already did it, **verify their work instead of redoing it** and close the item as "landed by `<commit>`, independently verified". |
| Allocating a scarce shared identifier (ADR number, migration index, port, collection name) | Re-enumerate at **write** time counting untracked files too, and once more immediately before the final save. Never trust the number read at plan time. |
| Committing staged or planned paths | `git status --porcelain <paths>` immediately before `git add`. Confirm the diff still exists, is still yours, and contains **only** yours. Path-scoped adds; never `-A` under concurrency. |
| Starting a heavy lane (full test sweep, model inference, docker build) | `scripts/resource-guard.sh`, or wrap the command in `scripts/with-heavy-lane.sh`, which adds a cross-session mutex. Reject or degrade before spending machine resources. |
| Editing a file another session may own | `git status` it. If it is dirty with foreign changes, coordinate — do not blend someone else's in-flight work into your commit. |

Two failure modes are worth naming because neither announces itself. **Absorption**: a `git add -A`
in another session sweeps your staged files into their commit, under their message. The content
survives; the reasoning does not, and the reasoning is what keeps a change alive through a future
cleanup. **Branch diversion**: `HEAD` is a single file in the checkout, so a `git checkout` between
your `git checkout -b` and your `git commit` lands your commit on their branch. Everything passes;
the signal arrives later, disguised as `gh pr create` reporting an empty branch.

Both are avoided completely by not sharing the checkout: `git worktree add` costs one command.

## What this is not

- **Not re-litigating decisions.** What you re-verify is the *state*, not the *mandate*. An approved
  scope stays approved.
- **Not polling.** One check immediately before execution, one more before an irreversible write to a
  shared resource. Not a loop.
- **A verification that finds the work already done is a success**, not wasted effort. Report it as
  such and close the item.

## The family it belongs to

Every discipline in this repository has the same shape — *look at the real state before spending*,
where the spend is credibility, code, tokens, machine resources, or duplicated work:

- [Prune-evidence gate](prune-evidence-gate.md) — a **claim** about code requires executable evidence.
- [Capability-absence claims](capability-absence-claims.md) — a **claim** about a competitor requires
  an ecosystem sweep.
- [Dependency-reuse-first](dependency-reuse-first.md) — **building** on a dependency requires
  inventorying it first.
- [Causal claims about runs](causal-claims-about-runs.md) — **explaining** a failure requires naming
  the observation that would disprove you, and checking it first.
- **This norm** — **executing** a queued item requires re-verifying current state.
- [Machine resource guard](machine-resource-guard.md) — **spending** machine resources requires the
  guard's verdict.
