---
type: Component
title: Gollem — Cognitive Orchestrator
description: The kernel of Aisotropy's LLM OS model — assembles surgical context, forces structured outputs from the Go struct, self-repairs with a bounded reflection loop, and injects only scope-permitted tools.
tags: [components, gollem, orchestrator, llm, structured-outputs, reflection-loop, go, sse]
timestamp: 2026-07-08T00:00:00Z
---

# Gollem — Cognitive Orchestrator

**Language / license:** Go / MIT · **Site:** `gollem.fugue-labs.ai` — describes itself as "the
production agent framework for Go," compiled to a static binary, agents typed via generics
(`Agent[T]`).

## Role in the system

Gollem is the **kernel** of the [LLM OS](../architecture/overview.md#llm-os) model: the CPU (LLM) does
the compute, but Gollem decides what gets into RAM (the context window), when the cycle with the
model fires, which tools are available, and what to do when the output doesn't satisfy the
contract. It takes control only at **step 4** of the
[request lifecycle](../architecture/overview.md#lifecycle-of-a-request), after
[Huma](huma-api.md) has already validated the input and populated the `ctx` with `tenant_id`/`user_id`/scopes, and
memory has already been hydrated in parallel.

It is intentionally **in-process** (not a separate microservice): the cognitive engine runs in the
same goroutine/binary as the rest of the Go stack, consistent with the golden rule of the
[stack](../architecture/overview.md) to minimize network hops where latency matters.

## Responsibilities

| Responsibility | How |
|---|---|
| Assemble surgical context | Concatenates *only* what the tiered memory delivered as relevant (summary buffer from [BadgerDB](badgerdb-working-memory.md), facts from [rqlite](rqlite-facts-memory.md), RAG from [Qdrant](qdrant-semantic-memory.md)) — never a prompt bloated with hidden templates. Applies [context engineering](../vision/principles.md#2-context-engineering--prompt-engineering) directly. |
| Force structured outputs | Derives the JSON Schema from the **same Go struct** Huma used to validate the inbound HTTP (`Agent[T]`: "schema generated from struct tags, with no hand-written JSON Schema"). See [the struct as single contract](../vision/thesis.md#the-struct-as-the-single-source-of-truth). |
| Reflection loop (self-repair / "output repair") | If the LLM returns JSON that fails schema validation, Gollem re-invokes with the exact typing error attached, up to N bounded retries — the functional equivalent of Pydantic AI in the Python ecosystem, but native to Go and without leaving the process. |
| Scoped tool dispatch | Injects into the LLM **only** the tools (`FuncTool[P]`) that the `ctx`'s `scopes` authorize for that `user_id` — never the tenant's full tool list. Each tool's dependencies are resolved via the execution ctx itself, with no global variables. |
| SSE streaming | Emits the response incrementally to the Huma layer; respects `ctx` cancellation — if the user closes the connection, Gollem cuts the in-flight LLM call and stops consuming tokens. |
| Checkpoints / continuity | Supports snapshots and state resumption after interruptions, to resume generation without starting from scratch *(exact mechanism to verify against the real API)*. |

## Integration surface

```
ctx{tenant_id, user_id, scopes}   (from Huma)
        │
        ▼
hydrated memory (BadgerDB + rqlite + Qdrant, in parallel)
        │
        ▼
┌─────────────────── Gollem ───────────────────┐
│ 1. assembles surgical context                 │
│ 2. filters tools by ctx scopes                │
│ 3. Agent[T] → LLM → structured output         │
│ 4. invalid? → reflection loop (max N)         │
│ 5. SSE stream, honors ctx.Done()              │
└────────────────────────────────────────────────┘
        │
        ▼
response to the user (via Huma) + event on BadgerDB's Outbox (sleep cycle)
```

- **With Huma:** Gollem never validates HTTP nor knows about the transport — it receives an already
  populated `ctx` and input already validated by [Huma](huma-api.md). The boundary is strict: Huma is
  the edge, Gollem is the compute. See [Huma — integration surface](huma-api.md#integration-surface).
- **With `context.Context`:** Gollem is purely a **reader** of the ctx (identity, scopes, deadline).
  Ctx cancellation is the sole mechanism for cutting spend — there are no parallel timers or
  kill-switches. This is a direct application of [ctx-based isolation](../vision/principles.md#derived-operating-principles).
- **With tiered memory:** Gollem is the final *consumer* of the hierarchy described in
  [tiered memory](../architecture/memory-tiers.md) — it doesn't decide what to hydrate (that
  happens earlier, in parallel, step 3 of the lifecycle), it only consumes what already arrived
  assembled. After responding, it enqueues the sleep cycle event in
  [BadgerDB's Outbox](../architecture/memory-tiers.md#4-batch-consolidation--the-sleep-cycle) — it does not
  write directly to Qdrant/rqlite on the hot path.
- **With the user's tools/primitives:** the set of tools Gollem can invoke comes
  cloned by `user_id` from rqlite (see
  [primitive self-improvement](../architecture/memory-tiers.md#primitive-self-improvement-behavioral-memory));
  Gollem never clones executable code, it only consumes metadata (descriptions, few-shots) already
  mutated by the sleep cycle.

## Key configuration / patterns

- **Generic `Agent[T]`:** the agent's type `T` **is** the contract struct — zero
  intermediate transformation between "what Huma validated" and "what the LLM must produce."
- **Typed `FuncTool[P]`:** each tool is a typed Go function, not a hand-assembled JSON dict;
  reduces the reflection loop's error surface because the tool's schema also comes from
  reflection.
- **Reflection loop with an explicit cap (N retries):** never infinite retries — if the budget
  runs out, it degrades to a controlled error instead of hanging the response. The value of N should
  be configurable per endpoint/tenant.
- **Iterator-based streaming (Go 1.23+), not channels:** Gollem exposes multiple delivery
  modes (delta, accumulated, debounced, unified) so the consumer (Huma's SSE layer) can choose
  granularity *(exact mode names to verify against the real API)*.
- **Never bloated prompts:** any temptation to "throw the whole context in just in case" violates
  the [context engineering](../vision/principles.md#2-context-engineering--prompt-engineering)
  principle and raises the cost of every tenant request.

## Gotchas and risks

- **Reflection loop maturity is an open question of the project** — see
  [components: open questions](index.md#open-questions-per-component). Before
  depending on N retries in production, real per-model schema failure rates must be measured.
- **Observability (OTel) hooks and dynamic tool injection** are also open questions
  — without this, the requirement of [observability by design](../vision/principles.md#derived-operating-principles)
  (span per interaction, attributable per-token cost) depends on manual instrumentation
  around Gollem, rather than being native.
- **License to confirm in the real repo** — the site describes it as "open source" without pinning the
  exact license in the material consulted; before adopting it in the core, it must be formally
  verified (a non-negotiable requirement of [sovereignty](../vision/principles.md#derived-operating-principles):
  MIT/Apache-2.0 only).
- **Coupling Gollem to a specific transport is an anti-pattern:** all streaming/cancellation
  must go through `ctx` and the Huma interface, never assuming HTTP/SSE directly inside the
  cognitive engine, so as not to break the boundary with the contract layer.

## Related decisions

- [ADR-0002](../decisions/index.md) — anti-framework-magic: Gollem is adopted precisely because
  it doesn't hide the final prompt or introduce invisible reasoning loops (unlike
  LangChain and its family).
- [ADR-0001](../decisions/index.md) — Go+Rust in-process-first stack: justifies why the cognitive
  engine runs embedded rather than as a separate inference service.
- Open question from [components](index.md#open-questions-per-component) about
  OTel hooks and dynamic tool injection — blocks full observability of the reflection loop.

## References

- Site: [gollem.fugue-labs.ai](https://gollem.fugue-labs.ai/)
- Perimeter counterpart: [Huma](huma-api.md)
- [Architecture overview](../architecture/overview.md) · [Tiered memory](../architecture/memory-tiers.md) · [Thesis](../vision/thesis.md) · [Design principles](../vision/principles.md)
