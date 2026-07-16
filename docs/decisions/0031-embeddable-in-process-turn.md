---
type: ADR
title: "ADR-0031 — Embeddable in-process App.Turn: call the full pipeline like a function, no HTTP hop"
description: A DX bake-off judged Aisotropy behind Pydantic AI on one axis that had nothing to do with the runtime's substance — the agent was a SERVER you POST to (New(...).Run(ctx) then curl an SSE route), not a FUNCTION you call (Pydantic AI's run_sync). This ADR adds App.Turn(ctx, ChatInput) — a library-first entry point that runs one chat turn in-process at FULL parity with POST /v1/.../chat. The parity is structural, not copied: the non-streaming pipeline was first extracted into api.ChatCore(ctx, ChatDeps, *ChatInput) and the runtime construction into (*App).startRuntime(ctx) — the SINGLE dependency-construction path both Run (HTTP) and Turn (in-process) boot from — so there is exactly one pipeline and one wiring, and neither entry point can drift on the constitutional gates (tenant isolation via ctx, quota-before-spend, fail-closed reflection). The runtime is built once lazily on the first Turn (turnOnce, mirroring the semanticOnce idiom) and reused across calls, so working memory persists between turns and the background workers boot once; App.Close tears it down (idempotent, nil-safe). HONEST SCOPE — the security nuance: Turn runs BELOW the HTTP boundary, so no auth middleware executes; the caller's own trusted code supplies TenantID/UserID/Scopes, exactly as the code that mints a JWT for the HTTP path is inside the trust boundary. Isolation between tenants still holds by construction (identity enters ctx and never travels any other way). Additive and default-safe: a Run-served App is byte-for-byte unchanged.
tags: [decisions, adr, adoption, dx, library-first, embeddable, turn, run-sync, pipeline-parity, isolation, lifecycle, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0031 — Embeddable in-process `App.Turn`: call the full pipeline like a function, no HTTP hop

## Status

✅ **Accepted — 2026-07-16.** Adds a second entry point to the public `aisotropy.App` — an
**in-process** `App.Turn(ctx, ChatInput) (ChatOutput, error)` that runs one chat turn at full
parity with `POST /v1/tenant/{tenant_id}/user/{user_id}/chat`, with **no HTTP server, no port, no
network hop**. It complements, and does not replace, `App.Run(ctx)` (serve the same runtime over
HTTP): both drive one shared pipeline over one shared runtime, so there is no second code path to
drift. This ADR does **not** supersede [ADR-0013](0013-public-extension-surface-facade-vs-promote.md)
(the builder façade) — it *extends* it with the embeddable surface that ADR named as pending.

## Context

A developer-experience bake-off across agent frameworks judged Aisotropy **behind Pydantic AI** on
one axis that is orthogonal to the runtime's actual substance (typed contracts, tiered memory,
tenant isolation, cost governance — all already shipped, Phases 0–8): **how you invoke it**. In
Pydantic AI the agent is a value you call like a function — `agent.run_sync("...")` returns the
result inline, ideal for a script, a notebook, a test, or embedding the agent inside a larger Go
program. In Aisotropy the *only* way to get a reply was to `New(...).Run(ctx)` (which boots an HTTP
server and **blocks**) and then `curl` / `http.Post` an SSE or JSON route on a loopback port — even
the package's own godoc `Example` functions had to spin up `httptest`-style servers in a goroutine
and reassemble a streamed body just to show "ask the agent one thing." That boilerplate *was* the
onboarding story, and it read as heavier than a one-line `run_sync`.

Critically, this was a **surface gap, not a substance gap**. The full non-streaming chat pipeline
already existed and was already correct — identity→ctx isolation, the pre-spend quota gate, input
guardrails, gateway model selection, memory hydration, the transactional-outbox user-turn persist,
the fail-closed reflection loop, output guardrails, cost accounting. But it lived **inside the HTTP
handler's closure** (`api.Register`'s inner function), reachable only by sending an HTTP request.
There was no seam to call it directly, and the runtime it needs (the memory tiers, model pipeline,
tools, guardrails, workers) was constructed **inline inside `Run`**, entangled with the router,
middleware, and server it also builds — so there was nothing to boot in-process either.

The open question: **how do we expose a `run_sync`-shaped in-process call at FULL parity with the
HTTP path, without forking the pipeline or the wiring into a second copy that can silently drift on
the constitutional gates?**

## Goals

