---
type: ADR
title: "ADR-0025 — Security-review hardening: default-closed ops endpoints, JWT posture, optional TLS enforcement"
description: A security review of the authn seam (ADR-0024) surfaced residual hardening gaps that ADR-0024 explicitly left as follow-ons or did not touch. Decision — three stdlib-only hardening levers, each with a default chosen for safety-by-construction. (1) The operator endpoints /costs and /bitrate return ALL-tenant aggregates and live OUTSIDE the /v1 auth chain; gate them default-CLOSED behind AISOTROPY_OPS_TOKEN with a constant-time (crypto/subtle) compare — 503 when unset, 401 on mismatch, token never logged. (2) Harden the HS256 JWTBearer — a 32-byte minimum signing secret with fail-fast-at-boot plus a fail-closed verifier as the last line, optional-but-required-when-set aud/iss (token-confusion / cross-audience replay), a max-TTL lifetime cap, and always-on nbf/iat sanity. (3) Optional plaintext rejection via AISOTROPY_REQUIRE_TLS so a bearer token cannot be transmitted in the clear. No new dependency (stdlib only). RS256/JWKS remains the one named capability follow-on.
tags: [decisions, adr, security, hardening, authentication, jwt, ops-endpoints, tls, multi-tenancy]
timestamp: 2026-07-14T00:00:00Z
---

# ADR-0025 — Security-review hardening: default-closed ops endpoints, JWT posture, optional TLS enforcement

## Status

✅ **Accepted — 2026-07-14.** This ADR is a **follow-up to [ADR-0024](0024-authenticated-identity-seam.md)**
(the authenticated-identity seam). ADR-0024 closed the front-door identity gap — path `tenant_id`/`user_id`
are now bound to a verified principal by a fail-closed Huma middleware. A security review of that shipped seam
found three residual hardening gaps: two that ADR-0024 named as follow-ons or left out of its first cut
(RS256/JWKS aside), and one — the operator endpoints — that sat entirely *outside* the `/v1` chain the seam
protects. This ADR records the decisions that close them. It does **not** supersede ADR-0024; it hardens the
same boundary.

## Context

ADR-0024 makes isolation by-construction end-to-end **for the `/v1` chat/knowledge routes** when a real
`Authenticator` is configured. The review confirmed that guarantee holds, then examined everything adjacent to
it. Three concrete gaps:

1. **Operator endpoints leak all-tenant data with no credential.** `GET /costs`
   (`costMeter.Snapshot()` — per-tenant token/cost aggregate across *every* tenant) and `GET /bitrate`
   (`bitrateStore.AllSummaries()` — cross-tenant memory-lift data) are registered as **raw chi routes**
   ([`aisotropy/app.go`](../../aisotropy/app.go) `/costs`, `/bitrate`), *outside* the Huma
   `UseMiddleware` chain that `BindAuthMiddleware`/`DenyAllMiddleware` feed. They are structurally different
   from `/v1`: `/v1` is per-`(tenant, user)` and returns one tenant's data behind the must-agree bind; the ops
   endpoints return **cross-tenant aggregates**. Before this ADR, any unauthenticated caller who could reach
   the port could read them — a cross-tenant disclosure that the ADR-0024 gate, by design, never covered.

2. **The HS256 `JWTBearer` verifier accepted a weak posture.** As first shipped (ADR-0024) the stdlib HS256
   verifier ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)) required only a valid signature + `exp`. It
   accepted a signing secret of **any length** (an HMAC-SHA256 key shorter than the 256-bit output is
   brute-forceable), performed **no audience/issuer check** (a token minted for another service with the same
   secret would verify — token confusion / cross-audience replay), and placed **no upper bound on token
   lifetime** (a mistakenly minted, effectively immortal token verified forever).

3. **Bearer credentials could travel in plaintext.** Both the `/v1` JWT and the new ops token are bearer
   credentials; nothing in the binary refused to accept them over a non-TLS connection.

