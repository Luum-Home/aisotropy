# Dify Arena

> **Framework:** [Dify](https://github.com/langgenius/dify) — self-hostable low-code LLM app platform (Python: Flask + Celery + Postgres + Redis).
> **Analysis mode:** **SOURCE-VERIFIED, NOT EXECUTED.** Grounded in the real Dify backend source at HEAD (commit `1aea3460af1d`, 2026-07-16), read line-by-line. The isolation + product findings are additionally proven by an executable smoke that transcribes Dify's real schema into its own ORM.
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
python scripts/smoke.py                 # exit 0 = harness healthy + leak demonstrated
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

## Isolation model

**Convention — shared-schema `tenant_id` discriminator column, enforced 100% in
application code.**

Dify's multi-tenancy is the classic single-database, shared-schema pattern:

- Top-level rows carry a `tenant_id` column: `App` (`model.py:417`), `Dataset`
  (`dataset.py:180`), `Document` (`dataset.py:546`), `DocumentSegment`
  (`dataset.py:919`). Isolation is a Python `.where(Model.tenant_id ==
  current_tenant_id)` the developer must remember on **every** read.
- **There is no Postgres Row-Level Security and no schema-per-tenant** — grep for
  `CREATE POLICY` / `ENABLE ROW` across the whole `api/` tree returns **0** hits.
- **`Conversation` and `Message` have no `tenant_id` at all** (`model.py:1141`,
  `:1506`). They key on `app_id`; tenant scope is purely transitive. Dify's own
  `conversation_service.py:51` does `select(Conversation).where(Conversation.app_id
  == app_model.id)` and trusts that `app_model` was tenant-resolved upstream. Hold
  an `app_id` and skip that upstream check → cross-tenant read.

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
| 1 | Multi-tenant isolation | **Convention** | shared-schema `tenant_id` + app-layer `.where()`; no RLS; `Conversation` has no tenant_id. Leak proven in `scripts/smoke.py` + `tests/` |
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
- `python scripts/smoke.py` → **exit 0** (positive control held; leak demonstrated on 3 real query shapes)
- `python scripts/product_isolation_demo.py` → **exit 0** (cross-product leak; no `product_id` exists)
- `pytest tests/ -q` → **9 passed**
- `pip-licenses` (real, on the harness venv) → 7/7 permissive; Dify manifest classified source-declared in `scripts/license_scan.txt`

**Not executed** (needs the full multi-container platform + a provider key): a real
`POST /v1/chat-messages` agent turn. The live code path exists in `orbit/agent.py`
and activates only when `DIFY_API_URL`/`DIFY_API_KEY` point at a real server.

## Adversarial isolation result

**LEAK CONFIRMED — convention-based, on Dify's real state model.** Multi-tenancy is a
shared-schema `tenant_id` column enforced entirely by hand-written SQLAlchemy
`.where()`; there is no RLS or schema isolation, and `Conversation`/`Message` carry no
`tenant_id`, so a query that forgets the app-ownership gate crosses tenants. See
`scripts/smoke.py`. And there is **no product axis** — `product_id` exists nowhere in
`api/models` (`scripts/product_isolation_demo.py`).
