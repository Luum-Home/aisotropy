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
| [0013](0013-public-extension-surface-facade-vs-promote.md) | Public extension surface: façade package + builder vs. promoting `internal/` | 🟡 **proposed** |

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
  🟡 **proposed → pending approval**. The Phase 8 (adoption surface) decision: let a third party build
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

## Next ADRs to write

- New ones arising from the research phase (e.g., the codec format for [Bet 2](../research/bet-02-working-memory-codec.md), deterministic model versioning).
