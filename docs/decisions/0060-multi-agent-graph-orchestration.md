---
type: ADR
title: "ADR-0060 — Multi-agent graph orchestration: a thin native DAG executor of scoped RunToolAgent nodes, output→input edges, under the ctx-scope invariant"
description: >
  Delivers ADR-0016 lever #4 / roadmap Phase 9's L2 composition SDK for the "node=agent, edge=output→input"
  case: a public Graph/Node/Edge surface where each node is a scoped single-agent RunToolAgent run with its OWN
  scopes + tool subset + persona, and one node's structured output is piped as another node's input across a
  build-time-validated DAG. Chooses a THIN NATIVE DAG executor over internal/llm.RunToolAgent (reusing the
  taskagent.RunTasks slot-limited concurrency pattern) INSTEAD OF wiring the vendored gollem ext/graph or
  ext/team — with code-truth evidence that ext/team constructs agents off a raw core.Model, bypassing the
  ToolCaller/breaker/quota/scope-gate seams and pulling in ext/orchestrator's second persistence substrate
  that ADR-0016 OQ#3 already declined. Every node inherits the request's already-scoped context.Context and
  never constructs a Scope (the ADR-0016 invariant); edges fail closed on a shape mismatch; cycles are rejected
  at build time (DAG only in v1). DESIGN ONLY — no code shipped by this ADR.
tags: [decisions, adr, phase-9, composition, multi-agent, graph, dag, swarm, tenancy, facade, gollem]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0060 — Multi-agent graph orchestration: a thin native DAG executor of scoped `RunToolAgent` nodes

## Status

📐 **Proposed (2026-07-17).** Design-only; no code shipped by this ADR (implementation plan in
[`0060-multi-agent-graph-orchestration-plan.md`](0060-multi-agent-graph-orchestration-plan.md)).

This ADR **delivers** [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) **lever #4 (L2
composition SDK)** and the [Phase 9](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b)
DoD item *"a consumer composes a multi-step graph / multi-agent team against the public L2 surface, and a
test proves an L2 node cannot obtain a `Scope`"* (`milestones.md:206-207`), for the **node=agent /
edge=output→input** slice.

It **refines** ADR-0016 on one sub-decision: ADR-0016 said the L2 surface would *"wrap `ext/graph` +
`ext/team`"* (ADR-0016:200, 252). This ADR revisits that mechanism choice against the vendored code
(read directly, cited below) and instead chooses a **thin native DAG executor over
`internal/llm.RunToolAgent`**. The reframing of ADR-0013's two axes (sealed kernel vs. public
composition) and the load-bearing invariant are **kept unchanged**; only the engine sub-choice moves,
with evidence. It sits above [ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) (the
`ToolCaller`/`RunToolAgent` node body), depends on [ADR-0011](0011-tenant-scoped-statement-builder.md)
(the `FromContext`-only `Scope`), reuses the concurrency wedge of
[ADR-0037](0037-concurrent-sandboxed-task-agents.md) (`taskagent.RunTasks`), and is governed by the
anti-magic gate of [ADR-0002](0002-anti-framework-magic-struct-as-contract.md).

Audit that established the gap and building-block catalog:
[`docs/research/multi-agent-graph-readiness-2026-07-17.md`](../research/multi-agent-graph-readiness-2026-07-17.md).

## Context

The audit (`multi-agent-graph-readiness-2026-07-17.md:21-37`) confirms, three ways, that aisotropy has
**no** native multi-agent graph: no code path feeds one agent's structured output into another agent's
input; `ext/{graph,team,orchestrator}` are vendored but imported by **0 lines** of runtime code
(re-verified for this ADR: `grep` over `internal/ aisotropy/ cmd/` finds `gollem/core` ×9,
`gollem/ext/mcp` ×6, `gollem/ext/otel` ×2, and **0** of graph/team/orchestrator); and the docs
themselves classify it as undelivered Phase 9 lever #4.

What **does** exist is exactly the substrate a graph layer builds on — all real, all cited:

