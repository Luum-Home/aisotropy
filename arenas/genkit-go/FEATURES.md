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

## The 11-point checklist

### 1. Multi-tenant isolation — CONVENTION (arguably weaker: no primitive at all)
- **Mechanism:** developer-passed session key into a developer-built store (`orbit/store.go`),
  fed to `ai.WithMessages`. Verified: stable Genkit exposes **no** `Session`/`Memory`/`Thread`
  type in the `genkit` or `ai` packages.
- **Proven:** `TestIsolationLeak` seeds tenantA's secret and reads it back from a `tenantB`
  identity through the real `genkit.Generate` — the model returns tenantA's secret.
- **AISOTROPY:** isolation by construction — ctx-propagated tenant + per-key/per-statement scoping;
  the unscoped read is unrepresentable (`Scope{}` has unexported fields).

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

### 5. Working memory — DIY
- **Mechanism:** none built in. You build the store (this arena did, `orbit/store.go`). Durable
  execution is a **third-party** plugin (`genkitx-temporal`).
- **AISOTROPY:** BadgerDB working memory by default (µs, in-process), doubling as the outbox.

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

### 10. Sovereignty / licensing — BUILT-IN (good)
- Apache-2.0, Go-style stability promise, fully self-hostable. No SaaS lock-in for the core.

### 11. Language — Go
- Idiomatic, generics-heavy, strongly typed. Same host language as aisotropy's core.

## Summary

Genkit Go is a **general-purpose AI application library** (Google's own framing). Its generation
primitives are genuinely strong and shipped: structured output, tool-calling, streaming, and
dotprompt all work offline in this arena against the real framework. Where it is thin is exactly
aisotropy's thesis: **no memory tiers, no cost governance, and — critically — no isolation
primitive**. Multi-tenancy is a discipline you must build and remember, and the leaky path costs
the same as the correct one.

**vs. aisotropy:** Genkit wins on breadth of shipped generation features and Firebase-ecosystem
fit; aisotropy wins on isolation-by-construction, the complete built-in memory/cost stack, and
OTel `gen_ai` semconv compliance.
