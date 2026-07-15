---
type: ADR
title: "ADR-0024 — Authenticated identity seam: bind the trusted path params to a verified principal"
description: Tenant isolation was by-construction at STORAGE, but identity was a trusted, unauthenticated path param — any caller could address /v1/tenant/{any}/user/{any}/chat, so the moat's isolation began one layer above an unbuilt authn seam. Decision — a pluggable Authenticator interface invoked by one Huma middleware that MUST-AGREE binds the verified principal to the path tenant/user (403 on mismatch, 401 on missing/invalid), with a default-deny posture so insecure is unreachable by accident (option C); reference impls NoAuth + stdlib-HS256 JWTBearer, RS256/JWKS as follow-on. Closes the front-door gap the thesis admits, making isolation by-construction end-to-end when a real Authenticator is configured; OpenAPI struct contract and the NewScope sites unchanged.
tags: [decisions, adr, authentication, identity, multi-tenancy, isolation, security, huma, middleware]
timestamp: 2026-07-14T00:00:00Z
---

# ADR-0024 — Authenticated identity seam: bind the trusted path params to a verified principal

## Status

✅ **Accepted — 2026-07-14.** This ADR **supersedes and closes** the "authentication gap"
admitted in [`docs/vision/thesis.md`](../vision/thesis.md#the-authentication-gap-be-honest-about-it):
the thesis states plainly that `tenant_id`/`user_id` are *"trusted, validated path params — not
authenticated principals"* and that *"no authentication is wired into the current binary."* This
decision defines the seam that turns that admission into a closed hole. The regression anchor
`TestPathParamTrustUnauthenticatedRequestIsAcceptedAuthnRegressionAnchor`
(`aisotropy/isolation_e2e_test.go`) — which today asserts *"authless request is accepted"* and
carries an explicit note to *invert its expectation when the Authenticator lands* — is the
executable contract this ADR is written against.

## Context

Aisotropy's one surviving, verified differentiator is **multi-tenant isolation by construction**:
across ~30 surveyed frameworks it is the only self-hostable, permissive-OSS runtime whose storage
tiers isolate `(tenant_id, user_id)` unbypassably (the ~146 `NewScope`/`NewTenantScope` call sites,
the `scopecheck` analyzer, `internal/tenancy`, ADR-0011). But that isolation is a claim about the
**storage layer** only. It correctly and unbypassably isolates *whatever* `(tenant_id, user_id)` it
is handed — and today it is handed **unauthenticated, caller-asserted path params**.

The exact posture as built:

- `tenant_id`/`user_id` arrive as Huma path params constrained to `^[a-zA-Z0-9_-]{1,64}$`,
  re-validated for reserved characters, then propagated natively via `context.Context`
  (`internal/api/chat.go`, ADR-0007/0011).
- The **only** HTTP middleware in the request path is chi's `RequestID` + `Recoverer`
  (`aisotropy/app.go` `router.Use(...)`). There is **no** JWT/Bearer/security-scheme middleware
  anywhere.
- Consequence, stated plainly: **any client can call `/v1/tenant/{any}/user/{any}/chat` today** with
  no credential and be served (HTTP 200). The handler package doc admits the path param is *"the
  walking-skeleton stand-in"* for a JWT auth middleware.

So the moat's isolation **begins one layer above an unbuilt authentication seam**. Storage isolation
without identity authentication is a correctly-locked vault whose key is written on the outside of
the door. Binding `tenant_id` to an authenticated principal has been treated as *"the deploying
operator's responsibility"* — a defensible but incomplete story, because nothing in the binary makes
the insecure path *hard to reach*: it is, in fact, the **only** path, and the default.

This ADR closes the gap: it makes the authenticated identity chain a **first-class, pluggable seam**
of the runtime, not an assumed reverse-proxy the operator must remember to deploy.

## Goals

- Bind the request's **verified principal** to the path `(tenant_id, user_id)` so the storage tiers
  isolate an *authenticated* identity, not a self-asserted one — making isolation **by-construction
  end-to-end** when a real Authenticator is configured.
- Keep the seam **pluggable and provider-agnostic** (mirroring ADR-0009's stance for the LLM layer):
  the runtime owns the *binding contract*, not the credential format.
- Make the **insecure posture impossible to reach by accident** — an operator who wires nothing must
  get default-deny, and must **explicitly** opt into "no auth."
- **Zero change to the OpenAPI struct contract** (ADR-0002 struct-as-contract): no new/changed JSON
  tags or fields on `ChatInput`/`ChatRequest`. The `~8` `NewScope`-feeding handler sites stay
  unchanged in shape — they simply now receive a *verified* identity.

## Non-goals

- **NOT** a specific identity provider, session store, or user database. The runtime authenticates a
  presented credential against a pluggable `Authenticator`; it does not issue credentials, manage
  users, or own an IdP.
- **NOT** authorization policy beyond the identity↔path bind and the existing scope-gated tool
  dispatch. Fine-grained RBAC/ABAC is out of scope (see the open follow-on below on scopes).
- **NOT** RS256/JWKS in the first cut — asymmetric verification against a rotating JWKS endpoint is a
  named follow-on, not a blocker for the seam.
- **NOT** replacing the storage-scoping isolation. This *complements* ADR-0011: it adds the missing
  front-door authentication above an already-unbypassable storage floor.

## Options considered

The load-bearing question is not "which credential format" — it is **what the default is when the
operator wires nothing**. Three postures:

### Option A — Keep authentication out of the binary (operator's reverse proxy)

Document that a JWT/auth middleware in front of the handlers is the deploying operator's job; ship no
seam.

- **+** Zero code; maximal deployment flexibility.
- **−** This is the **status quo that this ADR exists to close.** Nothing makes the insecure path
  hard to reach; the *only* path is the unauthenticated one, and the differentiator's central claim
  ("isolation by construction") is quietly false at the front door. Rejected.

### Option B — Seam with a default of "open" (opt-in security)

Ship a pluggable `Authenticator` seam, but default to allow-all when unconfigured (`WithAuthenticator`
is optional; omitting it means no auth).

- **+** Backward-compatible; no behavioral break for current callers.
- **−** **Insecure by default.** An operator who forgets to wire an `Authenticator` ships the exact
  hole this ADR closes, silently. "Secure only if you remember" is the failure mode that produced the
  gap in the first place. Rejected.

### Option C — Seam with a default of "deny" (CHOSEN)

Ship the pluggable `Authenticator` seam **and** make an unconfigured runtime **default-deny**: it will
not serve the chat path unless *either* a real `Authenticator` is wired *or* the operator explicitly
selects the no-auth reference implementation via `aisotropy.WithAuthenticator(aisotropy.NoAuth())`.

- **+** **Insecure is impossible to reach accidentally.** The only way to get the old open behavior is
  to type `NoAuth()` — an operator-visible, greppable, review-flaggable opt-out (consistent with the
  anti-magic principle, ADR-0002: no silent behavior).
- **+** Fits the existing functional-options builder (`aisotropy.New(opts...)`, `app.go`) exactly:
  `WithAuthenticator(...)` is one more `Option`, alongside `WithLLM`, `WithRateLimit`, etc.
- **−** **Behavioral break for implicit-open callers** — a runtime that today serves with no auth will
  now refuse to start/serve until it is told what to do. Mitigated: the break is a **one-line**
  opt-out (`WithAuthenticator(NoAuth())`), and the break is the *point* — it forces a conscious
  security decision exactly once, at wiring time. This is the correct direction for a break to run.

## Decision

**Adopt Option C.** Add a pluggable `Authenticator` seam, invoked by **one** Huma middleware, that
**MUST-AGREE** binds the verified principal to the path identity, defaulting to **deny** unless an
`Authenticator` is explicitly chosen.

### The seam

A small interface (provider-agnostic, mirroring ADR-0009):

```go
// Authenticator verifies a request's credential and returns the principal it
// attests to. It authenticates identity; it does not authorize the request.
type Authenticator interface {
    Authenticate(r *http.Request) (Principal, error)
}

// Principal is the verified identity a credential attests to.
type Principal struct {
    TenantID string
    UserID   string
    Scopes   []string // verified permission scopes (see open follow-on)
}
```

Wired via the existing builder: `aisotropy.WithAuthenticator(a Authenticator) Option`.

### One Huma middleware, MUST-AGREE bind

A single Huma middleware sits at the handler boundary, *before* the identity reaches
`context.Context` and the `NewScope` sites. It:

1. Calls the configured `Authenticator.Authenticate(r)`.
2. **On missing/invalid/unverifiable credential → `401 Unauthorized`.**
3. **On a verified principal whose `(TenantID, UserID)` does not equal the path
   `(tenant_id, user_id)` → `403 Forbidden`.** This is the MUST-AGREE bind: a valid token for tenant
   A **cannot** be used to address tenant B's path. The path param is no longer trusted; it must
   *agree* with the verified principal.
4. On agreement → the request proceeds, and the identity that flows into `ctx` and every `NewScope`
   call is now the **verified** one.

Distinguishing 401 (no/invalid credential) from 403 (valid credential, wrong tenant/user) is
deliberate: the two are different failures and different signals to the caller.

### Default-deny

`aisotropy.New(...)` with no `WithAuthenticator` selects a **deny-all** default: the chat path is
refused. To run, the operator must supply **either**:

- a real `Authenticator` (e.g. the reference `JWTBearer` below, or their own), **or**
- the explicit escape hatch `WithAuthenticator(NoAuth())`.

Insecure is therefore never the *default* — only ever an explicit, visible choice.

### Reference implementations

Two ship with the runtime, so the seam is usable out of the box:

| Impl | Behavior | License surface |
|---|---|---|
| `NoAuth()` | Trusts the path params exactly as today (`Principal{path tenant, path user}`); the explicit opt-out that reproduces current behavior. | none (stdlib only) |
| `JWTBearer(...)` | Verifies an `Authorization: Bearer <jwt>` HS256 signature with **Go stdlib `crypto/hmac` + `crypto/sha256`** (no third-party JWT dependency), extracts `tenant_id`/`user_id`/`scopes` claims into the `Principal`. | stdlib only — no new dep, satisfies the 100% MIT/Apache gate |

**RS256 / asymmetric verification against a JWKS endpoint is a named follow-on** (a second reference
impl), not part of this cut. The HS256 stdlib impl proves the seam end-to-end with **zero new
dependencies** (no `golang-jwt`, nothing to license-vet), consistent with the sovereignty and
supply-chain posture (ADR-0001, license-policy).

## Consequences

- **The front-door gap is closed → isolation is by-construction END-TO-END** when a real
  `Authenticator` is configured. The differentiator's central claim stops being true only at the
  storage layer and becomes true from the credential to the vector filter. The thesis's
  "authentication gap" section is superseded by this ADR.
- **Behavioral break for implicit-open callers** (Option C's cost): a runtime that served with no auth
  now refuses until told what to do. **Mitigation:** the one-line `WithAuthenticator(NoAuth())`
  opt-out reproduces the old behavior explicitly and visibly. The break is intentional — it converts a
  silent-insecure default into a conscious, one-time wiring decision.
- **OpenAPI struct contract unchanged** (ADR-0002): no JSON tag or field of `ChatInput`/`ChatRequest`
  changes; the generated OpenAPI 3.1 schema is untouched. Authentication lives in middleware and a
  builder option, not in the request body.
- **The `~8` `NewScope`-feeding handler sites are unchanged in shape** — they still call
  `facts.NewScope(tenant, user)` / `semantic.NewScope(...)`; the only difference is the identity they
  receive is now *verified*, not caller-asserted. No `scopecheck`-analyzed call site moves.
- **The regression anchor flips:** `TestPathParamTrustUnauthenticatedRequestIsAcceptedAuthnRegressionAnchor`
  must be inverted and renamed once the seam is wired with a non-`NoAuth` Authenticator — an
  unauthenticated request to an arbitrary tenant/user must then return **401/403**, not 200. Its
  failure under a real Authenticator is the intended signal that the posture changed (the test's own
  doc comment already prescribes this).
- **No new dependency, no new license surface:** the HS256 reference impl uses Go stdlib only; the
  100% MIT/Apache constitutional gate is preserved.

## Open follow-on (recorded, not yet decided)

**Verified `Principal.Scopes` should replace client-asserted `ChatRequest.Scopes` for tool-gating.**
Today the scopes that gate tool access come from the request **body**
(`ChatRequest.Scopes []string`, `internal/api/chat.go:71`; consumed at
`toolReg.PromptFragment(in.Body.Scopes)` and the double scope-gate in tool dispatch). That is a
**second trust hole of the same shape as this one**: a client currently **self-grants its own tool
scopes** by asserting them in the request, exactly as it previously self-asserted its own tenant/user
in the path. Once a verified `Principal` exists, tool-gating should read `Principal.Scopes` (the
verified, credential-derived scopes) instead of `Body.Scopes`. This is deliberately **not** resolved
here because it touches the request-body contract and the tool-dispatch scope-gate, and warrants its
own ADR — but it is recorded so the scopes hole is not forgotten now that the identity hole is closed.

## References

- [`docs/vision/thesis.md` — "The authentication gap"](../vision/thesis.md#the-authentication-gap-be-honest-about-it) (the admission this ADR supersedes/closes)
- [ADR-0011](0011-tenant-scoped-statement-builder.md) (isolation by construction at storage — the floor this ADR builds the front door above) · [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md)
- [ADR-0009](0009-provider-agnostic-llm-layer.md) (the provider-agnostic-seam precedent this mirrors) · [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract, kept unchanged; no silent behavior)
- [ADR-0013](0013-public-extension-surface-facade-vs-promote.md) / [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) (the `aisotropy.New(opts...)` builder this `WithAuthenticator` option extends)
- `aisotropy/isolation_e2e_test.go` — `TestPathParamTrustUnauthenticatedRequestIsAcceptedAuthnRegressionAnchor` (the executable regression anchor that inverts when the seam lands)
- [multi-tenancy](../architecture/multi-tenancy.md) · [runtime as built](../architecture/runtime-as-built.md)
