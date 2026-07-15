---
type: Survey
title: "P3 deep-dive — cost governance & observability across 7 arenas + aisotropy"
description: Run-verified comparison of per-tenant cost tracking, pre-spend budget gates, and observability across Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta, and the aisotropy runtime. Every cell was proven by an offline demo that actually executed.
tags: [arenas, cost, observability, otel, gen_ai, pre-spend-gate, per-tenant, competitive]
timestamp: 2026-07-15T00:00:00Z
---

# P3 — Cost governance & observability

> **Question:** does the framework help you (a) track **cost per tenant**, (b) **reject/degrade
> before you spend** tokens, and (c) **observe** the LLM call — or do you build all three yourself?
> **Method:** one offline, deterministic demo per arena (`cost_obs_demo.*`), no API key, fake model,
> synthetic token counts (`len(text)//4` or a made-up price). Token *numbers* are illustrative; the
> **BUILT-IN / DIY / PAID verdicts** — proven by execution — are the deliverable.

**A word on the synthetic numbers.** Every dollar figure and token count below is fake by
construction (a fake model × a made-up price). That is deliberate and load-bearing: the thing under
test is whether the *framework* supplies the lever, not whether the arithmetic is right. A
pre-spend gate was proven the only honest way — a **call-counter on the fake model**: on a rejected
turn the counter must not advance (the model was never invoked). Every arena's demo shows that
counter delta.

---

## The matrix

**Classes:** `BUILT-IN` (framework ships it, works offline out of the box) · `DIY` (you build it —
framework gives a raw signal at best, no tenant/budget concept) · `PAID` (only via a hosted /
proprietary product) · `NOT-POSSIBLE` (framework cannot provide it offline by construction).
Sub-cells split the honest reality where one word would overclaim.

