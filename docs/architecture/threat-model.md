---
type: Concept
title: Threat model — the multi-tenant isolation boundary
description: The security threat model for Aisotropy's #1 differentiator, tenant/user isolation. Enumerates the trust boundary, the assets, the concrete threats (cross-tenant leak, prompt-injection tool bypass, identity forgery, cost exhaustion, consolidation poisoning, memory-persistence poisoning, secret leakage, supply chain), the mitigation that is REAL vs aspirational today, and the tenant-leak testing strategy. Honest about the load-bearing gap: identity is not yet authenticated.
tags: [architecture, security, threat-model, multi-tenancy, isolation, prompt-injection, scopes]
timestamp: 2026-07-14T00:00:00Z
---

# Threat model — the multi-tenant isolation boundary

Tenant isolation-by-construction is the project's strongest, hardest-to-retrofit claim
([ADR-0020](../decisions/0020-build-vs-adopt-lang-suite-mastra.md), the competitive
[moat](../research/competitive-landscape.md)). A moat that isolation-critical needs an explicit
threat model — otherwise "isolation by construction" is an assertion, not an engineering property.
This document enumerates the boundary, the threats, and — per the
[reality ledger](reality-ledger.md) discipline — separates the mitigations that are **real today**
from the ones that are **aspirational**.

This complements [multi-tenancy](multi-tenancy.md) (the isolation *design*) and the
[constitutional gates](../../.claude/rules/constitutional-gates.md) (the merge-time enforcement).

> **Validation note (2026-07-14):** every threat row below was re-verified against the code
> (file/symbol references checked, not copied from other docs). Two changes from that pass:
> the identity caveat now cites the exact contract (`internal/api/chat.go`), and **T9**
> (memory-persistence poisoning) was added — the hydration path was previously covered only
> as T2's "injected content in retrieved context" vector, without naming the persistence loop.

> **Authn-seam + hardening note (2026-07-14, [ADR-0024](../decisions/0024-authenticated-identity-seam.md)
> + [ADR-0025](../decisions/0025-security-hardening-authn-ops.md)):** the load-bearing caveat below has
> **materially changed**. ADR-0024 shipped a fail-closed authenticated-identity seam (a Huma middleware
> that binds the verified principal to the path `(tenant, user)`, default-deny for library users), and
> the ADR-0025 security review then hardened it: the **operator endpoints `/costs`/`/bitrate` are now
> gated default-closed** (T10), and the HS256 `JWTBearer` gained **audience/issuer binding** (T11, token
> confusion), a **32-byte minimum secret with fail-fast + fail-closed** (T12, weak-secret brute force),
> and a **max-lifetime cap** (T13, immortal tokens). **T3 is accordingly downgraded from the #1
> aspirational gap to REAL-when-configured.** Honest residual: authentication is **opt-in** — the shipped
> binary still defaults to `NoAuth()` (with a startup warning) unless `AISOTROPY_JWT_SECRET` is set, so a
> deployment that ignores the warning is still trusting the edge. The aud/iss/max-TTL checks are optional
> (off by default, so existing tokens keep verifying); TLS enforcement is opt-in
> (`AISOTROPY_REQUIRE_TLS`). See the acknowledged-LOW list after the gap ledger for what was deliberately
> **not** code-fixed.

## The load-bearing caveat, stated first

**Identity can now be authenticated in-process — but only when the operator opts in.** ADR-0024
shipped the authenticated-identity seam and ADR-0025 hardened it, so the old "no auth middleware
exists" caveat no longer holds by construction. `tenant_id`/`user_id` are still **path params**
(charset-constrained, Huma-validated — `ChatInput` in [`internal/api/chat.go`](../../internal/api/chat.go)),
but a single **fail-closed Huma middleware now runs in front of every `/v1` route**
([`internal/api/authn.go`](../../internal/api/authn.go)): when a real `Authenticator` is configured it
**must-agree**-binds the verified principal to the path `(tenant, user)` (401 on missing/invalid, 403 on
mismatch) and the **verified** scopes — not the client-asserted body scopes — gate tools
(`effectiveScopes`). Library users who wire nothing get **default-deny** (401 on every `/v1` request).

