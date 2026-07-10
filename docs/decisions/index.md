---
type: Section Index
title: Architecture decisions (ADRs)
description: Chronological log of Aisotropy's architecture decisions, with context, options, trade-offs, and consequences. Fulfills the role of the OKF bundle's log.md.
tags: [decisions, adr, log]
timestamp: 2026-07-08T00:00:00Z
---

# Architecture decisions (ADRs)

Decision log. Each ADR: **Context → Options considered → Decision → Consequences**
(Google design-doc style format: includes *goals/non-goals* and *discarded alternatives*).

| ADR | Title | Status |
|---|---|---|
| [0000](0000-framework-name-aisotropy.md) | Framework name | **✅ accepted → "Aisotropy" (AI + isotropy)** |
| [0001](0001-go-rust-in-process-first-stack.md) | Go+Rust stack, in-process-first | **✅ accepted → in-process Go / dedicated Rust services** |
| [0002](0002-anti-framework-magic-struct-as-contract.md) | Anti-framework-magic (discard LangChain; struct as single contract) | **✅ accepted** |
| [0003](0003-specialized-hybrid-stack-vs-surrealdb.md) | Specialized hybrid stack vs. multi-model SurrealDB | **✅ accepted → hybrid** |
| [0004](0004-facts-memory-rqlite-vs-litefs.md) | rqlite vs LiteFS for facts memory | **✅ accepted → rqlite** |
| [0005](0005-datafusion-go-rust-boundary.md) | Rust↔Go boundary for DataFusion (FFI vs worker vs WASM) | **✅ accepted → separate Rust worker** |
| [0006](0006-reranker-cohere-vs-bge-local.md) | Reranker: Cohere SaaS vs local BGE-reranker | **✅ accepted → local BGE** |
| [0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) | Physical multi-tenancy: single filtered DB vs DB-per-tenant | **✅ accepted → hybrid/tiered** |
| [0008](0008-reflection-loop-gollem-deferral.md) | Phase 1 reflection loop: in-house loop vs Gollem | **✅ accepted → in-house loop now, Gollem in Phase 2** |
| [0009](0009-provider-agnostic-llm-layer.md) | Provider-agnostic, multi-connection LLM layer | **✅ accepted → Registry of named Client connections** |
| [0010](0010-gollem-adoption-scope-and-observability.md) | Gollem adoption scope, and the two-path observability decision | **✅ accepted → selective adopt/adopt-later/do-not-adopt matrix** |
| [0011](0011-tenant-scoped-statement-builder.md) | Tenant-scoped statement builder: isolation by construction for the pooled facts tier | **✅ accepted → `internal/tenancy`; an unscoped statement is a construction-time error** |
| [0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) | Gollem native tool-calling vs. homegrown agentloop | 🟡 **proposed** |
| [0013](0013-public-extension-surface-facade-vs-promote.md) | Public extension surface: façade package + builder vs. promoting `internal/` | **✅ accepted → façade `aisotropy/` + builder (B+C)** |
| [0014](0014-bits-per-token-reference-scorer-boundary.md) | Bits-per-token reference scorer: Go↔subprocess boundary + staged Python→Rust (Bet 6) | **✅ accepted → offline scorer, staged** |
| [0015](0015-bpt-eval-model-and-python-batch-runtime.md) | Pinned eval model + Python-torch batch runtime for Bet 6 (Phase 1) | **✅ accepted → distilgpt2 (Apache-2.0), env-gated** |
| [0016](0016-dual-surface-sealed-runtime-composition-sdk.md) | Dual surface: sealed runtime kernel + composition SDK over ctx-scoped nodes (be both A runtime and B framework) | **✅ accepted → dual A+B, invariant "node never constructs a Scope"; levers #1 (typed tools) + #2 (MCP) delivered** |
| [0017](0017-semantic-tier-multi-kind-memory.md) | Semantic tier as a multi-kind associative memory (Document/Fact/Turn), with the scoped MemoryStore aggregate | **✅ accepted → per-kind lifecycle/bound/weight; facade owns Fact-kind vectors; Turn-kind bounded** |
| [0018](0018-knowledge-ingestion-surface.md) | Knowledge-ingestion surface (Document kind): dual-surface SDK + HTTP, BYO embedder, chunk→embed→KindDocument | **✅ accepted → `WithEmbedder` + `IngestKnowledge` + `POST .../knowledge`, (tenant,user)-scoped** |
| [0019](0019-tenant-wide-document-sharing.md) | Tenant-wide document sharing: tenant-only scope + merged retrieval, KindDocument only | **✅ accepted → `NewTenantScope` + `IngestTenantKnowledge` + `POST .../tenant/{id}/knowledge`; user-boundary relaxed for Documents only, never cross-tenant** |

