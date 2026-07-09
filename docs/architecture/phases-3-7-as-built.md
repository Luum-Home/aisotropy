---
type: Architecture Reference
title: "Phases 3–7 as-built — constitutional-gate → test coverage matrix"
description: The verification companion to runtime-as-built.md — which constitutional gate is enforced by which test, the reproduce commands, and an honest debt ledger of what is implemented-but-unverified or shipped-without-review.
tags: [architecture, as-built, verification, constitutional-gates, definition-of-done, phases, coverage]
timestamp: 2026-07-09T00:00:00Z
---

# Phases 3–7 as-built — gate → test coverage matrix

> **Companion to** [`runtime-as-built.md`](runtime-as-built.md), which narrates *how* the system is
> wired. This doc answers the different question: **what proves it?** — mapping each constitutional
> gate (`.claude/rules/constitutional-gates.md`) to the test that enforces it, plus an honest ledger
> of what is built but not yet verified.
> **As of:** 2026-07-09. Reproduce everything with `go test ./...` (15/15 packages green at write time).

## Phase implementation status

| Phase | Capability | Package(s) | Live surface | Status |
|---|---|---|---|---|
| 3 | Facts memory (SQLite+FTS5 / rqlite, Engram scoring, supersede) | `internal/facts` | hydration + consolidation | Implemented |
| 4 | Semantic memory (in-memory / Qdrant gRPC, UUIDv5 idempotency, reranking) | `internal/semantic` | hydration + consolidation | Implemented |
| 5 | Transactional Outbox (BadgerDB, at-least-once drain) + consolidation sink | `internal/memory`, `internal/consolidation` | async worker + cold-boot recovery | Implemented |
| 6 | Sleep cycle (DataFusion Rust subprocess) + friction-driven primitive rewriting | `internal/sleepcycle`, `crates/sleep-cycle`, `internal/primitives` | periodic offline worker | Implemented (Rust deps unreviewed — see debt) |
| 7 | Quota (token-bucket), circuit breaker, scope-gated tools, per-tenant cost, agentic endpoint | `internal/memory` (quota), `internal/llm` (breaker/degrade), `internal/tools`, `internal/costmeter`, `internal/agentloop` | `/chat/agent`, `GET /costs`, quota pre-checks | Implemented |

Endpoints now live: `POST …/chat`, `POST …/chat/stream`, `POST …/chat/agent`, `GET …/costs`.

## Constitutional gate → enforcing test

| Gate (constitutional-gates.md) | Enforcing test | Location | Verdict |
|---|---|---|---|
| **Quota checked before the LLM call; a tenant cannot exceed its bucket under concurrency** | `TestLimiterConcurrentCannotExceedBurst` | `internal/memory/quota_test.go:23` | ✅ the exact test the gate mandates |
| **Tenant isolation — no cross-tenant reads (facts, hand-written FTS5 SQL)** | `TestEverySQLStatementIsTenantScoped` | `internal/facts/scope_isolation_test.go:15` | ✅ build fails if any statement drops the tenant/user predicate |
| **Tenant isolation — statement builder, by construction** | `TestSameTenantDifferentUsersIsolated`, `TestConcurrentTenantsDoNotRace` | `internal/tenancy/isolation_test.go:164,190` | ✅ |
| **Tenant isolation — semantic vectors filtered during the scan (not post-filter)** | `TestTenantAndUserIsolation` | `internal/semantic/semantic_test.go:102` | ✅ |
| **Tenant isolation — streaming path** | `TestChatStream_TenantIsolation` | `internal/api/chat_stream_test.go:238` | ✅ |
| **Structured output fails closed on persistent schema violation (reflection loop, no silent fallback)** | `TestChat_FailsClosedOnPersistentSchemaViolation` | `internal/api/chat_test.go:147` | ✅ returns HTTP 502, bounded retries |
| **Identity validated once, from ctx, not body/header** | `TestChat_InvalidIdentityRejected`, `TestNewScopeValidatesThroughTenancy` | `internal/api/chat_test.go:123`, `internal/facts/scope_isolation_test.go` | ✅ |
| **Provider failure is contained (circuit breaker fails fast)** | breaker suite | `internal/llm/breaker_test.go` | ✅ |
| **Governance by absence — scope-forbidden tools never reach the prompt** | `TestPermittedGatesByScope`, `TestPromptFragmentOmitsForbiddenTools` | `internal/tools/tools_test.go:16,45` | ✅ |
| **Per-tenant cost accounting is attributable and concurrency-safe** | `TestRecordAndStats`, `TestRecordConcurrent` | `internal/costmeter/costmeter_test.go:11,128` | ✅ |
| **Real token usage flows (not SSE delta counts); cost is non-zero end-to-end** | `TestWithOTel_GenAIUsageIsRealNotDeltaCount`, `TestGollemModelStubProducesRealUsageAndCost` | `internal/llm/otel_test.go`, `internal/llm/gollem_test.go` | ✅ (dollar cost still 0 in prod — pricing table unset, deliberate) |
| **Summary buffer stays bounded (token-economy hygiene)** | `TestNoopSummarizerStaysBoundedAcrossRounds`, `TestNoopSummarizerSummaryStaysBounded` | `internal/memory/summarizer_test.go`, `bench/token-economy/bench_test.go` | ✅ drove measured reduction 54%→72% |

