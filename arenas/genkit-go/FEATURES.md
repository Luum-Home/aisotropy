---
type: Feature matrix
title: "Genkit Go — features vs aisotropy (real integration)"
description: Coverage map of Firebase Genkit Go v1.10.0 capabilities compared to aisotropy's built-ins, verified against a real go get + build + offline smoke.
tags: [arenas, genkit-go, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Genkit Go — Features

> Framework: `github.com/firebase/genkit/go` **v1.10.0** (Apache-2.0). Every "BUILT-IN" claim below
> was exercised offline against the real framework via a fake model registered with
> `genkit.DefineModel` — see `orbit/` and the passing `go test ./orbit/`.

Coverage classes: **BUILT-IN** (framework ships it) · **CONVENTION** (works only if the developer
follows an unenforced discipline) · **DIY** (you build it yourself / BYO) · **PAID** (requires a
paid/hosted service) · **NOT-POSSIBLE** (no path).

## What Genkit Go does genuinely well (steelman, run-verified)

Before the gap analysis: Genkit Go is a **strong, general-purpose AI application library** with real
strengths. The 11-point checklist below is scored against *aisotropy's multi-tenant-runtime thesis*,
which is deliberately narrow — it is not a verdict on Genkit's overall quality. Three headline
strengths, each **exercised offline against the real v1.10.0 API** in `cmd/strengths`
(`go run ./cmd/strengths`, exit 0):

- **Flows — BUILT-IN, excellent.** `genkit.DefineFlow` / `genkit.DefineStreamingFlow` (genkit/genkit.go)
  turn a function into a named, **observable, composable, deployable** unit: `core.Flow.Run` /
  `core.Flow.Stream` (core/flow.go). Proven: a flow composing another flow ran and **auto-emitted
  OTel spans** on Genkit's own `tracing.TracerProvider` with zero extra wiring; a streaming flow
  delivered progressive chunks. Flows are the unit Genkit deploys as HTTP handlers and shows in its
  Dev UI — a genuine DX advantage.
- **Dotprompt — BUILT-IN, excellent.** `genkit.LookupPrompt` + `ai.Prompt.Execute` + `ai.WithInput`
  (ai/generate.go). The model, handlebars template, and **typed input schema** live in a `.prompt`
  file (`orbit/prompts/orbit_support.prompt`), not Go source — hot-editable, no recompile. Proven:
  executed with two different input structs through the same file.
- **Model-agnostic provider abstraction — BUILT-IN, excellent.** `genkit.DefineModel` /
  `genkit.LookupModel` + the `ai.Model` interface + `ai.WithModelName`. Every provider (googlegenai,
  openai, anthropic, ollama, ...) is a registry entry selected by name; **caller code is
  provider-independent**. Proven: one identical `genkit.Generate` call, changing only the model-name
  string, dispatched to two distinct providers with different output.

**Honest niche statement:** Genkit's flow + dotprompt + deployment DX (Firebase/Google-backed) is
excellent for building and shipping AI features. aisotropy is a **self-hosted, multi-tenant Go
runtime** — a *different* niche. They overlap on exactly one axis (isolation-by-default), covered in
§1. Everything below is scored on that axis; it does not diminish the strengths above.

## The 11-point checklist

### 1. Multi-tenant isolation — CONVENTION (memory primitive exists, but it is tenant-unaware)
- **Mechanism:** developer-passed session key, fed to `ai.WithMessages`. Genkit-Go v1.10.0 **does**
  ship a session-memory primitive — the **experimental** `ai/exp` Agents API with a pluggable
  `SessionStore` and a shipped in-memory `localstore.NewInMemorySessionStore` (July-2026 Agents API
  preview; gated behind `requireExperimental`, "may change"). But it is keyed by a developer-chosen
  session id with **no** tenant/user/product binding — its `SessionSnapshot`/`SessionState` carry
  only a `SessionID` field. It gives you **persistence, not isolation**. There is no tenant-scoped
  `Session`/`Memory`/`Thread` type; multi-tenancy stays developer-built. (JS-vs-Go asymmetry:
  Genkit-**JS** has had stable-ish `ai.chat()`/`Session`/`SessionStore` longer; Go reached parity
  only via this preview/beta.)
- **Proven — twice:** `TestIsolationLeak` seeds tenantA's secret and reads it back from a `tenantB`
  identity through the real `genkit.Generate` (via a DIY stand-in store with identical semantics);
  **and** `TestRealSessionStoreIsTenantUnaware` reproduces the same cross-tenant read against
  Genkit's REAL `localstore.NewInMemorySessionStore` — no DIY store in the path — because the store's
  only lookup key is the session id.
- **AISOTROPY:** isolation by construction — ctx-propagated tenant + per-key/per-statement scoping;
  the unscoped read is unrepresentable (`Scope{}` has unexported fields).

#### 1b. Third isolation axis — product_id — CONVENTION (run-verified `cmd/productiso`)
- **Run:** `go run ./cmd/productiso` (from `arenas/genkit-go`). Exit **0**. Offline, real
  `genkit.Generate`. Closes the gap that the earlier smoke only stressed tenant/user;
  aisotropy scopes memory by **(tenant, user, session, product_id)** and the product axis
  (two products of the *same* tenant+user — e.g. alice@acme's `orbit-crm` vs `orbit-helpdesk`)
  was never tested here.
- **Mechanism:** Genkit's shipped memory primitive (the experimental `ai/exp` SessionStore) is
  keyed by a session id alone and has no product/tenant concept, so `product_id` is just another
  developer-passed string. Nothing folds it into the store key; the framework binds it to
  **nothing**.
- **Proven — positive control:** `orbit-crm` seeds a marker and reads it back through the real
  pipeline (`✓ POSITIVE CONTROL: orbit-crm read its own marker back through genkit.Generate`).
- **Proven — CROSS-PRODUCT LEAK (label it a leak):** with the common `tenant:user:session` key
  (product axis absent), a same-tenant+user request for a **different** product
  (`orbit-helpdesk`) reads `orbit-crm`'s secret — the real model returns it verbatim
  (`✗ CROSS-PRODUCT LEAK: a orbit-helpdesk request (same tenant+user) read orbit-crm's secret`).
  Scenario 2 shows that folding product into the key *isolates* — but purely by convention:
  mis-passing the product string (`orbit-crm`'s key on an `orbit-helpdesk` request) re-opens the
  leak, and Genkit validates nothing (`✗ CONVENTION LEAK`).
- **AISOTROPY:** `product_id` rides in `context.Context` alongside tenant/user/session and is
  bound into the memory `Scope` by construction — a cross-product read is unrepresentable, not a
  discipline the developer must remember. Genkit has no product primitive to enforce at all.

### 2. Structured output — BUILT-IN
- **Mechanism:** `genkit.GenerateData[Out]` sets the output format to JSON, injects the schema,
  and validates the model response against it (rejects non-conforming JSON — observed live).
- **Proven:** `Classify("...refund...charged twice")` → `{category: billing, priority: high}`.
- **Caveat vs aisotropy:** Genkit validates and errors on a schema miss, but there is **no built-in
  automatic repair loop**. aisotropy's Gollem reflection loop re-prompts with the exact typing
  error until it fixes (fail-closed with self-repair).

### 3. Tools — BUILT-IN
- **Mechanism:** `genkit.DefineTool[In,Out]`; the model emits an `ai.ToolRequest` part and
  Genkit's `Generate` loop executes the Go function and re-invokes the model with the result.
- **Proven:** "Where is my order #4821?" → real tool round trip → "order 4821 is shipped…".
- **AISOTROPY:** `WithTypedTool[T]` + scope-gated tool injection (only permitted tools are exposed
  per tenant scope).

### 4. Streaming (SSE) — BUILT-IN
- **Mechanism:** `ai.WithStreaming(callback)`; the fake model streams word-by-word chunks through
  the real callback. The live server relays them as SSE `data:` frames.
- **AISOTROPY:** Huma SSE with `ctx` cancellation that stops the upstream LLM call immediately.

### 5. Working memory — BUILT-IN (experimental), tenant binding DIY
- **Mechanism:** Genkit-Go v1.10.0 ships an **experimental** session store — `ai/exp` `SessionStore`
  + the in-memory `localstore.NewInMemorySessionStore` (persists/replays `Messages` across turns;
  Firestore via plugin), part of the July-2026 Agents API preview (gated behind
  `requireExperimental`, "may change"). It is exercised in `cmd/parity` (feature #2) and
  `TestRealSessionStoreIsTenantUnaware`. What stays **DIY** is the tenant/user/product binding: the
  store is keyed by a bare session id. This arena keeps a minimal DIY stand-in (`orbit/store.go`)
  for the end-to-end leak path — identical semantics. Durable execution is a **third-party** plugin
  (`genkitx-temporal`).
- **AISOTROPY:** BadgerDB working memory by default (µs, in-process), doubling as the outbox — and
  bound to `(tenant, user, session, product)` from ctx by construction.

### 6. Semantic memory (vectors) — DIY
- **Mechanism:** Genkit ships retriever/indexer *interfaces* and plugins (e.g. pgvector), but no
  in-tree vector store; concrete storage is BYO/plugin.
- **AISOTROPY:** in-process cosine (active) + Qdrant gRPC with tenant-payload filtering during scan.

### 7. Facts memory (journal) — DIY
- **Mechanism:** no equivalent journal/FTS abstraction.
- **AISOTROPY:** Engram-style SQLite+FTS5 facts journal.

### 8. Cost governance — split verdict: usage visibility BUILT-IN, everything else DIY
- **Run-verified** offline against v1.10.0 in `cmd/costobs` (`go run ./cmd/costobs`):
  - **Per-call usage — BUILT-IN visibility.** `genkit.Generate` returns `resp.Usage`
    (`*ai.GenerationUsage` with `InputTokens`/`OutputTokens`/`TotalTokens`, plus characters,
    images, cached, thoughts). The framework carries the field on every response — proven by
    reading it back per turn (token counts in the demo are SYNTHETIC, `len(text)/4`; the point
    is the framework surfaces the field, not the arithmetic).
  - **Per-tenant aggregation — DIY.** `ai.GenerationUsage` is per-call and has *no* tenant
    concept. The demo hand-rolls a `costLedger` keyed by `tenant_id` (2 turns tenantA + 2
    tenantB → per-tenant token/cost totals). Nothing in Genkit does this for you.
  - **Pre-spend gate — DIY (no framework API).** Re-verified: Genkit Go ships **no**
    usage-limit/budget option. The only knob is `ai.WithMaxTurns`, a tool-loop *iteration* cap
    that **errors after spending**, not a per-tenant token budget checked *before* the call. The
    demo implements a DIY gate that checks a per-tenant token counter before `genkit.Generate`
    and **proves the model is never invoked on a rejected turn** via a call-counter on the fake
    model (`orbit.ModelCallCount()`: `calls 1->1, delta=0` on the over-budget turn).
  - **Pricing (tokens → $) — DIY.** No price table in the framework.
- **vs. Pydantic AI:** that framework's `UsageLimits` runs a check *before* the request; Genkit
  Go has no equivalent.
- **AISOTROPY:** pre-spend token-bucket quota gate (reject/degrade before spending) + per-tenant meter.

### 9. Observability — BUILT-IN transport, PROPRIETARY schema, DIY per-tenant tag
- **Run-verified** offline in `cmd/costobs`: attach a plain
  `tracetest.NewInMemoryExporter()` to `tracing.TracerProvider()` (the same
  `*sdktrace.TracerProvider` Genkit writes to) — no framework patching — and run ONE turn. It
  captured **7 real spans**: root `orbitChat`, two `generate`, two `fake/orbit` model-action
  spans, and the `lookupOrder` tool span (plus our own `orbit.turn`).
- **Schema is proprietary, not semconv.** Every captured attribute key is `genkit:*`:
  `genkit:name`, `genkit:input`, `genkit:output`, `genkit:path`, `genkit:state`, `genkit:type`,
  `genkit:isRoot`, `genkit:metadata:flow:name`, `genkit:metadata:subtype`. **Confirmed at
  runtime: zero `gen_ai.*` semantic-convention keys.**
- **tenant_id on spans — DIY.** Genkit puts **no** tenant attribute on its spans. Tenant identity
  only survives as an opaque JSON blob buried inside `genkit:input` (the serialized flow input).
  To get a queryable `tenant_id` dimension the demo sets it itself on a wrapping `orbit.turn`
  span — labeled DIY.
- **Hosted backend — PAID/OPTIONAL:** Firebase/Google Cloud telemetry (or any OTLP collector) is
  a separate service; local in-memory capture needs none of it.
- **AISOTROPY:** OTel `gen_ai.*` semconv-aligned (ADR-0010) — market-standard *and* ahead of Genkit.

### 10. Sovereignty / licensing — BUILT-IN (good), run-verified against aisotropy's gate
- Apache-2.0, Go-style stability promise, fully self-hostable. No SaaS lock-in for the core.
- **Executable governance (`scripts/license_scan.sh`, exit 0).** aisotropy's constitutional gate
  is "the stack is deliberately 100% permissive (MIT/BSD/Apache-2.0/ISC) ... no copyleft or
  source-available/BUSL without an explicit ADR." Scanned THIS arena's **actual** dependency tree with
  `go-licenses report ./...` (the REAL tool output, not a guess):
  - **24 external deps** — **24 permissive, 0 copyleft, 0 restricted/source-available, 0 unclassified.**
  - Breakdown: **13 Apache-2.0 · 6 MIT · 4 BSD-3-Clause · 1 ISC.**
  - **No non-permissive package found** — no GPL/LGPL/AGPL/MPL, no BUSL/SSPL/ELv2/Commons-Clause.
  - The 6 `genkit-go-arena/*` "Unknown" rows go-licenses prints are the arena's OWN packages
    (no LICENSE file, correctly), excluded from the external tally.
  - **Verdict: PASS** — Genkit Go's real dep tree HOLDS the permissive line aisotropy claims;
    the whole framework (incl. `firebase/genkit/go` Apache-2.0, the `xeipuuv/*` JSON-schema stack
    Apache-2.0, OpenTelemetry Apache-2.0, `dotprompt` Apache-2.0) stays inside the gate.

### 11. Language — Go
- Idiomatic, generics-heavy, strongly typed. Same host language as aisotropy's core.

## Comprehensive feature-parity walk (run-verified, `cmd/parity`)

> Run: `go run ./cmd/parity` (from `arenas/genkit-go`). Exit 0. One runnable file
> (`cmd/parity/feature_parity_demo.go`) walks 25 features from `arenas/ARENA-SPEC-v2.md`, each
> **exercised** against the real framework (v1.10.0) fully offline, printing
> `FEATURE <n> <name>: [CLASS] <evidence>`. Deeper investigation of v1.10.0 for this walk found
> two framework surfaces the 11-point checklist above understated — corrected inline below.

**Run-verified tally: BUILT-IN=6 · PARTIAL=5 · DIY=13 · NOT-POSSIBLE=1** (25 features).

| # | Feature | Class | Run evidence (offline, real framework) |
|---|---|---|---|
| 1 | Tiered memory by access temperature | **NOT-POSSIBLE** | ships 1 session-snapshot store + 1 (plugin) vector store; no FTS tier, no offline-batch tier → not 4 distinct-nature tiers |
| 2 | Working memory (recent history) | **BUILT-IN** (experimental) | `ai/exp` `SessionStore` + `localstore.NewInMemorySessionStore` persisted+replayed 4 msgs across turns; other session id → nil. Tenant binding is DIY |
| 3 | Facts memory (keyword/FTS) | DIY | no FTS/journal primitive; hand-rolled index |
| 4 | Semantic memory (vector RAG) | **BUILT-IN** (plugin) | official `plugins/localvec` cosine store: `DefineRetriever`+`Index`, query hit the right doc, `ai.WithDocs` fed it to `Generate`. Tenant payload-filter is DIY |
| 5 | Summary / rolling buffer | DIY | no summarizer; `SessionState.Custom` is just a slot you fill |
| 6 | Importance×recency×relevance scoring | DIY | no scoring primitive; hand-rolled composite |
| 7 | Contradiction detection / supersede | DIY | no supersede primitive |
| 8 | Knowledge ingestion (chunk+embed) | PARTIAL | framework embedder + `localvec.Index` embedded chunks; the chunker is DIY |
| 9 | BYO embedder (swappable) | **BUILT-IN** | `genkit.DefineEmbedder` is a first-class action; two embedders index the same corpus, each owning its vector space → different top-1 (proven) |
| 10 | Fail-closed reflection loop | PARTIAL | `GenerateData` **rejected** the schema break (framework error); the retry/repair loop is DIY |
| 11 | SSE streaming + immediate ctx-cancel | **BUILT-IN** | `ai.WithStreaming`; cancelling ctx after 3 chunks stopped the model mid-stream (model observed cancel; not all 30 tokens) |
| 12 | Stateless response cache | DIY | no cache primitive; DIY (tenant,user)-keyed cache; hit made 0 model calls |
| 13 | Scope-gated tools | DIY | tools are BUILT-IN but there is **no scope concept**; gating = you choose the `WithTools` subset; a call to an un-offered tool is framework-rejected |
| 14 | Multi-step agent loop | **BUILT-IN** | `genkit.Generate` ran tool→model automatically; `ai.WithMaxTurns` caps it |
| 15 | Typed tools + MCP | **BUILT-IN** | `DefineTool[In,Out]` compile-time typed (ran); MCP ships as official `plugins/mcp` (`NewMCPHost`) — not exercised offline |
| 16 | Bearer token → identity binding | DIY | framework core binds **no** identity; DIY middleware (401 deny / 403 must-agree) |
| 17 | Per-tenant PRE-SPEND quota gate | DIY | no pre-spend API (`WithMaxTurns` errors *after* spending); DIY gate rejected over-budget turn with 0 model calls |
| 18 | Circuit breaker | DIY | no breaker; DIY opened after 5 failures, 6th short-circuited (model invocations=5) |
| 19 | Quota-pressure model degradation | DIY | no degradation hook; DIY router picks cheap model at pressure ≥0.95 |
| 20 | Cost meter (per-tenant) | PARTIAL | `resp.Usage` BUILT-IN per call; per-tenant meter DIY |
| 21 | OTel spans | PARTIAL | BUILT-IN transport (7 spans); schema **proprietary** (`genkit:*`, 0 `gen_ai.*` semconv); `tenant_id` only via DIY tag |
| 22 | Health / readiness probe | DIY | no framework probe endpoint; DIY read-only tier probe |
| 23 | Product evaluation (IR / judge) | PARTIAL | `genkit.DefineEvaluator`+`genkit.Evaluate` runner BUILT-IN (scored a dataset); IR P@k/MRR + LLM-judge DIY (plugin ships only DEEP_EQUAL/REGEX) |
| 24 | Transactional outbox (atomic msg+event) | DIY | no outbox; `SaveSnapshot` gives single-snapshot atomicity only; cross-store atomic write is DIY |
| 25 | Async consolidation (offline dedup) | DIY | no sleep-cycle primitive; DIY offline dedup |

### Corrections to the earlier draft of this arena
- **Working memory / "no memory primitive at all":** the earlier draft's headline was FALSE.
  v1.10.0 ships an **experimental** Agents/Session API (`genkit/exp` + `ai/exp`) with a pluggable
  `SessionStore` — an in-memory store is shipped (`localstore.NewInMemorySessionStore`), Firestore
  via plugin. It persists conversation `Messages` + custom state and is exercised here (§5, `cmd/parity`
  feature #2, and `TestRealSessionStoreIsTenantUnaware`). It is still keyed by a **developer-passed
  session id with no tenant/user/product binding**, so it is persistence, not isolation, and the
  leak stands against the real primitive; it is gated behind `requireExperimental` ("may change").
- **Semantic memory (§6 above said DIY):** Genkit ships an **official in-tree offline vector store**,
  `plugins/localvec` (cosine, on-disk), plus first-party `pinecone`/`weaviate`/`postgresql`/`alloydb`
  plugins. The demo drives `localvec` end-to-end offline, so vector RAG is BUILT-IN (via plugin);
  what stays DIY is the **tenant/user payload filter during the scan** (aisotropy's isolation gate).

## Summary

Genkit Go is a **general-purpose AI application library** (Google's own framing). Its generation
primitives are genuinely strong and shipped: structured output, tool-calling, streaming, dotprompt,
an offline vector store (`localvec`), an experimental session store, and an evaluator runner all
work offline in this arena against the real framework. Where it is thin is exactly aisotropy's
thesis: **no tiered memory of different nature, no cost/quota governance gates, and — critically —
no tenant-isolation primitive**. Multi-tenancy is a discipline you must build and remember, and the
leaky path costs the same as the correct one.

**vs. aisotropy:** Genkit wins on breadth of shipped generation features and Firebase-ecosystem
fit; aisotropy wins on isolation-by-construction, the complete built-in memory/cost stack, and
OTel `gen_ai` semconv compliance.
