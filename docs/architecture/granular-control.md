---
type: Concept
title: Granular control — Security × cost × behavior
description: The three axes of Aisotropy's native granular control — data-level filtering, a token circuit breaker, and dynamic cloning/assembly of primitives — all resolved with middleware and ctx, with no AI library.
tags: [architecture, granular-control, security, cost, tool-injection, prompt-engineering]
timestamp: 2026-07-08T00:00:00Z
---

# Granular control — Security × cost × behavior

The framework's [thesis](../vision/thesis.md#the-stance-zero-magic) is that **granular control is
native, not an AI-library feature**: it's resolved with traditional software patterns — HTTP
middleware and Go's `context.Context` — over the same three axes any serious multi-tenant system
needs to govern: **what data** a user can see, **how much** it costs the tenant, and **what the
agent is allowed to do** on their behalf. All three axes share a single source of truth: the
`ctx` populated by [Huma](../components/huma-api.md) in step 1 of the
[request lifecycle](overview.md#lifecycle-of-a-request) (see
[multi-tenancy](multi-tenancy.md)).

## The three axes

| Axis | Level | Question it answers | Mechanism |
|---|---|---|---|
| **Security** | Data-level | Can this user *see* this data? | JWT scopes → physical filter at every store |
| **Cost** | Token-level | Can this tenant *afford* this call? | Rate limiting + circuit breaker before touching the provider |
| **Behavior** | Agent-level | Can this agent *perform* this action? | Tool cloning per role + dynamic system-prompt assembly |

## 1. Security — data-level filtering

