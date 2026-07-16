# task-fanout — concurrent sandboxed task agents (parallel key-brokering) demo

A minimal, runnable demonstration of Aisotropy's **concurrent sandboxed task
agents** ([ADR-0037](../../docs/decisions/0037-concurrent-sandboxed-task-agents.md)):
run **N agents in parallel**, each doing real work with real hands — shell,
files, git — inside its **own** isolated Docker container, **while the provider
API key never enters any of those containers.**

It builds directly on the single-task
[task-agent](../task-agent/README.md) demo ([ADR-0036](../../docs/decisions/0036-sandboxed-task-agent-key-brokering.md)):
`RunTasks` is a fan-out over `RunTask`, so every isolation guarantee of one task
holds **per task** under concurrency. This example is a separate Go module (it
`replace`s the repo root) that builds on Aisotropy's **public surface only** —
`aisotropy.New()`, `app.RunTasks(...)`, `aisotropy.RunTasksOptions`, and
`aisotropy.SandboxToolScopes()`. It cannot import `internal/…`, exactly like a
third-party consumer.

## The property this shows: N containers, still zero API keys

Aisotropy is a **key-BROKER, not a key-forwarder** — and that stays true under a
parallel fan-out.

```
                         HOST process (this program)
   ┌──────────────────────────────────────────────────────────────────┐
   │  the "brains" (host-side): internal/llm makes every LLM call here  │
   │  → spends ANTHROPIC_API_KEY host-side, N times, in parallel        │
   │  → the KEY LIVES HERE, and NEVER LEAVES, for any task              │
   └───────┬───────────────────┬───────────────────┬──────────────────┘
           │ hands only        │ hands only        │ hands only
           ▼                   ▼                   ▼
   ┌───────────────┐   ┌───────────────┐   ┌───────────────┐
   │ container #1  │   │ container #2  │   │ container #3  │   ← each isolated:
   │ own worktree  │   │ own worktree  │   │ own worktree  │     own branch,
   │ run_shell …   │   │ run_shell …   │   │ run_shell …   │     own container,
   │ NO API KEY    │   │ NO API KEY    │   │ NO API KEY    │     NO API KEY
   └───────────────┘   └───────────────┘   └───────────────┘
```

Concurrency changes **nothing** about isolation: each task still goes through the
same `RunTask`, so each gets its own worktree, its own container, and the
key-broker invariant. The tasks are isolated **by construction** because each
`RunTask` already is. This is the exact **inverse** of running N
coding-CLIs-in-a-box (sandcastle, Claude Code, Codex, Aider, Cursor, Gemini CLI,
OpenCode) in parallel — that would mean N copies of the raw provider key, one
inside each box. Here the key was never in any of the rooms.

## What the program does

1. Creates a **throwaway git repo** in a temp dir (the demo never touches a real
   checkout). All three tasks target **this same repo**, which is what exercises
   the per-repo merge serialization.
2. Builds the runtime with `aisotropy.New()` and **no credentials**, so it uses
   the keyless **stub** model.
3. Calls `app.RunTasks(ctx, reqs, RunTasksOptions{Concurrency: 2, OnEvent: …})`
   with **three** `TaskRequest`s (each creating a disjoint file). With
   `Concurrency: 2`, at most two containers boot at once; the third takes a slot
   as one frees.
4. Prints a **multiplexed live event stream** — `started` / `step` / `shell` /
   `commit` / `done` / `error`, each tagged with its task index — as the
   concurrent tasks interleave.
5. Prints the per-task outcomes in input order.

## Run it

```bash
cd examples/task-fanout
go run .
```

Requirements:

- **Docker** — the sandbox provider shells out to the `docker` CLI (no Docker Go
  SDK). Without a running Docker daemon, each task fails at sandbox creation;
  `RunTasks` isolates those failures onto the individual outcomes (see below) and
  the program prints them per task.
- **git** — used to build the throwaway repo here and by `RunTask`'s worktree
  plumbing.

## Two properties this demo makes concrete

**Per-task error isolation.** A task that fails sets `Err` on its **own**
`TaskOutcome`; the batch **continues** and still returns every other outcome. An
individual task failure is never the top-level error. The top-level error is
non-nil **only** for a whole-batch failure — the passed `context` being cancelled
(e.g. a timeout), which also stops scheduling not-yet-started tasks.

**The merge race, handled.** All three tasks merge their branch into the same
repo's HEAD. `RunTasks` serializes just the per-repo git-metadata steps (worktree
add and merge-to-HEAD) behind a per-repo lock, so merges apply one at a time and a
failed merge is aborted rather than left corrupting HEAD for the next task.
Everything expensive — the containers, the host-side loops, the in-worktree
commits — runs fully in parallel and ungated. Tasks on **different** repos share
no lock and stay fully parallel.

## Honest note: the stub vs. a real model

With the keyless stub, every task produces a canned response and **no actionable
tool call**. So each task completes gracefully with **zero commits** — the fan-out
exercises the full concurrent plumbing (three parallel worktrees → three parallel
containers → three host-side loops → serialized merge-or-discard) and proves every
container boots **without a key** under concurrency, but the agents do not actually
edit files, because there is no real model deciding to. The program says so
explicitly in its output.

Point Aisotropy at a real provider and the same `RunTasks` call drives three real
models **in parallel** that will use their hands to make edits and commit them —
each merged into the shared HEAD one at a time, still without any key ever
entering any container:

```bash
export OPENROUTER_API_KEY=sk-or-...   # or OPENAI_API_KEY / ANTHROPIC_API_KEY
go run .
```

Provider selection follows the same precedence as the rest of Aisotropy:
`OPENROUTER_API_KEY` → `OPENAI_API_KEY` → `ANTHROPIC_API_KEY` → keyless stub.

## What ships in v1 (and what does not)

**v1 (this demo exercises it):** a bounded-concurrency fan-out over `RunTask`;
per-task isolation (own worktree, own container, key-broker invariant per box);
per-task error isolation; a per-repo lock serializing worktree-add and
merge-to-HEAD with abort-on-conflict; and a live, index-tagged multiplexed event
stream (`started` / `step` / `shell` / `commit` / `done` / `error`), emission
serialized so `OnEvent` need not be goroutine-safe.

**Named follow-ons (NOT in v1):** automatic cross-branch conflict **resolution**
(a real content conflict surfaces as that task's `Err` with HEAD aborted-clean —
resolution is not attempted); an HTTP/SSE route for `RunTasks` (façade +
callback only, matching `RunTask`'s shape); and everything `RunTask` itself defers
in [ADR-0036](../../docs/decisions/0036-sandboxed-task-agent-key-brokering.md)
(additional providers, richer hands, multi-repo primitives). Running the coding
CLIs inside the box — in parallel or otherwise — is explicitly **not** Aisotropy's
model; that would reintroduce the key-in-sandbox problem this primitive exists to
invert. See
[ADR-0037](../../docs/decisions/0037-concurrent-sandboxed-task-agents.md) for the
full scope and rationale.
