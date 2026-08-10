---
type: Concept
title: Runtime as built — what aisotropyd actually wires and runs
description: A code-grounded description of the aisotropyd runtime as it is actually assembled in aisotropy/app.go (App.Run) — the HTTP surface, the streaming request lifecycle, the async outbox/sleep-cycle side, the memory tiers, the Phase 7 granular-control machinery, and an honest ledger of what is not yet wired.
tags: [architecture, as-built, runtime, request-lifecycle, outbox, quota, circuit-breaker, cost-meter, phase-7]
timestamp: 2026-07-10T00:00:00Z
---

# Runtime as built — what aisotropyd actually wires and runs

This document describes the aisotropyd runtime **as the code assembles it today**, verified by
reading the source rather than the design docs. Where the running code and the aspirational
architecture docs disagree, this document follows the **code** and flags the gap. Its companions
describe the *intended* design: [overview](overview.md), [request lifecycle](request-lifecycle.md),
[tiered memory](memory-tiers.md), [consistency](consistency.md), [multi-tenancy](multi-tenancy.md),
[granular control](granular-control.md), [observability](observability.md). This is the honest
"here is what boots" counterpart.

Everything below is anchored in the composition root. **As of Phase 8 (ADR-0013) that root moved**:
the wiring now lives in the importable builder [`aisotropy/app.go`](../../aisotropy/app.go)
(`App.Run` constructs every tier, decorator, and route), and
[`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) is a ~97-line thin consumer of it. Read
this doc together with the one-screen [reality ledger](reality-ledger.md) (REAL vs dormant vs
aspirational) and the [data model](data-model.md) / [API reference](api-reference.md).

> **Freshness note (2026-07-10):** this document is being reconciled to recent work. Corrections
> since it was first written, now reflected below: (1) the wiring point is `aisotropy/app.go`, not
> `main.go`; (2) the agent endpoint now supports **native provider tool-calling** via Gollem
> `Agent[T]` when the client implements `llm.ToolCaller` (ADR-0012) — the old "no native
> function-calling" claim is corrected in §6; (3) knowledge-ingestion routes exist
> (`POST/DELETE .../knowledge`, user- and tenant-scoped, ADR-0018/0019) beyond the three chat routes
> tabled below; (4) the semantic tier is multi-kind (Document/Fact/Turn, ADR-0017). Where a detail
> here still lags the code, the [reality ledger](reality-ledger.md) is the arbiter.

> **Freshness note (2026-07-14):** second reconciliation pass, all claims re-verified against
> `aisotropy/app.go` at HEAD: (1) the §1 route table is now exhaustive — it adds `GET /health`,
> the conditional `GET /bitrate` (Bet 6), and the four knowledge routes that were previously only
> mentioned in prose; (2) every wiring attribution now points at `App.Run` in `aisotropy/app.go`
> (with verified line references) instead of `main.go`; (3) the LLM client selection is now the
> multi-provider registry (`internal/llm/registry.go`), precedence
> `OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub` — not Anthropic-or-stub;
> (4) `AISOTROPY_RATE_PER_SEC` / `AISOTROPY_BURST` are read by `ratePerSecFromEnv` /
> `burstFromEnv` in `app.go` and only apply when `WithRateLimit` was not used; (5) the outbox
> description now covers the Bet 6 held-out bitrate sampling exception and the fact that
> assistant turns never carry an outbox event; (6) per-user primitive friction consolidation is
> wired (no longer "fully un-wired") — see the amended §6 bullet for what still isn't closed.

> **Freshness note (2026-07-14, authn seam — [ADR-0024](../decisions/0024-authenticated-identity-seam.md)):**
> the authenticated-identity seam shipped, so the earlier "path tenant/user are trusted with no
> credential" posture no longer holds by construction. Reflected below: (1) a single **fail-closed
> auth middleware** now runs as one Huma API middleware in front of **every** `/v1` route
> ([`internal/api/authn.go`](../../internal/api/authn.go), wired at
> [`app.go:412-417`](../../aisotropy/app.go)) — either the configured `Authenticator` with a
> must-agree bind (verified principal == URL path tenant/user), or a default-deny gate when none is
> registered; the health/liveness/costs/openapi/docs routes stay outside it (see §1); (2) tool-gating
> now resolves scopes through `effectiveScopes` — the **verified principal's** scopes when a real
> authenticator ran, falling back to client body scopes only under NoAuth / no middleware (see §2 and
> §5); (3) the shipped binary is **securable by config** via `AISOTROPY_JWT_SECRET` (see the env
> table). Line references are HEAD-accurate as of this pass; other line numbers in this doc may lag.

> **Freshness note (2026-07-14, security hardening — [ADR-0025](../decisions/0025-security-hardening-authn-ops.md)):**
> a security review of the ADR-0024 seam added two more request-path gates, both reflected below:
> (1) the operator endpoints `GET /costs` and `GET /bitrate` (all-tenant aggregates, registered as raw
> chi routes *outside* the `/v1` auth chain) are now guarded **default-closed** by `api.OpsAuthGate`
> ([`internal/api/ops_gate.go`](../../internal/api/ops_gate.go), wired via `router.With(opsGate)` at
> [`app.go:504-508`,`540`](../../aisotropy/app.go)) — 503 when `AISOTROPY_OPS_TOKEN` is unset, else a
> constant-time bearer-token check (401 on miss); (2) an **optional** plaintext-rejection middleware
> (`AISOTROPY_REQUIRE_TLS`, [`app.go:403-413`](../../aisotropy/app.go)) rejects non-TLS requests with 400.
> The HS256 verifier also gained a 32-byte-minimum secret (fail-fast at boot), optional `aud`/`iss`
> binding, and a max-TTL cap — see the env table. All stdlib, no new dependency.

> **Freshness note (2026-07-15, capabilities integration — [ADR-0028](../decisions/0028-capabilities-integration.md)):**
> the three book-gap capability packages ADR-0027 shipped as standalone/CODE-ONLY are now wired (or
> deliberately not), reflected in §2, §5, §6 and the env table below: (1) **guardrails**
> ([`internal/guardrails`](../../internal/guardrails), bundled as `api.Guard` in
> [`internal/api/guard.go`](../../internal/api/guard.go)) run on **all three** chat routes — input
> redaction + injection/output **detection ALWAYS**, but **blocking is OPT-IN** behind
> `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` / `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` (default **off** =
> annotate + log); streaming output is annotate-only (a stream can't be un-sent). (2) The
> **model-selection gateway** ([`internal/gateway`](../../internal/gateway), `selectClient` in
> `guard.go`) is wired on the `chat` and `chat/stream` routes: its **cost-budget** policy is live when
> `AISOTROPY_COST_BUDGET_USD` **and** a cheap client are set, its request-shape rules are
> **ready-but-inert** (no image/UI-selector signal on `ChatInput` yet), it is **not** the refuted
> Bet-4 difficulty router (ADR-0021), and the streaming quota-pressure degrade still applies as an
> override on top of the gateway pick. (3) The **response cache**
> ([`internal/responsecache`](../../internal/responsecache)) is **deliberately NOT wired** into the
> per-user-hydrated chat path (a prompt-keyed cache over a memory-hydrated response is a
> staleness/isolation footgun) and stays CODE-ONLY, a standalone primitive for stateless flows.
> Guardrails + gateway move CODE-ONLY → REAL; the cache stays CODE-ONLY.

> **Freshness note (2026-07-15, four named follow-ons — [ADR-0029](../decisions/0029-followons-rs256-streaming-signals-judge.md)):**
> the follow-ons ADR-0024/0025 and ADR-0028 deferred are now built, all stdlib-only and default-off,
> reflected in §1, §2, §6 and the env table: (1) **RS256/JWKS auth** — `AuthenticatorFromEnv`
> ([`aisotropy/authenv.go`](../../aisotropy/authenv.go)) now selects a second, asymmetric verification
> family from `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` (static RSA key) or `AISOTROPY_JWT_JWKS_URL` (JWKS,
> fetched/TTL-cached/`kid`-matched with `math/big`, fail-closed), with the **algorithm-confusion
> defence** that the *configured* mode fixes the expected `alg` — never the token
> ([`internal/auth/jwt.go`](../../internal/auth/jwt.go), [`jwt_rs256.go`](../../internal/auth/jwt_rs256.go)).
> Exactly one mode may be configured. (2) **Streaming hard output-blocking** — with
> `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` on, `chat/stream` now **buffers** the whole response (no live
> flush) so a flagged reply can be replaced with a refusal before a byte is sent (latency-for-safety,
> opt-in); off (default) keeps the live flush + annotate-only path. (3) **`ChatInput.model_preference`**
> — an **additive `omitempty`** body field (existing clients byte-for-byte unaffected, ADR-0002 held)
> mapped to `gateway.Request.UISelector`, so `gateway.OverridePolicy` (now first in the chain) honors an
> explicit target over the cost-budget rule; it is a preference not authorization, an unknown name
> resolves to the default, and the streaming quota-pressure `DegradeAt` still overrides it. (4) **eval
> `LLMJudge` on the output guardrail** — opt-in behind `AISOTROPY_GUARDRAILS_LLM_JUDGE`, attached via the
> `api.EvalOutputCheck` adapter over the `guardrails.OutputCheck` seam (`api → {eval, guardrails}`;
> guardrails never imports eval), driven by the **raw** `llmClient` (not the circuit-broken chat client),
> with judge-error posture tracking the blocking posture (fail-open when annotating, fail-closed when
> hard-blocking). All four opt-in / default-off; no new dependency, no contract break.

> **Freshness note (2026-07-15, JWKS rotation + image signal + stateless `/ask` — [ADR-0030](../decisions/0030-jwks-rotation-image-signal-stateless-ask.md)):**
> three seams ADR-0029 left unproven or inert are now closed, reflected in §1, §2, §5, §6 and the env
> table, all stdlib-only and default-safe: (1) **JWKS key rotation is proven end-to-end** over the shipped
> `jwksSource` — a rotated-in signing key is picked up via the bounded unknown-`kid` refetch, a rotated
> whole set is refreshed at the TTL boundary, and an unknown-`kid` burst is throttled to one fetch per
> window ([`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go),
> [`jwt_rs256_test.go`](../../internal/auth/jwt_rs256_test.go)); nothing in the key-handling code changed —
> this adds the tested property.
>
> **Corrected and extended 2026-08-10.** This entry used to add that "the throttle bounds failures too,
> since `refreshLocked` stamps `lastFetch` even on failure". That was broader than the code: `lastFetch`
> gated only the unknown-`kid` refetch, so a **TTL-expired** cache re-attempted the fetch on *every*
> request while the IdP was down — one upstream call per request, precisely when it could least be
> afforded. Worse, the refresh error was discarded whenever any cached set existed, so a key the IdP had
> **rotated out** kept verifying tokens for as long as the endpoint stayed broken, silently. Both are now
> closed: a **bounded staleness window** (`WithJWKSMaxStaleness`, default 30m on top of the 5m TTL) serves
> the cache through a blip and then drops it and rejects, and a failing refresh is retried no more than
> once per `minRefetch` — gated on the last **failure**, never the last attempt, so a healthy endpoint past
> its TTL still refreshes immediately (`jwks_staleness_test.go`). The residual, stated rather than implied:
> a rotated-out key remains valid for up to **ttl+maxStale** — bounded, not zero. (2) **`ChatRequest.images`** — an **additive `omitempty`** `[]string`
> presence signal (existing clients byte-for-byte unaffected, ADR-0002 held) — activates the gateway's
> previously-inert capability **`ImageRule`**: `len(images) > 0 → gateway.Request.HasImage`, routed to the
> **`AISOTROPY_VISION_MODEL`** target (new env var; empty → primary, so routing is unchanged when unset).
> **HONEST SCOPE:** routing **signal only** — the text-only `llm.Client`s do NOT transmit image bytes to
> the model; the multimodal call is the remaining follow-on. (3) The **new stateless
> `POST /v1/.../ask`** route makes [`internal/responsecache`](../../internal/responsecache) **LIVE** — no
> working-memory hydration, no session, no consolidation outbox, so caching is SAFE (no memory-hydrated
> personalization → no staleness footgun); per-`(tenant, user)` isolation by construction in both cache
> layers; behind the SAME authn must-agree bind as `/chat`. The **stateful** chat path stays uncached by
> decision. Response cache moves CODE-ONLY → **REAL** (on `/ask` only); the image rule moves
> ready-but-inert → **REAL**; no new dependency, no contract break.

