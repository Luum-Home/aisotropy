---
type: ADR
title: "ADR-0030 — JWKS rotation proven end-to-end, an image request-shape routing signal, and a stateless response-cached /ask endpoint"
description: ADR-0029 shipped RS256/JWKS verification, a model-preference signal, streaming hard output-blocking, and the eval LLM-judge — but left three seams either unproven or inert. This ADR records the three that are now closed, each stdlib-only, additive, and default-safe. (1) JWKS key ROTATION is now proven end-to-end — a rotated-in signing key is picked up (unknown-kid bounded refetch) and a rotated whole set is refreshed at the TTL boundary, while a burst of unknown-kid tokens cannot hammer the IdP (the refetch-throttle bounds network fetches, even on failure). (2) ChatRequest.images — an additive, omitempty presence signal — activates the gateway's previously-inert capability ImageRule (target from the NEW AISOTROPY_VISION_MODEL env var), so an image-bearing turn routes to the vision/premium target; HONEST SCOPE: this wires the ROUTING SIGNAL only — the current text-only llm.Clients do not transmit image bytes to the model, so the multimodal model CALL is a named follow-on. (3) The NEW stateless POST /v1/.../ask endpoint makes internal/responsecache a LIVE request-path lever safely: it is stateless by design (no working-memory hydration, no session, no consolidation outbox), which is exactly what makes caching safe (no memory-hydrated personalization → no staleness footgun), it is per-(tenant,user) isolated by construction in both cache layers, and it sits behind the same authenticated-identity must-agree bind as /chat. All three opt-in / default-safe; no new dependency, no OpenAPI contract break (the sole struct addition is an additive omitempty field, ADR-0002 preserved).
tags: [decisions, adr, auth, jwks, rotation, gateway, image-routing, multimodal, responsecache, ask, stateless, security, follow-ons]
timestamp: 2026-07-15T00:00:00Z
---

# ADR-0030 — JWKS rotation proven end-to-end, an image request-shape routing signal, and a stateless response-cached `/ask` endpoint

## Status

✅ **Accepted — 2026-07-15.** This ADR records three seams that [ADR-0029](0029-followons-rs256-streaming-signals-judge.md)
shipped or named but left either **unproven** or **inert**, now closed:

1. **JWKS key rotation, proven end-to-end.** ADR-0029 built the JWKS verifier (fetch, TTL-cache,
   `kid`-match, bounded refetch, fail-closed) but its acceptance evidence stopped at *matching a
   present key*. The operationally load-bearing property of a JWKS deployment — that when the IdP
   **rotates** its signing key, aisotropyd picks up the rotated-in key **without a restart** and
   **without hammering** the endpoint — is now demonstrated by tests over the shipped code.
2. **An image request-shape signal (`ChatRequest.images`) that activates the gateway's `ImageRule`.**
   ADR-0029 wired the gateway's `OverridePolicy` (via `model_preference`) and cost-budget rule, but
   left the capability-based **`ImageRule` present-but-inert** for lack of any image signal on
   `ChatInput`. This ADR adds the signal and the operator target env var (`AISOTROPY_VISION_MODEL`).
3. **A stateless, response-cached `POST /v1/.../ask` endpoint.** Every ADR since ADR-0028 has recorded
   `internal/responsecache` as **CODE-ONLY by decision** — a tested primitive kept *off* the chat path
   because a prompt-keyed cache over a per-user memory-hydrated response is a staleness/isolation
   footgun. This ADR builds the **stateless surface** those ADRs pointed to, which is exactly where the
   cache is safe.

Each is **stdlib-only, additive, and default-safe**: no new dependency, no OpenAPI struct-contract
break (ADR-0002 preserved — the one struct change is an additive `omitempty` field), and every new
behavior is either opt-in (env-gated) or purely additive (a new route, a new field). A runtime that
sets no new env var and sends no new field behaves byte-for-byte as it did before this cut. This ADR
does **not** supersede any prior classification; it *proves* the JWKS rotation ADR-0029 built,
*activates* ADR-0028's inert `ImageRule`, and *promotes* `internal/responsecache` from CODE-ONLY to
LIVE on a new stateless route (never on the stateful chat path).

## Context

Three seams remained either unproven or deliberately un-activated after ADR-0029, each for a concrete
reason:

1. **JWKS rotation was built but not demonstrated.** The whole point of pointing aisotropyd at an IdP's
   JWKS URL (rather than pinning one static RSA key) is to survive the IdP's routine **key rotation**
   with no operator action. But rotation has two failure modes a naive cache invites: (a) a rotated-in
   `kid` is never picked up because the cache is only refreshed on TTL expiry (so a fresh token 401s
   until the TTL elapses), or (b) the cache refetches on *every* unknown `kid`, so a flood of
   unknown-`kid` tokens (an easy DoS, or just a misconfigured client) **hammers the IdP**. The shipped
   `jwksSource.keyForKid` ([`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)) resolves
   both — an unknown `kid` triggers a *bounded* refetch (throttled by `minRefetch`), and a past-TTL
   cache refreshes on the next lookup — but ADR-0029's acceptance evidence did not yet include the
   rotation and throttle proofs. Without them the rotation guarantee is a claim, not a tested property.
2. **The gateway's `ImageRule` had no input.** ADR-0028 registered a capability-based `ImageRule`
   (route an image-bearing turn to a premium/vision target) but `ChatInput` carried no field to say "this
   turn has an image," so the rule could never fire — `gateway.Request.HasImage` was always false. ADR-0029
   added the `model_preference` signal (activating `OverridePolicy`) but explicitly left the **image**
   request-shape rule inert, noting it needed a further additive contract field. An operator who wants
   image-bearing turns steered to a more capable model had no lever.
3. **`internal/responsecache` was a primitive with nowhere safe to live.** ADR-0028 and ADR-0029 both
   recorded the response cache as tested-and-isolation-correct but **not** wired, precisely because the
   only request path that existed — `/chat` and `/chat/stream` — is **stateful**: it hydrates per-user
   working memory, appends turns, and feeds the consolidation outbox, so an answer is personalized to a
   session's evolving context. Caching such an answer by `(tenant, user, prompt)` would serve a
   context-wrong or stale reply on a later turn. The cache needed a **stateless** surface — one whose
   only context source is tenant-scoped RAG, with no memory-hydrated personalization folded in — to be
   safe. That surface did not exist.

The open question this ADR answers, for each seam, is **what proof / activation / new surface closes
it** — holding the invariants the prior ADRs set (stdlib-only dependency policy, fail-closed auth,
by-construction tenant isolation, anti-magic struct-as-contract, and honest opt-in/default-safe
behavior).

## Goals

- **Prove** JWKS rotation end-to-end over the *shipped* code (rotated-in key picked up; TTL-boundary
  refresh; unknown-`kid` burst bounded to one fetch per window), so the rotation guarantee is a tested
  property, not a claim.
- **Activate** the gateway's `ImageRule` with an additive request-shape signal and an operator-visible
  target env var — **without** a breaking contract change and **without** overclaiming (the model does
  not yet see the image bytes).
- **Promote** `internal/responsecache` to LIVE on a **new stateless route** where caching is safe by
  design, with **per-`(tenant, user)` isolation by construction** and the **same authenticated-identity
  must-agree bind** as `/chat`.
- Do all three with **no new dependency** (Go stdlib + already-stable internals) and **no OpenAPI
  contract break** (ADR-0002): the sole struct change is a new **`omitempty`** field, and the sole route
  change is a *new* endpoint (nothing existing is altered).
- Be **honest about scope**: image processing is a **routing signal only** (no image bytes reach the
  model yet); `/ask` is **stateless by design** (its cache safety *depends* on that statelessness).

## Non-goals

- **NOT** transmitting image bytes to the model. The current `llm.Client` implementations
  (Anthropic / OpenAI-compat / stub) are **text-only**; the image parts are neither embedded, persisted,
  nor forwarded. A multimodal `llm.Client` is the named follow-on. `ChatRequest.images` today changes
  **routing only**.
- **NOT** making `AISOTROPY_VISION_MODEL` the default target. Unset means image-bearing turns route to
  the primary (the `ImageRule` stays wired but its target *is* the default), so routing is unchanged from
  before the signal existed.
- **NOT** caching `/chat` or `/chat/stream`. The response cache stays **off** the stateful path — a
  memory-hydrated response must not be prompt-keyed. `/ask` is a *sibling* of `/chat`, not a variant.
- **NOT** giving `/ask` any session state, working-memory hydration, or consolidation. Its statelessness
  is the load-bearing invariant that makes its cache safe; adding any of those would reintroduce the
  staleness footgun.
- **NOT** loosening tenant isolation for a cache hit. A cross-tenant cache hit is prevented **by
  construction** (both cache layers key/partition by `(tenant, user)`), never by a post-filter.
- **NOT** changing the JWKS code. The rotation/throttle behavior shipped with ADR-0029; this ADR adds
  the **proof**, not new key-handling logic.

## Options considered

### JWKS rotation — how a rotated-in key is picked up

- **Refresh the cache only on TTL expiry.** Rejected — a key rotated in *before* the TTL elapses would
  401 every token signed with it until the cache happens to expire, an unacceptable rotation lag.
- **Refetch on every unknown `kid`.** Rejected — a flood of unknown-`kid` tokens (hostile or
  misconfigured) would storm the IdP's JWKS endpoint, turning a client error into an outbound DoS.
- **Unknown-`kid`-triggered refetch, *bounded* by a min-refetch window, plus TTL refresh (CHOSEN,
  already shipped).** `jwksSource.keyForKid` refreshes a past-TTL cache on the next lookup **and**
  refetches once on an unknown `kid` — but only if the last fetch was more than `minRefetch` ago
  ([`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)). `refreshLocked` stamps `lastFetch`
  **even on a failed fetch**, so the throttle also bounds repeated *failures*. A fetch failure with no
  usable cached key **fails closed**. This is the design ADR-0029 chose; the decision here is to make it
  a **tested** property.