The honest residual: authentication is **opt-in for the shipped binary**. `AuthenticatorFromEnv`
([`aisotropy/authenv.go`](../../aisotropy/authenv.go)) selects the stdlib HS256 `JWTBearer` only when
`AISOTROPY_JWT_SECRET` is set; unset, the binary boots in `NoAuth()` mode (path `(tenant, user)` trusted
verbatim) **with a startup warning**. So every isolation guarantee below is now of the form *"correct, and
enforced in-process **once an authenticator is configured**; correct given a trusted edge otherwise."* For
any non-local deployment the operator MUST set `AISOTROPY_JWT_SECRET` (and, for defence in depth, an
`AISOTROPY_OPS_TOKEN` and TLS in front / `AISOTROPY_REQUIRE_TLS`) — or keep running behind an authenticated
gateway/proxy. See the [operations runbook](../deployment/runbook.md) for the secure-deployment env vars.

## Trust boundary & assets

```
  UNTRUSTED                    │  TRUSTED (behind the fail-closed authn middleware — ADR-0024/0025;
                               │           in-process when an Authenticator is configured, else NoAuth)
  caller / model output        │
 ─────────────────────────────┼──────────────────────────────────────────────────────────────
  HTTP request (tenant_id,     │  Huma contract validation → ctx(tenant,user) → per-tier scoped
  user_id path; scopes,        │  reads/writes: BadgerDB (working), SQLite/rqlite (facts, FTS5),
  session_id, prompt body)     │  Qdrant/cosine (semantic), + the async outbox → sleep-cycle worker
  LLM completions (tool calls) │
```

**Assets to protect** (all partitioned by `(tenant, user[, session])`):

- Working-memory chat history + summary buffer (BadgerDB).
- Facts / tasks / rules (SQLite+FTS5 or rqlite).
- Semantic vectors — `KindDocument` (user- or tenant-scoped), `KindFact`, `KindTurn` (Qdrant/cosine).
- Per-tenant token spend + quota state (cost meter, rate-limit buckets).
- Provider API keys / secrets.

## Threats and mitigations

