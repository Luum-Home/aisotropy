# graphbot — the smallest multi-agent graph on Aisotropy

`graphbot` is the multi-agent counterpart of [`weatherbot`](../weatherbot): a
**researcher → writer → reviewer** DAG built entirely on the public façade
(`github.com/Luum-Home/aisotropy/aisotropy`), per
[ADR-0060](../../docs/decisions/0060-multi-agent-graph-orchestration.md).

- A **node** is a scoped agent: its own persona, scope set, tool subset, and step
  budget. Each node is one `RunToolAgent` run.
- An **edge** pipes one node's structured output into the input of the next
  (`output → input`). A `PromptFunc` that cannot assemble a valid input **fails
  closed** — the downstream agent never runs on a half-built payload.
- The graph is a **diamond**: `researcher` feeds both `writer` and `reviewer`, and
  `writer` also feeds `reviewer`, so the reviewer waits for **both** parents.
- Every node inherits the run's already-scoped `context.Context`; **no node can
  construct or widen a tenant `Scope`** (the ADR-0016 invariant).

## Run it (keyless, no Docker)

```bash
cd examples/graphbot
go run .
```

With no `ANTHROPIC_API_KEY` the credential-less stub drives the nodes, so the
output is a deterministic walk of the graph that shows the `output → input`
piping. The stub is not a native tool-caller, so `researcher` records 0 tool
steps; point `WithLLM` at a real provider (or any `aisotropy.ToolCaller`) to see
genuine tool use and prose.

## Why a separate module

Like every example here, `graphbot` is its own Go module with a `replace`
directive to the repo root. Go's internal-package rule physically forbids it from
importing `aisotropy/internal/...`, so the fact that it compiles proves it uses the
**public surface only** — the Phase 9 composition DoD.

## The public surface it uses

- `aisotropy.NewGraph()` / `AddNode` / `AddEdge` / `SetEntry` / `Build` — build and
  validate the DAG (cycles are rejected at `Build`).
- `aisotropy.Node`, `aisotropy.Edge`, `aisotropy.GraphInput`, `aisotropy.EdgePayload`,
  `aisotropy.NodeResult`, `aisotropy.PromptFunc` — the composition contracts.
- `aisotropy.WithGraph(name, g)` — mount a graph on the `App`.
- `App.RunGraph(ctx, name, in)` — run it in-process under a scoped ctx.
- `aisotropy.WithGraphIdentity(ctx, tenant, user)` — pre-scope a ctx for
  multi-tenant runs (omitted here → the single-tenant embedded default).
