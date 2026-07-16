---
type: Concept
title: Concepts — the mental model
description: The one-page mental model for Aisotropy — LLM-as-OS, memory by access temperature, isolation by construction, key-brokering, and struct-as-contract — how to think about the runtime before learning the API.
tags: [concepts, mental-model, architecture, onboarding]
timestamp: 2026-07-16T00:00:00Z
---

# Concepts — the mental model

Before you learn the API, learn how to *think* about Aisotropy. There are five ideas. Hold these and
the API stops surprising you — every option and every type is one of these five made concrete. Each
section links to the architecture doc or ADR that carries the depth.

## 1. The LLM is a CPU; Aisotropy is the OS around it

Aisotropy models an agent as an **operating system whose CPU is the LLM**. The pieces map cleanly:

- **Huma** is the syscall layer — it validates the contract, extracts identity, streams responses.
- **The agent loop** is the kernel — it schedules tool calls, runs the reflection loop, dispatches.
- **The context window** is RAM; **the memory tiers** are the storage hierarchy (next section).
- **`context.Context`** is the process descriptor that isolates one tenant across the whole call chain.

You don't "prompt a model." You run a process on an OS that happens to think. Depth:
[architecture overview → the LLM-OS model](architecture/overview.md).

## 2. Memory has a temperature

There is no single database. Memory is split by **access temperature**, exactly like a computer's
memory hierarchy — the right store for each tier's latency and lifecycle:

| Tier | Analogy | What it holds | When it's read |
|---|---|---|---|
| **Working** | RAM / L1 | the last N messages + a summary buffer of the active session | hot, every turn (µs, in-process) |
| **Facts** | Disk / SSD | tasks, rules, hard preferences, summarized episodes (FTS5 keyword search) | warm |
| **Semantic** | long-term associative | embeddings of the tenant's documents and knowledge (similarity search) | cold |

A background **sleep cycle** consolidates across all three offline. When you reuse a `SessionID`
across `Turn` calls, you're keeping working memory warm; when you `IngestKnowledge`, you're filling
the semantic tier. Depth: [tiered memory](architecture/memory-tiers.md).

## 3. Isolation is by construction, not by convention

This is the load-bearing idea. A tenant's identity (`TenantID` / `UserID`) enters
`context.Context` **once**, at the boundary, and travels *only* there. Every memory scope is derived
by the runtime from that ctx — never passed as a body field, never chosen by consumer code.

The consequence is what makes it safe: a consumer can **read** identity
(`aisotropy.TenantFromContext(ctx)`) but has **no way to construct a scope** or read across tenants.
The unscoped, cross-tenant read is not "discouraged" — it is **unrepresentable** in the public API.
A different `TenantID` cannot reach this one's data because there is no code path that would let it.

Depth: [multi-tenancy](architecture/multi-tenancy.md) and
[ADR-0011: tenant-scoped statement builder](decisions/0011-tenant-scoped-statement-builder.md). The
physical side (pooled vs. dedicated stores) is
[ADR-0007](decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md).

## 4. Key-brokering: the agent gets hands, never the wallet

When an agent *acts* — runs a shell command, writes a file, commits — Aisotropy does **not** hand it
your API key. The one privileged operation that spends the key (the model call) happens **host-side,
in your process**. Only the *execution* of a tool the model chose is dispatched into a throwaway
sandbox. The sandbox holds the agent's hands; the key stays in your wallet.

This is the exact inverse of a coding-CLI-in-a-box, which must place the raw key *inside* the sandbox
because the CLI calls the model from in there. `RunTask` has no field for a provider key, by design.

Depth: [ADR-0036: sandboxed task agent key-brokering](decisions/0036-sandboxed-task-agent-key-brokering.md)
and the [threat model](architecture/threat-model.md).

## 5. The struct is the single contract (anti-magic)

Aisotropy has **no hidden templating, no opaque abstraction layer**. One Go struct is the single
source of truth. For an HTTP request, that struct drives validation *and* the OpenAPI schema *and*
the contract enforced on the model — all at once. For a tool, the same idea: a typed Go struct drives
the JSON Schema the model is shown, the decode of the model's arguments, and the fail-closed
reflection when those arguments don't fit.

A persona is a **literal string you write** — there is no template engine behind it. When you read an
option, what you see is what runs. Depth:
[ADR-0002: anti-magic, struct-as-contract](decisions/0002-anti-framework-magic-struct-as-contract.md)
and the [thesis](vision/thesis.md). The provider-agnostic LLM seam behind it is
[ADR-0009](decisions/0009-provider-agnostic-llm-layer.md).

## Where to go next

You now have the frame. Put it to work in [the Tutorial](tutorial/index.md) — the first three rungs
are exactly ideas 1–3 above (a turn, a typed tool, isolation), and the later ones are 4–5 (auth,
knowledge, key-brokered tasks, extension). Back to [Getting Started](getting-started.md).