### The image signal's placement and honesty

- **A header/query param, or overloading `model_preference`.** Rejected — a capability signal ("this turn
  carries an image") belongs in the typed contract (ADR-0002: the Go struct is the single source of truth
  for validation and the OpenAPI schema), and it is a *distinct* concept from an explicit model
  preference (`ImageRule` vs `OverridePolicy`), so conflating them would muddy both.
- **Claim multimodal support once the signal routes.** Rejected as dishonest — routing an image turn to a
  "vision" target does not make the text-only client send the image. Overclaiming here would be exactly
  the kind of silent gap the runtime-as-built ledger exists to prevent.
- **An additive `omitempty []string` presence signal, honestly scoped to routing (CHOSEN).**
  `ChatRequest.Images` ([`internal/api/chat.go`](../../internal/api/chat.go)) is additive + `omitempty`,
  so the OpenAPI schema gains an optional field and existing clients are byte-for-byte unaffected. Its
  **only** effect is `len(Images) > 0 → gateway.Request.HasImage → ImageRule`. The field's doc string and
  the ADR both state the honest scope: routing signal only, bytes not transmitted.

### Where the response cache can live safely

- **Wire the cache onto `/chat` / `/chat/stream`.** Rejected (ADR-0028's original decision, re-affirmed) —
  those responses depend on per-user hydrated working/facts/semantic memory that a prompt key excludes; a
  hit would serve a context-wrong or stale answer. Isolation is also at risk if the key ever omitted the
  user.
- **A new stateless sibling route whose only context is tenant-scoped RAG (CHOSEN).** `POST /v1/.../ask`
  ([`internal/api/ask.go`](../../internal/api/ask.go)) does no working-memory hydration, carries no
  session id, and writes no outbox event. With **no memory-hydrated personalization folded into the
  prompt**, a cached answer for `(tenant, user, prompt)` cannot go stale against a user's changing working
  memory — statelessness is *precisely* the property that makes caching safe. The cache keys/partitions by
  `(tenant, user)` in **both** layers, so a cross-tenant hit is structurally impossible.

## Decision

### 1. JWKS key rotation, proven end-to-end over the shipped verifier

The rotation and throttle behavior of `jwksSource`
([`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go)) is now covered by tests that exercise
the real fetch/cache/`kid`-match path against an in-process JWKS server with an injected clock
([`internal/auth/jwt_rs256_test.go`](../../internal/auth/jwt_rs256_test.go)):

- **A rotated-in signing key is picked up without a restart.** `TestJWTBearer_JWKS_RotationSuccess`: the
  IdP first serves only `key-A`; a token signed with `key-B` is **rejected** while `key-B` is unknown and
  the unknown-`kid` refetch is still throttled (the bootstrap fetch was this same tick). The IdP then
  **rotates `key-B` in** (overlap: `key-A` stays valid); once the `minRefetch` window elapses, the next
  `key-B` token triggers exactly **one** refetch and **verifies** — proving the rotated-in key is picked
  up, with the endpoint hit only twice (bootstrap + one refetch).
- **A rotated whole set is refreshed at the TTL boundary.** `TestJWTBearer_JWKS_TTLExpiryRefetch`: a
  `key-A` token verifies and populates the cache; the IdP rotates the *entire* set (`key-A` retired,
  `key-C` served); a `key-C` token is **rejected within the TTL** (cache still holds only `key-A`, and the
  bootstrap fetch was this tick so no refetch fires) and **accepted once the TTL boundary is crossed**,
  with exactly one TTL-driven refetch — proving TTL expiry refreshes the rotated set (and that a
  within-TTL lookup does **not** inflate the fetch count).
- **An unknown-`kid` burst cannot hammer the IdP.** `TestJWTBearer_JWKS_RefetchThrottle`: a burst of 25
  unknown-`kid` tokens at one instant produces exactly **one** endpoint fetch (the bootstrap), and only
  **one more** refetch is permitted after the `minRefetch` window elapses — proving the refetch-throttle
  bounds network fetches regardless of token volume. Because `refreshLocked` stamps `lastFetch` even on a
  failed fetch, the same window throttles repeated *failures*, so a down IdP is not stormed either.

Defaults are conservative and operator-tunable via the `JWKSOption`s ADR-0029 shipped: TTL 5 min
(`WithJWKSCacheTTL`), min-refetch window 1 min (`WithJWKSMinRefetchInterval`), response bounded to 1 MiB,
and **fail-closed** throughout (a fetch failure with no usable cached key, or a `kid` no fetched key
matches, returns `ErrUnauthenticated`). No key material is ever logged or returned in an error.

### 2. `ChatRequest.images` — an additive request-shape signal that activates the gateway `ImageRule`

`ChatRequest` gains `Images []string` (`json:"images,omitempty"`,
[`internal/api/chat.go`](../../internal/api/chat.go)) — an **optional presence signal** that this turn
carries image content:

- **It activates the capability `ImageRule`.** On the `chat` and `chat/stream` routes, `selectClient(...,
  len(in.Body.Images) > 0)` sets `gateway.Request.HasImage`
  ([`internal/api/guard.go`](../../internal/api/guard.go),
  [`internal/api/chat.go`](../../internal/api/chat.go),
  [`internal/api/chat_stream.go`](../../internal/api/chat_stream.go)). `buildModelGateway` chains
  `gateway.ImageRule(imageTarget)` **after** the explicit `OverridePolicy` but **before** the cost-budget
  rule ([`aisotropy/app.go`](../../aisotropy/app.go),
  [`internal/gateway/policies.go`](../../internal/gateway/policies.go)), so an image-bearing turn is
  routed to the vision/premium target **regardless of spend** — a multimodal request cannot be correctly
  served by shaping down to the cheap text model. It is inert-safe: an empty/absent `Images` makes
  `HasImage` false, the rule abstains, and the chain falls through to cost/default exactly as before.
- **The target is `AISOTROPY_VISION_MODEL` (new), else the primary.** `visionModelFromEnv` reads the env
  var ([`aisotropy/app.go`](../../aisotropy/app.go)); it names a client **already registered** in the
  gateway (in this runtime `primary` or `cheap`). Empty/unset → `imageTarget = gwPrimary` (routing
  unchanged). An unregistered name resolves **safely to the default** at `Route` time, so an operator
  typo can never drop a request.
- **HONEST SCOPE — routing signal only.** The current `llm.Client` implementations are **text-only**, so
  the image **bytes are NOT transmitted to the model**. The field's contents are neither inspected beyond
  their presence, nor embedded, persisted, or forwarded. A multimodal `llm.Client` is the named follow-on;
  until it exists, this ensures an image turn at least lands on the most capable configured target. The
  field's `doc` string states this in the published OpenAPI schema so a client is not misled.
- **The contract is preserved (ADR-0002).** Additive + `omitempty`: the generated OpenAPI 3.1 schema
  gains an optional property, and existing clients that never send it are byte-for-byte unaffected — not a
  breaking change.

### 3. `POST /v1/tenant/{tenant_id}/user/{user_id}/ask` — a stateless, response-cached endpoint (makes `internal/responsecache` LIVE)

A **new** route makes the response cache a live request-path lever, on a surface where caching is safe
([`internal/api/ask.go`](../../internal/api/ask.go), wired in
[`aisotropy/app.go`](../../aisotropy/app.go)):

- **Contract (the struct IS the contract, ADR-0002).** `AskRequest{ prompt (maxLength 2000), product_id }`
  — no `session_id` (the route is stateless), no `scopes` (it injects no tools). `AskOutput{ reply,
  tenant_id (isolation echo), cached (true when served from the cache with no LLM call), stub }`.
  `tenant_id`/`user_id` are **path params** (identity belongs in the path, never the body).
- **Stateless by design — which is what makes the cache safe.** `/ask` does **no** working-memory
  hydration, carries **no** session id, and writes **no** consolidation outbox event. Its only context
  source is **tenant-scoped semantic RAG** (`hydrateSystemPrompt` is called with a **nil** working memory
  and **nil** facts store, so it folds in only the persona base prompt plus semantic fragments). With no
  memory-hydrated personalization in the prompt, a cached answer for `(tenant, user, prompt)` cannot go
  stale against a user's changing working memory. Caching a `/chat` answer would be unsafe for exactly
  this reason; caching an `/ask` answer is not.
- **Per-`(tenant, user)` isolation by construction.** The cache is scoped to `(tenant, user)` in **both**
  layers (`responsecache.Scope`): a `Get` under one scope can never return an entry written under another,
  so a cross-tenant cache hit — a catastrophic data leak — is **impossible by construction, not by a
  post-filter**. `TestAsk_CrossScopeIdenticalPromptIsCacheMiss` proves an identical prompt under a
  different `(tenant, user)` is a **miss**.
- **The request flow, as coded.** `askIdentity` validates `(tenant, user)` via `facts.NewScope`
  (routing through `internal/tenancy`, ADR-0011; a violation → **422**) and injects identity into `ctx`
  (the api-layer carriers plus `internal/llm`'s OTel carriers) → **input guardrail** (`guard.checkInput`,
  redact + detect always; the **redacted** `forwarded` text — never the raw prompt — is what becomes the
  cache key, feeds RAG, and reaches the model; a `Suspicious` injection verdict → **400** when
  `BlockInjection` is on) → **cache `Get`** on the redacted prompt (a **HIT** returns the cached answer
  with **no LLM call**; a lookup error is treated as a miss, never a request failure) → on **MISS**:
  stateless `hydrateSystemPrompt` (persona + semantic RAG) → `collectCompletion` (one plain `StreamChat`
  accumulated to a full reply; honors `ctx` cancellation, cutting token spend; surfaces a mid-stream error
  rather than treating a truncated stream as success) → **output guardrail** (`guard.checkOutput`; when
  `BlockOutput` is on, a flagged reply is replaced with a safe refusal, so it is the **safe** text that
  both caches and returns) → **cache `Set`** under the `(tenant, user)` scope (a store failure is logged,
  not fatal) → return. `TestAsk_SecondIdenticalRequestServedFromCacheNoSecondLLMCall` proves the second
  identical request is served from cache with **no second LLM call**; `TestAsk_InputRedactionApplies`
  proves a secret in the prompt is redacted before it reaches the model or the cache key.
- **Behind the same fail-closed authn gate as `/chat`.** `RegisterAsk` mounts the route on the **same**
  `humaAPI`, **after** `UseMiddleware` installs the authenticated-identity gate
  ([`aisotropy/app.go`](../../aisotropy/app.go)), so `/ask` inherits the **must-agree bind**: under a real
  authenticator the verified principal's `(tenant, user)` MUST equal the path `(tenant, user)` (401
  missing/invalid, 403 mismatch) before the handler runs; with no authenticator, default-deny (401).
- **Nil-safe throughout, tuned conservatively.** A **nil** cache means "always compute" (no caching); a
  **nil** semStore/embedder means "no RAG context" (persona-only). `buildResponseCache` builds an exact
  (hash) LRU layer **always** (4096 entries, 1 h TTL) plus a semantic (nearest-neighbor) layer **when an
  embedder exists** (cosine threshold 0.95 so only near-identical paraphrases hit, 1 h TTL, 512
  entries/scope); a semantic-layer construction failure degrades to exact-only (logged), never failing the
  runtime. `/ask` is driven by the **protected (circuit-breakered)** client, the same one `/chat` uses.

## Consequences

- **JWKS deployments survive IdP key rotation, provably and safely.** An operator can front aisotropyd
  with an OIDC IdP's JWKS URL and rely — now with tested evidence — on rotated-in keys being picked up
  without a restart, TTL-driven refresh of a rotated set, and a refetch-throttle that stops an
  unknown-`kid` burst (or a down IdP) from being stormed. Fail-closed throughout.
- **Image-bearing turns can be routed to a capable target — but the model does not see the image yet.**
  Setting `AISOTROPY_VISION_MODEL` and sending `images` steers a turn to the vision/premium target over
  cost shaping. This is **routing only**: no image bytes reach the text-only clients. A reader must not
  infer multimodal understanding; the multimodal `llm.Client` is the remaining follow-on.
- **`internal/responsecache` is now LIVE — on a stateless route, never on the stateful chat path.** `/ask`
  gives a real latency/cost win (a cache hit skips the LLM call) with cross-tenant isolation by
  construction and the same authn bind as `/chat`. The chat path stays uncached by decision, because its
  memory-hydrated response cannot be safely prompt-keyed.
- **No new dependency, no new license surface, no contract break.** All three are Go stdlib +
  already-stable internals; the sole struct change is an additive `omitempty` field, and the sole route
  change is a new endpoint (ADR-0002 preserved — nothing existing is altered).
- **Honest defaults and honest scope.** `AISOTROPY_VISION_MODEL` unset routes images to the primary
  (unchanged); a runtime that sends no `images` and hits no `/ask` is byte-for-byte unchanged. `/ask` is
  stateless by design — a reader must not expect it to remember a session, and must not expect an image
  turn to be *understood* rather than merely *routed*.

## References

- **Prior ADRs whose seams this closes/activates/promotes:**
  [ADR-0029](0029-followons-rs256-streaming-signals-judge.md) (built the RS256/JWKS verifier + the
  gateway override/cost rules — this ADR *proves* its JWKS rotation and *activates* the still-inert image
  rule) · [ADR-0028](0028-capabilities-integration.md) (registered the `ImageRule` and recorded
  `internal/responsecache` as CODE-ONLY — this ADR *activates* the former and *promotes* the latter on a
  stateless route) · [ADR-0024](0024-authenticated-identity-seam.md) /
  [ADR-0025](0025-security-hardening-authn-ops.md) (the authn seam + hardening `/ask` inherits)
- **Invariants held:** [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract:
  the additive `omitempty` `images` field and the `AskRequest`/`AskOutput` structs are the OpenAPI
  contract; no silent behavior) · [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) /
  [ADR-0011](0011-tenant-scoped-statement-builder.md) (tenant isolation by construction, which the
  `(tenant, user)`-scoped cache preserves) · [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md)
  (Bet-4 refuted — why the gateway still has no length/difficulty rule)
- **JWKS rotation:** [`internal/auth/jwt_rs256.go`](../../internal/auth/jwt_rs256.go) (`jwksSource`,
  `keyForKid`, `refreshLocked`, the bounded unknown-`kid` refetch + TTL refresh + fail-closed);
  [`internal/auth/jwt_rs256_test.go`](../../internal/auth/jwt_rs256_test.go)
  (`TestJWTBearer_JWKS_RotationSuccess`, `TestJWTBearer_JWKS_TTLExpiryRefetch`,
  `TestJWTBearer_JWKS_RefetchThrottle`)
- **Image request-shape signal:** [`internal/api/chat.go`](../../internal/api/chat.go)
  (`ChatRequest.Images`), [`internal/api/guard.go`](../../internal/api/guard.go) (`selectClient` →
  `gateway.Request.HasImage`), [`aisotropy/app.go`](../../aisotropy/app.go) (`buildModelGateway`,
  `visionModelFromEnv`, `AISOTROPY_VISION_MODEL`),
  [`internal/gateway/policies.go`](../../internal/gateway/policies.go) (`ImageRule`),
  [`internal/gateway/route.go`](../../internal/gateway/route.go) (`Request.HasImage`)
- **Stateless `/ask`:** [`internal/api/ask.go`](../../internal/api/ask.go) (`RegisterAsk`, `askIdentity`,
  `collectCompletion`, `AskRequest`/`AskOutput`), [`aisotropy/app.go`](../../aisotropy/app.go)
  (`buildResponseCache`, the `RegisterAsk` call after `UseMiddleware`),
  [`internal/responsecache`](../../internal/responsecache) (`ResponseCache`, `Scope`, exact + semantic
  layers), [`internal/api/ask_test.go`](../../internal/api/ask_test.go)
  (`TestAsk_SecondIdenticalRequestServedFromCacheNoSecondLLMCall`,
  `TestAsk_CrossScopeIdenticalPromptIsCacheMiss`, `TestAsk_InputRedactionApplies`)
- [runtime as built](../architecture/runtime-as-built.md) (the request-lifecycle ledger updated
  alongside this ADR — the `/ask` route, the `AISOTROPY_VISION_MODEL` env var, the now-LIVE response
  cache, and the now-active image rule) · [operations runbook](../deployment/runbook.md) (the operator
  route + env-var tables updated alongside this ADR)
