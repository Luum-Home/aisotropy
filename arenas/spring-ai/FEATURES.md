---
type: Feature matrix
title: "Spring AI — features vs aisotropy"
description: Coverage map of Spring AI 2.0.0 capabilities vs aisotropy's built-ins, grounded in what was executed or source-verified against the shipped jars.
tags: [arenas, spring-ai, features, comparison, jvm]
timestamp: 2026-07-16T00:00:00Z
---

# Spring AI — Features

**Framework:** Spring AI 2.0.0 (`spring-ai-client-chat` / `spring-ai-model` / `spring-ai-commons`),
Apache-2.0, Java/JVM. **Integration status: REAL** — Orbit compiled and run against the genuine jars
with a keyless `EchoChatModel`. Each row is tagged **RAN** (executed in a demo here) or
**SOURCE** (verified from the shipped classes via `javap`/`unzip -l`, or from the reference docs;
not executed). Nothing is asserted from general knowledge.

**Tally across the 42 coverage-class rows (excludes 5.7, a license-posture scale):**
**BUILT-IN 8 · PARTIAL 11 · CONVENTION 3 · DIY 20 · PAID 0 · NOT-POSSIBLE 0.**
Distinctive shape: **zero PAID, zero NOT-POSSIBLE** — Spring AI is fully OSS and, being a Spring
library, the JVM ecosystem turns most gaps into DIY rather than hard walls. What it does NOT close
is the axis that matters here: isolation is convention, and pre-spend governance is absent.

---

## Area 1 — Memory tiers

### 1.1 Temperature hierarchy (4-tier) — **PARTIAL** · SOURCE
Spring AI exposes **two surfaces of different nature**: `ChatMemory` (conversation message window,
keyed by `conversationId`, backed by `ChatMemoryRepository`) and `VectorStore` (semantic, separate
`spring-ai-vector-store` module). But there is **no full-text/facts (FTS) tier** and **no offline
batch/consolidation tier**, and the two surfaces are not organized by access temperature. aisotropy
= 4 stores of genuinely different nature (BadgerDB µs / SQLite+FTS5 / Qdrant / DataFusion).

### 1.2 Working memory — **BUILT-IN** · RAN
`MessageWindowChatMemory` + `InMemoryChatMemoryRepository` + `MessageChatMemoryAdvisor`. **Executed:**
turn 3 recalled turn-1's order id via automatic hydration (`FeatureDemoMain` line 1, stored msgs=6).
The advisor loads history for the `conversationId`, injects it into the prompt, and persists the new
turn. Comparable to aisotropy's BadgerDB working memory (Spring's is a `ConcurrentHashMap` in the
default repo, JDBC/Cassandra/Neo4j repos available).

### 1.3 Facts memory (FTS journal) — **DIY** · SOURCE
No keyword/FTS facts journal. `ChatMemory` is message history; there is no `search(query)` over a
full-text index. aisotropy ships a SQLite+FTS5 Engram journal. DIY on Spring AI.

### 1.4 Semantic memory (vectors) — **BUILT-IN** · SOURCE
`VectorStore` is a first-class Spring AI abstraction (PgVector, Qdrant, Redis, Chroma, …) with
`QuestionAnswerAdvisor` for RAG and `FilterExpressionBuilder` for metadata filters. *Source-verified
as a shipped module* (`spring-ai-vector-store`, not on this arena's minimal chat classpath, so not
executed here). Tenant/user scoping is developer-supplied metadata (see 4.3).

### 1.5 Engram dual-index (fact↔vector) — **DIY** · SOURCE
No fact↔vector dual-index invariant. DIY.

### 1.6 Summary buffer / context engineering — **PARTIAL** · RAN
`MessageWindowChatMemory` **evicts** at the window boundary (turn-aware, retains `SystemMessage`).
**Executed:** `maxMessages=4` kept 4 of 6 turns (`FeatureDemoMain` line 2). It **drops** oldest
turns — a rolling **summary condensation** (aisotropy's `LLMSummarizer`) is not built in (1.x has no
summarizing memory advisor). Eviction BUILT-IN; summary DIY → PARTIAL.

### 1.7 Engram scoring (Rel×Imp×Rec) — **DIY** · SOURCE
Window memory is recency-only (last N). No importance/relevance composite. DIY.

### 1.8 Contradiction detection / supersede — **DIY** · SOURCE
No supersede/archival of stale facts. DIY.

### 1.9 Knowledge ingestion (user + tenant-shared) — **PARTIAL** · SOURCE
Spring AI ships an **ETL pipeline** (document readers, `TokenTextSplitter`, `VectorStore.add()`) —
real ingestion machinery. But user-private vs tenant-shared scoping is developer metadata, not a
first-class dual surface. PARTIAL.

