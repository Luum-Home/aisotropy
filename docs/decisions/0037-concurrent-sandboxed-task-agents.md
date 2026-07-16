---
type: ADR
title: "ADR-0037 — Concurrent sandboxed task agents: App.RunTasks, a bounded fan-out over RunTask that keeps per-task isolation and the key-broker invariant under parallelism"
description: >
  ADR-0036 shipped RunTask — one sandboxed agentic coding task whose defining property is KEY-BROKERING
  (the LLM call, and the provider key, stay host-side; only tool EXECUTION is dispatched into a container
  the agent never fully trusts). This ADR adds the concurrency wedge on top: App.RunTasks fans N tasks
  out in parallel from a single Go process, bounded by a concurrency limit, and — because it builds
  directly ON RunTask — keeps every single-task guarantee PER TASK under the fan-out: each task gets its
  OWN git worktree, its OWN sandbox container, and the key never enters ANY container. This is the
  "Go × isolation" wedge: a plain library call runs many untrusted agents concurrently, key-free, from an
  in-process service — no orchestrator, no N copies of the key in N boxes. As shipped (v1): a new
  internal/taskagent.RunTasks(ctx, client, tracer, []Request, Options) drives the fan-out with
  golang.org/x/sync/errgroup + SetLimit as a slot-limited WaitGroup (NO new dependency — x/sync is
  already in go.mod), captures each task's failure on its OWN Outcome (per-task error isolation; the
  batch continues), makes the parent-ctx cancellation the SOLE top-level error, serializes the two
  shared-repo git-metadata mutations (worktree-add and merge-to-HEAD) behind a per-repo lock with
  abort-on-conflict so concurrent merges never corrupt HEAD, and multiplexes a live index-tagged event
  stream (started/step/shell/commit/done/error) whose emission is serialized so OnEvent need not be
  goroutine-safe. Fronted by the public aisotropy.App.RunTasks + TaskOutcome/TaskEvent/RunTasksOptions.
  Honest scope: the merge strategy is SERIALIZE-AND-MERGE (preserving RunTask's auto-merge), not
  branch-only; a real cross-branch content conflict surfaces as that task's Err with HEAD aborted-clean
  (automatic conflict RESOLUTION is a named follow-on); commit events are derived at completion (SHAs
  settle at merge time) while started/step/shell are genuinely live; there is no HTTP/SSE route
  (façade + callback only, matching RunTask). STATUS: accepted — reconciled against the shipped
  internal/taskagent.RunTasks + aisotropy.App.RunTasks code and its Docker-gated tests.
tags: [decisions, adr, sandbox, task-agent, key-brokering, concurrency, fan-out, isolation, docker, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0037 — Concurrent sandboxed task agents: `App.RunTasks`, a bounded fan-out over `RunTask` that keeps per-task isolation and the key-broker invariant under parallelism

## Status

✅ **Accepted — shipped 2026-07-16** (`internal/taskagent/runtasks.go` and the
`aisotropy.App.RunTasks` façade at `aisotropy/runtasks.go` are in the tree with tests). It is the
concurrency layer directly on top of [ADR-0036](0036-sandboxed-task-agent-key-brokering.md): where
0036 runs **one** sandboxed agentic task with the **key-broker invariant** (brain + key host-side,
hands in a container), this ADR runs **N of them in parallel** from a single Go process and proves the
invariant holds **per container under concurrency**.

> **Isolation under concurrency is proven against real Docker**, not asserted:
> `TestRunTasks_ConcurrentDocker_KeyFree_NoHeadCorruption` fans three tasks out against one shared repo
> with real containers, probes each container's env for the provider key (broker probe), and asserts the
> shared repo's HEAD is never corrupted by the concurrent merges. The docker-free fan-out /
> ordering / cancellation tests (`TestRunTasks_FanOut_OrderIsolationEvents`,
> `TestRunTasks_CancelledContextIsTopLevelError`, `TestRunTasks_Empty`) and the façade tests
> (`TestApp_RunTasks_KeylessConcurrentFacade`, `TestApp_RunTasks_Empty`) pass under `-race`;
> Docker-dependent tests `t.Skip` when the daemon is absent (the honesty gate).

This ADR **extends** [ADR-0036](0036-sandboxed-task-agent-key-brokering.md) without touching its
kernel: `RunTasks` only **schedules and serializes** — each task still goes through the exact same
`RunTask`, so the worktree, the container, the `agentloop.ToolExecutor` seam
([ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)), the scope-gating, and the
key-broker enforcement point are inherited verbatim. It holds the constitutional **license gate**:
**no new dependency** — the fan-out uses `golang.org/x/sync/errgroup`, already present in `go.mod`
(BSD-3, part of `x/sync`), as a slot-limited `WaitGroup`. It holds the **multi-tenancy gate**: a
tenant identity injected into `ctx` is inherited by each task's `RunTask` through the derived context;
`RunTasks` reads no tenant from any `Request` and shares no cross-task buffer.

## Context

[ADR-0036](0036-sandboxed-task-agent-key-brokering.md) established the differentiator: aisotropy runs
the LLM call **host-side**, so a sandboxed agent can do real shell/git/file work in a container it
never fully trusts **without the provider API key ever entering that container** — the exact inverse
of a coding-CLI-in-a-box, which must place the raw key inside the box because the CLI calls the model
from in there. That primitive runs **one** task.

The obvious next question is throughput and shape: an LLM-OS should be able to dispatch **many**
agentic tasks at once — a batch of refactors across a repo, a fan-out of independent fixes, a
parallel sweep of candidate branches — from a single service, cheaply, without an external
orchestrator. Two ways to get there:

1. Make the caller loop over `RunTask` themselves. Correct but leaves every hard part — bounding
   concurrency so N containers don't swamp the Docker daemon and the provider rate limit, isolating
   one task's failure from the batch, and the **merge race** (N tasks all merging into the same repo's
   HEAD at once corrupts it) — as an exercise for the consumer, who will get the merge race wrong.
2. Provide a **native fan-out primitive** that owns those hard parts once, correctly, and inherits
   0036's isolation for free.

This ADR takes option 2, and it is deliberately the **"Go × isolation" wedge**: because inference is
host-side and the agent loop is an ordinary in-process Go call, running N untrusted agents
concurrently is *a library call with a concurrency limit* — `app.RunTasks(ctx, reqs, opts)` — not a
distributed job system, not a container orchestrator, and crucially **not N copies of the provider key
in N boxes**. The thing that is hard for everyone else (parallel untrusted agents that never hold the
key) falls out of the host-side architecture, and Go's goroutines + `errgroup` make the fan-out a few
dozen lines instead of a service.

## Goals

- A **native `RunTasks` primitive** that runs N `RunTask`s **concurrently**, bounded by a caller
  concurrency limit, returning one outcome per request **in input order**.
- **Isolation is inherited, not re-implemented:** each task goes through the same `RunTask`, so each
  gets its own worktree, its own container, and the **key-broker invariant** per box. The fan-out adds
  **zero** new trust surface — a design where concurrency leaks a key into a container, or lets two
  tasks share a worktree/container, has failed.
- **Per-task error isolation:** an individual task's failure lands on **its own** outcome and the
  batch **continues**; a task failure is **never** the top-level error. The top-level error is
  reserved for a whole-batch failure — the parent `ctx` being cancelled.
- **Correct handling of the merge race:** N tasks merging into the same repo's HEAD must not corrupt
  it. The shared-repo git-metadata mutations are serialized; a failed merge is **aborted** so a
  conflict never leaves HEAD half-merged for the next task.
- **A live, multiplexed event stream:** one index-tagged feed interleaving all tasks
  (`started`/`step`/`shell`/`commit`/`done`/`error`), emission serialized so the consumer's callback
  need not be goroutine-safe — the SSE-shaped hook for a fan-out.
- **No new dependency; single-binary posture preserved:** the concurrency primitive is
  `golang.org/x/sync/errgroup`, already in `go.mod`.
- **Docker-availability-gated tests** (the honesty gate): the concurrent real-container test `t.Skip`s
  when the `docker` CLI is absent, matching 0036 and the rqlite/Qdrant/sleep-cycle pattern. No faked
  container behavior stands in for a real one.

## Non-goals (v1 scope, held deliberately small)

- **NOT a change to `RunTask`'s trust model or scope.** Everything 0036 held out of scope (the six
  coding CLIs, additional providers, multi-repo primitives, richer hands) stays out of scope here.
  `RunTasks` schedules `RunTask`; it does not widen it. The only edits to the kernel are two unexported
  `Request` hooks (`repoGate`, `onStep`) and routing the worktree-add / merge through gated helpers —
  a standalone `RunTask` with a nil gate behaves exactly as before.
