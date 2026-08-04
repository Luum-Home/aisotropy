---
type: ADR
title: "ADR-0016 — Dual surface: sealed runtime kernel + composition SDK over ctx-scoped nodes"
description: The decision to make Aisotropy BOTH a hostable multi-tenant runtime (A) AND a composable agent framework (B) without regressing tenant isolation — by reframing ADR-0013's single rejection into two separable axes (kernel vs composition), stating one load-bearing invariant (a composed node receives an already-scoped ctx and never constructs a Scope), layering the public surface (L1 façade / L2 composition SDK over sealed L0 kernel), and choosing MCP as the polyglot tool boundary while deferring a TypeScript authoring layer.
tags: [decisions, adr, phase-9, extension, framework, composition, multi-agent, graph, mcp, polyglot, tenancy, facade]
timestamp: 2026-07-09T00:00:00Z
---

# ADR-0016 — Dual surface: sealed runtime kernel + composition SDK over ctx-scoped nodes

## Status

✅ **Accepted (2026-07-09).** Operator-approved. **Levers #1 and #2 delivered** (green:
`go build/vet/test ./...` = 18 pkgs, `examples/weatherbot` public-surface-only): #1 typed tool args
([SDD](../roadmap/sdd/typed-tool-args.md)) — `WithTypedTool[T]` with server-side schema validation
(`enum`/`min`/`max`/`required`, reflected on violation) completing struct-as-contract for tool I/O;
#2 MCP-backed tools — `WithMCPServer(...)` exposes a stdio MCP server's tools as scope-gated,
schema-validated Aisotropy tools (polyglot tool execution, core stays Go); #3 `docker-compose.yml`
reference deployment — boots aisotropyd with rqlite + Qdrant ACTIVE so the tiered-memory moat runs by
default (image build verified, 49.9 MB; `GET /health` becomes a real network readiness probe),
sleep-cycle as an opt-in overlay; #5 declarative wiring config — `Config`/`OptionsFromConfig` wires
`addr`/limits/pricing/personas/MCP servers from YAML (deployment values only; behavior stays Go,
anti-magic). **Only #4 (L2 composition SDK) remains, intentionally deferred until 2–3 real consumers
shape it.** Extends and refines
[ADR-0013](0013-public-extension-surface-facade-vs-promote.md) (it does **not** reverse it: the
`internal/` seal and the façade+builder stay). Sits above [ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)
(native tool-calling) and depends on the isolation primitive of
[ADR-0011](0011-tenant-scoped-statement-builder.md). Governed by the anti-magic gate of
[ADR-0002](0002-anti-framework-magic-struct-as-contract.md). Implements a new
[Phase 9 — Composition surface](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b).

## Context

[What Aisotropy is](../architecture/what-aisotropy-is.md) places Aisotropy as a hostable
multi-tenant agent **runtime** (Letta-class), and ADR-0013 gave it a narrow public façade so a
third party can register a tool + executor + persona from another module. The operator's stated
product goal is stronger: **Aisotropy should be _both_** —

- **(A)** a hostable multi-tenant runtime you operate (what exists), and
- **(B)** a composable agent framework you build with — arbitrary tool topologies, graphs /
  workflows, multi-agent teams, and bots that are **not** necessarily a multi-tenant HTTP server.

ADR-0013 explicitly rejected (B)'s enabling option ("promote `internal/`") because it would make
`facts.Scope`/`NewScope`/`ExportAll` public and **regress tenant isolation** — the load-bearing
moat ([competitive landscape](../research/competitive-landscape.md): isolation-by-construction is
the one dimension no incumbent covers). That rejection was correct, but it **conflated two
different axes** under one decision:

- **Kernel axis** — tenancy, `Scope`, the memory tiers, quota, cost, breaker. Exposing this is the
  isolation regression ADR-0013 rightly refused.
- **Composition axis** — tool topology, graphs, workflows, multi-agent routing. **None of this
  needs to construct a `Scope`.** A composed node receives a `context.Context` and reads identity
  back out of it; it never mints tenant identity.

The engine for (B) is **already vendored**: `external/gollem/ext/{graph,orchestrator,team,mcp}`
provides graph composition, a durable task DAG (scheduler/runner/recovery), multi-agent teams
(`SpawnTeammate`), and an MCP client whose `CallTool(ctx, name, args map[string]any)` already
speaks typed JSON. Aisotropy simply does not expose any of it. So enabling (B) is a **disciplined
exposure** problem, not an engine-building or a seal-breaking one.

## Goals / Non-goals

