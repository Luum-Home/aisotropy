# Dify Arena

> **Framework:** [Dify](https://github.com/langgenius/dify) — self-hostable low-code LLM app platform (Python: Flask + Celery + Postgres + Redis).
> **Analysis mode:** **SOURCE-VERIFIED, NOT EXECUTED.** Grounded in the real Dify backend source at HEAD (commit `1aea3460af1d`, 2026-07-16), transcribed into its own ORM; the *structural* schema claims were independently re-verified against Dify's live source in the honesty pass. The isolation + product findings are reproduced by an executable smoke over that transcribed schema (a labelled simulation — the naive-omission cross-read is a property of the shared-schema pattern, not a Dify code defect).
> **Positioning:** The broadest *product* in the arena set — batteries-included RAG/agents/tools — but its multi-tenancy is app-layer convention, it has no product scope axis, and its **own license forbids the multi-tenant SaaS that Orbit is** without a paid commercial license.

## What is real here vs. what is source-analysis (read this first)

Dify has **no in-process / embeddable Python library**. It is a **server platform**:
the API (Flask), a Celery worker, the web console, Postgres, Redis, a code-execution
sandbox, and a vector DB — all separate containers. The only way to run an Orbit
agent is to `POST` to a live Dify server's REST API (`/v1/chat-messages`) with a
per-app API key, and that server needs a database, the worker, and a model-provider
key. **None of that can boot in this offline env**, and this arena does **not** fake
a server connection.

So, exactly like the [`letta`](../letta) arena did for its server, this is an **honest
source analysis**:

| Piece | Status |
|---|---|
| Dify backend source (`api/models`, `api/services`, `api/core/rag`, `LICENSE`, `pyproject.toml`) | **Real** — cloned at HEAD `1aea3460` (2026-07-16), read line-by-line; every claim cites `api/<path>:<line>` |
| Isolation model (shared-schema `tenant_id`, app-layer `.where()`, no RLS) | **Source-verified** — grep-confirmed 0 `CREATE POLICY`/`ENABLE ROW` in `api/` |
| The LICENSE multi-tenant clause | **Real** — quoted verbatim below from the repo-root `LICENSE` |
| Orbit chatbot payload (`orbit/agent.py:build_chat_payload`) | **Real** — the genuine `/v1/chat-messages` body shape |
| A running Dify **agent turn** (`POST /chat-messages`) | **NOT run here** — needs the full platform + a provider key |
| The offline isolation/product smoke | **Labelled behavioral simulation** — Dify's real schema transcribed into **Dify's own ORM (SQLAlchemy 2.0)**, real query contract, deterministic responder instead of the LLM |

> **Provenance / honesty caveat (line numbers).** The `api/<path>:<line>` anchors
> were transcribed from the pinned clone `1aea3460`. In this honesty pass the
> *structural* claims were independently re-verified against Dify's live source and
> all hold: `Conversation`/`Message` carry `app_id` but **no `tenant_id`**, `App`
> carries `tenant_id`, `Account.set_tenant_id_with_session` membership-checks a
> `TenantAccountJoin` row, no `product_id` column exists anywhere, and the LICENSE
> §1a multi-tenant clause is verbatim-real. What was **not** re-verified here are the
> exact *line offsets* at that (future-dated) commit — the clone was not available to
> re-open — so treat the `:NNN` numbers as approximate pointers, not re-checked
> facts. Where two arena files disagreed on a number they were reconciled to one.

Why a simulation of the persistence layer at all: to prove feature #1 (tenant
isolation) against Dify's *real* state model without a live Postgres, `orbit/models.py`
transcribes Dify's actual tables (`App.tenant_id`, `Conversation.app_id`-only,
`Dataset`/`DocumentSegment.tenant_id`) and `orbit/store.py` replicates the *documented,
developer-written* filter contract (`.where(App.tenant_id==?)`, `conversation_service`'s
`.where(Conversation.app_id==app_model.id)`) using Dify's genuine ORM. Every model
and query cites the real source line it mirrors.

