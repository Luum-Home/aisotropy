---
type: Feature matrix
title: "LangGraph — features vs aisotropy"
description: Coverage map of LangGraph capabilities compared to aisotropy's built-ins.
tags: [arenas, langgraph, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# LangGraph — Features

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in (via LangChain)
- **Mechanism:** `with_structured_output` + `OutputFixingParser` / `RetryOutputParser`
- **Retry:** Opt-in — developer must attach `OutputFixingParser` manually
- **AISOTROPY COMPARISON:** Equivalent capability, but LangChain's is **opt-in** (developer must
  remember to attach the parser). aisotropy's is **default/runtime-wired** — no opt-in needed.
  This is the "default posture" nuance: same capability, different default.

### Tool calling
- **Status:** Built-in
- **Mechanism:** Graph nodes can be tools; `ToolNode` for execution
- **Integration:** First-class in the graph builder
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  LangGraph uses graph nodes; aisotropy uses Go generics.

### Streaming
- **Status:** Built-in
- **Mechanism:** `StreamingStdioCallbackHandler` + async iteration
- **Cancellation:** Python `asyncio.CancelledError` propagates
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE + `ctx` cancellation. Both stream
  deltas; aisotropy adds HTTP-level `Accept: text/event-stream` semantics.

### Working memory
- **Status:** Built-in
- **Mechanism:** Checkpointers (SQLite, Postgres, Redis) + Store
- **Features:** Durable execution, state persistence, graph checkpoints
- **AISOTROPY COMPARISON:** LangGraph's checkpointers are more mature (SQLite/Postgres/Redis).
  aisotropy's BadgerDB is in-process µs — faster but less durable. Both are built-in.

### Semantic memory (vectors)
- **Status:** Built-in
- **Mechanism:** `Store` with semantic cache + vector retrieval
- **Features:** Embedding integration, similarity search
- **AISOTROPY COMPARISON:** Both have built-in vector memory. LangGraph uses LangChain embeddings;
  aisotropy uses in-process cosine brute-force (active) + Qdrant gRPC (dormant).

## DIY / missing capabilities

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need PostgreSQL/SQLite with FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts journal out of the box.

### Cost governance (#8) — RUN-VERIFIED via `cost_obs_demo.py`
- **Per-call cost visibility:** **Built-in.** Every `AIMessage` carries
  `.usage_metadata` (`input_tokens` / `output_tokens` / `total_tokens`). Verified: read
  straight off the reply after each `GRAPH.invoke`.
- **Aggregation:** **Built-in helpers exist** (`UsageMetadataCallbackHandler`,
  `get_usage_metadata_callback()`) but they sum usage **keyed by MODEL NAME — no tenant
  concept.** Per-tenant attribution/totals is **DIY** (keep your own ledger; demo does).
- **Pre-spend budget gate:** **DIY — not provided.** Correction to the earlier note:
  LangChain 1.x has **no `UsageLimits`** and **no `check_before_request` equivalent** (that
  is a *Pydantic AI* API, not LangChain). The closest built-ins are
  `langchain.agents.middleware.ModelCallLimitMiddleware` (limits the *number of model
  CALLS* per run/thread, per-agent, reactive — not a token budget) and the reactive
  usage callbacks. Verified against langchain 1.3.13 / langchain-core 1.4.9: no pre-spend
  token gate. The demo implements the per-tenant token gate DIY and **proves the model is
  not reached on a rejected turn** (call-counter delta = 0).
- **AISOTROPY COMPARISON:** aisotropy ships a **pre-spend quota gate (token-bucket)** +
  **per-tenant cost meter** as runtime defaults. LangGraph gives per-call token visibility
  for free but leaves per-tenant aggregation and the pre-spend gate to the developer.

### Observability (#10) — RUN-VERIFIED via `cost_obs_demo.py`
- **Native OTel spans:** **Built-in — and they DO reach your own OTel exporter offline.**
  Correction to the earlier "Paid only" note: with `LANGSMITH_OTEL_ENABLED=true` +
  `LANGSMITH_OTEL_ONLY=true` and a global `TracerProvider` installed *before* importing
  LangChain, LangGraph emits **gen_ai-semconv spans** into an in-memory exporter with **no
  cloud call and no real API key**. Verified: 3 spans captured (`chat-turn` chain, `chat`
  node, `CountingFakeModel` LLM) carrying `gen_ai.system=langchain`,
  `gen_ai.operation.name`, and `gen_ai.usage.{input,output,total}_tokens` on the model span.
