---
type: implementation-plan
title: "ADR-0060 implementation plan — multi-agent graph orchestration (thin native DAG executor)"
description: Task breakdown, order, tests, and acceptance criteria for building the node=agent / edge=output→input graph executor decided in ADR-0060.
tags: [plan, phase-9, composition, multi-agent, graph, dag]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0060 implementation plan — multi-agent graph orchestration

Companion to [ADR-0060](0060-multi-agent-graph-orchestration.md). Build order, tests, and acceptance
criteria for the **thin native DAG executor over `llm.RunToolAgent`** (node=agent, edge=output→input).

## Complexity classification

**LARGE** (multi-package, new runtime capability, cross-cutting isolation concern). Per
`.claude/rules/cos/definition-of-done.md` (Large) and `constitutional-gates.md`, the following are
**BLOCKERS** — the change does not merge without them:
- **Task 6 (tenant-isolation test)** — proves two nodes / two concurrent graphs cannot cross tenants
  and that no `Node`/`Edge`/`PromptFunc` can obtain a `Scope` (the ADR-0016 invariant, machine-enforced,
  = the Phase 9 DoD item `milestones.md:207`).
- **Task 5 (fail-closed edge test)** — proves a malformed/missing upstream output fails the node closed,
  never a silent coercion.
- `go build ./... && go vet ./... && go test ./...` green; `gofmt -l` clean (ADR-0043 local CI gate).

Follows the **research-first** escape hatch as satisfied: ADR-0060 IS the operator-triaged design; this
plan is Phase 2 (implementation) with the engine decision already resolved.

## Package layout

| Path | Kind | Contents |
|---|---|---|
| `internal/graph/graph.go` | new (kernel-internal) | `Node`, `Edge`, `Spec`, `Graph`, `Build()` (topo-sort validation), `NodeResult`, `EdgePayload` |
| `internal/graph/exec.go` | new | `Execute(ctx, deps, g, input)` — layered concurrent scheduler reusing the `RunTasks` slot-limited errgroup pattern; node body = `llm.RunToolAgent` |
| `internal/graph/*_test.go` | new | topology, edge-mapping, fail-closed, concurrency, cancellation, **tenant-isolation** tests |
| `aisotropy/graph.go` | new (façade) | public `Node`/`Edge`/`GraphSpec`/`Graph`/`GraphInput`/`NodeResult`/`EdgePayload`/`GraphResult`, `WithGraph`, `App.RunGraph`; wraps `internal/graph` (types wrapped, not re-exported — same discipline as ADR-0016 L2) |
| `aisotropy/graph_test.go` | new | public-surface-only test (a consumer composes researcher→writer→reviewer via the façade) |
| `examples/graphbot/` | new (optional) | runnable public-surface-only example, mirroring `examples/weatherbot` |

Do **not** import `external/gollem/ext/graph`, `ext/team`, or `ext/orchestrator` (ADR-0060 engine
decision). The node body imports `internal/llm` (`RunToolAgent`); the scheduler mirrors
`internal/taskagent/runtasks.go` (do not add a dependency — `errgroup` is already in `go.mod`).

## Task breakdown (in order)

### Task 1 — `internal/graph` types + `Build()` topology validation
- Define `Node`, `Edge`, `Spec` (builder), `Graph` (built/frozen), `NodeResult`, `EdgePayload`.
- `Build()` validates and freezes: unknown node IDs on edges; duplicate node IDs; duplicate edges;
  a declared entry that exists; **cycle rejection** via Kahn topological sort (residual set ⇒ error);
  `node.Tools ⊄ registry.Permitted(node.Scopes)` (needs the registry — pass it into `Build` or validate
  at mount in Task 4; prefer validating tool∈scope at `Build` time against the graph's declared tool
  set, and re-validating against the live registry at mount); node-count ceiling.
- **Tests:** valid DAG builds; cycle → error; dangling edge → error; diamond builds; tool-not-in-scope
  → error; >ceiling → error.
