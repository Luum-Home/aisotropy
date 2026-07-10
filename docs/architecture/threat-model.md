---
type: Concept
title: Threat model — the multi-tenant isolation boundary
description: The security threat model for Aisotropy's #1 differentiator, tenant/user isolation. Enumerates the trust boundary, the assets, the concrete threats (cross-tenant leak, prompt-injection tool bypass, identity forgery, cost exhaustion, consolidation poisoning, secret leakage, supply chain), the mitigation that is REAL vs aspirational today, and the tenant-leak testing strategy. Honest about the load-bearing gap: identity is not yet authenticated.
tags: [architecture, security, threat-model, multi-tenancy, isolation, prompt-injection, scopes]
timestamp: 2026-07-10T00:00:00Z
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

## The load-bearing caveat, stated first

**Identity is not authenticated yet.** `tenant_id` and `user_id` are **path params**
(charset-constrained, Huma-validated), and `scopes` travel in the **request body** as an explicit
"JWT-derived permissions stand-in" ([runtime as built](runtime-as-built.md) §1). There is **no JWT
verification, no auth middleware** in the runtime today. So every isolation guarantee below is of
the form *"correct **given** a trusted (tenant, user, scopes) triple."* The mechanism that would make
that triple trustworthy — authenticating the caller and deriving scopes from a verified token — is
**the single most important unbuilt security control**, and it gates the credibility of the entire
moat for any real deployment. Everything else in this model assumes a trusted identity edge in front
of the runtime (a gateway/authenticated proxy) until that control exists in-process.

## Trust boundary & assets

```
  UNTRUSTED                    │  TRUSTED (assumes an authenticated identity edge — NOT yet built)
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
| **T2** | **Prompt-injection → tool bypass / privilege escalation** (model coerced into calling a tool the caller's scopes don't grant) | adversarial prompt / injected content in retrieved context | **Governance by absence**: a non-permitted tool is never named in the prompt, AND the chosen tool name is re-validated against `Registry.Permitted(scopes)` before execution — structural, so a hallucinated/injected name **cannot** execute. Both the homegrown `agentloop` and the native Gollem path enforce it (ADR-0012). | **REAL** — `agentloop` + native path, fail-on-call executor tests |
| **T3** | **Identity / scope forgery** (caller claims another tenant, or grants itself `write:*`) | scopes in the request body; unauthenticated path params | ⚠️ **Not mitigated in-process.** Any caller can hit `/v1/tenant/X/user/Y` with arbitrary `scopes`. Depends entirely on an upstream authenticated gateway today. | **ASPIRATIONAL** — the #1 gap; needs JWT/auth middleware that derives `(tenant,user,scopes)` from a verified token |
| **T4** | **Cost / quota exhaustion** (one tenant burns the LLM budget / DoS via token spend) | flooding the chat routes | Per-tenant token-bucket rate limit **before any LLM call** (reject/degrade, not after); circuit breaker fails fast on provider failure; quota-pressure model degradation to a cheaper model at ≥95%. | **REAL** on `/chat` + `/chat/stream`; ⚠️ **gap**: `/chat/agent` is NOT rate-limited, and the buckets + cost meter are **per-instance** (no cross-replica quota) |
| **T5** | **Consolidation / sleep-cycle cross-tenant surface** | the batch worker's `ExportAll` reads across tenants | The sleep-cycle `ExportAll` is a **deliberate, reviewed cross-tenant read**; dedup results are `Delete`d back **within each `(tenant,user)` scope**. It is the one place tenant data co-resides in one process buffer — flagged, not accidental. | **REAL but a reviewed exception** — dormant by default (needs `AISOTROPY_SLEEPCYCLE_BIN`) |
| **T6** | **Secret leakage** (API keys in logs/traces/commits) | `slog` call sites, OTel spans, commits | Secrets via env only; the content-policy + secret-scanning gates block committing them; OTel captures `gen_ai.usage.*` + identity, not payload secrets. | **REAL** — constitutional gate + hooks |
| **T7** | **Supply-chain compromise** | a malicious/typo-squatted dependency | Small surface: Go module graph + one vendored Rust crate; 100% MIT/Apache (a copyleft/BUSL dep needs an ADR); digest pinning discipline. | **REAL** (structural) — far smaller attack surface than an npm/pip tree |
| **T8** | **PII in logs / model context** | user PII flowing into audit logs or the model | Gollem `RedactPII` interceptor (email pattern) on the reflection path; audit log records message *shape*, not content. | **PARTIAL** — reflection path only; conservative pattern (email); streaming path not covered |

## The honest gap ledger (do not oversell the moat)

1. **No authentication (T3).** Until a verified-token → `(tenant,user,scopes)` control exists
   in-process, the isolation is "correct given a trusted edge," and MUST be deployed behind an
   authenticated gateway. This is the top security priority.
2. **`/chat/agent` is unmetered and un-rate-limited** — a cost/DoS hole (T4).
3. **Rate limit + cost meter are per-instance** — a multi-replica deployment has no shared quota;
   a tenant could exceed its bucket by fanning out across replicas (T4).
4. **PII redaction is reflection-path-only and email-only** (T8).
5. **The sleep-cycle `ExportAll` is the one sanctioned cross-tenant read** (T5) — correct today, but
   any future change to it is a high-blast-radius isolation risk and must stay reviewed.

## Testing strategy (how the moat is proven, not asserted)

- **Per-tier scope-isolation tests** (`internal/facts/scope_isolation_test.go` and peers): write
  under `(tenantA,userA)`, assert a read under `(tenantB,*)` or `(tenantA,userB)` returns nothing.
- **Compile-time isolation**: `internal/tenancy` makes an unscoped facts statement a build error
  (ADR-0011) — the strongest possible test (it can't compile wrong).
- **Tool scope-gate tests**: a `mustNotRun` executor asserts a non-permitted / hallucinated tool
  name never reaches execution on both dispatch paths.
- **What is NOT yet tested**: an end-to-end tenant-leak fuzz across the *full* request path with a
  real authenticated identity (blocked on T3), and cross-replica quota isolation.

## References

- [multi-tenancy](multi-tenancy.md) — the isolation design this model attacks.
- [reality ledger](reality-ledger.md) — REAL vs dormant vs aspirational status.
- [ADR-0011 — tenant-scoped statement builder](../decisions/0011-tenant-scoped-statement-builder.md) · [ADR-0007 — physical tenant topology](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md) · [ADR-0012 — native tool-calling scope-gate](../decisions/0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)
- [constitutional gates](../../.claude/rules/constitutional-gates.md) — merge-time enforcement of T1/T4/T6.
- [granular control](granular-control.md) — the security × cost × behavior knobs.