- **Important nuance:** the OTel emission **bridge ships inside the `langsmith` SDK package**
  (the `langsmith` dependency, not a standalone LangChain OTel module). It is free to run and
  can point at your own collector — but the machinery is LangSmith's.
- **`tenant_id` on spans:** **DIY.** Not automatic — you inject it into run
  `metadata`/`tags`; it then surfaces as `langsmith.metadata.tenant_id` (verified on all 3
  spans). LangGraph has no tenant concept to put there for you.
- **Managed dashboard:** **Paid.** LangSmith cloud is the native managed backend/UI for these
  traces (SaaS, key-gated) — the sales-gated path.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai-semconv tracing **with tenant scope by
  default**, plus `/costs` and `/health` endpoints — no SaaS dependency and no per-call
  wiring. LangGraph reaches the same span quality offline, but the tracer lives in the
  LangSmith SDK and tenant scoping is developer-owned; the managed path is sales-gated
  (a sovereignty concern for regulated deployments).

### Multi-tenant isolation
- **Status:** Tools provided; developer wires them
- **Naive posture:** on the raw `Store`, `Store.search()` uses element-wise namespace prefix
  matching and prefix depth is developer convention — a shallow prefix like `('acme',)` scans
  every tenant nested under `acme`. That is an under-scoped query (a dev bug the primitive does
  not catch), the same property any KV store has with no auth layer — not a framework defect.
- **Correct posture:** deep namespaces `(org, user, ...)` + the LangGraph Server
  `@auth.on.store()` handler (importable from `langgraph_sdk`, runs on every store op) rewrite/
  assert the namespace to the authenticated caller, making cross-tenant reads structurally
  impossible. Verified in `scripts/smoke.py` PART B via an identity-bound store facade.
- **AISOTROPY COMPARISON:** aisotropy binds all four axes from the verified identity via
  `context.Context` **by default**, as a `Scope{}` value object with unexported fields — a
  wrong/forgotten key is unrepresentable. LangGraph gives you the same capability but you must
  wire the auth handler. aisotropy's own raw stores also depend on its Scope/handler layer;
  isolation is an application-boundary property in both. aisotropy's edge is default-wired +
  unforgeable, not a stronger primitive.

### Multi-PRODUCT isolation (3rd axis) — RUN-VERIFIED via `product_isolation_demo.py`
Run: `.venv/bin/python product_isolation_demo.py` (exit 0; OFFLINE, no LLM). Verified against
langgraph 1.2.9. aisotropy scopes memory by a **4-axis `Scope` value object**
`(tenant_id, user_id, session_id, product_id)`, all bound to the verified identity at the
handler boundary and carried via `context.Context`. This demo isolates the **product_id** axis,
under both the naive raw-store posture and the correct identity-bound posture.

- **Status:** Tools provided; developer wires them. On the raw `BaseStore` there is **no
  `product` concept** — `product_id` is one more element in the namespace tuple
  `(tenant, user, product)`, and depth is developer convention.