- **AC:** `go test ./internal/graph/ -run Build` passes; a cyclic spec returns a non-nil error naming
  the cycle.

### Task 2 — node execution (single scoped agent)
- `runNode(ctx, deps, node, input, from)`: assemble the prompt via `node.Prompt(input, from)`
  (fail-closed on error); compute `specs = Permitted(node.Scopes) ∩ node.Tools`; call
  `llm.RunToolAgent(nodeCtx, tc, tracer, node.System, prompt, specs, run, node.MaxSteps)`
  (`internal/llm/toolagent.go:65`); map `[]ToolStep` → `NodeResult{Steps, Output}`.
- The `run` closure reuses the `chat_agent.go:249-259` dispatch (typed tool → `ToolArgsError`
  reflected as result; untyped → executor) so the double scope-gate and typed reflection are identical
  to the existing agent route.
- `nodeCtx` is **derived** from the request ctx (`context.WithValue`/cancel only) — never a fresh ctx,
  so identity is inherited and cannot be re-minted.
- **Tests:** a node with a stub `ToolCaller` runs its tools and returns ordered `Steps`; a node with
  empty `Tools` calls no tool; a non-offered tool name never reaches the executor (reuse the
  `toolagent` scope-gate test shape).
- **AC:** `go test ./internal/graph/ -run Node -race` passes.

### Task 3 — layered concurrent scheduler + edge piping
- `Execute(ctx, deps, g, input) (GraphResult, error)`: maintain in-degree per node; ready set = nodes
  with all in-edges resolved; run each ready layer concurrently via the `errgroup` + `SetLimit`
  slot-limited pattern (mirror `runtasks.go:148-149`); on each completion, apply each out-`Edge.Map`
  (nil ⇒ `{Text: src.Output}`) to produce the downstream `EdgePayload`, decrement dependents' in-degree,
  and enqueue newly-ready nodes. Diamond joins block until **all** parents resolve.
- Cancellation: run under the request ctx; on `ctx.Err()` stop scheduling and let in-flight nodes
  observe cancellation (mirror `runtasks.go:152-155, 193-200`).
- **Tests:** linear chain pipes output→input; diamond waits for both parents (assert reviewer sees both
  payloads); independent branches run concurrently (timing/`-race`); cancelled ctx stops the graph and
  surfaces `ctx.Err()`.
- **AC:** `go test ./internal/graph/ -race` green.

