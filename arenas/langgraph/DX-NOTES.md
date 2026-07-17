---
type: Developer notes
title: "LangGraph — DX notes"
description: Honest developer-experience assessment of LangGraph from the perspective of building a multi-tenant production agent.
tags: [arenas, langgraph, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# LangGraph — DX Notes

## The good

### Largest ecosystem
Hiring pool, community, integrations, pre-built chains — LangGraph has it all. If you need
an integration with a specific LLM provider, vector store, or data source, it probably exists.
aisotropy has zero community and a tiny integration surface by comparison.

This is LangGraph's biggest strength and aisotropy's biggest weakness. For teams that need
breadth of integrations, LangGraph is the rational choice.

### `create_agent` in one line — fastest time-to-first-prototype
Single-tenant agent development is incredibly fast. You can go from zero to a working chatbot
in an afternoon. aisotropy requires building the pieces yourself — slower initial development
but more control long-term.

### Checkpointers — durable execution
SQLite, Postgres, Redis checkpointers provide durable execution out of the box. This is more
mature than aisotropy's in-process BadgerDB + transactional outbox pattern. For long-running
agent workflows with pause/resume, LangGraph's checkpointers are the better choice.

### LangGraph 1.0 stability
After three years of v0.x churn, 1.0 shipped with a stable API. TheOSS core is MIT-licensed.

## The painful

### Isolation is opt-in wiring, not a default
On the raw `Store`, `Store.search()` is an **element-wise** prefix scan over the namespace tuple.
Namespace tenants as `(org, tenant, user)` and a search of just `("acme",)` — one element short of
full scoping — returns EVERY tenant nested under the `acme` org (`tenantA`, `tenantB`, ...). On the
bare primitive, prefix depth is developer convention: this is an under-scoped query, the same class
of bug any KV store has when hit with no authorization layer — **not** a framework defect, and not
specific to LangGraph. (The match is element-wise, not string-prefix: `search(("tenant",))` does not
match `("tenantA", ...)`.)

The fix ships with the framework and it works. LangGraph Server's `@auth.on.store()` handler
(importable from `langgraph_sdk`) runs on every store op and can rewrite/assert the namespace to
the authenticated caller. Combined with deep namespaces `(org, user, product)`, asserting
`namespace[:3] == (org, user, product)` makes both the shallow-prefix scan and a wrong-key read
**structurally impossible** — the direct analogue of aisotropy's Scope. The demos prove it: an
identity-bound store facade (plain app code, no server needed) closes both the tenant-axis and
product-axis leaks on the OSS store.

The honest, narrower difference: aisotropy binds all four axes
(`tenant_id`, `user_id`, `session_id`, `product_id`) from the verified identity via
`context.Context` **by default**, as a `Scope{}` value object with unexported fields — a
wrong/forgotten key is unrepresentable. LangGraph gives you the same capability but you must wire
it. aisotropy's own raw stores likewise depend on its Scope/handler layer; isolation is an
application-boundary property in both. aisotropy's edge is that the binding is default-wired and
the value object cannot be forged.

Residual sovereignty note: the `@auth.on.store()` namespace-**rewrite** runs inside a LangGraph
server (self-hosted or the managed Platform tier). The namespace-**assertion** pattern is plain app
code any dev can add on the OSS store.

### LangSmith is paid/SaaS-gated
Production tracing and persistence require LangSmith, which is:
- Free tier limited to 1M node executions
- Full self-host + LangSmith are Enterprise/sales-gated
- Highest lock-in of the major observability tools per 2026 comparisons

For regulated deployments (HIPAA, GDPR, finance), a SaaS observability dependency is
disqualifying. aisotropy ships OTel gen_ai semconv compliant tracing with no SaaS requirement.

### Python 3.14 compatibility wall
Many packages (including some LangChain dependencies) don't have wheels for Python 3.14 yet.
You're stuck on 3.13. aisotropy (Go) doesn't have this problem — Go versions are backward
compatible and the binary runs on any system with the right libc.

### Repackaging churn
Parsers moved from `langgraph` to `langchain-classic` in v1.0. This is the kind of API
instability that makes long-term maintenance painful. aisotropy's sealed engine + public
façade pattern avoids this — the public API (`aisotropy/`) is stable; the internal engine
(`internal/`) can evolve freely.

## The verdict

LangGraph is the **ecosystem play** — largest community, most integrations, fastest
time-to-first-prototype for single-tenant. Multi-tenant isolation is achievable and robust
(deep namespaces + `@auth.on.store()`), but it is wiring you own rather than a default; the paid
LangSmith dependency for production observability is a genuine sovereignty concern.

**Best for:** Teams that need ecosystem breadth and fast prototyping, are willing to wire the
authorization layer for multi-tenant isolation, and can tolerate a SaaS observability dependency.

**Worst for:** Teams that want tenant isolation guaranteed by default (not by remembering to wire
the auth handler) and self-hosted observability with no SaaS path.

**vs. aisotropy:** LangGraph wins on ecosystem and time-to-prototype; aisotropy wins on
isolation-**by-default** (the Scope value object binds all four axes automatically and cannot carry
a wrong key), self-hosted observability, and long-term maintainability. The trade is: ecosystem
breadth with isolation-you-wire + a paid observability path vs. isolation-wired-by-default +
self-hosted sovereignty. Both enforce isolation at the application boundary; the difference is
whether it is on by default and unforgeable.