Each gap is a *default* question as much as a mechanism question — consistent with ADR-0024's own framing
(Option C: "what is the default when the operator wires nothing"). The review's guiding rule: a mechanism whose
insecure state is reachable by accident is not a fix.

## Goals

- **Close the operator-endpoint cross-tenant disclosure** with a gate whose *default is closed*, so an
  unconfigured deployment cannot leak all-tenant aggregates — mirroring ADR-0024's default-deny posture for the
  adjacent `/v1` chain.
- **Raise the HS256 verifier to a defensible posture** (minimum secret strength, audience/issuer binding, a
  lifetime cap, standard temporal claims) **without** forcing a break on operators who already mint conformant
  tokens — the new claim checks are *optional, and required only when configured*.
- **Let an operator refuse plaintext bearer credentials** when the binary terminates TLS itself, without
  breaking the common behind-a-proxy topology where TLS is terminated upstream.
- **Add no dependency.** Every lever is Go stdlib only (`crypto/subtle`, `crypto/hmac`, `crypto/sha256`,
  `crypto/tls`, `net/http`) — the 100% MIT/Apache constitutional gate and the offline-green build stay intact.

## Non-goals

- **NOT RS256 / JWKS.** Asymmetric verification against a rotating JWKS endpoint remains the one named
  **capability follow-on** from ADR-0024 (it needs asymmetric-key + key-rotation plumbing this minimal verifier
  does not attempt). This ADR hardens the HS256 path; it does not replace it.
- **NOT an authorization policy** beyond ADR-0024's identity↔path bind and the existing scope-gated tool
  dispatch. The ops token is a single shared operator credential, not per-operator RBAC.
- **NOT mandatory TLS.** TLS enforcement is *opt-in* precisely because most deployments sit behind a
  proxy/load balancer that terminates TLS and forwards plain HTTP (where `r.TLS` is legitimately nil).
