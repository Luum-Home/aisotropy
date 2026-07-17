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
| 1 | Multi-tenant isolation | **CONVENTION** (org-wide) / **BUILT-IN by agent_id** (agent-scoped) | Org-wide `blocks.list`/`passages.search` take optional, developer-passed `identity_id`/`project_id`/`tags` — under-scope them and you scan the org. The **agent-scoped** subresources (`agents.blocks.list(agent_id)`, `agents.passages.search(agent_id)`) isolate by `agent_id` by construction; Letta's documented multi-tenant pattern is **one agent per principal**. Per-user **key** isolation is **PAID** (Letta Cloud projects) |
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

## What Letta does genuinely well (steelman, RUN-VERIFIED where offline allows)

Letta is a **memory-OS product**. Three capabilities are genuinely differentiated —
things aisotropy does **not** productize the same way — each grounded in the pinned
`letta-client==1.12.1` surface. Run:

```bash
arenas/letta/.venv/bin/python arenas/letta/scripts/strengths_demo.py            # exit 0
arenas/letta/.venv/bin/python -m pytest arenas/letta/tests/test_strengths.py -q  # 6 passed
```

| Strength | Real SDK symbol | Status | aisotropy contrast (honest) |
|---|---|---|---|
| **Self-editing core memory** (MemGPT heritage) | `client.blocks.update(block_id, value=)` / `client.agents.blocks.update(block_label, agent_id, value=)`; base tools `core_memory_append` / `core_memory_replace` via `include_base_tools=True` | **RUN-VERIFIED** (offline) — a real `BlockResponse` rewritten in place across turns, edit persists | aisotropy's memory is runtime-hydrated & deterministic; it does **not** ship an LLM that edits its own core-memory blocks |
| **Sleep-time consolidation** | `agents.create(enable_sleeptime=True)` (docstring: "memory management will move to a background agent thread"); `agents.messages.compact(...)` | **DESCRIBED** — lever SDK-verified offline; execution needs a server (**NOT-RUN**) | direct analog of aisotropy's DataFusion sleep cycle — but Letta ships it as a product API today; aisotropy's is dormant behind `AISOTROPY_SLEEPCYCLE_BIN` |
| **Stateful agent-as-a-service** | durable `AgentState` (`id`/`memory`/`blocks`/`tools`/`sources`/`enable_sleeptime`); `agents.messages.list(agent_id)` recall history | **DESCRIBED** — state shape shown offline against the real type; cross-restart durability needs server+DB (**NOT-RUN**) | aisotropy is stateless-per-request, hydrating memory from its tiers each call — a deliberately different model; Letta's persistent agent is a real strength for long-lived assistants |

**Two-sided read:** Letta is genuinely great at *stateful, self-editing,
self-consolidating memory* (its home turf). aisotropy differs on a narrow but
load-bearing axis — isolation-by-default and deterministic memory — with the
single-org caveat detailed below.

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

### Multi-tenant isolation — CONVENTION on the org-wide surface (proven), BUILT-IN by agent_id
- **Status:** the ORG-WIDE primitives `client.blocks.list(identity_id=?, project_id=?, tags=?)`
  take every filter as optional and developer-passed — omit them and you scan the whole org;
  `blocks.retrieve(id)` applies no tenant check. Proven in `scripts/smoke.py` and
  `tests/test_isolation.py` against the real types. **But** the AGENT-SCOPED subresources
  (`client.agents.blocks.list(agent_id)`, `client.agents.passages.search(agent_id)`) require
  an `agent_id` and isolate by construction — Letta's documented multi-tenant pattern is
  **one agent per principal**, mapping verified caller → `agent_id` at the app layer. The
  residual gap vs. aisotropy is that binding is single-org and app-level, not framework-level:
  OSS Letta has no must-agree bind from an authenticated caller to a tenant, and the org-wide
  primitives remain available with no guard. (Letta's older per-principal **identities** are
  being deprecated in favour of per-agent scoping; in 1.12.1 the SDK exposes only
  `agents.identities.attach/detach`, no top-level identities resource.)
- **AISOTROPY COMPARISON:** aisotropy enforces isolation **by construction** — ctx
  propagation + per-statement/key scoping bound from the request boundary; the unscoped read
  is *unrepresentable* and there is no org-wide primitive to fall back to.

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
| 16 | Bearer-token -> identity bind | **CONVENTION** | api_key bearer auths to the server; no must-agree bind to a path tenant. Isolation is per-agent-by-convention (one agent per principal; reads via `agents.blocks.list(agent_id)`/`agents.passages.search(agent_id)`) + app-layer caller→agent_id mapping; per-user keys PAID (Cloud). Older `agents.identities` are being deprecated in favour of per-agent scoping |
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
- **CONVENTION (1):** #16 — caller→tenant binding (single-org: no framework bind; org-wide under-scoping leak proven in `smoke.py`, agent-scoped reads isolate by `agent_id`).
- **NOT-POSSIBLE (1):** #24 — transactional outbox.

**The gap in one line:** Letta MATCHES aisotropy on memory tiers (its home turf —
3 tiers of different nature + sleep-time) but DIFFERS on *how*: LLM-self-managed
& probabilistic vs runtime-hydrated & deterministic; and it has none of
aisotropy's governance-by-construction — pre-spend gate, fail-closed reflection,
isolation binding, and the transactional outbox are DIY / CONVENTION / NOT-POSSIBLE.