## 1. The HTTP surface (every route actually registered)

`App.Run` builds a `chi` router (`middleware.RequestID`, `middleware.Recoverer`, plus — **only when
`AISOTROPY_REQUIRE_TLS` is truthy** — a pre-route plaintext-rejection `Use` middleware that 400s any
request with `r.TLS == nil`, [`app.go:403-413`](../../aisotropy/app.go), ADR-0025; OFF by default for
behind-a-proxy TLS termination), mounts a Huma v2 API on it via the `humachi` adapter
(`huma.DefaultConfig("Aisotropy", "0.1.0")`), and registers the following — nothing else:

| Route | Where registered | In OpenAPI contract? | What it does |
|---|---|---|---|
| `GET /healthz` | `app.go:390` (raw chi handler) | No | Liveness only. Writes `{"status":"ok"}` with HTTP 200. No dependency checks. |
| `GET /health` | `app.go:507` (raw chi handler, [`internal/health`](../../internal/health/health.go)) | No | Readiness. Cheap, read-only, `_health`-tenant-scoped probes of the facts + semantic tiers (2s per-probe timeout). 200 `ok` / 503 `degraded`. Deliberately never probes the LLM (no tokens spent on polling). |
| `GET /costs` | `app.go:508` (raw chi handler, **`router.With(opsGate)`**) | No | JSON-encodes `costMeter.Snapshot()` — the per-tenant token/cost aggregate (see [`internal/costmeter`](../../internal/costmeter/costmeter.go)). Sorted by tenant id. **All-tenant data → gated default-closed** by `api.OpsAuthGate` (ADR-0025): 503 until `AISOTROPY_OPS_TOKEN` is set, then a constant-time bearer-token check (401 on miss). |
| `GET /bitrate` | `app.go:540` (raw chi handler, **`router.With(opsGate)`**) — **only when `AISOTROPY_BITRATE_BIN` is set** | No | Bet 6 ops view: JSON-encodes `bitrateStore.AllSummaries()` — per-tenant bits-per-token memory-lift summaries. Absent (404) when the bitrate path is disabled; same default-closed `AISOTROPY_OPS_TOKEN` gate as `/costs` when present. |
| `GET /openapi.json`, `/openapi.yaml`, `GET /docs` | Huma `DefaultConfig` | n/a (they *are* the contract) | Auto-generated OpenAPI 3.1 schema + Stoplight docs UI. The Go struct is the single source of truth. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat` | `api.Register` ([`chat.go`](../../internal/api/chat.go)) | Yes (`operationID: chat`) | Non-streaming, structured-output chat. Runs the reflection loop and **fails closed** with 502 on a persistent schema violation. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/stream` | `api.RegisterStream` ([`chat_stream.go`](../../internal/api/chat_stream.go)) | Yes (`operationID: chat-stream`) | SSE streaming chat, token by token. The full memory + outbox + cost path. Emits `token` / `done` / `error` events. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agent` | `api.RegisterAgent` ([`chat_agent.go`](../../internal/api/chat_agent.go)) | Yes (`operationID: chat-agent`) | Bounded structured-output tool loop over scope-permitted tools. Returns the ordered list of tool decisions + results. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/aisdk` | `api.RegisterAISDK` ([`aisdk.go`](../../internal/api/aisdk.go)) | Yes (`operationID: chat-aisdk`) | Vercel AI SDK "UI Message Stream" wire adapter: decodes a `messages[]` transcript, runs the SAME `StreamCore` pipeline, re-encodes as the AI SDK SSE part protocol (`x-vercel-ai-ui-message-stream: v1`, terminates `data: [DONE]`). |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/chat/agui` | `api.RegisterAGUI` ([`agui.go`](../../internal/api/agui.go)) | Yes (`operationID: chat-agui`) | AG-UI protocol adapter (CopilotKit et al.): `RunAgentInput` in, AG-UI event stream out (`RUN_STARTED … TEXT_MESSAGE_* … RUN_FINISHED`), same `StreamCore` pipeline. |
| `POST /v1/chat/completions` | `api.RegisterOpenAICompat` ([`openai_compat.go`](../../internal/api/openai_compat.go)) | Yes (`operationID: openai-chat-completions`) | OpenAI Chat-Completions compat shim (ADR-0066): streaming (`chat.completion.chunk` + `[DONE]`, `stream_options.include_usage` honored) and non-streaming, text-only v1 floor — never the Responses API. **The one route outside the tenant/user prefix**: identity comes exclusively from verified JWT claims via the principal-derived mode of `BindAuthMiddleware` ([`authn.go`](../../internal/api/authn.go)); NoAuth cannot serve it (no path identity to copy → 401 by construction), nil authenticator stays deny-all. Quota rejection maps to 429/`rate_limit_error`. Third wire costume over `StreamCore`; enabler for Pipecat voice + self-hosted chat UIs (`examples/voice-pipecat/`). |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/ask` | `api.RegisterAsk` ([`ask.go`](../../internal/api/ask.go)) | Yes (`operationID: ask`) | **Stateless**, response-cached question (ADR-0030). NO working-memory hydration, no session, no consolidation outbox — its only context is tenant-scoped semantic RAG, which is exactly what makes caching safe. Consults a per-`(tenant, user)` response cache first (HIT → cached answer, **no LLM call**); on a MISS builds RAG context, makes one LLM call, runs the output guardrail, caches under the `(tenant, user)` scope, and returns. Cross-tenant hit impossible by construction ([`internal/responsecache`](../../internal/responsecache)). `AskOutput.cached` echoes whether the cache served it. |
| `POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge` | `api.RegisterKnowledge` ([`knowledge.go`](../../internal/api/knowledge.go)) | Yes (`operationID: ingest-knowledge`) | User-scoped knowledge ingestion (ADR-0018): chunks + embeds a document into `KindDocument` vectors, sharing the exact store/embedder pair chat RAG reads. |
| `DELETE /v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}` | `api.RegisterKnowledgeDelete` ([`knowledge.go`](../../internal/api/knowledge.go)) | Yes (`operationID: delete-knowledge`) | Removes a user-scoped document's chunks by id. |
| `POST /v1/tenant/{tenant_id}/knowledge` | `api.RegisterTenantKnowledge` ([`knowledge.go`](../../internal/api/knowledge.go)) | Yes (`operationID: ingest-tenant-knowledge`) | Tenant-shared knowledge ingestion (ADR-0019): a corpus ingested once, retrievable by every user of that tenant via the merged RAG search. |
| `DELETE /v1/tenant/{tenant_id}/knowledge/{doc_id}` | `api.RegisterTenantKnowledgeDelete` ([`knowledge.go`](../../internal/api/knowledge.go)) | Yes (`operationID: delete-tenant-knowledge`) | Removes a tenant-shared document's chunks by id. |