### Task 4 — façade wiring (`aisotropy/graph.go`)
- Public `Node`/`Edge`/`GraphSpec`/`GraphInput`/`NodeResult`/`EdgePayload`/`GraphResult` (wrap the
  internal types); `NewGraph()`, `AddNode`, `AddEdge`, `SetEntry`, `Build()`; `WithGraph(name, g)`
  (accumulates on `App`, like `WithTool`, `app.go:270`); `App.RunGraph(ctx, name, in)` builds/gets the
  scoped runtime (reuse `App.Turn`'s `startRuntime`/scoped-ctx path, `app.go:404`) and calls
  `internal/graph.Execute` with the runtime's protected client (`breaker.go:48`), registry, executor,
  typed handlers, and tracer.
- Inject identity: `RunGraph` derives the scoped ctx from its argument ctx (or `WithDefaultTenant` for
  the single-tenant/embedded case), exactly as `Turn` does — the executor never mints a `Scope`.
- **Tests:** `aisotropy/graph_test.go` composes researcher→writer→reviewer through the **public surface
  only** (like `examples/weatherbot`) with a stub LLM and asserts `res.Output` is the reviewer's.
- **AC:** `go test ./aisotropy/ -run Graph` passes; the test imports nothing from `internal/`.

### Task 5 — fail-closed edge test (BLOCKER)
- Prove: (a) an `Edge.Map` returning an error fails the destination node and the graph (no downstream
  run); (b) a `PromptFunc` returning an error (e.g. missing upstream payload) fails closed; (c) a node
  whose upstream produced empty output and whose `PromptFunc` rejects it never issues an LLM call
  (assert the stub client's call count for the downstream node == 0).
- **AC:** `go test ./internal/graph/ -run FailClosed` passes; assertion that the downstream agent was
  never invoked on a malformed input.

### Task 6 — tenant-isolation + no-Scope test (BLOCKER, = Phase 9 DoD `milestones.md:207`)
- Prove the ADR-0016 invariant is **machine-enforced**:
  1. Two graphs run concurrently under two different tenant ctxs (`tenancy.WithTenant`,
     `internal/tenancy/scope.go:28`); assert each node only ever observes its own tenant via
     `TenantFromContext` (instrument a tool/executor that records the tenant it saw) — no cross-tenant
     observation, under `-race`.
  2. A node's `PromptFunc`/`Edge.Map`/executor is handed **no** `Scope`, **no** store handle — assert by
     API shape (the public `Node`/`Edge` signatures expose none) and by a compile-fenced test that a
     node body cannot call `tenancy.FromContext` to widen scope beyond its ctx identity (it gets back
     only its own tenant's scope).
  3. The executor threads a **derived** ctx (assert the node ctx's tenant == the request ctx's tenant
     for every node, and differs across the two concurrent graphs).
- **AC:** `go test ./internal/graph/ -run Isolation -race` passes; a reviewer can point to the test as
  proof the invariant holds by construction, not by review.

### Task 7 — docs + example + ADR cross-refs
- Add `examples/graphbot/` (public-surface-only, runnable) mirroring `examples/weatherbot`.
- Update `docs/roadmap/milestones.md` Phase 9 lever #4 and the two graph DoD checkboxes once green.
- Add a component doc `docs/components/graph.md` (or a section in `docs/architecture/adoption-surface.md`)
  describing the node/edge/executor and the engine decision. Cross-reference ADR-0060 from ADR-0016.
  (Index wiring `docs/decisions/index.md` is the orchestrator's job — do not edit it here.)
- **AC:** `examples/graphbot` builds and runs against the stub; docs updated; `gofmt -l` clean.

## Acceptance criteria (whole change)

1. `go build ./... && go vet ./... && go test ./... && gofmt -l .` all green (ADR-0043 local CI gate).
2. **Tenant isolation (blocker):** `go test ./internal/graph/ -run Isolation -race` passes; two
   concurrent graphs for different tenants never cross; no node can obtain a widened `Scope`.
3. **Fail-closed edges (blocker):** `go test ./internal/graph/ -run FailClosed` passes; a malformed
   upstream output never reaches the downstream agent.
4. **Public-surface composition:** a consumer builds a 3-node researcher→writer→reviewer graph via
   `aisotropy.WithGraph` + `App.RunGraph`, importing nothing from `internal/` (mirrors
   `examples/weatherbot`) — satisfies Phase 9 DoD `milestones.md:206`.
5. **Scope-gating per node:** a node offered only `node.Tools ⊆ Permitted(node.Scopes)`; a
   non-offered/hallucinated tool name can never execute (reuse the `toolagent` scope-gate assertion).
6. **Cost/quota bound:** every node's LLM call routes through the protected (circuit-broken) client and
   the per-tenant quota gate; a graph's worst-case spend is statically bounded (node ceiling × MaxSteps).
7. **Cancellation:** cancelling `RunGraph`'s ctx stops in-flight nodes and unscheduled nodes.
8. **No new dependency, no seal broken, no `ext/graph`/`ext/team`/`ext/orchestrator` import.**

## Out of scope (named follow-ons)
- Typed edges (`Edge[T]`), bounded critic-loop cycles (`WithMaxIterations`), durable/resumable graphs
  (outbox-backed), an HTTP/SSE graph route (multiplexed node events), and `WithContinueOnError`
  per-branch tolerance. Each is an open question in ADR-0060 §Open questions.
