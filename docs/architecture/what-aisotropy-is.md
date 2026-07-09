---
type: Concept
title: What Aisotropy is (and is not) — runtime, not a library, not a bot
description: The taxonomic placement of Aisotropy in the agent-tooling space — it is a hostable multi-tenant agent RUNTIME (Letta-class), not an import-me library (LangChain/Mastra-class) and not a single finished bot. Documents the current extension model (wiring-level in cmd/aisotropyd, internal/ boundary) and the honest gap between "runtime" and "adoptable framework".
tags: [architecture, positioning, taxonomy, runtime, framework, extension, as-built]
timestamp: 2026-07-09T00:00:00Z
---

# What Aisotropy is (and is not)

A recurring question when reading this repository for the first time is: *"Is this the
**framework** on which bots are built (like Mastra or the LangChain suite), or is it a
**bot** — a worked example?"* This document answers it directly, because neither reading is
right and the confusion is worth closing once, in the repo, not per-reader.

The companion docs cover *why* ([thesis](../vision/thesis.md), [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md))
and *against whom* ([competitive landscape](../research/competitive-landscape.md)). This one
fixes *what category of thing it is* and *how you extend it today*.

## The one-line answer

**Aisotropy is a hostable, multi-tenant AI-agent _runtime_.** It is the thing you *run* to host
many tenants' agents — not a library you *import* to assemble your own app, and not a single
finished bot. Its closest category neighbor is **Letta/MemGPT (a runtime/server)**, not
LangChain or Mastra (import-me libraries/SDKs).

## The taxonomy, disambiguated

| Category | Example | You interact with it by… | Is Aisotropy this? |
|---|---|---|---|
| **Import-me library / SDK** | LangChain, Mastra, Vercel AI SDK, Genkit | `import`-ing packages and composing them inside *your* application | **No** — the packages live under `internal/` and are not importable by third parties (Go visibility rule). |
| **Hostable runtime / server** | Letta/MemGPT, Aisotropy | Running the server (`aisotropyd`) and talking to it over its HTTP contract | **Yes.** |
| **Single application / bot** | any concrete assistant with a persona + domain tools | Using the one product it is | **No** — there is no persona and no domain anywhere; every route is tenant/user/product-parametric. |
| **Managed SaaS platform** | LangSmith platform, Letta Cloud | Signing up and paying per use | **No** — it is self-hosted and sovereign by design (100% MIT/Apache, runs on your hardware). |

### Why it is not a library (LangChain/Mastra-class)

- All the reusable machinery — `internal/llm`, `internal/memory`, `internal/facts`,
  `internal/semantic`, `internal/tenancy`, `internal/routing`, `internal/tools`,
  `internal/agentloop`, `internal/primitives` — sits under **`internal/`**. Go forbids
  importing an `internal/` package from another module, so **no third party can `import` these
  today**. This is deliberate: the public surface is the *HTTP contract*, not a Go API.
- You do not compose Aisotropy into your app; you run `aisotropyd` and call its endpoints.

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

## The extension model today (honest as-built)

Aisotropy is a runtime **in intent**, but its extension surface is delivered at **wiring
level**, not as a published API. To build *your* bot on top today you edit the composition root
[`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go):

| Seam | What you do | Where |
|---|---|---|
| **Tools** | Add `tools.Tool{Name, Description, RequiredScope}` entries to `tools.NewRegistry(...)` | `main.go` (currently 3 demo tools) |
| **Tool execution** | Provide a real `agentloop.ToolExecutor` — *"the host application supplies the concrete implementation"* — replacing `echoExecutor` | `internal/agentloop` interface; wired in `main.go` |
| **Persona / base prompt** | Supply the base prompt selected by `product_id` during hydration | `internal/api` hydration path |
| **Backends** | Swap LLM provider, facts store (SQLite/rqlite), semantic store (in-memory/Qdrant) via env vars | `*.OpenFromEnv` constructors |

Every tier is an interface with swappable implementations (`llm.Client`, `facts.Store`,
`semantic.Store`, `agentloop.ToolExecutor`, `primitives.Rewriter`), so the *seams exist and are
intentional* — but wiring them is a Go edit in `main.go`, not a config file, DSL, plugin
registration, or SDK call.

## The gap between "runtime" and "adoptable framework"

What is missing for a third party to build their bot *without forking* is **not domain logic** —
it is a **public extension surface**:

- promote the extension packages out of `internal/` (or expose a stable façade), and/or
- offer declarative agent/tool/persona registration instead of hand-wiring `main.go`.

That promotion is tracked as **[Phase 8 — Adoption surface](../roadmap/milestones.md#phase-8--adoption-surface-public-api-and-sdk)**
in the roadmap. Until then, the accurate description is: *a functional multi-tenant agent
runtime with wiring-level extension points, not yet a published framework/SDK others adopt.*

## References

- Self-description: [`README.md`](../../README.md) — "A multi-tenant, local-first, sovereign AI-agent runtime."
- Anti-magic stance: [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)
- What actually boots: [runtime as built](runtime-as-built.md)
- Positioning vs incumbents: [competitive landscape](../research/competitive-landscape.md)
- Extension roadmap: [Phase 8 — Adoption surface](../roadmap/milestones.md#phase-8--adoption-surface-public-api-and-sdk)
