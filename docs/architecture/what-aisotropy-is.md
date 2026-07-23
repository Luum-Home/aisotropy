---
type: Concept
title: What Aisotropy is (and is not) — a dual-surface runtime + library, not a bot
description: The taxonomic placement of Aisotropy in the agent-tooling space — since Phase 8/9 (ADR-0013/0016/0031) it is a DUAL surface: a hostable multi-tenant agent RUNTIME (Letta-class) AND an importable Go library (via the public aisotropy/ façade + builder), with only the isolation-critical kernel sealed under internal/. It is still not a single finished bot. Documents the shipped extension model (public façade, App.Turn/App.Run) and what composition-SDK work remains.
tags: [architecture, positioning, taxonomy, runtime, framework, extension, as-built]
timestamp: 2026-07-09T00:00:00Z
---

# What Aisotropy is (and is not)

> **Status note (reconciled 2026-07-17).** This doc was first written at Phase 7 (2026-07-08), when the
> only public surface was the HTTP contract. **Phases 8–9 changed the taxonomy**: Aisotropy is now a
> **dual surface** — a sealed multi-tenant runtime **and** an importable Go library
> ([ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md),
> [ADR-0016](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md),
> [ADR-0031](../decisions/0031-embeddable-in-process-turn.md)). The public `aisotropy/` façade + builder
> ships (`go get …/aisotropy`, `aisotropy.New`, `app.Turn`, `app.Run`); only the isolation-critical
> kernel stays under `internal/`. The "not a library / wiring-level extension only" passages below are
> corrected inline; where any tension remains, the [reality ledger](reality-ledger.md) and the
> [README](../../README.md) are the arbiters.

A recurring question when reading this repository for the first time is: *"Is this the
**framework** on which bots are built (like Mastra or the LangChain suite), or is it a
**bot** — a worked example?"* This document answers it directly, because neither reading is
right and the confusion is worth closing once, in the repo, not per-reader.

The companion docs cover *why* ([thesis](../vision/thesis.md), [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md))
and *against whom* ([competitive landscape](../research/competitive-landscape.md)). This one
fixes *what category of thing it is* and *how you extend it today*.

## The one-line answer

**Aisotropy is a hostable, multi-tenant AI-agent _runtime_ that is also an importable Go library**
(a dual surface, ADR-0016). You can *run* it to host many tenants' agents over its HTTP contract, **or
`import` the public `aisotropy/` façade and call it in-process** (`app.Turn`) — what stays sealed is the
isolation-critical kernel, not the whole codebase. It is still **not a single finished bot** (no persona,
no domain). Its category neighbors are **Letta/MemGPT** (runtime/server) on the hosting axis and the
composable SDKs (LangChain/Mastra) on the import axis — Aisotropy is deliberately both.

## The taxonomy, disambiguated

| Category | Example | You interact with it by… | Is Aisotropy this? |
|---|---|---|---|
| **Import-me library / SDK** | LangChain, Mastra, Vercel AI SDK, Genkit | `import`-ing packages and composing them inside *your* application | **Yes, since Phase 8/9** — the public `aisotropy/` façade + builder is importable (`aisotropy.New`, `WithTypedTool`, `App.Turn`); only the isolation-critical kernel stays sealed under `internal/`. |
| **Hostable runtime / server** | Letta/MemGPT, Aisotropy | Running the server (`aisotropyd`) and talking to it over its HTTP contract | **Yes.** |
| **Single application / bot** | any concrete assistant with a persona + domain tools | Using the one product it is | **No** — there is no persona and no domain anywhere; every route is tenant/user/product-parametric. |
| **Managed SaaS platform** | LangSmith platform, Letta Cloud | Signing up and paying per use | **No** — it is self-hosted and sovereign by design (100% permissive license, zero copyleft, runs on your hardware). |

### The sealed kernel vs. the importable façade

- The isolation-critical **kernel** — `internal/llm`, `internal/memory`, `internal/facts`,
  `internal/semantic`, `internal/tenancy`, `internal/routing`, `internal/tools`,
  `internal/agentloop`, `internal/primitives` — stays under **`internal/`**, so no third party can
  `import` those packages directly (a consumer brings their own tools/backends but never their own
  tenant-isolation).
- The **public surface is a Go API too**, not only the HTTP contract: the importable `aisotropy/`
  façade re-exports the extension seams as type aliases plus a functional-options builder
  (ADR-0013/0016). So you can **either** run `aisotropyd` and call its endpoints **or** import
  `aisotropy` and compose it into your Go app (`app.Turn` in-process, `app.Run` over HTTP).