| # | Threat | Vector | Mitigation | Status |
|---|---|---|---|---|
| **T1** | **Cross-tenant data leak** (tenant A reads tenant B's memory/RAG) | a memory read/RAG query not scoped by `(tenant,user)` | `tenant_id` injected once at the Huma boundary and propagated **only** via `context.Context`; every tier read/write is scoped in the key/statement, never in an intermediate layer. Facts use the [tenant-scoped statement builder](../../internal/tenancy) where an unscoped statement is a **compile error** (ADR-0011). Qdrant filters by payload **during** the HNSW scan (not a post-filter — a post-filter is a leak). | **REAL** — enforced by the isolation constitutional gate + `scope_isolation_test.go` |
| **T2** | **Prompt-injection → tool bypass / privilege escalation** (model coerced into calling a tool the caller's scopes don't grant) | adversarial prompt / injected content in retrieved context (the persistence side of this vector is T9) | **Governance by absence**: a non-permitted tool is never named in the prompt, AND the chosen tool name is re-validated against `Registry.Permitted(scopes)` before execution — structural, so a hallucinated/injected name **cannot** execute. Both the homegrown `agentloop` and the native Gollem path enforce it (ADR-0012). | **REAL** — `agentloop` + native path, fail-on-call executor tests |
| **T3** | **Identity / scope forgery** (caller claims another tenant, or grants itself `write:*`) | scopes in the request body; path params | **Closed in-process WHEN an Authenticator is configured** (ADR-0024/0025): the fail-closed `BindAuthMiddleware` ([`internal/api/authn.go`](../../internal/api/authn.go)) must-agree-binds the **verified** principal to the path `(tenant, user)` (401 missing/invalid, 403 mismatch), and `effectiveScopes` gates tools on the **verified** scopes so a client cannot self-grant scopes via the body. Library users who wire nothing get **default-deny**. Residual: the shipped binary is **opt-in** — `NoAuth()` (with warning) unless `AISOTROPY_JWT_SECRET` is set. | **REAL when configured** (was the #1 aspirational gap). The stdlib HS256 verifier is hardened (min-32 secret + fail-fast/closed T12, aud/iss T11, max-TTL T13). Follow-on: RS256/JWKS |
| **T4** | **Cost / quota exhaustion** (one tenant burns the LLM budget / DoS via token spend) | flooding the chat routes | Per-tenant token-bucket rate limit **before any LLM call** (reject/degrade, not after); circuit breaker fails fast on provider failure; quota-pressure model degradation to a cheaper model at ≥95%. | **REAL** on `/chat` + `/chat/stream`; ⚠️ **gap**: `/chat/agent` is NOT rate-limited, and the buckets + cost meter are **per-instance** (no cross-replica quota) |
| **T5** | **Consolidation / sleep-cycle cross-tenant surface** | the batch worker's `ExportAll` reads across tenants | The sleep-cycle `ExportAll` is a **deliberate, reviewed cross-tenant read**; dedup results are `Delete`d back **within each `(tenant,user)` scope**. It is the one place tenant data co-resides in one process buffer — flagged, not accidental. | **REAL but a reviewed exception** — dormant by default (needs `AISOTROPY_SLEEPCYCLE_BIN`) |
| **T6** | **Secret leakage** (API keys in logs/traces/commits) | `slog` call sites, OTel spans, commits | Secrets via env only; the content-policy + secret-scanning gates block committing them; OTel captures `gen_ai.usage.*` + identity, not payload secrets. | **REAL** — constitutional gate + hooks |
| **T7** | **Supply-chain compromise** | a malicious/typo-squatted dependency | Small surface: Go module graph + one vendored Rust crate; 100% permissive (MIT/BSD/Apache-2.0/ISC), zero copyleft (a copyleft/BUSL dep needs an ADR); digest pinning discipline. | **REAL** (structural) — far smaller attack surface than an npm/pip tree |
| **T8** | **PII in logs / model context** | user PII flowing into audit logs or the model | Gollem `RedactPII` interceptor (email pattern) on the reflection path; audit log records message *shape*, not content. | **PARTIAL** — reflection path only; conservative pattern (email); streaming path not covered |
| **T9** | **Memory-persistence poisoning** (prompt injection that *survives the turn*: adversarial text written into a memory tier becomes instruction-adjacent on every later hydration) | any write path into the tiers — chat turns (outbox → consolidation embeds `KindTurn` and distills facts, [`internal/consolidation/sink.go`](../../internal/consolidation/sink.go)), the knowledge ingest routes (`KindDocument`), including the **tenant-shared** ingest (ADR-0019) whose documents hydrate into *every* user's prompts in the tenant | `hydrateSystemPrompt` ([`internal/api/chat_stream.go`](../../internal/api/chat_stream.go)) concatenates facts + semantic hits + the working-memory summary into the system prompt as plain text under section headers — **no data/instruction separation, no sanitization of stored content**. Consequences are *contained*, not prevented: the T2 scope-gate means poisoned text still cannot execute a non-permitted tool, structured output constrains the reply shape, and poison stays inside its own `(tenant,user)` partition — except tenant-shared documents, which fan out tenant-wide. | **PARTIAL** — containment (T2 scope-gate + T1 partitioning + structured output) is REAL; input-side defense (provenance separation, stored-content sanitization) is **unbuilt** — a separate tracked surface, deliberately out of scope of the authn work (ADR-0025) |
| **T10** | **Cross-tenant disclosure via operator endpoints** (an unauthenticated caller reads all-tenant aggregates) | `GET /costs` (`costMeter.Snapshot()`, per-tenant cost across every tenant) and `GET /bitrate` (`bitrateStore.AllSummaries()`, cross-tenant memory-lift) are raw chi routes registered **outside** the `/v1` auth chain | **Gated default-closed** (ADR-0025): `OpsAuthGate` ([`internal/api/ops_gate.go`](../../internal/api/ops_gate.go), wired at `router.With(opsGate)` in [`aisotropy/app.go`](../../aisotropy/app.go)) returns **503** when `AISOTROPY_OPS_TOKEN` is unset (safe default — no leak) and otherwise requires `Authorization: Bearer <token>` compared with `crypto/subtle.ConstantTimeCompare`; a miss is a generic **401**. Token never logged; errors reveal nothing. | **REAL** — default-closed, constant-time. Shared single operator token (not per-operator RBAC); still per-process aggregates (see runbook §1) |
| **T11** | **JWT token confusion / cross-audience replay** (a token minted for a different service, signed with the same HS256 secret, is replayed at aisotropyd) | a valid-signature bearer token whose `aud`/`iss` were never meant for this runtime | **Optional aud/iss binding** (ADR-0025, [`internal/auth/jwt.go`](../../internal/auth/jwt.go)): `WithAudience`/`AISOTROPY_JWT_AUDIENCE` requires the `aud` claim (string or array, RFC 7519) to contain the expected value; `WithIssuer`/`AISOTROPY_JWT_ISSUER` requires exact `iss`; a missing/mismatched claim is rejected (fail closed). | **REAL when configured** — optional (off by default so existing tokens keep verifying); recommended for any multi-service secret-sharing deployment |
| **T12** | **Weak-secret brute force** (a short HS256 signing secret is brute-forced/dictionary-attacked, letting an attacker forge any principal) | an HMAC-SHA256 secret shorter than the 256-bit output | **32-byte minimum, fail-fast + fail-closed** (ADR-0025): `ValidateHS256Secret` (min `MinHS256SecretBytes = 32`) makes the binary **refuse to boot** on a weak `AISOTROPY_JWT_SECRET` ([`aisotropy/authenv.go`](../../aisotropy/authenv.go), error never contains the secret); as last-line defense a verifier built from a short secret sets `secretTooShort` and **rejects every request** ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)). | **REAL** — enforced at boot and in the verifier |
| **T13** | **Long-lived / immortal tokens** (a mistakenly minted token with a far-future or no bounded lifetime authenticates indefinitely) | a token whose `(exp - iat)` is absurdly large | **Optional max-TTL cap + required `exp`** (ADR-0025): `WithMaxTTL`/`AISOTROPY_JWT_MAX_TTL` rejects a token whose `(exp - iat)` — or `(exp - now)` when `iat` is absent — exceeds the cap; `exp` is **always required** (a token with no expiry is rejected), `nbf` honored when present, and a far-future `iat` rejected ([`internal/auth/jwt.go`](../../internal/auth/jwt.go)). | **REAL when configured** — the cap is optional; the required-`exp` / `nbf` / `iat` checks are always on |