## Decisions already made in the source conversations

These were closed in the original design and are documented as retroactive "accepted" ADRs:

- **Discard LangChain** for its overhead, hidden prompts, and invisible token loops (ADR-0002).
- **Discard Turso as core** for its BSL license on `sqld`; use rqlite (MIT) or LiteFS
  (Apache-2.0) to avoid licensing gray areas.
- **Discard GlareDB** (abandoned at the end of 2025); use Apache DataFusion, its core engine.
- **Do not consolidate on SurrealDB** for the core (ADR-0003): working memory loses latency
  out of process, and Qdrant scales better as a dedicated vector engine.

## ADRs written

- **[ADR-0000 — Framework name](0000-framework-name-aisotropy.md):** ✅ **accepted → "Aisotropy"**
  (AI + isotropy). Retroactive formalization of the naming decision closed in the original design
  conversations.
- **[ADR-0001 — Go+Rust, in-process-first](0001-go-rust-in-process-first-stack.md):** ✅ **accepted →
  in-process Go where latency matters, dedicated Rust services where scale matters** (the stack's
  golden rule). Retroactive formalization.
- **[ADR-0002 — Anti-framework-magic](0002-anti-framework-magic-struct-as-contract.md):** ✅
  **accepted → discard LangChain-style magic; the Go struct is the single contract** (HTTP
  validation + OpenAPI 3.1 + JSON Schema enforced on the LLM). Retroactive formalization.
- **[ADR-0003 — Specialized hybrid stack](0003-specialized-hybrid-stack-vs-surrealdb.md):** ✅
  **accepted → hybrid specialized stack over a single multi-model DB** (SurrealDB discarded:
  working memory loses latency out of process; Qdrant matures better at scale). Retroactive
  formalization.
