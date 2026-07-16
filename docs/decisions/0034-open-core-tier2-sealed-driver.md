---
type: ADR
title: "ADR-0034 — The Tier-2 sealed-driver: how the isolation-critical memory backends are opened without ever handing out the read-authority"
description: ADR-0033 set the RULE (open policy seams raw, seal isolation-critical seams) and named the sealed-driver as the way Tier-2 is opened. THIS ADR specifies the sealed-driver mechanism itself, because it is the one place a "bring your own store" feature usually leaks multi-tenant isolation. The design has three parts, all shipped. (1) A tenant-UNAWARE driver — semantic.VectorBackend (upsert/search/delete/scan over an OPAQUE namespace) and facts.DocBackend (put/get/search/delete/scan/export-all) — that never sees a tenant_id, only opaque namespace strings and opaque ids/payload it must treat as black boxes. (2) A runtime-owned SEALED scoping decorator — semantic.NewVectorBackendStore / facts.NewDocBackendStore — whose namespace derivation (namespaceFor, an UNEXPORTED deterministic SHA-256 over the ctx-scoped tenant\x00user) a plugin can neither compute nor forge, and which reattaches identity from the Scope on read, never from the backend payload; so a buggy or hostile backend cannot widen a scan past its own namespace — the worst it can do is fail its own tenant's round-trip. (3) The internal/plugintest conformance + isolation-proof suite (RunVectorBackendSuite / RunDocBackendSuite + RefVectorBackend / RefDocBackend reference impls) that a candidate driver must pass, so "bring your own backend" is verifiable, not trust-me — the central proof writes a marker for tenant A through the decorator and asserts tenant B sees nothing while the marker is physically present in the raw backend under A's namespace. Honest deltas: the facts driver shipped as DocBackend (the spec-time placeholder in ADR-0033 was KVBackend); the driver payload types (BackendVector/BackendHit, BackendDoc/BackendDocHit) are exported from the internal packages but not yet re-exported on the public façade, so a fully EXTERNAL module cannot implement a driver against them today (implementable inside this module and its tests); physical dedicated-tenant tier routing is deliberately NOT layered over a BYO backend (the backend owns persistence; the sealed decorator owns isolation).
tags: [decisions, adr, open-core, sealed-driver, tier-2, isolation, bring-your-own-backend, semantic, facts, conformance, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0034 — The Tier-2 sealed-driver: how the isolation-critical memory backends are opened without ever handing out the read-authority

## Status

✅ **Accepted — 2026-07-16.** Specifies and ratifies the **sealed-driver** mechanism that
[ADR-0033](0033-open-core-extension-seams.md) chose for its **Tier-2** (isolation-critical memory
backends) but described only at the level of the tiering *rule*. ADR-0033 decides *which* seams get
the sealed-driver treatment; this ADR is the design record for *how* the sealed driver is built and
why it cannot leak — the concrete driver ABI, the unexported namespace seal, and the conformance
harness that makes "bring your own backend" verifiable. All three parts are **shipped and wired**:
the drivers (`semantic.VectorBackend` / `facts.DocBackend`), the sealed decorators
(`semantic.NewVectorBackendStore` / `facts.NewDocBackendStore`), the `internal/plugintest`
conformance + isolation-proof suite, and the public install options
(`WithVectorBackend` / `WithFactsBackend`) threaded through `startRuntime` and the shared
`ensureSemantic` build path.

This ADR **extends** [ADR-0033](0033-open-core-extension-seams.md) (it is Tier-2's design detail),
sits under [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md)'s sealed-kernel /
composition split, and preserves [ADR-0011](0011-tenant-scoped-statement-builder.md)'s
isolation-by-construction invariant (every `Scope` is built by the runtime from `ctx`). It adds **no
new dependency** and does **not** change the HTTP contract; a runtime that installs neither option
boots byte-for-byte unchanged (the built-in in-memory / SQLite / cosine stores remain the default).

## Context

The single most common way a multi-tenant AI framework leaks isolation is its "pluggable store"
feature. The typical shape is a public `Store` interface whose methods take a raw query or predicate
and return raw rows — so the *plugin* decides what is read. The moment that interface is public, the
isolation guarantee degrades to "as good as every backend anyone ever plugged in": a buggy or hostile
backend can read across tenants, and the framework cannot stop it because it handed the plugin the
read-authority.

ADR-0033 named this failure mode and drew the line: a **policy seam** (no authority over which
tenant's data is touched) may be opened as a raw interface; an **isolation-critical seam** (a backend
the runtime reads/writes tenant data through) may **not**. The facts store and the semantic store are
squarely isolation-critical, yet a real product needs them pluggable — a consumer wants to bring
Qdrant, Weaviate, pgvector, Elasticsearch, or their own house engine. ADR-0033 committed to opening
them "later, only as tenant-unaware drivers behind a sealed, runtime-owned scoping decorator." This
ADR is that "later" made concrete, and records the mechanism precisely enough that a reviewer can
verify the leak is structurally impossible rather than merely discouraged.

The governing discriminator is unchanged from ADR-0033: **who holds the authority to decide which
tenant's data is touched.** The design goal is to give the plugin *storage* authority (it decides how
bytes are persisted and indexed) while the runtime retains *identity* authority (it, and only it,
decides which tenant a call belongs to). The seam must make it impossible for the two to be confused.

## Goals

- A **narrow, tenant-UNAWARE driver interface** the community implements: it operates on an **opaque
  namespace string** plus opaque ids/payload it must treat as black boxes, and it has **no concept**
  of tenant, user, or `Scope`. A backend author writes storage, never isolation.
- A **runtime-owned SEALED scoping decorator** that turns any conforming driver into a real
  `semantic.Store` / `facts.Store` satisfying the constitutional isolation gate — deriving the
  namespace from the ctx `Scope` on every call and reattaching identity from the `Scope` (never from
  the backend's payload) on every read.
- A **seal a plugin cannot reach around:** the namespace derivation is **unexported**, so no code
  outside the store's own package — least of all a community backend — can compute or forge the
  namespace for a tenant it was not handed a `Scope` for.
- A **conformance + isolation-proof suite** a candidate driver must pass, so "bring your own backend"
  is a **verifiable** claim (round-trip, CRUD, search semantics, and the central cross-tenant seal
  proof), not trust-me.
- **Additive, default-safe wiring** (`WithVectorBackend` / `WithFactsBackend`): unset ⇒ the built-in
  stores, byte-for-byte; set ⇒ the sealed driver replaces the default store at the one correct build
  point for *both* the HTTP RAG surface and the SDK ingestion surface.

## Non-goals

- **NOT** opening the stores as **raw** `Store` interfaces — ever. `extend.go` states the refusal
  explicitly ("Do not move a `facts.Store` / `semantic.Store` into this file to 'make it
  pluggable'"). Only the tenant-unaware driver is exposed; the `Scope`, its constructors, working
  memory, and the tool `Registry` remain fully sealed with no consumer seam.
- **NOT** letting the driver see identity on any scoped path. The semantic driver never receives
  tenant/user at all; the facts driver persists tenant/user in its opaque `Fields` map **only** so the
  single audited cross-tenant `ExportAll` (the sleep cycle) can reconstruct identity with no `Scope`
  in hand — every *scoped* facts read takes identity from the `Scope`, never those fields.
- **NOT** claiming full **external-module** implementability today. The driver interfaces
  (`VectorBackend` / `DocBackend`) are re-exported on the façade (`WithVectorBackend` /
  `WithFactsBackend` + the `VectorBackend` / `DocBackend` aliases), but their **payload types**
  (`BackendVector` / `BackendHit`, `BackendDoc` / `BackendDocHit`) live in the `internal/` packages
  and are **not re-exported** yet, so a driver cannot be authored from outside this module until they
  are. Inside this module and its tests, both drivers are fully implementable (the `plugintest`
  reference backends prove it). This is the same "aliased ≠ externally-implementable" honesty gap
  ADR-0033 flags for Tier-1, applied to the Tier-2 payloads.
- **NOT** layering physical dedicated-tenant tier routing (ADR-0007) over a BYO backend. When a
  backend is installed alongside `AISOTROPY_DEDICATED_TENANTS`, the runtime logs that tier routing is
  not applied over the sealed driver — the decorator already provides per-tenant isolation by
  namespace, and the community backend owns its own persistence topology.

## Options considered

### The driver interface shape

- **A raw `Store` interface (methods take arbitrary predicates / return arbitrary rows).** Rejected —
  this *is* the leak ADR-0033 exists to prevent: the plugin decides what to read, so isolation depends
  on every plugin getting scoping right.
- **A tenant-aware driver that receives the `Scope` and is trusted to filter by it.** Rejected —
  handing the plugin the `Scope` hands it the read-authority; a wrong filter is a cross-tenant read.
  It also forces the plugin to re-implement isolation, the exact duplication ADR-0011 removed
  internally.
- **A tenant-UNAWARE driver over an opaque namespace (CHOSEN).** The driver receives only an opaque
  `ns` string it must confine every operation to. It cannot filter by tenant because it never learns
  the tenant; it cannot forge a namespace because it cannot compute one. Storage authority to the
  plugin, identity authority to the runtime.

### Where isolation is enforced

- **In the driver (documented as the plugin's responsibility).** Rejected — "documented
  responsibility" is precisely the guarantee that degrades to the weakest plugin.
- **In a runtime-owned decorator the plugin cannot bypass (CHOSEN).** `NewVectorBackendStore` /
  `NewDocBackendStore` are the **only** blessed way to turn a driver into a `Store`. The consumer
  holds the result as a `Store` and never touches the namespace derivation; the plugin holds only its
  own driver handle and never sees the decorator. The read-authority never leaves the runtime.

### How the namespace is derived

- **A readable, plugin-supplied, or reversible namespace.** Rejected — anything a plugin can compute
  or reverse lets it address another tenant's bucket.
- **An UNEXPORTED, deterministic, non-reversible derivation (CHOSEN).** `namespaceFor(sc)` is a
  package-private SHA-256 over `tenant + "\x00" + user` with a versioned prefix
  (`aisotropy-sem-v1-` / `aisotropy-facts-v1-`). Deterministic so a persistent backend keeps
  addressing the same namespace across restarts; `\x00`-separated so `("a","bc")` and `("ab","c")`
  never collide into one bucket; unexported so no plugin can produce it.

## Decision

### 1. The tenant-unaware drivers (opaque namespace, opaque payload)

**`semantic.VectorBackend`** ([`internal/semantic/backend.go`](../../internal/semantic/backend.go)) —
`Upsert(ctx, ns, []BackendVector)`, `Search(ctx, ns, query, k) []BackendHit`, `Delete(ctx, ns, ids)`,
`Scan(ctx, ns) []BackendVector`, `Close()`. A `BackendVector` is `{ID, Vector, Payload
map[string]string}`; the backend stores all three verbatim and interprets none. **Identity is not in
the payload** — tenant/user live only in the namespace and are reattached from the `Scope` on read.

**`facts.DocBackend`** ([`internal/facts/backend.go`](../../internal/facts/backend.go)) —
`Put(ctx, ns, key, BackendDoc)`, `Get(ctx, ns, key)`, `Search(ctx, ns, query, k) []BackendDocHit`,
`Delete(ctx, ns, keys)`, `Scan(ctx, ns) []BackendDoc`, `ExportAll(ctx) []BackendDoc`, `Close()`. A
`BackendDoc` is `{Key, Content, Fields map[string]string}`; `Content` is the keyword-searchable text,
`Fields` an opaque blob. The one deliberate difference from the vector driver: `Fields` carries
tenant/user **for the `ExportAll` cross-tenant path only**, so the sleep cycle can reconstruct
identity with no `Scope`.

Both driver doc comments state the black-box contract in the strongest terms: a backend **MUST NOT**
parse a namespace, derive access control from a payload key, or share storage across namespaces.
Only items written under the **same** `ns` may be returned from `Search`; returning an item from
another namespace is "an isolation breach the decorator cannot repair" — but one the decorator makes
unreachable, because the plugin can never be handed another tenant's namespace.

### 2. The sealed scoping decorators (the seal)

`NewVectorBackendStore(backend, dim)` and `NewDocBackendStore(backend)` wrap a driver into a real
`Store`. Every scoped method:

1. **Validates the write identity** against the `Scope` (`ErrScopeMismatch` on a cross-tenant write,
   `ErrDimensionMismatch` on a wrong-width vector — matching the built-in stores exactly).
2. **Derives the namespace** with the unexported `namespaceFor(sc)` and confines the driver call to
   it. The `(tenant,user)` filter *is* the namespace, applied during the driver scan — never a
   post-filter (a post-filter is the isolation leak ADR-0007/ADR-0033 forbid).
3. **Reattaches identity from the `Scope`** on every returned item (`pointFromBackend` /
   `factFromDoc`), **never** from the backend payload. This is the read half of the seal: even a
   backend that returned another tenant's item would have it relabeled with the caller's scope — but
   it cannot, because the item came from the caller's own namespace.

Behavioral parity with the built-in stores is preserved: the `Kind` post-filter over an over-fetched
candidate pool (`backendKindPoolFactor = 8`, because `Kind` is a payload attribute, not part of the
isolation namespace); the `Relevance × Importance × Recency` re-rank and superseded-row exclusion for
facts (`docCandidatePoolFactor = 4`); `PruneTurns` age/count eviction confined to the scope's
namespace; and the documented tradeoff that `Supersede` on a KV driver is two sequential writes (no
cross-key transaction, unlike the SQLite store's real `tx`).

The single audited cross-tenant path is `facts.DocBackend.ExportAll` (the sleep cycle's export),
which reconstructs identity from each document's stored fields (`factFromExportedDoc`), excludes
superseded rows, and mirrors the SQLite backend's `exportAllSQL` — exactly the same single reviewed
breach of per-call scoping the built-in store already has, not a new one.

### 3. The conformance + isolation-proof suite

[`internal/plugintest`](../../internal/plugintest) makes "bring your own backend" verifiable:

- **`RunVectorBackendSuite(t, factory)`** — RoundTrip, KindFilter, Delete, DeleteBySource,
  PruneTurns, ScopeMismatch, DimensionMismatch, and **Isolation**.
- **`RunDocBackendSuite(t, factory)`** — RoundTrip, Supersede, Delete, ExportAll, ScopeMismatch, and
  **Isolation**.
- **The central Isolation proof.** A marker is written for tenant A **through the decorator**, then
  tenant B searches and must see **nothing**; an id collision across tenants must not
  cross-contaminate. When the driver also implements the optional `RawVectorInspector` /
  `RawDocInspector` (a `Namespaces()` accessor), the suite additionally asserts the marker is
  **physically present** in the raw backend under A's namespace — so the test proves it was the
  decorator's unexported namespace seal, not the backend's own logic, that kept B out. A backend that
  cannot physically hold the marker fails ("the isolation contrast is meaningless").
- **Reference drivers.** `RefVectorBackend` / `RefDocBackend` are correct, minimal, dependency-free
  in-memory implementations — the substrate the suite runs against in CI and a worked example a
  community author reads to see the black-box contract honored.

### 4. Wiring — one correct build point per tier

`WithVectorBackend(b)` / `WithFactsBackend(b)`
([`aisotropy/extend.go`](../../aisotropy/extend.go)) store the driver on `appExt`. At boot:

- **Facts** is sealed in `startRuntime` ([`aisotropy/app.go`](../../aisotropy/app.go)):
  `facts.NewDocBackendStore(a.ext.factsBackend)` replaces the default facts store when set, else the
  original constructor runs byte-identically.
- **Semantic** is sealed in the shared `ensureSemantic` build path
  ([`aisotropy/knowledge.go`](../../aisotropy/knowledge.go)):
  `semantic.NewVectorBackendStore(a.ext.vectorBackend, emb.Dimension())` replaces the default
  semantic store. `ensureSemantic` — not `startRuntime` — is the correct point because it is the
  **single memoized build path shared by both** the chat/HTTP RAG surface and the SDK
  `IngestKnowledge` surface; sealing only in `startRuntime` would split-brain SDK ingestion (writing
  to the default store) against chat reads (from the sealed backend). This is a deliberate,
  correctness-driven one-file placement, noted so a reviewer expecting the swap purely in `app.go`
  knows why it is not.

### The differentiator, restated in mechanism

A typical framework's pluggable store hands the plugin an interface that decides *what to read*.
Aisotropy hands the plugin an interface that can only decide *how to store*, and keeps *what to read*
inside an unexported, non-forgeable namespace derivation the plugin never touches. **Bring your own
backend, not your own isolation** — and the `plugintest` suite is the receipt.

## Consequences

- **A consumer can plug a real vector/keyword engine and cannot regress isolation.** The worst a
  broken or hostile backend can do is fail its own tenant's round-trip; it cannot widen a scan past
  its namespace, because it can neither see nor compute another tenant's namespace.
- **Isolation is verifiable per backend, not assumed.** A candidate driver either passes
  `RunVectorBackendSuite` / `RunDocBackendSuite` (including the cross-tenant seal proof) or it does
  not ship — "bring your own backend" carries a test, not a promise.
- **Behavioral parity is preserved.** The sealed stores match the built-in stores' error contracts
  (`ErrScopeMismatch`, `ErrDimensionMismatch`, `ErrFactNotFound`), ranking, pruning, and the single
  audited `ExportAll` cross-tenant path — so swapping the backend does not silently change semantics.
- **Default-safe and dependency-free.** Neither option installed ⇒ the built-in stores, byte-for-byte;
  no new module dependency; no OpenAPI contract change.
- **Honest gaps (named, not hidden).**
  - **External implementability:** the driver **payload types** (`BackendVector`/`BackendHit`,
    `BackendDoc`/`BackendDocHit`) are exported from the `internal/` packages but not yet re-exported
    on the façade, so a driver cannot be authored from an **external** module today (it is fully
    implementable inside this module and its tests). Re-exporting them — alongside the Tier-1 payload
    aliases ADR-0033 owes — is the tracked follow-on.
  - **Naming:** the facts driver shipped as `DocBackend` (the document/task journal), not the
    spec-time placeholder `KVBackend` used while ADR-0033 was being written.
  - **Tier routing:** physical dedicated-tenant routing is not layered over a BYO backend (logged at
    boot); the backend owns its own persistence topology.
- **The Tier-2 half of the open-core positioning is now executable.** With Tier-1's Reranker debt
  closed in ADR-0033 and Tier-2's sealed driver shipped here, "we seal the isolation competitors
  leak" is backed by running code and a conformance suite, not a slide.

## References

- **Extends / detail-of:** [ADR-0033](0033-open-core-extension-seams.md) (the tiering rule and the
  per-seam status; this ADR is its Tier-2 design record) · **sibling:**
  [ADR-0032](0032-plugin-ecosystem.md) (the plugin ecosystem; tool contributions ride Tier-1, the
  memory backends are deliberately Tier-2 and are opened only as sealed drivers)
- **Invariants held:** [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) (sealed kernel
  L0 — the drivers sit *below* the seal, the decorator *is* the seal) ·
  [ADR-0011](0011-tenant-scoped-statement-builder.md) (isolation by construction — `namespaceFor`
  derives from the `Scope`-from-`ctx` the decorator is handed) ·
  [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) (payload/namespace filtering during
  the scan, never a post-filter) · [ADR-0017](0017-semantic-tier-multi-kind-memory.md) (the multi-kind
  points the vector decorator round-trips and Kind-filters) ·
  [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md) (the facts journal the doc decorator mirrors,
  including `ExportAll`)
- **Drivers + sealed decorators:** [`internal/semantic/backend.go`](../../internal/semantic/backend.go)
  (`VectorBackend`, `BackendVector`/`BackendHit`, `NewVectorBackendStore`, unexported `namespaceFor`) ·
  [`internal/facts/backend.go`](../../internal/facts/backend.go) (`DocBackend`,
  `BackendDoc`/`BackendDocHit`, `NewDocBackendStore`, `ExportAll`, unexported `namespaceFor`)
- **Conformance suite:** [`internal/plugintest`](../../internal/plugintest)
  (`RunVectorBackendSuite`/`RunDocBackendSuite`, `RefVectorBackend`/`RefDocBackend`,
  `RawVectorInspector`/`RawDocInspector`, the cross-tenant Isolation proofs)
- **Wiring:** [`aisotropy/extend.go`](../../aisotropy/extend.go) (`WithVectorBackend`/`WithFactsBackend`,
  the `VectorBackend`/`DocBackend` façade aliases, `appExt.vectorBackend`/`factsBackend`) ·
  [`aisotropy/app.go`](../../aisotropy/app.go) (`startRuntime` — facts seal) ·
  [`aisotropy/knowledge.go`](../../aisotropy/knowledge.go) (`ensureSemantic` — semantic seal at the
  shared HTTP-RAG + SDK-ingestion build point)