- **NOT automatic cross-branch conflict RESOLUTION.** v1 preserves `RunTask`'s auto-merge and
  serializes it. If two task branches genuinely conflict in content, the second merge fails; `RunTasks`
  best-effort `git merge --abort`s so the shared HEAD is restored clean, and surfaces the merge error
  as **that task's** `Outcome.Err`. Resolving the conflict (rebase, three-way, LLM-assisted) is a named
  follow-on, not a silent gap.
- **NOT a distributed / cross-process scheduler.** `RunTasks` is an in-process, bounded goroutine
  fan-out on one host. Cross-host scheduling, a durable work queue, and ret/resume are follow-ons — the
  primitive is deliberately a library call, not a job system.
- **NOT an HTTP/SSE route.** Like `RunTask`, `RunTasks` ships as an in-process façade + an `OnEvent`
  callback. Wiring the multiplexed stream to an SSE endpoint is a straightforward follow-on (the event
  shape is already SSE-friendly) but is not in v1.
- **NOT a relaxation of tenant isolation.** This is the *execution*-isolation / concurrency axis. A
  tenant identity injected into `ctx` is inherited by each task; no cross-task buffer holds two tenants'
  work. Tenant *data* isolation remains ctx-scoping + [ADR-0034](0034-open-core-tier2-sealed-driver.md)
  sealed drivers.