- **[ADR-0004 — rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md):** ✅ **accepted → rqlite**.
  Decisive finding: LiteFS is in **maintenance mode** (Fly.io: "limited updates, no significant
  development plan, no support"; last release April 2025) — the same pattern that sank
  GlareDB. rqlite also provides configurable strong consistency (Raft), key for the durable ACK the
  Transactional Outbox needs.
- **[ADR-0005 — DataFusion boundary](0005-datafusion-go-rust-boundary.md):** ✅ **accepted →
  separate Rust worker** (batch subprocess with Parquet/Arrow IPC). Since the sleep cycle is
  batch/offline, there's no in-process latency requirement; CGO would break Go's static binary,
  and WASM is still experimental in 2026. Evolution path: Arrow Flight SQL if interactive
  analytics appears.
- **[ADR-0006 — Reranker](0006-reranker-cohere-vs-bge-local.md):** ✅ **accepted → local
  BGE-reranker-v2-m3** (Apache-2.0), with Qwen3-Reranker-0.6B as a drop-in upgrade; **Cohere
  discarded**. In the hot path, a SaaS adds 80–150 ms of network latency to the TTFT and sends
  tenant data outside (violates sovereignty). The open-vs-Cohere quality gap **closed in 2026**.
  jina discarded for its non-commercial license. It's the first step of the
  [sovereign NN core (Bet 5)](../research/bet-05-sovereign-nn-core.md).
- **[ADR-0007 — Physical multi-tenancy](0007-multitenancy-fisico-db-unica-vs-por-tenant.md):** ✅
  **accepted → hybrid/tiered**: pooled (single DB filtered by `tenant_id` over an rqlite cluster)
  as the default; a dedicated cluster only for large or compliance-critical tenants. Decisive
  finding: **rqlite is a single DB per Raft cluster** (issue #927 open) → a literal
  "DB-per-tenant" = one full cluster per tenant = unviable at scale. It also resolves the leader
  write ceiling flagged by ADR-0004.

- **[ADR-0008 — Phase 1 reflection loop](0008-reflection-loop-gollem-deferral.md):** ✅ **accepted →
  in-house loop (`Reflect[T]`) now, Gollem in Phase 2**. Gollem (`core.RunWithReflection[T]`)
  requires wrapping `llm.Client` in `core.Model` and can't be exercised without an API key; the
  walking skeleton prioritizes the verifiable SSE flow. Tracked as debt with an explicit
  integration plan for Phase 2.

- **[ADR-0009 — Provider-agnostic LLM layer](0009-provider-agnostic-llm-layer.md):** ✅ **accepted →
  `Registry` of named `Client` connections**. `internal/llm` no longer locks the runtime into a
  single provider chosen once by precedence; `RegistryFromEnv()` registers every provider with
  configured credentials (`openrouter`/`openai`/`anthropic`, always `stub`) simultaneously, and
  `FromEnv()` is now `RegistryFromEnv().Default()` for backward compatibility. `Client` is
  unchanged. Unblocks per-request routing for [bet-04](../research/bet-04-compressibility-routing.md).

- **[ADR-0010 — Gollem adoption scope and observability](0010-gollem-adoption-scope-and-observability.md):**
  ✅ **accepted → selective adopt/adopt-later/do-not-adopt matrix**. Grounded in a full
  reverse-engineering pass over Gollem v0.5.1 (`external/GOLLEM-ANALYSIS.md`): adopt now (OTel
  `gen_ai.*` attributes, `CostTracker`, guardrails/`RedactPII`/`AuditLog`) as `AgentOption`s on the
  existing `GollemReflect` agent; adopt the *pattern* but not the code from `ext/orchestrator`
  (its SQLite substrate conflicts with the BadgerDB Outbox + rqlite Raft-ACK design, ADR-0004); do
  **not** adopt `autocontext`, `ext/memory` SQLite stores, or auto-`Retrieve` `KnowledgeBase`
  (conflict with the tiered-memory design and the "Gollem consumes already-hydrated memory"
  boundary); build the per-tenant pre-provider token-bucket circuit breaker in-house rather than via
  `core.UsageQuota` (run-scoped/post-hoc, not a pre-provider breaker). Also resolves the two-path
  observability split: the streaming chat path (`chat_stream.go` → `Client.StreamChat` directly)
  gets its own provider-agnostic `WithOTel(Client)` decorator, while Gollem's `ext/otel` covers only
  the reflection/`Agent[T]` path.

- **[ADR-0011 — Tenant-scoped statement builder](0011-tenant-scoped-statement-builder.md):** ✅
  **accepted → `internal/tenancy`**. Replaces the "always remember `WHERE tenant_id`" discipline
  for the pooled rqlite facts tier (ADR-0007) with an API where a tenant-unscoped statement is a
  construction-time error, not a code-review catch — built before any Phase 3 rqlite SQL exists so
  the facts tier cannot be written unsafely. Closes the "silent isolation leak" admission in
  `request-lifecycle.md`.

- **[ADR-0012 — Gollem as the agent-loop engine: native tool-calling vs. the homegrown agentloop](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md):**
  ✅ **accepted → Option C, core delivered**. The dedicated ADR that ADR-0010's guardrail requires
  before any Gollem adoption that would widen the `llm.Client` seam toward native tool-calling.
  Records that Gollem is already the reflection engine (`chat.go`) but the agent endpoint
  (`chat_agent.go`) hand-rolled tool dispatch via the structured-output `agentloop` because `Client`
  exposed no function-calling primitive. **Option C**: an *optional, additive* `ToolCaller`
  capability interface (`internal/llm/tools.go`, discovered by type assertion), with the agent
  endpoint capability-detecting it and driving a native tool loop (`runNativeToolLoop`) — the
  `agentloop` retained as the fallback for providers/stub that don't implement it — **without**
  changing the `Client` contract or subordinating ADR-0009's provider-agnostic `Registry`/routing to
  Gollem's provider layer. Both dispatch branches enforce the identical double scope-gate. Rejects
  widening `Client` itself (breaks every implementer) and Gollem-native provider ownership (ADR-0010
  guardrail). Delivered: the seam + native dispatch + fallback + scope-gate tests, plus
  `OpenAICompatClient` (OpenRouter/OpenAI) implementing `ToolCaller` — **live-validated end to end**
  against `openrouter:qwen/qwen3.7-plus`. Tracked follow-up: optionally an Anthropic-SDK `ToolCaller`
  and routing the native path through Gollem `Agent[T]` for its guardrails/`CostTracker`/OTel.

- **[ADR-0013 — Public extension surface: façade + builder](0013-public-extension-surface-facade-vs-promote.md):**
  ✅ **accepted (2026-07-09) → implemented**. The Phase 8 (adoption surface) decision: let a third party build
  their bot on Aisotropy **without forking**. Chooses **B + C** — a thin importable façade package
  `aisotropy/` that re-exports only the extension types via **type aliases** (`Tool`, `ToolExecutor`,
  `LLMClient`, `Persona`, …) plus a functional-options **builder** (`aisotropy.New(...).Run(ctx)`) that
  internalizes everything `cmd/aisotropyd/main.go` does today — over **promoting the `internal/`
  packages wholesale** (Option A, rejected: freezes ~10 churning packages and regresses isolation by
  making `facts.Scope`/`ExportAll` public). Fixes two real internal gaps on the way (executor-injection
  in `RegisterAgent`, a `PersonaProvider` seam). Preserves anti-magic (ADR-0002), isolation-by-construction
  (ADR-0011), and reconstruction-phase churn freedom (compat scoped to one 0.x package). Must sit *above*
  ADR-0012's native tool-calling. Front-loaded open question: keep `ToolExecutor` args as
  `map[string]string` for 0.x (provisionally yes), revisit with ADR-0012.

- **[ADR-0014 — Bits-per-token reference scorer](0014-bits-per-token-reference-scorer-boundary.md):**
  ✅ **accepted (2026-07-09)**. Instruments [Bet 6](../research/bet-06-bits-per-token-metric.md) (the
  compression North Star). Because no serving provider exposes log-probs, bpt is computed **offline by a
  fixed reference model in a batch subprocess** (the ADR-0005 pattern), behind a stable Go
  `internal/bitrate.Runner` contract. **Staged scorer:** Phase 1 = Python spike wrapped as an
  `AISOTROPY_BITRATE_BIN` subprocess (ships the validated +1.469/+1.731 bits lift now), Phase 2 = Rust
  candle behind the *identical* contract (sovereign, Bet 5 on-ramp). Rejects Option A-promote (nothing)
  and defer-to-Bet-5. "With/without memory" reuses `hydrateSystemPrompt` (ADR-0002); a held-out sampler
  prevents leakage; a durable tenant-scoped store (ADR-0011) holds the series; surfaced via `GET /bitrate`
  + OTel `agent.compression.bpt`.
- **[ADR-0015 — Pinned eval model + Python batch runtime](0015-bpt-eval-model-and-python-batch-runtime.md):**
  ✅ **accepted (2026-07-09)**. The dependency gate for ADR-0014 Phase 1: ratifies a **contained,
  time-boxed** Python/torch/transformers batch runtime (offline, optional, env-gated, subprocess-only —
  the Go module and core binary gain nothing) and pins **`distilgpt2` (Apache-2.0)** as the shipped
  eval model for license cleanliness and cross-time comparability; `DeepESP/gpt2-spanish` stays opt-in
  pending a weight-license check. Superseded when the Phase-2 Rust scorer lands.
- **[ADR-0016 — Dual surface: sealed kernel + composition SDK](0016-dual-surface-sealed-runtime-composition-sdk.md):**
  ✅ **accepted (2026-07-09)**. Makes Aisotropy **both** (A) a hostable multi-tenant runtime **and**
  (B) a composable agent framework — arbitrary tool topologies, graphs/workflows, multi-agent teams,
  embeddable single-tenant bots — **without** the isolation regression that made ADR-0013 reject
  promoting `internal/`. Reframes that single rejection into **two separable axes**: a **sealed
  kernel** (tenancy/`Scope`/memory tiers, never public) and a **composition surface** (safely
  public), held together by one load-bearing invariant — *a composed node receives an already-scoped
  `ctx` and never constructs a `Scope`* (already realized by `internal/tenancy.FromContext`). Layers
  the public surface: **L0** sealed kernel / **L1** façade (ADR-0013, unchanged) / **L2** composition
  SDK **wrapping** the already-vendored Gollem `ext/{graph,orchestrator,team,mcp}` under scoped nodes.
  Decides the **polyglot fork**: adopt MCP-backed tool *execution* (any language) + OpenAPI-generated
  clients now; **defer** a TypeScript *authoring* SDK. Sequences 5 levers by leverage —
  (1) typed tool args ([SDD](../roadmap/sdd/typed-tool-args.md)), (2) MCP tools, (3) `docker-compose`
  reference (light the memory-tier moat by default), (4) L2 graphs/teams (only after 2–3 real
  consumers), (5) wiring-only declarative config. Extends (does not reverse) ADR-0013; preserves
  ADR-0011 isolation and ADR-0002 anti-magic. Implements [Phase 9](../roadmap/milestones.md#phase-9--composition-surface-dual-a--b).

- **[ADR-0017 — Semantic-tier multi-kind memory](0017-semantic-tier-multi-kind-memory.md):**
  ✅ **accepted (2026-07-09)**. The semantic tier stores **kind-tagged** vectors — `KindDocument`
  (curated tenant/user knowledge), `KindFact` (distilled facts dual-indexed via the `engram` facade,
  their lifecycle tied to the fact), and `KindTurn` (raw turns, bounded by a TTL/cap pruner). The
  `PointID(scope, kind, sourceID)` encoding keeps a fact's and a turn's vector from ever colliding,
  and each kind gets its own retrieval + lifecycle policy.

- **[ADR-0018 — Knowledge-ingestion surface](0018-knowledge-ingestion-surface.md):**
  ✅ **accepted (2026-07-09)**. A **dual-surface** knowledge ingestion path — the SDK
  `IngestKnowledge` and the HTTP `POST .../knowledge` (+ DELETE) — that writes `KindDocument` vectors
  (user- or tenant-scoped), with a **bring-your-own** OpenAI-compatible embedder
  (`AISOTROPY_EMBED_URL`), so a consumer feeds curated knowledge without touching the kernel.

- **[ADR-0019 — Tenant-wide document sharing](0019-tenant-wide-document-sharing.md):**
  ✅ **accepted (2026-07-09)**. A **tenant-wide shared `KindDocument` bucket**: a document ingested
  once at tenant scope is retrievable by every user in that tenant, merged with the user-scoped
  results at hydration — extending ADR-0017/0018 without weakening the per-user isolation of facts
  and turns.

- **[ADR-0020 — Build vs. adopt the LangChain suite or Mastra](0020-build-vs-adopt-lang-suite-mastra.md):**
  ✅ **accepted (2026-07-10)**. Promotes the [competitive-landscape](../research/competitive-landscape.md)
  research report into a **falsifiable positioning decision**: building the sovereign in-process Go+Rust
  runtime is the right call **only** for a Go-fluent team shipping a long-lived, multi-tenant,
  sovereignty-constrained SaaS — and the wrong call otherwise (adopt LangChain-suite/Mastra for
  speed-to-market, ecosystem breadth, single-tenant, or a TS/Python team). Includes an honest win/loss
  scorecard (wins: isolation-by-construction, sovereignty, efficiency, supply chain, no-magic; **loses
  badly on ecosystem/integrations/DX/community**) and explicit **"when this stops being true" revisit
  triggers** (team leaves Go, product stops being multi-tenant, a comparator closes the sovereignty or
  isolation gap, time-to-market dominates, or a fair benchmark shrinks the efficiency edge). Depends on
  the [reality ledger](../architecture/reality-ledger.md) staying accurate.

## Next ADRs to write

- New ones arising from the research phase (e.g., the codec format for [Bet 2](../research/bet-02-working-memory-codec.md)).
