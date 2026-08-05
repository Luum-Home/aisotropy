---
type: Concept
title: Multi-tenancy — Isolation via ctx
description: How Aisotropy guarantees real isolation between tenants and users — native propagation via Go's context.Context, anti-leak composite keys, and per-layer filtering. The physical topology is left for ADR-0007.
tags: [architecture, multi-tenancy, ctx, isolation, security]
timestamp: 2026-07-08T00:00:00Z
---

# Multi-tenancy — Isolation via ctx

Aisotropy treats multi-tenancy as a **structural** property of the runtime, not a filter bolted
on afterward. The mechanism is deliberately boring: Go's `context.Context`, the same pattern any
native HTTP service uses to propagate deadlines and cancellation. Zero AI library involved — it's
the direct application of the [zero magic](../vision/thesis.md#the-stance-zero-magic) principle.

## The mechanism: ctx as a process descriptor

The [Huma](../components/huma-api.md) handler boundary is the **only** layer authorized to
*write* identity into the `ctx`, and — as of the **authenticated-identity seam**
([ADR-0024](../decisions/0024-authenticated-identity-seam.md), shipped *2026-07-14*) — that write
now happens **after** the identity has been verified and bound. `tenant_id` and `user_id` still
arrive as validated **path parameters** (Huma rejects anything outside `^[a-zA-Z0-9_-]{1,64}$`
with a 422 before the handler runs), and `scopes` is still a **client-asserted body field**
(`ChatRequest.Scopes` — see `internal/api/chat.go`), but a single fail-closed **Authenticator**
middleware now sits in front of every `/v1` route and closes the front-door gap.

**How the seam works** (`internal/auth/auth.go`, `internal/api/authn.go`,
`aisotropy/auth.go`, wired in `aisotropy/app.go:404-417`):

- One Huma API middleware (`api.BindAuthMiddleware`) is registered via `humaAPI.UseMiddleware`
  **before the first route**. It calls the configured `Authenticator.Authenticate`, which verifies
  the caller's credential and returns a `Principal{TenantID, UserID, Scopes}`.
- **Must-agree bind:** the verified `Principal.TenantID`/`UserID` MUST equal the URL path
  `tenant_id`/`user_id`. A mismatch is a **403**; a missing/invalid credential is a **401**. Only
  after the bind passes does the middleware inject identity into `ctx` (`withVerifiedPrincipal`),
  so the ~8 downstream `NewScope` sites keep reading `in.TenantID`/`in.UserID` from the path
  **unchanged** — those values are now *proven equal to a verified principal*, not a bare
  assertion. The `Scope` is minted from a verified identity.
- **Default-deny (Option C):** if no authenticator is registered, `app.go` installs
  `api.DenyAllMiddleware`, which rejects **every** `/v1` request with 401. Running insecure is not
  reachable by accident — it requires an explicit opt-out.
- **`NoAuth()` opt-out (honest caveat):** `aisotropy.NoAuth()` (`internal/auth/auth.go`) is a named
  Authenticator that performs **no verification** — it returns the path `tenant_id`/`user_id`
  verbatim, so the must-agree bind is a tautology that never rejects. This reproduces the pre-seam
  behavior (the path param is still *trusted*, not authenticated), but it is now an **explicit
  deployment choice** for local/dev or an upstream gateway that already authenticates — never the
  silent default.
- **Reference JWT impl:** `aisotropy.NewJWTAuthenticator` (`internal/auth/jwt.go`) verifies an
  `Authorization: Bearer <jwt>` credential — HS256 signature + `exp`, using **only the Go standard
  library** (no `golang-jwt`, so the 100% permissive dependency policy is untouched). Claims map
  `tenant_id|tid → TenantID`, `user_id|uid|sub → UserID`, `scope|scp|scopes → Scopes`. RS256/JWKS
  is a documented follow-on.

The net effect: **given a configured (non-`NoAuth`) Authenticator, isolation is by-construction
end-to-end** — a verified principal is bound to the path identity before any `Scope` is minted, and
the storage tiers isolate that now-verified `(tenant_id, user_id)` unbypassably. The one remaining
honest gap is that `Principal.Scopes` (the *verified* scopes) do not yet replace the
client-asserted `ChatRequest.Scopes` used for tool-gating — that switch is a tracked follow-on (see
`PrincipalFromContext` in `internal/api/authn.go`).

What **is** real and enforced today is the propagation invariant: identity is injected into the
`context.Context` exactly once at the handler boundary (`withIdentity`/`chatIdentity`,
`internal/api/chat.go`) and Go natively propagates it through the entire call chain — handlers,
memory hydration, [Gollem](../components/gollem-orchestrator.md), tools. No downstream component
parses request data on its own or re-resolves identity: everyone *reads* from the same `ctx`,
never reconstructs it.

```
path {tenant_id, user_id} + body {scopes}       (Authenticator middleware verifies
      │                                          the credential and MUST-AGREE binds
      ▼                                          the principal to the path identity
[Huma: contract validation + handler boundary]   before ctx is written; ADR-0024)
      │                                        → ctx{tenant_id, user_id}
[Authenticator: verify + must-agree bind]
                                         │
                    native Go propagation (no global state)
                                         │
        ┌────────────────┬──────────────┼──────────────┬────────────────┐
        ▼                ▼              ▼              ▼                ▼
    BadgerDB          rqlite         Qdrant          Gollem          Tools/primitives
  (tenant:…:user:    (WHERE          (payload        (surgical        (injection
   …:session:…)       tenant_id=?     filter in        context)         conditioned
                       AND user_id=?)  HNSW)                            by scopes)
```

**Why this is enough for real isolation:** `context.Context` in Go is immutable by design — every
`WithValue` creates a new node in the context tree instead of mutating a shared one, and each
goroutine of a request has its own branch. There's no way for a concurrent request from tenant B
to "see" tenant A's `ctx`, because there is no shared global variable where that could happen.
This is what the project calls
[isolation via ctx](../vision/principles.md#derived-operating-principles): the guarantee comes
from the language and the concurrency pattern, not from a coding discipline someone has to
remember to apply at every layer.

## Anti-leak composite key

The hard rule, no exceptions, at every stateful layer: **never share global agent-memory
instances across concurrent requests from different contexts.** A *context leak* — reusing a
buffer, a cache, or an agent session between two different tenants — is exactly the kind of bug a
"magic" framework with implicit state facilitates, and that Aisotropy makes structurally
difficult: every storage key (cache, session, buffer) carries the composite prefix

```
tenant:{tenant_id}:user:{user_id}:session:{session_id}
```

That is the literal output of `Key.prefix()` in `internal/memory/key.go` — three axes, each
validated by `ValidateIdentityComponent` against reserved characters before the key can reach the
key space.

> **Corrected 2026-08-04.** This block used to read `tenant_id:product_id:session_id`, which was
> wrong twice: it omitted `user_id`, which IS an axis, and it named `product_id`, which is not.
> A parenthetical below it then claimed "`product_id` applies when the same tenant operates
> multiple products/modules" — also false. `product_id` appears in **no** key and **no** filter in
> `internal/memory`, `internal/tenancy`, `internal/facts` or `internal/semantic`; it is a persona
> selector arriving in the request body (`aisotropy/turn.go`), and `internal/llm/otel.go` says
> outright that it "is not yet threaded through this seam". On the product axis aisotropy is L0.
>
> This was not a typo in one file. The same four-axis premise is what the arena programme scored
> itself on, and it took three independent ecosystem sweeps — adk-go, eve and dify, each arriving
> by a different route on 2026-08-04 — to surface it, because every document that could have
> caught it was repeating the same claim. See `docs/architecture/arena-sweep-{adk-go,eve,dify}-2026-08-04.md`.

The composite key serves a dual purpose: it
isolates *and* documents the scope of each piece of data in the identifier itself, without relying
on whoever reads the data to remember to apply the correct filter.

## Isolation per layer

Each tier of the [memory hierarchy](memory-tiers.md) implements isolation with the tool that's
native to it — there is no generic isolation layer on top; each store resolves the problem with
its own most efficient mechanism:

| Layer | Isolation mechanism | Detail |
|---|---|---|
| [Qdrant](../components/qdrant-semantic-memory.md) (semantic) | **Rigid payload filter during the HNSW scan** | The `tenant_id`/`user_id` filter is applied *during* the graph traversal, not as a post-filter over already-fetched k-nearest-neighbors. It is structurally impossible for a scan to cross into another tenant's data, and it's faster than filtering afterward. |
| [rqlite](../components/rqlite-facts-memory.md) (facts) | **Mandatory `WHERE tenant_id=? AND user_id=?`, enforced by construction** | Every FTS5 read or write query carries the explicit filter in the statement — never trusting an implicit filter from an intermediate layer, and never left to a coding discipline someone has to remember: [`internal/tenancy`](../decisions/0011-tenant-scoped-statement-builder.md) makes an unscoped statement impossible to construct (see [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)). Applies the same way under the single-filtered-DB strategy; under DB-per-tenant the filter becomes implicit via the connection string, see below. |
| [BadgerDB](../components/badgerdb-working-memory.md) (working) | **Composite keys `tenant:{t}:user:{u}:session:{s}`** | Isolation lives in the key itself, not in a query filter — BadgerDB has no query language, so the key *is* the scoping mechanism. Reserved characters (`:`, NUL, newline) are rejected in every component so a crafted id can never forge a cross-tenant prefix (`internal/memory/key.go`). It also leverages the LSM-tree's lexicographic ordering to iterate by prefix without scanning the rest of the keyspace. |

## What this document does NOT decide: the physical topology

There are two possible physical strategies to materialize the logical isolation described above:

1. **Single filtered DB** — a single rqlite cluster (or a single Qdrant collection) shared across
   all tenants, with the `tenant_id`/`user_id` filter applied on every query/search.
2. **DB-per-tenant** — a separate physical database (or collection) per tenant, with isolation
   given by the topology itself (connection string or dynamic collection) instead of an explicit
   `WHERE` in every statement.

This decision **is not made in this document** — it's the exact scope of **[ADR-0007](../decisions/index.md)**
("Multi-tenancy strategy: single filtered DB vs. DB-per-tenant"), which also directly intersects
with [ADR-0004](../decisions/index.md) (rqlite vs. LiteFS for facts memory): the choice of store
affects how cheap it is to spin up a per-tenant database. Qdrant already explicitly documents
that today it uses the shared-collection + payload-index model (see the
[Qdrant component](../components/qdrant-semantic-memory.md#key-configurationpatterns)), but
that the "tiered multitenancy" model for large tenants remains to be verified — the same decision
axis ADR-0007 must resolve for rqlite.

What this document does establish, independent of which topology wins: **logical** isolation —
what identity travels in the `ctx`, what key or filter each layer builds from that identity —
doesn't change based on the chosen physical topology. What changes is whether that filter is
applied within a shared `WHERE` or is implicit in which physical database it connects to.

## Why this wins in enterprise

- **No context leaks between concurrent requests**, the central promise of the framework's
  [thesis](../vision/thesis.md#the-problem).
- **End-to-end auditable**: the `ctx` is readable Go code, not an opaque security layer from an
  external framework — you can trace exactly where identity is written and where it's read.
- **Free observability**: since `tenant_id`/`user_id` travel in the `ctx`, every OTel span can be
  attributed to a tenant with no additional instrumentation — see [observability](observability.md).

*(Status note 2026-07-14: the ctx-injection point and the per-layer keys/filters above are
verified against the real implementation — `internal/api/chat.go`, `internal/memory/key.go`,
`internal/tenancy`, `internal/semantic`. The authenticated-identity seam that derives/verifies
identity from a credential and must-agree-binds it to the path — `internal/auth/{auth,jwt}.go`,
`internal/api/authn.go`, `aisotropy/auth.go`, wired in `aisotropy/app.go:404-417`
(ADR-0024) — **is now built and tested** (9 `internal/auth` JWT tests + 5 `internal/api` middleware
tests + the `aisotropy/isolation_e2e_test.go` regression anchor). With a configured, non-`NoAuth`
Authenticator, the isolation chain is verified end-to-end; with `NoAuth()` — or before any
authenticator is configured (default-deny rejects all `/v1` until one is) — the path param is
trusted verbatim and the end-to-end guarantee is not active.)*

## References

- [Architecture overview](overview.md) — request lifecycle, step 1 (ctx extraction).
- [Tiered memory](memory-tiers.md) — the memory triangle whose isolation this document describes.
- [Granular control](granular-control.md) — how the same `ctx` feeds security, cost, and behavior decisions.
- [Huma — Contracts and API layer](../components/huma-api.md) — who writes the ctx.
- [Qdrant — Semantic Memory](../components/qdrant-semantic-memory.md) · [rqlite — Facts Memory](../components/rqlite-facts-memory.md) · [BadgerDB — Working Memory](../components/badgerdb-working-memory.md)
- [ADR-0007](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md) — physical multi-tenancy strategy (recommended: hybrid/tiered). [ADR-0004](../decisions/0004-facts-memory-rqlite-vs-litefs.md) — rqlite vs. LiteFS (accepted, intersects with 0007). [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — the tenant-scoped statement builder (`internal/tenancy`) that enforces the rqlite row above by construction, ahead of any Phase 3 facts-tier code. [ADR-0024](../decisions/0024-authenticated-identity-seam.md) — the authenticated-identity seam (default-deny Authenticator + must-agree bind) that verifies the identity *before* the ctx is written, making isolation by-construction end-to-end given a configured authenticator.
- [Thesis](../vision/thesis.md) — multi-tenancy with real isolation as one of the four enterprise requirements.