The JWT carries explicit `scopes` (e.g. `["read:financial-data", "write:tasks"]`) that Huma
injects into the `ctx` along with `tenant_id`/`user_id` (see
[identity and scope extraction](../components/huma-api.md#responsibilities)). From there, each
store in the [memory hierarchy](memory-tiers.md) filters independently by tenant/user — there is
no single central authorization point that, if it fails, exposes everything:

- In [Qdrant](../components/qdrant-semantic-memory.md), the `tenant_id`/`user_id` filter
  **physically discards** other tenants' vectors *during* the HNSW graph scan, not after fetching
  the results — see [rigid payload filtering](../components/qdrant-semantic-memory.md#key-configurationpatterns).
- In [rqlite](../components/rqlite-facts-memory.md), every `SELECT`/`UPDATE` carries
  `WHERE tenant_id=? AND user_id=?` as a mandatory clause, never optional.
- Specific `scopes` (beyond tenant/user) further determine *which subset* of data within the
  tenant itself is visible — e.g. `read:financial-data` conditions whether a tool or a query can
  touch that collection/table at all (see the behavior axis, below, for the case of tools).

Tenant/user isolation detail per layer is in [multi-tenancy](multi-tenancy.md); this axis adds the
scopes dimension *within* the same tenant/user.

## 2. Cost — token-level circuit breaker

Cost control happens **before** spending a single token against the LLM provider — it's step 2 of
the [request lifecycle](overview.md#lifecycle-of-a-request), immediately after identity
extraction:

- **Rate limiting with a sliding window**, backed by [BadgerDB](../components/badgerdb-working-memory.md#data-model--keys)
  under the key `quota:{tenant}` (or local Redis if the deployment prefers it) — a token-bucket
  refreshed per request.
- **Circuit breaker via input-token estimation**: before calling the provider, a local tokenizer
  in Go (candidate: `tiktoken-go` *(to be verified)*) estimates how many tokens the assembled
  prompt will cost. If the tenant has already exceeded ~95% of its quota, the system **degrades to
  a cheaper model or rejects with `429`** directly in Huma — the request never reaches the
  provider's API. This is what the [thesis](../vision/thesis.md#the-problem) calls "brutal" token
  efficiency: cost is cut at the edge, not discovered on the bill.

**Cross-reference with compressibility routing:** the quota circuit breaker decides *whether*
spending is allowed; [Bet 4 — compressibility routing](../research/bet-04-compressibility-routing.md)
refines *which model* to route to within what the quota allows, using surprise bits instead of a
separately trained classifier. Bet 4 itself documents this integration: the compressibility
threshold `θ` can move with the tenant's quota (graceful degradation — lowering `θ` when the
tenant is near the limit pushes more queries toward the cheap model). In other words, quota
decides the spending ceiling; compressibility decides the fine-grained allocation within that
ceiling.

## 3. Behavior — agent-level control

This is the axis where "no AI library" shows the most, because it's the one magic frameworks
usually solve with opaque permission layers over the agent itself. Aisotropy resolves it with two
mechanisms, both applied **before** the LLM sees anything:

### Thematic cloning of primitives per role

The tools/primitives available to an agent are not a static global list: they're **cloned per
role/user** (same metadata mechanism described in
[primitive self-improvement](memory-tiers.md#primitive-self-improvement-behavioral-memory)).
If the `ctx` indicates the user does **not** have the scope a tool requires, that tool **simply
isn't injected** into the payload sent to the LLM — it's not sent disabled, it's not sent with a
"don't use this" instruction: it doesn't exist in the payload at all.

The consequence is direct: **the model doesn't even know the tool exists**, so there's no surface
for it to hallucinate an attempt to bypass security ("let me use the forbidden tool anyway") — the
governance problem is solved by absence of information, not by a behavioral instruction the LLM
could ignore or be convinced to ignore (prompt injection). It's the application of
[Huma](../components/huma-api.md#key-configuration--patterns)'s golden rule: "one handler, one
struct" — here, "one scope, one subset of tools," with no exceptions at the prompt level.

### Dynamic system-prompt assembly (dynamic prompt engineering)

The system prompt isn't a static template or a hidden framework string — it's **assembled on the
fly like a lego template**, with fragments coming from different sources in the `ctx`:

```
system_prompt = fragment(tenant)     -- brand voice, tone, tenant restrictions
              + fragment(product)   -- context of the active module/product
              + fragment(user)      -- style preferences, Engram-style
```

Each fragment is resolved independently from the identity in the `ctx` — the tenant defines its
brand voice, the product contributes the context of the module the user is in, and the user
contributes learned preferences (the same pattern [facts memory](../components/rqlite-facts-memory.md)
resolves for hard preferences). The result is "surgical context" (see [overview](overview.md#lifecycle-of-a-request),
step 4): nothing bloated, nothing generic — every fragment has a reason to be there.

**Cross-reference with primitive self-improvement:** the user fragment and cloned tools aren't
static over time — the sleep cycle can rewrite a tool's *description* or add a counter-example
when it detects friction (see
[primitive self-improvement](memory-tiers.md#primitive-self-improvement-behavioral-memory)).
Behavior control, then, is not just "what gets injected" but also "how what's injected is
described," and both evolve per user without touching executable code.

## Why granular control is native, not a feature

All three axes share the same property: **none depends on an AI library to work**. Security is a
query/payload filter any traditional backend already knows how to do. Cost is a rate limiter and a
circuit breaker, patterns from two decades of distributed systems. Behavior is conditional
construction of a JSON payload before sending it to an API — no different from building an HTTP
request conditioned by permissions. The only "new" piece is that the payload in question ends up
in an LLM instead of another microservice; the control mechanism is the same middleware + `ctx`
that governs the rest of the system. This is, in practice, the concrete advantage of having
avoided "magic code" (see [thesis](../vision/thesis.md#the-stance-zero-magic)): there's no
framework permission layer to audit separately — it's the same readable Go code that governs any
other endpoint.

*(Conceptual pattern; exact name of the local tokenizer, exact shape of the token-bucket, and the
precise format of the system-prompt template assembly — to be verified against the real
implementation before coding.)*

## References

- [Multi-tenancy](multi-tenancy.md) — the `ctx` that feeds this document's three axes.
- [Architecture overview](overview.md) — request lifecycle, steps 2 (quota) and 4 (surgical context + tools).
- [Tiered memory](memory-tiers.md#primitive-self-improvement-behavioral-memory) — primitive self-improvement.
- [Huma — Contracts and API layer](../components/huma-api.md) — scope extraction into the ctx.
- [BadgerDB — Working Memory](../components/badgerdb-working-memory.md) — quota token-bucket.
- [Qdrant — Semantic Memory](../components/qdrant-semantic-memory.md) — physical filter during the HNSW scan.
- [Bet 4 — Compressibility routing](../research/bet-04-compressibility-routing.md) — refinement of the cost axis.
- [Thesis](../vision/thesis.md) — the zero-magic stance behind this design.