## Setup

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt        # SQLAlchemy (Dify's real ORM) + pytest + pip-licenses
```

## Smoke test (offline, no server, no key)

```bash
python scripts/smoke.py                 # exit 0 = harness healthy + naive-omission cross-read reproduced
pytest tests/ -q                        # 9 passed
```

If `DIFY_API_URL` + `DIFY_API_KEY` are set it additionally attempts a **real
read-only server ping** (`GET /v1/parameters`) and reports the result honestly
(never fabricated).

## Live serve (needs a real Dify platform)

```bash
# 1. Stand up the full Dify stack (multi-container):
git clone https://github.com/langgenius/dify && cd dify/docker
cp .env.example .env && docker compose up -d      # api+worker+web+db+redis+sandbox+vectordb
# 2. In the web console: create a Chat app, publish it, copy its per-app API key.
export DIFY_API_URL=http://localhost/v1
export DIFY_API_KEY=app-xxxxxxxxxxxxxxxx
python scripts/smoke.py                 # now also pings the live server
```

On the live path, `orbit/agent.py:send_message` POSTs the real `/v1/chat-messages`
body. **Note:** the per-app API key scopes to **one app** — running Orbit as a
multi-tenant service (many workspaces behind one deployment) is exactly the
LICENSE-restricted use below.

## What Dify does genuinely well

The isolation smokes below are adversarial by design. In fairness, Dify is the
**broadest *product* in the arena set** — a batteries-included, low-code LLM app
platform — and that breadth is a real strength aisotropy (a library + runtime, not a
product) does not try to match. Grounded in the real source; run-verified where the
offline/no-server path allows.

```bash
python scripts/strengths_demo.py          # exit 0 — bind + data planes + payload run-verified
```

**1. Membership-checked identity→tenant bind — RUN-VERIFIED.**
`Account.set_tenant_id_with_session` (`account.py:166`) verifies a `TenantAccountJoin`
row before an account may act as a workspace — you cannot switch into a workspace you
don't belong to. This is a *real, structurally-enforced* identity bind, stronger than
most arenas here, and it executes against the transcribed ORM (`strengths_demo.py`).
It gates *which tenant you may be*; the per-query filter is still the developer's job.

**2. Real product data planes (chat history + per-dataset RAG) — RUN-VERIFIED (shape).**
Two durable, server-side planes ship out of the box: conversation history
(`Conversation`/`Message`) and per-dataset RAG knowledge (`Dataset`/`DocumentSegment`
+ a per-dataset vector collection over ~15 backends via `vector_factory`). Both are
product features you'd otherwise hand-build; seeded and correctly-scoped-read here.

**3. Genuine `/v1/chat-messages` streaming contract — RUN-VERIFIED (payload).**
A stable REST API: `response_mode="streaming"` (SSE) + a stop-generation endpoint.
The real request body is built offline; the live POST is **NOT-RUN** (needs a server).

**4. Batteries-included product surface — DESCRIBED (server/UI-backed, NOT-RUN).**
Dify's headline strength, and inherently not executable offline, so described from the
real source, not faked: a **visual low-code app builder**, **workflow orchestration**
(node DAG + per-node retry), agent apps (function-calling/ReAct) with built-in +
OpenAPI tools + MCP, a **provider registry** across dozens of model vendors,
per-message cost with a real USD field + moderation, and a large **plugin ecosystem**.

**The honest two-sided read.** Dify is genuinely great at being a *complete product*:
ship a polished bot in an afternoon with no backend code. aisotropy does **not** compete
on product breadth or UX — it is an embeddable runtime. Where aisotropy differs, and it
is narrow but load-bearing: (a) it is self-hostable for **multi-tenant** use with **no
license blocker**, and (b) isolation is **by construction** — the unscoped cross-read is
*unrepresentable*, not merely avoidable. Dify is stronger as a product; aisotropy is
stronger on this one isolation-and-sovereignty axis. Both statements are true.

## Isolation model

**Convention — shared-schema `tenant_id` discriminator column, enforced 100% in
application code.** This is the *standard shared-schema multi-tenant tradeoff*, not a
Dify defect: correct when the developer writes the ownership gate, cross-reads when
they skip it — the same same-key/wrong-key failure mode of any shared-schema-no-RLS
store (a bare SQL table, or aisotropy's own BadgerDB if you dropped the Scope filter).

Dify's multi-tenancy is the classic single-database, shared-schema pattern:

- Top-level rows carry a `tenant_id` column: `App` (`model.py:417`), `Dataset`
  (`dataset.py:180`), `Document` (`dataset.py:546`), `DocumentSegment`
  (`dataset.py:919`). Isolation is a Python `.where(Model.tenant_id ==
  current_tenant_id)` the developer must remember on **every** read.
- **There is no Postgres Row-Level Security and no schema-per-tenant** — grep for
  `CREATE POLICY` / `ENABLE ROW` across the whole `api/` tree returns **0** hits.
- **`Conversation` and `Message` have no `tenant_id` at all** (`model.py:1141`,
  `:1506` — structurally re-verified against live Dify). They key on `app_id`;
  tenant scope is purely transitive. Dify's own `conversation_service.py:51` does
  `select(Conversation).where(Conversation.app_id == app_model.id)` and **keeps** the
  upstream tenant gate (`app_model` is tenant-resolved first). A *developer* who
  holds an `app_id` and skips that gate reads across tenants — so the failure mode
  lives in the naive caller's omission, not in Dify's own service code.

```python
# The correct read and the leak are one clause apart (Dify's real query shapes):
select(App).where(App.tenant_id == current_tenant_id)         # correct: App has tenant_id
select(Conversation).where(Conversation.app_id == app.id)      # safe ONLY if `app` was tenant-checked
select(Conversation).where(Conversation.app_id == raw_app_id)  # LEAK: no tenant gate, no tenant_id column
session.get(Conversation, conv_id)                             # LEAK: raw id, nothing to check
select(DocumentSegment)                                        # LEAK: forgot .where(tenant_id==?)
```

**One thing Dify does better than most arenas:** the account→tenant bind is *real*.
`Account.set_tenant_id_with_session` (`account.py:166`) verifies a `TenantAccountJoin`
membership row before it will let an account act as a tenant — you cannot switch into
a workspace you don't belong to. But that gates *which tenant you may be*, not whether
each individual data query remembers to filter by it.

```text
Dify's memory/data planes (all real, transcribed in orbit/models.py):
  Conversation history -> Conversation/Message (Postgres, keyed app_id)   ~ working memory
  Knowledge base       -> Dataset/Document/DocumentSegment (tenant_id) +  ~ semantic memory
                          per-dataset vector collection (dataset_id filter)
  (no FTS facts journal tier; no offline batch/consolidation tier)
