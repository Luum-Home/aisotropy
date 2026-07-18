---
title: "Multi-agent graph / swarm orchestration readiness — code-truth audit"
date: 2026-07-17
type: research
status: read-only investigation (no code changed)
topic_key: architecture/multi-agent-graph-readiness
scope: internal/taskagent, internal/llm, internal/agentloop, internal/routing, internal/api, internal/primitives, internal/consolidation, internal/memory, internal/sandbox, aisotropy/
---

# Multi-agent graph / swarm orchestration readiness

> Question: does aisotropy have **native multi-agent graph / swarm orchestration** — a model
> where **agents are nodes** and **one agent's output is another agent's input** (agent→agent
> handoff, a DAG/graph/pipeline of interconnected agents)?

Every claim below cites `file:line` read directly from source (not grep-guessed). `arenas/` was
deliberately not touched.

---

## 1. Verdict

**ABSENT.** Aisotropy has **no** native multi-agent graph/swarm/DAG where one agent's output
feeds another agent's input. What exists is a **single-agent tool loop** (one model, its tools
feed back into *its own* context), optionally **fanned out over independent, non-communicating
task instances**, wrapped in **decorators** (breaker/otel) and fronted by **tenant→tier routing**
and a **producer/consumer memory pipeline**. None of these is a graph of interconnected agents.

The multi-agent/graph capability is **explicitly planned but not built**: ADR-0016 (Accepted)
names it "L2 composition SDK" and the roadmap files it under Phase 9 lever #4, both with
**unchecked** DoD. The Gollem orchestration engine it would wrap (`ext/graph`, `ext/team`,
`ext/orchestrator`) is present in the vendored tree but **imported by zero lines of aisotropy
code**.

Confidence: **high** for absence — the negative is corroborated three ways (no agent→agent code
path in any read package; no import of the graph/team/orchestrator libraries; and the docs
themselves classify it as future work).

---

## 2. Evidence per component (file:line)

### (a) Single-agent tool loop — the only "agent" shape that exists

**`internal/agentloop/agentloop.go`** — the structured-output loop. `Loop.Run`
(`agentloop.go:132`) drives up to `MaxSteps` turns against **one** `llm.Client`
(`agentloop.go:59`). A tool result is appended back into the **same** running prompt for the
**same** model — `current = fmt.Sprintf("%s\n\nTool %q returned:\n%s\n\nContinue.", ...)`
(`agentloop.go:152`). There is no second agent: the executor is a `ToolExecutor`
(`agentloop.go:35`), i.e. a tool, not an agent. The package doc frames its entire purpose as
scope-safe tool-calling for one model (`agentloop.go:1-17`).

**`internal/llm/toolagent.go`** — the native tool-calling twin. `RunToolAgent`
(`toolagent.go:65`) builds exactly **one** `core.Agent[string]` over `NewGollemModel(client)`
(`toolagent.go:74`, `toolagent.go:121`) and registers each offered tool as a Gollem *function
tool* (`toolagent.go:100-108`). A tool handler runs `exec` and records a `ToolStep`
(`toolagent.go:85-98`); `ToolStep` (`toolagent.go:36`) is a *tool* result, not an agent handoff.
One `agent.Run(ctx, prompt)` (`toolagent.go:123`) is the whole loop.

**`internal/api/chat_agent.go`** — the HTTP agent route. `RegisterAgent` (`chat_agent.go:106`)
mounts `POST .../chat/agent` and, per request, runs **one** agent: either
`runNativeToolAgent` → `llm.RunToolAgent` (`chat_agent.go:176-177`, `chat_agent.go:261`) or the
`agentloop.Loop` fallback (`chat_agent.go:183-185`). Output is a flat `[]AgentStep`
(`chat_agent.go:22`, `chat_agent.go:192-195`) — tool decisions of a single agent, never a
downstream agent's input.

### (b) Parallel fan-out of INDEPENDENT tasks — not a graph

**`internal/taskagent/runtasks.go`** — `RunTasks` (`runtasks.go:114`) runs N `RunTask` calls
**concurrently**, one goroutine per request, bounded by an `errgroup` slot limit
(`runtasks.go:148-149`). Each produces an independent `Outcome` (`runtasks.go:74-79`,
`runtasks.go:176-177`). The tasks **do not feed each other**: the only cross-task coordination is
a per-repo mutex (`repoGate`, `runtasks.go:140`, `runtasks.go:160`) that serializes the shared-repo
git `worktree add` / `merge-to-HEAD` plumbing (`runtask.go:450-479`) so concurrent merges don't
corrupt HEAD — a filesystem lock, not a data edge. The doc calls this "a fan-out of INDEPENDENT
tasks" and "per-task error isolation" (`runtasks.go:81-107`). ADR-0037's own title/body call
`RunTasks` a "**fan-out orchestrator**" of independent tasks
(`docs/decisions/0037-concurrent-sandboxed-task-agents.md:195`), and it lists true orchestration
as a *future* path ("orchestrator. Two ways to get there", ADR-0037:72).

