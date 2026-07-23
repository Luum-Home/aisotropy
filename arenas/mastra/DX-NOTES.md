---
type: Developer notes
title: "Mastra — DX notes"
description: Honest developer-experience assessment of Mastra from the perspective of building a multi-tenant production agent.
tags: [arenas, mastra, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# Mastra — DX Notes

## The good

### Most batteries-included TS framework
Memory, vectors, tools, streaming, structured output — all built-in. You don't need to choose
between three libraries for each concern. This is Mastra's biggest strength: **less wiring,
more agent-building**.

aisotropy achieves the same outcome through the builder pattern (`New(WithLLM(...), WithMemory(...))`),
but Mastra's DX is more cohesive because it's all in one ecosystem.

### Four real scoping levers — closest to construction
Mastra's memory scoping is the most authoritative of the three competitors. It gives a competent
developer four levers: `resourceId` (owner partition, with thread↔resource ownership validation),
`threadId` (session partition), working-memory `scope: 'resource'|'thread'`, and
`semanticRecall.filter` (MongoDB-style metadata filter over the vector store — the docs' own
`filter: { projectId: { $eq: 'project-a' } }`). Applied, they isolate tenants, users, and products
correctly (run-verified in `scripts/product_isolation_demo.ts`).

aisotropy goes further on the axis that matters: `Scope{}` value objects with unexported fields,
built from verified identity and auto-propagated via `context.Context`, make a wrong or forgotten
axis **unrepresentable** and filter it server-side on every read. Mastra's levers are real but
convention — each is a per-call responsibility, and the default (axis in no lever) silently does not
partition. That per-call convention is exactly the surface `mastra#11856` slipped through.

### Memory processors — cognitive efficiency
Automatic trimming of retrieved messages before they hit the context window. This is a real
cognitive layer feature — reduces token spend by discarding low-signal history.

aisotropy's equivalent is the compression bet (#6: bits/token) + surgical context assembly,
but Mastra implements this at the framework level rather than requiring manual configuration.

### Apache 2.0 license
Relicensed from ELv2 after enterprise pushback — this is empirical market proof that permissive
licensing matters for adoption. aisotropy's 100% permissive (MIT/BSD/Apache-2.0/ISC), zero-copyleft
stance is validated by Mastra's experience.

## The painful

### Largest build of the three (~1240 lines)
Writing the AI-SDK v5 model stub by hand was the most frustrating part. The abstraction layer
is thick — you spend more time fighting the framework's internals than building agent logic.

aisotropy's sealed engine is larger overall, but the public façade (`aisotropy/`) is thin and
predictable — you build against a stable interface, not framework internals.

### TypeScript ecosystem is young
Tooling, docs, and community are still maturing. Compared to LangGraph's ecosystem (largest in
the space) or Pydantic AI's polished DX, Mastra feels like the newcomer.

aisotropy's Go ecosystem is smaller than Python/TS but more mature for infrastructure — the
language itself enforces stricter patterns that help with long-lived projects.

### mastra#11856 — the convention gap, illustrated by a historical bug
Even the framework with the most authoritative keys shipped this bug (since fixed): `listMessages`
ignored `resourceId` and filtered only by `threadId`, crossing tenants that shared a thread. It is
the concrete face of the convention gap — one forgotten filter on one path. Because Mastra's scoping
is a per-call convention rather than an auto-propagated identity axis, the surface for this class of
mistake exists framework-wide. aisotropy's value-object `Scope` (unexported fields, server-derived,
carried via `context.Context`) filters the axis on every read, so the same mistake cannot compile.

### Partial built-in, partial DIY
Memory and vectors are built-in, but facts journal, cost governance, and observability are DIY.
This "halfway" state is worse than both extremes: you get Mastra's complexity without getting
aisotropy's completeness.

## The verdict

Mastra is the **most complete TS framework** of the three — but "complete" is relative. It has
the best memory story (resource-scoped + vector recall + processors) but still lacks facts
memory, cost governance, and observability.

**Best for:** TS teams that want batteries-included agent building with decent memory
management and are willing to accept that isolation is convention, not construction.

**Worst for:** Teams building regulated multi-tenant SaaS where cross-tenant leaks are
unacceptable — you'll need to add your own guards on top of Mastra's convention.

**vs. aisotropy:** Mastra wins on TS ergonomics and partial built-ins; aisotropy wins on
isolation-by-construction, complete built-in stack, and sovereignty. The trade is: polished
TS framework with gaps vs. complete Go runtime with constitutional guarantees.