### 1.10 Primitive self-improvement — **DIY** · SOURCE
No friction-driven per-user tool-metadata rewriting. DIY (nobody has this).

### 1.11 Bring-your-own embedder — **BUILT-IN** · SOURCE
`EmbeddingModel` is a swappable interface (`org/springframework/ai/embedding/EmbeddingModel.class`
confirmed on classpath) with OpenAI/Ollama/ONNX-Transformers (local) impls. Self-hostable. BUILT-IN.

---

## Area 2 — Contract & structured output

### 2.1 Struct-as-contract (1 struct, 3 uses) — **DIY** · SOURCE
`BeanOutputConverter` derives a JSON schema for the LLM from a POJO/record — but HTTP validation and
OpenAPI come from separate Spring MVC + springdoc declarations. Not one definition driving all three
(aisotropy's Huma struct). DIY.

### 2.2 Fail-closed reflection loop (502) — **PARTIAL** · RAN
`BeanOutputConverter.convert()` parses + validates and **throws** on malformed JSON (fail-closed
*parse*). **Executed:** `getFormat()`/`getJsonSchema()` produced, `convert(json)` → typed `Ticket`
(`FeatureDemoMain` line 3) — the plain converter is fail-closed at the *parse* step: a schema
violation **throws**, it is not an automatic re-request with the typing error. Spring AI **2.0** now
ships *self-correcting structured output* (provider-native + schema-validation retry) per the
[2026-06 blog](https://spring.io/blog/2026/06/23/spring-ai-self-correcting-structured-output/), so a
bounded repair loop is available in-framework (it was DIY in 1.x). Orbit still exercises the plain
converter, not that retry advisor — SOURCE, not executed here. → PARTIAL. aisotropy's reflection loop
is default and returns 502.

### 2.3 SSE + immediate ctx-cancel — **BUILT-IN** · SOURCE
`StreamingChatModel.stream(Prompt) : Flux<ChatResponse>` (confirmed via `javap`; `ChatModel` extends
it). Streaming is BUILT-IN and Reactive; **cancellation** propagates via Reactor `Flux` unsubscribe/
dispose (Reactor semantics — source-verified, **not executed** here). aisotropy's Huma SSE cuts the
upstream call on `ctx.Done()`.

### 2.4 Stateless /ask + response cache — **DIY** · SOURCE
No built-in response cache (exact or semantic) in the core. DIY.

---

## Area 3 — Tools & agentic

### 3.1 Scope-gated tools (governance by absence) — **DIY** · SOURCE
You assemble the tool list and pass it to `.defaultTools()`/`.defaultToolCallbacks()`. No
scope-gating primitive — filtering tools by caller scope is your code. DIY.

### 3.2 Native agent loop / tool calling — **BUILT-IN** · SOURCE
Native function/tool calling with a framework-run tool-execution loop
(`org/springframework/ai/model/tool/*` — `ToolExecutionResult`, `ToolExecutionEligibilityChecker`
confirmed on classpath). Multi-step reason→act→observe is BUILT-IN.

### 3.3 Typed tools + MCP — **BUILT-IN** · SOURCE
`@Tool` annotation (typed method tools, `org/springframework/ai/tool/annotation/Tool.class`
confirmed) + **MCP client shipped** (`mcp-core` / `mcp-json-jackson2` 0.18.3 on the resolved tree).
BUILT-IN, matching the Go/Letta cluster.

### 3.4 Model-selection gateway (router) — **DIY** · SOURCE
No cost/capability router. You pick the `ChatModel` bean (multiple can coexist). DIY.

---

## Area 4 — Isolation & auth (the crux)

### 4.1 Multi-tenant isolation BY CONSTRUCTION — **CONVENTION** · RAN
Memory keyed by a single opaque `conversationId` String, bound to no principal. **Executed leak**
(`SmokeMain`): a globex caller on acme's key reads acme's secret; `findConversationIds()` enumerates
every tenant's conversations; `findByConversationId(rawKey)` reads with no tenant check. Correct and
leaky reads are one argument apart. aisotropy = by-construction (ctx + per-key/statement scoping).

### 4.2 Value-object Scope (unforgeable) — **DIY** · SOURCE
`conversationId` is a bare `String` — forgeable and collidable (see 4.7 leak 3). No value object with
unexported fields. DIY (all arenas are DIY here).

### 4.3 Vector metadata filter (in-scan, not post-filter) — **CONVENTION** · SOURCE
`VectorStore` search takes a `FilterExpression` (`FilterExpressionBuilder`) applied by the backing
store during search — but the tenant filter is **developer-supplied metadata**, not bound to an
identity. Omit or mis-set it → cross-tenant vectors. CONVENTION (matches Mastra/LangGraph).

### 4.4 Authenticated-identity seam (must-agree bind) — **DIY** · SOURCE
Spring AI binds no principal to `conversationId`. The **enterprise-JVM nuance**: Spring Security is
the idiomatic, readily-wired way to authenticate the caller and derive the memory key from the
verified principal — so this is the **most natural DIY of any arena** — but nothing in Spring AI
*enforces* the bind; you wire it. DIY (not NOT-POSSIBLE, because the ecosystem makes it trivial).

### 4.5 JWKS key rotation — **DIY** · SOURCE
Spring Security's `oauth2-resource-server` does JWKS rotation, but that is the ecosystem, not Spring
AI, and it is not wired to memory scope. DIY.

### 4.6 Guardrails (redact/inject/output) — **PARTIAL** · SOURCE
`SafeGuardAdvisor` (keyword/sensitive-term block, `org/springframework/ai/chat/client/advisor/
SafeGuardAdvisor.class` confirmed) + `ModerationModel` (output moderation abstraction). Real but
narrow: no PII/secret redaction that rewrites what downstream sees, no injection detector. PARTIAL.

### 4.7 Multi-product isolation (4th scope axis) — **CONVENTION** · RAN
No product concept; product is one more unescaped segment of the `conversationId` string.
**Executed** (`ProductIsolationMain`): controls held; **3 cross-product leaks reproduced** with the
same tenant+user — (1) wrong-product substitution, (2) org-wide `findConversationIds()` scan,
(3) **delimiter-injection collision** where two distinct identities map to the same flat key
(`"acme|alice|productX|s1"`). aisotropy's `product_id` rides `context.Context` in the `Scope` value
object; a wrong/forgotten product is unrepresentable.

---

## Area 5 — Cost & governance

### 5.1 Per-tenant PRE-SPEND quota gate — **DIY** · SOURCE
No pre-spend token-bucket gate. DIY. aisotropy rejects (429) per-tenant *before* the LLM call.

### 5.2 Circuit breaker — **DIY** · SOURCE
No built-in breaker (resilience4j is the ecosystem, not Spring AI). DIY.

### 5.3 Quota-pressure model degradation — **DIY** · SOURCE
No pressure-based cheap-model fallback. DIY.

### 5.4 Cost meter (per-tenant) — **PARTIAL** · SOURCE
`ChatResponse.getMetadata().getUsage()` returns `getPromptTokens()/getCompletionTokens()/
getTotalTokens()/getNativeUsage()` per call (confirmed via `javap`) — per-call usage BUILT-IN.
Per-tenant aggregation is DIY (no tenant concept). PARTIAL.

### 5.5 Constitutional-gate governance surface — **DIY** · SOURCE
No enforced isolation/cost/license/contract gates. DIY.

### 5.6 License 100% MIT/Apache, self-hostable — **BUILT-IN** · SOURCE
Apache-2.0, fully self-hostable, no SaaS gate anywhere in the framework. BUILT-IN.

### 5.7 Dependency license posture (scanned) — **PERMISSIVE** · RAN
`license-maven-plugin` over the real 2.0.0 tree (`scripts/license_scan.sh`, exit 0):
- **34 dependencies scanned**, **100% permissive**.
- **Apache-2.0** (27): all Spring 7 / Spring-AI 2.0 / Jackson (2.x annotations + 3.x core/databind) /
  Micrometer / Reactor / victools / networknt / itu / classmate / swagger / commons-logging / jspecify.
- **MIT** (2): jtokkit, slf4j-api; **MIT-0** (1): reactive-streams.
- **BSD** (4): ST4 (BSD), ANTLR 3 (BSD), ANTLR 4 (BSD-3-Clause), HdrHistogram (BSD-2-Clause/CC0).
- **Strong copyleft (GPL/AGPL/LGPL): 0. Weak copyleft (MPL-2.0): 0. Restricted/source-available
  (BUSL/SSPL/ELv2/Commons-Clause/FSL): 0.**
- **Verdict:** a **pure permissive line** — stronger than the Python arenas (Pydantic AI / LangGraph /
  Letta each carry an MPL-2.0 leaf via `certifi`). Spring AI joins Mastra / Eino / ADK / Genkit with
  no weak-copyleft even. Clears aisotropy's hard gate outright; does not literally match the strict
  "100% MIT/Apache" (BSD/CC0 present) but carries zero caution items.

---

## Area 6 — Observability

### 6.1 OTel gen_ai spans w/ tenant native — **PARTIAL** · SOURCE
Spring AI emits **Micrometer Observations** (`ChatClientObservationConvention`,
`ChatClientObservationContext` confirmed) around chat calls — low-cardinality gen_ai-style keys.
Bridging to OTel needs `micrometer-tracing` + an exporter (add-on, not in this tree). `tenant_id`:
DIY (no tenant concept). PARTIAL — native instrumentation, but the OTel span + tenant tag are wiring.

### 6.2 Health / readiness probes (no LLM spend) — **PARTIAL** · SOURCE
Spring Boot **Actuator** `/health` is the idiomatic BUILT-IN in the ecosystem, and Spring AI ships
health indicators for some models/vector stores — but a **tier-scoped readiness that never spends an
LLM token** (aisotropy's `/health`) is DIY. PARTIAL.

### 6.3 Bits-per-token North Star — **DIY** · SOURCE
No compression/memory-lift metric. DIY (nobody has it).

### 6.4 Product-evaluation harness (IR + judge) — **PARTIAL** · SOURCE
`Evaluator` interface + `RelevancyEvaluator` + `FactCheckingEvaluator` shipped
(`org/springframework/ai/chat/evaluation/*` confirmed) — LLM-as-judge relevancy/faithfulness is
BUILT-IN. IR metrics (P@k/MRR) and isolation-preserving retrieval eval are DIY. PARTIAL.

---

## Area 7 — Consolidation / sleep

### 7.1 Transactional outbox (atomic msg+event) — **DIY** · SOURCE
No outbox primitive. **The enterprise-JVM edge:** a JDBC-backed `ChatMemoryRepository` and a JDBC
vector store on the same `DataSource` *can* be wrapped in one Spring `@Transactional` — so atomic
message+intent is **buildable in-process**, unlike the 5 arenas where it is NOT-POSSIBLE (separate
stores, no shared transaction). But the **idempotent redelivery + cold-boot recovery worker** is
entirely DIY, and there is no outbox abstraction. DIY (with a real buildability advantage worth
naming). aisotropy writes turn+intent in one BadgerDB ACID txn, drained idempotently.

### 7.2 Sleep-cycle consolidation — **DIY** · SOURCE
No offline batch dedup/consolidation engine. DIY.

---

## Area 8 — Sovereignty & runtime shape

### 8.1 Single-binary zero-config runtime — **PARTIAL** · SOURCE
A Spring Boot **fat-jar** is a single self-contained, self-hostable deployable (needs a JRE) — but
it is a library you assemble, not a zero-config tiered-memory runtime. PARTIAL.

### 8.2 Dual surface (sealed kernel + SDK) — **PARTIAL** · SOURCE
Rich SDK/composition surface (advisors, tools, memory), but **no sealed multi-tenant isolation
kernel** — user code touches the raw `conversationId`. PARTIAL.

### 8.3 Go+Rust subprocess boundary — **DIY** · SOURCE
Pure JVM; N/A. DIY.

### 8.4 Provider-agnostic model registry — **BUILT-IN** · SOURCE
`ChatModel` is a provider-agnostic interface with many impls, swappable via Spring beans/config;
multiple providers coexist as beans. BUILT-IN.

---

## Headline (memory tiers — the aisotropy differentiator)

Spring AI gives **one memory nature that Orbit actually uses** — a `conversationId`-keyed message
window (`ChatMemory`) — plus a **separate `VectorStore`** abstraction in another module. There is
**no FTS/facts tier** (1.3 DIY) and **no offline batch-consolidation engine** (7.2 DIY), so it is a
**PARTIAL** on the 4-tier temperature hierarchy (1.1), not a hierarchy of four different-nature
stores. What it ships genuinely BUILT-IN: working memory + hydration advisor, provider-agnostic model
seam, native tool calling + `@Tool` + MCP, BYO embedder, a vector store abstraction, and an OSS
permissive license with no SaaS gate. Everything governance-shaped — the pre-spend gate, circuit
breaker, pressure degradation, per-tenant meter, scope-gated tools, isolation binding — is DIY or
convention.

**vs. aisotropy:** Spring AI is the most *enterprise-idiomatic* arena (Spring beans, Micrometer,
Actuator, Spring Security all one dependency away) and it has **zero PAID and zero NOT-POSSIBLE** —
the JVM ecosystem fills gaps as DIY rather than walls. But on the three axes that matter — isolation,
tiered memory, and pre-spend governance — it lands exactly where the field does: isolation is a
`conversationId` string bound to nothing, memory is one window nature, and there is no pre-spend gate.
aisotropy makes the leaky read unrepresentable and gates spend per-tenant before the call; Spring AI
leaves both to developer discipline (with excellent scaffolding to build them).
