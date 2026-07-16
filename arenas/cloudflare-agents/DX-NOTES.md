---
type: Developer notes
title: "Cloudflare Agents — DX notes"
description: Honest developer-experience assessment of the Cloudflare Agents SDK from the perspective of building a multi-tenant production support bot.
tags: [arenas, cloudflare-agents, durable-objects, dx, developer-experience]
timestamp: 2026-07-16T00:00:00Z
---

# Cloudflare Agents — DX Notes

> Build: ~230 lines of TypeScript (`src/index.ts` + two test files) against `agents@0.17.4`.
> First green isolation test in `workerd` took one `pnpm install` and one `wrangler.jsonc`.

## The good

### The mental model is small and correct
"One Agent = one Durable Object = one embedded SQLite" is the entire model, and it is the
*right* model for per-entity state. You address an object by name, call methods on it by RPC,
and its `this.sql` is genuinely local. No connection pool, no external DB, no cache layer.
For a support bot keyed by (tenant, user, product), the object-per-conversation shape is
exactly what you want — and it's the one framework here where the shared store simply doesn't
exist. That is a real architectural advantage, not a marketing line.

### `this.sql` is a joy, and it's a *real* SQLite
Tagged-template SQL with typed rows (`this.sql<User>\`SELECT ...\``) is clean, and the DO's
SQLite is a full SQLite — my FTS5 probe returned a live match
(`{"ok":true,"hit":"production uses aws lambda"}`). So you can build a keyword facts journal
*inside* the object, which LangGraph literally cannot do. Working memory hydration "just
works": turn 2 saw `hydrated:2` with zero wiring.

### Offline, real-runtime testing is first-class
`@cloudflare/vitest-pool-workers` runs your tests **inside real `workerd`**, with real Durable
Objects and real SQLite — no mocks, no cloud, no key. `wrangler dev --local` gives the same
runtime over HTTP. This is the best offline-fidelity story of any arena: what runs in the test
is the production runtime, not a simulation. 7/7 tests, 2 s.

### MCP, scheduling, and streaming are built in
MCP client **and** server are first-class (`agents/mcp`). `this.schedule()` gives cron/delay
alarms for free (a real building block for async consolidation). Streaming (WebSocket
hibernation, `StreamingResponse`, `AIChatAgent`) is the framework's spine. These are the three
things it ships that most arenas make you assemble.

### MIT SDK, clean install
`agents` is MIT; the install is fast and the dep tree clears the hard-blocker license gate
(0 GPL/AGPL/BUSL). No ELv2/BSL games.

## The painful

### Isolation *feels* automatic but the binding is a bare string
This is the trap. Because each object has its own SQLite, you *feel* isolated — and you are,
at the object level. But `getAgentByName(ns, name)` binds `name` to nothing. I reproduced the
leak in four lines: assemble the name without `product_id` and two products share one object.
The framework will not stop you, warn you, or type-check you. It is the same
mis-assembled-key failure as every other arena, just hidden behind a boundary that *looks*
airtight. A regulated multi-tenant team has to add its own name-construction discipline and
tests — the framework gives no `Scope` value object to make the wrong key unrepresentable.

### It's a rented runtime, full stop
Durable Objects run only on Cloudflare. Local dev is real (`workerd`/`miniflare`), but you
cannot self-host the production guarantee — the durability, the global single-instance
uniqueness, the per-object isolation are Cloudflare platform properties. For a sovereignty- or
air-gap-constrained deployment this is disqualifying, and no amount of MIT licensing on the
SDK changes it. The strongest isolation substrate in this comparison is also the least
sovereign.

### Everything above the runtime is DIY
Pre-spend quota gate, per-tenant cost meter, circuit breaker, guardrails, gen_ai OTel spans,
structured-output repair, contradiction/supersede, importance scoring, a facts *tier* (vs. a
raw SQLite you build it in) — none of it ships. The SDK's "observability" is a
diagnostics-channel event bus for lifecycle events, not gen_ai telemetry; LLM usage/telemetry
lives in the rented **AI Gateway**. Vector RAG is the rented **Vectorize** product. You are
buying a superb actor runtime and a platform catalog to bolt onto it — not a memory/governance
framework.

### The memory "hierarchy" is one store
It's one SQLite per object (plus optional rented Vectorize, plus optional rented AutoRAG).
There is no built-in temperature hierarchy of different-nature tiers and no offline batch
consolidation engine — `schedule()` gives you the trigger, the logic is yours.

## The verdict

Cloudflare Agents is the **best isolation substrate** in the field and a genuinely pleasant,
high-fidelity runtime — MCP, scheduling, streaming, and a real in-process SQLite, all testable
offline in the actual production engine. Its price is twofold: the identity→object **binding
is developer convention** (mis-assemble the name → leak, which I proved), and the runtime is
**rented**, not self-hostable. Above the runtime, the whole memory-tiering and governance
surface is DIY or another rented product.

**Best for:** teams already on Cloudflare building per-entity stateful agents who want
object-level isolation and a clean edge runtime, and who will own the name-construction
discipline and governance themselves.

**Worst for:** sovereign/self-hosted multi-tenant SaaS, or teams that want tenant isolation,
pre-spend cost governance, and a tiered auditable memory to be *guaranteed by the framework*
rather than assembled and rented.

**vs. aisotropy:** Cloudflare wins the storage-boundary lane outright — it deletes the shared
store, which aisotropy still has to guard with a value-object `Scope`. But aisotropy makes the
*identity binding itself* unrepresentable-to-get-wrong (`Scope` with unexported fields,
`product_id` carried on `context.Context` like tenant/user), ships four memory tiers of
different nature + enforced pre-spend/cost/guardrail governance, and runs as a **self-hostable
binary you own**. The trade: the strongest isolation *substrate*, rented, with governance left
to you — vs. by-construction identity scoping and enforced governance in a sovereign runtime.