## Debt ledger — implemented but NOT yet verified / reviewed

Honest gaps, in rough priority order. None block the green build; all are follow-ups a reviewer must not mistake for done.

1. **SSE ctx-cancellation gate has no dedicated test.** The gate ("a cancelled client request must stop the upstream LLM call, not just stop writing") is respected in code (`stub.go`/stream path honor `ctx.Done()`), but `chat_stream_test.go` no longer contains a test that asserts a mid-stream cancel halts the upstream call. **Add one.**
2. **274 transitive Rust crates are license-unreviewed.** `crates/sleep-cycle` pulls the Arrow/DataFusion/tokio tree (`Cargo.lock`, 274 packages). The direct deps are Apache-2.0/MIT and the ecosystem is almost certainly clean, but the constitutional license gate requires an *explicit* review, which has not run. **Run a `cargo-deny`/license sweep and record it.**
3. **`facts.Scope` / `semantic.Scope` fields are still exported.** Identity now validates through `internal/tenancy` (ADR-0011 reconciliation), but a bare `facts.Scope{...}` literal still compiles — the constructor is a mandate backed by `TestEverySQLStatementIsTenantScoped`, not a compile-time barrier. Unexport-hardening (mirroring `tenancy.Scope`) is deferred; `semantic.Scope` warrants the identical reconciliation. See ADR-0011 §"Reconciliation with the facts tier".
4. **Adversarial-gate depth.** Phase 7 shipped an `ec0fb42 fix: apply adversarial-review findings` pass, but the DoD for Critical-complexity work (auth/quota/cost) asks for a deeper adversarial + security review than a single sweep. **Re-run against the quota/cost/tenant paths specifically.**
5. **Placeholders standing in for real models.** `semantic.NewHashEmbedder` (deterministic hash, not a real embedding model) and the empty `gollemModelPricing` table are documented drop-in seams, not production components. **Wire real ones before any cost/quality claim.**
6. **Networked backends (rqlite, Qdrant) are integration-tested only manually.** `rqlite_manual_test.go` / `qdrant_manual_test.go` require live services and are not in the default `go test` run. **Stand up a CI integration lane.**

## Reproduce

```bash
go build ./...                 # compiles (Go 1.26); Rust worker: cargo build --release --manifest-path crates/sleep-cycle/Cargo.toml
go vet ./...                   # clean
gofmt -l cmd internal bench    # empty
go test -race ./...            # 15/15 packages green
# spot-check a specific gate:
go test -race ./internal/memory/ -run TestLimiterConcurrentCannotExceedBurst -v
```

## Cross-links

- [runtime-as-built.md](runtime-as-built.md) — the as-wired narrative this matrix verifies.
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) — the facts↔tenancy reconciliation (debt item 3).
- [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md) — the Go/Rust sleep-cycle boundary (debt item 2).
- [constitutional gates](../../.claude/rules/constitutional-gates.md) — the source of the gate column.
- [token-economy benchmark](../../bench/token-economy/) — the measured 54%→72% reduction (last matrix row).
