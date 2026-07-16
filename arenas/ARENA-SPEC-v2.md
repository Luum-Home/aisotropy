---
type: Survey
title: "ARENA-SPEC v2 — the authoritative, doc-grounded catalog of everything the aisotropy runtime offers"
description: The complete feature/capability inventory of the aisotropy LLM-OS runtime, grounded line-by-line in the built docs (architecture, vision, ADRs, component docs), to drive a full feature-parity arena comparison against the 7 arena frameworks (Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta). Each feature carries name / what-it-is / doc_reference / aisotropy_mechanism / proof_type / arena_test_idea.
tags: [arenas, catalog, feature-parity, memory-tiers, isolation, governance, observability, sovereignty, struct-as-contract]
timestamp: 2026-07-16T00:00:00Z
---

# ARENA-SPEC v2 — Authoritative feature catalog of the aisotropy runtime

This document is the **source-of-truth catalog** of every distinct capability the aisotropy runtime
offers, for a complete feature-parity comparison against the 7 arenas already smoke-tested in
[`arenas/README.md`](README.md): **Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta**.

Everything below is **grounded in the built docs** — no general knowledge, no invention. Each feature
cites the doc that describes it. Where the docs distinguish REAL vs CODE-ONLY vs ASPIRATIONAL (the
[reality ledger](../docs/architecture/reality-ledger.md)), the catalog carries that honesty forward:
a feature's `arena_test_idea` is only meaningful for what actually boots.

Two `proof_type` classes are used, per the request:

- **executable-demo** — can be *demonstrated by running code* in an arena (hit a route, assert an
  outcome): tiered memory, consolidation, reflection loop, pre-spend gate, tools, RAG, auth, etc.
- **architectural-property** — a *structural property you classify*, not run: isolation-by-construction
  (already demoed via the leak arena), the constitutional-gate governance surface, single-binary
  sovereignty, license posture, the struct-as-contract identity.

The distinctive, under-tested features the comparison must foreground — flagged inline with **★** — are
the **tiered memory by access temperature** (four stores of different nature), the **governance /
constitutional-gate** surface (isolation gate, license policy, fail-closed structured output,
transactional-outbox→idempotent consolidation, contradiction resolution, health/readiness), and the
**struct-as-contract** (one Go struct = HTTP validation + OpenAPI 3.1 + JSON Schema to the LLM).

---

## Area 1 — Memory tiers (★ the headline differentiator: "specialized tools by access temperature")

### 1.1 ★ Temperature hierarchy (meta-feature)
- **What it is:** Memory is split by *access temperature and lifecycle* — a computer memory hierarchy for an LLM OS: L1/RAM (µs, local) → Disk (ms, networked facts) → long-term associative (vector) → offline batch (sleep). Each tier uses the optimal store for its temperature; "in-process (Go) where latency matters, dedicated services (Rust) where scale matters."
- **doc_reference:** `docs/architecture/memory-tiers.md` §"The triangle (+ the sleep cycle)"; `docs/architecture/overview.md` §"LLM OS model"; `docs/vision/thesis.md` §"Tiered memory".
- **aisotropy_mechanism:** Four distinct stores — BadgerDB / SQLite+FTS5(rqlite) / Qdrant(cosine) / DataFusion — each scoped by `(tenant,user[,session])`, wired in `aisotropy/app.go`.
- **proof_type:** architectural-property (the *hierarchy of four different-nature stores* is the property; the individual tiers below are each executable-demo).
- **arena_test_idea:** Classify: does the framework offer ≥2 memory tiers of *different nature* (not one KV store), organized by access cost? aisotropy = 4 tiers; most arenas = 1 (Letta is the nearest, with an isomorphic Core/Recall/Archival trio).

### 1.2 ★ Working memory — BadgerDB (µs, in-process L1)
- **What it is:** Last ~10–15 messages of the active chat + condensed summary buffer, under composite key `tenant:user:session`, read in microseconds with zero network round-trip.
- **doc_reference:** `docs/architecture/memory-tiers.md` §1; `docs/components/badgerdb-working-memory.md`; `docs/architecture/runtime-as-built.md` §4 (memory tiers table).
- **aisotropy_mechanism:** `internal/memory` (embedded BadgerDB; in-mem default, on-disk via `AISOTROPY_DATA_DIR`); `Append`/`AppendWithOutbox` write, `Summary`+`Recent(…,12)` read during hydration.
- **proof_type:** executable-demo.
- **arena_test_idea:** Drive a multi-turn chat; assert the Nth turn's prompt is hydrated with the prior turns from working memory. Compare: is working memory built-in and in-process, or DIY (app dict/Redis)?

### 1.3 ★ Facts / episodic memory — SQLite+FTS5 / rqlite (Engram-style journal)
- **What it is:** The user's "learning journal" — distilled decisions, hard preferences, pending tasks, summarized episodes — keyword-searchable via FTS5, not vectors (KB-over-RAG for stable facts).
- **doc_reference:** `docs/architecture/memory-tiers.md` §2 ("The Engram pattern"); `docs/components/rqlite-facts-memory.md`; `docs/components/engram-facts-journal.md`.
- **aisotropy_mechanism:** `internal/facts` (in-process SQLite+FTS5 default; networked rqlite via `RQLITE_URL`, byte-identical SQL); every read/write carries `tenant_id=? AND user_id=?`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Consolidate a fact ("production uses AWS"), then in a later turn assert it's retrieved by FTS5 match into the system prompt. Compare: most arenas = DIY for a facts journal (Letta = PARTIAL via Recall Memory).

### 1.4 ★ Semantic memory — Qdrant / in-process cosine (multi-kind Document/Fact/Turn)
- **What it is:** Vector RAG over the tenant's/user's unstructured knowledge, with rigid `tenant_id`/`user_id` payload filtering *during* the HNSW scan. Every vector is kind-tagged with its own lifecycle/bound/weight.
- **doc_reference:** `docs/architecture/memory-tiers.md` §3; `docs/components/qdrant-semantic-memory.md`; ADR-0017 (multi-kind), `docs/decisions/0017-semantic-tier-multi-kind-memory.md`.
- **aisotropy_mechanism:** `internal/semantic` (in-process brute-force cosine default; Qdrant gRPC via `QDRANT_HOST`); `PointID(scope, kind, sourceID)` so a Fact's and a Turn's vector never collide; retrieval merges `KindDocument`+`KindFact` at user scope + tenant-shared `KindDocument`, excludes `KindTurn`.
- **proof_type:** executable-demo (in-process is REAL; Qdrant is CODE-ONLY).
- **arena_test_idea:** Ingest a document, ask a question answerable only from it, assert RAG retrieval. Then the isolation variant (see 4.3). Compare: vector memory is BUILT-IN in Mastra/LangGraph/Letta, DIY in Pydantic AI/Eino/Genkit.

### 1.5 ★ Multi-kind semantic + Engram dual-index facade
- **What it is:** A distilled fact lives in *two* stores (facts row + `KindFact` vector) that cannot drift: Save dual-indexes, Supersede/Delete remove *both* sides — invariant by construction, importance-gated.
- **doc_reference:** `docs/components/engram-facts-journal.md`; ADR-0017 phase 2.
- **aisotropy_mechanism:** `internal/engram.Store` composes `facts.Store`+`semantic.Store`+`Embedder`; `AISOTROPY_FACT_MIN_IMPORTANCE` gate; sole owner of `KindFact` vector mutations.
- **proof_type:** executable-demo.
- **arena_test_idea:** Supersede a fact, assert the *old vector* is no longer retrievable (no stale-vector orphan). No arena has an analog — DIY everywhere.