## Options considered

### How do concurrent tasks return the merge race's answer?

Every task's `RunTask` merges its branch into the **same** `RepoDir` HEAD (`--no-ff`), so the shared
repo's HEAD/branch-ref/index is the one contended resource. Two options:

- **Drop auto-merge; return branch-only under concurrency.** Each task would leave its commits on its
  branch and hand the merge decision entirely to the caller. Simple and race-free, but it *regresses*
  `RunTask`'s single-task ergonomics (which auto-merges) purely because you asked for more than one
  task — the batch would behave differently from a loop of singles. Rejected.
- **Serialize the shared-repo git steps, preserving auto-merge (CHOSEN).** Everything expensive — the
  container create, the host-side agent loop, the in-container exec, the in-worktree commits, and
  `CommitsSince` — runs **concurrently and ungated** (git writes loose objects into the shared object
  store atomically, so only HEAD/branch-ref/index moves need serializing). Only the two
  git-**metadata** mutations — the `git worktree add` (concurrent adds race the repo lock) and the
  `MergeToHead` — take a **per-repo lock** (`repoGates`, keyed by `RepoDir`). Tasks on the same repo
  merge one at a time; tasks on **different** repos share no lock and stay fully parallel. A gated
  merge that fails is `git merge --abort`ed so a conflict never poisons the next task's merge. This
  keeps `RunTask`'s auto-merge semantics identical under a fan-out and confines the serialization to
  microseconds of git plumbing.

### What concurrency primitive bounds the fan-out?

- **A hand-rolled semaphore + `sync.WaitGroup`.** Works, no dependency question, but re-implements
  exactly what `errgroup.SetLimit` already does and loses the derived-context cancellation for free.
- **`golang.org/x/sync/errgroup` with `SetLimit` (CHOSEN).** Already in `go.mod`/`go.sum`
  (`v0.20.0`, BSD-3 — part of `x/sync`, **no new dependency**). Used here as a **slot-limited
  `WaitGroup`**: the task goroutines never return an error (a per-task failure is captured on its
  `Outcome`, not propagated), so the group is purely a bounded launcher; its derived `gctx` is passed
  into each `RunTask` so a parent-`ctx` cancel propagates into in-flight tasks, and scheduling of
  not-yet-started tasks stops early on cancel.

### What is the top-level error?

- **Aggregate task failures into the return error.** Rejected — it conflates "one task's `docker run`
  failed" with "the batch could not run", forcing the caller to unpack an aggregate to find which
  tasks actually succeeded.
- **Per-task `Err` on the outcome; top-level error reserved for whole-batch failure (CHOSEN).** Each
  task's failure rides on its own `Outcome.Err` and the batch continues. The **top-level** error is
  non-nil **only** when the parent `ctx` is cancelled — which also stops scheduling and stamps `ctx`'s
  error onto any outcome whose slot never got scheduled. A caller can therefore trust: top-level nil ⇒
  every task got to run (check each `Err`); top-level non-nil ⇒ the batch was cancelled.

### How is progress streamed across concurrent tasks?