## The honest gap ledger (do not oversell the moat)

1. **Authentication is now in-process but opt-in (T3).** The verified-token → `(tenant,user,scopes)`
   control now exists (ADR-0024/0025): the fail-closed middleware and the hardened HS256 verifier close
   T3/T11/T12/T13 **when configured**, and the operator endpoints are gated (T10). The residual gap is
   **operator discipline, not a missing mechanism**: the shipped binary defaults to `NoAuth()` (with a
   startup warning) unless `AISOTROPY_JWT_SECRET` is set, so a non-local deployment that ignores the
   warning is still "correct given a trusted edge." Setting the secret (plus an ops token and TLS) is the
   top deployment-time security step; RS256/JWKS is the one named capability follow-on.
2. **`/chat/agent` is unmetered and un-rate-limited** — a cost/DoS hole (T4).
3. **Rate limit + cost meter are per-instance** — a multi-replica deployment has no shared quota;
   a tenant could exceed its bucket by fanning out across replicas (T4).
4. **PII redaction is reflection-path-only and email-only** (T8).
5. **The sleep-cycle `ExportAll` is the one sanctioned cross-tenant read** (T5) — correct today, but
   any future change to it is a high-blast-radius isolation risk and must stay reviewed.
6. **Hydrated memory is instruction-adjacent (T9).** Facts, semantic hits, and the working-memory
   summary are folded into the system prompt verbatim; a poisoned stored item rides along on every
   later turn of that `(tenant,user)` scope. Containment is real (scope-gated tools, structured
   output, per-scope partitioning) but there is no sanitization or provenance separation on the
   retrieval side, and the tenant-shared knowledge route is the widest fan-out for a single
   poisoned write.

## Acknowledged-LOW — deliberately not code-fixed (ADR-0025 review)

The ADR-0025 security review left three items **unfixed on purpose** — each is either not an
authentication concern or carries no cross-tenant risk. Recorded here so "we looked and chose not to
act" is explicit, not an oversight:

- **`session_id` is caller-supplied in the request body.** It carries **no authorization meaning**: it
  partitions memory *within* the already-verified `(tenant, user)` scope, so a forged or guessed session
  id can only reach that same principal's own sessions — **no cross-tenant or cross-user reach**. Not
  worth a control. (Under `NoAuth` the whole `(tenant, user)` triple is trusted anyway, so `session_id`
  adds no incremental exposure.)
- **Bearer auth assumes TLS in transit.** Both the `/v1` JWT (T3/T11–T13) and the ops token (T10) are
  bearer credentials; on a plaintext hop they can be sniffed and replayed. TLS **termination** stays the
  operator's responsibility (proxy/load balancer, the common topology). The only in-binary lever is
  **opt-in** plaintext rejection via `AISOTROPY_REQUIRE_TLS` (ADR-0025 §3) — off by default so
  behind-a-proxy deployments are unaffected. Deploy bearer auth only over TLS.
- **Prompt-injection via hydrated memory (T9) remains a separate tracked surface.** Input/output
  guardrails, provenance separation, and stored-content sanitization are a distinct workstream, **not**
  part of the authn hardening — ADR-0025 deliberately does not touch it. Containment (T2 scope-gate,
  structured output, per-scope partitioning) is what stands today.

## Testing strategy (how the moat is proven, not asserted)

- **Per-tier scope-isolation tests** (`internal/facts/scope_isolation_test.go` and peers): write
  under `(tenantA,userA)`, assert a read under `(tenantB,*)` or `(tenantA,userB)` returns nothing.
- **Compile-time isolation**: `internal/tenancy` makes an unscoped facts statement a build error
  (ADR-0011) — the strongest possible test (it can't compile wrong).
- **Tool scope-gate tests**: a `mustNotRun` executor asserts a non-permitted / hallucinated tool
  name never reaches execution on both dispatch paths.
- **End-to-end isolation + authn suite** (`aisotropy/isolation_e2e_test.go`): per-tier and concurrent
  cross-tenant leak tests over the wired runtime, plus the ADR-0024 regression anchor
  `TestPathParamTrustUnauthenticatedRequestIsRejectedByDefaultAuthnRegressionAnchor` — which now asserts
  an unauthenticated request to an arbitrary tenant/user is **rejected by default** (the pre-seam anchor
  that asserted it was *accepted* has flipped, confirming the T3 posture change).
- **What is NOT yet tested**: a full tenant-leak fuzz across the request path with *varied* verified JWT
  principals (identity is now authenticatable, so this is no longer blocked on T3 — just not yet
  written); cross-replica quota isolation; and a stored-content injection test for T9 (poison a
  fact/document, then assert on a later turn that tool access is still scope-gated and the reply schema
  still enforced — the *containment* is covered indirectly by the T2 tests, the persistence path itself
  is not exercised).

## References

- [multi-tenancy](multi-tenancy.md) — the isolation design this model attacks.
- [reality ledger](reality-ledger.md) — REAL vs dormant vs aspirational status.
- [ADR-0024 — authenticated-identity seam](../decisions/0024-authenticated-identity-seam.md) · [ADR-0025 — security-review hardening (ops endpoints, JWT posture, optional TLS)](../decisions/0025-security-hardening-authn-ops.md) — T3/T10–T13.
- [ADR-0011 — tenant-scoped statement builder](../decisions/0011-tenant-scoped-statement-builder.md) · [ADR-0007 — physical tenant topology](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md) · [ADR-0012 — native tool-calling scope-gate](../decisions/0012-gollem-native-tool-calling-vs-homegrown-agentloop.md) · [ADR-0019 — tenant-wide document sharing](../decisions/0019-tenant-wide-document-sharing.md)
- [operations runbook](../deployment/runbook.md) — the secure-deployment env vars (`AISOTROPY_JWT_SECRET`, `AISOTROPY_OPS_TOKEN`, `AISOTROPY_JWT_AUDIENCE`/`ISSUER`/`MAX_TTL`, `AISOTROPY_REQUIRE_TLS`).
- [constitutional gates](../../.claude/rules/constitutional-gates.md) — merge-time enforcement of T1/T4/T6.
- [granular control](granular-control.md) — the security × cost × behavior knobs.