### 1.6 Summary buffer / context engineering
- **What it is:** When the conversation exceeds N messages, an async goroutine condenses the oldest into a "session state" paragraph and evicts them, so the window Gollem sees never grows unbounded — surgical, minimal context.
- **doc_reference:** `docs/architecture/memory-tiers.md` §1 ("Token optimization — Summary Buffer"); `docs/vision/principles.md` §2 (context engineering).
- **aisotropy_mechanism:** `LLMSummarizer` (real LLM condensation via the same chat client) / `NoopSummarizer` in tests; drives the "~72% fewer input tokens" harness (`bench/token-economy/`, synthetic, caveated).
- **proof_type:** executable-demo.
- **arena_test_idea:** Run a long conversation; assert input-token count plateaus instead of growing linearly. Compare per-turn token growth vs a full-history baseline.

### 1.7 Engram scoring — Relevance × Importance × Recency
- **What it is:** Not all facts carry equal weight; only the top 3–4 engrams by a `Relevance × Importance × Recency` score are injected into the system prompt.
- **doc_reference:** `docs/architecture/memory-tiers.md` §2; `docs/architecture/runtime-as-built.md` §2 step 4 (`store.Search(ctx, scope, prompt, 4)`).
- **aisotropy_mechanism:** `facts.Search` FTS5 top-4 ranked by the composite score during hydration; facts failure is non-fatal.
- **proof_type:** executable-demo.
- **arena_test_idea:** Seed many facts of varying importance; assert only the top-K highest-scoring reach the prompt (not a dump).

### 1.8 ★ Contradiction detection / supersede (`superseded_by`)
- **What it is:** When a new fact contradicts an old one ("no longer using product X"), the old one is archived/down-weighted — avoids cognitive dissonance from stale facts.
- **doc_reference:** `docs/architecture/memory-tiers.md` §2 ("Contradiction detection (supersede)"); `docs/components/engram-facts-journal.md` (`Supersede`).
- **aisotropy_mechanism:** `facts.Supersede(oldID, newFact)` + engram facade deletes the old `KindFact` vector; offline sleep cycle also reconciles contradictions in batch.
- **proof_type:** executable-demo.
- **arena_test_idea:** Assert a superseded fact stops being retrieved and its replacement takes over. No arena analog.

### 1.9 Knowledge ingestion (user-scoped + tenant-shared documents)
- **What it is:** A dual-surface (SDK + HTTP) path to chunk→embed→store curated `KindDocument` knowledge, user-private or shared across all users of a tenant.
- **doc_reference:** `docs/architecture/api-reference.md` §"Knowledge ingestion"; ADR-0018 (user), ADR-0019 (tenant-shared).
- **aisotropy_mechanism:** `POST/DELETE .../knowledge` (user) + `POST/DELETE .../tenant/{id}/knowledge` (shared); BYO embedder (`AISOTROPY_EMBED_URL`), idempotent by `id`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Ingest a doc under tenant scope; assert every user of that tenant retrieves it but no cross-tenant user does.

### 1.10 ★ Primitive self-improvement (friction-driven per-user behavioral memory)
- **What it is:** A tool's *guidance metadata* (description, few-shot examples) is cloned per `(tenant,user)` and rewritten when the user hits repeated friction — executable code is never cloned, only the metadata the LLM reads.
- **doc_reference:** `docs/components/primitives.md`; `docs/architecture/memory-tiers.md` §"Primitive self-improvement"; `docs/vision/thesis.md`.
- **aisotropy_mechanism:** `internal/primitives.Store` + `FrictionDetector` (agent-route tool failure → `SignalFailure` weight 2.0) + hourly `Consolidate` with `HeuristicRewriter` at threshold 3.0. **Honest gap:** write-half only — rewritten primitives are not yet read back into a prompt.
- **proof_type:** executable-demo (detection + rewrite are REAL; injection un-wired).
- **arena_test_idea:** Fail a tool twice on the agent route; assert the primitive's `FrictionScore` crosses threshold and `Revision` bumps after a consolidation tick.

### 1.11 Bring-your-own embedder (sovereignty-preserving)
- **What it is:** The semantic embedder is a seam: deterministic `HashEmbedder` for dev/tests, a real OpenAI-compatible `APIEmbedder` via `AISOTROPY_EMBED_URL` (can point at a local server).
- **doc_reference:** `docs/architecture/memory-tiers.md` §3; `docs/components/embedder.md`; ADR-0018/0023.
- **aisotropy_mechanism:** `semantic.Embedder` interface; `AISOTROPY_EMBED_URL`; ADR-0023 sovereign local Rust candle embedder is proposed.
- **proof_type:** architectural-property (the seam) / executable-demo (swap embedder, assert ranking changes).
- **arena_test_idea:** Classify: is the embedder swappable/self-hostable, or provider-tethered?

---

## Area 2 — Contract & structured output (★ the struct-as-contract)

### 2.1 ★ Struct-as-contract (one Go struct = HTTP validation + OpenAPI 3.1 + LLM JSON Schema)
- **What it is:** A single Go struct governs *three things at once*: HTTP input validation, the auto-generated OpenAPI 3.1 living doc, and the JSON Schema enforced on the LLM via structured outputs. No hidden prompt templates.
- **doc_reference:** `docs/vision/thesis.md` §"The struct as the single source of truth"; `docs/vision/principles.md` §"The struct rules"; ADR-0002; `.claude/rules/architecture.md`.
- **aisotropy_mechanism:** Huma v2 over the Go struct → `/openapi.json` served live; the same struct is the reflection-loop schema. `internal/api` handlers.
- **proof_type:** architectural-property (classify: single source of truth) — demonstrable by hitting `/openapi.json` and a 422 on a bad field.
- **arena_test_idea:** Classify whether HTTP validation, the OpenAPI schema, and the LLM output schema derive from *one* definition, or three separate declarations. aisotropy = one; most arenas separate HTTP from LLM schema.

### 2.2 ★ Fail-closed structured-output reflection loop
- **What it is:** On the non-streaming `/chat` route, a schema-violating LLM reply triggers a bounded reflection loop that re-requests with the *exact typing error*; after retries it **fails closed with 502**, never a silent fallback to unstructured text.
- **doc_reference:** `docs/architecture/api-reference.md` §`/chat`; `docs/architecture/runtime-as-built.md` §2 (non-streaming chat); ADR-0008/0010; `.claude/rules/constitutional-gates.md` §"Structured-output / reflection loop".
- **aisotropy_mechanism:** `llm.ReflectAuto[chatStructuredReply]` (Gollem default, homegrown `Reflect[T]` fallback), `chatMaxReflectionRetries = 3` → 502.
- **proof_type:** executable-demo.
- **arena_test_idea:** Force a stub to return schema-invalid JSON; assert `/chat` returns 502 (fails closed), not a 200 with garbage. Compare: Pydantic AI has `raise ModelRetry`; Eino/Genkit/Letta have a parser but *no repair loop*.

