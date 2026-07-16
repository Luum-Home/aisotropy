---
type: Tutorial
title: "07 — Concurrent tasks"
description: Fan many sandboxed tasks out in parallel with App.RunTasks — per-task isolation holds under concurrency, and per-task failures don't abort the batch.
tags: [tutorial, runtasks, concurrency, fan-out, sandbox]
timestamp: 2026-07-16T00:00:00Z
---

# 07 — Concurrent tasks

**Goal:** run many sandboxed tasks at once — without weakening any of lesson 06's guarantees.

`App.RunTasks` is a bounded parallel fan-out over `RunTask`. It builds *directly on* `RunTask`, so
every per-task guarantee holds under concurrency: each task gets its **own** worktree, its **own**
container, and the **key-broker invariant** (the key is spent host-side, never enters any box).

```go
app := aisotropy.New()
defer func() { _ = app.Close() }()

reqs := []aisotropy.TaskRequest{
	{RepoDir: "./my-repo", Prompt: "Create a.txt with 'alpha', commit it.",   Scopes: aisotropy.SandboxToolScopes()},
	{RepoDir: "./my-repo", Prompt: "Create b.txt with 'bravo', commit it.",   Scopes: aisotropy.SandboxToolScopes()},
	{RepoDir: "./my-repo", Prompt: "Create c.txt with 'charlie', commit it.", Scopes: aisotropy.SandboxToolScopes()},
}

outcomes, err := app.RunTasks(ctx, reqs, aisotropy.RunTasksOptions{
	Concurrency: 2, // at most 2 containers at once; the third takes a slot as one frees
	OnEvent: func(ev aisotropy.TaskEvent) { // live, index-tagged stream across all tasks
		fmt.Printf("[task %d] %s %s\n", ev.Index+1, ev.Kind, ev.Message)
	},
})
if err != nil { // top-level error == WHOLE-BATCH failure (e.g. ctx cancelled), not one task's
	log.Fatal(err)
}
for _, o := range outcomes { // one outcome per request, in input order
	if o.Err != nil {
		fmt.Printf("task %d failed (isolated, batch continued): %v\n", o.Index+1, o.Err)
		continue
	}
	fmt.Printf("task %d: %d commit(s)\n", o.Index+1, len(o.Result.Commits))
}
```

Two contract points make this safe to rely on:

- **Per-task failure isolation.** A task that fails sets `Err` on its *own* `TaskOutcome`; the batch
  continues and still returns every other outcome. An individual failure is *never* the top-level
  error — that's reserved for a whole-batch failure like the passed `ctx` being cancelled.
- **The merge race is handled.** All three tasks target the same repo, so `RunTasks` serializes just
  the per-repo git-metadata steps (worktree add, merge-to-`HEAD`) behind a per-repo lock — the
  containers and host-side loops run fully in parallel, merges apply one at a time, and a conflicting
  merge is aborted so it never corrupts `HEAD` for the next task.

`OnEvent` multiplexes all tasks into one feed, each event tagged with its task `Index` — the hook you
fan into an SSE stream. `RunTasks` serializes emission, so your callback need not be goroutine-safe.

**Requirements & honesty note.** Same as lesson 06: needs **Docker**, and under the keyless stub every
task completes with zero commits (proving the concurrent plumbing and key-free containers). A real
provider key drives real parallel edits.

**Runnable example:** [`examples/task-fanout`](../../examples/task-fanout) — three tasks against one
throwaway repo with `Concurrency: 2` and a live multiplexed event printer.

**Depth:** [ADR-0037 — concurrent sandboxed task agents](../decisions/0037-concurrent-sandboxed-task-agents.md)
(built on [ADR-0036](../decisions/0036-sandboxed-task-agent-key-brokering.md)).

Next: [08 — Extend the runtime](08-extend.md).