- **PART A — raw store + naive convention (developer bugs the primitive won't catch):**
  - **Positive control (verified):** exact `get(("acme","userA","productX"), key)` reads
    productX's own private marker. Correct full-depth scoping works.
  - **2a — shallow-prefix bug:** a handler "serving productY" that scopes to
    `search(("acme","userA"))` and **forgets the product element** scans *every* product under
    that user — productX's marker surfaced (2 namespaces). An under-scoped query.
  - **2b — wrong-product bug:** an exact `get()` built with the wrong product string
    (`productX` where `productY` belonged) returns productX's data. The raw store performs no
    authorization — a same-key/wrong-key property of any KV store, not a LangGraph defect.
  - **Naive-config control (verified):** exact `get()` at the *correct* `(tenant,user,productY)`
    depth does NOT see productX's marker. Full-depth scoping is safe **if** the developer
    supplies the right element every time.
- **PART B — correct config (verified):** an identity-bound store facade (the `@auth.on.store()`
  analogue, forcing `namespace[:3]` to the caller) **eliminates both bugs** — the shallow-prefix
  search is rewritten to the caller's own namespace (productX never returned) and the
  wrong-product `get()` is rejected. Cross-product read is structurally impossible.
- **AISOTROPY COMPARISON:** `product_id` is a **field of the `Scope` value object**, constructed
  from verified identity **by default** — a forgotten or wrong `product_id` is
  **unrepresentable**. LangGraph reaches the same guarantee, but only once you wire the auth
  handler; the raw primitive alone leaves depth to developer discipline. aisotropy's own raw
  stores likewise rely on its Scope/handler layer; the edge is default-wired + unforgeable.

### Dependency LICENSE posture (executable governance) — RUN-VERIFIED via `scripts/license_scan.sh`
Run: `./scripts/license_scan.sh` (exit 0; OFFLINE; reads installed venv metadata). Tool:
`pip-licenses 5.5.5` over the real langchain/langgraph venv tree. aisotropy's constitutional
gate: "100% permissive (MIT/BSD/Apache-2.0/ISC), no copyleft/BUSL". This scans whether the framework's **actual**
dependency tree holds that line — reporting REAL `pip-licenses` output, not guesses.

- **Total packages scanned:** **53** (transitive tree of langgraph 1.2.9 / langchain 1.3.13 /
  langchain-anthropic / fastapi / uvicorn / opentelemetry, etc.).
- **Permissive:** **51 / 53** — MIT (26), BSD-2/3-Clause + "BSD License" (14), Apache-2.0 +
  "Apache Software License" (7 + dual `Apache-2.0 OR *`), plus PSF-2.0 (typing_extensions),
  0BSD/Zlib/CC0 (inside numpy's dual tag). All SAFE under aisotropy's own `license-policy.md`.
- **Strong copyleft (GPL/AGPL/LGPL):** **0.**
- **Restricted / source-available (BUSL/SSPL/ELv2/Commons-Clause/FSL):** **0.**
- **Weak copyleft (MPL-2.0):** **2** — `certifi` (`MPL-2.0`) and `orjson`
  (`MPL-2.0 AND (Apache-2.0 OR MIT)`, i.e. dual-licensed, usable under a permissive option).
  MPL-2.0 is file-level weak copyleft = **⚠️ CAUTION** per aisotropy's `license-policy.md`,
  **not a BLOCKER** (the constitutional gate names GPL/AGPL + source-available specifically).
- **Verdict:** The langgraph tree is **not purely permissive** — it is
  ~96% permissive with BSD/PSF permissive licenses and **2 weak-copyleft MPL-2.0** packages
  (`certifi`, `orjson`). It carries **zero** strong-copyleft and **zero** source-available/BUSL
  packages, so it clears aisotropy's *hard* blocker gate; the two MPL-2.0 deps are the only
  non-permissive items and are advisory (CAUTION), not fatal. aisotropy's own tree is not
  literally 100% MIT/Apache either — it carries BSD/ISC plus 1 transitive MPL-2.0 leaf
  (`docs/architecture/license-audit.md`) — both pass the same hard-blocker gate. The scan script
  exits non-zero if any hard-blocker license ever enters the tree — governance you can run in CI.

## Comprehensive feature parity (#1–#26) — RUN-VERIFIED via `feature_parity_demo.py`

Run: `.venv/bin/python feature_parity_demo.py` (exit 0; 26 `FEATURE` lines; OFFLINE fake
model + synthetic data). Verified against langgraph 1.2.9 / langchain 1.3.13 /
langchain-core 1.4.9 / langsmith 0.10.5 / opentelemetry-sdk 1.43.0. Every row below is the
class the demo **prints after actually executing** the framework (or the DIY version it had
to write, labelled DIY/NOT-POSSIBLE).

**Tally: BUILT-IN 8 · PARTIAL 7 · DIY 9 · NOT-POSSIBLE 2 (26 total).**

| # | Feature | Class | Run-verified evidence |
|---|---|---|---|
| 1 | Temperature-tier hierarchy | PARTIAL | 2 KV-nature surfaces (checkpointer + Store); no FTS tier, no offline batch tier. aisotropy=4 stores of different nature |
| 2 | Working memory (recent history) | BUILT-IN | `checkpointer(thread_id)` auto-hydrated 4 msgs across turns; unrelated thread isolated |
| 3 | Facts memory w/ keyword/FTS search | NOT-POSSIBLE | Store has no full-text/BM25 tier; `search(query=)` vector-only, `search(filter=)` exact-dict. Closest = DIY substring scan |
| 4 | Semantic / vector RAG | BUILT-IN | `Store(index=embed)` → `search(query=)` returned top vector hit (score 0.65) |
| 5 | Summary / rolling buffer | PARTIAL | `trim_messages()` BUILT-IN but DROPS (kept 5/20); rolling SUMMARY condensation is DIY |
| 6 | Importance×recency×relevance scoring | DIY | Store ranks by cosine only; composite Rel×Imp×Rec hand-rolled |
| 7 | Contradiction detection / supersede | DIY | `Store.put` = key-upsert only; semantic supersede hand-rolled (near-score 0.4 → delete old) |
| 8 | Knowledge ingestion (chunk+embed) | PARTIAL | embed-on-put + vector retrieval BUILT-IN; chunking is a separate splitter pkg (DIY here) |
| 9 | Bring-your-own embedder | BUILT-IN | `index={'embed': <callable>}` accepts any fn; swapping embedders changed ranking |
| 10 | Fail-closed reflection loop | PARTIAL | Pydantic parse/validate BUILT-IN (recovered in 2 tries); bounded retry+fail-closed LOOP is DIY (OutputFixingParser removed in LangChain 1.x) |
| 11 | SSE / token streaming | BUILT-IN | `.astream(stream_mode=...)` yields incremental events; `messages` mode for tokens |
| 12 | Immediate cancellation | BUILT-IN | `asyncio.CancelledError` propagates into the running node, cuts mid-flight, no partial persisted |
| 13 | Response cache (stateless) | BUILT-IN | `set_llm_cache(InMemoryCache())` short-circuited identical prompt (2 invokes → 1 call). GLOBAL cache, no tenant partition (footgun); per-tenant partition DIY |
| 14 | Scope-gated tools | DIY | you filter the tool list by scope before binding; `write_tasks` absent when scope missing. Gate is your code |
| 15 | Multi-step agent loop | BUILT-IN | `create_react_agent(model, tools)` ran reason→act→observe; tool actually invoked |
| 16 | Typed tools / MCP | PARTIAL | `@tool` derives typed JSON-Schema BUILT-IN; MCP is a separate adapter pkg (not core) |
| 17 | Bearer-token → identity bind | PARTIAL | The bare `Store`/graph primitive has no auth; but `@auth` (incl. `@auth.on.store()`, importable from `langgraph_sdk`) binds identity + rewrites store namespaces when run in a LangGraph **Server** — self-hosted OSS server or the managed Platform tier. Managed control plane is paid; the handler pattern itself is OSS. aisotropy binds JWT→scope in the runtime by default (401 default-deny / 403 mismatch) |
| 18 | Pre-spend per-tenant quota gate | DIY | no `UsageLimits`/`check_before_request`; hand-rolled gate proven (allowed Δ=1, rejected Δ=0) |
| 19 | Circuit breaker | DIY | no breaker (`with_fallbacks` is failure-fallback); hand-rolled breaker opened after 5 fails, rest fast-failed |
| 20 | Quota-pressure degradation | DIY | `with_fallbacks` is failure-based not pressure-based; hand-rolled pressure router (0.97→cheap, 0.40→primary) |
| 21 | Cost meter (per-tenant) | PARTIAL | `usage_metadata` per-call BUILT-IN; per-tenant aggregate DIY (helpers key by model name, no tenant) |
| 22 | OTel gen_ai spans | BUILT-IN | 3 gen_ai-semconv spans captured offline in-memory; `tenant_id` on spans is DIY; managed LangSmith is PAID |
| 23 | Health / readiness probe | DIY | library has no server; hand-rolled read-only tier probe (no LLM spend) |
| 24 | Product evaluation (IR metrics) | DIY | no offline IR harness (LangSmith evaluators = SaaS/PAID); P@2/recall hand-computed over real Store retrieval |
| 25 | Transactional outbox (atomic msg+event) | NOT-POSSIBLE | checkpointer + Store are separate stores, no shared ACID txn; msg+event can't be atomic (crash orphans one) |
| 26 | Async consolidation (dedup offline) | DIY | no sleep-cycle engine; hand-rolled offline dedup+denoise pass (5→2 facts) |

**Headline (memory tiers, the aisotropy differentiator):** LangGraph gives **2 tiers of the
same nature** — a checkpointer (thread history) and a Store (namespaced KV + optional vector
index). It has **no distinct full-text/FTS tier** (F3 NOT-POSSIBLE — `search` is vector-only
or exact-dict, never keyword/BM25) and **no offline batch-consolidation engine** (F26 DIY).
The **transactional outbox is NOT-POSSIBLE** (F25) because the checkpointer and Store share no
ACID transaction, so a message and its consolidation event cannot be written atomically.
aisotropy has **4 stores of different nature** (BadgerDB µs KV / SQLite+FTS5 keyword / Qdrant
vector / DataFusion offline batch) plus an atomic BadgerDB outbox. What LangGraph genuinely
ships BUILT-IN: working memory, vector RAG, BYO embedder, the agent loop, streaming +
cancellation, an exact response cache, and gen_ai OTel spans. All governance (pre-spend gate,
circuit breaker, pressure degradation, per-tenant meter, scope-gating) is DIY.

## What LangGraph does genuinely well (aisotropy has NO equivalent) — RUN-VERIFIED via `strengths_demo.py`

Run: `.venv/bin/python strengths_demo.py` (exit 0; OFFLINE, no LLM). Verified against
langgraph 1.2.9. The isolation demos show the one axis where aisotropy differs; this section is
the honest other side. LangGraph is a **durable, stateful graph runtime**. aisotropy is a
request/response LLM-OS with a memory hierarchy — it has **no durable graph, no checkpoint/resume,
no human-in-the-loop pause topology, no time-travel, no cyclic-graph orchestration**. On these axes
LangGraph wins outright. Each strength is exercised against the real API and self-validated.

| Strength | Pinned API (langgraph 1.2.9) | Run-verified evidence |
|---|---|---|
| Durable execution + human-in-the-loop | `StateGraph`; `checkpoint.memory.InMemorySaver`; `types.interrupt` / `types.Command` | Graph paused at `interrupt()`; the compiled instance was **deleted** and a **new graph on the same saver** recovered the pending node (`('approval_gate',)`) + interrupt payload, then `Command(resume="APPROVED")` completed the run. State lives in the checkpointer, not the object. |
| Time-travel over checkpoint history | `CompiledGraph.get_state_history(config)` | 5 checkpoints enumerated for a 3-iteration run; a **new run forked from a past checkpoint** (`steps=0`) replayed forward to `steps=3`. |
| Streaming modes on a cyclic graph | `CompiledGraph.stream(..., stream_mode="values"\|"updates")`; `add_conditional_edges` (cycles) | A self-looping node (agent-loop topology) observed as full snapshots (`values`: 0→1→2→3) and per-node deltas (`updates`: 3×`{'tick': ...}`) in the same run. |

**AISOTROPY COMPARISON (honest):** aisotropy does **not** implement a durable graph engine and
makes no claim to. Its streaming is LLM token deltas over Huma SSE with `ctx` cancellation — not a
graph-topology stream — and its "reflection loop" is a bounded in-request retry, not a resumable
checkpointed workflow. If your problem is a long-running, pausable, human-gated, forkable stateful
workflow, **LangGraph is the right tool and aisotropy is not**. aisotropy's differentiation is a
different axis entirely: multi-tenant isolation-by-default (unforgeable `Scope`), governance
defaults (pre-spend gate, breaker, per-tenant meter), and self-hosted sovereignty.

## Summary

LangGraph has the **largest ecosystem** and the most mature checkpointing story. Its isolation
model is robust when wired (deep namespaces + `@auth.on.store()` identity binding make cross-tenant
reads structurally impossible) but it is opt-in wiring, not a default — on the raw `Store`,
namespace depth is developer convention. Production observability (LangSmith) is paid/SaaS-gated,
which is a genuine sovereignty concern.

**vs. aisotropy:** LangGraph wins on ecosystem and checkpointing maturity; aisotropy wins on
isolation-**by-default** (the Scope value object binds all four axes automatically and cannot carry
a wrong/forgotten key), cost governance, self-hosted observability, and sovereignty. Both enforce
isolation at the application boundary — the difference is whether it is on by default and
unforgeable, not whether one framework can be made secure and the other cannot. The trade is:
largest ecosystem with isolation-you-wire + a paid SaaS observability path vs. a complete
self-hostable runtime with isolation and governance wired by default.
