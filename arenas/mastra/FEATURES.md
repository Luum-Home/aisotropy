---
type: Feature matrix
title: "Mastra — features vs aisotropy"
description: Coverage map of Mastra capabilities compared to aisotropy's built-ins.
tags: [arenas, mastra, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Mastra — Features

## Where Mastra genuinely wins (TS-native DX) — run-verified

Before the isolation critique, the honest headline: Mastra is a **TypeScript-native agent framework**,
and on its home turf it is excellent — a category aisotropy (a Go runtime) does not compete in at all.
`scripts/strengths_demo.ts` (`npm run strengths`) executes these live against the pinned install
(`@mastra/core@1.51.0`), fully offline, exit 0:

### Durable, typed workflows — **BUILT-IN, run-verified**
- **Symbols:** `createWorkflow`, `createStep` (`@mastra/core/workflows`); `Mastra` (`@mastra/core`);
  `LibSQLStore` (`@mastra/libsql`).
- **Executed:** `.then` (sequential) + `.branch` (conditional) control flow, plus **suspend/resume**
  human-in-the-loop. A `$200` charge routes through the auto-approve branch; a `$5000` charge routes to
  a `humanReview` step that **suspends** (run snapshot persisted), then `resume()`s with typed reviewer
  input to a `success`. Each step carries Zod `input`/`output`/`resume`/`suspend` schemas end to end;
  a malformed workflow input is **rejected at the Zod boundary** (verified).
- **AISOTROPY COMPARISON:** aisotropy has a transactional outbox + async consolidation, but **no
  TS-native, schema-typed workflow DSL with suspend/resume** — this is a Mastra strength, full stop.

### First-class evals / scorers — **BUILT-IN, run-verified**
- **Symbol:** `createScorer` (`@mastra/core/evals`).
- **Executed:** a composable `preprocess → generateScore → generateReason` pipeline. A code-based
  keyword-coverage scorer (no LLM) scores a complete answer `1.00` and an off-topic answer `0.00`, each
  with a human-readable reason.
- **AISOTROPY COMPARISON:** aisotropy exposes product-eval metrics (P@k/recall) over its own stores but
  **no first-class, composable scorer abstraction** like Mastra's. Another Mastra win.

### Batteries-included memory DX — **BUILT-IN**
- Working memory + semantic recall + memory processors, all built-in and run-verified in
  `scripts/feature_parity_demo.ts` (`npm run parity`, features 1/3/4/8/12).

**The honest frame:** these are TS-native ergonomics aisotropy does not try to offer. The arena's one
narrow counter-point — isolation *by default* via an unforgeable `Scope{}` axis wired from
`context.Context` — is below, and it does **not** diminish the strengths above. Right tool, right job.

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in
- **Mechanism:** AI-SDK v5 native structured output with Zod schemas
- **Retry:** Automatic self-correction via AI-SDK's `output` parameter
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Gollem reflection loop. Both are fail-closed.
  Mastra's is AI-SDK-native; aisotropy's is homegrown but equally effective.

### Tool calling
- **Status:** Built-in
- **Mechanism:** Native tool definitions with TypeScript type inference
- **Integration:** First-class in the agent constructor
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  Mastra uses TS decorators; aisotropy uses Go generics.

### Streaming
- **Status:** Built-in
- **Mechanism:** AI-SDK streaming with SSE support
- **Cancellation:** Node.js `AbortController` propagates to LLM
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE + `ctx` cancellation. Both stream
  deltas; Mastra adds AI-SDK v5's richer streaming primitives.

### Working memory
- **Status:** Built-in
- **Mechanism:** `MemoryStorage` with `resourceId`/`threadId` scoping
- **Features:** Message history, memory processors (trimming before context window)
- **AISOTROPY COMPARISON:** Mastra's is thread-scoped with memory processors; aisotropy's is
  temperature-tiered (BadgerDB → SQLite+FTS5 → Qdrant → DataFusion). Both are built-in,
  but aisotropy's hierarchy is deeper.

### Semantic memory (vectors)
- **Status:** Built-in
- **Mechanism:** Vector recall with embedding integration
- **Features:** Automatic embedding, similarity search, memory processors
- **AISOTROPY COMPARISON:** Both have built-in vector memory. Mastra uses AI-SDK embeddings;
  aisotropy uses in-process cosine brute-force (active) + Qdrant gRPC (dormant).

## DIY / missing capabilities

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need PostgreSQL/SQLite with FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts journal out of the box.

### Cost governance
- **Status:** Partial — per-call usage is built-in; aggregation, dollars, and the gate are DIY
- **Mechanism (RUN-VERIFIED, `scripts/cost_obs_demo.ts`, `npm run cost-obs`, exit 0):**
  - Per-call token usage: **[BUILT-IN]** — `agent.generate()` returns
    `result.usage = {inputTokens, outputTokens, totalTokens}` on every call (verified across 4 turns).
  - Per-tenant aggregation: **[DIY]** — the framework returns per-call usage only; the tenant
    ledger (`Map<tenantId, totals>`) is hand-rolled in the demo.
  - Dollar cost: **[DIY]** — Mastra returns tokens, not `$`; the pricing table + math are ours.
  - Pre-spend budget gate: **[PARTIAL — corrected 2026-07-16, superseding the earlier "DIY / NOT
    built-in" claim]** — Mastra **does** ship a built-in pre-spend gate: `CostGuardProcessor`
    (`@mastra/core/processors`) runs on `processInputStep` *before each LLM call*, with
    `scope: 'run'|'resource'|'thread'`, a time `window`, and `strategy: 'block'|'warn'`. The
    comprehensive demo (`npm run parity`, feature 17) registers and runs it live (registered=true,
    ran=true). Caveat: it is **monetary-cost-based and explicitly "approximate"** (reads async
    observability metrics; requires observability storage), so a zero-cost fake model cannot *trip*
    it offline. There is no per-*tenant token-bucket 429* primitive; the earlier grep only searched
    `usageLimit|checkBeforeRequest` (0 hits) and missed `CostGuardProcessor`. The DIY hard
    token-bucket (ledger check before `generate()`, rejected turn Δ=0 model calls, CONFIRMED) is
    still what gives the deterministic 429-before-spend guarantee.
- **AISOTROPY COMPARISON:** aisotropy has a **built-in** pre-spend quota gate + per-tenant cost
  meter — rejects or degrades *before* the LLM call. Mastra gives you the raw usage number and
  leaves the tenant ledger, the dollar conversion, and the reject-before-spend gate to you.

### Observability
- **Status:** Built-in (native Mastra AI tracing) — tenant tagging is DIY; hosted UI is PAID
- **Mechanism (RUN-VERIFIED, `scripts/cost_obs_demo.ts`, exit 0):**
  - Native tracing/spans: **[BUILT-IN]** — `@mastra/observability` `Observability({...})` on
    `new Mastra()` emits `agent_run`, `model_generation`, `model_step`, `model_inference`,
    `model_chunk` spans automatically (25 spans / 5 traces captured in one run).
  - In-memory span capture: **[BUILT-IN]** — `TestExporter` collects + queries spans fully
    offline. A real OTLP/console exporter (`OtelExporter`/`ConsoleExporter`) is a one-line swap.
  - gen_ai usage on spans: **[BUILT-IN]** — the `model_generation` span carries
    `usage.inputTokens/outputTokens` (GenAI semconv), verified in the captured span.
  - tenant_id on spans: **[DIY]** — the metadata mechanism is built-in, but you must pass
    `tracingOptions.metadata: { tenantId }` yourself; it is not auto-derived (25/25 spans carried
    `metadata.tenantId` only because the demo stamps it).
  - Hosted trace UI: **[PAID]** — Mastra Cloud / Langfuse / Braintrust exporters are optional paid
    backends; not required for the offline in-memory path above.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai-semconv tracing plus `/costs` and `/health`
  endpoints by default, and stamps tenant onto spans from the `context.Context` boundary (no
  per-call opt-in). Mastra's tracing is genuinely built-in and semconv-aligned, but tenant tagging
  is a per-call responsibility, and the cost/health surface is DIY.

### Multi-tenant isolation
- **Status:** Convention (four documented levers; isolable by a competent dev)
- **Pattern:** `resourceId` + `threadId` (thread↔resource ownership-validated) + working-memory
  `scope: 'resource'|'thread'` + `semanticRecall.filter` (MongoDB-style metadata filter over the
  vector store, e.g. the docs' `filter: { projectId: { $eq: 'project-a' } }`).
- **AISOTROPY COMPARISON:** Mastra's scoping keys are authoritative (harder to get wrong than
  Pydantic AI or LangGraph), and a developer who applies the levers isolates correctly. The gap is
  convention vs construction: every lever is a per-call responsibility, and the default path (axis in
  no lever) silently does not partition — the class of omission behind `mastra#11856` (`listMessages`
  missing the `resourceId` filter). aisotropy's `Scope{}` value object (unexported fields, built from
  verified identity, auto-propagated via `context.Context`) makes a forgotten axis unrepresentable and
  filters it server-side on every read.

## Comprehensive feature-parity walk (RUN-VERIFIED, `npm run parity`, exit 0)

`scripts/feature_parity_demo.ts` exercises **25 executable features** against the real installed
framework, fully OFFLINE (fake `LanguageModelV2` + deterministic hash embedder; libsql temp-file
DBs). Each line is `[CLASS]` backed by executing the framework (or clearly running the DIY version we
had to write). Absence claims are grep-verified against `node_modules/@mastra/core/dist`. Counts:
**9 BUILT-IN · 5 PARTIAL · 9 DIY · 2 NOT-POSSIBLE**.

| # | Feature | Class | Evidence (executed) |
|---|---|---|---|
| 1 | working-memory (recent history, resource/thread) | **BUILT-IN** | `Memory({lastMessages:3})` recall returned the 3 newest msgs, dropped the oldest |
| 2 | facts/episodic KEYWORD (FTS5) tier | **NOT-POSSIBLE** | no full-text/facts memory tier in `@mastra/core`; closest is vector recall (F3) |
| 3 | semantic/VECTOR retrieval (RAG) | **BUILT-IN** | `semanticRecall` + `LibSQLVector` recalled a fact from a *different* thread (scope:resource) |
| 4 | summary/rolling buffer | **BUILT-IN** | `lastMessages` bounds the window + `summarizeConversation()` + `TokenLimiter` processor ship |
| 5 | importance×recency×relevance scoring | **DIY** | framework returns similarity-only; the composite score is ours |
| 6 | contradiction detection / supersede | **DIY** | no built-in supersede; DIY delete-old-vector kept only the newer fact |
| 7 | knowledge ingestion (chunk+embed doc) | **PARTIAL** | vector store + embed BUILT-IN; chunking DIY (`@mastra/rag` not installed) |
| 8 | BYO embedder | **BUILT-IN** | `Memory({embedder})` accepted + invoked our custom `EmbeddingModelV2` |
| 9 | **TIERED TEMPERATURE** (distinct tiers) | **PARTIAL** | 2 tiers of different nature (history + vector) + WM template, but ONE store + ONE vector; NO full-text, NO offline batch tier |
| 10 | fail-closed structured output | **BUILT-IN** | `structuredOutput{errorStrategy:'strict'}` parsed valid, THREW on malformed (not garbage-200). Bounded-N reflection loop = PARTIAL (single structuring-agent repair pass) |
| 11 | SSE streaming + immediate cancel | **BUILT-IN** | `agent.stream()` + `AbortController.abort()` propagated to the model producer |
| 12 | response cache (stateless, per-tenant) | **BUILT-IN** | `ResponseCache` processor: 2nd identical ask = HIT (model not called), other tenant = scope-isolated MISS. Exact-hash; cosine/semantic cache not built-in |
| 13 | scope-gated tools | **DIY** | Agent injects all tools you pass; DIY predicate hides a tool unless the scope is present |
| 14 | multi-step agent loop | **BUILT-IN** | `agent.generate({maxSteps})` executed the tool then produced a final answer over 2 steps |
| 15 | typed tools / MCP | **PARTIAL** | typed tools BUILT-IN (`createTool`+Zod); MCP = separate `@mastra/mcp`, not installed here |
| 16 | bearer-token → identity bind | **DIY** | no identity-binding middleware; DIY HMAC verify + must-agree bind (valid / path-mismatch / forged) |
| 17 | pre-spend quota gate | **PARTIAL** | **BUILT-IN `CostGuardProcessor`** runs pre-call (registered+ran), but cost-based/approximate — can't trip offline; DIY hard token-bucket gives the deterministic 429 (noSpend=true) |
| 18 | circuit breaker | **DIY** | no built-in breaker (grep=0); DIY breaker opened after 5 failures, 6th fast-failed |
| 19 | quota-pressure degradation | **DIY** | no built-in degrade seam; DIY pressure router →primary / →cheap at ≥95% |
| 20 | cost meter (per-tenant) | **PARTIAL** | per-call `usage` BUILT-IN; per-resource cost aggregation via observability `getMetricAggregate` BUILT-IN (needs pricing); `/costs` surface DIY |
| 21 | OTel spans (gen_ai usage) | **BUILT-IN** | `@mastra/observability` captured spans; `model_generation` carries usage. `tenant_id` on spans = DIY (per-call stamp) |
| 22 | health / readiness probe | **DIY** | no built-in health endpoint; DIY read-only probe of storage.listThreads + vector.listIndexes |
| 23 | product evaluation (P@k/recall) | **DIY** | metrics ours over the framework's REAL vector store (P@3, recall); `@mastra/evals` separate/uninstalled |
| 24 | transactional outbox | **NOT-POSSIBLE** | no atomic message+event write; per-domain `saveMessages` — a crash leaves them inconsistent |
| 25 | async consolidation (dedup/contradiction) | **DIY** | `summarizeConversation()` BUILT-IN, but dedup/contradiction reconciliation is ours (dropped 2 duplicate rows, 4→2) |

**Headline (memory tiers — the crux):** Mastra's tiered-memory story is **PARTIAL**, not the 4-tier
temperature hierarchy aisotropy claims. You get genuine BUILT-IN **recent-history** (SQL/KV) +
**vector recall** + an LLM **working-memory template** — but all through **one `Memory` over one
storage + one vector store**. There is **no keyword/FTS facts tier** (feature 2, NOT-POSSIBLE) and
**no offline batch/sleep tier** (feature 25 is DIY, feature 24 outbox is NOT-POSSIBLE). Governance
that aisotropy bakes in as constitutional gates — hard per-tenant token-bucket, circuit breaker,
quota-pressure degrade, identity-bind, health — is DIY here, though Mastra *does* ship a built-in
**approximate cost gate** (`CostGuardProcessor`) and a **per-tenant response cache** that the earlier
notes missed.

## Multi-PRODUCT isolation — the 3rd/4th axis (run-verified)

`scripts/product_isolation_demo.ts` (`npm run product-isolation`) — RUNS OFFLINE against
**real `@mastra/memory` + `LibSQLStore` + `LibSQLVector`**. Extends the tenant/user isolation story to
aisotropy's `product_id` axis. aisotropy keys every memory read/write on FOUR native
`context.Context` axes — `(tenant_id, user_id, session_id, product_id)`. Mastra names two of them
directly (`resourceId`, `threadId`) and exposes two more levers (working-memory `scope`,
`semanticRecall.filter`). There is **no primitive NAMED `product_id`** (grep for
`productId|product_id|productScope` over `@mastra/{core,memory}/dist` = **0 hits**, executed by the
demo), but product isolation is achievable by placing product on one of the four levers.

Executed evidence (exit 0):

- **(a) Positive control** — `productX` under `(tenantA, userA, session-42)` reads its own
  marker. ✓
- **(b) NAIVE / DEFAULT-CONVENTION path (no lever used)** — a request tags `product_id="productY"` as
  metadata while reusing the SAME `resourceId`+`threadId`, and still reads `productX`'s marker.
  `recall` keys on `resourceId`+`threadId`, so a product tag placed in **none** of the four levers is a
  scoping no-op. This is a **developer omission, not a framework leak** — steps (c)/(d) prevent the
  read. (The prior "CROSS-PRODUCT LEAK / never keys on it" framing was a strawman: reading back with
  the same keys trivially returns the same data.)
- **(c) COMPETENT path #1 — composite `resourceId`** — folding product into the `resourceId`
  (`tenantA:userA:productX`) is protected: Mastra binds a `threadId` to its owning `resourceId` and
  **THREW** `validateThreadIsOwnedByResource` ("Thread ... is for resource ...:productX but resource
  ...:productZZZ-typo was queried") on a cross-product read. The read is **prevented**.
- **(d) COMPETENT path #2 — `semanticRecall.filter`** — the docs' own metadata-filter lever
  (`filter: { projectId: { $eq: 'project-a' } }`). Filtering recall to `resource_id = productX` returns
  only `productX`'s row and not `productY`'s — the memory layer **does** key on the axis. Caveat: the
  default message-embedding path indexes a fixed vector-metadata set
  (`{message_id, thread_id, resource_id, role, content, created_at}` — verified here), so the filter
  scopes on the indexed `resource_id` (i.e. the composite key). To filter on a bespoke `productId`
  field you own the vector upsert (the RAG / knowledge-ingestion path) so it is actually indexed.
- **Honest caveat kept:** on the bare message-history path (`semanticRecall` off) the only extra
  per-call filter Mastra exposes is `dateRange` — there is no metadata filter there. Metadata/product
  scoping therefore lives on the semantic-recall path (lever 4) or the composite key (lever 1). Pick
  the right lever for the path you are on.

**Verdict:** `product_id` isolation in Mastra is **convention, not construction**. A competent
developer isolates product with any of the four real levers (run-verified in (c) and (d)); the naive
default path does not partition. The distinction from aisotropy is not "Mastra leaks" — it is that
none of Mastra's levers is a server-authoritative, unforgeable, auto-propagated axis: each is applied
per call, and a forgotten one silently fails open. aisotropy makes `product_id` a native `Scope{}`
field filtered server-side on every read, so a wrong or forgotten product reads nothing **by
construction**.

## Executable governance — dependency LICENSE posture (run-verified)

aisotropy's constitutional gate: **100% permissive (MIT/BSD/Apache-2.0/ISC), no copyleft/BUSL/SSPL/ELv2**. Ran the REAL
tool over this arena's actual installed tree:

```
npx license-checker --summary   →   MIT: 219 · Apache-2.0: 31 · ISC: 11 · BSD-3-Clause: 7
                                     BlueOak-1.0.0: 3 · BSD-2-Clause: 3 · (AFL-2.1 OR BSD-3-Clause): 1
                                     0BSD: 1 · UNLICENSED: 1        (exit 0)
```

`--json` classification over **277 packages**:

| Bucket | Count |
|---|---|
| Permissive (MIT/BSD/Apache/ISC/0BSD/BlueOak-1.0.0/AFL-or-BSD) | **276** |
| Copyleft (GPL/LGPL/AGPL/MPL/EPL) | **0** |
| Restricted / source-available (BUSL/SSPL/**ELv2**/Commons-Clause/FSL) | **0** |
| Unknown/unlicensed | **1** — `mastra-arena@0.0.1` (this arena's own `private:true` root, not a dependency) |

- **Explicit ELv2 check (task-requested):** grep of every package name+license for
  `Elastic|ELv2|BUSL|SSPL|Commons-Clause|source-available|FSL|Prosperity` = **0 hits**. The
  ELv2/source-available concern does **not** materialize in this arena's installed tree.
- **Mastra core packages** are all Apache-2.0, verified individually: `@mastra/core@1.51.0`,
  `@mastra/memory@1.23.0`, `@mastra/libsql@1.16.0`, `@mastra/observability@1.16.1`,
  `@mastra/schema-compat@1.3.4` — **Apache-2.0** each.
- `BlueOak-1.0.0` (3 pkgs) is a permissive, OSI-recognized model license — counted permissive.

**Verdict:** Mastra's real dependency tree **holds the permissive line** aisotropy's license gate
requires. Zero copyleft, zero source-available/BUSL/SSPL/ELv2. The only non-SPDX entry is the
arena's own private root package. This framework would **pass** aisotropy's constitutional
license gate as installed.

## Summary

Mastra is the **most batteries-included** of the three — memory, vectors, tools, streaming,
structured output, and (run-verified) **native OTel-semconv observability** all built-in. But it's
still a **framework**, not a runtime. Facts memory is NOT-POSSIBLE and most cost governance is DIY
(though a built-in approximate cost gate `CostGuardProcessor` + a per-tenant `ResponseCache` exist);
observability is built-in but tenant tagging is a per-call opt-in and the `/costs`/`/health` surface
is DIY. The
isolation model (`resourceId`/`threadId`) is
the closest any competitor comes to construction, but mastra#11856 proves it's still convention.

**vs. aisotropy:** Mastra wins on TS DX and built-in memory tiers; aisotropy wins on isolation-by-
construction, facts memory, cost governance, and sovereignty. The trade is: best TS experience
with partial built-ins vs. complete Go runtime with everything wired.