| Framework | Per-tenant cost tracking | Pre-spend gate | Observability |
|---|---|---|---|
| **Pydantic AI** (Py) | **DIY** — `result.usage`→`RunUsage` per-call is BUILT-IN; tenant ledger + budget-minus-spent hand-rolled (`UsageLimits` is per-**run**, no tenant concept) | **BUILT-IN** — `UsageLimits(count_tokens_before_request=True)`→`count_tokens()`→`check_before_request()` raises `UsageLimitExceeded` **before** `model.request()`. Proven: rejected turn `generation_calls==0`. Per-**run**, not per-tenant. | **BUILT-IN** — native OTel `gen_ai` spans via `logfire.instrument_pydantic_ai()` (send_to_logfire=False, in-memory exporter, offline). Hosted Logfire = **PAID**. `tenant_id` on framework spans = **DIY** (only on the wrapping `logfire.span`). |
| **Mastra** (TS) | **DIY** — `result.usage {input/output/totalTokens}` per-call BUILT-IN; per-tenant Map ledger + pricing hand-rolled | **DIY** — no built-in usage-limit (`grep @mastra/core usageLimit\|checkBeforeRequest\|budget` = 0 hits; usage is post-call only). DIY gate proven: rejected turn model-call Δ=0. | **BUILT-IN** — `@mastra/observability` native tracing (25 spans/5 traces, in-memory `TestExporter`, offline); `gen_ai` usage attrs on `model_generation`. `tenant_id` = **DIY** (`tracingOptions.metadata`). Hosted UI (Mastra Cloud/Langfuse/Braintrust) = **PAID**. |
| **LangGraph** (Py) | **DIY** — `AIMessage.usage_metadata` per-call BUILT-IN + within-scope aggregation by **model name** BUILT-IN; per-**tenant** attribution DIY. Verified tenantA=37/tenantB=35. | **DIY** — no token gate in the framework. `ModelCallLimitMiddleware` is a call-**count** cap (per-agent, reactive), not a token budget. DIY gate proven: rejected Δ=0 (37+20>25, model never reached). | **BUILT-IN transport** — native `gen_ai`-semconv OTel spans to your **own** in-memory exporter offline (langsmith SDK bridge, dummy key). Hosted **LangSmith dashboard = PAID**. `tenant_id` on spans = **DIY** (→`langsmith.metadata.tenant_id`, verified on 3 spans). |
| **Eino** (Go) | **DIY** — `schema.Message.ResponseMeta.Usage` per-call is a BUILT-IN first-class field; `callbacks.RunInfo` has **no tenant** → cost meter DIY | **DIY** — no pre-spend primitive; usage observable only **after** the call. DIY gate proven: call-counter 1→1 across rejected turn (0 model calls spent). | **BUILT-IN span/timing** — native `callbacks` aspect captured 2 spans + per-component timings + usage into in-memory exporter. **No native OTel `gen_ai` emitter** (eino-ext targets APMPlus/CozeLoop/Langfuse = external/partly **PAID**). `tenant_id` on spans = **DIY** (injected via `context.Context`). |
| **ADK Go** (Go) | **DIY** — `event.UsageMetadata` (Prompt/Candidates/Total) per-call BUILT-IN + `gen_ai.usage.*` on span; no tenant, no cost meter → aggregation DIY | **DIY** — no pre-spend primitive; "ADK measures, doesn't gate." DIY gate proven: rejected turn `Calls 5→5` (runner never invoked). | **BUILT-IN** — OTel `gen_ai.*` spans via delegating global `TracerProvider` (13 spans captured with a `tracetest.SpanRecorder`, **no ADK config**). `tenant.id` native on ADK spans = **false** → **DIY** wrapper span. Self-hostable/free via OTLP; hosted APM = **PAID**/optional. |
| **Genkit Go** (Go) | **DIY** — `resp.Usage {Input/Output/TotalTokens}` per-call BUILT-IN; `ai.GenerationUsage` has no tenant → aggregation + pricing DIY | **DIY** — no framework API. `ai.WithMaxTurns` is a tool-loop cap that errors **after** spending (not per-tenant, not a budget). DIY gate proven: rejected Δ=0. | **BUILT-IN OTel transport** — 7 real Genkit spans via `tracetest`, no patch — but proprietary `genkit:*` schema, **zero `gen_ai.*` keys**. `tenant_id` is NOT a Genkit span attribute → **DIY**. Hosted backend = **PAID**/optional. |
| **Letta** (Py) | **DIY** — `LettaResponse.usage` (prompt/completion/total, step_count, cache) per-call is a genuine SDK type = BUILT-IN **visibility**; per-tenant meter + USD DIY | **DIY** — no pre-spend/`check_before_request`. `max_steps` is a server-side agent-loop cap, not a token/$ budget. DIY gate proven: call-counter 4→4 on rejected turn. | **BUILT-IN but SERVER-side** — real `ProviderTrace` per LLM call carries `agent_tags` (tenant/user), `user_id`, `org_id`, `billing_context`; but `runs.trace.retrieve` needs **ClickHouse** → **NOT-POSSIBLE** offline from the client. Client-side `tenant_id`-on-span = **DIY** (`InMemorySpanExporter`). Hosted per-tenant dashboards = **PAID** (Letta Cloud). |
| **aisotropy** (Go+Rust) | **BUILT-IN** — `costmeter.Meter` aggregates real provider `Usage` **per `tenant_id`** (in/out tokens, requests, `CostUSD`), wired to the ctx tenant by construction (`chat.go:300`, `chat_stream.go:371`). Honest limit: post-hoc, per-process, not durable/cross-replica. | **BUILT-IN + per-TENANT + BY CONSTRUCTION** — token-bucket in BadgerDB (`memory.Limiter`), `Allow()` checked **before** the LLM call on **both** routes → `429` before any spend (`chatCheckQuota`, `chat.go:214`, `chat_stream.go:184`), plus quota-pressure **degrade** to a cheap model + cost-budget routing. | **BUILT-IN** — `WithOTel` decorator emits `gen_ai.*` semconv spans with **real** provider `Usage` **and `tenant_id`/`user_id` natively on the serving span** (from ctx, not a bolt-on wrapper — `otel.go:200-205`). No-op by default; OTLP via standard OTel env vars; 100% self-hostable/free. |

Every row above was executed offline (`EXIT=0`), with the pre-spend gate proven by a physical model
call-counter. Sources: each arena's `scripts/cost_obs_demo.*` (or `cmd/costobs/`) + `FEATURES.md`;
aisotropy's cells from `internal/costmeter`, `internal/memory/quota.go`, `internal/llm/otel.go`
(tests `go test ./internal/costmeter/ ./internal/memory/ ./internal/llm/` — all `ok`).

---

## The honest nuance (read this before quoting the matrix)

Three separate things get conflated as "cost + observability." Kept apart, the reality is:

1. **Per-call usage VISIBILITY is table-stakes.** Every single framework — all 7 + aisotropy —
   hands you input/output/total tokens on the reply as a BUILT-IN field (`result.usage`,
   `usage_metadata`, `ResponseMeta.Usage`, `event.UsageMetadata`, `resp.Usage`,
   `LettaResponse.usage`). Nobody loses here. Do **not** claim this as an aisotropy edge.