**Goals**

- Let a consumer compose **arbitrary agent topology** (multiple tools, graph/workflow, multi-agent
  teams, routing) against a **public** surface, from another Go module.
- Let a consumer embed Aisotropy as a **library** (in-process `Turn`, single-tenant degenerate),
  not only operate it as a server — so (A) and (B) are literally the same artifact.
- Preserve, with zero regression, the three load-bearing invariants: **isolation-by-construction**
  (ADR-0011), **anti-magic / struct-as-contract** (ADR-0002), **sovereignty / MIT-Apache**
  (ADR-0001).
- Open a polyglot path for the part that actually blocks non-Go teams (**tool behavior**) without
  moving the core off Go.

**Non-goals**

- **Not** un-sealing the kernel: `tenancy`, `Scope`, `facts.ExportAll`, `memory.Key` stay
  `internal/` forever. This ADR does not touch ADR-0013's Option-A rejection.
- **Not** shipping a behavior DSL (a YAML that carries tool/agent *behavior* is exactly the
  ADR-0002 magic). Declarative config is scoped to **wiring**, never behavior.
- **Not** committing to a TypeScript **authoring** SDK now (see the polyglot fork below).
- **Not** building an orchestration engine — Gollem's is reused, wrapped in scoping.

## The load-bearing invariant

> **A composed node receives an already-scoped `context.Context` and never constructs a `Scope`.**

This single rule is what makes "both A and B" safe. It is already realized by
[`internal/tenancy`](../../internal/tenancy/doc.go): the only way to obtain a `Scope` is
`tenancy.FromContext(ctx)`, which reads and re-validates tenant/user identity injected once at the
handler boundary (`internal/api/chat.go` → `llm.WithTenant`/`llm.WithUser`); there is no
constructor from raw strings in that package's exported surface. Every memory access a node makes
goes through a runtime-owned accessor that calls `FromContext` internally. Therefore:

- The composition surface (L2) can be arbitrarily rich — graphs, teams, custom routing — and still
  **cannot** read or cross tenants, because no L2 type can mint or widen a `Scope`.
- Isolation stops being "don't forget the namespace" (LangChain/Mastra) and stays a
  construction-time property, exactly as ADR-0011 established for the facts tier — now extended to
  the whole composition layer.

If a proposed L2 feature *would* need to construct a `Scope` from data, that is the signal it
belongs in L0 (runtime-owned), not L2 (consumer-owned). The invariant is the design test.

## The three-layer model

| Layer | Contents | Visibility | Compat contract |
|---|---|---|---|
| **L0 — Kernel** | `tenancy`, `Scope`, memory tiers, quota, cost meter, circuit breaker, routing | `internal/`, **never public** | none — churns freely (reconstruction phase) |
| **L1 — Runtime façade** | `aisotropy.New(WithTool/WithPersona/WithToolExecutor/…).Run(ctx)` — the hosted 80% case | public, narrow | 0.x, additive-within-minor (ADR-0013) |
| **L2 — Composition SDK** | graph/workflow/team composition + in-process `Turn`, all over ctx-scoped nodes | public, rich | 0.x, additive-within-minor |

L1 is (A). L2 is (B). Both are consumers of L0; neither can reach into it. L2 is *not* a rewrite of
L1 — L1 remains the zero-config path, and an L2 graph is what a consumer reaches for when a single
tool loop is not enough.

### L2 mechanism (the exact seam, grounded)

The runtime already injects a scoped ctx at the HTTP boundary. L2 generalizes that into a
**node-execution contract** the runtime owns:

1. The runtime builds the scoped ctx once per request/turn (as `chat.go` does today).
2. It hands that ctx to the Gollem graph/team runner (`ext/graph`, `ext/team`) as the base context
   for every node/teammate.
3. A node's only Aisotropy-facing capabilities are: (a) `TenantFromContext`/`UserFromContext`
   (read-only, already public), (b) runtime-owned memory accessors that derive `Scope` via
   `FromContext` internally, (c) the consumer's own registered tools/executors. A node is handed
   **no** `Scope`, **no** store handle, **no** `ExportAll`.
4. Tool offering inside any node still passes the **double scope-gate** the agent loop enforces
   (`internal/tools.Registry.Permitted` + post-parse re-validation, preserved from ADR-0012):
   composition does not widen what a node may call.

