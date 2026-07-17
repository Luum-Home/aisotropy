---
type: Developer notes
title: "Letta (MemGPT) — DX notes"
description: Honest developer-experience assessment of Letta from building a multi-tenant Orbit agent against the real letta-client 1.12.1 SDK.
tags: [arenas, letta, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# Letta — DX Notes

> Written after actually installing `letta-client==1.12.1`, wiring Orbit against its
> real API, and running the isolation matrix offline against its real types.

## Installing the real thing

- `pip install letta-client` is **clean and lightweight** on Python 3.13 — a pure-Python
  Stainless SDK (`anyio`, `httpx`, `pydantic`, `sniffio`, `typing-extensions`). No native
  build, no DB. It installs in seconds and its Pydantic types (`BlockResponse`,
  `AgentState`, `Passage`, the response-format schemas) are importable and constructible
  **without a server** — great for typed offline work.
- `pip install letta` (the OSS **server**) is a different animal: it resolves to
  `letta-0.16.8` and pulls **~200 transitive deps** (SQLAlchemy, alembic, FastAPI,
  temporalio, onnxruntime, llama-index, provider SDKs). It resolves on 3.13, but then you
  need a running `letta server` + a database + an LLM provider key before a single agent
  can take a step. This is the "Letta is a server, not a library" reality in one command.

## The good

### Memory hierarchy is isomorphic to aisotropy — and the types are real
Core Memory (`Block`) ≈ BadgerDB working memory. Recall Memory (message history) ≈
SQLite+FTS5 facts memory. Archival Memory (`Passage`) ≈ Qdrant semantic memory. Building
Orbit's memory layer against `client.blocks` / `client.passages` felt like a direct port of
aisotropy's tiers. Powerful validation that the memory-OS thesis is correct.

### Sleep-time compute — productized
`agents.create(enable_sleeptime=True)` ships the paired background consolidation agent —
the direct analog of aisotropy's DataFusion sleep cycle, but Letta has it in the API today
while aisotropy's is dormant behind `AISOTROPY_SLEEPCYCLE_BIN`.

### The SDK is well-typed and self-documenting
Because it's Stainless-generated, every resource (`agents`, `blocks`, `passages`,
`archives`, `identities`) and every param is a typed Pydantic model. Discovering the real
multi-tenancy levers was just reading `BlockListParams` / `AgentCreateParams` — no guessing.

## The painful

### Nothing runs without a server (and a provider key)
The SDK is a REST client. `Letta(base_url=...)` is mandatory to do anything with an agent;
there is no embedded/in-process mode in the client. For an offline, keyless CI smoke you
either stand up the full server stack or model the data plane yourself. We did the latter,
explicitly and with the real types — but it's friction aisotropy simply doesn't have (its
stub LLM + in-process stores run with zero external services).

### Multi-tenancy is single-org — isolate per-agent, map the caller yourself
The honest shape: OSS Letta is a **single org**. Two read surfaces, and you have to know the
difference. The **org-wide** primitives (`client.blocks.list()` with no arguments returns
**every** block in the org; `client.blocks.retrieve(id)` does **no** tenant check) are
one keyword argument from a cross-read, and the type system does nothing to stop you. The
**agent-scoped** subresources (`client.agents.blocks.list(agent_id)`,
`client.agents.passages.search(agent_id)`) DO isolate — they require an `agent_id` and never
span agents — so Letta's documented pattern is **one agent per principal**, with your app
mapping verified caller → `agent_id`. That works, but the binding is app-level, not
framework-level: nothing forbids a developer from reaching for the org-wide primitive, and
there's no must-agree bind from an authenticated caller to a tenant. Per-*user* key isolation
only appears in Letta Cloud (per-project keys); the older per-principal **identities** are
being deprecated in favour of per-agent scoping. For regulated multi-tenant SaaS, the fact
that the unsafe primitive is always one call away is a real liability.

### LLM-self-managed memory — probabilistic, not deterministic
Memory is edited by the LLM via tool calls — emergent, hard to audit. aisotropy's memory is
runtime-hydrated: deterministic, auditable, developer-controlled. For "prove exactly what
data was read and when," Letta's approach is a liability; aisotropy's is an asset.

### Python server-side objects, not in-process µs
Agents are durable server-side objects behind a network hop and a database. aisotropy's
in-process µs working-memory class is a different performance paradigm.

### No cost governance; observability is server-level only
No pre-spend gate, no per-tenant meter. The server ships OTel exporters but there's no
per-tenant `/costs`/`/health` product surface. DIY for multi-tenant billing.

## The verdict

Letta is the **memory-OS validator** — isomorphic memory hierarchy and productized
sleep-time compute prove the concept works, and the real SDK is pleasant to build against.
But it's Python, server-bound, LLM-driven (probabilistic), and convention-based on tenancy.

**Best for:** teams that want productized memory consolidation, can run the server, and are
comfortable with LLM-driven memory. Single-tenant or trusted multi-tenant.

**Worst for:** regulated multi-tenant SaaS where deterministic memory,
isolation-by-construction, and cost governance are non-negotiable.

**vs. aisotropy:** Letta validates the memory-OS thesis; aisotropy delivers it with Go
performance, deterministic memory, and constitutional isolation. The trade: productized
memory-OS with LLM uncertainty vs. deterministic runtime with structural guarantees.
