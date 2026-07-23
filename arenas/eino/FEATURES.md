---
type: Feature matrix
title: "Eino — features vs aisotropy (REAL integration)"
description: Coverage map of the real CloudWeGo Eino v0.9.12 framework against the 11-point Orbit checklist, with coverage classes.
tags: [arenas, eino, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Eino — Features (verified against v0.9.12)

Coverage classes: **BUILT-IN** (framework ships it, works out of the box) · **CONVENTION** (framework
gives a primitive but scoping/correctness is the developer's responsibility) · **DIY** (you must build
it yourself) · **PAID** (only via a paid/proprietary product) · **NOT-POSSIBLE** (framework cannot
provide it by construction).

Everything below was checked against the real source in the module cache
(`github.com/cloudwego/eino@v0.9.12`) and exercised where marked "exercised in smoke/tests".

## 1. Multi-tenant isolation — **NOT-POSSIBLE** (by framework)

- **Real API:** `compose.CheckPointStore { Get(ctx, id string) ([]byte,bool,error); Set(ctx, id string, []byte) error }` + `compose.WithCheckPointID(string)` — a human-in-the-loop interrupt/resume KV, not a memory tier.
- **Finding:** the persistence key is a single developer-supplied string. No tenant/user dimension
  exists anywhere in the interface, and there is **no default implementation**. Per-node state
  (`WithGenLocalState`) carries only the fields the developer declares. Eino is an orchestration
  library with no tenancy primitive — the same category as LangGraph or Pydantic AI.
- **Proof (exercised):** `orbit/memory.go` builds a real interrupt/checkpoint graph; `TestIsolationSharedIDCollapses`
  seeds tenant A under `"shared-session"` and shows that when tenant B composes the same bare ID the
  keys collapse and the store returns the seeded value — driven by Eino's real runtime. This is what
  any string-keyed KV does (including aisotropy's BadgerDB); the classification is NOT-POSSIBLE
  because Eino has no tenant dimension to catch a dropped segment, not because Eino "leaks".
- **AISOTROPY:** isolation by construction — the scope key is composed from ctx (per-statement/key
  scoping), so a wrong/short key is unrepresentable. No string-keyed store can catch a bad key
  after the fact; aisotropy's edge is that the developer never composes the key by hand.

## 2. Structured output + retry — **DIY / partial**

- **Real API:** `schema.NewMessageJSONParser` / provider JSON mode; `react.AgentConfig` has no
  fail-closed schema-repair loop.
- **Finding:** you can parse structured output, but there is no built-in "schema violation → bounded
  reflection retry" loop. That repair loop is DIY.
- **AISOTROPY:** default/runtime-wired fail-closed reflection loop (Gollem + homegrown fallback).

## 3. Tool calling — **BUILT-IN**

- **Real API:** `components/tool.InvokableTool`, `components/tool/utils.InferTool[Req,Resp]`,
  `compose.ToolsNodeConfig`, `flow/agent/react.NewAgent`.
- **Proof (exercised):** `orbit/orbit.go` registers a real `lookup_order` tool via `InferTool` and
  runs it through `react.NewAgent`. `TestOrbitAgentTool` asserts the tool result is folded into the
  final answer. Type-safe: the request struct is reflected into a `schema.ToolInfo`.
- **AISOTROPY:** equivalent to `WithTypedTool[T]`.

## 4. Streaming — **BUILT-IN**

- **Real API:** `model.BaseChatModel.Stream(...) (*schema.StreamReader[*schema.Message], error)`;
  the compiled `compose.Runnable` also exposes `Stream()`.
- **Proof (exercised):** `orbit.Stream()` iterates a real `*schema.StreamReader`; `TestOrbitStream`
  asserts multiple chunks. The HTTP server exposes `/chat/stream`.
- **AISOTROPY:** equivalent to Huma SSE + `ctx` cancellation.

## 5. Working memory — **CONVENTION**

- **Real API:** `compose.CheckPointStore` + `WithCheckPointID` (state persisted at interrupts);
  AutoMemory middleware exists in eino-ext (event-log style).
- **Finding:** persistence works, but it is keyed by a bare developer string (see #1). Orbit also
  keeps an app-level `workingMemory map[string][]string` because the developer must own the keying.
- **AISOTROPY:** BadgerDB (µs) working-memory tier, tenant-scoped by construction.

## 6. Semantic memory (vectors) — **DIY**

- **Finding:** not in core. Retrieval/vector-backed memory middleware is proposed in issue #995
  (opened 2026-04-24), not implemented. You wire your own vector store.
- **AISOTROPY:** in-process cosine (active) + Qdrant gRPC (code-only), tenant-filtered during scan.

## 7. Facts memory (journal) — **DIY**

- **Finding:** no built-in facts/journal store. DIY on top of any DB.
- **AISOTROPY:** SQLite + FTS5 facts journal out of the box.

## 8. Cost governance — **BUILT-IN visibility / DIY gate + aggregation**

- **Real API:** `schema.Message.ResponseMeta.Usage` (`*schema.TokenUsage{PromptTokens,
  CompletionTokens, TotalTokens, ...}`) is returned from every `ChatModel` call; the same usage is
  surfaced to callbacks via `model.CallbackOutput.TokenUsage`.
- **Finding (RUN-VERIFIED, `cmd/costobs/cost_obs_demo.go`):** per-call token usage is a **first-class
  framework field** you read straight off the result — [BUILT-IN]. But there is **no tenant
  dimension** (`callbacks.RunInfo` = Name/Type/Component only) and **no pre-spend gate primitive**
  (no equivalent of Pydantic AI's `UsageLimits.check_before_request`); usage is only observable
  *after* the call. So per-tenant **aggregation** and the **reject-before-spend** check are [DIY].
- **Proof (exercised):** the demo runs 2 turns for `tenantA` + 2 for `tenantB` and prints per-tenant
  token/cost totals from a DIY `costMeter`; then sets a tiny 20-token budget for `tenantC`, allows
  turn 1 and **rejects turn 2 before `chain.Invoke`** — proven by a call-counter on the fake model
  that does NOT advance on the rejected turn (`calls 1 -> 1`). Token counts are SYNTHETIC (runes/4),
  clearly labelled; the framework wiring is real.
- **AISOTROPY:** pre-spend quota gate + per-tenant cost meter wired in the runtime (reject/degrade
  before spending) — here both are the developer's responsibility.

## 9. Observability — **BUILT-IN spans / DIY tenant + OTel / PAID backends**

- **Real API:** `callbacks.NewHandlerBuilder().OnStartFn/OnEndFn(...).Build()` + attach via
  `compose.WithCallbacks(h)` (or `callbacks.AppendGlobalHandlers`). Eino fires OnStart/OnEnd/OnError
  around every component (ChatModel, Tool, Graph/Chain); `model.ConvCallbackOutput(out)` yields the
  message whose `ResponseMeta.Usage` carries token counts.
- **Finding (RUN-VERIFIED, `cmd/costobs/cost_obs_demo.go`):** the callback/aspect mechanism is
  **native** — one `chain.Invoke(..., WithCallbacks(h))` captured **2 spans** (`chat_model` /
  `Chain`) with per-component **timings + token usage** into an in-memory exporter [BUILT-IN]. But
  `tenant_id` is **NOT** on spans by construction: `RunInfo` has no tenant/user field. In the demo it
  appears only because we stuffed tenant into `context.Context` and read it in the handler — that
  attribution is [DIY]. Core Eino ships **no native OTel `gen_ai.*` semconv emitter**; turnkey
  targets live in eino-ext (APMPlus/Volcengine, CozeLoop, Langfuse, LangSmith) and are external /
  partly [PAID] SaaS.
- **AISOTROPY:** OTel gen_ai semconv compliant, tenant on every span by ctx propagation — here the
  span plumbing is BUILT-IN but the OTel-semconv + tenant attribution layer is DIY.

## 10. Sovereignty / licensing — Apache-2.0 (with caveat)

- **Finding:** Apache-2.0, self-hostable code. But the framework is ByteDance-operated and its
  primary observability target (APMPlus) is a ByteDance/Volcengine product — a data-residency /
  sovereignty caveat for Western regulated buyers.
- **AISOTROPY:** 100% permissive (MIT/BSD/Apache-2.0/ISC), zero copyleft, Western-operated, fully self-hostable.

## 11. Language — Go

- Same language and runtime class as aisotropy. This makes Eino the most directly comparable
  framework; the architectural difference (orchestration library vs sovereign multi-tenant runtime)
  is the whole story.

## What Eino does genuinely well (STEELMAN — RUN-VERIFIED, `cmd/strengths/eino_strengths_demo.go`)

The checklist above maps where Eino has **no** answer (tenancy, tiered memory, pre-spend gate). The
other half of a fair comparison is where Eino is genuinely strong — its home axis: **type-safe,
composable, streaming graph orchestration in Go**. Eino is ByteDance's production framework
(LangGraph-for-Go); aisotropy is a multi-tenant *runtime*, not a general orchestration library, so on
this axis Eino is the reference and it earns it. Three strengths, each grounded in a real v0.9.12
symbol and executed offline (exit 0):

| Strength | Real v0.9.12 API | Run-verified evidence |
|---|---|---|
| 1. **Type-safe graph + free mode boxing** | `compose.NewGraph[I,O]` · `compose.InvokableLambda[I,O]` · `compose.Runnable[I,O]` | Typed `string→int` graph (`START→parse→double→END`); Go generics **compile-check** every edge. The one compiled `Runnable` served `Invoke` (→`10`) and `Stream` (→`[10]`) with no extra code — author Invoke-only, get `Invoke/Stream/Collect/Transform` all boxed and type-checked. |
| 2. **First-class stream primitives** | `schema.Pipe[T]` · `StreamReader.Copy(n)` · `schema.MergeStreamReaders[T]` | `Copy(3)` → 3 independent readers each saw the full `[1 2 3 4 5]` (framework-managed fan-out buffering); `MergeStreamReaders` folded 2 streams into `[10 20 30 40 50]` exactly-once. Streaming is a native concern, not bolted on. |
| 3. **Callbacks / aspects (AOP seam)** | `callbacks.NewHandlerBuilder().OnStartFn/OnEndFn().Build()` · `compose.WithCallbacks` | One handler, attached at call time, wrapped every component: **3 start / 3 end** spans (`[Graph Lambda Lambda]`) around a 2-node graph, node bodies untouched. This is the seam eino-ext's Langfuse/APMPlus/LangSmith backends plug into. |

**Run:** `go run ./cmd/strengths` (from `arenas/eino/`) — exits 0, prints 3/3.

This is fully consistent with the de-strawman: Eino **is** a genuinely strong Go orchestration library
**and** it has no tenancy/isolation axis. Both are true; neither cancels the other. aisotropy's
differentiator is the narrow-but-decisive axis Eino doesn't play on — isolation-by-default, composed
from `context.Context` so a wrong scope key is unrepresentable.

## Summary

Eino is a genuinely capable **Go-native orchestration library** (LangGraph-for-Go) with real,
type-safe tool calling, streaming, and graph/checkpoint state — all of which this arena exercises
against the real v0.9.12 code. What it does **not** have is any tenancy story: its persistence key is
a bare string with no tenant dimension and no default implementation, so multi-tenant isolation is
**NOT-POSSIBLE by the framework** and must be bolted on by the developer (who can silently get it
wrong, as the smoke test proves).

**vs. aisotropy:** Eino wins on Go-native velocity and CloudWeGo ecosystem fit; aisotropy wins on
isolation-by-construction, temperature-tiered memory, pre-spend cost governance, and sovereignty.

---

# Comprehensive feature-parity walk (RUN-VERIFIED — `feature_parity_demo.go`)

Beyond the 11-point checklist above, `feature_parity_demo.go` exercises **25 of aisotropy's
executable-demo features** (ARENA-SPEC-v2.md) against the real Eino v0.9.12 runtime, fully offline
(stub/counting models, no API key, SYNTHETIC token counts). Each line below is printed by the demo
after the framework actually executed the path.

**Run:** `go run ./feature_parity_demo.go` (from `arenas/eino/`) — exits 0.

**Run-verified tally (25 features):** `BUILT-IN=3 · PARTIAL=4 · CONVENTION=3 · DIY=14 · NOT-POSSIBLE=1`.

| # | Feature | Class | Run-verified evidence |
|---|---|---|---|
| 1 | working-memory | **CONVENTION** | Real `compose.CheckPointStore` round-trip under our `acme:alice:s1` key → recall correct. Persistence is Eino's; the `tenant:user:session` key + recent-buffer are ours. |
| 2 | facts / FTS keyword search | **DIY** | Inverted-scan over `production database` → correct top hit. Eino ships no facts/journal or full-text tier (aisotropy = SQLite FTS5). |
| 3 | semantic RAG | **PARTIAL** | Real `retriever.Retrieve(WithEmbedding,WithTopK)` → `k1` score 0.596. `retriever.Retriever`/`indexer.Indexer`/`embedding.Embedder` are BUILT-IN **interfaces**; the cosine store + tenant filter are ours (no in-core vector DB). |
| 4 | summary / rolling buffer | **DIY** | 8 turns, keep=3 → prompt tokens plateau `[8 15 22 29 29 29 29 29]`. No summary-buffer primitive in Eino. |
| 5 | importance×recency×relevance scoring | **DIY** | 3 facts scored → correct top-1. Eino ranks retrieval by cosine only. |
| 6 | contradiction / supersede | **DIY** | Superseded old→new; stale vector gone. No supersede concept in Eino. |
| 7 | knowledge ingestion (chunk+embed) | **PARTIAL** | Real `document.Transformer` seam split 1 doc→6 chunks → embedded+indexed → retrieved. Interfaces BUILT-IN; splitter/store bodies ours. |
| 8 | BYO embedder | **CONVENTION** | `embedding.Embedder` is a BUILT-IN interface seam; swapped `HashEmbedder(saltA,128)`↔`(saltB,512)` accepted by `retriever.WithEmbedding`. Impl/self-hosting ours. |
| 9 | **tiered temperature hierarchy** | **NOT-POSSIBLE** | The crux. Eino has NO tiered-memory concept — one bare-string checkpoint KV + retrieval seams, one flat nature. The 4 aisotropy tiers were each hand-assembled (working=CONVENTION, facts=DIY, vector=PARTIAL, summary=DIY). |
| 10 | reflection loop (fail-closed) | **DIY** | `schema.NewMessageJSONParser` (BUILT-IN) flags bad JSON; the bad→retry→parsed and all-bad→fail-closed **loop** is ours. Eino has the parser, no repair loop. |
| 11 | SSE streaming + cancel | **BUILT-IN** | Real `schema.Pipe` StreamReader/Writer; ctx cancel after 3 chunks stopped the producer at 3/12. Native. |
| 12 | response cache (stateless) | **DIY** | 2nd identical query = hit (model calls 1→1); cross-tenant `globex` = MISS (key mixes `tenant\0user\0prompt`). No cache in Eino. |
| 13 | scope-gated tools | **CONVENTION** | Granted-scope binds `lookup_order` (used); denied-scope binds `[]` (unused). Governance-by-absence achievable — but binding is the developer's choice, not a framework scope check. |
| 14 | multi-step agent loop | **BUILT-IN** | Real `flow/agent/react.Agent` ran model→tool→model and folded the tool result. Native ReAct loop. |
| 15 | typed tools / MCP | **BUILT-IN** | `utils.InferTool` reflected the Go struct into a `schema.ToolInfo`. Typed tools BUILT-IN; MCP execution is eino-ext (PARTIAL, external). |
| 16 | auth: bearer→identity | **DIY** | HMAC verify + must-agree bind (cross-path token → `tenant-mismatch-403`). Eino has NO auth/identity/transport layer; entirely ours. |
| 17 | pre-spend quota gate | **DIY** | Budget=20: turn-2 rejected **before** `Invoke` (model calls 1→1). Eino exposes usage only AFTER the call. |
| 18 | circuit breaker | **DIY** | 5 failures open the breaker; 6th fast-fails with no model call (5→5). No breaker in Eino. |
| 19 | quota-pressure degradation | **DIY** | Pressure 0.97 (pref=premium) → routed to `cheap` (safety valve wins). No load-aware router in Eino. |
| 20 | cost meter (per-tenant) | **PARTIAL** | `schema.Message.ResponseMeta.Usage` is a BUILT-IN per-call field; per-tenant totals aggregated by our meter (no tenant dimension in Eino). |
| 21 | OTel spans | **PARTIAL** | `callbacks.NewHandlerBuilder`+`WithCallbacks` captured 2 spans w/ timing+usage (BUILT-IN aspect). `tenant_id` on span only via our ctx; no native OTel `gen_ai` emitter (eino-ext backends external/PAID). |
| 22 | health / readiness probe | **DIY** | DIY read-only readiness over facts+vector tiers (no LLM spend). Eino is a library with no server/health endpoint. |
| 23 | product evaluation (IR) | **DIY** | P@2=1.00 R@2=1.00 over our retriever vs a labeled gold set. No eval harness in Eino. |
| 24 | transactional outbox | **DIY** | Atomic msg+event append; crash-before-commit persists nothing; count invariant holds. No transactional store in Eino (aisotropy = BadgerDB ACID). |
| 25 | async consolidation (dedup) | **DIY** | Offline dedup 5→2 unique. No sleep-cycle/consolidation in Eino (aisotropy = DataFusion Rust subprocess). |

**Headline.** Only **3 of 25** are truly BUILT-IN — the multi-step ReAct agent loop, streaming, and
typed tools. The memory story is the decisive gap: the **4-tier hierarchy by access temperature is
NOT-POSSIBLE as a concept** — Eino gives one bare-string checkpoint KV plus retrieval **interface
seams** (no store, no tenant dimension), so every memory tier (working/facts/vector/summary), the
pre-spend gate, circuit breaker, quota-pressure degradation, transactional outbox, auth binding,
health probe, response cache, and offline consolidation all had to be hand-built. Eino is a genuinely
strong **Go orchestration library**; it is not a sovereign multi-tenant runtime, and this walk shows
exactly where the DIY burden falls.

---

# GAP 1 (RUN-VERIFIED) — multi-PRODUCT isolation, the 3rd axis — **NOT-POSSIBLE (by framework)**

aisotropy scopes every memory read/write by the tuple **(tenant_id, user_id, session_id, product_id)**.
The earlier isolation proof only exercised tenant/user; **product_id was never tested**. It is the same
story one axis over — and this closes it, by RUNNING against the real Eino v0.9.12 checkpoint runtime.

- **Real API (unchanged):** `compose.CheckPointStore { Get/Set(ctx, id string) }` + `compose.WithCheckPointID(string)`.
  product_id is **not** a dimension the framework knows about — only a substring the developer concatenates
  into the single checkpoint-ID string.
- **Proof (exercised — `cmd/productiso/product_isolation_demo.go`, exit 0) — both paths at equal weight:**
  - **(A) Correct path (isolated):** `acme/alice/productX` seeds a marker under the fully qualified key
    `"acme:alice:productX:s-42"` and reads its OWN marker back. `acme/alice/productY`, using its OWN
    fully qualified key `"acme:alice:productY:s-42"`, **MISSES** — isolation holds because the two
    products get two different keys. This is the supported, correct usage: Eino ran no product check;
    it compared two distinct strings, which is exactly right for a correctly composed key.
  - **(B) Footgun path (collapses):** a one-line-apart mistake — the dev's key composer drops the product
    segment (`productBlindKey` → `"acme:alice:s-42"`). With product dropped, productX and productY compose
    the **identical** string, so the store returns the seeded value (`SECRET-productX-7b41e9a0c2`). This is
    **not Eino returning the wrong thing** — a string-keyed KV faithfully returns what was stored under the
    queried key, exactly as any such store does (including aisotropy's BadgerDB). Direct store probe:
    keyspace is `[acme:alice:s-42]` — a bare `[]string`, **no product_id column/tag/filter anywhere** in
    `Get/Set(ctx, id string)`, so Eino has no dimension to catch the dropped segment.
- **Verdict:** `product_id` is **CONVENTION-ONLY** — a developer substring the framework binds to nothing.
  Correct keying isolates (A); a dropped segment collapses two products onto one key (B). Identical story to
  the tenant/user axis already shown; the extra key buys zero framework enforcement (it is a DIY
  classification, not an Eino bug).
- **AISOTROPY:** all four axes (tenant/user/session/product) are composed from ctx by construction, so a
  wrong/short key is unrepresentable. No string-keyed store can catch a bad composed key after the fact;
  aisotropy's edge is that the developer never hand-composes it.

**Run:** `go run ./cmd/productiso` (from `arenas/eino/`) — exits 0; prints the correct path isolating and the footgun path collapsing.

# GAP 2 (RUN-VERIFIED) — executable governance = dependency LICENSE posture — **PASS (holds the permissive line)**

aisotropy's constitutional gate is "**100% permissive (MIT/BSD/Apache-2.0/ISC), no copyleft/BUSL/SSPL/ELv2**". This scans Eino's
REAL dependency tree with the actual `github.com/google/go-licenses` tool — executed, not asserted.

- **Tool / command (exit 0):** `go install github.com/google/go-licenses@latest` then
  `go-licenses report ./...`, wrapped in `scripts/license-scan.sh`. Output is the tool's, not a guess.
- **Result — 24 linked third-party packages, license mix:**

  | License | Count | Class |
  |---|---|---|
  | MIT | 11 | permissive |
  | BSD-3-Clause | 6 | permissive |
  | Apache-2.0 | 6 | permissive |
  | BSD-2-Clause | 1 | permissive |
  | **copyleft (GPL/LGPL/AGPL/MPL)** | **0** | — |
  | **restricted/source-available (BUSL/SSPL/ELv2/Commons-Clause)** | **0** | — |

  Full module graph (`go list -m all`) = **77 modules** incl. indirect/test; the linked import graph
  go-licenses classifies is 24. Core `github.com/cloudwego/eino` = **Apache-2.0**. **No non-permissive
  package found** — the `grep -iE 'GPL|LGPL|AGPL|MPL|BUSL|SSPL|ELv2|Commons'` scan returns NONE.
- **Verdict:** Eino's real dep tree is **100% permissive (MIT/BSD/Apache)** and would **pass** aisotropy's
  license gate as-is. (Sovereignty caveat from feature #10 still stands: the code is Apache-2.0 and
  self-hostable, but the framework is ByteDance-operated and its turnkey observability targets are
  Volcengine/ByteDance SaaS — a data-residency concern that is orthogonal to the dependency-license mix.)

**Run:** `./scripts/license-scan.sh` (from `arenas/eino/`) — exits 0, prints the mix + `NONE` for non-permissive.
