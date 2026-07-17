---
type: Feature matrix
title: "Dify — features vs aisotropy"
description: Coverage map of Dify capabilities compared to aisotropy's built-ins, grounded in the real Dify backend source (langgenius/dify @ 1aea3460, 2026-07-16) — source-verified, not executed.
tags: [arenas, dify, features, comparison, source-verified]
timestamp: 2026-07-16T00:00:00Z
---

# Dify — Features

> **SOURCE-VERIFIED, NOT EXECUTED.** Dify is a Flask + Celery + Postgres + Redis
> platform with no in-process library; it cannot boot in this offline env (see
> [`README.md`](./README.md) and [`DX-NOTES.md`](./DX-NOTES.md)). Every class below
> is grounded in the genuine Dify backend source at HEAD (commit `1aea3460`,
> 2026-07-16). Concrete anchors are cited as `api/<path>:<line>`. The isolation and
> product rows are additionally exercised by an **executable** smoke that transcribes
> Dify's real schema and query contract into its own ORM (SQLAlchemy 2.0).
>
> **Honesty caveat:** in this pass the *structural* claims (which tables carry
> `tenant_id`, `Conversation`/`Message` app_id-only, the membership-checked
> account→tenant bind, the absence of `product_id`, the LICENSE §1a clause) were
> **independently re-verified against Dify's live source**. The exact `:<line>`
> offsets at the pinned commit were **not** re-checked (the clone was unavailable to
> re-open) — treat them as approximate pointers. The smoke reproduces the *naive
> developer's* omission of the ownership gate against the transcribed contract; it is
> the standard shared-schema-no-RLS tradeoff, **not** proof that Dify's own code leaks.

## Coverage classes

**BUILT-IN** (framework provides it) · **CONVENTION** (possible but developer-enforced,
no guardrail) · **DIY** (you build it) · **PARTIAL** · **PAID** (only in the hosted /
enterprise tier) · **NOT-POSSIBLE** (framework cannot provide it by construction).

## The 11-point Orbit checklist