### Why it is not a single bot (example-case reading)

- The HTTP contract is generic and tenant-parametric:
  `POST /v1/tenant/{tenant_id}/user/{user_id}/chat` with a bare
  `{prompt, product_id, scopes} → {reply}` shape ([`internal/api/chat.go`](../../internal/api/chat.go)).
  There is no persona, no system prompt hardcoded to one assistant, no domain.
- It carries platform machinery that only makes sense for hosting *many* agents: physical
  tenant-tier routing (`AISOTROPY_DEDICATED_TENANTS` → per-tenant facts/semantic resources via
  [`internal/routing`](../../internal/routing/router.go), ADR-0007), per-tenant quota
  ([`internal/memory`](../../internal/memory/quota.go)), per-tenant cost metering
  ([`internal/costmeter`](../../internal/costmeter/costmeter.go)), and construction-time tenant
  isolation ([`internal/tenancy`](../../internal/tenancy/doc.go)).

### The only "example" residue

The three tools wired in [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go)
(`search_docs`, `read_financials`, `write_tasks`) and the `echoExecutor` in
[`chat_agent.go`](../../internal/api/chat_agent.go) are **placeholders**, not a real bot. The
executor only echoes the invocation; the code says so plainly (*"Real tools replace it"*). They
exist so the endpoints demonstrate scope-gated tool injection end-to-end — not to ship a
domain.

## The extension model today (Phase 8+9 — shipped)

A third party builds *their* bot **from another Go module** by importing the public `aisotropy/`
façade and supplying only *domain* (tools + executor + persona) — no fork, no `main.go` edit. The
reusable engine stays sealed under `internal/`; the façade re-exports only the extension seams as type
aliases plus a functional-options builder ([adoption surface](adoption-surface.md)):

| Seam | What you do | Public API |
|---|---|---|
| **Tools** | Register a typed Go function as a tool (its `jsonschema` tags drive the model's schema + arg decode) | `aisotropy.WithTypedTool[T](...)` ([`aisotropy/tools.go`](../../aisotropy/tools.go)) |
| **Tool execution** | Supply a real `ToolExecutor` (the host's concrete implementation) | `aisotropy.WithToolExecutor(...)` |
| **MCP tools** | Attach an MCP server's tools | `aisotropy.WithMCPServer(...)` ([`aisotropy/mcp.go`](../../aisotropy/mcp.go)) |
| **Plugins** | Load a declarative `aisotropy-plugin.yaml` (mcp / go-import / wasm) | `aisotropy.WithPlugin` / `WithPluginDir` ([`aisotropy/extend.go`](../../aisotropy/extend.go), ADR-0032) |
| **Persona / base prompt** | Supply the persona selected by `product_id` | `aisotropy.WithPersona(...)` |
| **Backends** | Swap LLM provider, facts store (SQLite/rqlite), semantic store (in-memory/Qdrant), embedder | builder options + `*.OpenFromEnv` env |
| **Config-file wiring** | Wiring-only YAML/JSON instead of code | `aisotropy.OptionsFromConfigFile(...)` ([`aisotropy/config.go`](../../aisotropy/config.go)) |

Two entry points on the same runtime value: `app.Turn(ctx, in)` runs one full pipeline in-process (no
HTTP hop, [ADR-0031](../decisions/0031-embeddable-in-process-turn.md)); `app.Run(ctx)` serves the same
runtime behind the HTTP contract. `cmd/aisotropyd/main.go` is itself a ~97-line consumer of this public
surface — the runtime is dogfooded through its own API.

## What still stays sealed (by design) vs. what is pending

The one thing sealed on purpose is the **isolation-critical kernel** (tenancy/`Scope`/memory tiers):
`internal/` packages remain un-importable so a consumer can bring their own tools/backends but never
their own tenant-isolation ([ADR-0016](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md)).
What is still **pending** is the broader **L2 composition SDK** — graphs/teams/workflows over
ctx-scoped nodes (ADR-0016 lever #4), tracked in
**[Phase 9 — Composition surface](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b)**.
The accurate description today: *a multi-tenant agent runtime that is also an importable, embeddable
library, with the higher-order composition SDK still to come.*

## References

- Self-description: [`README.md`](../../README.md) — "A multi-tenant, local-first, sovereign AI-agent runtime."
- Anti-magic stance: [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)
- What actually boots: [runtime as built](runtime-as-built.md)
- Positioning vs incumbents: [competitive landscape](../research/competitive-landscape.md)
- Extension roadmap: [Phase 8 — Adoption surface](../roadmap/milestones.md#phase-8--adoption-surface-public-api-and-sdk)
