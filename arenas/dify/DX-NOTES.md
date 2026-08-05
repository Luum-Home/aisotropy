---
type: Developer notes
title: "Dify — DX notes"
description: Honest developer-experience assessment of Dify from a source-verified analysis of building a multi-tenant Orbit chatbot on it (langgenius/dify @ 1aea3460).
tags: [arenas, dify, dx, developer-experience, source-verified]
timestamp: 2026-07-16T00:00:00Z
---

# Dify — DX Notes

> Written after cloning Dify's real backend at HEAD (`1aea3460`, 2026-07-16),
> reading `api/models`, `api/services`, `api/core/rag`, the `LICENSE`, and
> `pyproject.toml` line-by-line, and transcribing its tenant schema into an
> executable isolation smoke against its own ORM. **No live Dify server ran** —
> see README for why (it's a multi-container platform, not a library).

## The headline before anything else: the license

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

Dify is the only framework in this arena set whose **own license blocks Orbit's
use case outright**. The modified Apache-2.0 §1a forbids using the source "to
operate a multi-tenant environment" without a commercial license, and it defines a
tenant as a workspace. A support-desk SaaS with many customer workspaces behind one
deployment is exactly that. You can self-host Dify for *your* company; you cannot
resell it as a shared multi-tenant service. For a builder, this is a go/no-go gate
that sits *above* every technical feature — and it is easy to miss because the file
is named `LICENSE` and starts with "Apache License 2.0."

## The good

> Steelman, run-verified where the offline path allows: `python scripts/strengths_demo.py`
> (membership bind + data planes + streaming payload run-verified; product surface
> DESCRIBED/NOT-RUN) — exit 0.

### The broadest product surface in the arena set
Dify is a genuine low-code LLM *platform*, not a library. Out of the box you get a
visual app builder, RAG knowledge bases (~15 vector backends via `vector_factory`),
agent apps (function-calling + ReAct), a typed tools system (built-in + custom
OpenAPI + workflow-as-tool + MCP), a provider registry spanning dozens of model
vendors, and per-message cost tracking with a **real USD field** (`Message.total_price`
+ `currency`, `model.py:1541`) — richer cost visibility than most arenas here. For a
non-multi-tenant single-company assistant, you can ship a polished bot in an afternoon
without writing backend code.

### The account→tenant bind is actually enforced (better than most)
Unlike Letta (dev-passed filters bound to nothing), Dify's
`Account.set_tenant_id_with_session` (`account.py:166`) verifies a `TenantAccountJoin`
membership row before letting an account act as a workspace. You cannot switch into a
workspace you don't belong to. That's a real, membership-checked identity→tenant bind.

### Moderation and annotation-reply are built in
Input/output moderation (`core/moderation`: keyword + OpenAI moderation) and
annotation-reply — a per-app curated semantic cache with a `score_threshold`
(`model.py:246`) — are product features, not DIY. Both are things you'd otherwise
hand-roll.

## The painful

### Isolation is shared-schema convention, and the chat plane is worse
Tenant isolation is a `tenant_id` **column** plus a hand-written
`.where(tenant_id==?)` on every query. There is **no Postgres RLS and no
schema-per-tenant** (0 `CREATE POLICY`/`ENABLE ROW` in `api/`). `Conversation`
and `Message` have **no `tenant_id` column at all** — they key on `app_id` and inherit
tenant scope transitively. Dify's own `conversation_service.py:51` filters
`.where(Conversation.app_id == app_model.id)` and **keeps** the upstream tenant gate
(`app_model` is tenant-resolved first). A handler that holds an `app_id` and *skips*
that gate reads across tenants — but that is the naive caller's omission, not Dify's
code: this is the **standard shared-schema-multi-tenant tradeoff** (identical on a bare
SQL table or aisotropy's own BadgerDB if you dropped the Scope filter), correct when the
developer writes the gate and cross-reading when they don't. What differs across
frameworks is only whether the ungated read is *unrepresentable* (aisotropy's
ctx-propagated Scope) or merely *available* (shared-schema `.where()`). Dify sits in the
second, mainstream, camp — the tradeoff every widely-deployed shared-schema platform
makes for operational simplicity.

### There is no product axis

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md -->

<!-- CORRECTED 2026-08-04 by the sweep above: this section used to say "the schema has
     exactly two boundaries". That count was wrong, and it was already wrong at the commit
     this arena read — api/models/agent.py existed at 1aea3460af1d (25,829 bytes, GitHub
     contents API) and carries agent_workspaces / agent_workspace_bindings / agent_drive_files,
     which are real sub-tenant scope keys. What survives is the narrower, verified claim. -->

The schema does not model a product axis: there is **no `product_id` scope column
anywhere** in `api/models` (the sole `payment_product_id` is a billing SKU on
`ProviderOrder`, `api/models/provider.py:224`). The boundaries it *does* model are the
workspace (`tenant_id`), the app (`app_id`) and — since the 2026-05/06 agent migrations —
the agent and its working workspace (`agent_workspaces`, `agent_drive_files`, keyed
`(tenant_id, agent_id, …)`, `api/models/agent.py:464,502,568`); none of those is a
customer-facing product. A builder who needs "one tenant + user, many products" must
either model each product as an App (product isolation then rides the same leaky `app_id`
convention), as an unbound tag, or — the vendor's own answer — as a whole extra workspace,
which is Enterprise-licensed. The RAG plane (`Dataset`/`DocumentSegment`) has no
product/app/agent column at all, so a shared-tenant knowledge base has no product boundary
whatsoever. `test_no_product_id_column_exists` asserts this structurally.

### It's a multi-container platform, not a binary
Running Dify means api + Celery worker + web console + Postgres + Redis + a
code-execution sandbox + a vector DB, all as separate containers, plus a
model-provider key. There is no in-process/embedded mode — the SDK story is "POST to
our REST API." For an offline, keyless CI smoke there is nothing to run; this is the
polar opposite of aisotropy's single zero-config binary with in-process stores and a
stub LLM.

### Governance is a dashboard, not a construction
Pre-spend control is a per-app request-rate limit (`api_rpm`/`api_rph`) plus billing
quotas in the paid tier; a per-tenant token/$ budget checked before the call, a
provider circuit breaker, and quota-pressure degradation are DIY or PAID. The
transactional outbox is **NOT-POSSIBLE**: Celery tasks are enqueued separately from
the DB write, so a crash between the row write and the task enqueue orphans one — there
is no atomic (row + intent) co-commit.

### Dependency posture: fine, with two LGPL leaves
The 41 declared direct backend deps are 39 permissive (Flask/Celery/redis/boto3/
google-*/otel), with two weak-copyleft LGPL-3.0 leaves — `psycopg2-binary` and
`gmpy2` — both used unmodified as libraries (CAUTION, not a blocker). 0 GPL/AGPL, 0
BUSL/SSPL in the tree. The dep tree is not the problem; Dify's own license is.

## The verdict

Dify is the **most product-complete** framework in the arena — if you want a
batteries-included LLM app platform and you're deploying it for a single organization,
it's hard to beat on time-to-first-bot. But for **Orbit specifically** it fails twice:
its license forbids the multi-tenant SaaS shape, and — even if you bought the
commercial license — its isolation is shared-schema app-layer convention with no
product axis, no crash-safe outbox, and a heavy multi-container footprint.

**Best for:** a single company standing up an internal assistant / knowledge bot with
rich RAG and a visual builder, no multi-tenant resale.

**Worst for:** a multi-tenant support-desk SaaS (Orbit) — blocked by license, and
isolation-by-convention where a cross-tenant leak is an incident.

**vs. aisotropy:** Dify wins on product breadth and UX polish; aisotropy wins on
isolation-by-construction, the product scope axis, the transactional outbox,
single-binary sovereignty, and — decisively — a permissive license you may operate
multi-tenant without paying anyone.
