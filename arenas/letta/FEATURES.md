---
type: Feature matrix
title: "Letta (MemGPT) — features vs aisotropy"
description: Coverage map of Letta capabilities compared to aisotropy's built-ins, grounded in the real letta-client 1.12.1 API.
tags: [arenas, letta, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Letta — Features

> Grounded in the **real** `letta-client==1.12.1` SDK. API symbols named below
> (`client.blocks.list`, `agents.create(response_format=...)`, `Passage`, etc.) are
> the genuine current surface, verified against the installed package. See
> [`README.md`](./README.md) for what runs offline vs. what needs a live server.

## Coverage classes

Each feature is one of: **BUILT-IN** (framework provides it), **CONVENTION**
(possible but developer-enforced, no guardrail), **DIY** (you build it), **PARTIAL**,
**PAID** (only in the hosted tier), **NOT-POSSIBLE**.

| # | Feature | Class | Real API surface / note |
|---|---|---|---|
| 1 | Multi-tenant isolation | **CONVENTION** | `identity_id`/`project_id`/`tags` are optional, developer-passed filters on `blocks.list`/`agents.list`/`passages.search`; per-user **key** isolation is **PAID** (Letta Cloud projects) |
| 2 | Structured output + retry | **BUILT-IN** (no repair loop) | `agents.create(response_format={"type":"json_schema", ...})`; validation provider-side, no fail-closed reflection retry |
| 3 | Tool calling | **BUILT-IN** | LLM self-manages memory via base tools; `include_base_tools=True` |
| 4 | Streaming (SSE) | **BUILT-IN** | `agents.messages.create(..., stream=True)` / `.stream()` |
| 5 | Working memory | **BUILT-IN** | Core Memory `Block`s (in-context editable); `client.blocks` / `agents.blocks` |
| 6 | Semantic memory (vectors) | **BUILT-IN** | Archival Memory `Passage`s; `client.passages.search`, `client.archives` |
| 7 | Facts memory (journal) | **PARTIAL** | Recall Memory = searchable message history (`agents.messages`), not a dedicated FTS task/rule journal |
| 8 | Cost governance | **PARTIAL** (visibility BUILT-IN, gate DIY) | RUN-VERIFIED (`scripts/cost_obs_demo.py`): per-call usage IS returned — `LettaResponse.usage` (real `Usage` type: prompt/completion/total tokens, `step_count`, cache tokens) + `client.runs.usage.retrieve(run_id)`. But **per-tenant aggregation + USD are DIY** (no per-tenant meter, no dollar field), and there is **no pre-spend gate** (`Usage` is post-call; `max_steps` is a server-side loop cap, not a token/$ budget) |
| 9 | Observability | **PARTIAL** (native OTel BUILT-IN but SERVER-side) | RUN-VERIFIED: real `ProviderTrace` type carries `agent_tags`/`user_id`/`org_id`/`billing_context` per LLM call; `client.runs.trace.retrieve(run_id)` returns OTEL spans but its docstring requires **ClickHouse configured** — emission is server-side, **NOT-POSSIBLE from the offline client**. Client-side tenant-tagged spans are **DIY** (demo captures one in a real OTel `InMemorySpanExporter`); hosted per-tenant cost/health dashboards are **PAID** (Letta Cloud) |
| 10 | Sovereignty / licensing | **BUILT-IN** (Apache 2.0) | Self-hostable via Docker/Postgres |
| 11 | Language | Python | Server-side durable objects (not in-process) |

## Built-in capabilities

### Memory hierarchy — isomorphic to aisotropy (real types)
- **Core Memory** → `Block` (in-context editable blocks; RAM) ≈ BadgerDB working memory
- **Recall Memory** → message history (out-of-context searchable; disk) ≈ SQLite+FTS5 facts memory
- **Archival Memory** → `Passage` (unbounded vector store; cold) ≈ Qdrant semantic memory
- **AISOTROPY COMPARISON:** the closest conceptual match to aisotropy's memory tiers.
  Letta *validates the memory-OS thesis* independently — and here we use its **real**
  `BlockResponse`/`Passage` types, not a facsimile.

### Sleep-time compute — productized
- **Mechanism:** paired background agent consolidates memory during idle time
  (`agents.create(enable_sleeptime=True)` in the real API).
- **AISOTROPY COMPARISON:** direct analog of aisotropy's DataFusion sleep cycle. Letta is
  the **only** framework in this matrix that ships productized sleep-time consolidation.
  aisotropy's is dormant behind `AISOTROPY_SLEEPCYCLE_BIN`.

### Tool calling (LLM-managed)
- **Mechanism:** the LLM manages memory via base tools (`core_memory_append`,
  `archival_memory_insert/search`) — probabilistic.
- **AISOTROPY COMPARISON:** opposite approach. Letta: LLM self-manages memory (emergent).
  aisotropy: runtime-hydrated, deterministic and auditable.

## Convention / DIY / missing

### Multi-tenant isolation — CONVENTION (proven leak)
- **Status:** `client.blocks.list(identity_id=?, project_id=?, tags=?)` — every filter
  optional and developer-passed. Omit them and you scan the whole org. `blocks.retrieve(id)`
  applies no tenant check. Proven in `scripts/smoke.py` and `tests/test_isolation.py`
  against the real types.
- **AISOTROPY COMPARISON:** aisotropy enforces isolation **by construction** — ctx
  propagation + per-statement/key scoping; the unscoped read is *unrepresentable*.

### Structured output (fail-closed) — no repair loop
- **Status:** `response_format` schema is honored by the provider, but there is no
  fail-closed reflection retry on a schema break.
- **AISOTROPY COMPARISON:** aisotropy fails closed (502 on `/chat`) with a Gollem
  reflection loop.

### Cost governance — PARTIAL (visibility BUILT-IN, gate + per-tenant meter DIY)
- **Status (RUN-VERIFIED, `scripts/cost_obs_demo.py`):** usage VISIBILITY is built-in —
  every `agents.messages.create` returns `LettaResponse.usage`, the real `Usage` type
  (`prompt_tokens`/`completion_tokens`/`total_tokens`, `step_count`, cache tokens), and
  `client.runs.usage.retrieve(run_id)` gives per-run totals. What Letta does **not** give:
  (a) a **pre-spend gate** — `Usage` only exists *after* the call, and the only limit param,
  `max_steps`, is a server-side agent-loop cap, not a token/cost budget checked before the
  model runs; (b) a **per-tenant meter or any USD figure** — no dollar field on `Usage`. The
  demo runs the DIY versions (a per-tenant token/cost ledger + a pre-call budget check that
  rejects an over-budget tenant *before* the fake model is invoked — proven via a call
  counter) and labels them DIY.
- **AISOTROPY COMPARISON:** aisotropy has a token-bucket quota gate that rejects (429) BEFORE
  the LLM call *by construction*, plus a per-tenant cost meter (`/costs`). Letta gives you the
  raw usage number; the gate and the meter are yours to build.

### Observability — PARTIAL (native OTel BUILT-IN, but SERVER-side)
- **Status (RUN-VERIFIED):** native tracing exists — the Letta server bundles OpenTelemetry
  exporters and records a real `ProviderTrace` per LLM call carrying `agent_tags` (where
  tenant/user identity rides), `user_id`, `org_id`, `billing_context`, `latency_ms`;
  `client.runs.trace.retrieve(run_id)` returns the OTEL spans. BUT that retrieval docstring
  states **"Requires ClickHouse to be configured for trace storage"** — emission is
  server-side, so from the offline client it is **NOT-POSSIBLE** to emit a Letta-native span.
  The demo therefore also shows the **DIY** client path: a real OpenTelemetry
  `InMemorySpanExporter` capturing a `letta.agent.step` span with `tenant_id`/`user_id`
  attributes you set yourself. Turn-key per-tenant cost/health dashboards are **PAID** (Letta
  Cloud), not the OSS surface.
- **AISOTROPY COMPARISON:** aisotropy ships a per-tenant `/costs` + `/health` product surface;
  tenant identity is on the request by construction, not a developer-passed span tag.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv tracing, `/costs`, `/health`.

## Comprehensive feature-parity matrix (RUN-VERIFIED, 25 executable features)

Grounded by executing `scripts/feature_parity_demo.py` against the real
`letta-client==1.12.1` SDK surface (offline; real request types, synthetic
tokens/vectors). Each row's class is what the demo prints and asserts.

Run:
```bash
arenas/letta/.venv/bin/python arenas/letta/scripts/feature_parity_demo.py   # exit 0
```

| # | Feature | Class | Run-verified evidence (real SDK symbol) |
|---|---|---|---|
| 1 | Temperature hierarchy (tiered memory) | **BUILT-IN** | 3 tiers of different nature: `client.blocks` (Core) / `client.messages`+`MessageSearchParams` (Recall) / `client.archives`+`client.passages` (Archival). aisotropy=4 |
| 2 | Working memory (in-context state) | **BUILT-IN** | real `BlockResponse` core-memory block via `client.blocks` |
| 3 | Facts memory (keyword/FTS journal) | **PARTIAL** | Recall = searchable message history (`MessageSearchParams`); not a first-class FTS decisions/rules journal |
| 4 | Semantic memory (vector RAG) | **BUILT-IN** | real `Passage` archival store; `client.archives`+`passages.search` (offline cosine stand-in for HNSW) |
| 5 | Summary buffer (context engineering) | **BUILT-IN** | MemGPT core: server recursive summarizer + eviction (window plateaus) |
| 6 | Memory scoring (Rel×Imp×Rec) | **DIY** | Letta ranks archival by vector similarity (relevance) only; composite importance×recency is DIY |
| 7 | Contradiction / supersede | **PARTIAL** | self-editing memory BUILT-IN but LLM-driven (`core_memory_replace`); no deterministic `superseded_by`/cross-store invariant |
| 8 | Knowledge ingestion (chunk+embed) | **BUILT-IN** | `client.folders` (Sources/files) + `archival_memory_insert` chunk+embed server-side |
| 9 | BYO embedder | **BUILT-IN** | real `EmbeddingConfig` swappable seam (openai vs self-hosted ollama endpoint) |
| 10 | Fail-closed reflection loop | **DIY** | `response_format`/`JsonSchemaResponseFormat` validation BUILT-IN, but the repair loop is DIY (no fail-closed retry) |
| 11 | SSE streaming + immediate cancel | **PARTIAL** | streaming BUILT-IN (`messages.stream`, `create(streaming=/stream_tokens=)`); `messages.cancel` server-side, no client spend-cutoff guarantee |
| 12 | Stateless /ask + response cache | **DIY** | agents are stateful; no stateless route/cache — DIY exact cache (cross-tenant MISS) |
| 13 | Scope-gated tools (gov by absence) | **DIY** | per-agent tool sets BUILT-IN, but caller-scope visibility gating is DIY |
| 14 | Multi-step agent loop | **BUILT-IN** | server heartbeat/tool-chaining loop + `max_steps` + real `InitToolRule` tool_rules |
| 15 | Typed tools + MCP | **BUILT-IN** | real `Tool` (JSON-schema args) via `client.tools.create/upsert` + native `client.mcp_servers` |
| 16 | Bearer-token -> identity bind | **CONVENTION** | api_key bearer auths to the server; no must-agree bind to a path tenant. Tenant = `agents.identities`/tags; per-user keys PAID (Cloud) |
| 17 | Per-tenant pre-spend quota gate | **DIY** | no check-before-request (`Usage` post-call; `max_steps` is a loop cap). DIY gate proven via call counter (rejected turn spent 0) |
| 18 | Circuit breaker | **DIY** | no provider breaker in the client — DIY (open after N fails, fast-fail) |
| 19 | Quota-pressure degradation | **DIY** | no built-in load-based model swap — DIY policy |
| 20 | Cost meter (per-tenant) | **PARTIAL** | per-call `Usage` BUILT-IN + `runs.usage.retrieve`; per-tenant aggregation + USD DIY (no $ field) |
| 21 | OTel gen_ai spans w/ tenant_id | **PARTIAL** | native `ProviderTrace` (agent_tags/user_id/org_id) BUILT-IN but SERVER-side (needs ClickHouse); offline client span DIY |
| 22 | Health / readiness probe | **PARTIAL** | real `HealthResponse` (server liveness); no per-memory-tier readiness probe |
| 23 | Product evaluation (IR / judge) | **DIY** | no eval harness — DIY P@k/MRR over the real archival retrieval |
| 24 | Transactional outbox (atomic) | **NOT-POSSIBLE** | no atomic (message+event) txn + idempotent drain; closest is a durable message log with no co-committed intent |
| 25 | Async consolidation (sleep-time) | **BUILT-IN** | productized sleep-time compute: real `agents.create(enable_sleeptime=True)` paired background agent. Only arena with this |

**Run-verified tally (25 executable features):** BUILT-IN 9 · PARTIAL 6 · DIY 8 ·
CONVENTION 1 · NOT-POSSIBLE 1.

- **BUILT-IN (9):** #1, #2, #4, #5, #8, #9, #14, #15, #25 — the memory tiers,
  RAG, ingestion, BYO embedder, the agent loop, typed tools/MCP, and sleep-time
  consolidation. This is Letta's strength: it is the memory-OS competitor.
- **PARTIAL (6):** #3, #7, #11, #20, #21, #22 — facts-as-message-history,
  LLM-driven self-editing (no deterministic supersede), streaming without a
  client spend-cutoff, usage-without-per-tenant-meter, server-side OTel,
  liveness-without-tier-readiness.
- **DIY (8):** #6, #10, #12, #13, #17, #18, #19, #23 — the whole governance
  surface (pre-spend gate, circuit breaker, degradation, scope-gated tools,
  reflection repair loop, response cache, eval) + composite memory scoring.
- **CONVENTION (1):** #16 — identity binding (isolation leak proven in `smoke.py`).
- **NOT-POSSIBLE (1):** #24 — transactional outbox.

**The gap in one line:** Letta MATCHES aisotropy on memory tiers (its home turf —
3 tiers of different nature + sleep-time) but DIFFERS on *how*: LLM-self-managed
& probabilistic vs runtime-hydrated & deterministic; and it has none of
aisotropy's governance-by-construction — pre-spend gate, fail-closed reflection,
isolation binding, and the transactional outbox are DIY / CONVENTION / NOT-POSSIBLE.

## Multi-PRODUCT isolation — the 3rd scope axis (RUN-VERIFIED, CROSS-PRODUCT LEAK)

aisotropy scopes every memory read/write by **four** keys —
`(tenant_id, user_id, session_id, product_id)` — bound once at the Huma boundary and
propagated via `context.Context`. The isolation smoke here already showed the tenant/user
axes leak in Letta (developer-passed convention). This closes the gap on the **product**
axis, which the arena had never tested.

**Finding: `product_id` is just another developer-passed key the framework never binds to
anything.** Letta's real list/search API surface —
`client.blocks.list(project_id=, identity_id=, label=, tags=, match_all_tags=)` and
`client.passages.search(query=, tags=, tag_match_mode=, archive_id=)` — has **no
`product_id` parameter at all**. The only place a product boundary can live is a
developer-invented `product:<id>` tag that must be passed (with `match_all_tags=True`) on
*every* read. Miss it once and a sibling product reads your data.

Run:
```bash
arenas/letta/.venv/bin/python arenas/letta/scripts/product_isolation_demo.py   # exit 0
arenas/letta/.venv/bin/python -m pytest arenas/letta/tests/test_product_isolation.py -q  # 6 passed
```

RUN-VERIFIED output — same tenant `acme`, same user `alice`, two products
(`checkout` seeded, `analytics` reading):

```
[letta] Seeding (tenant=acme, user=alice, product=checkout) marker: lt-82a1934cf5713b89
  OK  Positive control: product 'checkout' saw its own marker
[letta] Leak #1 — product 'analytics' scopes by tenant+user (Letta has no product filter):
  LEAK 'analytics' saw 'checkout' marker (product axis not enforced)
[letta] Leak #2 — 'analytics' passes its OWN full tags but default OR match (real API default):
  LEAK ANY-tag default matched checkout via shared tenant/user tags
[letta] Leak #3 — client.blocks.retrieve(id) from a product-'analytics' path:
  LEAK retrieve-by-id returned checkout's block, no product check
[letta] Leak #4 — passages.search() scoped by tenant/user (no product):
  LEAK archival search crossed the product boundary

Result: CROSS-PRODUCT LEAK CONFIRMED — same tenant+user, wrong product reads the marker
```

Four independent cross-product reads confirmed: (1) the natural Letta-native scope
(project=tenant + tenant/user tags) omits product because there is no product filter to
pass; (2) the real API default `match_all_tags=False` matches on shared tenant/user tags
even when the caller passes its own `product:analytics` tag; (3) `blocks.retrieve(id)`
performs no product check; (4) archival vector search on tenant/user tags crosses the
boundary too. The positive control holds (harness healthy); the leak is the expected finding.

- **AISOTROPY COMPARISON:** `product_id` is one of four ctx-propagated scope keys, enforced
  per-statement — the cross-product read is **unrepresentable**, not one forgotten tag away.
  In Letta the product axis is convention on top of convention: a tag on the tenant/user
  tags that are *themselves* only convention. Class: **CONVENTION** (product isolation).

## Dependency license posture — executable governance gate (RUN-VERIFIED)

aisotropy's constitutional gate (`.claude/rules/constitutional-gates.md`) is **"100%
MIT/Apache, no copyleft/BUSL/SSPL/ELv2"**. This scans **this arena's real venv dependency
tree** with `pip-licenses` and reports the actual license mix — no guessed licenses.

Run:
```bash
arenas/letta/.venv/bin/pip-licenses --ignore-packages pip-licenses prettytable wcwidth
# record: arenas/letta/scripts/license_scan.txt
```

**Note on scope:** this arena pins `letta-client==1.12.1` (the thin, Stainless-generated
REST SDK) plus `pytest` and `opentelemetry-sdk` — **not** the full `letta` server package.
So the scanned tree is the **client** tree (22 packages), not the heavy server tree. The
scanner tooling itself (`pip-licenses`/`prettytable`/`wcwidth`) is excluded from the count.

**Real pip-licenses result — 22 packages:**

| Bucket | Count | Packages |
|---|---|---|
| Permissive (MIT / BSD / Apache-2.0 / ISC / PSF-2.0) | **21** | annotated-types, anyio, distro, h11, httpcore, httpx, idna, iniconfig, letta-client (Apache-2.0), opentelemetry-api/-sdk/-semantic-conventions (Apache-2.0), packaging (Apache-2.0 OR BSD), pluggy, pydantic, pydantic_core, pytest, sniffio (Apache;MIT), typing-inspection, typing_extensions (PSF-2.0), Pygments (BSD-2) |
| Weak copyleft (MPL 2.0) | **1** | **certifi** — Mozilla Public License 2.0 |
| Strong copyleft (GPL / LGPL / AGPL) | **0** | — |
| Restricted / source-available (BUSL / SSPL / ELv2 / Commons-Clause / FSL) | **0** | — |

**The one non-permissive package: `certifi` (MPL 2.0).** MPL 2.0 is *file-level* weak
copyleft — it obligates publishing modifications to the MPL-covered files themselves, but
does not reach into separately-licensed code and does **not** trigger SaaS source-disclosure
(unlike AGPL/SSPL). Under aisotropy's own `license-policy.md` it is classified ⚠️ **CAUTION**,
not a 🚫 BLOCKER. certifi is a ubiquitous CA-bundle transitive dep pulled by `httpx`; it is
the single technical exception to the literal "100% MIT/Apache" wording.

- **AISOTROPY COMPARISON:** the executed scan confirms Letta's client tree essentially holds
  the permissive line aisotropy claims — **zero** strong-copyleft, **zero** BUSL/SSPL/ELv2,
  and the only deviation is weak-copyleft certifi (CAUTION, not blocked), which aisotropy's
  own dependency tree also carries transitively via any HTTPS client. Letta itself is
  Apache-2.0 (feature #10). Verdict: **license gate PASSES** on this arena's real tree, with
  certifi/MPL-2.0 named as the sole caution.

## Summary

Letta is the **closest conceptual neighbor** to aisotropy. Its memory hierarchy
(Core/Recall/Archival) is nearly isomorphic to aisotropy's (BadgerDB/SQLite+FTS5/Qdrant),
and it's the only framework that has productized sleep-time consolidation.

But Letta is Python, LLM-self-managed memory (probabilistic vs aisotropy's deterministic),
server-side durable objects, and convention-based multi-tenancy. It validates the
memory-OS thesis while leaving aisotropy's exact combination (Go, isolation-by-construction,
runtime-hydrated deterministic memory) open.

**vs. aisotropy:** Letta wins on memory-hierarchy productization and sleep-time compute;
aisotropy wins on isolation-by-construction, deterministic memory, Go performance, and cost
governance. The trade: productized memory-OS with LLM-driven uncertainty vs. deterministic
runtime with constitutional isolation guarantees.