```

## The license — the governing governance finding

Dify is **not plain Apache-2.0**. Its repo-root `LICENSE` is a **modified Apache-2.0**
with additional conditions. Verbatim §1a:

> **a. Multi-tenant service:** Unless explicitly authorized by Dify in writing, you
> may not use the Dify source code to operate a multi-tenant environment.
> — Tenant Definition: Within the context of Dify, one tenant corresponds to one
> workspace. The workspace provides a separated area for each tenant's data and
> configurations.

**Orbit is a multi-tenant support chatbot** (many customer workspaces behind one
service). Operating it on self-hosted Dify is precisely the prohibited use and
requires a **commercial license** from LangGenius (the Enterprise tier). Under
aisotropy's own [`license-policy.md`](../../.claude/rules/cos/license-policy.md), a
commercial-use/field-of-use restriction of this kind is a 🚫 **BLOCKER** for a SaaS
product — the same class as BSL/SSPL/ELv2. This is the single most important finding
in the arena, and it is a *governance* finding, not a dependency one.

## Features covered

| # | Feature | Status | Notes (real Dify source) |
|---|---|---|---|
| 1 | Multi-tenant isolation | **Convention** | shared-schema `tenant_id` + app-layer `.where()`; no RLS; `Conversation` has no tenant_id. Naive-omission cross-read reproduced in `scripts/smoke.py` + `tests/` (standard shared-schema tradeoff, not a Dify defect) |
| 2 | Structured output + retry | Partial | workflow structured-output + per-node retry; no fail-closed reflection loop |
| 3 | Tool calling | Built-in | agent apps + built-in/OpenAPI tools + MCP |
| 4 | Streaming | Built-in | `response_mode=streaming` SSE + stop endpoint |
| 5 | Working memory | Built-in | conversation history (Postgres, server-side) |
| 6 | Semantic memory | Built-in | Knowledge base RAG, ~15 vector backends |
| 7 | Facts memory (journal) | DIY | no FTS decisions/rules journal |
| 8 | Cost governance | Partial | per-message `total_price`+`currency` BUILT-IN; per-app rate limit; per-tenant $ gate = billing tier |
| 9 | Observability | Partial | OTel instrumentation + tracing integrations; `gen_ai`+tenant tag DIY |
| 10 | Sovereignty / licensing | **Blocker for Orbit** | modified Apache-2.0 forbids multi-tenant operation without a commercial license |
| 11 | Language | Python | multi-container server platform |

Full per-feature comparison (43 rows) in [`FEATURES.md`](./FEATURES.md).

## What actually executed (closed loop)

Offline, no server, no LLM key:

- `python -m compileall orbit scripts tests` → OK
- `python scripts/smoke.py` → **exit 0** (positive control held; naive-omission cross-read reproduced on 3 real query shapes)
- `python scripts/product_isolation_demo.py` → **exit 0** (no product axis exists to enforce; no `product_id`)
- `python scripts/strengths_demo.py` → **exit 0** (steelman: membership bind + data planes + streaming payload run-verified; product surface DESCRIBED/NOT-RUN)
- `pytest tests/ -q` → **9 passed**
- `pip-licenses` (real, on the harness venv) → 7/7 permissive; Dify manifest classified source-declared in `scripts/license_scan.txt`

**Not executed** (needs the full multi-container platform + a provider key): a real
`POST /v1/chat-messages` agent turn. The live code path exists in `orbit/agent.py`
and activates only when `DIFY_API_URL`/`DIFY_API_KEY` point at a real server.

## Adversarial isolation result

**Shared-schema convention, honestly bounded — a reproduced developer-bug, not a Dify
defect.** Multi-tenancy is a shared-schema `tenant_id` column enforced by hand-written
SQLAlchemy `.where()`; there is no RLS or schema isolation, and `Conversation`/`Message`
carry no `tenant_id`. The smoke's `_UNCHECKED`/`_UNSCOPED` reads are the *naive caller's
omission* of the ownership gate — the same same-key/wrong-key cross-read you'd hit on any
shared-schema-no-RLS store (a bare SQL table, or aisotropy's own BadgerDB if you dropped
the Scope filter). Dify's own services keep the gate; what the smoke measures is that the
pattern makes the ungated read *available*, not that Dify itself leaks (`scripts/smoke.py`).
Separately, there is **no product axis at all** — `product_id` exists nowhere in
`api/models`, so the fourth scope boundary is simply unmodeled, and even the tenant-correct
RAG read crosses products (`scripts/product_isolation_demo.py`).

**vs. aisotropy:** aisotropy binds all four scope axes `(tenant, user, session, product)`
from `context.Context` at the request boundary, enforced per-statement — the unscoped read
is *unrepresentable*, and there is no ungated primitive to reach for. Dify wins on product
breadth; aisotropy wins on isolation-by-construction and a license with no multi-tenant
field-of-use limit. Both are true.