The public L2 surface is therefore a small set of composition constructors (e.g.
`WithGraph(spec)`, `WithTeam(spec)`, a node type whose handler signature receives ctx + typed
args) plus the in-process `App.Turn(ctx, Request) (Response, error)`. The Gollem types are wrapped,
not re-exported, so `ext/*` can churn under a stable Aisotropy contract (same discipline as the L1
type aliases, but wrapped because these carry behavior).

### Embeddability / single-tenant

`App.Turn(ctx, req)` exposes one agent turn as an in-process call. Combined with a
`WithDefaultTenant(id)` option, a consumer runs Aisotropy as a **library** with `N = 1` tenant —
no HTTP, no multi-tenant surface — which answers "a bot that is not a multi-tenant server" without
a second code path: multi-tenancy degenerates rather than being bypassed. The HTTP server (L1) is
then just `Turn` behind Huma routes.

## The polyglot fork (decided here)

Two distinct asks hide under "polyglot", with very different cost and thesis-fit:

- **Polyglot tool _execution_** — tool *behavior* in any language. **ADOPTED.** Delivered via MCP:
  a registered tool may be backed by an MCP server (`ext/mcp`, already `map[string]any`) the
  consumer writes in TS/Python/anything. The runtime stays Go, owns scoping, injects the scoped
  ctx, and dispatches the call. This unblocks the part that actually gates non-Go teams (writing
  tool logic) with an on-thesis, already-vendored mechanism. Complemented by **typed client SDKs
  generated from the autogenerated OpenAPI 3.1 spec** (TS/Python clients that *talk to* a hosted
  Aisotropy — free, no core change).
- **Polyglot bot _authoring_** — a TypeScript SDK to *define* bots with Mastra-class DX.
  **DEFERRED** (open question). It is a separate, expensive bet, arguably off-thesis (the core's
  value is Go in-process + struct-as-contract), and it competes on exactly the ergonomics axis the
  competitive analysis says not to fight. If pursued, it must emit L1/L2 wiring config + MCP tools,
  never a parallel engine. Not scoped by this ADR.

## Options considered

### Option A — Reverse ADR-0013, promote `internal/` for composability — rejected

The straight path to (B), and the one ADR-0013 already rejected: it exposes `Scope`/`ExportAll` and
regresses isolation. "Both A and B" via this option buys (B) by destroying the (A) moat. Discarded
for the same reason ADR-0013 discarded it — and this ADR exists precisely to get (B) *without* it.

### Option B — Stay L1-only, tell consumers to fork for graphs — rejected

Honest but concedes the entire (B) lane to LangGraph/ADK Go/Eino for anyone needing more than a
single tool loop, and contradicts the operator's stated goal. Also wastes the already-vendored
Gollem orchestration.

### Option C — Layered dual surface (L0 sealed / L1 façade / L2 composition over ctx-scoped nodes) — chosen

Separates the two axes ADR-0013 conflated. Exposes composition richly while the kernel stays
sealed, held together by the one invariant. Reuses Gollem's engine wrapped in scoping. Adds
embeddability so (A) and (B) are one artifact. Maximizes reach at the cost of a second (still 0.x,
still narrow-per-feature) public contract to maintain.

## Trade-off comparison

| Criterion | A — Promote `internal/` | B — L1-only | **C — Layered dual (chosen)** |
|---|---|---|---|
| Enables composable framework (B) | Yes | No | **Yes** |
| Tenant isolation (ADR-0011) | Regresses | Preserved | **Preserved (invariant)** |
| Anti-magic (ADR-0002) | Neutral | Preserved | **Preserved (wiring-only config, wrapped types)** |
| Reconstruction-phase churn freedom | Lost | Preserved | **Preserved (Gollem wrapped, not re-exported)** |
| Reuses vendored engine | n/a | Wasted | **Yes (`ext/graph`/`team`/`mcp`)** |
| Non-Go tool authors | No | No | **Yes (MCP)** |
| New maintenance surface | ~10 packages frozen | none | **1 wrapped SDK, 0.x per-feature** |

## Recommended decision

**Adopt Option C.** Keep L0 sealed (ADR-0013 unchanged). Add:

1. **L2 composition SDK** — graph/workflow/team composition wrapping `ext/graph`/`ext/orchestrator`/
   `ext/team`, every node executed under a runtime-injected scoped ctx (the invariant), tools still
   double-scope-gated.
2. **Embeddable `App.Turn` + `WithDefaultTenant`** — Aisotropy as a library; single-tenant = N=1.
3. **MCP-backed tools** — polyglot tool *behavior*; OpenAPI-generated clients for polyglot callers.
4. **Wiring-only declarative config** (which tools/scopes/personas/backends/limits) — never
   behavior.