- **Let each task call the caller's callback directly.** Rejected — the callback would then be invoked
  from N goroutines at once and would have to be goroutine-safe, pushing synchronization onto every
  consumer.
- **One multiplexed, index-tagged stream with serialized emission (CHOSEN).** Each `Event` carries the
  task `Index`, and `RunTasks` funnels all emissions through an internal mutex so the consumer's
  `OnEvent` sees **one event at a time** — it need not be goroutine-safe (the documented contract).
  `started`/`step`/`shell` stream **live** (started before work; step via the executor `onStep` hook;
  shell via the existing per-line hook, tagged with the task index and chained to the caller's own
  `OnLine`); `commit` is drawn from the finished `Result` (a SHA and its merge to HEAD only settle at
  completion); `done`/`error` are terminal. This ordering is documented honestly rather than pretending
  commit SHAs are known mid-flight.

## Decision (v1 / minimal)

### 1. `internal/taskagent.RunTasks` — the fan-out orchestrator

`RunTasks(ctx, client, tracer, []Request, Options) ([]Outcome, error)` runs one goroutine per request,
bounded by `Options.Concurrency` (`<= 0` selects `min(NumCPU, maxDefaultConcurrency=8)`, at least 1 —
each concurrent task holds a live container plus a host-side LLM stream, so the default is clamped so a
big host does not swamp the Docker daemon or the provider rate limit). It builds only on the existing
`RunTask`: each goroutine constructs its `Request`, attaches the per-repo gate and the per-step hook,
calls `RunTask(gctx, …)`, and records the result/error on `Outcome[i]`. Outcomes are returned **in
input order**.

- **`Outcome{Index, Request, Result, Err}`** — per-task; `Err` is the individual failure (batch
  continues). A task whose slot was never scheduled (parent ctx cancelled mid-fan-out) carries ctx's
  error.
- **`Options{Concurrency, OnEvent}`** — the concurrency bound and the multiplexed event sink.
- **`Event{Index, Kind, Tool, Line, Message}`** with exported kind constants
  `EventStarted`/`EventStep`/`EventShell`/`EventCommit`/`EventDone`/`EventError`.
- **`repoGates`** — a `map[string]*sync.Mutex` keyed by `RepoDir` (its own map guarded by a mutex),
  handing each task the lock for its repo so same-repo git-metadata steps serialize while different-repo
  tasks stay fully parallel.

### 2. Two unexported hooks on `RunTask`'s `Request` — the only kernel edits

`Request` gains `repoGate sync.Locker` and `onStep func(tool, result string)`, both set **only** by
`RunTasks`. The worktree-add and merge-to-HEAD are routed through `gatedWorktreeAdd` /
`gatedMergeToHead`, which take the gate when non-nil and are otherwise the prior calls. A standalone
`RunTask` leaves both nil and behaves exactly as in ADR-0036 (no gate, no abort-on-merge-failure change
to its documented behavior). `gatedMergeToHead` additionally `git merge --abort`s a **failed gated**
merge so a conflict restores HEAD clean for the next task; the original merge error still propagates as
that task's failure.

### 3. The public façade `aisotropy.App.RunTasks`

`App.RunTasks(ctx, []TaskRequest, RunTasksOptions) ([]TaskOutcome, error)` resolves the App's client
(`WithLLM` or `llm.FromEnv()`), maps the public `TaskRequest`s to internal `Request`s, adapts the
public `RunTasksOptions.OnEvent(TaskEvent)` to the internal `Options.OnEvent(Event)`, calls
`taskagent.RunTasks`, and maps `Outcome`→`TaskOutcome`. Public types: `TaskOutcome{Index, Request,
Result, Err}`, `TaskEvent{Index, Kind, Tool, Line, Message}`, `RunTasksOptions{Concurrency, OnEvent}`.
It boots no HTTP runtime and does not touch the App's `Run`/`Turn` state — the same in-process shape as
`RunTask`.

### The wedge, restated in one line

Because inference is host-side, running **N untrusted agents in parallel, each key-free in its own
container**, is a single Go library call with a concurrency limit — the thing every
coding-CLI-in-a-box design cannot do (it would mean N copies of the raw key, one per box), delivered as
`app.RunTasks(ctx, reqs, opts)`.

## Consequences