| # | Feature | Class | Real Dify source / note |
|---|---|---|---|
| 1 | Multi-tenant isolation | **CONVENTION** | Shared-schema `tenant_id` column (`App`/`Dataset`/`Document`/`DocumentSegment`) + hand-written `.where(tenant_id==?)`; **no Postgres RLS, no schema-per-tenant** (0 `CREATE POLICY`/`ENABLE ROW` in `api/`). `Conversation`/`Message` have **no tenant_id** — scope is transitive through `app_id` (`model.py:1141`, structurally re-verified vs live Dify). Naive-omission cross-read reproduced in `scripts/smoke.py` + `tests/` — the standard shared-schema tradeoff, not a Dify defect (Dify's own services keep the gate). |
| 2 | Structured output + retry | **PARTIAL** | Workflow LLM node has structured-output extraction + per-node retry config, but no fail-closed schema reflection loop at the API contract level |
| 3 | Tool calling | **BUILT-IN** | Agent apps (function-calling / ReAct) + built-in tools + custom OpenAPI tools + workflow-as-tool + MCP (`core/tools`, `core/mcp`) |
| 4 | Streaming (SSE) | **BUILT-IN** | `POST /v1/chat-messages response_mode=streaming` (SSE) + a stop-generation endpoint (server-side cancel, not a client ctx spend-cutoff) |
| 5 | Working memory | **BUILT-IN** | Conversation history in Postgres (`Conversation`/`Message`) + prompt "Memory" window; server-side/ms, not µs in-process |
| 6 | Semantic memory (vectors) | **BUILT-IN** | Flagship RAG: Knowledge base → per-dataset vector collection (`core/rag/datasource/vdb`, `vector_factory.py:38`), ~15 vector backends (Qdrant/Weaviate/Milvus/pgvector/…) |
| 7 | Facts memory (journal) | **DIY** | No dedicated FTS decisions/rules journal; closest is RAG chunks |
| 8 | Cost governance | **PARTIAL** | Per-message cost IS built-in — `Message.total_price` (Decimal) + `currency` + `message_tokens`/`answer_tokens` (`model.py:1541`); per-APP rate limit `api_rpm`/`api_rph` (`model.py:431`) is a real pre-spend request gate. **Per-tenant token/$ pre-spend gate + a `/costs` meter endpoint are billing-tier (PAID cloud) / DIY on self-host** |
| 9 | Observability | **PARTIAL** | `opentelemetry-instrumentation-*` deps + per-app tracing integrations (Langfuse/LangSmith/OpenTelemetry/Arize/Weave); `gen_ai` semconv + native `tenant_id` on the span not guaranteed — workspace tag is DIY |
| 10 | Sovereignty / licensing | **PARTIAL / BLOCKER for Orbit** | Self-hostable but a **multi-container** platform (api+worker+web+db+redis+sandbox+vectordb). Modified Apache-2.0 **forbids operating a multi-tenant environment without a commercial license** — Orbit's exact use case (see below) |
| 11 | Language | Python | Server-side durable objects (Postgres), not in-process |

## Comprehensive feature-parity matrix (43 rows, SOURCE-VERIFIED)

Grounded in the real Dify backend source. Rows marked **▶** are additionally
exercised by an executable smoke against Dify's transcribed schema + real ORM
(`scripts/smoke.py`, `scripts/product_isolation_demo.py`, `scripts/license_scan.txt`,
`tests/` — 9 passed, all exit 0). The rest are honest source classification.

| # | Feature | proof | Class | Grounding (real Dify source) |
|---|---|---|---|---|
| 1.1 | Temperature hierarchy (4-tier different nature) | arch | **PARTIAL** | 2 surfaces: SQL conversation history + per-dataset vector store. No FTS facts tier, no offline-batch tier. aisotropy=4 |
| 1.2 | Working memory | exec | **BUILT-IN** | `Conversation`/`Message` (Postgres) + prompt Memory window; server-side, not µs |
| 1.3 | Facts memory (FTS journal) | exec | **DIY** | no decisions/rules FTS journal |
| 1.4 | Semantic memory (multi-kind vectors) | exec | **BUILT-IN** | Knowledge base + `vector_factory` (~15 vdb backends), per-dataset collection |
| 1.5 | Engram dual-index (fact↔vector) | exec | **DIY** | no fact↔vector co-index invariant |
| 1.6 | Summary buffer / context engineering | exec | **PARTIAL** | prompt Memory window + conversation summary; not a recursive evicting summarizer surface |
| 1.7 | Memory scoring (Rel×Imp×Rec) | exec | **DIY** | RAG ranks by vector similarity (+ optional rerank model); composite importance×recency DIY |
| 1.8 | Contradiction / supersede | exec | **DIY** | no `superseded_by` / cross-store reconcile |
| 1.9 | Knowledge ingestion (user + shared) | exec | **BUILT-IN** | chunk→embed→index pipeline (`indexing_runner.py`); dataset permissions for sharing |
| 1.10 | Primitive self-improvement (friction) | exec | **DIY** | none |
| 1.11 | BYO embedder | arch | **BUILT-IN** | model runtime: many embedding providers, swappable per dataset |
| 2.1 | Struct-as-contract (1 struct, 3 uses) | arch | **DIY** | UI/config-driven; HTTP, storage and LLM schemas are separate |
| 2.2 | Fail-closed reflection loop (502) | exec | **PARTIAL** | workflow structured-output + per-node retry; no fail-closed schema-repair loop |
| 2.3 | SSE + immediate ctx-cancel | exec | **BUILT-IN** | `response_mode=streaming` SSE + stop endpoint (server-side cancel) |
| 2.4 | Stateless /ask + response cache | exec | **PARTIAL** | Completion app is stateless; **annotation-reply** is a per-app curated semantic cache w/ `score_threshold` (`model.py:246`) — not a per-(tenant,user) auto response cache |
| 3.1 | Scope-gated tools (gov by absence) | exec | **CONVENTION** | tool set is per-APP config, not gated by the caller's request scopes |
| 3.2 | Native vs homegrown agent loop | exec | **BUILT-IN** | Agent app: function-calling + ReAct strategies |
| 3.3 | Typed tools + MCP | arch | **BUILT-IN** | built-in tools + custom OpenAPI tools + MCP client/server (`core/mcp`) |
| 3.4 | Model-selection gateway | exec | **PARTIAL** | model config per app/node + credential **load balancing**; no cost/capability auto-router |
| 4.1 | Isolation BY CONSTRUCTION | arch | **CONVENTION** | shared-schema `tenant_id` + app-layer `.where()`; no RLS/schema isolation |
| 4.2 | Value-object Scope (unforgeable) | arch | **CONVENTION** | bare `str` `tenant_id`; nothing type-blocks an unscoped query |
| 4.3 | Vector in-scan tenant filter (not post) | arch | **CONVENTION** | per-dataset collection filtered by `dataset_id`; tenant scope transitive via `Dataset.tenant_id`, not a tenant filter on the scan |
| 4.4 | Authenticated-identity seam (must-agree bind) | exec | **PARTIAL** | **real** account↔tenant membership bind — `set_tenant_id_with_session` verifies a `TenantAccountJoin` row (`account.py:166`); stronger than most arenas. But per-request data queries then trust `current_tenant_id` by convention; per-app API keys scope to one app |
| 4.5 | JWKS key rotation | exec | **DIY** | console JWT is app-issued; enterprise SSO/OIDC exists; no proven JWKS-rotation seam in OSS |
| 4.6 | Guardrails (redact/inject/output) | exec | **PARTIAL** | input + output **moderation** built-in (`core/moderation`: keyword + OpenAI moderation); structured PII-redaction / injection-detection limited |
| 4.7 ▶ | Multi-product isolation (4th scope axis) | arch | **CONVENTION** | **no `product_id` anywhere** — workspace=tenant is the only boundary; a "product" is app_id convention or an unbound tag; RAG plane has no product column. Missing-axis cross-read run-reproduced (unmodeled boundary, not a defect) |
| 5.1 | Per-tenant PRE-SPEND quota gate (429) | exec | **PARTIAL** | per-APP `api_rpm`/`api_rph` request-rate gate (`model.py:431`) rejects pre-spend; per-**tenant** token/$ budget is billing-tier (PAID) |
| 5.2 | Circuit breaker | exec | **DIY** | credential load-balancing failover, but no provider circuit breaker |
| 5.3 | Quota-pressure degradation | exec | **DIY** | no load-based model downgrade |
| 5.4 | Cost meter (per-tenant) | exec | **PARTIAL** | per-message `total_price`+`currency`+tokens BUILT-IN (`model.py:1541`) — richer than most arenas (real $ field); a queryable per-tenant meter endpoint is the cloud billing dashboard (PAID) |
| 5.5 | Constitutional-gate governance surface | arch | **DIY** | isolation/cost/license/contract left to developer discipline |
| 5.6 | License 100% MIT/Apache, self-hostable | arch | **PARTIAL / BLOCKER** | **modified Apache-2.0**: multi-tenant operation needs a commercial license (see §License) |
| 5.7 ▶ | Dependency license posture (scanned) | exec | PERMISSIVE +LGPL leaf | 39/41 direct deps permissive; 2 weak-copyleft LGPL-3.0 (`psycopg2-binary`, `gmpy2`); 0 GPL/AGPL/BUSL/SSPL in the tree. **But Dify's OWN license blocks multi-tenant SaaS** |
| 6.1 | OTel gen_ai spans w/ tenant native | exec | **PARTIAL** | OTel instrumentation + tracing integrations; `gen_ai` semconv + native tenant tag DIY |
| 6.2 | Health / readiness probes | exec | **PARTIAL** | server health endpoints; no per-memory-tier readiness probe |
| 6.3 | Bits-per-token North Star | exec | **DIY** | none |
| 6.4 | Product-evaluation harness (IR + judge) | exec | **DIY** | annotation-based feedback, but no P@k/MRR + judge harness |
| 7.1 | Transactional outbox (atomic, idempotent) | exec | **NOT-POSSIBLE** | Celery tasks enqueued separately from DB writes — no atomic (row+intent) co-commit in one txn |
| 7.2 | Sleep-cycle consolidation | exec | **DIY** | Celery does async indexing/retention, but no memory-consolidation/dedup/contradiction pass |
| 8.1 | Single-binary zero-config runtime | arch | **NOT-POSSIBLE** | multi-container platform (api+worker+web+postgres+redis+sandbox+vectordb) |
| 8.2 | Dual surface (sealed kernel + SDK) | arch | **PARTIAL** | REST API + plugin system, but no embeddable in-process kernel |
| 8.3 | Polyglot subprocess boundary | arch | **DIY** | code-execution runs in a separate sandbox service (a service boundary), but not a Go/Rust worker contract |
| 8.4 | Provider-agnostic LLM registry | arch | **BUILT-IN** | model runtime supports dozens of providers, swappable |

### Class tally (42 coverage-class rows; 5.7 scored on the license scale)

| Class | Count | Rows |
|---|---|---|
| BUILT-IN | 8 | 1.2, 1.4, 1.9, 1.11, 3.2, 3.3, 8.4 (+ #3 checklist) |
| PARTIAL | 12 | 1.1, 1.6, 2.2, 2.4, 3.4, 4.4, 4.6, 5.1, 5.4, 6.1, 6.2, 8.2 |
| CONVENTION | 5 | 3.1, 4.1, 4.2, 4.3, 4.7 |
| DIY | 13 | 1.3, 1.5, 1.7, 1.8, 1.10, 2.1, 4.5, 5.2, 5.3, 5.5, 6.3, 6.4, 7.2, 8.3 |
| NOT-POSSIBLE | 2 | 7.1, 8.1 |
| (BLOCKER, license) | — | 5.6 — see below |

Reading it: Dify is **broad on the product surface** (RAG, agent loop, typed
tools+MCP, provider registry, per-message cost with a real $ field, moderation,
annotation-reply cache) — a low-code LLM *app platform*, batteries included. It is
**weak exactly on aisotropy's axis**: isolation is app-layer convention (no RLS,
Conversation has no tenant_id), there is no product axis, the transactional outbox
is NOT-POSSIBLE, and it is a heavy multi-container runtime, the opposite of a
single zero-config binary.

## Multi-PRODUCT isolation — the 4th scope axis (RUN-VERIFIED: the axis is UNMODELED)

aisotropy scopes every read/write by four keys `(tenant_id, user_id, session_id,
product_id)` via `context.Context`. Dify's schema has exactly **two** boundaries —
workspace (`tenant_id`, LICENSE §1a "one tenant == one workspace") and app
(`app_id`). **There is no `product_id` column anywhere in `api/models`** (structurally
re-verified against live Dify: `account.py` and `model.py` carry no such column). This
isn't a "leak" so much as a **missing axis** — Dify simply does not model a fourth
product boundary, so there is nothing to enforce and the tenant-correct read still
crosses products.

Run:
```bash
arenas/dify/.venv/bin/python arenas/dify/scripts/product_isolation_demo.py   # exit 0
arenas/dify/.venv/bin/python -m pytest arenas/dify/tests/test_product_isolation.py -q  # 3 passed
```

RUN-VERIFIED output — same tenant `acme`, same user `alice`, two products modelled
as two Apps (the only sub-tenant boundary Dify offers):

```
[dify] Seeding (tenant=acme, user=alice, product=checkout app=319b6140) marker: df-c7d7ed4a03643e6b
  OK  Positive control: product 'checkout' saw its own marker
[dify] Leak #1 — RAG segments scoped by tenant only (no product/app column exists):
  LEAK 'analytics' read 'checkout' segment via the shared tenant KB (no product axis)
[dify] Leak #2 — conversation read by another product's app_id, no product gate:
  LEAK product boundary is just app_id convention — one bad id crosses it
[dify] Leak #3 — get_conversation(id): no product_id column to check against:
  LEAK retrieve-by-id has no product dimension to enforce

Result: no product boundary exists to enforce — same tenant+user, wrong product
  reads the marker (a MISSING AXIS, not a Dify defect — no product_id anywhere).
```

`test_no_product_id_column_exists` asserts structurally that **no** Orbit/Dify
model carries a `product_id` column. The RAG plane (`Dataset`/`DocumentSegment`)
has `tenant_id` but no product/app column, so a shared-tenant knowledge base has
**no product boundary at all** — the correct Dify-scoped read still crosses it.

- **AISOTROPY COMPARISON:** `product_id` rides `context.Context` in the `Scope`
  value-object like tenant/user, enforced per-statement — the cross-product read
  is **unrepresentable**. Class here: **CONVENTION** (and, for the RAG plane,
  arguably NOT-POSSIBLE without inventing a column).

## Dependency license posture — SOURCE-DECLARED (+ the governing finding)

Full record: [`scripts/license_scan.txt`](./scripts/license_scan.txt).

Dify's backend tree (Flask/Celery/onnxruntime/provider SDKs, native
gmpy2/psycopg2) can't be installed/booted offline, so **Part A** is a real
executed `pip-licenses` scan of this arena's harness venv (7/7 permissive) and
**Part B** classifies Dify's 41 declared direct runtime deps from
`api/pyproject.toml`:

