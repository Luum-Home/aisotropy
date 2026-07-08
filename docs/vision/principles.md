---
type: Concept
title: Design principles (Karpathy + OKF)
description: The principles that govern both the framework and this documentation — the LLM OS model, context engineering, knowledge-base-over-RAG, and the Open Knowledge Format.
tags: [vision, principles, karpathy, okf, context-engineering]
timestamp: 2026-07-08T00:00:00Z
---

# Design principles

Two lines of thinking govern this project — and, deliberately, also govern *how this
knowledge base is written*.

## Andrew Karpathy

### 1. The "LLM OS" model
Karpathy describes the LLM as the **CPU of a new kind of operating system**, where the
context window is the **RAM** and everything else (tools, long-term memory, other
processes) is orchestrated peripherals. Aisotropy adopts this literally: see the full
mapping in [architecture/overview](../architecture/overview.md#llm-os) and
[tiered memory](../architecture/memory-tiers.md).

- **Gollem** = kernel (agent loop, scheduling of the cycle with the LLM).
- **Huma** = syscall layer (contract, validated input/output).
- **`context.Context`** = process descriptor (isolates tenant/user, propagates timeouts and quotas).
- **Memory** = RAM→disk→long-term hierarchy (BadgerDB→rqlite→Qdrant).

### 2. Context engineering > prompt engineering
The art isn't "writing the perfect prompt"; it's **curating what enters the context
window**. The entire design of tiered memory, the Summary Buffer, RAG with reranking, and
LLM routing exists for one thing: making sure Gollem receives a **surgical, minimal**
context. Every token that doesn't contribute is cost and noise that induces hallucination.

### 3. Curated knowledge base > RAG (for stable knowledge)
Karpathy popularized the **"LLM-wiki"** pattern: for stable, structured knowledge, a
curated, cross-linked markdown base that the LLM reads directly beats embedding-based RAG
(which chunks, loses structure, and brings in noise). Aisotropy applies this on two levels:

- **In the product:** vector RAG ([Qdrant](../components/qdrant-semantic-memory.md)) is
  reserved for the tenant's massive/unstructured knowledge; the user's stable *facts and
  rules* live as curated text searchable via FTS5 ([rqlite](../components/rqlite-facts-memory.md)),
  Engram-style. Not everything is a vector.
- **In this doc:** see OKF, below.

### 4. Software 2.0/3.0 and "keeping the AI on a leash"
Behavior is specified in English plus schemas, but **on a leash**: forced structured
outputs, guardrails, reflection loop, eval-driven. Autonomy is a *slider*, not a default —
human-in-the-loop wherever the cost of error is high (see
[granular control](../architecture/granular-control.md)).

## Open Knowledge Format (OKF)

**OKF** (Google Cloud, v0.1) is the **formalization of the LLM-wiki pattern**: an open,
portable, vendor-neutral specification for representing curated knowledge that serves both
humans and agents. It's "just markdown, just files, just YAML frontmatter" — no
proprietary SDKs or runtimes.

We adopt OKF as the **format for this knowledge base** (and as the reference pattern for
the product's facts memory). Its rules:

| OKF rule | How we apply it |
|---|---|
| Bundle = directory of markdown with YAML frontmatter | All of `docs/` is the bundle. |
| `type` field mandatory; `title`/`description`/`tags`/`timestamp` standard | Frontmatter in every file. |
| `index.md` per level (progressive disclosure) | Every folder has its summary index. |
| Relationships via markdown cross-references → knowledge graph | We link concepts with relative links. |
| Producer/Consumer independence; format, not platform | Renders on GitHub, readable by any agent, zero lock-in. |
| Optional `log.md` for chronological history | The [ADRs](../decisions/index.md) fill that role as a decision log. |

**Why the meta-move matters:** documenting a memory/knowledge framework *as an open
knowledge base* is dogfooding. If the OKF/LLM-wiki pattern is good enough for an agent to
consume as its KB, it's good enough for our own docs.

## Derived operating principles

1. **Zero magic** — if you don't know what string was sent, it's a design bug.
2. **The struct rules** — one definition, three uses (HTTP, OpenAPI, LLM schema).
3. **Isolation via `ctx`** — `tenant_id`/`user_id` travels natively, never in global state.
4. **Honest eventual consistency** — nothing synchronous while the user waits; see
   [consistency](../architecture/consistency.md).
5. **Sovereignty** — MIT/Apache-2.0 only, in-process by default, network only when needed.
6. **Observable by design** — one OTel Span per interaction, with attributable per-token cost.
