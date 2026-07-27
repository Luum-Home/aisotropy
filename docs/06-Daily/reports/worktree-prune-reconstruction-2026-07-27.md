---
type: Report
title: "Surgical reconstruction — what the pruned worktrees, branches and stash actually held"
description: A byte-level accounting of the two agent worktrees, four branches and one safety stash removed on 2026-07-27, proving via blob-ancestry (did this exact content ever exist in main's history?) that 483 differing files were all exact earlier states of main and that the single unaccounted path was a 50.9MB compiled binary. Includes the recovery SHAs and the reusable audit scripts the investigation produced.
tags: [report, git, worktrees, forensics, evidence, cleanup]
timestamp: 2026-07-27T00:00:00Z
---

# Surgical reconstruction — the pruned worktrees, branches and stash

## Why this exists

Two agent worktrees, four `worktree-agent-*` branches and (afterwards) one safety stash were
deleted on 2026-07-27. The deletion was authorised on the strength of a **sampled** verification:
three files diffed byte-for-byte, ~30 of 46 differing lines read, three of 158 changed files
spot-checked for later history. That reasoning reached a conclusion larger than its evidence —
the same failure class this repo already documents for code claims taken from doc reads
(`.claude/rules/prune-evidence-gate.md`).

This report replaces the sample with an exhaustive accounting, performed **after** the deletion
(everything remained recoverable: the commit is still in the object database, the stash SHA was
recorded before dropping).

## The method: blob ancestry, not line counting

Counting "lines the base does not have" cannot distinguish the two cases that matter:

| Case | Symptom | Truth |
|---|---|---|
| Stale branch | N lines base lacks | base **rewrote** the file since; the lines are an OLD version |
| Unmerged work | N lines base lacks | the work **never landed**; the lines are real and would be lost |

Both look identical in a diff. The distinguishing question is exact and answerable:

> **Did this exact blob ever exist in `main`'s history for this path?**

If yes, the file is an earlier state `main` has evolved past — accounted for. If never, a human
must read it. This is implemented in [`scripts/blob-ancestry-audit.sh`](../../../scripts/blob-ancestry-audit.sh),
which walks every commit in `main` touching each path and compares blob SHAs. It is also why
`git cherry` is the wrong tool here: it compares **patch-ids**, so work that landed via a
different parent (rebase, re-commit) reports as "not applied" while being byte-identical — as
happened with the graph commit below.

## Findings

### 1. Branch `worktree-agent-a723f81a920d3aae0` @ `9634e4f` (deleted)

*"feat(graph): multi-agent graph orchestration (ADR-0060)"*, committed 2026-07-17.

| Classification | Count |
|---|---|
| ANCESTOR (exact earlier state of main) | **244** |
| UNIQUE (blob never in main's history) | **0** |
| ABSENT (path not in main at all) | **1** |

The single ABSENT path is `examples/graphbot/graphbot` — a **50.9 MB compiled binary**, a build
artifact. Its source (`main.go`, `README.md`, `go.mod`, `go.sum`) is complete in `main`, and the
binary is correctly excluded by `.gitignore`; it should never have entered a commit at all.

The commit's own work landed on `main` independently as `1fd59ee`, which is why `git cherry`
reported it as unapplied while `internal/graph/graph.go`, `internal/graph/exec.go` and
`aisotropy/graph.go` are byte-identical between the two.

### 2. Safety stash (`stash@{0}`, from worktree `agent-a9bcfcf3abe979e30`)

Uncommitted "reasoning" work from 2026-07-17, taken as a snapshot immediately before removing
the worktree.

| Half | ANCESTOR | UNIQUE | ABSENT |
|---|---|---|---|
| tracked (`stash@{0}`) | 237 | **0** | 0 |
| untracked (`stash@{0}^3`) | 2 | **0** | 0 |

The 46 lines that the first pass flagged as "unique to the worktree" are confirmed, by blob
ancestry, to be exact earlier states of files `main` has since rewritten. The decisive example,
readable by eye: the worktree's `agentloop.Decision` is `{Tool, Args}`; `main`'s is
`{Tool, Args, Answer}` — `main` is a **superset**, the worktree a photograph of "before".

The work itself is committed on `main` (ADR-0061, accepted; `internal/llm/reasoning.go` present
and byte-identical to the worktree copy).

### 3. Branches `-a90a12f0be9219d5f`, `-a9a32b9e7dbf96c50`, `-a9bcfcf3abe979e30`

All three pointed at `9ef786a`, already an ancestor of `main` — zero commits outside it.

## Verdict

**483 differing files across all three artifacts; 0 blobs that never existed in `main`'s history;
1 path absent from `main`, and it is a compiled binary.** Nothing was lost. `main` is complete.

## Recovery information (kept deliberately)

Should any of this ever need to be resurrected before git's garbage collection reclaims it:

- Deleted branch commit: `9634e4f046122ff554e554775c540a8d0d0ada82`
  (`git branch <name> 9634e4f`)
- Safety stash commit: `5404913e0e2a6e45f9cf848107b8777a4c75f0e3`
  (`git stash store 5404913e0e2a6e45f9cf848107b8777a4c75f0e3`)

Both are unreferenced objects after the prune; they survive until `git gc` runs.

## What this produced (so the next prune is cheap)

- [`scripts/worktree-audit.sh`](../../../scripts/worktree-audit.sh) — the fast screen: which
  worktrees, branches and stashes hold anything the base lacks. Reports; never absolves.
- [`scripts/blob-ancestry-audit.sh`](../../../scripts/blob-ancestry-audit.sh) — the deep answer
  for whatever the screen flags: per-file HEAD / ANCESTOR / UNIQUE / ABSENT.

Both are read-only and follow the repo's guard exit-code convention (0 clean / 1 findings /
2 cannot evaluate). The standing lesson is the one the incident actually taught: **a sample that
supports a conclusion is not the same as evidence for it** — and when the action is irreversible,
only the exhaustive form counts.
