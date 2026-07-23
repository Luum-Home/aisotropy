---
type: Feature matrix
title: "Pydantic AI — features vs aisotropy"
description: Coverage map of Pydantic AI v2 capabilities compared to aisotropy's built-ins.
tags: [arenas, pydantic-ai, features, comparison]
timestamp: 2026-07-15T00:00:00Z
---

# Pydantic AI — Features

## Built-in capabilities

### Structured output (FAIL-CLOSED)
- **Status:** Built-in
- **Mechanism:** Pydantic models + `TypeAdapter` for JSON schema generation
- **Retry:** `raise ModelRetry(message)` triggers automatic self-correction on next turn
- **Reflection loop:** Yes — `UnexpectedModelBehavior` catches schema violations and retries
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Gollem reflection loop. Both are
  fail-closed. Difference: aisotropy's is **default/runtime-wired** (no opt-in needed),
  Pydantic AI's is decorator-based (`@agent.tool` + `raise ModelRetry`).

### Tool calling
- **Status:** Built-in
- **Mechanism:** `@agent.tool` decorator with type-safe function signatures
- **DI integration:** `RunContext[deps]` gives first-class dependency injection into tools
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's `WithTypedTool[T]`. Both are type-safe.
  Pydantic AI uses decorators; aisotropy uses builder pattern.