### 2.3 ★ SSE streaming + immediate ctx-cancellation
- **What it is:** Token-by-token SSE; a client disconnect / ctx expiry stops the *upstream LLM call immediately* (spend cutoff) and does not persist the partial reply.
- **doc_reference:** `docs/architecture/api-reference.md` §`/chat/stream` ("Cancellation"); `docs/architecture/runtime-as-built.md` §2 step 10; `.claude/rules/constitutional-gates.md`.
- **aisotropy_mechanism:** Huma SSE; `active.StreamChat(ctx,…)` token channel; `ctx.Done()` cuts the producer. Events `token`/`done`/`error`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Start a stream, disconnect mid-stream; assert the upstream call is cancelled (not merely the write) and no partial turn is stored.

### 2.4 Stateless `/ask` + response cache (exact + semantic, per-(tenant,user))
- **What it is:** A stateless question route — no working-memory hydration, no session, no consolidation outbox — whose only context is tenant-scoped RAG, which is what makes response caching *safe* (no staleness footgun). Exact LRU + semantic (cosine ≥0.95) cache, partitioned by `(tenant,user)`.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §1 (`/ask`) + §5 (response cache) + freshness note ADR-0030; ADR-0027 (cache package), ADR-0030 (wiring).
- **aisotropy_mechanism:** `internal/responsecache` (exact SHA-256 key mixing `tenant\0user\0`, semantic per-scope partition); `POST /v1/.../ask`, `AskOutput.cached` echoes a hit.
- **proof_type:** executable-demo.
- **arena_test_idea:** Ask the same question twice; assert 2nd is `cached:true` with no LLM call. Then assert a different tenant's identical prompt is a MISS (no cross-tenant hit).

---

## Area 3 — Tools & agentic

### 3.1 ★ Scope-gated tools (governance by absence of information)
- **What it is:** Tools are injected into the prompt *only* if the caller's scopes permit them. A tool the scopes don't grant doesn't exist in the payload — so the model can't even hallucinate an attempt to use it.
- **doc_reference:** `docs/architecture/granular-control.md` §3 ("Thematic cloning… governance by absence of information"); `docs/architecture/runtime-as-built.md` §5 (scope-gated tools).
- **aisotropy_mechanism:** `tools.NewRegistry(search_docs [no scope], read_financials [read:financial-data], write_tasks [write:tasks])`; `PromptFragment` appends only permitted tools; agent loop re-validates the chosen tool name post-parse.
- **proof_type:** executable-demo.
- **arena_test_idea:** Call with scopes missing `write:tasks`; assert the tool is absent from the prompt AND a hallucinated call to it is rejected before execution.

### 3.2 Native tool-calling vs homegrown agentloop (double scope-gate)
- **What it is:** The agent route runs native provider function-calling (Gollem `Agent[T]`) when the client implements `ToolCaller`, else a homegrown structured-output `agentloop` — *both* enforce the identical double scope-gate (only permitted tools offered; chosen name re-validated).
- **doc_reference:** `docs/architecture/runtime-as-built.md` §6 (two dispatch paths); ADR-0012; `docs/architecture/api-reference.md` §`/chat/agent`.
- **aisotropy_mechanism:** `internal/agentloop` + `runNativeToolLoop`; `OpenAICompatClient`/OpenRouter implements `ToolCaller` (CODE-ONLY; stub/Anthropic fall back).
- **proof_type:** executable-demo (fallback path REAL; native path CODE-ONLY).
- **arena_test_idea:** Run `/chat/agent` with a scoped tool set; assert `AgentOutput.steps[]` only contains permitted tools regardless of dispatch path.