| Building block | Where (`file:line`) | Role in a graph |
|---|---|---|
| Scoped single-agent node body | `internal/llm/toolagent.go:65` (`RunToolAgent`) | The **node** primitive: run one agent to completion, return `[]ToolStep` |
| Structural scope-gate | `internal/llm/toolagent.go:50-54`, `agentloop.go:105-111` | Only offered (scope-permitted) tools can execute — survives per node |
| Bounded concurrent scheduler + per-task isolation | `internal/taskagent/runtasks.go:114-202` | Slot-limited `errgroup`, per-task error isolation — run independent nodes of a layer in parallel |
| Structured node output an edge pipes | `internal/agentloop/agentloop.go:42` (`Decision`), `internal/llm/toolagent.go:36` (`ToolStep`), `internal/api/chat_agent.go:22` (`AgentStep`) | The **edge** payload |
| ctx-based tenant isolation | `internal/api/chat.go:170` (`withIdentity`) → `internal/tenancy/scope.go:28,46,87` (`WithTenant`/`TenantFromContext`/`FromContext`) | Every node inherits the already-scoped ctx; `Scope` obtainable **only** via `FromContext` |
| Scope-gated tool registry | `internal/api/chat_agent.go:234` (`Registry.Permitted`), `runtask.go:488` | Each node offered only its permitted tools |
| Circuit breaker over the client | `internal/llm/breaker.go:48` (`WithCircuitBreaker`) | Each node's LLM call goes through the same protected client |
| Durable event-pipe template | `internal/memory/outbox.go`, `internal/consolidation/sink.go:44` | Upgrade path for durable checkpoint/resume edges (not v1) |
| Façade Option builder | `aisotropy/app.go:212` (`type Option`), `app.go:270-311`, `extend.go:471-508` | Where `WithGraph(...)` slots in, matching the existing Option-function style |

The missing thing, precisely (audit §3): **no type accepts the structured output of one
`RunToolAgent`/`Loop.Run` and feeds it as the input of another agent.** The parallel fan-out
(`RunTasks`) starts every task from its own `Request.Prompt` (`runtask.go:333`) and never reads a
sibling's `Outcome` — it is a fan-out of *independent* tasks, not a graph. This ADR adds the one
missing abstraction (the edge) and the executor that honors it.

## Goals / Non-goals

**Goals**
- A **public** L2 surface for a DAG where **nodes are scoped agents** and **edges pipe one node's
  structured output into the next node's input**, composable from another Go module.
- Each node runs its OWN persona + scope set + tool subset; **no two nodes share a tenant buffer**.
- Every node inherits the request's already-scoped `context.Context`; **no node can construct or widen a
  `Scope`** (the ADR-0016 invariant, machine-enforced by test).
- Independent branches run concurrently (reuse the `RunTasks` slot-limited pattern); diamond joins wait
  for all parents; a cycle is rejected at build time.
- Fail-closed edges: a node output an edge cannot map is an error, never a silent coercion.

**Non-goals**
- **Not** a general workflow engine (no arbitrary control flow, no side-effecting steps between nodes,
  no human-in-the-loop pauses). A node is an agent; an edge is a data pipe. Nothing else.
- **Not** durable/resumable execution in v1 — in-memory piping only (consistent with ADR-0016 OQ#3,
  which keeps **one** persistence substrate; the outbox/sink pattern is the named upgrade path).
