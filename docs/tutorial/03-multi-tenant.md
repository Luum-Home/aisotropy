---
type: Tutorial
title: "03 — Multi-tenant isolation"
description: The core idea — isolation by construction. Identity travels via ctx, every scope is runtime-derived, and the cross-tenant read is unrepresentable.
tags: [tutorial, multi-tenant, isolation, ctx]
timestamp: 2026-07-16T00:00:00Z
---

# 03 — Multi-tenant isolation

**Goal:** see why one tenant *cannot* read another's data — and why that holds without you writing a
single guard.

This is the load-bearing lesson. The same `App` serves every tenant, concurrently, from one runtime.
What keeps them apart is not a check you remember to write — it's the shape of the API.

```go
app := aisotropy.New()
defer func() { _ = app.Close() }()

// Same App, same runtime, two tenants. Safe for concurrent use across tenants.
acme, _ := app.Turn(ctx, aisotropy.ChatInput{TenantID: "acme",  UserID: "alice", SessionID: "s1", Prompt: "..."})
globex, _ := app.Turn(ctx, aisotropy.ChatInput{TenantID: "globex", UserID: "bob", SessionID: "s1", Prompt: "..."})

fmt.Println(acme.TenantID, globex.TenantID) // "acme" "globex" — each echoes the tenant that served it
```

Here is the mechanism, in three facts:

1. **Identity enters ctx once.** `TenantID` / `UserID` are read from the request and injected into
   `context.Context` at the boundary. They travel *only* there — never as a body field an untrusted
   caller could forge downstream.
2. **Every scope is runtime-derived.** Each memory tier builds its `(tenant, user)` scope from that
   ctx. Consumer code never passes a scope and never chooses one.
3. **The unscoped read is unrepresentable.** The public API gives you `TenantFromContext(ctx)` to
   **read** identity — and *no* constructor for a scope, no cross-tenant export, no way to widen a
   read. A tool that wanted to peek at another tenant has no function to call. Isolation isn't
   enforced; it's *structural*.

That's why `Turn` is documented safe for concurrent use across tenants: two goroutines serving two
tenants derive two disjoint scopes from two ctxs, and there is no shared buffer that could mix them.

**Runnable example:** [`multi-tenant`](../../examples/multi-tenant) — two tenants writing memory
through the same `App`, with a read from one proving it can't see the other's data.

**Depth:** [multi-tenancy](../architecture/multi-tenancy.md),
[ADR-0011 — tenant-scoped statement builder](../decisions/0011-tenant-scoped-statement-builder.md)
(the "every scope is built from ctx" invariant), and
[ADR-0007 — physical multi-tenancy](../decisions/0007-multitenancy-fisico-db-unica-vs-por-tenant.md)
(pooled vs. dedicated stores).

Next: [04 — Auth & scopes](04-auth-scopes.md).