`tenant_id` is a **path param** on every `/v1/...` route except the OpenAI-compat shim (whose
identity is exclusively the verified JWT principal — see its row above and ADR-0066), and
`user_id` on every user-scoped one
(the two tenant-shared knowledge routes carry only `tenant_id`), charset-constrained at the
contract level (`pattern:"^[a-zA-Z0-9_-]{1,64}$"`, Huma rejects with 422 before the handler
runs). On the chat routes, `session_id` and `scopes` travel in the request body (`ChatRequest`): a
session id carries no authorization meaning, and the body `scopes` are the caller-asserted
permissions — honored only as a fallback (see the auth gate below and §5); under a real
authenticator the *verified* principal's scopes win.

### The fail-closed auth gate in front of every `/v1` route (ADR-0024)

Before any `/v1` operation runs, `App.Run` installs **one** Huma API middleware via
`humaAPI.UseMiddleware`, registered *before* the first `huma.Register` call
([`app.go:412-417`](../../aisotropy/app.go)) so it wraps every operation:

- If an `Authenticator` is registered (`aisotropy.WithAuthenticator`), the gate is
  `api.BindAuthMiddleware` ([`internal/api/authn.go`](../../internal/api/authn.go)): it authenticates
  the caller's credential, then enforces a **must-agree bind** — the verified principal's
  tenant/user MUST equal the URL path tenant/user. A missing/invalid credential is **401**, a
  mismatch is **403**, and on either the handler never runs (fail-closed). On success it injects the
  verified principal (tenant/user + verified scopes) into `ctx` before the handler.
- If **no** authenticator is registered, the gate is `api.DenyAllMiddleware`: every `/v1` request is
  rejected **401** (default-deny) — running unauthenticated is never the silent default; it is an
  explicit opt-in via `NoAuth()`. The shipped `aisotropyd` binary opts in through
  `AuthenticatorFromEnv`, which selects **exactly one** verification mode from the environment (more
  than one refuses to boot): `AISOTROPY_JWT_SECRET` → **HS256** bearer verification;
  `AISOTROPY_JWT_PUBLIC_KEY[_FILE]` → **RS256** against a static RSA public key;
  `AISOTROPY_JWT_JWKS_URL` → **RS256** against a JWKS endpoint (ADR-0029); unset → `NoAuth()` with a
  startup warning (see [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) and the env table). The
  **algorithm-confusion defence**: the *configured* mode fixes the accepted `alg` and a mismatched
  header is rejected before any signature check — the token can never steer an RS256-configured verifier
  onto the HMAC path ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)).

