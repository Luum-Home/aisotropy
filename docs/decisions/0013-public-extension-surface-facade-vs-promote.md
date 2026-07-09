---
type: ADR
title: "ADR-0013 — Public extension surface: façade package + builder vs. promoting internal/ packages"
description: The decision for Phase 8 (adoption surface) — how to let a third party build their bot on Aisotropy without forking. Chooses a thin importable façade package (type aliases) plus a functional-options builder over promoting the internal/ packages wholesale, preserving anti-magic, tenant-isolation-by-construction, and reconstruction-phase freedom.
tags: [decisions, adr, phase-8, extension, api, facade, builder, sdk, adoption]
timestamp: 2026-07-09T00:00:00Z
---

# ADR-0013 — Public extension surface: façade package + builder vs. promoting `internal/` packages

## Status

✅ **Accepted (2026-07-09).** Operator-approved and implemented in the same session (the importable
`aisotropy/` façade package + builder, dogfooded through `cmd/aisotropyd`, with an
`examples/weatherbot` nested module proving public-surface-only). Implements
[Phase 8 — Adoption surface](../roadmap/milestones.md#phase-8--adoption-surface-public-api-and-sdk).
Companion to [what Aisotropy is](../architecture/what-aisotropy-is.md) (which forward-references this
decision as the runtime→framework gap) and sits *above* the now-accepted
[ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) native tool-calling — which
landed keeping `ToolExecutor`'s `map[string]string` args unchanged, confirming this ADR's provisional
args decision.

## Context

Aisotropy is a hostable multi-tenant agent **runtime**, but its only extension surface today is
**wiring-level**: to ship a bot a third party must edit the composition root
[`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) and recompile the tree. Three things are
hand-wired and none is reachable from another Go module:

- **Tools** — a `tools.NewRegistry(tools.Tool{...}, ...)` literal in `main.go`.
- **Tool execution** — worse than "wired in main.go": `RegisterAgent` *hardcodes* `echoExecutor{}`
  inside [`internal/api/chat_agent.go`](../../internal/api/chat_agent.go). The
  `agentloop.ToolExecutor` interface is documented as *"the host application supplies the concrete
  implementation"* ([`agentloop.go`](../../internal/agentloop/agentloop.go)) but there is **no
  parameter** through which a host can supply it.
- **Persona / base prompt** — a package-private `systemPromptFor(productID)` in
  [`chat_stream.go`](../../internal/api/chat_stream.go) returning a hardcoded string; not injectable
  even from within the module.

Every reusable package (`llm`, `memory`, `facts`, `semantic`, `tenancy`, `routing`, `tools`,
`agentloop`, `primitives`, `costmeter`) sits under `internal/`, which Go forbids importing from
another module. So no third party can build on Aisotropy without forking.

## Goals / Non-goals

**Goals**

- Make a bot definable from **another Go module** — register a tool + a real `ToolExecutor` + a
  per-`product_id` persona **without editing `cmd/aisotropyd/main.go`**.
- Keep the promoted public contract **as small as possible** and versionable independently of the
  fast-churning `internal/` guts (the project is pre-1.0, reconstruction phase).
- Preserve the three load-bearing invariants: **anti-magic / struct-as-contract** (ADR-0002),
  **tenant isolation by construction** (ADR-0011), **sovereignty / MIT-Apache** (ADR-0001).

**Non-goals**

- Not freezing the `internal/` packages' APIs. Not shipping a config/manifest DSL for tools
  (a `ToolExecutor` is Go behavior a YAML file cannot carry, and a behavior-less declarative tool
  list is exactly the "magic" ADR-0002 warns against). Not committing to a `v1` public API now.

## Options considered

### Option A — Promote packages out of `internal/` into `pkg/` (or a second module) — discarded

Move `tools`, `agentloop`, `llm`, `facts`, `semantic`, etc. to importable paths.

Directly enables an out-of-repo consumer, but exposes **~10 packages' entire exported surface** as a
stability contract during the phase where they churn weekly, and **regresses isolation**:
`facts.Scope`/`semantic.Scope` constructors and `facts.Store.ExportAll` (the reviewed cross-tenant
read) would become callable by any consumer. Maximizes what must stay stable while handing consumers
far more than they need.

### Option B — Keep `internal/`, expose a versioned **façade** package — part of the decision

A single importable top-level package `github.com/Luum-Home/aisotropy/aisotropy` that re-exports
**only** the extension types via Go **type aliases** (`type Tool = tools.Tool`,
`type ToolExecutor = agentloop.ToolExecutor`, `type LLMClient = llm.Client`). Because aliases
preserve type identity, a consumer constructing `aisotropy.Tool{...}` gets exactly
`internal/tools.Tool` while never naming `internal/…`. The façade imports internal (same module,
allowed); the consumer imports only the façade (another module, allowed). The compat promise
attaches to one curated package; `internal/` stays free to churn.

### Option C — Declarative registration via a **builder** — part of the decision

`aisotropy.New(opts ...Option) *App` with functional options (`WithTool`, `WithToolExecutor`,
`WithPersona`, `WithLLM`, `WithRateLimit`, …) and `App.Run(ctx)` that internalizes everything
`main.go` does today (memory tiers, outbox worker, breaker, cost meter, health, graceful shutdown),
parameterized by the consumer's registrations. This is the piece that actually removes the
`main.go` edit: registration becomes options, not code in the composition root. Isolation is
*strengthened* — the builder owns constructing every `Scope` from `ctx`; the consumer never touches
scoping.

## Trade-off comparison

| Criterion | A — Promote `internal/` | B — Façade aliases | C — Builder | **B + C (recommended)** |
|---|---|---|---|---|
| Enables out-of-repo consumer | Yes | Yes (types) | Yes (removes `main.go` edit) | **Yes, fully** |
| Public contract size | ~10 whole packages | 1 thin package | option set | **1 package + options** |
| Reconstruction-phase churn freedom | Lost | Preserved | Preserved | **Preserved** |
| Tenant isolation (ADR-0011) | Regresses (`Scope`/`ExportAll` public) | Preserved (choose aliases) | Strengthened (runtime owns `Scope`) | **Strengthened** |
| Anti-magic (ADR-0002) | Neutral | Preserved (aliases are the real structs) | Preserved (literal-value options) | **Preserved** |

## Recommended decision

**Adopt B + C as a staged combination; defer A.** Ship one importable façade package `aisotropy/`
that (i) re-exports the handful of extension types via aliases and (ii) provides the builder/runtime
`App`. Refactor `cmd/aisotropyd/main.go` into a thin consumer of that same builder (dogfood), and add
an in-repo example bot as a **nested module** with its own `go.mod` — so Go's own `internal/` rule
*physically* enforces "public surface only" with no lint. Do **not** move the `internal/` packages.

Exposed surface (and only this): `Tool`, `ToolExecutor`, `LLMClient`, `Token`, `Usage`, `Persona`,
`PersonaProvider`, `TenantFromContext`/`UserFromContext` (read-only), and the builder options.
Explicitly **not** exposed: `tools.Registry`, `facts.Scope`/`NewScope`/`ExportAll`, `memory.Key`,
`internal/tenancy`, `internal/routing`, any `llm` decorator.

## Consequences

**Positive**

- A third party builds a bot from another module by supplying only *domain* (tool + executor +
  persona); they inherit every Phase-7 guarantee (quota, breaker, cost meter, tiered memory,
  isolation, graceful shutdown) for free.
- The stability burden lands on one thin, mostly-aliases package; the runtime's guts keep breaking
  freely mid-reconstruction.
- Two real internal gaps get fixed as a side effect: `RegisterAgent` becomes executor-injectable and
  persona becomes a `PersonaProvider` seam (default reproduces today's string exactly).

**Negative / technical debt**

- A new public contract now exists, in tension with the "break freely" stance. Mitigation: scope the
  promise to the `aisotropy` package only, pinned at **0.x** — additive within a minor, breaking
  allowed on a minor bump *with a CHANGELOG + migration note*. Commit to `v1` only after 2–3 real
  consumers exist.
- The façade must **hide `agentloop` internals** so [ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)'s
  native tool-calling can land *underneath* a consumer's `ToolExecutor` without breaking them.

## Open questions

1. **`args map[string]string` freeze.** `ToolExecutor.Execute` takes `args map[string]string`.
   Native tool-calling (ADR-0012) and richer tools may want typed/nested JSON. Publishing
   `map[string]string` and later needing `map[string]any`/`json.RawMessage` is a breaking façade
   change. **Decision (resolved 2026-07-09):** keep `map[string]string` for the 0.x façade. ADR-0012
   (native tool-calling) has since landed **without** widening the args contract, so no break is
   forced now; any future widening remains a documented 0.x change, not a silent one.
2. **When to promote to `v1`.** What evidence (number of external consumers, surface stability
   duration) triggers the `v1` commitment.
3. **Whether the persona seam should eventually carry more than a base prompt** (few-shots, tool
   hints) — deferred; any addition must stay literal-string / no hidden assembly (ADR-0002).

## References

- [Phase 8 — Adoption surface](../roadmap/milestones.md#phase-8--adoption-surface-public-api-and-sdk) — the DoD this satisfies.
- [What Aisotropy is (and is not)](../architecture/what-aisotropy-is.md) — the runtime→framework gap this closes.
- [ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md) — the anti-magic gate the persona/registration surface must not violate.
- [ADR-0011 — Tenant-scoped statement builder](0011-tenant-scoped-statement-builder.md) — why `Scope` construction stays runtime-owned.
- [ADR-0012 — Gollem native tool-calling vs. homegrown agentloop](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) — must land underneath the façade's `ToolExecutor`.
- Design companion (as-built): [adoption surface](../architecture/adoption-surface.md).
- Consumer guide: [build your own agent](../guides/build-your-own-agent.md).