- Add a **library-first** entry point — `app.Turn(ctx, in)` — that runs one chat turn in-process and
  returns the reply inline, so embedding the agent is a function call, not a server + client.
- Achieve **structural parity, not copied parity**: the in-process and HTTP paths must execute the
  **same pipeline body** over the **same runtime wiring**, so a change to a constitutional gate lands
  in exactly one place and neither entry point can drift.
- **Build the runtime once and reuse it**: the memory tiers, model pipeline, tools, guardrails, cost
  meter, rate limiter, and background workers boot exactly once per `App`, so working memory persists
  across turns and the workers are not re-spawned per call.
- Give the embedded surface an honest **lifecycle**: a `Close` that tears the in-process runtime down
  (workers stopped before the stores they write to, tracer flushed), idempotent and nil-safe.
- Preserve **tenant isolation by construction** on the in-process path, and be **honest about the one
  real difference**: `Turn` runs below the HTTP boundary, so no auth middleware runs and the caller
  supplies identity directly.
- Be **additive and default-safe**: a `Run`-served App is byte-for-byte unchanged; no new dependency,
  no OpenAPI contract change (the HTTP contract is untouched).

## Non-goals

- **NOT** an in-process **streaming** surface. `Turn` runs the **non-streaming** `chat` pipeline
  (`ChatCore`), which is where the fail-closed reflection loop lives. A token-channel in-process API
  (the embedded analogue of `/chat/stream`) is a named follow-on, not this ADR.
- **NOT** exposing the agent/ask/knowledge routes in-process. `Turn` is the chat turn; the streaming,
  agentic-tool, stateless-`/ask`, and knowledge-ingestion surfaces remain HTTP-only (knowledge
  ingestion already has its own SDK methods, ADR-0018). The `appRuntime` carries those extras for
  `Run`, but `Turn` touches only the chat deps.
- **NOT** changing the HTTP path's behavior, contract, or boot side effects. The runtime construction
  moved into `startRuntime` **verbatim** — same order, same logs, same env reads — so `Run` produces
  the identical runtime it did before.
- **NOT** adding an auth check to `Turn`. In-process, the caller is inside the trust boundary
  (see the honest-scope note below); adding a credential gate there would be theater, not security.
  Authentication remains the HTTP boundary's job, unchanged.
- **NOT** sharing a runtime between `Run` and `Turn` on the same `App`. `Run` builds its **own**
  runtime local to the call (which it owns and tears down on shutdown); `Turn` builds the App-memoized
  one. The two never share nor double-build — the same discipline `ensureSemantic` already enforces
  for the semantic index.

## Options considered

### How to reach the chat pipeline in-process

- **Call `Run` and drive it over loopback HTTP from within the same process.** Rejected — this is the
  status quo the bake-off flagged: it still needs a server, a port, a goroutine, an HTTP client, and
  SSE reassembly, and it pays a serialize/deserialize + network-stack round trip to talk to *itself*.
  It is not `run_sync`; it is `run_sync` with a server bolted underneath.
- **Copy the pipeline body into a new in-process method.** Rejected — a second copy of the
  identity→quota→guardrail→hydrate→reflect→cost sequence is exactly how the constitutional gates
  drift: a fix to the quota-before-spend ordering, or the fail-closed reflection posture, would have
  to be made in two places and would eventually be made in one. The codebase's core value is a
  *single* pipeline; two copies destroy it.
- **Extract the pipeline into a shared function both entry points call (CHOSEN).** `api.ChatCore(ctx,
  ChatDeps, *ChatInput)` ([`internal/api/core.go`](../../internal/api/core.go)) is the pipeline body,
  lifted **verbatim** out of `Register`'s closure. The HTTP handler now calls `ChatCore`; `Turn`
  calls `ChatCore`. `ChatDeps` mirrors `Register`'s parameters one-for-one, so the only difference
  between the two entry points is *how `ctx` and `*ChatInput` arrive*, never *what the pipeline does
  with them*. One body, two callers.

### How to boot the runtime the in-process call needs

- **Let `Turn` construct its own leaner runtime inline.** Rejected — a second construction path is the
  *wiring* analogue of a second pipeline: it would read env differently, wire guardrails/gateway/
  workers differently, and drift from what `Run` boots. The parity has to hold for construction too.