`RunTask` → `runLoop` (`runtask.go:486`) itself just dispatches to the same single-agent loop
(`llm.RunToolAgent` at `runtask.go:496`, or `agentloop.Loop` at `runtask.go:507`). So even each
fan-out node is a single agent, not a sub-graph.

### (c) Decorator/middleware delegation — not agent→agent

**`internal/llm/breaker.go`** — `WithCircuitBreaker` (`breaker.go:48`) wraps a `Client`; if the
client is also a `ToolCaller` it returns a `circuitToolCaller` (`breaker.go:56-59`,
`breaker.go:67`) that gates `CallTools` on the same breaker (`breaker.go:77-84`). This is a
**decorator over one client** (breaker state + delegate), not a second agent consuming the first's
output. Same category: the otel tracing hook (`toolagent.go:148`) and the reflection loop.

**`internal/llm/reflect.go`** — `Reflect` (`reflect.go:70`) is single-agent **self-repair**:
generate → validate → retry with the exact error as feedback (`reflect.go:80-92`). `ReflectAuto`
(`reflect_auto.go:62`) picks the Gollem or homegrown backend but is still one model correcting
itself, not two agents in a chain.

### (d) Tenant→tier routing — not agent→agent

**`internal/routing/router.go`** — `Router.Resource(base, tenant)` (`router.go:57`) maps a
tenant to a **physical resource name** (Shared → pooled `base`; Dedicated → `base_tenant`,
`router.go:64-67`). `Policy.Tier` (`policy.go:10`, `tier.go:6`) classifies a tenant as
`Shared`/`Dedicated` (`tier.go:14-20`). This routes **data storage**, never a message from one
agent to another.

### (e) Transactional outbox → async consolidation — a producer/consumer pipeline, not agents

**`internal/memory/outbox.go`** — a durable event queue: a chat turn + its "intent to
consolidate" are written in one ACID txn (`AppendWithOutbox`), and a worker `RunOutboxWorker`
drains pending events to the sinks with at-least-once + backoff (`outbox.go:11-20`).

**`internal/consolidation/sink.go`** — `Sink.Consolidate` (`sink.go:44`) takes one drained
`OutboxEvent` and writes it to the facts (FTS5) and semantic (vector) **stores**, in parallel
(`sink.go:50-57`). The two goroutines write to two **stores** (`sink.go:65`, `sink.go:90`), not to
two agents. This is a memory-write pipeline (producer = chat turn, consumer = worker), with **no
LLM/agent in the loop** — so it is not an agent→agent edge either.

**`internal/primitives/primitives.go`** — per-(tenant,user) behavioral memory: a clone of a
tool's *guidance metadata* (`primitives.go:41-70`), rewritten offline by the sleep cycle when
friction crosses a threshold (`primitives.go:14-25`). Written by `frictionExecutor`
(`chat_agent.go:53-66`) and read back by `overlayRegistry` (`chat_agent.go:289`). This is a
write-then-read *self-improvement* loop on tool text — still one agent reading improved tool
descriptions, not an agent graph.

---

## 3. What exists vs. what's missing

| Concept | Present? | Where |
|---|---|---|
| (a) Single-agent tool loop | ✅ yes | `agentloop.go:132`, `toolagent.go:65`, `chat_agent.go:106` |
| (b) Parallel fan-out of **independent** tasks | ✅ yes | `runtasks.go:114` |
| (c) Decorator/middleware over a client/ToolCaller | ✅ yes | `breaker.go:48`, `reflect.go:70` |
| (d) Tenant→tier data routing | ✅ yes | `router.go:57` |
| (e) Outbox→consolidation producer/consumer (no agent) | ✅ yes | `outbox.go`, `sink.go:44` |
| **Node=agent / edge=output→input DAG** | ❌ **no** | *nowhere in the read packages* |
| Agent→agent handoff / delegation between distinct agents | ❌ no | — |
| Graph/workflow/team executor wired into the runtime | ❌ no | `ext/{graph,team,orchestrator}` vendored, **0 imports** |

**The missing thing precisely:** no type or function anywhere in the runtime accepts the
structured output of one `RunToolAgent`/`Loop.Run`/`RunTask` and feeds it as the **input prompt or
input args of another agent**. Fan-out (b) starts every task from its own `Request.Prompt`
(`runtask.go:333`) and never reads a sibling's `Outcome`. The pipeline (e) moves *memory rows*
between *stores*, with no model call on the edge.

---

## 4. Building blocks a graph layer would build on (all real, cited)

A future node=agent / edge=output→input executor already has its substrate:

1. **Scoped single-agent node body** — `llm.RunToolAgent(ctx, tc, tracer, system, prompt, specs,
   run, maxSteps)` (`toolagent.go:65`). This is exactly a "run one agent to completion and return
   structured `[]ToolStep`" — the natural **node** primitive. Its structural scope-gate
   (`toolagent.go:50-54`) means a node can be given a restricted toolset.
2. **Bounded concurrent scheduler + per-task isolation** — `taskagent.RunTasks` (`runtasks.go:114`)
   already gives slot-limited concurrency, per-task error isolation, and a per-resource serialization
   gate (`runtasks.go:140`). A DAG executor could reuse this to run independent nodes of one "layer"
   in parallel.
3. **The `ToolCaller` seam + `ToolSpec`** — native tool-calling abstraction preserved through
   decorators (`breaker.go:56-59`, `breaker.go:67`). Edges could be modeled as tools ("call
   downstream node X") or the raw seam reused directly.
4. **`agentloop.ToolExecutor` interface + `Decision` structured output** (`agentloop.go:35`,
   `agentloop.go:42`) — a clean dispatch seam and a JSON contract; a node's structured output
   (`ToolStep`/`AgentStep`, `toolagent.go:36`, `chat_agent.go:22`) is what an edge would pipe.
5. **ctx-based tenant isolation** — identity enters ctx once at the boundary (`withIdentity` at
   `chat_agent.go:124`; `tenancy.WithTenant/WithUser` used in `sink.go:72`,`sink.go:105`) and is
   inherited by every downstream call. A composed node inherits the already-scoped ctx and never
   constructs a `Scope` — the exact ADR-0016 invariant.
6. **Scope-gated tool registry** — `Registry.Permitted(scopes)` (`chat_agent.go:234`,
   `runtask.go:488`) and the double scope-gate both loops enforce (`agentloop.go:105-111`,
   `toolagent.go:50-54`). Each graph node can be given only its permitted tools.
7. **Durable event-pipe pattern** — the transactional outbox + idempotent sink
   (`outbox.go:11-20`, `sink.go`) is a proven template if graph edges ever need durable,
   at-least-once handoff (checkpoint/resume) rather than in-memory piping.
8. **Façade Option builder** — `WithTool`/`WithToolExecutor` (`app.go:270`,`app.go:276`),
   `WithTypedTool[T]`, `WithMCPServer`, `WithTenantRoutingPolicy` (`extend.go:488`) — the public
   composition surface where a `WithGraph(...)`/`WithTeam(...)` option would slot in.
9. **Vendored (but unused) engine** — `external/gollem/ext/graph`, `ext/team`, `ext/orchestrator`
   exist in the tree; aisotropy currently imports only `gollem/core`, `ext/mcp`, `ext/otel`
   (verified: 9× core, 6× ext/mcp, 2× ext/otel; **0×** graph/team/orchestrator/temporal).

---

## 5. Design sketch (PROPOSAL ONLY — not implemented)

A minimal node=agent / edge=output→input layer on top of the existing seams:

- **Node** = `type Node struct { ID string; System, PromptTemplate string; Tools []ToolSpec;
  Scopes []string; MaxSteps int }`. Running a node = `llm.RunToolAgent(nodeCtx, tc, tracer,
  node.System, resolvedPrompt, permittedSpecs, run, node.MaxSteps)` (`toolagent.go:65`), returning
  its structured `[]ToolStep`/final output.
- **Edge** = `type Edge struct { From, To string; Map func(out NodeResult) (promptOrArgs string) }`.
  The executor pipes one node's structured output through `Map` into the next node's input prompt —
  the single new abstraction that does not exist today.
- **Executor** = a DAG scheduler that topologically orders nodes, runs independent nodes of a layer
  **concurrently reusing the `RunTasks` slot-limited pattern** (`runtasks.go:114-202`), and blocks a
  node until its in-edges' sources complete. In-memory piping to start; the outbox/sink pattern
  (`outbox.go`, `sink.go`) is the upgrade path for durable checkpoint/resume (matching ADR-0016's
  note that adopting Gollem `ext/orchestrator`'s SQLite store was **declined** to avoid a second
  persistence system — ADR-0016:254-256).
- **Or**: wrap the vendored `gollem/ext/graph` + `ext/team` under the scoping invariant (ADR-0016
  lever #4) instead of hand-rolling — the roadmap's stated intent (`milestones.md:190-193`).

**Constitutional constraints the layer MUST respect** (from
`.claude/rules/constitutional-gates.md`, already honored by the seams above):
1. **Tenant isolation via ctx** — every node receives the request's already-scoped ctx; a node
   never reads `tenant_id` from a data field and never constructs a `Scope` (the ADR-0016 machine-
   enforced invariant, DoD `milestones.md:206-207`).
2. **No two tenants in one buffer/goroutine scope** — the DAG executor must not let one node's
   goroutine hold a second tenant's data; `RunTasks`' per-task isolation model (`runtasks.go:84-97`)
   is the template.
3. **Scope-gated tools per node** — each node offered only `Registry.Permitted(node.Scopes)`; the
   double scope-gate (`agentloop.go:105`, `toolagent.go:50`) must survive composition.
4. **Fail-closed structured output on edges** — a malformed node output that an edge can't map must
   fail closed (reflection/reject), never silently coerce — reuse the `ToolArgsError` reflection
   signal (`typed_tools.go:22`, `chat_agent.go:252-255`).
5. **Quota-before-spend + circuit breaker per node** — each node's LLM call goes through the same
   pre-spend gate and breaker (`breaker.go:48`) so N nodes can't bypass the token budget.

---

## 6. Documentation status

The gap is **documented as intended future work**, not silently missing:

- **ADR-0016 — "Dual surface: sealed runtime kernel + composition SDK over ctx-scoped nodes"**
  (`docs/decisions/0016-...md`). **Status: ✅ Accepted (2026-07-09)** (ADR-0016:11-13). It explicitly
  scopes graph/workflow/**multi-agent team** composition as **lever #4 ("L2 composition SDK")** and
  states "**None of this** [is built]" for the composition axis (ADR-0016:51), that the engine is
  "already vendored — `external/gollem/ext/{graph,orchestrator,team,mcp}`" and this phase "**exposes
  it under scoping**, it does not build one" (ADR-0016:55-56, and `milestones.md:173-175`). Levers
  #1 (typed tools), #2 (MCP tools), #3 (compose), #5 (config) are delivered; **#4 is not**.
- **Roadmap — Phase 9 "Composition surface"** (`docs/roadmap/milestones.md:164-213`). Objective:
  make aisotropy "a composable agent framework (B) — arbitrary tool topologies, graphs/workflows,
  **multi-agent teams**" (`milestones.md:165-167`). Lever #4 (graph/workflow/team SDK) is **not
  checked** (`milestones.md:190-193`); DoD item "A consumer composes a multi-step graph /
  multi-agent team against the public L2 surface" is **unchecked** (`milestones.md:206-207`).
- **ADR-0037** (`docs/decisions/0037-concurrent-sandboxed-task-agents.md`) describes `RunTasks` as
  a **fan-out** of independent tasks (ADR-0037:195) and lists real orchestration as a future path
  (ADR-0037:72) — corroborating that today's concurrency is (b), not a graph.
- **Positioning docs** frame this consistently: `why-go.md:20` — "Go has agent orchestration
  *libraries*, but no isolation-first … runtime"; competitive notes call Eino "a LangGraph-for-Go …
  orchestration library, not a runtime" (`competitive-landscape.md:271-283`). Aisotropy positions
  itself as the isolation runtime, with graph composition as an **optional** later module (B).

So: **the roadmap and an Accepted ADR both mention multi-agent/graph orchestration — as planned,
undelivered Phase 9 lever #4.**

---

## 7. Honest uncertainties

- I read the orchestration/dispatch logic of the packages named in the task in full, plus the
  outbox/consolidation/primitives/routing/breaker/reflect seams. I did **not** exhaustively read
  every file in `internal/api/` (58 files) — e.g. `agui.go`, `aisdk.go`, `knowledge.go` — nor the
  `internal/gateway/`, `internal/eval/`, `internal/bitrate/` packages. `agui.go`/`aisdk.go` are
  protocol adapters (AG-UI / Vercel AI SDK wire formats) over the same single-agent routes; I judged
  them out of scope for an *agent-graph* seam but did not line-read them. Low risk, non-zero.
- The `aisotropy/app.go` façade is ~80KB; I enumerated its `With*` builder options (no
  graph/team/pipeline option exists) but did not read every line of its wiring. The public option
  surface is authoritative for "is graph composition exposed?" and it is not.
- I confirmed **0 imports** of `gollem/ext/{graph,team,orchestrator}` in `internal/`, `aisotropy/`,
  `cmd/` (excluding `arenas/`, per instruction). If graph wiring lived *only* inside `arenas/`
  (which I did not open), this audit would not see it — but `arenas/` is explicitly experimental and
  not the runtime.
- "ABSENT" is a claim about the current `main` tree at 2026-07-17. It is well-corroborated (no code
  path + no import + docs classify it as future), but absence proofs are inherently bounded by what
  was read; I actively searched for a hidden seam (grep for swarm/handoff/DAG/graph/orchestrat across
  all non-test Go) and found only the single-agent "orchestrator" naming and the unused vendored
  engine.

TRUST_REPORT: SCORE=88 STATUS=MEDIUM EVIDENCE=11 UNCERTAINTIES=4