- **Parallel agents, still zero keys in the boxes.** The headline of ADR-0036 — the agent never holds
  the provider key — now holds **per container under concurrency**, proven by a real-Docker test that
  probes each container's env while three tasks run against one repo. No competitor whose LLM client
  lives in the box can offer "N untrusted agents in parallel, none holding the key."
- **Isolation was inherited, not rebuilt.** The fan-out added no new trust surface: same `RunTask`,
  same worktree/container/executor/scope-gating. The only kernel edits are two unexported hooks and a
  gated-merge helper, all no-ops for a standalone `RunTask`.
- **Failures are contained to their task.** A batch of N tasks where task *k* fails still returns the
  other N−1 outcomes; the caller distinguishes "batch cancelled" (top-level error) from "task *k*
  failed" (`Outcome[k].Err`) without unpacking an aggregate.
- **The merge race is closed, not assumed.** Concurrent merges into one HEAD are serialized behind a
  per-repo lock, and a failed merge is aborted rather than left corrupting HEAD — enforced by the
  no-HEAD-corruption assertion in the docker test, not promised.
- **No new dependency; single-binary posture preserved.** `errgroup` was already vendored; the fan-out
  is a slot-limited `WaitGroup` with a derived cancellable context.
- **Tests tell the truth.** The concurrent real-container test `t.Skip`s without Docker; the fast
  fan-out/ordering/cancellation and façade tests run everywhere under `-race`. The honest deviations
  are documented, not hidden: `commit` events are derived at completion (SHAs settle at merge time)
  while `started`/`step`/`shell` are genuinely live; a real cross-branch content conflict surfaces as
  that task's `Err` with HEAD aborted-clean, and automatic conflict **resolution** is a named
  follow-on; there is no HTTP/SSE route (façade + callback only).
- **Follow-ons are named, not silent.** Automatic conflict resolution; an SSE route for the multiplexed
  stream; cross-process/durable scheduling; and everything `RunTask` itself defers in ADR-0036.

## References

- **Builds directly on:** [ADR-0036](0036-sandboxed-task-agent-key-brokering.md) (the single sandboxed
  task agent + the key-broker invariant — `RunTasks` is a bounded fan-out over its `RunTask`, inheriting
  every isolation guarantee per task) · **same execution-isolation axis:**
  [ADR-0035](0035-wasm-plugin-sandbox.md) (WASM pure-function tier) and 0036 (container "hands" tier)
- **Seam reused unchanged:** [ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) /
  `internal/agentloop` (`ToolExecutor` — each task still runs the same host-side loop) ·
  **inference is host-side:** [ADR-0009](0009-provider-agnostic-llm-layer.md) /
  [ADR-0010](0010-gollem-adoption-scope-and-observability.md) / `internal/llm` (why N parallel agents
  can be key-free — the model call was never in the box)
- **Subprocess boundary discipline:** [ADR-0005](0005-datafusion-go-rust-boundary.md) (a `git`/`docker`
  CLI non-zero exit is a Go-observable error, no in-process crash) — applied here to the per-repo
  `git merge --abort` recovery
- **Different axis (held, not relaxed):** [ADR-0034](0034-open-core-tier2-sealed-driver.md) /
  [ADR-0011](0011-tenant-scoped-statement-builder.md) (tenant *data* isolation — ctx-scoping + sealed
  drivers; each concurrent task is single-tenant by `ctx`)
- **As shipped:** `internal/taskagent/runtasks.go` (`RunTasks`, `Outcome`, `Options`, `Event` + kind
  constants, `repoGates`, `defaultConcurrency`/`maxDefaultConcurrency`) + the two unexported `Request`
  hooks and `gatedWorktreeAdd`/`gatedMergeToHead` in `internal/taskagent/runtask.go` + the public
  `aisotropy.App.RunTasks`/`TaskOutcome`/`TaskEvent`/`RunTasksOptions` façade in `aisotropy/runtasks.go`
  · **runnable demo:** `examples/task-fanout/` (three concurrent tasks, `Concurrency: 2`, a multiplexed
  live `OnEvent` view, keyless-stub-honest) · **proof:**
  `TestRunTasks_ConcurrentDocker_KeyFree_NoHeadCorruption` (real Docker, broker probe +
  no-HEAD-corruption), `TestRunTasks_FanOut_OrderIsolationEvents`,
  `TestRunTasks_CancelledContextIsTopLevelError`, `TestRunTasks_Empty`,
  `TestApp_RunTasks_KeylessConcurrentFacade`, `TestApp_RunTasks_Empty`