- **Extract the runtime construction into one shared builder both entry points boot from (CHOSEN).**
  `(*App).startRuntime(ctx)` ([`aisotropy/app.go`](../../aisotropy/app.go)) is the single
  dependency-construction path: it opens the stores, starts the background workers, decorates the
  clients, discovers tools, and assembles guardrails + gateway — in **byte-for-byte the order `Run`
  used to inline** — and returns an `appRuntime` plus a `close()` that performs the full teardown.
  `Run` now calls `startRuntime` and layers only the **HTTP surface** (router, routes, authn
  middleware, server) on top; `Turn` calls `startRuntime` and layers nothing. The workers run under a
  `context.Background()`-derived `workerCtx` (not the caller's `ctx`), so they outlive any single call
  and are stopped only by `close()` — the same lifetime the inlined workers had.

### When to build the in-process runtime, and how many times

- **Build a fresh runtime per `Turn` call.** Rejected — re-booting the memory tiers and re-spawning
  the background workers on every call would make working memory *non-persistent across turns* (each
  turn would hydrate an empty buffer) and would leak workers. It also defeats the whole point: a
  conversation is many turns against one runtime.
- **Require the caller to explicitly start the runtime before the first `Turn`.** Rejected — that
  reintroduces a lifecycle ceremony the `run_sync` shape exists to remove. `New(...)` then
  `app.Turn(...)` must Just Work.
- **Lazily build once on the first `Turn`, memoize, and reuse (CHOSEN).** `ensureRuntime`
  ([`aisotropy/turn.go`](../../aisotropy/turn.go)) uses `sync.Once` (`turnOnce`) to build the runtime
  on the first call and memoize it (and any build error) — mirroring the `semanticOnce` idiom
  ADR-0018 already established for the semantic index. The build runs against `context.Background()`
  (so the shared runtime is not tied to the first call's `ctx`), while each `Turn` still passes its
  **own** `ctx` to `ChatCore`, so per-call cancellation and deadlines work exactly as on HTTP.

## Decision

### 1. `api.ChatCore` — the pipeline body, extracted verbatim, shared by both entry points

The non-streaming chat pipeline is now a standalone function
([`internal/api/core.go`](../../internal/api/core.go)):

```go
func ChatCore(ctx context.Context, d ChatDeps, in *ChatInput) (*ChatOutput, error)
```

It runs the exact sequence the HTTP handler ran: `chatIdentity` (tenant/user → `ctx`, via
`memory.Key.Validate`; a bad identity → **422**, never a silent unscoped read) → `chatCheckQuota`
(pre-spend token-bucket gate) → input guardrail (`Guard.checkInput`; the **redacted** prompt is what
hydrates, persists, and reaches the model) → `selectClient` (gateway model selection) →
`chatBuildSystemPrompt` (working memory + facts FTS5 + semantic RAG hydration) → `chatPersistUserTurn`
(transactional-outbox user-turn persist) → `chatReflect` (**fail-closed** structured-output reflection
loop) → output guardrail → `chatRecordTurn` (assistant-turn persist + cost record). Returned errors
are `huma.StatusError` values — they carry an HTTP status but are ordinary Go errors off the HTTP
path (inspect with `huma.GetErrorStatus` if the status matters). `ChatDeps` bundles the pipeline's
dependencies (`Client, WM, Store, Limiter, ToolReg, Persona, Meter, GW, Guard`), one-for-one the
values `Register` receives, with identical nil semantics. The HTTP handler's closure now simply calls
`ChatCore`; the pipeline lives in exactly one place.

### 2. `(*App).startRuntime` — the single runtime-construction path for `Run` and `Turn`

Runtime construction is now one method ([`aisotropy/app.go`](../../aisotropy/app.go)):

```go
func (a *App) startRuntime(ctx context.Context) (*appRuntime, error)
```

It builds the shared core — working memory, tier routing, facts + semantic stores (through the shared
`ensureSemantic`, ADR-0018), the consolidation sink + outbox worker (+ cold-boot reconciliation), OTel
tracing, the LLM circuit breaker, cheap-model degradation, the sleep-cycle + turn-prune + bitrate
schedulers, per-tenant rate limiting, MCP tool discovery, the scope-gated tool registry, the persona
provider, the cost meter, guardrails, and the model gateway — in **byte-for-byte the order `Run` used
to inline**, and returns an `appRuntime` with a `closeFn` performing the full teardown (workers
stopped **before** the stores they write to, MCP clients closed, tracer shut). The `appRuntime`'s
fields are the union of what both entry points need: the chat pipeline deps (which `Turn` projects to
`ChatDeps` via `chatDeps()`) plus the HTTP-only extras (`streamClient`, `cheapStreamClient`,
`typedHandlers`, `executor`, `frictionDetector`, `tracerProvider`, `bitrateStore`, `bptSampler`) that
only `Run` layers routes on. `Run` calls `startRuntime` and builds the HTTP surface on top of the
returned runtime, owning its teardown; the construction it used to inline is unchanged in effect.

### 3. `App.Turn` — the in-process, `run_sync`-shaped entry point

([`aisotropy/turn.go`](../../aisotropy/turn.go)):

```go
type ChatInput struct {
	TenantID, UserID, Prompt, SessionID, ProductID, ModelPreference string
	Scopes []string
	Images []string
}
type ChatOutput struct { Reply, TenantID string; Stub bool }

func (a *App) Turn(ctx context.Context, in ChatInput) (ChatOutput, error)
```

`Turn` calls `ensureRuntime` (lazy build-once, below), maps the flat `ChatInput` onto the HTTP
contract type `api.ChatInput` (identity on the top-level fields where `chatIdentity` reads it,
everything else as the body — exactly as the HTTP handler populates it from JSON), and calls
`api.ChatCore(ctx, rt.chatDeps(), apiIn)`. The result is flattened to `ChatOutput`. It is the same
pipeline the HTTP `/chat` route runs, minus the HTTP hop. `ChatInput.Scopes` gate tools directly
(they flow to the pipeline's effective scopes as the body scopes — the fallback used when no verified
principal is in `ctx`); `ChatInput.Images` wires the same gateway `ImageRule` routing signal ADR-0030
added (routing only — image bytes are still not transmitted to a text-only model).

### 4. Build once, reuse across turns; `Close` tears it down

`ensureRuntime` ([`aisotropy/turn.go`](../../aisotropy/turn.go)) uses `turnOnce sync.Once` to boot the
shared runtime on the first `Turn` and memoize it (and any build error, so a doomed construction is
not retried), mirroring `semanticOnce`. The build uses `context.Background()` so the runtime and its
workers outlive the first call's `ctx`; each `Turn` still passes its own `ctx` to `ChatCore` for
per-call cancellation. Because the one runtime (hence the one working-memory store) is reused, two
`Turn` calls on the same `(TenantID, UserID, SessionID)` accumulate in the same buffer — **working
memory persists across turns**, the in-process analogue of two HTTP requests on one session.

`App.Close` ([`aisotropy/knowledge.go`](../../aisotropy/knowledge.go)) is extended to tear down a
`Turn`-built runtime: if `a.runtime != nil` it delegates to `rt.close(bounded ctx)` (which also closes
the semantic store `ensureSemantic` memoized, so it is not double-closed); otherwise it closes the
lazily-built semantic store alone (the SDK-only path). It is **idempotent** (`closeOnce`) and
**nil-safe** (an App that called neither `Turn` nor `Run` returns `nil`). A `Run`-served App manages
its own graceful-shutdown teardown and does not need `Close`; an embedded consumer that uses `Turn`
should `defer app.Close()`.

### Honest scope — the one real difference, and why isolation still holds

`Turn` runs **below** the HTTP boundary, so **no HTTP auth middleware executes** — there is no
`BindAuthMiddleware` / must-agree bind / default-deny gate on this path, because those exist to
authenticate an **untrusted network client**. In-process, the caller is the consumer's **own trusted
Go code**: it supplies `TenantID`/`UserID`/`Scopes` directly, exactly as the code that mints a JWT for
the HTTP path is already inside the trust boundary. This is the deliberate, documented trust model of
an embedded library — the same posture `WithAuthenticator(NoAuth())` makes explicit for local HTTP
use, but intrinsic in-process because there is no wire to authenticate across.

Tenant **isolation between tenants still holds by construction**, and this is the load-bearing point:
`TenantID`/`UserID` enter `ctx` inside `ChatCore` (via `chatIdentity`) and **never travel any other
way**; every memory tier derives its scope from that `ctx`. A different `TenantID` on a later `Turn`
call cannot read the first tenant's data — the guarantee is structural, independent of *who* supplied
the identity. What the caller controls is *asserting* the identity (as any trusted mint-side code
does); what it cannot do is *cross* tenants once asserted.

## Consequences

- **Library-first DX: the agent is a function again.** `New(...)` then `app.Turn(ctx, in)` returns a
  reply inline — no server, no port, no goroutine, no SSE reassembly. This is the `run_sync`-shaped
  path the bake-off found missing; the package's godoc `Example`s now read as `New` → `Turn` → print
  ([`aisotropy/example_test.go`](../../aisotropy/example_test.go)), and `Run` is presented as the
  "serve it over HTTP" option, not the only door.
- **One pipeline, one wiring, provably parallel — no drift.** The HTTP and in-process paths share
  `ChatCore` (body) and `startRuntime` (construction), so a change to any constitutional gate lands in
  exactly one place. The in-process path is proven at the store level to persist memory across turns
  and to isolate tenants ([`aisotropy/turn_test.go`](../../aisotropy/turn_test.go)), the same
  guarantee the black-box HTTP probes assert over the network
  ([`aisotropy/isolation_e2e_test.go`](../../aisotropy/isolation_e2e_test.go)).
- **A real lifecycle for embedded consumers.** `Turn`'s runtime is built once and reused (working
  memory persists, workers boot once), and `App.Close` releases it deterministically, idempotently,
  and nil-safely. `defer app.Close()` is the whole ceremony.
- **An honest, documented security nuance.** In-process identity is caller-supplied — no auth
  middleware runs. This is correct for a library (the caller is inside the trust boundary) but must be
  stated plainly so no one mistakes `Turn` for an authenticated surface: it is not, by design.
  Isolation between tenants is unchanged and structural. The `README` Quickstart, the
  [build-your-own-agent guide](../guides/build-your-own-agent.md), and the `ChatInput` doc comment all
  carry this note.
- **Additive and default-safe.** No new dependency, no OpenAPI contract change, and a `Run`-served App
  is byte-for-byte unchanged (the runtime construction moved verbatim; `Run` builds the same runtime
  and layers the same HTTP surface). A consumer that never calls `Turn` sees no behavioral difference.
- **Named follow-ons (honest gaps).** An in-process **streaming** `Turn` (token channel, the embedded
  analogue of `/chat/stream`) and in-process access to the agent/ask surfaces are not in this cut. The
  chat turn is the shape `run_sync` parity demanded first.

## References

- **Extends:** [ADR-0013](0013-public-extension-surface-facade-vs-promote.md) (the `aisotropy.App`
  builder façade — this ADR adds the embeddable `Turn` surface it named as pending) ·
  [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) (the dual-surface / sealed-runtime
  direction; `Turn` is the L1-embeddable call over the sealed runtime)
- **Invariants held:** [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract;
  `ChatInput`/`ChatOutput` are flat Go structs, the HTTP OpenAPI contract is untouched) ·
  [ADR-0011](0011-tenant-scoped-statement-builder.md) (tenant isolation by construction, preserved on
  the in-process path — identity enters `ctx` and never travels otherwise) ·
  [ADR-0024](0024-authenticated-identity-seam.md) (the HTTP authn seam `Turn` deliberately runs below)
- **Reused signals:** [ADR-0018](0018-knowledge-ingestion-surface.md) (`semanticOnce` build-once idiom
  `turnOnce` mirrors; the shared semantic store `Close` delegates to) ·
  [ADR-0030](0030-jwks-rotation-image-signal-stateless-ask.md) (`ChatInput.Images` reuses the gateway
  `ImageRule` routing signal — routing only, no image bytes to the model)
- **Pipeline + wiring extraction:** [`internal/api/core.go`](../../internal/api/core.go)
  (`ChatCore`, `ChatDeps`) · [`aisotropy/app.go`](../../aisotropy/app.go) (`startRuntime`, `appRuntime`,
  `Run` now layering the HTTP surface on top) · [`internal/api/chat.go`](../../internal/api/chat.go)
  (`Register` now calling `ChatCore`)
- **In-process surface:** [`aisotropy/turn.go`](../../aisotropy/turn.go) (`Turn`, `ChatInput`,
  `ChatOutput`, `ensureRuntime`, `chatDeps`) · [`aisotropy/knowledge.go`](../../aisotropy/knowledge.go)
  (`Close` extended to tear down the `Turn`-built runtime)
- **Proof:** [`aisotropy/turn_test.go`](../../aisotropy/turn_test.go)
  (`TestTurnRunsPipelineInProcess`, `TestTurnMemoryPersistsAcrossTurnsInProcess`,
  `TestTurnIsolationNoCrossTenantLeak`, `TestTurnRejectsInvalidIdentity`,
  `TestTurnConcurrentFirstCallsLazyInitRace`, `TestCloseIdempotentWithoutTurn`) ·
  [`aisotropy/example_test.go`](../../aisotropy/example_test.go) (godoc Examples now `run_sync`-shaped)
