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

### `resourceId`/`threadId` scoping — closest to construction
Mastra's memory scoping is the most authoritative of the three competitors. `resourceId` acts
as a tenant-level partition, and `threadId` as a session-level partition. The ownership
validation makes it harder to accidentally cross tenants than Pydantic AI's `RunContext`.

aisotropy goes further: `Scope{}` value objects with unexported fields make cross-tenant reads
**unrepresentable** in type system. Mastra gets close but not all the way.

### Memory processors — cognitive efficiency
Automatic trimming of retrieved messages before they hit the context window. This is a real
cognitive layer feature — reduces token spend by discarding low-signal history.

aisotropy's equivalent is the compression bet (#6: bits/token) + surgical context assembly,
but Mastra implements this at the framework level rather than requiring manual configuration.

### Apache 2.0 license
Relicensed from ELv2 after enterprise pushback — this is empirical market proof that permissive
licensing matters for adoption. aisotropy's 100% MIT/Apache stance is validated by Mastra's
experience.

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

### mastra#11856 — real cross-tenant leak
Even the closest framework to isolation-by-construction had a production bug: `listMessages`
ignored `resourceId` and only filtered by `threadId`, leaking data across tenants sharing a
thread. This is the class of bug that aisotropy's value-object `Scope` prevents at the type level.

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