## Multi-PRODUCT scoping — the 3rd axis, honestly (RUN-VERIFIED)

aisotropy scopes every memory read/write by **four** keys —
`(tenant_id, user_id, session_id, product_id)` — bound once at the Huma boundary and
propagated via `context.Context`. `test_isolation.py` already proves Letta leaks the
tenant/user axes when a developer **under-scopes the org-wide primitives**. This section
asks the honest follow-up for a third axis (product): what does a *competent* Letta
developer do, and what genuine sharp edges remain?

**The competent answer is one agent per principal.** Letta's documented multi-tenant
pattern ([docs.letta.com/guides/agents/multi-user](https://docs.letta.com/guides/agents/multi-user))
is a distinct agent per `(tenant, user, product)`, read through the **agent-scoped
subresources**: `client.agents.blocks.list(agent_id)` and
`client.agents.passages.search(agent_id, query=...)`
([passages/search](https://docs.letta.com/api/python/resources/agents/subresources/passages/methods/search/)).
Those **require** an `agent_id` (the real SDK rejects an empty one; the route is
`/v1/agents/{agent_id}/archival-memory/search`) and return **only that agent's** memory.
Isolation is by `agent_id`, **by construction** — no `product:<id>` tag, and nothing to
forget. A competent developer therefore **avoids three of the four sharp edges** this
arena's earlier draft dressed up as "cross-product leaks."

What genuinely **remains** is narrower and real — Letta OSS is a **single org** with no
framework binding between the caller and a tenant/product, so *if* you drop from the
agent-scoped reads to the org-wide primitives:

- **Single-org, no caller binding.** `client.blocks.list(...)` has `project_id` (tenant)
  but no product filter and no caller binding; under-scoping returns a sibling's block.
  This is the **same** single-org finding `test_isolation.py` proves for tenant/user — not
  a distinct product mechanism.
- **Unsafe DEFAULT, not a structural leak.** If you lean on the tag convention on the
  org-wide surface, the SDK default `match_all_tags=False` (ANY-tag / OR) matches on the
  shared tenant/user tags **even when you also pass a distinct `product:<id>` tag**. The
  fix is one parameter away — `match_all_tags=True`. It is a DX footgun, not a cross-product
  leak. (The default really is ANY-tag: the `blocks.list` docstring reads "*If True, only
  returns blocks that match ALL given tags. Otherwise, … ANY of the passed-in tags.*")
- **Genuine, but needs a foreign id.** `client.blocks.retrieve(id)` performs no ownership
  check — but it only bites a caller who **already holds** a foreign block id (a
  row-PK-without-row-level-security shape, not an ambient org-wide scan).

> **Correction (adversarial-audit follow-up).** An earlier draft of this section modelled a
> top-level org-wide `passages.search()` **without** `agent_id` as a fourth, distinct
> "cross-product leak." That framing was wrong: the real `letta-client==1.12.1`
> `client.passages.search` **accepts `agent_id`** (its docstring: "*If neither agent_id nor
> archive_id is provided, searches ALL passages in the organization*"), and the isolated
> path is the agent-scoped `agents.passages.search(agent_id)`. The org-wide search crossing
> a boundary is just the same single-org finding, not a product-specific one. It has been
> removed; the archival axis now demonstrates the agent-scoped search **isolating**.

Run:
```bash
arenas/letta/.venv/bin/python arenas/letta/scripts/product_isolation_demo.py   # exit 0
arenas/letta/.venv/bin/python -m pytest arenas/letta/tests/test_product_isolation.py -q  # 8 passed
```

RUN-VERIFIED output — same tenant `acme`, same user `alice`, two products each on its own
agent (`checkout` seeded, `analytics` reading):

```
[letta] Seeding principal (acme/alice/checkout, agent=agent-af31436e…) marker: lt-…
  OK  Positive control: checkout's agent saw its own marker
[letta] Strongest config — one agent per principal, agent-scoped reads:
  OK  agent-scoped reads isolate: checkout invisible to analytics' agent (no product tag needed)
[letta] Residual sharp edges — ONLY if you use the ORG-WIDE primitives …:
  NOTE org-wide blocks.list is single-org: no product filter, no caller binding
  FOOTGUN ANY-tag default matched checkout; match_all_tags=True is clean
  GENUINE retrieve-by-id returned checkout's block (needs a foreign id first)

Result: agent-scoped reads isolate by construction; org-wide primitives remain single-org
```

- **AISOTROPY COMPARISON:** `product_id` is one of four ctx-propagated scope keys bound at
  the boundary — enforced per-statement, the cross-product read is **unrepresentable** and
  there is **no** org-wide primitive to fall back to. Letta reaches product isolation the
  competent way (one agent per principal, isolated by `agent_id`); the residual gap is
  narrower: the app must map verified caller → `agent_id` itself, and the org-wide
  primitives carry no binding if a developer reaches for them. Class for product scoping via
  the agent-scoped reads: **BUILT-IN (by agent_id)**; via the org-wide tag convention:
  **CONVENTION** with an unsafe default.

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
