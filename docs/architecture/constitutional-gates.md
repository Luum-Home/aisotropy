---
type: Process
title: "Constitutional gates — the checks a change to isolation, quota or the LLM path must clear"
description: >-
  The non-negotiable checks that run before code touching tenant isolation, the memory tiers or the
  model call path can merge. Multi-tenant data isolation (tenant_id travels only via context.Context,
  every store read scoped, Qdrant filtered during the HNSW scan and never after), quota state
  consistency (reject or degrade BEFORE spending tokens), the permissive-only licence policy,
  fail-closed structured output, and the test-before-merge baseline. This is the tracked canonical;
  .claude/rules/constitutional-gates.md is a harness projection of it and is gitignored, so this file
  is what a clone gets.
tags: [architecture, process, gates, multi-tenancy, isolation, quota, licence, contributing]
timestamp: 2026-08-05T00:00:00Z
---

# Constitutional gates

Aisotropy is a multi-tenant LLM runtime. The checks below are not style preferences and not review
suggestions: they are the properties the project's central claim rests on, and a change that breaks
one of them breaks the reason the project exists. They apply to anything touching tenant isolation,
the memory tiers, or the model call path.

> **Why this file exists, and why it is here rather than only in `.claude/rules/`.** The agent-facing
> projection at `.claude/rules/constitutional-gates.md` is gitignored — `.claude/` holds
> FSL-1.1-MIT dev tooling that is deliberately kept out of an Apache-2.0 repository's history — so a
> clone never receives it. That left the guards in `scripts/` public and tracked while the standard
> they enforce was machine-local: a contributor could be blocked by a gate with no way to read what
> it wants. Established 2026-08-05.

## 1. Multi-tenant data isolation (hard gate)

- **`tenant_id` travels exclusively via `context.Context`**, injected once at the Huma handler
  boundary from the validated path parameter. Never accept it from a body, header or query as the
  source of truth for authorization. A tenant id supplied by the model — as a tool argument, for
  instance — is the same defect wearing different clothes.
- **Every new read or write to a memory tier scopes by tenant** (and by user where the table is
  user-scoped). For the semantic tier this means **payload filtering during the HNSW scan, not
  after**: a post-filter is a leak that returns the right answer, not a fix.
- **No code path holds two tenants' data in the same buffer or goroutine scope** without an explicit,
  reviewed reason.

The enforcement is structural rather than procedural, which is the point: `internal/tenancy` has no
exported constructor for a scoped statement builder, its fields are unexported, and the tenant column
can be neither passed to `Where` nor made a `SET` target. An unscoped statement is a compile error,
not a review finding. There are **zero** raw SQL strings outside that package in production code, and
that is checkable in one command:

```bash
grep -rn --include='*.go' -E '"(SELECT|INSERT|UPDATE|DELETE)[ ]' internal aisotropy cmd \
  | grep -v _test.go | grep -v '^internal/tenancy/'
```

## 2. Quota and cost state

- **Quota is checked before any LLM call is made.** Reject with 429 or degrade to a cheaper model
  *before* spending tokens, never after. A gate that runs after the spend is a metric, not a gate.
- **Any change to quota or circuit-breaker logic ships with a test** proving a tenant cannot exceed
  its bucket under concurrent requests.
- **Know which numbers are control inputs and which are observability.** The per-tenant cost meter
  feeds `gateway.CostAwarePolicy`, which decides when to shape a tenant onto a cheaper model — so it
  is not merely a dashboard, and its durability and replica-scope are correctness concerns. See the
  [threat model](threat-model.md)'s gap ledger.

## 3. Licence policy (repo-wide, hard gate)

The stack is deliberately **100% permissive — MIT / BSD / Apache-2.0**. Do not introduce a
dependency under a copyleft (GPL/AGPL) or source-available (BUSL/SSPL/ELv2/FSL/Commons Clause)
licence without an explicit ADR arguing the exception. The gate is executable, covers transitive
resolution and the docker-compose image allowlist, and refuses to pass when it cannot evaluate a
dependency rather than assuming the best:

```bash
bash scripts/license-audit.sh --root-only
```

## 4. Structured output and streaming

- LLM calls expecting structured output **fail closed**: a schema violation triggers the bounded
  reflection loop with the exact typing error, never a silent fallback to unstructured text.
- **SSE handlers respect context cancellation immediately.** A cancelled client request must stop the
  upstream model call, not merely stop writing to the response — otherwise cancellation costs the
  same as completion.

## 5. Test before merge

- Nothing touching `internal/` or `cmd/` merges without `go test ./...` passing.
- **Secrets only via environment variables** — never committed, never logged. Check `slog` call sites
  for accidental key material.
- **The Go struct IS the OpenAPI contract** (Huma). Changing a field's JSON tag or removing a field
  is a breaking change and needs an explicit versioning discussion, not a silent patch release.

## What "gate" means here

A gate that cannot evaluate its check reports that it could not, and that is **never** read as a
pass. This convention is repo-wide and was learned the expensive way: `scripts/prune-guard.sh` once
returned a clean bill of health for a package inside a nested Go module it could not analyse, which
would have authorised deleting live code with more authority than a stale document. The same rule now
governs every guard in `scripts/`.

## References

- [Multi-tenancy](multi-tenancy.md) — the isolation design these gates protect.
- [Threat model](threat-model.md) — the attacks they answer, and the honest gap ledger of what they
  do not yet cover.
- [Prune-evidence gate](prune-evidence-gate.md) · [Capability-absence claims](capability-absence-claims.md)
  — the sibling evidence disciplines, both with tracked canonicals like this one.
- [`CONTRIBUTING.md`](../../CONTRIBUTING.md) — how to run the gates locally before pushing.