### Streaming
- **Status:** Built-in
- **Mechanism:** `agent.run_stream(prompt)` (v2 async context manager) yields deltas; run-verified in the feature-parity matrix (#11) via `run_stream` + `task.cancel()`
- **Cancellation:** Python `asyncio.CancelledError` propagates naturally
- **AISOTROPY COMPARISON:** Equivalent to aisotropy's Huma SSE with `ctx` cancellation.
  Both stream deltas; aisotropy adds HTTP-level `Accept: text/event-stream` semantics.

## DIY / missing capabilities

### Working memory
- **Status:** Not built-in
- **Pattern:** App-level dictionary, Redis, or custom state management
- **AISOTROPY COMPARISON:** aisotropy ships BadgerDB in-process working memory with µs latency,
  summary buffer, and transactional outbox — all wired by default.

### Semantic memory (vectors)
- **Status:** Not built-in
- **Pattern:** BYO Chroma, Pinecone, Weaviate, Qdrant, or any vector store
- **AISOTROPY COMPARISON:** aisotropy has in-process cosine brute-force (active) + Qdrant gRPC
  (CODE-ONLY, dormant behind `QDRANT_HOST`). Both require wiring for production vectors.

### Facts memory (journal)
- **Status:** Not built-in
- **Pattern:** No equivalent — would need SQLite/PostgreSQL + FTS5 manually wired
- **AISOTROPY COMPARISON:** aisotropy ships Engram-style SQLite+FTS5 facts memory out of the box.

### Cost governance
- **Status:** Partial — REAL per-run pre-spend gate; per-tenant is DIY
- **RUN-VERIFIED** (`scripts/cost_obs_demo.py`, pydantic-ai 2.10.0, offline `TestModel`):
  - **Per-call usage visibility — [BUILT-IN].** `result.usage` returns a `RunUsage`
    (`input_tokens`/`output_tokens`/`total_tokens`). Note: in v2.x `usage` is a **property**,
    not the older `result.usage()` method call.
  - **Pre-spend budget gate — [BUILT-IN], and it genuinely fires *before* the model.**
    `UsageLimits(input_tokens_limit=N, count_tokens_before_request=True)` makes the framework
    call `model.count_tokens()` (cheap) then `check_before_request()` raises
    `UsageLimitExceeded` **before** `model.request()`. The demo proves it with a call-counter:
    on the rejected turn `generation calls == 0` (only the pre-count ran). Verified message:
    `The next request would exceed the input_tokens_limit of 2 (input_tokens=14)`.
    (`count_tokens_before_request` needs a model implementing `count_tokens()` — Google/Bedrock
    in prod; our offline fake implements it. `TestModel`/`FunctionModel` do NOT.)
  - **Per-tenant aggregation & per-tenant budget — [DIY].** `UsageLimits` is scoped to a single
    **run**, not a tenant. The demo sums `result.usage` into a `TenantLedger` and derives each
    turn's limit from `budget - already_spent[tenant]` — all app code. The framework has no
    tenant concept.
- **AISOTROPY COMPARISON:** aisotropy wires the **pre-spend quota gate + per-tenant cost meter**
  by default (token-bucket in BadgerDB, `/costs` endpoint). Pydantic AI gives you the pre-spend
  *primitive* (a real, pre-generation raise) but leaves per-tenant accounting to you.

### Observability
- **Status:** Built-in native OTel tracing (opt-in one-liner); hosted dashboard is paid; tenant tagging is DIY
- **RUN-VERIFIED** (`scripts/cost_obs_demo.py`, logfire 4.37.0 + opentelemetry-sdk, offline):
  - **Native OTel gen_ai tracing — [BUILT-IN].** `logfire.configure(send_to_logfire=False)` +
    `logfire.instrument_pydantic_ai()` emits standard OTel spans captured here with an
    `InMemorySpanExporter`. Observed spans: `invoke_agent <agent>` and `chat <model>` carrying
    `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.usage.input_tokens` /
    `output_tokens`, `gen_ai.aggregated_usage.*`, plus input/output messages and
    `gen_ai.conversation.id`. Runs fully offline — no data leaves the process.
  - **Hosted trace dashboard — [PAID].** Sending those same spans to the **Logfire SaaS**
    dashboard is the paid path (same story as LangSmith). The OTel export itself is OSS/free —
    you can point it at your own collector.
  - **`tenant_id` on spans — [DIY].** The framework's spans do **not** carry `tenant_id`
    (verified: `tenant_id on framework spans == False`). It appears only on the wrapping
    `logfire.span("tenant.turn", tenant_id=...)` we open ourselves.
- **AISOTROPY COMPARISON:** aisotropy ships OTel gen_ai semconv tracing plus `/costs`, `/health`,
  and circuit-breaker metrics wired by default, with tenant identity propagated via
  `context.Context`. Pydantic AI's tracing is comparable in span quality but is opt-in and
  tenant-unaware — the tenant dimension is your responsibility.

### Dependency license posture (executable governance)
- **Status:** RUN-VERIFIED — the real dep tree holds the permissive line aisotropy claims
- aisotropy's constitutional gate is "100% permissive (MIT/BSD/Apache-2.0/ISC), no copyleft/BUSL/SSPL/ELv2". This is the
  same gate executed against **this arena's actual dependency tree** (not a guess).
- **RUN-VERIFIED** (`.venv/bin/python -m piplicenses --format=csv`, pip-licenses 5.5.5, exit 0):
  - **53 packages** in the arena venv (pip-licenses excludes itself + its two deps
    `prettytable`/`wcwidth`, which are scan tooling, not arena deps).
  - **51 permissive** — MIT (26 incl. `pydantic`, `pydantic-ai-slim`, `pydantic-graph`, `anthropic`,
    `fastapi`, `logfire`, `tiktoken`, `jiter`), BSD-2/BSD-3 (11 incl. `starlette`, `uvicorn`,
    `httpx`, `click`, `protobuf`), Apache-2.0 (13 incl. all `opentelemetry-*`, `openai`, `requests`,
    `distro`), ISC (1 `griffelib`), plus `typing_extensions` (**PSF-2.0**) and `regex`
    (**Apache-2.0 AND CNRI-Python**) — PSF/CNRI are BSD-style permissive.
  - **2 weak copyleft (MPL-2.0, file-level)** — `certifi` (Mozilla Public License 2.0) and `tqdm`
    (`MPL-2.0 AND MIT`). Per aisotropy's own `license-policy.md`, MPL 2.0 is **CAUTION, not a
    BLOCKER**: only changes to MPL-covered *files* must be published; both are used unmodified as
    libraries, so no source-disclosure obligation attaches to arena code.
  - **0 strong copyleft** (no GPL / LGPL / AGPL).
  - **0 restricted / source-available** (no BUSL / SSPL / ELv2 / Commons-Clause / FSL).
- **Notable non-MIT/Apache packages found (named, per task):** `certifi` (MPL-2.0), `tqdm`
  (MPL-2.0 AND MIT), `typing_extensions` (PSF-2.0), `regex` (Apache-2.0 AND CNRI-Python). None is a
  hard blocker under aisotropy's gate; the two MPL packages are the only copyleft and are CAUTION-tier.
- **Verdict:** the framework's real dep tree **does** hold the permissive line — zero hard blockers,
  the only copyleft is file-level MPL in leaf utilities (`certifi`, `tqdm`). aisotropy could adopt
  this tree without tripping its "no copyleft/BUSL/SSPL" constitutional gate (the two MPL leaves
  would need an explicit CAUTION note, consistent with the policy).

### Multi-tenant isolation
- **Status:** No isolation layer (DIY / out-of-scope) — not a defect
- **Pattern:** Pydantic AI is a **stateless agent library** that ships no memory or multi-tenancy
  layer by design ("Agents are stateless and designed to be global"). `deps` is pure dependency
  injection — a developer-constructed object bound to nothing (no principal, no auth, no tenant
  concept). Scoping is a BYO store keyed by a key you compute; `tenant_id` on `RunContext[deps]`
  is just one such developer-computed field.
- **RUN-VERIFIED** (`scripts/smoke.py`, exit 0): positive control passes and correct-key scoping
  is safe (the exact-key store partitions when the key is right). A wrong developer-computed key
  reads the wrong slice — because there is **no framework isolation layer to reject it**. That is
  plain-dict behavior (true of any KV store, including aisotropy's BadgerDB), exercised against the
  arena's own 15-line dict — the framework is not in that path. This is a DIY classification, not
  a leak: a leak would mean a framework boundary failed, but Pydantic AI ships no such boundary.
- **AISOTROPY COMPARISON:** aisotropy binds all axes at the Huma handler boundary and propagates
  them via `context.Context` + per-statement/key scoping + a constitutional gate — so the
  **default** read path is already scoped and no code path can issue an unscoped read. Pydantic AI
  leaves key computation and enforcement entirely to the developer.

### Multi-PRODUCT scoping (3rd axis — product_id)
- **Status:** No isolation layer (DIY / out-of-scope) — `product_id` is just another
  developer-computed deps key, same as `tenant_id`
- **RUN-VERIFIED** (`scripts/product_isolation_demo.py`, pydantic-ai 2.10.0, offline `TestModel`,
  exit 0): aisotropy scopes memory by the 4-tuple `(tenant_id, user_id, session_id, product_id)`;
  the isolation-model smoke covered tenant/user/session, so this demo adds the **product** axis and
  shows the same structural truth one axis deeper.
  - **(a) Positive control — PASS.** Seeded `tenantA/userA/PRODUCT-X` with a random marker; the
    correct `(tenantA, userA, productX)` key reads it back.
  - **Correct scoping — PASS.** Same tenant + same user + the *correct other product* (`productY`)
    reads its own slice → NO marker. The product axis genuinely partitions **when the key is right**,
    so the wrong-key read below is about the key, not a broken store.
  - **(b) No isolation layer on the product axis.** A request that should be scoped to `productY`,
    but with `deps.product_id` computed as `productX` (SAME tenant, SAME user — only the product key
    differs), reads productX's slice. `get_recent_messages()` (what the `get_context` tool calls)
    uses `deps` verbatim, and the framework has no product concept to reject the wrong key. Actual
    run output:
    ```
    [pydantic-ai] No isolation layer on the product axis — the developer owns the key:
      Request should be scoped to productY, but deps.product_id was computed as 'productX'.
      SAME tenant, SAME user — only the product key differs.
      [DIY] Confirmed: a wrong developer-computed product_id reads the wrong slice — no framework layer to prevent it
    ```
  - **Root cause:** `product_id` is no more privileged than `tenant_id` — the framework binds it to
    nothing (no principal, no API surface, no auth), because Pydantic AI is a stateless agent library
    with no memory or multi-tenancy layer. It is a dict key you chose to put in the lookup tuple, so
    a wrong key returns the wrong slice — exactly like any KV store. This is DIY/out-of-scope, not a
    framework defect.
- **AISOTROPY COMPARISON:** aisotropy binds all four axes at the Huma handler boundary and propagates
  them via `context.Context` — a wrong product_id can't be silently substituted downstream because
  no read is issued without the ctx-scoped key, so its **default** path is already scoped.

## Comprehensive feature-parity matrix (RUN-VERIFIED)

`scripts/feature_parity_demo.py` exercises **25 executable features** from
`arenas/ARENA-SPEC-v2.md` against pydantic-ai 2.10.0, fully offline
(`TestModel`/`FunctionModel`, no network, no key). Every `[BUILT-IN]`/`[PARTIAL]`
line **executes the framework**; every `[DIY]`/`[NOT-POSSIBLE]` line runs the
fallback we had to write and labels it as ours. Run: `./.venv/bin/python
scripts/feature_parity_demo.py` (exit 0 = all 25 assertions held).

Run-verified tally: **5 BUILT-IN · 4 PARTIAL · 14 DIY · 2 NOT-POSSIBLE**.

| # | Feature | Class | Run-verified evidence |
|---|---|---|---|
| 1 | Working memory (recent history/tenant/user/session) | **PARTIAL** | `message_history` threads prior turns into a run (BUILT-IN conduit); the store + `(tenant,user,session)` scoping is our dict (DIY half). |
| 2 | Facts/episodic + keyword (FTS5-style) search | **DIY** | No facts store; our keyword index returned acme rows, 0 cross-tenant. |
| 3 | Semantic/vector retrieval (RAG) | **DIY** | No vector store; our cosine over a DIY embedder ranked the target doc #1. BYO Chroma/Qdrant. |
| 4 | Summary / rolling buffer | **DIY** | No buffer manager; our `condense()` folded 17 turns → summary + 3 recent. |
| 5 | Importance × recency × relevance scoring | **DIY** | No scorer; our composite score picked the runbook over chitchat. |
| 6 | Contradiction detection / supersede | **DIY** | No supersede; our upsert archived `postgres 15` → `postgres 16`. |
| 7 | Knowledge ingestion (chunk + embed) | **DIY** | No ingestion path; our chunker produced chunks + embeddings. |
| 8 | BYO embedder | **DIY** | No embedder seam (no vector tier to bring one into); you own it entirely. |
| 9 | **Tiered memory by access temperature** | **NOT-POSSIBLE** | Framework ships **0** memory stores; `message_history` is a conduit, not a tier. aisotropy = 4 tiers (KV µs / FTS ms / vector / batch). |
| 10 | Fail-closed reflection loop | **BUILT-IN** | Schema break → retried with the exact error (`RetryPromptPart`); on exhaustion raised `UnexpectedModelBehavior` (fails closed, no silent text). |
| 11 | SSE streaming + immediate ctx-cancel | **BUILT-IN** | `run_stream` streamed deltas; `task.cancel()` stopped the upstream producer at 10/25 (no further emission) — spend cut, not just the write. |
| 12 | Stateless response cache (per-tenant) | **DIY** | No cache; our LRU served the 2nd call with 0 generation, cross-tenant MISS. |
| 13 | Scope-gated tools (absence-of-information) | **PARTIAL** | The `prepare` hook (BUILT-IN) hid the tool without scope and revealed it with scope; the scope **value** is developer-passed deps (DIY/CONVENTION, no identity enforcement). |
| 14 | Multi-step agent loop | **BUILT-IN** | Framework auto-ran tool-call → result → continue to a final answer, no orchestration code from us. |
| 15 | Typed tools + MCP | **PARTIAL** | Typed tools BUILT-IN (JSON schema auto-derived from the fn signature); MCP is the opt-in extra `pydantic-ai-slim[mcp]` (not installed here). |
| 16 | Bearer-token → identity binding | **DIY** | Framework has **no** auth; our `verify_bearer` resolved identity but nothing binds it to deps — a forged deps tenant is not rejected. |
| 17 | Per-tenant PRE-SPEND quota gate | **BUILT-IN** | `UsageLimits(count_tokens_before_request=True)` raised `UsageLimitExceeded` **before** generation (rejected turn: generation calls = 0). Per-RUN gate BUILT-IN; per-TENANT budget DIY. |
| 18 | Circuit breaker | **DIY** | No breaker; our `Breaker` opened after 3 failures and fast-failed the 4th (upstream stayed at 3). |
| 19 | Quota-pressure model degradation | **DIY** | Per-run `model=` swap is easy, but the pressure→cheap policy is our code (40% → primary, 97% → cheap). |
| 20 | Per-tenant cost meter | **PARTIAL** | `result.usage` per call is BUILT-IN; our per-tenant sum separated acme (112) vs globex (55) tokens. |
| 21 | OTel gen_ai spans | **BUILT-IN** | `instrument_pydantic_ai()` emitted gen_ai spans offline (model+usage+messages); `tenant_id` is **not** on them (DIY); hosted Logfire dashboard is PAID. |
| 22 | Health / readiness probe | **DIY** | Framework is a library, not a server — no probe endpoint; our `readiness()` checks our stores. |
| 23 | Product evaluation (IR + LLM-judge) | **DIY** | `pydantic-evals` is a separate/uninstalled package; our IR gave P@3 = 0.67, recall = 1.00; LLM-judge via structured output scored 8/10. |
| 24 | Transactional outbox (atomic msg + event) | **NOT-POSSIBLE** | Framework has **no** persistence/txn primitive; our in-memory pair is not crash-durable ACID. aisotropy = one BadgerDB ACID txn. |
| 25 | Async consolidation (dedup / contradictions) | **DIY** | No sleep-cycle; our pass folded 4 → 2 events (deduped + dropped noise). |

### Reading of the matrix

- **BUILT-IN (5):** reflection loop, streaming+cancel, multi-step agent loop,
  per-RUN pre-spend gate, OTel gen_ai spans. These are Pydantic AI's genuine
  strengths — the **agent loop + typed-output + spend/observability primitives**.
- **PARTIAL (4):** working memory (conduit, no store), scope-gated tools (`prepare`
  hook but unenforced scope), typed tools + MCP (typed yes, MCP opt-in), cost meter
  (per-call usage yes, per-tenant no). The framework hands you the *primitive*; the
  *runtime concern wrapped around it* is yours.
- **DIY (14) + NOT-POSSIBLE (2):** the entire **memory-tier + consolidation** block.
  Pydantic AI ships **zero** memory stores. A *tiered* hierarchy of different-nature
  stores and a *transactional outbox* aren't just unshipped — they can't be expressed
  against the framework at all (it has no persistence layer).

This is the honest headline: on the **memory tiers** (aisotropy's four-DB
"specialized tools by access temperature") Pydantic AI is a blank slate — 8 of 9
memory rows are DIY and the tiered-hierarchy row is NOT-POSSIBLE.

## Genuine strengths (steelman — RUN-VERIFIED)

The rows above are the "what's missing" side. This is the other side: where Pydantic AI is
genuinely excellent — **type-safety and agent-loop DX** — it is best-in-class. `scripts/strengths_demo.py`
DEMONSTRATES three real strengths with runnable v2 code, fully offline against the pinned
`pydantic-ai>=2.0.0` (installed 2.10.0). Run: `./.venv/bin/python scripts/strengths_demo.py`
(exit 0 = all three held). Every line executes the framework and asserts on the real result.

- **Structured output is a TYPED CONTRACT — fail-closed + self-correcting — [BUILT-IN].**
  `Agent(output_type=<PydanticModel>)` returns a *validated object*, not a string to parse.
  RUN-VERIFIED (v2 symbols `Agent(output_type=...)`, `pydantic.Field`, `Agent(retries=...)`,
  `UnexpectedModelBehavior`):
  - `output_type=Weather` → validated `Weather(city='Paris', temp_c=21.5)`.
  - `output_type=Union[Refund, Escalate]` → routed to the `Escalate` typed branch (typed routing
    for free, discriminated by the output tool the model picks).
  - Reflection loop: an output violating a `Field(pattern=...)` constraint was retried with the
    exact validation error and validated on generation #2 (`sku='ABC-1234'`).
  - Fail-closed: with retries exhausted on a never-valid output, the framework raised
    `UnexpectedModelBehavior` — never silent bad text.
- **First-class TYPED dependency injection — [BUILT-IN].** `deps_type=Deps` + `RunContext[Deps]`
  threads a typed dependency object into every tool with full static typing. RUN-VERIFIED: a tool
  read `ctx.deps.tenant_id` / `ctx.deps.api_budget` off a typed `Deps` (v2 symbols `deps_type=`,
  `RunContext[Deps]`, `@agent.tool`).
- **MODEL-AGNOSTIC by construction — [BUILT-IN].** The same agent + tools run against different
  model backends with zero code change. RUN-VERIFIED: one agent ran unchanged against `TestModel`
  and `FunctionModel` via `agent.override(model=...)` — the production analog of swapping providers
  (OpenAI ↔ Anthropic ↔ Google) or degrading to a cheap model under pressure.

**Not demonstrated (not in the pinned 2.10.0 install, stated honestly):** `DurableAgent`
(`pydantic_ai.durable_exec` is present but the Temporal/DBOS backends are not installed),
`pydantic-evals` (separate uninstalled package — see feature #23), and MCP toolsets
(`pydantic-ai-slim[mcp]` extra not installed — see feature #15). These are real v2 capabilities;
they are simply not exercised here because the arena venv does not carry their optional deps.

**Honest overlap with aisotropy:** aisotropy *also* does fail-closed structured output (the Gollem
reflection loop) and typed tools — that part overlaps. The difference is category: Pydantic AI is a
Python **type-safe agent library** whose edge is developer ergonomics around the agent loop;
aisotropy is a Go **runtime** that additionally ships memory tiers, multi-tenancy, cost, and
observability by default. The strengths above are exactly the layer where a library shines, they
are real, and they are Pydantic AI's to keep.

## Summary

Pydantic AI v2 is the **best-typed DX** of the three frameworks — but it's fundamentally an
**agent orchestration library**, not a runtime. Most infrastructure concerns (memory tiers,
multi-tenancy) are DIY. Two are more nuanced than "DIY", and the run-verified demo shows it:
**cost** ships a *real pre-spend gate* (`UsageLimits.check_before_request` +
`count_tokens_before_request` raise `UsageLimitExceeded` before the model call — proven with a
call-counter), and **observability** ships *native OTel gen_ai tracing* one `instrument_pydantic_ai()`
call away. What's still DIY is the **per-tenant dimension** on both: per-tenant cost aggregation,
per-tenant budgets, and `tenant_id` on spans are all your code — the framework has no tenant concept.
The structured output + retry path is smooth, but that's a commodity feature now (collapsed as a
differentiator per competitive-landscape).

Where aisotropy pays the full upfront cost to build **everything** as built-in, Pydantic AI
gives you the best experience for the **agent loop** and leaves the rest to you.
