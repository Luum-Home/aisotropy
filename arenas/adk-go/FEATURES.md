---
type: Feature matrix
title: "ADK Go — features vs aisotropy (real integration)"
description: Coverage map of Google ADK Go (google.golang.org/adk/v2 v2.0.0) verified against real source, compared to aisotropy's built-ins.
tags: [arenas, adk-go, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# ADK Go — Features

Verified against `google.golang.org/adk/v2@v2.0.0` source and exercised by the
real `orbit/` integration in this arena (builds, tests, and smokes offline).

Coverage classes: **BUILT-IN** (framework primitive), **CONVENTION** (works,
but by developer discipline), **PARTIAL**, **DIY**, **PAID**, **NOT-POSSIBLE**.

## #1 Multi-tenant isolation — CONVENTION (no tenant binding)

- **Real mechanism:** `session.Service.Get/Create` keyed by composite
  `(appName, userID, sessionID)`. `runner.Run(ctx, userID, sessionID, …)`
  passes `userID`/`sessionID` straight through.
- **Why CONVENTION, not BUILT-IN construction:** ADK has no `TenantID` field
  and does not bind the identity to an authenticated caller. The composite key
  isolates *when the developer folds tenant into it*; drop that component and
  two tenants collide silently (proven in `orbit/isolation.go`,
  `TestTenantDropLeaks`).
- **AISOTROPY COMPARISON:** aisotropy carries `tenant_id` in `context.Context`
  and scopes every memory read per-statement/per-key — the unscoped read is
  unrepresentable. ADK's *construction class* matches `adk-python`'s composite
  PK, but `adk-python#1641` shows even that leaked when a sub-query dropped the
  `user_id` filter. ADK Go leaves the whole *tenant* dimension to discipline.
- **Correction:** the prior arena claim ("no scoping / untyped `map[string]any`")
  was factually wrong about the real framework and is fixed here.

## #2 Structured output + retry — BUILT-IN

- **Real mechanism:** `llmagent.Config.OutputSchema *genai.Schema` +
  `OutputKey` (writes validated output into session state).
- **AISOTROPY COMPARISON:** aisotropy fail-closes on `/chat` (502) with a
  Gollem reflection loop. ADK validates against the schema at the node; retry
  semantics live in the workflow engine (`workflowagent` retry/timeout), not a
  dedicated repair loop.

## #3 Tool calling — BUILT-IN

- **Real mechanism:** `tool.Tool` interface; `functiontool`, `agenttool`,
  `mcptoolset` (MCP native), `geminitool` (e.g. GoogleSearch).
- **AISOTROPY COMPARISON:** equivalent to aisotropy's `WithTypedTool[T]`; ADK
  additionally ships first-class MCP + A2A tool surfaces.

## #4 Streaming (SSE) — BUILT-IN

- **Real mechanism:** `agent.RunConfig.StreamingMode = agent.StreamingModeSSE`;
  `runner.Run` yields events incrementally (`iter.Seq2[*session.Event, error]`).
- **AISOTROPY COMPARISON:** equivalent to aisotropy's Huma SSE + `ctx`
  cancellation.

## #5 Working memory — BUILT-IN

- **Real mechanism:** `session.Service` with three backends: `InMemoryService()`,
  `session/database` (sqlite/gorm), `session/vertexai`.
- **AISOTROPY COMPARISON:** aisotropy's BadgerDB is in-process, self-hostable,
  µs. ADK's in-mem/sqlite backends are self-hostable; the rich managed backend
  is Vertex-tethered.

## #6 Semantic memory (vectors) — PARTIAL

- **Real mechanism:** `memory.Service` (`InMemoryService()` does keyword
  `SearchMemory`; `memory/vertexai` is the vector-backed MemoryBank).
- **AISOTROPY COMPARISON:** aisotropy has in-process cosine (active) + Qdrant
  (self-hostable). ADK's real vector recall is **GCP-managed**, not
  self-hostable OSS.

## #7 Facts memory (journal) — DIY

- No FTS5/journal primitive. aisotropy ships SQLite+FTS5 facts.

## #8 Cost governance — PARTIAL (post-hoc visibility; gate is DIY)

**RUN-VERIFIED** by `cmd/costobs/cost_obs_demo.go` (`go run ./cmd/costobs`,
exit 0) against the real runner + session service.

- **Per-call usage — BUILT-IN visibility.** `model.LLMResponse.UsageMetadata`
  (`*genai.GenerateContentResponseUsageMetadata`: `PromptTokenCount`,
  `CandidatesTokenCount`, `TotalTokenCount`, `ThoughtsTokenCount`,
  `CachedContentTokenCount`) is a real ADK field, and `session.Event` **embeds
  `model.LLMResponse`**, so `event.UsageMetadata` rides every event. The runner
  also stamps it on the `generate_content` span as `gen_ai.usage.input_tokens`
  / `gen_ai.usage.output_tokens` (see #9/#10). The demo reads it off the event
  stream per call — no reflection, no wrapper. *(demo counts are synthetic
  `len(text)/4`; the field plumbing is the real framework.)*
- **Per-tenant aggregation — DIY.** ADK has **no tenant** and no cost meter;
  the demo's `costMeter` (map keyed by tenant) is entirely developer-owned.
  Verified: tenantA and tenantB each totalled 33 in / 27 out tok over 2 turns.
- **Pre-spend budget gate — DIY (no ADK primitive).** ADK has no equivalent of
  Pydantic AI `UsageLimits.check_before_request`; usage is **post-hoc** only.
  The demo implements the gate and **proves the model does not run on rejection**
  via a call-counter on the fake model: the allowed turn moves `Calls` 4→5, the
  rejected (over-budget) turn leaves it 5→5 (unchanged) — the runner is never
  invoked. This is the one aisotropy constitutional gate ADK cannot supply.
- **AISOTROPY COMPARISON:** aisotropy rejects/degrades **before** spending
  tokens (token-bucket, constitutional gate) + a per-tenant meter, all built-in.
  ADK gives you the *numbers* (built-in, per call) but the *tenant dimension*,
  *aggregation*, and *pre-spend gate* are all developer code. ADK measures;
  aisotropy measures **and** gates.

## #9 Observability — BUILT-IN (OTel gen_ai spans; tenant_id is DIY)

**RUN-VERIFIED** by `cmd/costobs/cost_obs_demo.go` — installs an in-memory OTel
`tracetest.SpanRecorder` on the **global** tracer provider and captures the
spans ADK emits (ADK's `internal/telemetry` acquires its tracer from
`otel.GetTracerProvider()`, a delegating global, so a runtime
`otel.SetTracerProvider` captures it — no ADK config needed).

- **Spans emitted — BUILT-IN.** One traced turn produced real ADK spans:
  `invoke_agent orbit_support` and `generate_content <model>`, carrying
  `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.agent.name/description`,
  `gen_ai.conversation.id` (= the ADK sessionID), `gen_ai.response.finish_reasons`,
  and the usage attributes `gen_ai.usage.input_tokens` /
  `gen_ai.usage.output_tokens` / `gen_ai.usage.cache_read.input_tokens` /
  `gen_ai.usage.reasoning.output_tokens`. Free, self-hostable (OTLP HTTP
  exporters are already in the dep graph).
- **tenant_id on spans — DIY.** ADK's own spans carry **no `tenant.id`** (the
  demo confirms: `tenant.id present on ADK's own spans natively = false`) — ADK
  has no tenant concept. The demo enriches DIY by wrapping the turn in an
  `orbit.turn` parent span with a `tenant.id` attribute; only that developer
  span carries it. `gen_ai.conversation.id` exposes the *session* key, not a
  tenant, so per-tenant trace filtering is developer-added.
- **Hosted APM (e.g. LangSmith) — PAID/optional.** Not required: the OTel path
  is fully self-hostable.
- **AISOTROPY COMPARISON:** the dimension where ADK ties aisotropy — both emit
  OTel `gen_ai.*`. Differentiates ADK vs Eino (proprietary APM) and Genkit
  (proprietary schema). aisotropy's spans carry `tenant_id` by construction
  (it's in `context.Context`); on ADK it's a developer enrichment.

## #10 Sovereignty / licensing

- Apache 2.0 (Google). Self-hostable core (in-mem/sqlite session + memory);
  the rich memory/session backends are Vertex-tethered. Google ownership +
  the "works standalone, works best on Vertex" gradient is a buyer
  consideration for non-Google shops.

## #11 Language — Go

- Idiomatic Go 1.25 (`iter.Seq2`, generics-free public surface). Go trails the
  Python line in release cadence.

## Comprehensive feature parity — RUN-VERIFIED (`go run ./cmd/parity`, exit 0)

`cmd/parity/feature_parity_demo.go` walks **25 executable features** from
`arenas/ARENA-SPEC-v2.md` and EXECUTES the real ADK stack (real `llmagent` +
`runner` + `session.Service` + `memory.Service` + `tool.Toolset`; only the model
is faked via the framework's own `model.LLM` seam) to justify each class. Every
`[BUILT-IN]`/`[PARTIAL]` is an invoked ADK code path; DIY/NOT-POSSIBLE/PAID show
the closest ADK primitive. Synthetic token counts (`len/4`) and keyword-set
"embeddings" are labeled; no API key, no network.

**Tally: 6 BUILT-IN · 4 PARTIAL · 1 PAID · 9 DIY · 5 NOT-POSSIBLE (25 total).**

| # | Feature | Class | Run-verified evidence (real ADK path) |
|---|---|---|---|
| 1 | Temperature hierarchy (tiers of nature) | PARTIAL | 2 real tiers — `session.Service` (working KV) + `memory.Service` (keyword recall); aisotropy has 4 (KV/FTS5/vector/batch) |
| 2 | Working memory (recent history) | BUILT-IN | turn 2 recalled turn 1's memo via runner-assembled session history |
| 3 | Facts / keyword search (FTS-like) | PARTIAL | `memory.SearchMemory` word-intersection matched on `launch` — **UNRANKED, session-derived, no FTS5/importance journal** |
| 4 | Semantic / vector RAG | PAID | keyword store MISSED the synonym query `rocket ignition` (0 hits); vectors only via `memory/vertexai` MemoryBank (managed, not OSS) |
| 5 | Summary buffer (context engineering) | DIY | no ADK condensation (`IncludeContents` only toggles history on/off); DIY summarizer |
| 6 | Scoring (Rel×Imp×Rec, top-K) | NOT-POSSIBLE | `memory.Entry` has **no score field**; `SearchMemory` is unordered set-intersection |
| 7 | Contradiction detection / supersede | NOT-POSSIBLE | no supersede/archival in `memory.Service`; DIY override |
| 8 | Knowledge ingestion (chunk+embed) | DIY | `AddSessionToMemory` ingests **sessions, not docs**; no chunk/embed — DIY wraps a doc as a synthetic session |
| 9 | BYO embedder (swap embedding fn) | NOT-POSSIBLE | in-memory recall is keyword (no embedder); Vertex path = managed embeddings, not swappable |
| 10 | Fail-closed reflection loop (502) | DIY | `llmagent.Config.OutputSchema` is BUILT-IN but there is **no repair/retry loop**; DIY retries to valid JSON |
| 11 | SSE streaming + immediate cancel | BUILT-IN | `RunConfig.StreamingModeSSE` delivered 8 partial token events; a cancelled ctx cut the upstream model to 0 tokens produced |
| 12 | Response cache (stateless, skip-model) | BUILT-IN | `BeforeModelCallbacks` returned a cached `LLMResponse`; `model.Calls` unchanged on repeat (framework skips the model) |
| 13 | Scope-gated tools (governance by absence) | PARTIAL | `tool.FilterToolset`+`AllowedToolsPredicate` (BUILT-IN) hid `write_tasks`; **scope→identity binding is DIY** (ADK has no scope) |
| 14 | Multi-step agent loop (tool call) | BUILT-IN | runner ran 2 model steps: `FunctionCall get_weather` → real tool exec → final text |
| 15 | Typed tools + MCP | BUILT-IN | `functiontool.New[T]` inferred JSON-Schema `params=[city]` from the Go struct; `tool/mcptoolset` consumes MCP natively |
| 16 | Bearer-token → identity binding | NOT-POSSIBLE | runner accepted a forged `userID` verbatim (no binding); a bearer→identity gate is entirely DIY middleware |
| 17 | Per-tenant PRE-SPEND quota gate | DIY | no ADK primitive (cost is post-hoc); DIY gate rejects over-budget tenant **before** `runner.Run` (model never runs) |
| 18 | Circuit breaker (fast-fail) | DIY | no ADK primitive; DIY breaker opens after 5 failures then fast-fails |
| 19 | Quota-pressure degradation (cheap model) | DIY | no ADK primitive; DIY router picks a cheap model at ≥95% pressure |
| 20 | Cost meter (per-tenant) | PARTIAL | per-call `event.UsageMetadata` BUILT-IN; **per-tenant aggregation is DIY** (ADK has no tenant) |
| 21 | OTel `gen_ai` spans | BUILT-IN | captured ADK spans with `gen_ai.*` attrs via the global tracer; `tenant_id` native on ADK spans = false (DIY enrich) |
| 22 | Health / readiness probe | DIY | no ADK health primitive; DIY read-only probes of session+memory tiers (no LLM spend) |
| 23 | Product evaluation (P@k / judge) | DIY | no ADK eval harness; DIY P@1 over `memory.SearchMemory` |
| 24 | Transactional outbox (atomic msg+intent) | NOT-POSSIBLE | `session.AppendEvent` is single-store; **no atomic message+event txn across stores, no outbox worker / cold-boot recovery** |
| 25 | Async consolidation (dedup / sleep) | DIY | no ADK sleep primitive (only `AddSessionToMemory` ingest); DIY dedup |

**Biggest gap vs aisotropy (memory tiers):** ADK Go ships **two** memory tiers of
different nature — `session.Service` (hot working KV) + `memory.Service` (warm
keyword recall) — and nothing else. There is **no self-hostable vector tier**
(vectors are Vertex MemoryBank, PAID/managed), **no FTS5 facts journal**, **no
importance/recency scoring**, **no supersede**, **no transactional outbox**, and
**no sleep-cycle consolidation**. aisotropy's 4-tier temperature hierarchy
(BadgerDB µs / SQLite+FTS5 / cosine vectors / DataFusion batch) collapses here to
2 tiers + a large DIY surface (9 DIY + 5 NOT-POSSIBLE of 25).

## Summary

ADK Go is a **real, capable orchestration runtime**: structured output, tools
(MCP/A2A), SSE, session/memory backends, and OTel `gen_ai` observability are
all first-class and verified here against source. Its isolation is
**convention with no tenant binding** — the composite key is real construction,
but nothing supplies or enforces a tenant dimension, so the leaky path is one
line shorter than the correct one.

**vs. aisotropy:** ADK wins on protocol breadth (MCP, A2A) and ties on OTel;
aisotropy wins on isolation-by-construction, self-hostable tiered memory,
pre-spend cost governance, and sovereignty (no Vertex gradient).