| Bucket | Count | Notable |
|---|---|---|
| Permissive (MIT/BSD/Apache-2.0/PSF) | **39** | flask, celery, redis, gevent, boto3, google-*, opentelemetry-*, httpx, gunicorn |
| Weak copyleft (LGPL-3.0) | **2** | **psycopg2-binary**, **gmpy2** (CAUTION; used unmodified as libs) |
| Strong copyleft (GPL/AGPL) | **0** | — |
| Restricted (BUSL/SSPL/ELv2) | **0** | — (in the dep tree) |

**THE GOVERNING FINDING is Dify's OWN license, not a dependency.** Dify ships
under a **modified Apache-2.0** whose §1a (quoted verbatim in `README.md`) forbids
using the source to "operate a multi-tenant environment" without a commercial
license, defining one tenant as one workspace. **Orbit is a multi-tenant chatbot
— its exact prohibited use.** Under aisotropy's own
`.claude/rules/cos/license-policy.md`, a commercial-use/field-of-use restriction
of this kind is a 🚫 **BLOCKER** for a SaaS product (same class as BSL/SSPL/ELv2).

- **AISOTROPY COMPARISON:** the dep tree clears the hard-blocker gate; Dify's
  modified-Apache multi-tenant clause does not. aisotropy is 100% MIT/Apache with
  no field-of-use limit — you may operate it multi-tenant, self-hosted, freely.

## Summary

Dify is the **broadest product** in the arena set — a low-code LLM app platform
with RAG, agents, typed tools + MCP, a provider registry, per-message cost with a
real USD field, moderation, and an annotation-reply semantic cache, all
batteries-included. But it is **weakest exactly on aisotropy's differentiators**:
tenant isolation is shared-schema app-layer convention (no RLS; `Conversation`
has no `tenant_id`), there is **no product axis at all**, the transactional outbox
is NOT-POSSIBLE, and it is a heavy multi-container runtime — the opposite of a
single zero-config binary. And its **own license forbids the multi-tenant SaaS
that Orbit is** without paying LangGenius.

**vs. aisotropy:** Dify wins on out-of-the-box product breadth and a polished UX;
aisotropy wins on isolation-by-construction, the product scope axis, the crash-safe
transactional outbox, single-binary sovereignty, and — decisively for Orbit — a
permissive license with no multi-tenant field-of-use restriction.