- **Not** cyclic graphs in v1 (see [Concurrency & topology](#concurrency--topology)).
- **Not** un-sealing the kernel: `tenancy`, `Scope`, `facts.ExportAll` stay `internal/` (ADR-0013 /
  ADR-0016 non-goal, unchanged).
- **Not** wiring `ext/team` or `ext/orchestrator` (see [Engine decision](#engine-decision-gollem-wrap-vs-thin-native)).

## The load-bearing invariant (inherited from ADR-0016, unchanged)

> **A composed node receives an already-scoped `context.Context` and never constructs a `Scope`.**

This is already realized: a `Scope` is obtainable **only** via `tenancy.FromContext(ctx)`
(`internal/tenancy/scope.go:87`), which reads and re-validates identity injected once at the boundary
(`internal/api/chat.go:170` `withIdentity`); the package exports no constructor from raw strings. The
graph executor is handed that ctx and threads it — **derived, never re-minted** — into every node's
`RunToolAgent` call. Therefore no `Node`, `Edge`, or `PromptFunc` a consumer writes can read or cross
tenants: the type it would need (`Scope`) is not in its reach. Isolation stays a construction-time
property, not a "don't forget the namespace" discipline.

## Decision

Add a new **`internal/graph/`** package (the executor, kernel-internal) and a thin **`aisotropy/`**
façade (`WithGraph` + the public `Graph`/`Node`/`Edge` contracts + `App.RunGraph`). A node is a scoped
`llm.RunToolAgent` run; an edge is an explicit output→input mapping; the executor topologically orders
the DAG, runs each ready layer's independent nodes concurrently through the `RunTasks` slot-limited
pattern, and blocks a node until all its in-edge sources complete.

### Public façade API (the surface an adopter writes)

Struct-as-contract, Option-function style — matching `WithTool`/`WithToolExecutor`/`WithTypedTool[T]`
(`aisotropy/app.go:270-311`, `extend.go`).

```go
package aisotropy

// GraphInput is the graph's entry payload (the request that seeds the entry node).
type GraphInput struct {
	Prompt string
	Data   map[string]any // optional structured seed, available to every node's PromptFunc
}

// NodeResult is a node's structured output — exactly what an edge pipes downstream.
// Mirrors the existing AgentStep contract (internal/api/chat_agent.go:22), re-exported.
type NodeResult struct {
	NodeID string
	Steps  []AgentStep // ordered tool decisions + results (public AgentStep alias)
	Output string      // the node agent's final text
}

// EdgePayload is the typed value an edge hands the destination node. Text feeds the
// prompt; Data carries structured fields a typed PromptFunc can read.
type EdgePayload struct {
	Text string
	Data map[string]any
}

// PromptFunc builds a node's input prompt from the graph input and the mapped payloads
// of its completed in-edges (keyed by source node ID). It is FAIL-CLOSED: returning a
// non-nil error fails the node (and the graph) rather than run the agent on a bad input.
type PromptFunc func(in GraphInput, from map[string]EdgePayload) (string, error)

// Node is a SCOPED AGENT: one RunToolAgent run with its own persona, scope set, tool
// subset, and step budget. Nodes never share a buffer; each runs under the request's
// derived, already-scoped ctx.
type Node struct {
	ID       string     // unique within the graph
	System   string     // this node's persona / system prompt
	Prompt   PromptFunc // maps graph input + in-edge payloads -> this node's input prompt
	Scopes   []string   // the ONLY scopes this node's agent runs under (⊆ request scopes)
	Tools    []string   // registered tool names this node may call (⊆ Permitted(Scopes))
	MaxSteps int        // per-node turn budget (bounds spend)
}

// Edge is a directed dependency From -> To. Map transforms the source's NodeResult into
// the EdgePayload the destination receives; nil = pass {Text: src.Output} through. A
// non-nil error from Map FAILS CLOSED — the destination never runs on a malformed input.
type Edge struct {
	From, To string
	Map      func(NodeResult) (EdgePayload, error)
}

// GraphSpec is the builder. Build() validates the topology (see below) once, up front.
type GraphSpec struct{ /* opaque */ }

func NewGraph() *GraphSpec
func (g *GraphSpec) AddNode(n Node) *GraphSpec
func (g *GraphSpec) AddEdge(e Edge) *GraphSpec
func (g *GraphSpec) SetEntry(nodeID string) *GraphSpec
func (g *GraphSpec) Build() (*Graph, error) // rejects: cycles, dangling/duplicate edges,
                                            // unknown node IDs, a node's Tools ⊄ Permitted(Scopes),
                                            // node Scopes ⊄ the graph's declared scope ceiling

// WithGraph mounts a built graph on the App under a name. Accumulates like WithTool.
func WithGraph(name string, g *Graph) Option

// App.RunGraph executes a mounted graph in-process under a scoped ctx. No HTTP route in
// v1 — façade + in-process only, matching App.RunTasks (ADR-0037). The runtime injects
// the scoped ctx (identity from RunGraph's ctx via WithDefaultTenant or the caller's
// already-identified ctx) before the first node runs.
func (a *App) RunGraph(ctx context.Context, name string, in GraphInput) (GraphResult, error)

// GraphResult carries every node's NodeResult (keyed by node ID) and the sink node's Output.
type GraphResult struct {
	Nodes map[string]NodeResult
	Output string // the terminal (sink) node's Output
}
```

### Concrete usage — researcher → writer → reviewer (output→input)

```go
research := aisotropy.Node{
	ID:     "researcher",
	System: "You research a topic and return terse bullet findings with sources.",
	Scopes: []string{"tools:search"},
	Tools:  []string{"web_search"},
	MaxSteps: 6,
	Prompt: func(in aisotropy.GraphInput, _ map[string]aisotropy.EdgePayload) (string, error) {
		return "Research this topic and list key findings:\n" + in.Prompt, nil
	},
}

write := aisotropy.Node{
	ID:     "writer",
	System: "You write a clear 3-paragraph brief from the supplied findings. No new claims.",
	Scopes: []string{}, // no tools — pure composition
	MaxSteps: 2,
	Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
		f, ok := from["researcher"]
		if !ok || f.Text == "" { // FAIL CLOSED: no upstream findings -> no writer run
			return "", fmt.Errorf("writer: missing researcher findings")
		}
		return "Write a brief from these findings:\n" + f.Text, nil
	},
}

review := aisotropy.Node{
	ID:     "reviewer",
	System: "You fact-check the brief against the findings and return APPROVED or a fix list.",
	MaxSteps: 2,
	Prompt: func(_ aisotropy.GraphInput, from map[string]aisotropy.EdgePayload) (string, error) {
		return "Findings:\n" + from["researcher"].Text + "\n\nBrief:\n" + from["writer"].Text, nil
	},
}

g, err := aisotropy.NewGraph().
	AddNode(research).AddNode(write).AddNode(review).
	AddEdge(aisotropy.Edge{From: "researcher", To: "writer"}).
	AddEdge(aisotropy.Edge{From: "researcher", To: "reviewer"}). // diamond: reviewer sees both
	AddEdge(aisotropy.Edge{From: "writer", To: "reviewer"}).
	SetEntry("researcher").
	Build() // rejects a cycle here, at build time
if err != nil { /* topology error */ }

app := aisotropy.New(
	aisotropy.WithLLM(client),
	aisotropy.WithTool(webSearchTool), aisotropy.WithToolExecutor(exec),
	aisotropy.WithGraph("brief", g),
)
res, err := app.RunGraph(ctx, "brief", aisotropy.GraphInput{Prompt: "impact of X on Y"})
// res.Output == reviewer's verdict; res.Nodes["writer"].Output == the brief
```

- **Node = scoped agent.** Each node's execution is `llm.RunToolAgent(nodeCtx, tc, tracer, node.System,
  resolvedPrompt, permittedSpecs, run, node.MaxSteps)` (`internal/llm/toolagent.go:65`) where
  `permittedSpecs = registry.Permitted(node.Scopes)` filtered to `node.Tools` — the same double
  scope-gate `chat_agent.go:234-259` uses. `researcher` may call `web_search`; `writer` and `reviewer`
  are given **no** tools and cannot escalate.
- **Edge = output→input.** `reviewer.Prompt` reads `from["researcher"]` and `from["writer"]` — the
  mapped `EdgePayload`s of its two in-edges. A missing/empty upstream is a returned error → **fail
  closed** (the reviewer agent never runs on a half-built input).

### Engine decision: gollem-wrap vs. thin-native

**Recommendation: THIN NATIVE executor over `RunToolAgent`. Do NOT wire `ext/graph` or `ext/team`.**
This refines ADR-0016's speculative "wrap `ext/graph` + `ext/team`" (ADR-0016:200) against the code the
vendored engine actually is (read for this ADR):

**`ext/graph` (`external/gollem/ext/graph/graph.go`) — a generic state machine, NOT an agent graph.**
- A `Graph[S any]` whose nodes are `func(ctx, *S) (nextNodeName, error)` (`graph.go:19-26`). It has **no
  agent concept** — you would still call `RunToolAgent` *inside* each node's `Run` yourself; the engine
  only gives control flow, fan-out+reduce, and a `maxIterations` cycle guard (`graph.go:16, 113-115`).
- It threads a **single shared mutable `*S`** through every node (`graph.go:103-104, 138`) and its
  fan-out merges branch states back into that one struct via a reducer (`graph.go:191-220`). A shared
  mutable buffer across nodes is precisely the *"no two tenants in one in-memory buffer"* shape the
  constitutional gate forbids — safe only if the caller re-imposes isolation, which the engine does not.
- Its fan-out uses **raw unbounded goroutines** (`graph.go:181-201`, `wg.Add(len(sends))`), *not* a
  slot limit — contradicting the `RunTasks` bounded-concurrency pattern the audit recommends reusing
  (`runtasks.go:148-149`), and able to swamp the provider rate limit / breaker.
- Verdict: it carries **no tenant, scope, quota, or breaker semantics** — wrapping it buys a ~130-line
  control-flow skeleton while forcing us to re-impose every gate around it. A native topological
  scheduler is comparable in size and avoids the shared-mutable-state footgun. (`ext/graph` remains
  usable, MIT-licensed, as an *internal* control-flow helper if ever wanted — but never as the public
  contract nor as the agent/tenant boundary.)

**`ext/team` (`external/gollem/ext/team/team.go`, `teammate.go`) — actively unsafe to wrap here.**
- It builds its **own** agents from a raw `core.Model`: `tm.agent = core.NewAgent[string](t.model,
  agentOpts...)` (`team.go:242`), running them via `tm.agent.Run(runCtx, ...)` (`teammate.go:340`).
  This **bypasses every aisotropy seam** that the constitutional gates live in: the `llm.ToolCaller`
  bridge, the circuit breaker (`breaker.go:48`), the quota/rate gate, the cost meter, the ext/otel
  tracing that `RunToolAgent` wires (`toolagent.go:114`), and the double scope-gate. Its worker tools
  are team-internal spawn/message/shutdown (`WorkerTools`, `team.go:211`), not the consumer's
  scope-gated registry. Wrapping it would **regress** the "quota-before-spend", "scope-gated tools per
  node", and "circuit-breaker per node" gates — the adapter would have to re-plumb all of them anyway.
- It requires an `ext/orchestrator` store (TaskStore/LeaseStore/CommandStore/ArtifactStore + recovery,
  `team.go:26-34, 97-101`) — the **second persistence substrate** ADR-0016 Open Question #3 explicitly
  **declined** on 2026-07-09 ("keep **one** consistency substrate", ADR-0016:252-262). Wiring `team`
  contradicts an already-accepted decision.
- It spawns **long-lived background goroutines** with a 24h lease TTL and a polling scheduler
  (`team.go:22-23`, `teammate.go:252-302`) that outlive a request — fundamentally wrong for a
  per-request, per-tenant runtime, and a cross-tenant state-retention hazard.

**Why native wins on the invariants (summary).** The ADR-0016 invariant and the constitutional gates
are enforced by *aisotropy* code (`RunToolAgent`'s structural scope-gate, the derived scoped ctx, the
protected client), **not** by either engine. A thin executor that (a) calls `RunToolAgent` per node,
(b) threads the derived scoped ctx, and (c) reuses `RunTasks`' slot-limited concurrency inherits all
gates *for free* and adds only the one missing piece — the edge. That is ADR-0016's "disciplined
exposure" done honestly: expose composition, keep the kernel and every gate intact.

| Criterion | Wrap `ext/graph` | Wrap `ext/team` | **Thin native (chosen)** |
|---|---|---|---|
| Node runs through `ToolCaller`/breaker/quota/otel seam | No (you re-add) | **No — bypassed** (`team.go:242`) | **Yes (`RunToolAgent`)** |
| Double scope-gate per node | Manual | **No — team tools** | **Yes (`Permitted`)** |
| ctx-scope invariant enforced | Manual | Manual | **Yes (derived ctx)** |
| Second persistence substrate | No | **Yes — declined (OQ#3)** | **No** |
| Bounded concurrency | No (raw goroutines) | Scheduler/leases | **Yes (`RunTasks` pattern)** |
| Background goroutines outlive request | No | **Yes (24h lease)** | **No** |
| New maintenance surface | wrap + re-gate | large + re-gate + store | **~1 internal pkg + façade** |

## Constitutional invariants the layer MUST respect

From [`.claude/rules/constitutional-gates.md`](../../.claude/rules/constitutional-gates.md), each is a
build/verify gate, not a guideline:

1. **Tenant isolation via `context.Context` only.** The graph executor receives the request's scoped
   ctx and derives (never re-mints) it into every node's `RunToolAgent` call; a node reads identity via
   `TenantFromContext` and obtains a `Scope` only through `FromContext`. No `Node`/`Edge`/`PromptFunc`
   accepts a `tenant_id` field as an authorization source. *(gate: "tenant_id MUST travel exclusively
   via context.Context".)*
2. **No two tenants in one in-memory buffer/goroutine scope.** Each node's goroutine holds only its own
   node's state and its in-edge payloads (all derived from the same single-tenant request). The executor
   never merges two requests; per-node isolation follows the `RunTasks` per-task model
   (`runtasks.go:84-97`). A test must prove two concurrently-run graphs for different tenants never
   cross (blocker — see plan Task 6).
3. **Scope-gated tools per node.** Each node is offered only `registry.Permitted(node.Scopes)` further
   restricted to `node.Tools`; the structural scope-gate (`toolagent.go:50-54`) means a hallucinated or
   non-offered tool name can never execute. `Build()` rejects `node.Tools ⊄ Permitted(node.Scopes)` up
   front. *(gate: scope-gated tools survive composition.)*
4. **Fail-closed structured output on edges.** An `Edge.Map` error, or a `PromptFunc` that cannot
   assemble a valid input from its in-edge payloads, **fails the node** (and the graph) — never a silent
   coercion. Where a node's own tool args are malformed, the existing typed-tool reflection signal
   (`ToolArgsError`, reflected back as the tool result, `chat_agent.go:252-255`) still applies inside
   the node. *(gate: structured output fails closed.)*
5. **Quota-before-spend + circuit breaker per node.** Every node's LLM call goes through the same
   protected (circuit-broken) client the runtime builds (`breaker.go:48`) and is subject to the
   per-tenant quota gate **before** the call — so an N-node graph cannot bypass the token budget by
   fanning out. Per-node `MaxSteps` bounds each node's turns; the graph carries a total node ceiling
   (see below). *(gate: "reject/degrade before spending tokens".)*
6. **ctx-cancellation stops the whole graph.** The executor runs under the request ctx; cancelling it
   cancels every in-flight node's `RunToolAgent` (which already respects ctx, `toolagent.go:123`) and
   stops scheduling not-yet-started nodes (the `RunTasks` cancellation contract, `runtasks.go:152-155,
   193-200`). *(gate: SSE/streaming handlers respect ctx cancellation immediately.)*
7. **License gate.** No new dependency: the executor uses `golang.org/x/sync/errgroup` (already in
   `go.mod`, BSD-3), like `RunTasks`. No copyleft/BUSL is introduced.

## Concurrency & topology

- **DAG only in v1; cycles rejected at `Build()`.** `Build()` runs a topological sort (Kahn's
  algorithm); a residual node set means a cycle → build error. Justification: (a) **cost/quota** — a
  cycle multiplies LLM spend unboundedly, in direct tension with the quota gate; (b) a single-agent
  **critic loop already has a native answer** (the reflection loop, `internal/llm/reflect.go:70`), so
  the common cyclic use case is covered without cyclic *agent* topology; (c) DAG-only keeps the
  execution and isolation story simple. A future **bounded** critic-loop (`WithMaxIterations(n)`,
  mirroring `ext/graph`'s `maxIterations` but with an explicit spend ceiling) is a named follow-on, not
  v1.
- **Layered concurrent execution.** Nodes with no unmet in-edges form the ready set; the executor runs
  them concurrently through the `RunTasks` slot-limited `errgroup` pattern (`runtasks.go:114-202`),
  bounded by a configurable limit (default `min(NumCPU, 8)`, `runtasks.go:20,205-214`). As each node
  completes, its `NodeResult` is mapped onto its out-edges and downstream nodes become ready when **all**
  their in-edges have resolved — so a **diamond join waits for all parents**.
- **Bounded blast radius.** `Build()` enforces a maximum node count (config, default e.g. 32) and each
  node a `MaxSteps` budget, so a graph's worst-case spend is statically bounded (node ceiling × MaxSteps
  × per-turn cost) — surfaced to the cost meter.
- **Per-node error isolation vs. fail-fast.** Default: a failed node fails the graph (its dependents
  cannot get a valid input → fail closed). A `WithContinueOnError`-style relaxation (record the failure
  on the `NodeResult`, let independent branches complete, like `RunTasks`' per-task `Outcome.Err`,
  `runtasks.go:74-97`) is an opt-in follow-on.

## Consequences

**Positive**
- Delivers Phase 9 lever #4's node=agent/edge=output→input slice on the **public** surface, with the
  isolation moat intact because the invariant (not visibility) guards it.
- Reuses proven substrate: the node body (`RunToolAgent`), the concurrency wedge (`RunTasks`), the
  scope-gate, the breaker — the new code is essentially the topological scheduler + the edge.
- One small internal package + a thin façade; no second persistence substrate, no background goroutines,
  no new dependency, no seal broken.

**Negative / technical debt**
- A second public composition contract (the graph surface) now exists alongside L1 — 0.x,
  additive-within-minor, same discipline as ADR-0016 mandates.
- Refines ADR-0016's stated mechanism (wrap `ext/graph`/`ext/team`). The reframing and invariant are
  untouched; the engine sub-choice moves with evidence — ADR-0016 should be cross-referenced (index
  wiring is the orchestrator's job, per repo convention).
- Cost multiplies per node — an N-node graph is N agent runs; mitigated by the node ceiling + per-node
  `MaxSteps` + the per-node quota gate, and surfaced to the cost meter.
- In-memory piping only: a process crash loses graph progress. Acceptable for v1 (matches `RunTasks`,
  ADR-0037); the outbox/sink pattern (`outbox.go`, `sink.go:44`) is the named durability upgrade if a
  consumer needs cross-restart resume (ADR-0016 OQ#3 remaining sub-question).

## Open questions

1. **Typed edges.** A generic `Edge[T]`/typed `PromptFunc` sharing the `WithTypedTool[T]` machinery
   (ADR-0016 OQ#2) — v1 ships `string` + `map[string]any`; typed edges are a follow-on once a consumer
   needs them.
2. **Bounded critic loops.** Whether/when to allow `WithMaxIterations(n)` cyclic sub-graphs with an
   explicit spend ceiling, vs. keeping critic loops as the single-agent reflection loop.
3. **Durable graphs.** The exact store-adapter shape if cross-restart resume is required — reusing the
   BadgerDB outbox / rqlite substrate, never a bundled SQLite store (ADR-0016 OQ#3).
4. **HTTP/SSE route.** v1 is façade + in-process (`App.RunGraph`), matching `RunTasks`. A streaming
   route that multiplexes node events (reusing `taskagent.Event`, `runtasks.go:38-55`) is a follow-on.

## References

- [ADR-0016 — Dual surface: sealed kernel + composition SDK](0016-dual-surface-sealed-runtime-composition-sdk.md) — this ADR delivers its lever #4 and refines the engine sub-choice.
- [ADR-0013 — Public extension surface](0013-public-extension-surface-facade-vs-promote.md) — the seal + façade, unchanged.
- [ADR-0012 — Native tool-calling](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) — `RunToolAgent`, the node body.
- [ADR-0011 — Tenant-scoped statement builder](0011-tenant-scoped-statement-builder.md) — the `FromContext`-only `Scope` the invariant relies on.
- [ADR-0037 — Concurrent sandboxed task agents](0037-concurrent-sandboxed-task-agents.md) — the `RunTasks` slot-limited concurrency reused here.
- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — struct-as-contract gate.
- [Multi-agent graph readiness audit (2026-07-17)](../research/multi-agent-graph-readiness-2026-07-17.md) — the gap + building-block catalog.
- [Phase 9 — Composition surface](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b) — the DoD this satisfies.
- [Constitutional gates](../../.claude/rules/constitutional-gates.md) — the invariants §"Constitutional invariants".
- Implementation plan: [`0060-multi-agent-graph-orchestration-plan.md`](0060-multi-agent-graph-orchestration-plan.md).