Sequence by leverage and dependency (a phase is done when verifiable by command, per the roadmap):

1. **Typed tool args** (`WithTypedTool[T]`, struct-as-contract for tool I/O, reflection loop on
   inputs) — highest leverage, lowest cost now, on-thesis; unblocks realistic tools and is a
   prerequisite for MCP arg fidelity. Specified in
   [typed-tool-args SDD](../roadmap/sdd/typed-tool-args.md).
2. **MCP-backed tools** — reuses the typed-args path; opens non-Go authors.
3. **`docker-compose` reference deployment** — lights the memory tiers (Qdrant/rqlite/sleep-cycle)
   so the moat runs by default, not only in the in-process fallback; prerequisite for real
   consumers (the perishable part of the moat, ~6–12 month window).
4. **L2 composition SDK** (graphs/teams) — the largest surface; build **after** 2–3 real consumers
   shape it, not speculatively (the classic general-framework trap).
5. **Wiring-only declarative config** — ergonomic sugar, last.

The v1 trigger of ADR-0013 (2–3 real consumers) is unblocked by steps 1–3, **not** by step 4.

## Consequences

**Positive**

- Aisotropy is genuinely (A) **and** (B): a hosted multi-tenant runtime, an embeddable library, and
  a composable framework — with the isolation moat intact because the invariant, not visibility,
  guards it.
- The vendored Gollem orchestration finally earns its place instead of sitting unused.
- Non-Go teams can author tools (MCP) and call the runtime (OpenAPI clients) without the core
  leaving Go.
- ADR-0013's isolation guarantee is not weakened; it is *extended* across the composition layer.

**Negative / technical debt**

- A second public contract (L2) now exists alongside L1, both in tension with "break freely."
  Mitigation: L2 is 0.x, additive-within-minor, and **wraps** (does not re-export) Gollem so the
  churny engine stays hidden.
- Wrapping Gollem's graph/team types is real adapter code to maintain and keep scope-correct; the
  invariant must be enforced by test (a node must be unable to obtain a `Scope`), not by review.
- Scope-explosion risk: a small project pursuing runtime + framework + polyglot can drown in
  surface. Mitigation: strict sequencing above; L2 gated on real consumers; TS authoring deferred.

## Open questions

1. **TS authoring layer** — if/when polyglot *authoring* (not just execution) is pursued, and on
   what consumer-demand evidence.
2. **L2 node handler signature** — the exact typed-args node contract (shares the `WithTypedTool[T]`
   generic from the typed-args SDD) and how per-node tool scoping composes with graph edges.
3. **Durable execution — RESOLVED (2026-07-09).** L2 wraps **`ext/graph` + `ext/team` only**
   (stateless / in-memory composition) for the first iteration. It does **not** wrap
   `ext/orchestrator`'s SQLite-backed durable DAG: doing so would introduce a **second persistence
   substrate** next to the BadgerDB transactional outbox + rqlite Raft-ACK, exactly the conflict
   ADR-0010 flagged (it declined to *adopt* orchestrator's SQLite store for this reason, and
   *wrapping* it would re-introduce the same split). Decision: keep **one** consistency substrate. If
   durable multi-step workflows are later required, back them with the **existing BadgerDB outbox /
   rqlite** via an adapter to Gollem's store interface — never a bundled SQLite store. So the L2
   surface is in-memory graphs/teams now; durability, if needed, reuses the runtime's own substrate,
   not a new one. Remaining sub-question: the exact store-adapter shape, deferred until a consumer
   needs cross-restart workflow durability.
4. **Config format** — whether wiring config is a Go literal only or also a file, and the schema.

## References

- [ADR-0013 — Public extension surface](0013-public-extension-surface-facade-vs-promote.md) — the seal + façade this extends.
- [ADR-0012 — Native tool-calling](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) — the `ToolCaller` seam the typed args and L2 nodes drive.
- [ADR-0011 — Tenant-scoped statement builder](0011-tenant-scoped-statement-builder.md) — the `FromContext`-only `Scope` the invariant relies on.
- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — the gate wiring-config and personas must not violate.
- [What Aisotropy is (and is not)](../architecture/what-aisotropy-is.md) — the A/B framing this resolves.
- [Competitive landscape](../research/competitive-landscape.md) — why the moat is isolation, not composition.
- [Phase 9 — Composition surface](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b) — the DoD this satisfies.
- [Typed-tool-args SDD](../roadmap/sdd/typed-tool-args.md) — lever #1, the first deliverable.