The `GET /healthz`, `GET /health`, `GET /costs`, `GET /bitrate`, `/openapi.json` and `/docs` routes
are registered directly on chi (or via the Huma adapter's `Handle` for openapi/docs), **outside** the
`/v1` operation middleware chain `UseMiddleware` feeds — that per-`(tenant, user)` auth gate covers only
the `/v1` operations registered through `huma.Register`. `healthz`/`health`/`openapi`/`docs` carry no
tenant data and stay open, but `GET /costs` and `GET /bitrate` return **all-tenant aggregates**, so they
carry their **own** default-closed operator gate — `api.OpsAuthGate(AISOTROPY_OPS_TOKEN)` (ADR-0025), a
shared bearer token compared constant-time, 503 until the token is set (see §5 and the route table above).

### Contract shapes

- **`ChatRequest`** (body): `prompt` (maxLength 2000), `product_id`, `session_id`, `scopes[]`,
  `model_preference` (optional, `omitempty`, ADR-0029 — an explicit gateway-target override), and
  `images[]` (optional, `omitempty`, ADR-0030 — a presence signal that routes the turn to the vision
  target via the `ImageRule`; **routing only**, image bytes are not sent to the model yet). Both new
  fields are additive, so existing clients are byte-for-byte unaffected.
- **`ChatOutput`** (non-streaming): `reply`, `tenant_id` (isolation echo), `stub` (true when the
  resolved LLM client is the credential-less stub).
- **SSE events** (streaming): `token{text}`, `done{tenant_id}`, `error{message}`.
- **`AgentOutput`**: `tenant_id`, `steps[]` where each step is `{tool, args, result}`.
- **`AskRequest`** (body, ADR-0030): `prompt` (maxLength 2000), `product_id` — **no** `session_id`
  (stateless) and **no** `scopes[]` (injects no tools). **`AskOutput`**: `reply`, `tenant_id` (isolation
  echo), `cached` (true when served from the response cache with no LLM call), `stub`.

## 2. The streaming request lifecycle, step by step as coded

Before the handler runs, the API-level **auth gate** (§1, ADR-0024) has already authenticated the
caller, enforced the must-agree bind, and injected the verified principal into `ctx` — or rejected
the request 401/403 so the handler is never reached. This is the `chat/stream` handler in
[`chat_stream.go`](../../internal/api/chat_stream.go), in exact execution order:

1. **Identity validation.** `withIdentity(ctx, tenant, user, session)` validates all three
   components through `memory.Key.Validate()` (non-empty, none of the reserved chars `:`, `\x00`,
   `\n` — see [`internal/memory/key.go`](../../internal/memory/key.go)) and injects tenant/user into
   `ctx`. It then also calls `llm.WithTenant` / `llm.WithUser` so the OTel decorator can attribute
   spans without `internal/llm` importing `internal/api`. A `facts.Scope` is built via
   `facts.NewScope(tenant, user)`, which routes identity through
   [`internal/tenancy`](../../internal/tenancy/scope.go) (ADR-0011). On any failure the handler
   emits an SSE `error` event and returns.
2. **Rate-limit gate (429-equivalent).** If a `*memory.Limiter` is present, `limiter.Allow(ctx,
   tenant)` takes one token from the per-tenant bucket **before any LLM call**. A denied request
   emits an SSE `error` ("rate limit exceeded…") and returns — no tokens spent. (See
   [`internal/memory/quota.go`](../../internal/memory/quota.go).)
3. **Input guardrail (redact + detect ALWAYS; block OPT-IN, ADR-0028).** If an `api.Guard` is wired
   (it is, in `App.Run`), `guard.checkInput` runs `guardrails.CheckInput(prompt)`
   ([`internal/api/guard.go`](../../internal/api/guard.go),
   [`internal/guardrails`](../../internal/guardrails)). The **redacted** text it returns
   (`forwarded`) — never the raw prompt — is what every downstream step uses (hydration, the outbox /
   held-out sample, and the model call), so a secret in the prompt never reaches the model nor
   persists in any memory tier. A `Suspicious` injection verdict is **annotated + logged** by default
   and only **rejected** (SSE `error`) when `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` is truthy. Logging
   is redaction types/counts + injection signal names only, never raw values.
4. **System-prompt hydration (PARALLEL since [ADR-0076](../decisions/0076-as-built-deltas-parallel-hydration-shutdown-join.md)).**
   `hydrateSystemPrompt` assembles the system prompt from the **redacted** `forwarded` text. The
   persona base prompt is read first (its failure is a hard precondition, so no store is touched when
   it fails); the three memory tiers below are then read **concurrently** — they are independent
   reads of one `(tenant, user, session)` and none consumes another's output, so hydration costs
   max(tier) instead of sum(tier). All but the last live tier are fanned out over goroutines joined
   by a `sync.WaitGroup`; the last runs inline on the request goroutine, so a wiring with a single
   live tier spawns nothing. Every goroutine inherits the **same request ctx** (one tenant, injected
   at the Huma boundary) and writes only its own result slot — race-free by construction, no widened
   scope. The failure semantics below are unchanged by the concurrency:
   - base prompt from `product_id`;
   - working-memory `Summary(ctx, key)` (condensed buffer) and `Recent(ctx, key, 12)` (last 12
     active messages);
   - facts-memory `store.Search(ctx, scope, prompt, 4)` — FTS5 top-4 engrams ranked
     Relevance × Importance × Recency. **A facts failure is non-fatal** (logged, proceeds without
     facts); a working-memory failure *is* fatal (SSE error).
5. **Scope-gated tool fragment.** If a `*tools.Registry` is present, `PromptFragment(...)` appends
   only the tools the caller's scopes permit. The scopes are the **authoritative** ones resolved by
   `effectiveScopes(ctx, in.Body.Scopes)` ([`chat_stream.go`](../../internal/api/chat_stream.go),
   [`internal/api/authn.go`](../../internal/api/authn.go)): the **verified principal's** scopes when a
   real authenticator vouched for them (`ScopesVerified`), so a client cannot escalate by smuggling
   extra scopes into the body; the client-asserted body scopes are honored only as the fallback under
   NoAuth (or no auth middleware, as in unit tests that call `Register` directly). A tool the scopes
   do not grant is never mentioned — governance by absence of information
   ([`internal/tools/tools.go`](../../internal/tools/tools.go)).
6. **`AppendWithOutbox` (atomic message + consolidation intent).** The user turn and a pending
   consolidation event are written in **one BadgerDB transaction**
   ([`internal/memory/outbox.go`](../../internal/memory/outbox.go)). The event id is a stable
   sha256-derived `outboxRef(key, prompt)` so re-sending the identical prompt consolidates onto the
   same fact/vector. This happens *before* the LLM call, because the user's message already
   happened. **Bet 6 exception:** when `AISOTROPY_BITRATE_BIN` is set, a deterministic ~5% of user
   turns (`bitrate.ShouldSample` — FNV-1a hash of the outbox ref, 1 in 20) are reserved as held-out
   bitrate samples instead: the
   user message still enters working memory via plain `wm.Append`, but **no outbox event is
   enqueued** — the turn is `Reserve`d in the bitrate store and deliberately kept out of the
   facts/semantic consolidation pipeline, so the later memory-lift measurement is not contaminated
   by leakage.
7. **Model selection through the gateway seam (ADR-0028 / ADR-0029 / ADR-0030).** `selectClient(ctx, gw,
   client, meter, tenant, user, forwarded, in.Body.ModelPreference, len(in.Body.Images) > 0)`
   ([`internal/api/guard.go`](../../internal/api/guard.go), [`internal/gateway`](../../internal/gateway))
   runs the routing policy to pick the client for this turn, projecting the request down to a
   `gateway.Request` carrying the tenant's cumulative spend (from the shared cost meter), **the caller's
   `model_preference` as `UISelector`** (ADR-0029), **and `HasImage` from the presence of `images`**
   (ADR-0030). `gateway.OverridePolicy` — **first** in the chain — honors a non-empty preference as a hard
   target that **wins over** everything below; it abstains when the preference is empty. The
   **`ImageRule`** — chained next, **before** the cost rule — routes an image-bearing turn
   (`HasImage == true`) to the `AISOTROPY_VISION_MODEL` target **regardless of spend** (a multimodal
   request must not be shaped down to the cheap text model); it abstains when there is no image, so
   image-less requests route exactly as before, and an empty/unset `AISOTROPY_VISION_MODEL` makes the
   target the primary (routing unchanged). The **cost-budget** policy then shapes an over-budget tenant to
   the cheap target — live only when `AISOTROPY_COST_BUDGET_USD` **and** a cheap client are configured;
   otherwise the gateway is a clean pass-through to `primary`. There is **no** prompt-length/difficulty
   rule (Bet-4 refuted, ADR-0021). An unknown target name resolves to the default and a routing miss falls
   back to the passed client — a decision (or a bad client/env value) never drops a request. **HONEST
   SCOPE (ADR-0030):** the image rule wires **routing only** — the text-only clients do not transmit image
   bytes to the model; the multimodal `llm.Client` is the follow-on.
8. **Quota-pressure model degradation (OVERRIDE on the gateway pick).** If a cheap client *and* a
   limiter are both configured, `limiter.Pressure(ctx, tenant)` peeks the bucket and
   `llm.DegradeAt(pressure, 0.95, active, cheap)` may override the gateway-chosen client with the
   cheaper model instead of a hard rejection
   ([`internal/llm/degrade.go`](../../internal/llm/degrade.go),
   [`internal/memory/quota_pressure.go`](../../internal/memory/quota_pressure.go)). The gateway
   **complements**, never replaces, this Phase-7 degrade — and it overrides **even an explicit
   `model_preference`** (the safety valve wins), because it is applied *after* the gateway pick. Absent a
   cheap client, the gateway pick stands.
9. **LLM stream (circuit-breakered + OTel).** `active.StreamChat(ctx, system, prompt)` opens a token
   channel. The client is `WithOTel(WithCircuitBreaker(primary))` — one `gen_ai` span per call, and
   fast-fail once the breaker is open (see steps below).
10. **Drain loop.** For each token:
   - `ctx.Done()` (client disconnect / expiry) → stop immediately; the partial reply is **not**
     persisted. Token spend is cut by the producer observing the same `ctx.Done()`.
   - `tok.Err != nil` → emit SSE `error`, stop; partial reply not persisted.
   - `tok.Done` → **persist the assistant turn** via plain `wm.Append` (only a clean, complete
     stream is stored; assistant turns carry **no outbox event** on either chat route — only user
     turns are consolidated), handle the **output guardrail** per the buffering mode (below), **record
     cost** via `meter.Record` from `tok.Usage` (real provider token counts, if supplied), then emit
     `done`.
   - otherwise → accumulate text; forward the `token` event **only in live mode** (in buffered mode the
     token is accumulated but not flushed).

   The output-guardrail handling at `Done` has **two modes**, keyed on `guard.outputBlockingEnabled()`
   (i.e. `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT`, ADR-0029):
   - **Off (default) — live flush + annotate-only.** Tokens were flushed as they arrived; the assembled
     reply is inspected with `guard.annotateOutput`, which logs a tripped toxicity/leak verdict but
     **never blocks** — a streamed response is already sent and cannot be un-sent.
   - **On — buffered + hard-block.** Tokens were **accumulated without flushing**; at `Done` the whole
     assembled reply is inspected with `guard.checkOutput`, and **only** the resulting safe text (or a
     safe refusal if it tripped a guardrail) is emitted **and** persisted — the toxic/leaky text never
     reaches the wire nor memory, mirroring the non-streaming route. The trade-off is time-to-first-token
     (the client waits for the full response); it is the operator's opt-in.

The non-streaming `chat` handler ([`chat.go`](../../internal/api/chat.go)) is similar but:
- it uses the reflection loop `llm.ReflectAuto[chatStructuredReply]` (Gollem backend by default with
  a homegrown `Reflect[T]` fallback, ADR-0008) and **fails closed with HTTP 502** if no valid
  structured reply is produced after `chatMaxReflectionRetries = 3` attempts;
- it runs the **same input guardrail** (redact + detect; reject with 400 only when
  `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` is on) and routes through the **same gateway seam**
  (`selectClient`), but has **no** quota-pressure degrade override — the gateway pick is final on this
  route (ADR-0028);
- it applies the **output guardrail with hard-blocking capability**: after the reflection loop and
  **before** persisting/returning, `guard.checkOutput` may — when `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT`
  is on and the reply trips a toxicity/leak check — replace the flagged reply with a safe refusal,
  which is then what both persists to memory and returns to the client (default posture: annotate +
  log, reply unchanged). This is the enforcement asymmetry with the streaming route, which is
  annotate-only;
- it persists the user turn via `AppendWithOutbox` before the call,
  so non-streaming turns feed the same consolidation pipeline, and the assistant turn via plain
  `wm.Append` on success only — assistant turns never enqueue an outbox event. It
  does **not** apply the Bet 6 held-out sampling branch (that lives on the streaming route only);
- it uses the breaker-wrapped client **without** the OTel decorator (per ADR-0010 the reflection
  path is meant to use Gollem's own `ext/otel`, not yet adopted) — though routing through the gateway
  whose `primary` is the OTel-wrapped `streamClient` incidentally re-adds OTel when the gateway
  resolves to that target.

## 3. The async / offline side

### Outbox worker (at-least-once drain)

At boot, `App.Run` builds a `consolidation.Sink` ([`app.go:315`](../../aisotropy/app.go),
[`internal/consolidation/sink.go`](../../internal/consolidation/sink.go)) over the facts store +
`HeuristicExtractor` and the semantic store + `HashEmbedder`, then:

1. **Cold-boot reconciliation:** `wm.RecoverOutbox(ctx, sink)` drains every event orphaned by a
   previous shutdown *before* the server starts serving — same drain path, no special recovery mode.
2. **Steady state:** `go wm.RunOutboxWorker(workerCtx, sink, 1s)` drains pending events back-to-back
   when present, polling every second when empty, backing off exponentially (200ms → 30s) on store
   errors ([`internal/memory/outbox.go`](../../internal/memory/outbox.go)).

For each drained event `Sink.Consolidate` runs two sinks: the facts sink `Extractor`-distils the
event's `Content` (the user prompt) into facts and routes each through the `engram` facade
(ADR-0017 Phase 2), which upserts the fact **and** a `KindFact` vector keyed by the fact id
(dual-index, importance-gated); the semantic sink embeds the raw turn as a bounded `KindTurn` vector.
Every vector is kind-tagged — `PointID(sc, kind, sourceID)` encodes the kind so a fact's and a
turn's vector never collide (ADR-0017). An event key is deleted **only after both sinks succeed** —
at-least-once, idempotent on redelivery. Both nil tiers are skipped (facts-only or semantic-only
deployments are allowed).

### Sleep-cycle worker (DataFusion, Rust subprocess)

Only wired when `AISOTROPY_SLEEPCYCLE_BIN` is set. `App.Run` builds a `sleepcycle.Coordinator`
([`app.go:349`](../../aisotropy/app.go), facts store + `factEngram` facade +
`sleepcycle.Worker{BinaryPath, Tracer}`) and runs `RunScheduler(workerCtx, 1h, …)`.
Each pass ([`internal/sleepcycle/coordinator.go`](../../internal/sleepcycle/coordinator.go)):
`ExportAll` (a reviewed cross-tenant read) → write NDJSON to a scratch dir → invoke the Rust binary
as a subprocess (`--input`/`--output`, ADR-0005) → parse the JSON `Summary` from stdout → `Delete`
each dropped duplicate id **within its own (tenant,user) scope**. A Rust panic surfaces as a
non-zero exit, retried with backoff up to 3 attempts; a failure is logged, never fatal, and never on
the request path. The Rust worker now **exists** at [`crates/sleep-cycle`](../../crates/sleep-cycle)
(DataFusion dedup over exported facts, ADR-0005), but this path is still **dormant by default**: it
runs only when the binary is built and `AISOTROPY_SLEEPCYCLE_BIN` points at it.

## 4. The memory tiers as built

| Tier | Package | Default (zero-config) | Networked / production | Filled by | Read by |
|---|---|---|---|---|---|
| **Working** (L1) | [`internal/memory`](../../internal/memory/working_memory.go) | In-memory BadgerDB (no persistence) | On-disk BadgerDB at `AISOTROPY_DATA_DIR` | `Append` / `AppendWithOutbox` on every chat turn | `Summary` + `Recent` during hydration; also holds the quota buckets and the outbox queue |
| **Facts** | [`internal/facts`](../../internal/facts/facts.go) | Ephemeral SQLite `:memory:` (FTS5) | File SQLite (`AISOTROPY_FACTS_DB` or `AISOTROPY_DATA_DIR/facts.db`) or **rqlite** (`RQLITE_URL`, ADR-0004) | Outbox worker → `Sink` → `HeuristicExtractor` → `Save` (UPSERT) | `Search` (FTS5 top-4) during hydration; `ExportAll`/`Delete` by the sleep cycle |
| **Semantic** (multi-kind) | [`internal/semantic`](../../internal/semantic/semantic.go) | In-memory brute-force cosine index | **Qdrant** (`QDRANT_HOST` + `QDRANT_PORT`/`QDRANT_API_KEY`/`QDRANT_USE_TLS`/`QDRANT_COLLECTION`, ADR-0001) | `KindFact` via the `engram` facade (dual-indexed distilled facts), `KindTurn` via consolidation (bounded), `KindDocument` via knowledge ingestion (ADR-0017/0018/0019). Embedder is BYO (`HashEmbedder` default, `APIEmbedder` via `AISOTROPY_EMBED_URL`) | **Read on the request path**: `hydrateSystemPrompt` searches `KindDocument`+`KindFact` (user scope) merged with tenant-shared `KindDocument`; `KindTurn` is bounded (TTL/cap) and excluded from primary RAG |

The embedder is the deterministic `HashEmbedder` placeholder and the working-memory summarizer is
`LLMSummarizer` (real LLM condensation via the same chat client, or `NoopSummarizer` in tests).
Every operation on every tier is scoped by `(tenant, user[, session])` — the multi-tenant isolation
gate lives in each key/statement, never in an intermediate layer.

## 5. Phase 7 granular control as built

All five knobs are constructed in `App.Run` ([`aisotropy/app.go`](../../aisotropy/app.go)) and
threaded through the handlers:

- **Rate limiter** — `wm.NewLimiter(rate, burst)`: a per-tenant token bucket persisted in the
  working-memory BadgerDB, serialized per tenant by an in-process mutex (BadgerDB runs with
  `DetectConflicts=false`). The `rate`/`burst` inputs come from the `WithRateLimit` builder option
  when set; otherwise `App.Run` reads `AISOTROPY_RATE_PER_SEC` / `AISOTROPY_BURST` via
  `ratePerSecFromEnv` / `burstFromEnv`, each falling back to the built-in defaults (10 / 30) when
  unset, unparsable, or non-positive ([`app.go:400-404`](../../aisotropy/app.go)). Checked on `chat`
  and `chat/stream` **before** the LLM call. (The `chat/agent` route does **not** rate-limit — no
  limiter is passed to `RegisterAgent`.)
- **Circuit breaker** — `llm.WithCircuitBreaker(client, 5, 30s)`: 5 consecutive `StreamChat` start
  failures open the breaker; calls then fail fast with `ErrCircuitOpen` for a 30s cooldown, then a
  half-open trial. Wraps both the primary and the cheap client. Shared by streaming, non-streaming,
  and agent paths.
- **Scope-gated tools** — `tools.NewRegistry(search_docs [no scope], read_financials
  [read:financial-data], write_tasks [write:tasks])`. Only scope-permitted tools enter the prompt;
  the agent loop re-checks the model's chosen tool name against the permitted set post-parse, so a
  hallucinated/injected tool name can never execute
  ([`internal/agentloop/agentloop.go`](../../internal/agentloop/agentloop.go)). All three routes
  (`chat`, `chat/stream`, `chat/agent`) resolve the gating scopes through the single `effectiveScopes`
  helper ([`internal/api/authn.go`](../../internal/api/authn.go)): under a real authenticator the
  **verified principal's** scopes are authoritative (a client cannot escalate via body scopes); under
  NoAuth / no auth middleware the client-asserted body scopes are the fallback (ADR-0024).
- **Model degradation** — `llm.CheapClientFromEnv()` (built from `AISOTROPY_CHEAP_MODEL`) wrapped in
  the same breaker + OTel. Consumed only by the streaming path at pressure ≥ 0.95. `nil` (no
  degradation) when the env var is unset.
- **Cost meter** — `costmeter.New()`: an in-process, per-tenant aggregate of real provider token
  usage (input/output tokens, request count, estimated USD at $0.15/$0.60 per 1M in/out). Recorded
  on each streamed `done`, exposed at `GET /costs`. Per-process and non-durable — a live
  observability aggregate, not a billing system of record.

The two book-gap capability seams wired by **ADR-0028** are constructed in the same `App.Run` and
threaded through the handlers:

- **Guardrails** — `api.Guard{G: guardrails.Default(), BlockInjection: …, BlockOutput: …}`
  ([`internal/api/guard.go`](../../internal/api/guard.go),
  [`internal/guardrails`](../../internal/guardrails)). Input redaction + injection detection run on
  **all three** chat routes; output toxicity/leak detection runs on `chat` (hard-block-capable) and
  `chat/stream` (annotate-only). **Detection is always on; blocking is opt-in** via
  `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` / `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` (default off). Every
  detector is advisory: it classifies, the handler decides. Logs types/counts/signal names only,
  never raw values or the response body.
- **Model-selection gateway** — `buildModelGateway(streamClient, cheapStreamClient,
  costBudgetFromEnv(), visionModelFromEnv())` ([`internal/gateway`](../../internal/gateway)), consulted
  via `selectClient` on `chat` and `chat/stream` (not `chat/agent`). Registers `primary` (the
  OTel-wrapped stream client) as the default; chains `OverridePolicy` (driven by `model_preference`,
  ADR-0029) → `ImageRule` (driven by `images` → `AISOTROPY_VISION_MODEL` target, ADR-0030) →
  `CostAwarePolicy` (cost-budget rule, enabled **only** when `AISOTROPY_COST_BUDGET_USD > 0` **and** a
  cheap client exists). With no preference, no image, and no budget it is a pure pass-through to `primary`.
  Deliberately no prompt-length/difficulty rule (Bet-4 refuted, ADR-0021). On the streaming route the
  quota-pressure degrade above still applies as an override on top of the gateway pick. The image rule
  routes only — image bytes are not yet sent to the model (ADR-0030, honest scope).
- **Response cache** — [`internal/responsecache`](../../internal/responsecache) is now **LIVE on the
  new stateless `/ask` route** (ADR-0030): `buildResponseCache(embedder)` builds an exact LRU layer
  (4096 entries, 1 h TTL) always, plus a semantic (nearest-neighbor) layer (cosine threshold 0.95, 1 h
  TTL, 512 entries/scope) when an embedder exists. Both layers key/partition by `(tenant, user)`, so a
  cross-tenant hit is impossible by construction. It remains **deliberately NOT on the stateful chat
  path** (ADR-0028): a prompt-keyed cache over a per-user memory-hydrated `/chat` response is a
  staleness/isolation footgun — see §6.

## 6. Honest "not yet wired / limitations"

- **Semantic tier is multi-kind (ADR-0017/0018/0019), read AND written.** Every vector carries a
  `Kind`: `KindDocument` (curated knowledge, ingested via the SDK `IngestKnowledge` / HTTP
  `POST .../knowledge`, user- or tenant-scoped), `KindFact` (distilled facts dual-indexed through the
  `engram` facade, whose lifecycle is tied to the fact — supersede/delete removes the vector), and
  `KindTurn` (raw turns, **bounded** by a TTL/cap pruner that runs always, independent of the Rust
  sleep cycle). `hydrateSystemPrompt` retrieves `KindDocument`+`KindFact` at user scope, merges the
  tenant-shared `KindDocument` bucket, and excludes raw `KindTurn` from the primary RAG. Wired on
  **both** chat routes: the streaming path and — since the chat-route parity fix — the non-streaming
  `chat`/`App.Turn` path via `ChatDeps.SemStore/Embedder/Reranker` (historically the non-streaming
  route passed a nil semantic store and could not semantic-recall). Hydration is now **goroutine-
  parallel** across the three tiers ([ADR-0076](../decisions/0076-as-built-deltas-parallel-hydration-shutdown-join.md)),
  closing the last delta against the aspirational [request lifecycle](request-lifecycle.md) — this
  bullet previously read "still sequential". The win is deployment-shaped and the ADR records both
  halves honestly: ~240ms → ~120ms with networked-tier latency, but a few microseconds *slower* with
  the bare-binary in-process defaults, where the fan-out costs more than the reads.
  The embedder is BYO: `HashEmbedder` by default
  (wires but does not rank semantically), a real `APIEmbedder` when `AISOTROPY_EMBED_URL` is set.
- **Both chat routes consolidate; only `chat/agent` does not.** `chat/stream` and `chat` both call
  `AppendWithOutbox` (the non-streaming route since commit `37c2a4e`), so both feed the outbox →
  facts/semantic pipeline. `chat/agent` is the only route that neither hydrates nor consolidates
  memory. (Historical note: non-streaming `chat` previously used plain `wm.Append` and never
  consolidated — that gap is now closed.)
- **The tenant-tier routing seam IS adopted (correcting a former stale claim here).**
  [`internal/routing`](../../internal/routing/router.go) (ADR-0007 Shared/Dedicated tenant tiering)
  is wired in the composition root: `app.go` builds `routing.NewStaticPolicy(dedicatedTenants…)` →
  `routing.NewRouter(tierPolicy)` and passes the router into `openFactsStore`/`openSemanticStore`, so
  the facts and semantic stores route dedicated tenants to physically isolated stores via
  `RoutingStore` (`internal/facts/routing_store.go`, `internal/semantic/routing_store.go`). It is
  publicly re-exported as `TenantRoutingPolicy` ([`extend.go`](../../aisotropy/extend.go)) and is
  **100% test-covered**. (This bullet previously read "nothing outside the package imports it" — that
  was false: it is imported by `app.go`/`knowledge.go`/`extend.go` and the two routing stores, and a
  2026-07-17 audit + `scripts/prune-guard.sh` confirmed it is live, wired, public, and isolation-critical.
  See [`prune-evidence-gate.md`](prune-evidence-gate.md).)
- **Primitive self-improvement writes but is never read back (open loop).**
  [`internal/primitives`](../../internal/primitives/primitives.go) (friction-driven per-user
  rewriting) is now **wired on the write side**: `App.Run` builds a `primitives.Store` +
  `FrictionDetector` ([`app.go:542-544`](../../aisotropy/app.go)) and passes the detector to
  `RegisterAgent`, so a failed tool call on the `chat/agent` route records `SignalFailure` friction
  against that user's primitive ([`chat_agent.go:62`](../../internal/api/chat_agent.go)). A separate
  hourly `runPrimitiveConsolidation` goroutine ([`app.go:622`](../../aisotropy/app.go)) calls
  `primitives.Consolidate` with the `HeuristicRewriter`, rewriting primitives whose accrued friction
  crossed `primitiveFrictionThreshold` (3.0). The gap: **nothing reads the rewritten primitives
  back** — no hydration path calls `Store.Get`/`ListForUser`, so an improved primitive never
  re-enters a prompt. The self-improvement loop is closed on the write half only; the read-back that
  would make it behavioral memory is not adopted. (This is distinct from the DataFusion sleep cycle
  above, which does facts dedup, not primitive rewriting.)
- **Model degradation needs `AISOTROPY_CHEAP_MODEL`.** Without it `CheapClientFromEnv()` returns
  `nil` and every near-quota tenant gets the primary model (the degradation branch is skipped).
- **The agent loop has TWO dispatch paths (ADR-0012/0063).** When the LLM client implements
  `llm.ToolCaller` — **both** the OpenAI-compatible/OpenRouter client (ADR-0012) **and** the
  `AnthropicClient` via native `tool_use` (ADR-0063) — the endpoint runs a Gollem `core.Agent[string]`
  with real function tools (native provider tool-calling), inheriting its guardrails/cost-tracker/OTel.
  Only the **keyless stub** (no provider to call) falls back to the homegrown `internal/agentloop`,
  which asks the model for a JSON `Decision` over the plain `StreamChat` interface. **Both paths enforce the identical double scope-gate** (only
  scope-permitted tools offered; the chosen name re-validated before execution). The default
  executor is `DemoExecutor` (echoes the call); a consumer injects a real one via the builder's
  `WithToolExecutor` (Phase 8). The agent route still does not hydrate memory, persist turns,
  rate-limit, or meter cost — see the [threat model](threat-model.md) T4.
- **Rate limiting and the cost meter are per-instance.** Both live in-process; there is no
  cross-replica shared quota or accounting. A multi-replica deployment would need to move this state
  to a networked tier.
- **Graceful shutdown now cancels AND joins in-flight handlers before closing any store
  ([ADR-0076](../decisions/0076-as-built-deltas-parallel-hydration-shutdown-join.md)).** `App.Run`
  runs a two-phase teardown: `server.Shutdown(shutdownCtx)` for the polite drain, then — on a
  **separate** `teardownTimeout` budget, never the drain's leftovers — `cancelInFlight()` +
  `waitWorkers(teardownCtx, &inFlightWG)` before `rt.close`. Every request context descends from a
  context the process owns (`http.Server.BaseContext`), because `net/http` itself never cancels an
  in-flight request on shutdown; a `sync.WaitGroup` middleware registered outermost on the chi router
  counts requests for the join. This closes the previously documented "pathological SSE handler still
  writing to working memory past the grace period could race `BadgerDB.Close()`" window. It stays
  **bounded**: a handler ignoring cancellation degrades teardown to best-effort rather than hanging
  the process — the same posture the background workers already had.
- **Reflection/fail-closed applies only to non-streaming chat — deliberately, and permanently
  ([ADR-0076](../decisions/0076-as-built-deltas-parallel-hydration-shutdown-join.md)).** The
  `chat/stream` path forwards raw tokens with no schema enforcement; the structured-output reflection
  loop and its 502 fail-closed behavior are the `chat` route's alone. The **reason**, not just the
  fact: the two routes have different response *contracts*. `chat` returns a validated
  `chatStructuredReply`, so `ReflectAuto[T]` has a `T` to enforce and a typing error to retry with;
  `chat/stream`'s contract IS unstructured token deltas, so there is no schema to violate and nothing
  to retry against. Even granting a schema, a retry after the first byte cannot un-send a committed
  200 — and the buffer-then-emit fix for that is already implemented for the *output guardrail*
  (`AISOTROPY_GUARDRAILS_BLOCK_OUTPUT`, ADR-0029), opt-in precisely because it costs
  time-to-first-token, the property the streaming route exists for. This asymmetry is registered as a
  permanent claim (`reflection-loop-nonstreaming-only`), not carried as debt.
- **Stale wiring note in `working_memory.go`.** Its type doc claims `ChatInput` "currently carries
  only `TenantID`" and that wiring memory into chat needs a contract change first — but the contract
  already carries `tenant_id`/`user_id` path params + `session_id` body, and the streaming path is
  fully wired. The comment predates the current wiring.
- **Guardrails default to annotate, not block (ADR-0028).** On a fresh deployment, redaction +
  detection run but neither injection nor output verdicts reject anything until
  `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` / `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` are set. Do not read
  "guardrails wired" as "guardrails enforcing."
- **Streaming output-guardrail is annotate-only BY DEFAULT — buffered hard-blocking is opt-in
  (ADR-0029).** With `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` off (default), `chat/stream` flushes live and
  can only annotate a flagged reply (a token-by-token stream can't be un-sent). Turning the toggle on
  makes `chat/stream` **buffer** the whole response and hard-block (replace with a refusal before the
  first byte), at the cost of time-to-first-token — closing the ADR-0028 follow-on. Non-streaming `chat`
  hard-blocks under the same toggle without buffering (it already has the whole reply).
- **The gateway's override AND image rules now both fire; only the model still can't see the image
  (ADR-0029 / ADR-0030).** The cost-budget rule, the `OverridePolicy` (driven by
  `ChatRequest.model_preference` → `UISelector`, ADR-0029), **and** the `ImageRule` (driven by
  `ChatRequest.images` → `HasImage`, target `AISOTROPY_VISION_MODEL`, ADR-0030) all route today. There is
  deliberately no length/difficulty rule (Bet-4 refuted, ADR-0021). A `model_preference` is a routing hint
  only (unknown names fall back to the default; quota-pressure `DegradeAt` still overrides it) — not
  authorization. **The image rule is routing-only:** the text-only `llm.Client`s (Anthropic /
  OpenAI-compat / stub) do **not** transmit the image bytes to the model — the `images` field is neither
  embedded, persisted, nor forwarded, only counted for presence. A multimodal `llm.Client` is the
  remaining follow-on; until then an image turn is merely *routed* to the most capable configured target,
  not *understood*.
- **The response cache is LIVE on `/ask` only — never on the stateful chat path (ADR-0028 / ADR-0030).**
  [`internal/responsecache`](../../internal/responsecache) now backs the stateless `POST /v1/.../ask`
  route (ADR-0030): stateless by design (no working-memory hydration, no session, no consolidation
  outbox), which is exactly what makes caching safe — with no memory-hydrated personalization in the
  prompt, a cached `(tenant, user, prompt)` answer cannot go stale. Both cache layers key/partition by
  `(tenant, user)`, so a cross-tenant hit is impossible by construction (not a post-filter). It remains
  **deliberately off** `/chat` and `/chat/stream`: those responses depend on per-user hydrated
  working/facts/semantic memory that a prompt key excludes, so a hit would serve a context-wrong or stale
  answer.
- **`/ask` is stateless by design (ADR-0030).** Unlike `/chat`, `/ask` does not hydrate working memory,
  carry a `session_id`, or write a consolidation outbox event — its only context is tenant-scoped semantic
  RAG. A reader must not expect it to remember a prior turn; that statelessness is the load-bearing
  invariant behind its cache safety. It runs the same input/output guardrails and sits behind the same
  authenticated-identity must-agree bind as `/chat` (registered on the same `humaAPI` after
  `UseMiddleware`), and is driven by the same protected (circuit-breakered) client.

## Environment variables (as consumed by the code)

| Variable | Consumed in | Effect |
|---|---|---|
| `AISOTROPY_ADDR` | `App.Run` (`aisotropy/app.go`) | HTTP listen address; the `WithAddr` option wins over it, and both fall back to `:8080` |
| `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` | `llm.RegistryFromEnv` (via `llm.FromEnv`) | Select the default LLM client by precedence `OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub` ([`internal/llm/registry.go`](../../internal/llm/registry.go)). Every provider with a key set is registered; the highest-precedence one becomes the default. With none set, the credential-less `stub` client is the default (`ChatOutput.stub = true`). |
| `AISOTROPY_JWT_SECRET` | `aisotropy.AuthenticatorFromEnv` ([`aisotropy/authenv.go`](../../aisotropy/authenv.go)) | Selects the `aisotropyd` binary's **HS256** authenticator (ADR-0024) — **one of the mutually-exclusive JWT modes** (setting more than one JWT mode refuses to boot). Set (non-empty) → `NewJWTAuthenticator([]byte(secret))`, verifying an `Authorization: Bearer <jwt>` HS256 credential with the Go stdlib only (no dependency); the auth gate then enforces the must-agree bind (401 on missing/invalid, 403 on tenant/user mismatch). Unset (and no RS256 mode set) → `NoAuth()` (path tenant/user trusted verbatim) with a startup **warning**. **Must be ≥ 32 bytes** (ADR-0025): a shorter secret is a fatal config fault — the binary refuses to boot (`auth.ValidateHS256Secret`). The secret value is never logged. |
| `AISOTROPY_JWT_PUBLIC_KEY` / `AISOTROPY_JWT_PUBLIC_KEY_FILE` | `aisotropy.AuthenticatorFromEnv` ([`aisotropy/authenv.go`](../../aisotropy/authenv.go), [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)) | Selects **RS256** verification against a **static RSA public key** (ADR-0029), for tokens minted by an external IdP. `PUBLIC_KEY` is a PEM literal (PKIX `PUBLIC KEY` or PKCS#1 `RSA PUBLIC KEY`); `PUBLIC_KEY_FILE` is a path to the same — the two are **mutually exclusive** (setting both refuses to boot). Mode label `jwt-rs256`. A parse/read failure is a fatal boot fault whose error **never contains key bytes**. The algorithm-confusion defence pins the verifier to RS256, so HS256 tokens are rejected outright ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)). |
| `AISOTROPY_JWT_JWKS_URL` | `aisotropy.AuthenticatorFromEnv` ([`aisotropy/authenv.go`](../../aisotropy/authenv.go), [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)) | Selects **RS256** verification against a **JWKS endpoint** (ADR-0029). The key set is fetched (`net/http`), parsed **stdlib-only** (`math/big` over the jwk `n`/`e` — no external JWK library), TTL-cached (default 5 min), and the token `kid` is matched to a key (bounded refetch, default 1 min, on an unknown `kid`); the response is bounded to 1 MiB. Mode label `jwt-rs256-jwks`. **Fails CLOSED**: a fetch failure with no usable cached key rejects the request (never falls open). |
| `AISOTROPY_JWT_AUDIENCE` / `AISOTROPY_JWT_ISSUER` / `AISOTROPY_JWT_MAX_TTL` | `aisotropy.AuthenticatorFromEnv` ([`aisotropy/authenv.go`](../../aisotropy/authenv.go)) | **Optional** JWT hardening (ADR-0025), applied to **whichever** JWT mode (HS256 or RS256) is selected. `AUDIENCE` → require the `aud` claim (string/array) to contain it (token-confusion defence); `ISSUER` → require `iss` to equal it; `MAX_TTL` (a Go duration) → cap `(exp - iat)` token lifetime — a malformed value is a fatal boot fault. Each unset = not checked, so existing tokens keep verifying. `exp` is always required, `nbf`/`iat` always validated when present ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)). |
| `AISOTROPY_OPS_TOKEN` | `api.OpsTokenFromEnv` → `api.OpsAuthGate` ([`internal/api/ops_gate.go`](../../internal/api/ops_gate.go)), wired in `App.Run` | Gates the operator endpoints `GET /costs` and `GET /bitrate` (all-tenant aggregates), default-closed (ADR-0025). Unset → both return **503**; set → the request must carry a matching `Authorization: Bearer <token>` (constant-time compare, `crypto/subtle`; generic 401 on miss). The token is never logged (only its presence). |
| `AISOTROPY_REQUIRE_TLS` | `requireTLSFromEnv` → pre-route chi middleware ([`aisotropy/app.go`](../../aisotropy/app.go)) | Truthy (`strconv.ParseBool`) → reject any non-TLS request (`r.TLS == nil`) with **HTTP 400**, so a bearer token can never be sent in plaintext (ADR-0025). **Off** by default — most deployments terminate TLS at a proxy and forward plain HTTP; enable only when aisotropyd terminates TLS itself. |
| `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` | `guardrailsBlockFromEnv` → `api.Guard.BlockInjection` ([`aisotropy/app.go`](../../aisotropy/app.go), [`internal/api/guard.go`](../../internal/api/guard.go)) | Truthy (`strconv.ParseBool`) → a `Suspicious` **input** injection verdict **rejects** the request (400 on `chat`/`chat/agent`, SSE `error` on `chat/stream`) instead of merely annotating it (ADR-0028). Unset/off (default) → redaction + detection still run, but nothing is blocked. |
| `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` | `guardrailsBlockFromEnv` → `api.Guard.BlockOutput` ([`aisotropy/app.go`](../../aisotropy/app.go), [`internal/api/guard.go`](../../internal/api/guard.go)) | Truthy → a flagged (toxicity/leak) reply is replaced with a safe refusal (which then persists + returns) instead of merely annotating it, on **both** chat routes (ADR-0028 + ADR-0029): the **non-streaming** `chat` substitutes the refusal after the reply is produced, and — new — the **streaming** `chat/stream` **BUFFERS** the whole response (no live token flush) so it, too, can inspect and refuse before emitting a byte (a **time-to-first-token** cost, opt-in). Also sets the LLM judge's error posture to **fail-closed** when the judge is attached (below). Unset/off (default) → annotate + log only; `chat/stream` keeps its live flush (no latency change). No effect on `chat/agent` (no output guardrail). |
| `AISOTROPY_GUARDRAILS_LLM_JUDGE` | `guardrailsBlockFromEnv` → the output-inspector branch in `App.Run` ([`aisotropy/app.go`](../../aisotropy/app.go), [`internal/api/eval_output_check.go`](../../internal/api/eval_output_check.go)) | Truthy (and an LLM client exists) → attaches an **LLM-as-a-judge Safety check** to the output guardrail via the `api.EvalOutputCheck` adapter over the `guardrails.OutputCheck` seam (ADR-0029; `internal/guardrails` still never imports `internal/eval`). Built on `eval.NewLLMJudge(llmClient, eval.Safety, …)` and flags a reply whose normalized safety score is **< 0.5** (`guardrailsJudgeSafetyThreshold`). Driven by the **raw** `llmClient` — deliberately not the circuit-broken chat client — so a flaky judge can't trip the chat breaker. Its **error posture tracks `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT`**: fail-open (annotate) when blocking is off, fail-closed (suspicious) when on. Construction failure → logged, falls back to the built-in output heuristics. Unset/off (default) → built-in heuristics only; keyless/stub runtimes unchanged. |
| `AISOTROPY_COST_BUDGET_USD` | `costBudgetFromEnv` → `buildModelGateway` ([`aisotropy/app.go`](../../aisotropy/app.go), [`internal/gateway`](../../internal/gateway)) | A positive USD budget enables the gateway's `CostAwarePolicy` cost-budget shaping — an over-budget tenant (by cumulative spend from the cost meter) is routed to the cheap target (ADR-0028). **Requires a cheap client** (`AISOTROPY_CHEAP_MODEL`) too; with either unset, the rule is skipped and the gateway is a pass-through to `primary`. An explicit `ChatRequest.model_preference` (`gateway.OverridePolicy`, ADR-0029) **wins over** this cost-budget rule. Unset/non-positive → shaping disabled. |
| `AISOTROPY_VISION_MODEL` | `visionModelFromEnv` → `buildModelGateway` ([`aisotropy/app.go`](../../aisotropy/app.go), [`internal/gateway`](../../internal/gateway)) | The gateway target **name** an image-bearing turn (`ChatRequest.images` present → `gateway.Request.HasImage`) is routed to via the `ImageRule`, chained **before** the cost-budget rule so an image request is not shaped down to the cheap text model (ADR-0030). Must name a registered client (in this runtime `primary` or `cheap`); an unregistered name resolves safely to the default at `Route` time. Unset/empty → images route to `primary` (routing unchanged). **HONEST SCOPE:** routing signal only — the text-only clients do **not** transmit image bytes to the model yet; the multimodal client is a follow-on. |
| `AISOTROPY_DATA_DIR` | working memory + facts | On-disk BadgerDB dir; also default `facts.db` location |
| `AISOTROPY_FACTS_DB` | `facts.OpenFromEnv` | Explicit SQLite file path |
| `RQLITE_URL` | `facts.OpenFromEnv` | Networked rqlite facts store |
| `QDRANT_HOST` (+ `QDRANT_PORT`/`QDRANT_API_KEY`/`QDRANT_USE_TLS`/`QDRANT_COLLECTION`) | `semantic.OpenFromEnv` | Qdrant semantic store |
| `AISOTROPY_CHEAP_MODEL` | `llm.CheapClientFromEnv` | Enables quota-pressure degradation |
| `AISOTROPY_SLEEPCYCLE_BIN` | `App.Run` (`aisotropy/app.go`) | Enables the DataFusion sleep-cycle worker |
| `AISOTROPY_BITRATE_BIN` | `App.Run` (`aisotropy/app.go`) | Enables the Bet 6 bitrate path: the durable bpt store, the held-out request-path sampler, the offline scorer, and the `GET /bitrate` route. Unset → the whole path is off and turn ingestion is byte-for-byte unchanged. |
| `AISOTROPY_RATE_PER_SEC` / `AISOTROPY_BURST` | `ratePerSecFromEnv` / `burstFromEnv` (`aisotropy/app.go`) | Per-tenant token-bucket rate (requests/sec) + burst capacity, feeding `wm.NewLimiter`. Read **only when `WithRateLimit` was not used** — an explicit builder option wins. Defaults 10 / 30 when unset, unparsable, or non-positive. |
| `AISOTROPY_REFLECTION_BACKEND` | `llm.ReflectionBackendFromEnv` | `own` forces the homegrown loop; anything else → Gollem |
| `OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `llm.NewTracerProvider` | Enables OTLP/HTTP trace export; unset → no-op tracer |

## References

- Wiring (composition root): [`aisotropy/app.go`](../../aisotropy/app.go) (`App.Run`);
  [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) is the ~97-line thin consumer of it
- Handlers: [`internal/api/chat.go`](../../internal/api/chat.go),
  [`chat_stream.go`](../../internal/api/chat_stream.go),
  [`chat_agent.go`](../../internal/api/chat_agent.go)
- Design counterparts: [overview](overview.md) · [request lifecycle](request-lifecycle.md) ·
  [tiered memory](memory-tiers.md) · [consistency](consistency.md) ·
  [multi-tenancy](multi-tenancy.md) · [granular control](granular-control.md) ·
  [observability](observability.md)