2. **A PRE-SPEND gate is genuinely rare — but Pydantic AI has a real one too.**
   Pydantic AI's `UsageLimits(count_tokens_before_request=True)` calls `count_tokens()` and raises
   `UsageLimitExceeded` **before** `model.request()` — proven by execution (`generation_calls==0`
   on the rejected turn). That is a real, framework-shipped pre-spend gate. **Everyone else is
   DIY** (Mastra/LangGraph/Eino/ADK Go/Genkit Go/Letta ship no pre-spend primitive; the closest
   built-ins — `ModelCallLimitMiddleware`, `ai.WithMaxTurns`, `max_steps` — are call/turn/step
   **counters that fire after or around spend**, not token budgets). So "has a pre-spend gate" does
   **not** by itself separate aisotropy from Pydantic AI.

3. **PER-TENANT cost is DIY essentially everywhere.** Six frameworks return tokens with **no tenant
   dimension at all**; the tenant ledger is always hand-rolled. Only aisotropy ships per-tenant
   aggregation as a first-class runtime object (`costmeter.Meter`) — but that is still **post-hoc
   measurement**, not a novel capability. The aggregation itself is not the moat.

4. **Observability OTel transport is common; `gen_ai.*` semconv is not.** Most arenas emit OTel
   spans to your own exporter offline (Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go —
   even Letta client-side). The **schema** is where they split: real `gen_ai.*` semconv only in
   **ADK Go** and **aisotropy**; Genkit is proprietary `genkit:*` (zero `gen_ai.*`); Eino has no
   native `gen_ai` emitter; **LangGraph's managed dashboard is PAID** (LangSmith) though its spans
   *do* reach your own exporter offline. And in **every** other arena `tenant_id` on a span is
   **DIY** — you inject it into a wrapper span or run metadata; it is never native to the
   framework's own serving span.

### So where is aisotropy's actual edge — stated precisely, not overclaimed

Not "it has a gate" (Pydantic AI does). Not "it aggregates cost" (post-hoc, table-stakes). Not
"it emits OTel `gen_ai`" (ADK Go ties). The edge is the **intersection that no single arena holds**:

> a **per-TENANT**, **pre-spend** gate, wired **by construction** (token-bucket in BadgerDB,
> `Allow()` checked before the LLM call → `429` before any spend), **plus** `tenant_id` living
> **natively on the `gen_ai` serving span** rather than on a DIY wrapper.

Line up the near-misses:

- **Pydantic AI** — real pre-spend gate, but it is **per-run**, has **no tenant** concept, and its
  `tenant_id`-on-span is DIY. Two of the three intersection properties missing.
- **ADK Go** — real `gen_ai.*` semconv (ties aisotropy on schema), but **no gate at all** and
  `tenant.id` is DIY (native = false). One property; the gate and native tenancy both absent.
- **Everyone else** — DIY gate, DIY per-tenant, DIY `tenant_id`-on-span, and (Genkit/Eino)
  non-`gen_ai` schema. Zero of the three.

A caveat that keeps this honest: aisotropy's gate is a per-tenant **request-rate** token bucket
(1 token = 1 request) with a quota-pressure **degrade** and cost-budget routing — it is *not* a
per-run **token-count** budget like Pydantic AI's `input_tokens_limit`. Different mechanism, same
constitutional guarantee: a tenant is rejected or degraded **before** the model is called. The
lead is the *combination* (per-tenant + pre-spend + by-construction + native tenant-tagged
`gen_ai` spans), not any one property in isolation.

---

## Bottom line for P3

**Where aisotropy genuinely leads:** the **intersection**. It is the only runtime that puts a
per-tenant pre-spend gate (`429`/degrade before the LLM call, by construction) *and* native
`tenant_id` on `gen_ai` serving spans in the same box. Each rival owns at most one leg of that:
Pydantic AI the pre-spend gate (per-run, tenantless), ADK Go the `gen_ai` schema (no gate). None
own the pair, and none make the gate per-tenant-and-by-construction.

**Where aisotropy is table-stakes (do not sell these as differentiators):**
- **Per-call usage visibility** — BUILT-IN in all 8; a tie.
- **Post-hoc per-tenant cost aggregation** — aisotropy ships it first-class, but it is still
  measurement after the fact; the mechanism is unremarkable, only the wiring is cleaner.
- **OTel transport** — BUILT-IN in most; not special.
- **`gen_ai.*` semconv emission** — matched by ADK Go.
- **Self-hostable / free telemetry** — matched by every OSS arena; only LangGraph's *dashboard* and
  the various hosted UIs are PAID.

Net: P3 is a **narrower** lane than P1 (isolation-by-construction). On cost + observability,
aisotropy leads on a specific, defensible intersection rather than on any single axis — and the
honest framing is exactly that, because Pydantic AI's real pre-spend gate and ADK Go's real
`gen_ai` semconv each cover one leg of what aisotropy combines.