- **NOT a shared/replicated ops store.** `/costs` and `/bitrate` remain per-process observability aggregates
  (see the runbook's single-process caveat); gating them does not make them a billing system of record.

## Decision

Three stdlib-only hardening levers, each with a safety-first default.

### 1. Operator endpoints — default-closed, constant-time bearer token

A single chi middleware `OpsAuthGate(opsToken)` ([`internal/api/ops_gate.go`](../../internal/api/ops_gate.go))
guards `/costs` and `/bitrate`, wired via `router.With(opsGate)` at both routes
([`aisotropy/app.go`](../../aisotropy/app.go) `/costs`, `/bitrate`). Posture (default-closed, consistent with
ADR-0024's Option C — no silent open):

- **`AISOTROPY_OPS_TOKEN` unset (`opsToken == ""`): the endpoints are CLOSED.** Every request gets **503** with
  a JSON body telling the operator to set the variable. An unconfigured deployment does **not** leak
  all-tenant data — this is the safe default.
- **Set: the request MUST carry `Authorization: Bearer <token>` matching `opsToken`** via
  `crypto/subtle.ConstantTimeCompare` (constant-time and length-safe). A missing header, malformed header, or
  mismatched token is a **generic 401** — the gate reveals nothing about *why* it rejected. The token is
  **never logged** (`main.go` logs only its *presence*, never the value).

`OpsTokenFromEnv()` is the single source of truth for the env-var name (`OpsEnvVar = "AISOTROPY_OPS_TOKEN"`) so
the wiring and the gate never diverge. The `/healthz`, `/health`, `/openapi.json`, `/docs` routes stay open (no
tenant data).

### 2. HS256 JWT hardening (all in [`internal/auth/jwt.go`](../../internal/auth/jwt.go))

- **Minimum secret strength, fail-fast + fail-closed.** `MinHS256SecretBytes = 32`; `ValidateHS256Secret`
  returns a clear error for a shorter secret. The boot path
  ([`aisotropy/authenv.go`](../../aisotropy/authenv.go) `AuthenticatorFromEnv`) calls it and **refuses to boot**
  on a weak `AISOTROPY_JWT_SECRET` (the error never contains the secret). As a defense-in-depth last line, a
  `jwtBearer` constructed from a short secret sets `secretTooShort` and **fails closed** — every
  `Authenticate` returns `ErrUnauthenticated` — so a brute-forceable key can never authenticate a request even
  if the fail-fast check were bypassed.
- **Audience binding — `WithAudience` / `AISOTROPY_JWT_AUDIENCE`.** When set, the token's `aud` claim (a string
  or a string array per RFC 7519) **must contain** the expected audience; a missing/malformed `aud` is rejected
  (fail closed). Closes token-confusion / cross-audience replay. **Optional** — unset means `aud` is not
  checked, so existing tokens keep verifying.
- **Issuer binding — `WithIssuer` / `AISOTROPY_JWT_ISSUER`.** When set, the token's `iss` must equal it exactly.
  Optional, same back-compat rule.
- **Max lifetime — `WithMaxTTL` / `AISOTROPY_JWT_MAX_TTL` (a Go duration).** When set, a token whose
  `(exp - iat)` — or `(exp - now)` when `iat` is absent — exceeds the cap is rejected, bounding an effectively
  immortal token. A malformed duration is a **fatal config fault** at boot (not a per-request outcome).
  Optional (unset = lifetime bounded only by the required `exp`).
- **Standard temporal claims, always on.** `exp` is **required** (a token with no expiry is rejected — fail
  closed). `nbf` is honored when present (respecting `WithLeeway` clock-skew), and an `iat` stamped in the far
  future (beyond leeway) is rejected as a forged/clock-broken issuer.

The public re-exports (`aisotropy.WithJWTAudience` / `WithJWTIssuer` / `WithJWTMaxTTL`,
[`aisotropy/auth.go`](../../aisotropy/auth.go)) let an embedder harden JWT verification via
`NewJWTAuthenticator(secret, WithJWTAudience("…"))`; `AuthenticatorFromEnv` wires the same three from the
environment for the shipped binary.

### 3. Optional TLS enforcement — `AISOTROPY_REQUIRE_TLS`

When `requireTLSFromEnv()` is truthy (per `strconv.ParseBool`), `App.Run` registers a chi `Use` middleware
**before any route** ([`aisotropy/app.go`](../../aisotropy/app.go), gated by `requireTLSFromEnv`) that rejects
any request with `r.TLS == nil` — **HTTP 400** `{"error":"TLS required"}` — so a bearer token (the `/v1` JWT or
`AISOTROPY_OPS_TOKEN`) can never be transmitted in plaintext. **OFF by default**: most deployments terminate
TLS at a proxy and forward plain HTTP (`r.TLS` legitimately nil); turn it on only when `aisotropyd` terminates
TLS itself.

## Consequences

- **The operator-endpoint cross-tenant disclosure is closed, default-closed.** An unconfigured deployment
  answers `/costs`/`/bitrate` with **503** instead of leaking cross-tenant aggregates. **Behavioral note:** an
  operator who relied on scraping `/costs` without a token must now set `AISOTROPY_OPS_TOKEN` and present it —
  a conscious, one-time securing step, the correct direction for the break to run.
- **The HS256 verifier now has a defensible posture** (≥32-byte secret enforced at boot *and* fail-closed;
  optional aud/iss binding; optional lifetime cap; required `exp`, honored `nbf`/`iat`). **Back-compat:** the
  aud/iss/max-TTL checks are **optional and off by default**, so tokens minted before this ADR keep verifying
  unchanged; the only hard break is that a **sub-32-byte signing secret now refuses to boot** (intentional — a
  weak key was never safe).
- **A bearer credential can be refused in the clear** when the operator opts into `AISOTROPY_REQUIRE_TLS`; the
  common behind-a-proxy topology is unaffected because the default is off.
- **No new dependency, no new license surface** — stdlib only (`crypto/subtle`, `crypto/hmac`, `crypto/sha256`,
  `crypto/tls`); the 100% MIT/Apache gate is preserved, consistent with ADR-0024 and ADR-0001.
- **Startup posture is observable, secrets are not.** `cmd/aisotropyd/main.go` logs the *presence* of the ops
  token, the auth mode (`jwt-hs256` vs the NoAuth warning), and whether TLS enforcement is on — never any
  secret value.

## Follow-on and residual posture

- **RS256 / JWKS remains the one named capability follow-on** (inherited from ADR-0024): asymmetric
  verification against a rotating JWKS endpoint, a second reference `Authenticator`. Not a blocker for the
  seam; the hardened HS256 path stands on its own for symmetric-secret deployments.

The review's **LOW-severity** items, recorded for honesty — **handled** vs **acknowledged (not code-fixed)**:

| Item | Disposition |
|---|---|
| Cross-tenant disclosure via `/costs`, `/bitrate` | **Handled** — default-closed `OpsAuthGate` (§1). |
| JWT token-confusion / cross-audience replay | **Handled** — optional `aud`/`iss` binding (§2). |
| Weak (brute-forceable) HS256 secret | **Handled** — 32-byte minimum, fail-fast at boot + fail-closed verifier (§2). |
| Effectively immortal tokens | **Handled** — optional `AISOTROPY_JWT_MAX_TTL` lifetime cap (§2). |
| Bearer token in plaintext | **Handled (opt-in)** — optional `AISOTROPY_REQUIRE_TLS` (§3). TLS termination itself stays the operator's responsibility. |
| `session_id` is caller-supplied in the request body | **Acknowledged, not code-fixed** — `session_id` carries **no authorization meaning**; it partitions memory *within* the already-verified `(tenant, user)` scope, so a forged/guessed session id can only reach that same principal's own sessions — no cross-tenant or cross-user risk. Not worth a control. |
| Prompt-injection via hydrated memory | **Acknowledged, out of scope** — this is a separate, already-tracked surface (threat-model **T9**): input/output guardrails, provenance separation, and stored-content sanitization. It is not an authentication concern and is deliberately untouched by this ADR. |

## References

- [ADR-0024 — authenticated-identity seam](0024-authenticated-identity-seam.md) (the seam this ADR hardens; its
  named RS256/JWKS follow-on is carried forward)
- [ADR-0011 — tenant-scoped statement builder](0011-tenant-scoped-statement-builder.md) (the storage-isolation
  floor) · [ADR-0009 — provider-agnostic LLM layer](0009-provider-agnostic-llm-layer.md) (the pluggable-seam
  precedent ADR-0024 mirrored) · [ADR-0002 — struct-as-contract, no silent behavior](0002-anti-framework-magic-struct-as-contract.md)
- [ADR-0001 — Go/Rust in-process-first stack](0001-go-rust-in-process-first-stack.md) / [constitutional gates](../../.claude/rules/constitutional-gates.md) (the 100% MIT/Apache gate this ADR preserves)
- Code: [`internal/api/ops_gate.go`](../../internal/api/ops_gate.go) (ops gate) ·
  [`internal/auth/jwt.go`](../../internal/auth/jwt.go) (HS256 hardening) ·
  [`aisotropy/authenv.go`](../../aisotropy/authenv.go) (env wiring + fail-fast) ·
  [`aisotropy/auth.go`](../../aisotropy/auth.go) (public re-exports) ·
  [`aisotropy/app.go`](../../aisotropy/app.go) (ops-gate + TLS middleware wiring) ·
  [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go) (boot, posture logging)
- [threat model](../architecture/threat-model.md) (the threats this ADR closes/acknowledges) ·
  [operations runbook](../deployment/runbook.md) (secure-deployment env vars) ·
  [runtime as built](../architecture/runtime-as-built.md) (ops-gate + TLS middleware in the pipeline)