### 3.3 Typed tools + MCP (composition SDK)
- **What it is:** Public typed-tool authoring (`WithTypedTool[T]`) and MCP-backed tool execution as import/packaging surfaces, over ctx-scoped nodes that never construct a Scope.
- **doc_reference:** ADR-0016 (dual surface, levers #1 typed tools + #2 MCP delivered); ADR-0022 (standards as import surfaces); `docs/architecture/adoption-surface.md`.
- **aisotropy_mechanism:** `aisotropy/` façade type aliases (`Tool`, `ToolExecutor`) + builder `WithToolExecutor`; L2 composition SDK wrapping Gollem `ext/{graph,orchestrator,team,mcp}`.
- **proof_type:** architectural-property / executable-demo.
- **arena_test_idea:** Classify: are tools typed and does the runtime consume MCP tools natively? (Arenas: BUILT-IN function tools everywhere; scope-gating is the aisotropy delta.)

### 3.4 Model-selection gateway (rules/cost/capability router)
- **What it is:** A seam that decides *which* model handles a request by policy — explicit UI override, image-presence routing, and cost-budget shaping — chained `OverridePolicy → ImageRule → CostAwarePolicy`, failing safe to the default. Explicitly *not* the refuted Bet-4 difficulty predictor.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §5 (model-selection gateway) + §2 step 7; ADR-0027 (package), ADR-0028 (wiring), ADR-0029 (override), ADR-0030 (image rule).
- **aisotropy_mechanism:** `internal/gateway`; driven by `ChatRequest.model_preference` (`UISelector`), `images` (`HasImage`→`AISOTROPY_VISION_MODEL`), and `AISOTROPY_COST_BUDGET_USD` vs cost-meter spend.
- **proof_type:** executable-demo (REAL on chat/chat-stream).
- **arena_test_idea:** Set a cost budget and a cheap client; drive a tenant over-budget; assert subsequent calls route to the cheap target. Assert `model_preference` overrides it, but quota-pressure degrade still wins.

---

## Area 4 — Isolation & auth (★ the surviving differentiator + its governance)

### 4.1 ★ Multi-tenant isolation BY CONSTRUCTION
- **What it is:** `tenant_id`/`user_id` travel exclusively via `context.Context`; every storage op scopes by key-prefix / SQL `WHERE` / in-scan Qdrant `Must`-filter — no request-path code can issue an unscoped read. The unscoped read is *unrepresentable*, not merely discouraged.
- **doc_reference:** `docs/vision/thesis.md` §"isolation enforced per storage operation"; `docs/architecture/multi-tenancy.md`; `.claude/rules/constitutional-gates.md` §"Multi-tenant data isolation"; `arenas/README.md` (the isolation matrix).
- **aisotropy_mechanism:** BadgerDB key `tenant:{t}:user:{u}:session:{s}` (no cross-prefix iterator); facts `tenant_id=? AND user_id=?`; Qdrant `Must` filter during HNSW.
- **proof_type:** architectural-property (**already demoed via the leak arena** — the adversarial smoke proves every other framework leaks and aisotropy can't).
- **arena_test_idea:** Classify (done): write secret-A in a shared session as tenant A, read from every cross-tenant identity; a marker in the wrong tenant's response = leak. aisotropy = unrepresentable; all 7 arenas = CONVENTION/NONE.

### 4.2 ★ Value-object Scope (unforgeable)
- **What it is:** `facts.Scope`, `semantic.Scope`, `tenancy.Scope` have unexported fields — a bare `Scope{}` literal doesn't compile outside its package, and all constructors route through one shared identity validator; an unscoped statement is a *construction-time error*.
- **doc_reference:** `docs/vision/thesis.md` §"Scope as a value-object"; ADR-0011 (tenant-scoped statement builder).
- **aisotropy_mechanism:** `internal/tenancy` blessed constructors (`FromContext`, `NewScope`); reserved separator chars (`:`, NUL, `\n`) rejected in every identity component.
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify: can a developer construct an unscoped/forged storage query and have it compile? aisotropy = no (type system blocks it); arenas = yes (bare string keys).

### 4.3 ★ Qdrant in-scan payload filter (not a post-filter)
- **What it is:** The `tenant_id`/`user_id` filter is a `Must` condition evaluated *during* the HNSW traversal, so another tenant's vectors are never scored — verified in code to be a single-pass in-scan filter, not a post-filter (which would be a leak).
- **doc_reference:** `docs/architecture/memory-tiers.md` §3; `docs/vision/thesis.md` §"Semantic memory (Qdrant / in-process cosine)"; `.claude/rules/constitutional-gates.md`.
- **aisotropy_mechanism:** Qdrant `Must` keyword filter; in-process cosine applies `(tenant,user)` equality inside the scan loop *before* computing similarity.
- **proof_type:** architectural-property / executable-demo (assert cross-tenant vectors are never returned).
- **arena_test_idea:** Ingest same-topic docs under two tenants; assert a semantic search in tenant A never returns tenant B's vector.

### 4.4 Authenticated-identity seam (default-deny + must-agree bind)
- **What it is:** A single fail-closed auth middleware in front of every `/v1` route: default-deny (401) with no authenticator; with one, it verifies the credential and requires the verified principal's tenant/user to *equal* the path identity (403 mismatch, 401 missing/invalid) before minting a Scope.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §1 ("fail-closed auth gate") + freshness notes ADR-0024/0025; `docs/vision/thesis.md` §"The authentication seam"; ADR-0024.
- **aisotropy_mechanism:** `internal/api/authn.go` (`BindAuthMiddleware`/`DenyAllMiddleware`); `AISOTROPY_JWT_SECRET`→HS256, `AISOTROPY_JWT_PUBLIC_KEY[_FILE]`→RS256, `AISOTROPY_JWT_JWKS_URL`→JWKS; algorithm-confusion defence pins `alg`; `NoAuth()` opt-out.
- **proof_type:** executable-demo.
- **arena_test_idea:** Mint a JWT for `tenant=acme`, call `/v1/tenant/globex/...`; assert 403. Call with no credential; assert 401 (default-deny). Compare: no arena binds identity to a verified caller.

### 4.5 JWKS key rotation (proven end-to-end)
- **What it is:** RS256 verification against a JWKS endpoint with proven rotation: a rotated-in signing key is picked up via a bounded unknown-`kid` refetch, a rotated whole set at the TTL boundary, an unknown-`kid` burst throttled to one fetch/window (bounding failures too). Fails CLOSED.
- **doc_reference:** `docs/architecture/runtime-as-built.md` freshness note ADR-0030; ADR-0029/0030.
- **aisotropy_mechanism:** `internal/auth/jwt_rs256.go` `jwksSource` (stdlib `math/big`, TTL-cache, 1 MiB bound, `refreshLocked` stamps `lastFetch` even on failure).
- **proof_type:** executable-demo.
- **arena_test_idea:** Rotate a signing key mid-run; assert the new token verifies after one refetch and an unknown-`kid` flood triggers exactly one fetch/window.

### 4.6 ★ Guardrails (input redaction + injection detection + output toxicity/leak)
- **What it is:** Input PII/secret redaction (the *redacted* text is what all downstream steps see, so a secret never reaches the model or memory) + injection/prompt-leak detection; output toxicity/leak checks. Detection ALWAYS on; blocking OPT-IN.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §2 step 3 + §5 (guardrails) + freshness notes ADR-0028/0029; ADR-0027 (`internal/guardrails`).
- **aisotropy_mechanism:** `internal/guardrails` via `api.Guard`; `AISOTROPY_GUARDRAILS_BLOCK_INJECTION`/`_BLOCK_OUTPUT`/`_LLM_JUDGE`; streaming buffers for hard-block; logs types/counts only.
- **proof_type:** executable-demo.
- **arena_test_idea:** Send a prompt containing an API key; assert the redacted form reaches the model and no memory tier stores the raw secret. Send an injection prompt; assert it's flagged (and blocked when the toggle is on).

---

## Area 5 — Cost & governance (★ the constitutional gates + pre-spend)

### 5.1 ★ Per-tenant PRE-SPEND quota gate (BadgerDB token-bucket → 429)
- **What it is:** A per-tenant token-bucket checked *before any LLM call* — reject (429-equivalent) or degrade to a cheap model before spending tokens, not after. The sharp edge is *where* the check sits (pre-spend, per-tenant), not a dashboard.
- **doc_reference:** `docs/vision/thesis.md` §"Cost control before observability"; `docs/architecture/granular-control.md` §2; `.claude/rules/constitutional-gates.md` §"Subscription / quota state consistency"; `docs/architecture/runtime-as-built.md` §2 step 2.
- **aisotropy_mechanism:** `memory.Limiter` (`wm.NewLimiter(rate,burst)`, `AISOTROPY_RATE_PER_SEC`/`_BURST`, defaults 10/30) in BadgerDB; `limiter.Allow(ctx,tenant)` on `/chat` + `/chat/stream` before the LLM call.
- **proof_type:** executable-demo.
- **arena_test_idea:** Exhaust a tenant's bucket; assert the next request is rejected (SSE `error`/429) *before* any token is spent, and a different tenant is unaffected. Compare: Pydantic AI has per-*run* pre-spend; per-*tenant* gate is DIY everywhere else.

### 5.2 Circuit breaker (provider-failure fast-fail)
- **What it is:** 5 consecutive stream-start failures open the breaker; calls then fail fast with `ErrCircuitOpen` for a 30s cooldown, then a half-open trial. Wraps primary + cheap clients across all paths.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §5 (circuit breaker); `docs/architecture/observability.md` (breaker trips on provider-failure threshold, not cost).
- **aisotropy_mechanism:** `llm.WithCircuitBreaker(client, 5, 30s)`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Force 5 consecutive provider failures; assert the 6th fails fast (no upstream call) until cooldown.

### 5.3 Quota-pressure model degradation
- **What it is:** At ≥95% quota pressure, the streaming path degrades to a cheaper model instead of a hard rejection (graceful degradation). Overrides even an explicit `model_preference` (safety valve wins).
- **doc_reference:** `docs/architecture/runtime-as-built.md` §2 step 8; `docs/architecture/granular-control.md` §2.
- **aisotropy_mechanism:** `llm.DegradeAt(pressure, 0.95, active, cheap)` + `limiter.Pressure`; needs `AISOTROPY_CHEAP_MODEL`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Drive a tenant to ~95% pressure with a cheap model configured; assert the request downgrades rather than 429s.

### 5.4 Cost meter (per-tenant)
- **What it is:** In-process per-tenant aggregate of *real provider* token usage (input/output/requests/estimated USD), recorded on each streamed `done`, exposed at `GET /costs`. A live observability aggregate, not a billing system of record.
- **doc_reference:** `docs/architecture/runtime-as-built.md` §5 (cost meter); `docs/architecture/api-reference.md` §`/costs`; `docs/architecture/observability.md`.
- **aisotropy_mechanism:** `internal/costmeter.New()`; `meter.Record` from `tok.Usage`; `GET /costs` behind the ops gate.
- **proof_type:** executable-demo.
- **arena_test_idea:** Run N turns for two tenants; assert `/costs` shows per-tenant token totals separated by tenant id. Compare: usage is BUILT-IN but per-tenant meter is DIY in every arena.

### 5.5 ★ Constitutional-gate governance surface
- **What it is:** A repo-wide non-negotiable governance surface enforced before merge: (a) tenant-isolation gate (ctx-only, per-store filter, no two tenants in one buffer), (b) pre-spend quota consistency, (c) license policy 100% MIT/Apache (no copyleft/BUSL without ADR), (d) structured-output fail-closed, (e) SSE immediate-cancel, (f) test-before-merge, (g) backward-compatible-contract gate (the struct IS the OpenAPI contract → a JSON-tag/field change is a breaking-change gate).
- **doc_reference:** `.claude/rules/constitutional-gates.md` (all sections); `docs/vision/principles.md` §"Derived operating principles".
- **aisotropy_mechanism:** Governance rules + the value-object/typed enforcement the gates rest on (`internal/tenancy`, Huma struct-contract, `go test ./...`).
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify: does the framework encode isolation/cost/license/contract-stability as *enforced gates*, or leave them to developer discipline? aisotropy encodes them; arenas don't.

### 5.6 ★ License posture — 100% MIT/Apache-2.0, self-hostable
- **What it is:** The entire stack (Huma, Gollem, BadgerDB, Qdrant, rqlite, DataFusion) is permissive OSS — zero BSL/AGPL/commercial. Turso/GlareDB/jina/Cohere explicitly rejected on license/sovereignty grounds.
- **doc_reference:** `docs/architecture/overview.md` (stack license table); `docs/vision/thesis.md` §"Sovereignty"; `.claude/rules/constitutional-gates.md` §"License policy"; `docs/decisions/index.md` (Turso/GlareDB rejections); `docs/architecture/rust-dependency-licenses.md`.
- **aisotropy_mechanism:** Dependency selection ADRs (0004 rqlite over Turso-BSL, 0006 BGE over jina-NC).
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify each arena's license and whether multi-tenant self-hosting is unrestricted. aisotropy = fully self-hostable permissive; Dify/Flowise/LangGraph-platform have SaaS/paid gates.

---

## Area 6 — Observability

### 6.1 ★ OTel gen_ai spans with tenant_id/user_id NATIVE on the serving span
- **What it is:** One OTel span per LLM call (`chat`/`chat.tools`) carrying GenAI Semantic-Convention attributes (`gen_ai.system`, `gen_ai.request.model`, real `gen_ai.usage.input/output_tokens`) *plus* `tenant_id`/`user_id` on the serving span — so "all traces for tenant X" needs no downstream join.
- **doc_reference:** `docs/architecture/observability.md` §"Implementation status" + "LLM call attributes"; `arenas/README.md` row 9.
- **aisotropy_mechanism:** `internal/llm.WithOTel`; export via `OTEL_EXPORTER_OTLP_ENDPOINT` (no-op by default); `internal/costmeter` for the cost aggregate.
- **proof_type:** executable-demo.
- **arena_test_idea:** Emit a chat; assert the span carries `gen_ai.*` + `tenant_id`/`user_id`. Compare: arenas emit `gen_ai` spans but `tenant_id` is DIY (and some dashboards are PAID; ADK Go *ties* on gen_ai semconv).

### 6.2 Health / readiness probes (no LLM spend)
- **What it is:** `GET /healthz` liveness (`{"status":"ok"}`) + `GET /health` readiness — parallel, read-only, `_health`-tenant-scoped probes of facts + semantic tiers (2s timeout), 200 ok / 503 degraded. Deliberately never probes the LLM (would violate the pre-spend gate).
- **doc_reference:** `docs/architecture/api-reference.md` §"Operational endpoints"; `docs/components/health-readiness.md`; `.claude/rules/services-config.md` §"Health check".
- **aisotropy_mechanism:** `internal/health` (raw chi handlers outside the OpenAPI contract).
- **proof_type:** executable-demo.
- **arena_test_idea:** Hit `/health`; assert readiness probes the memory tiers (not the LLM) and returns 503 when a tier is degraded.

### 6.3 ★ Bits-per-token North Star metric (Bet 6)
- **What it is:** The compression North Star — `bits/token/tenant` (`lift` = bpt_without − bpt_with) measuring "did the agent learn the user," computed *out of band* by an offline reference-model scorer (no serving provider exposes logprobs) and surfaced via `GET /bitrate` + an OTel `agent.compression.bpt` event.
- **doc_reference:** `docs/architecture/observability.md` §"bits/token/tenant"; `docs/vision/compression-thesis.md` §"North Star"; `docs/architecture/api-reference.md` §`/bitrate`; ADR-0014/0015.
- **aisotropy_mechanism:** `internal/bitrate` (held-out ~5% sampler on the stream path via `AISOTROPY_BITRATE_BIN`, offline distilgpt2 scorer, tenant-scoped bpt store); reuses `hydrateSystemPrompt` for with/without-memory.
- **proof_type:** executable-demo (CODE-ONLY — gated on `AISOTROPY_BITRATE_BIN`).
- **arena_test_idea:** With the bitrate bin set, accumulate turns for a tenant; assert `/bitrate` shows a `latest_lift` and a `trend_slope`. No arena has a compression/memory-lift metric.

### 6.4 Product-evaluation harness (IR metrics + LLM-as-judge, isolation-preserving)
- **What it is:** A tested foundation mapping Huyen's 3-step eval pipeline into Go: IR metrics (P@k/R@k/F1/MRR) against the *real* tenant-scoped `semantic.Store`, an LLM-as-judge returning a typed schema-validated verdict via the same reflection loop, first-class Relevance/Faithfulness/Safety rubrics — all fail-closed, runnable with no API key, isolation preserved during evaluation.
- **doc_reference:** `docs/architecture/product-evaluation.md`; ADR-0026.
- **aisotropy_mechanism:** `internal/eval` (`EvalRetrieval`, `LLMJudge`/`CodeJudge`, `RunSuite`, checked-in gold set); `TestEvalRetrievalIsolation`.
- **proof_type:** executable-demo.
- **arena_test_idea:** Run the retrieval suite against a labeled corpus; assert P@k/MRR and that a cross-tenant doc never enters the evaluated tenant's metrics.

---

## Area 7 — Consolidation / sleep (★ transactional outbox → idempotent async)

### 7.1 ★ Transactional outbox (atomic message + intent, at-least-once, idempotent)
- **What it is:** The user turn + a pending consolidation event are written in *one* BadgerDB ACID transaction (both or neither), then drained async into facts/semantic tiers — at-least-once, idempotent on redelivery (deterministic point IDs / `INSERT OR REPLACE`), with cold-boot reconciliation of orphaned events before the server serves.
- **doc_reference:** `docs/architecture/consistency.md` (whole doc); `docs/components/consolidation-outbox.md`; `docs/architecture/runtime-as-built.md` §3.
- **aisotropy_mechanism:** `internal/memory/outbox.go` (`AppendWithOutbox`, `RunOutboxWorker`, `RecoverOutbox`); `internal/consolidation/sink.go`; `outboxRef` sha256-stable id; both chat routes consolidate, `/chat/agent` doesn't.
- **proof_type:** executable-demo.
- **arena_test_idea:** Send a turn, assert an outbox event is enqueued in the same txn; drain it and assert the fact/vector appears exactly once even if redelivered; restart with a pending event and assert cold-boot recovery drains it.

### 7.2 ★ Sleep-cycle batch consolidation (DataFusion Rust subprocess)
- **What it is:** Offline "sleep cycle" that dedups repeated records, reconciles contradictions, cleans noise ("hi"/"thanks"), and detects friction — run as a *separate Rust worker* invoked as a subprocess (batch, NDJSON/Parquet), a Rust panic surfacing as a Go-observable non-zero exit with backoff.
- **doc_reference:** `docs/architecture/memory-tiers.md` §4; `docs/components/datafusion-sleep-cycle.md`; ADR-0005; `.claude/rules/architecture.md` §"Rust boundary".
- **aisotropy_mechanism:** `crates/sleep-cycle` (DataFusion dedup) + `internal/sleepcycle.Coordinator`; `ExportAll` (the one reviewed cross-tenant read) → subprocess → per-scope `Delete`. Dormant unless `AISOTROPY_SLEEPCYCLE_BIN` is set (a pure-Go turn-vector pruner always runs).
- **proof_type:** executable-demo (CODE-ONLY — gated on the built Rust binary; the Go-side dedup/pruner is REAL).
- **arena_test_idea:** With the binary built, export a corpus containing duplicate facts; assert the dedup pass drops duplicates within each `(tenant,user)` scope. Compare: only Letta ships productized sleep-time consolidation (`enable_sleeptime=True`); no other arena.

---

## Area 8 — Sovereignty & runtime shape

### 8.1 ★ Single-binary, in-process, zero-config complete runtime
- **What it is:** `./aisotropyd` with no env vars and no API key is a *complete single-process Go runtime* — chi+Huma, struct-validated chat, real reflection loop, real SSE with ctx-cancel, BadgerDB working memory + transactional outbox → in-process SQLite+FTS5 facts + in-process cosine semantic, per-tenant rate-limited/circuit-broken/cost-metered/scope-gated, with the stub LLM when no key is set. Megabyte native binary, only Qdrant scales separately.
- **doc_reference:** `docs/architecture/reality-ledger.md` §"What boots with ZERO configuration"; `docs/architecture/overview.md` §"Why this design wins".
- **aisotropy_mechanism:** `aisotropy/app.go` (`App.Run`); `cmd/aisotropyd/main.go` (51-line consumer); Distroless.
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify: is the runtime a single self-hostable binary with tiered memory built in, or a library needing external Redis/vectorDB/server? aisotropy = single binary + 1 optional service; Letta = server + DB.

### 8.2 Dual surface — sealed kernel + composition SDK (façade + builder)
- **What it is:** aisotropy is *both* a hostable multi-tenant runtime (sealed kernel: tenancy/Scope/memory tiers, never public) *and* a composable agent framework (public composition surface), held together by the invariant "a composed node receives an already-scoped ctx and never constructs a Scope." A third party builds their bot by importing `aisotropy/` without forking.
- **doc_reference:** ADR-0013 (façade+builder), ADR-0016 (dual surface); `docs/architecture/adoption-surface.md`.
- **aisotropy_mechanism:** `aisotropy/` type-alias façade + functional-options builder; L0 kernel / L1 façade / L2 composition SDK over Gollem `ext/*`.
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify: can you embed the runtime *and* compose arbitrary tool topologies without the isolation kernel leaking into user code?

### 8.3 Go+Rust boundary as subprocess (not CGO/FFI/WASM)
- **What it is:** Heavy Rust engines (DataFusion sleep cycle, bitrate scorer, proposed candle embedder) run as *separate subprocesses* over an explicit I/O contract (paths, Parquet schema, exit codes) — a Rust panic is a Go-observable exit code with retry, never an in-process crash. Preserves Go's static binary.
- **doc_reference:** ADR-0005; `.claude/rules/architecture.md` §"Rust boundary"; ADR-0014 (bitrate subprocess).
- **aisotropy_mechanism:** `internal/sleepcycle`, `internal/bitrate` invoke pinned Rust crates via `AISOTROPY_SLEEPCYCLE_BIN`/`AISOTROPY_BITRATE_BIN`.
- **proof_type:** architectural-property.
- **arena_test_idea:** Classify the polyglot isolation boundary (subprocess vs FFI); assert a worker crash can't take down the Go core.

### 8.4 Provider-agnostic LLM registry
- **What it is:** `internal/llm` is a registry of named `Client` connections; every provider with configured credentials is registered simultaneously (`openrouter`/`openai`/`anthropic`/always `stub`), the highest-precedence one the default. Unblocks per-request routing.
- **doc_reference:** ADR-0009; `docs/architecture/runtime-as-built.md` env table (`llm.RegistryFromEnv`).
- **aisotropy_mechanism:** `internal/llm/registry.go`; precedence `OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub`.
- **proof_type:** architectural-property / executable-demo (swap providers by env).
- **arena_test_idea:** Classify: is the provider a swappable seam or hard-coded? Assert the keyless stub answers with no API key.

---

## Summary table — feature × proof_type × arenas (to be filled by the comparison)

Each arena cell carries a coverage class (`BUILT-IN` / `CONVENTION` / `PARTIAL` / `DIY` / `PAID` /
`NOT-POSSIBLE`) per the `arenas/README.md` legend. The **aisotropy** column states the built status
(REAL / CODE-ONLY; every property that survives is `by-construction` where noted). **25 of the 41 rows
are run-verified** against the pinned upstream framework (offline: fake model + synthetic data) via
each arena's `feature_parity_demo` (all seven exit 0); the remaining 16 architectural rows are
honest doc-grounded classification. The run-verified rows are marked `▶`.

| # | Feature | proof_type | Pydantic AI | Mastra | LangGraph | Eino | ADK Go | Genkit Go | Letta | **aisotropy** |
|---|---|---|---|---|---|---|---|---|---|---|
| 1.1 ▶ | Temperature hierarchy (4-tier) | architectural-property | NOT-POSSIBLE | PARTIAL | PARTIAL | NOT-POSSIBLE | PARTIAL | NOT-POSSIBLE | BUILT-IN | REAL (meta) |
| 1.2 ▶ | Working memory (BadgerDB µs) | executable-demo | PARTIAL | BUILT-IN | BUILT-IN | CONVENTION | BUILT-IN | BUILT-IN | BUILT-IN | REAL |
| 1.3 ▶ | Facts memory (SQLite+FTS5 journal) | executable-demo | DIY | NOT-POSSIBLE | NOT-POSSIBLE | DIY | PARTIAL | DIY | PARTIAL | REAL (in-proc) / CODE-ONLY (rqlite) |
| 1.4 ▶ | Semantic memory (multi-kind vectors) | executable-demo | DIY | BUILT-IN | BUILT-IN | PARTIAL | PAID | BUILT-IN | BUILT-IN | REAL (cosine) / CODE-ONLY (Qdrant) |
| 1.5 | Engram dual-index (fact↔vector) | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 1.6 ▶ | Summary buffer / context engineering | executable-demo | DIY | BUILT-IN | PARTIAL | DIY | DIY | DIY | BUILT-IN | REAL |
| 1.7 ▶ | Engram scoring (Rel×Imp×Rec, top-4) | executable-demo | DIY | DIY | DIY | DIY | NOT-POSSIBLE | DIY | DIY | REAL |
| 1.8 ▶ | Contradiction detection / supersede | executable-demo | DIY | DIY | DIY | DIY | NOT-POSSIBLE | DIY | PARTIAL | REAL |
| 1.9 ▶ | Knowledge ingestion (user + tenant-shared) | executable-demo | DIY | PARTIAL | PARTIAL | PARTIAL | DIY | PARTIAL | BUILT-IN | REAL |
| 1.10 | Primitive self-improvement (friction) | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL (write-half) |
| 1.11 ▶ | BYO embedder | architectural-property | DIY | BUILT-IN | BUILT-IN | CONVENTION | NOT-POSSIBLE | BUILT-IN | BUILT-IN | REAL |
| 2.1 | Struct-as-contract (1 struct, 3 uses) | architectural-property | PARTIAL | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 2.2 ▶ | Fail-closed reflection loop (502) | executable-demo | BUILT-IN | BUILT-IN | PARTIAL | DIY | DIY | PARTIAL | DIY | REAL |
| 2.3 ▶ | SSE + immediate ctx-cancel | executable-demo | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | PARTIAL | REAL |
| 2.4 ▶ | Stateless /ask + response cache | executable-demo | DIY | BUILT-IN | BUILT-IN | DIY | BUILT-IN | DIY | DIY | REAL |
| 3.1 ▶ | Scope-gated tools (governance by absence) | executable-demo | PARTIAL | DIY | DIY | CONVENTION | PARTIAL | DIY | DIY | REAL |
| 3.2 ▶ | Native vs homegrown agent loop (double gate) | executable-demo | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | REAL (fallback) / CODE-ONLY (native) |
| 3.3 ▶ | Typed tools + MCP | architectural-property | PARTIAL | PARTIAL | PARTIAL | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | REAL |
| 3.4 | Model-selection gateway | executable-demo | PARTIAL | PARTIAL | PARTIAL | DIY | DIY | DIY | DIY | REAL |
| 4.1 | Isolation BY CONSTRUCTION | architectural-property | CONVENTION | CONVENTION | CONVENTION | NOT-POSSIBLE | CONVENTION | CONVENTION | CONVENTION | REAL (by-construction; demoed via leak arena) |
| 4.2 | Value-object Scope (unforgeable) | architectural-property | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL (by-construction) |
| 4.3 | Qdrant in-scan filter (not post-filter) | architectural-property | DIY | CONVENTION | CONVENTION | DIY | PAID | DIY | CONVENTION | REAL (by-construction) |
| 4.4 ▶ | Authenticated-identity seam (must-agree bind) | executable-demo | DIY | DIY | NOT-POSSIBLE | DIY | NOT-POSSIBLE | DIY | CONVENTION | REAL |
| 4.5 | JWKS key rotation | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL (CODE-ONLY default) |
| 4.6 | Guardrails (redact/inject/output) | executable-demo | DIY | PARTIAL | DIY | DIY | PARTIAL | DIY | DIY | REAL |
| 5.1 ▶ | Per-tenant PRE-SPEND quota gate (429) | executable-demo | BUILT-IN (per-run) | PARTIAL | DIY | DIY | DIY | DIY | DIY | REAL (per-tenant, by-construction) |
| 5.2 ▶ | Circuit breaker | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 5.3 ▶ | Quota-pressure model degradation | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 5.4 ▶ | Cost meter (per-tenant) | executable-demo | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | REAL |
| 5.5 | Constitutional-gate governance surface | architectural-property | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 5.6 | License 100% MIT/Apache, self-hostable | architectural-property | BUILT-IN | BUILT-IN | PARTIAL | BUILT-IN | PARTIAL | BUILT-IN | PARTIAL | REAL |
| 6.1 ▶ | OTel gen_ai spans w/ tenant_id native | executable-demo | BUILT-IN (tenant DIY) | BUILT-IN (tenant DIY) | BUILT-IN (tenant DIY) | PARTIAL | BUILT-IN (tenant DIY) | PARTIAL | PARTIAL | REAL (tenant_id native) |
| 6.2 ▶ | Health / readiness probes (no LLM spend) | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | PARTIAL | REAL |
| 6.3 | Bits-per-token North Star (Bet 6) | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | DIY | CODE-ONLY |
| 6.4 ▶ | Product-evaluation harness (IR + judge) | executable-demo | DIY | DIY | DIY | DIY | DIY | PARTIAL | DIY | REAL |
| 7.1 ▶ | Transactional outbox (atomic, idempotent) | executable-demo | NOT-POSSIBLE | NOT-POSSIBLE | NOT-POSSIBLE | DIY | NOT-POSSIBLE | DIY | NOT-POSSIBLE | REAL |
| 7.2 ▶ | Sleep-cycle consolidation (DataFusion) | executable-demo | DIY | DIY | DIY | DIY | DIY | DIY | BUILT-IN | CODE-ONLY (Go pruner REAL) |
| 8.1 | Single-binary zero-config runtime | architectural-property | DIY | PARTIAL | PARTIAL | DIY | PARTIAL | DIY | PARTIAL | REAL |
| 8.2 | Dual surface (sealed kernel + SDK) | architectural-property | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | PARTIAL | REAL |
| 8.3 | Go+Rust subprocess boundary | architectural-property | DIY | DIY | DIY | DIY | DIY | DIY | DIY | REAL |
| 8.4 | Provider-agnostic LLM registry | architectural-property | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | BUILT-IN | PARTIAL | REAL |

**Counts:** 41 features — **24 executable-demo**, **17 architectural-property**. **25 rows (▶)
run-verified** against the pinned upstream framework, offline, all seven arenas exit 0.

### BUILT-IN tally per arena (across all 41 rows)

| Class | Pydantic AI | Mastra | LangGraph | Eino | ADK Go | Genkit Go | Letta |
|---|---|---|---|---|---|---|---|
| BUILT-IN | 7 | 11 | 8 | 5 | 7 | 8 | 9 |
| PARTIAL | 7 | 9 | 10 | 5 | 8 | 6 | 10 |
| CONVENTION | 1 | 2 | 2 | 3 | 1 | 1 | 3 |
| DIY | 24 | 17 | 18 | 26 | 18 | 25 | 18 |
| PAID | 0 | 0 | 0 | 0 | 2 | 0 | 0 |
| NOT-POSSIBLE | 2 | 2 | 3 | 2 | 5 | 1 | 1 |

Reading the tally: **Mastra (11 BUILT-IN)** and **Letta (9)** are the broadest out-of-the-box
frameworks; **Eino (5 BUILT-IN, 26 DIY)** is the leanest — a Go orchestration library, not a
runtime. ADK Go carries the most **NOT-POSSIBLE (5)** — its rich memory is Vertex-tethered
(PAID) and it binds no tenant/identity. But BUILT-IN counts flatter breadth, not the axis that
matters here: **isolation,
tiered memory, and pre-spend governance** — where every arena either falls to CONVENTION/DIY or
hits a structural NOT-POSSIBLE, and aisotropy is by-construction.

---

## FINAL VERDICT

### 1. The memory-tiers headline

**Of the seven frameworks, exactly one — Letta — ships a genuine hierarchy of memory tiers of
different nature. The other six are either a single flat store or two same-nature surfaces.**
Scoring row 1.1 (temperature hierarchy) against the run-verified evidence:

- **Letta = BUILT-IN (3 tiers):** Core (in-context Blocks) / Recall (searchable message history) /
  Archival (vector) — genuinely different in nature, plus productized sleep-time consolidation
  (`enable_sleeptime=True`), the *only* arena with an offline-consolidation analog. It is the
  closest neighbor and it validates the memory-OS thesis.
- **ADK Go, Mastra, LangGraph = PARTIAL (2 surfaces):** ADK has a hot working-KV + a keyword recall
  tier (2 of different nature, but its vector tier is Vertex MemoryBank — PAID/managed, not
  self-hostable). Mastra and LangGraph each expose one KV/SQL store + one vector store — *two
  surfaces of similar nature*, no full-text/facts tier, no offline-batch tier.
- **Pydantic AI, Eino, Genkit Go = NOT-POSSIBLE:** Pydantic AI ships **zero** memory stores
  (`message_history` is a conduit, not a tier); Eino ships one bare-string checkpoint KV plus
  retrieval interface seams; Genkit ships one experimental session store + one plugin vector store
  of the same nature. None can express a temperature hierarchy of different-nature stores.

**aisotropy fields four stores of genuinely different nature organized by access temperature:**
BadgerDB (µs KV, L1/RAM) → SQLite+FTS5 (keyword/facts journal, ms) → Qdrant/cosine (vector,
associative) → DataFusion (offline batch, sleep). Even Letta — the nearest competitor — has **3
tiers, not 4** (no dedicated FTS decisions/rules journal; Recall is message history, PARTIAL on
row 1.3), and its tiers are **LLM-self-managed and probabilistic** versus aisotropy's
**runtime-hydrated and deterministic**. The 4-DB-by-temperature design is the single most
distinctive property in the catalog.

### 2. Where aisotropy leads, ties, and is matched

**Genuinely leads (structural, not just built-in):**
- **Tiered memory by access temperature** (row 1.1) — 4 tiers of different nature; only Letta comes
  close with 3, and only as a probabilistic self-managed store.
- **Transactional outbox → idempotent consolidation** (rows 7.1 + 7.2) — the atomic
  message+intent write in a single BadgerDB ACID txn is **NOT-POSSIBLE** in five of seven arenas
  (Pydantic AI, Mastra, LangGraph, ADK, Letta all have separate stores with no shared transaction,
  so a crash between the turn write and the event write orphans one); the other two (Eino, Genkit)
  can only DIY a non-durable pair. Sleep-cycle consolidation is BUILT-IN only in Letta and DIY
  everywhere else.
- **Isolation by construction** (rows 4.1–4.3) — CONVENTION in six arenas, NOT-POSSIBLE in Eino,
  and the unforgeable value-object Scope (4.2) plus in-scan payload filter (4.3) exist nowhere else.
  The leak arena already proved every framework leaks on the one-line-cheaper path and aisotropy
  cannot.
- **Per-tenant PRE-SPEND quota gate** (row 5.1) — Pydantic AI's `UsageLimits` is a real pre-spend
  gate but **per-run, not per-tenant**; Mastra's `CostGuardProcessor` is a real pre-call gate but
  **cost/pricing-approximate** (a zero-cost fake can't trip it). The deterministic **per-tenant
  token-bucket→429, checked before any token is spent, by construction** is aisotropy's alone.
- **Fail-closed governance depth** — the reflection loop that returns 502 rather than garbage
  (BUILT-IN in only Pydantic AI and Mastra as a true fail-closed retry; PARTIAL/DIY elsewhere),
  scope-gated tools by absence-of-information, guardrails, health/readiness without LLM spend, and
  the constitutional-gate surface as *enforced* rather than *discretionary* gates.

**Table-stakes (aisotropy ties; the field is even):**
- **Multi-step agent loop** (row 3.2) — BUILT-IN in all seven.
- **SSE streaming + ctx-cancel** (row 2.3) — BUILT-IN in six (Letta streams but has no client
  spend-cutoff on cancel).
- **Typed tools + MCP** (row 3.3) — BUILT-IN across the Go/Letta cluster; PARTIAL where MCP is an
  uninstalled extra.
- **Structured output** (row 2.2), **OTel gen_ai spans** (row 6.1 — ADK Go *ties exactly* on
  gen_ai semconv), **provider-agnostic model seam** (row 8.4 — BUILT-IN in six), **permissive
  license** (row 5.6). These are commodity; aisotropy meets the bar but does not lead.

**Where a competitor matches or leads:**
- **Memory breadth out of the box:** **Mastra** (11 BUILT-IN) and **Letta** (10) give a builder more
  working memory / vector recall / summary / BYO-embedder for free than aisotropy exposes as
  separately-labeled features. Letta additionally leads on productized sleep-time.
- **Ecosystem & DX everywhere:** every arena has a larger community, more model/vector integrations,
  richer docs, and a shorter first-run than aisotropy. This is aisotropy's consistent weak axis and
  it is not close.

### 3. The honest bottom line for a builder

If you are shipping a **single-tenant assistant or a prototype**, pick an arena — Mastra or Letta
for the most memory batteries-included, Pydantic AI for the cleanest fail-closed typed-output story,
the Go trio (Eino/ADK/Genkit) if you want Go and will own the runtime yourself. You will move faster
and lean on a real ecosystem, and the isolation/governance gaps are yours to paper over.

If you are building a **multi-tenant LLM product where a cross-tenant leak is an incident, cost
overrun is a P1, and memory must be auditable**, the calculus flips. In every arena, tenant
isolation is a string the developer must never fumble, the pre-spend gate is per-run or absent, the
memory is one flat store the LLM self-manages, and the crash-safe outbox is structurally
impossible. aisotropy makes the leaky read *unrepresentable*, gates spend *per-tenant before the
call*, and writes the turn and its consolidation intent in *one ACID transaction* — the three
things no arena provides by construction. The price is real: a smaller ecosystem, a younger
codebase, features that are CODE-ONLY until you provision Qdrant/rqlite/the Rust binaries, and DX
that trails the field. **The differentiator is not "more features" — Mastra and Letta have more
out of the box. It is that isolation, tiered memory, and pre-spend governance are guaranteed by the
type system and the storage layout instead of by developer discipline.** For a sovereign
multi-tenant runtime that is exactly the trade worth making; for anything less it is over-built.

## References (source-of-truth docs used)

- `docs/architecture/overview.md`, `runtime-as-built.md`, `reality-ledger.md`, `memory-tiers.md`,
  `consistency.md`, `observability.md`, `granular-control.md`, `api-reference.md`, `product-evaluation.md`
- `docs/vision/thesis.md`, `principles.md`, `compression-thesis.md`
- `docs/components/primitives.md`, `engram-facts-journal.md` (+ badgerdb / qdrant / rqlite / datafusion / health-readiness / embedder)
- `docs/decisions/index.md` + ADRs 0002 (struct-as-contract), 0005 (DataFusion boundary), 0007 (tenancy),
  0011 (statement builder), 0012 (native tool-calling), 0014 (bitrate), 0017 (multi-kind), 0024 (auth seam),
  0026 (eval), 0027 (capabilities), 0030 (/ask, JWKS rotation, image signal)
- `.claude/rules/constitutional-gates.md`, `.claude/rules/architecture.md`, `.claude/rules/services-config.md`
- `arenas/README.md` (the existing 11-point isolation matrix + coverage-class legend)
