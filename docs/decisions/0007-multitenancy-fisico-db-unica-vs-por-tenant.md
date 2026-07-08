---
type: ADR
title: "ADR-0007 — Physical multi-tenancy: single DB vs DB-per-tenant"
description: What physical topology materializes multi-tenant isolation for the facts layer (rqlite). A HYBRID/tiered model is recommended — single DB filtered by tenant_id (pooled) as default over an rqlite cluster, with a dedicated DB (its own cluster) only for large or compliance-critical tenants — because rqlite is a single DB per Raft cluster and pure "DB-per-tenant" doesn't scale.
tags: [decisions, adr, multi-tenancy, rqlite, isolation]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0007 — Physical multi-tenancy: single DB vs DB-per-tenant

## Status

**✅ Accepted (ratified by the user, 2026-07-08).** **Option C (hybrid / tiered)** is chosen: a single DB
filtered by `tenant_id` (pooled) as the default for most tenants, and a **dedicated DB (its
own rqlite cluster)** reserved for large tenants or those with compliance/sovereignty
requirements demanding physical isolation.

## Context

The engine for the [facts / episodic layer](../architecture/memory-tiers.md) is already
decided: **rqlite** (SQLite + FTS5 replicated via Raft), ratified in
[ADR-0004](0004-facts-memory-rqlite-vs-litefs.md). That ADR chose the **engine**; it explicitly
left the **physical topology** open (its "Option D") and deferred it here.

This ADR resolves **one** question: given that **logical** isolation is already guaranteed by
Go's `ctx` at every layer (see [multi-tenancy](../architecture/multi-tenancy.md)), how is that
isolation **physically materialized** in the facts layer?

- **Single filtered DB (pooled):** one rqlite cluster shared by all tenants; every query
  carries `WHERE tenant_id=? AND user_id=?`.
- **DB-per-tenant (siloed):** a separate physical database per tenant; isolation comes from
  the topology (which base you connect to), not a `WHERE`.

Logical isolation is already described in [multi-tenancy](../architecture/multi-tenancy.md)
and **doesn't change** based on what's decided here: rqlite's mandatory `WHERE tenant_id`
still exists in pooled, and becomes implicit (via connection string) under siloed. What
changes is **the blast radius, the write ceiling, operability, and the compliance story**.

### The finding that conditions everything: rqlite is **one DB per Raft cluster**

Research (2026) confirms the decisive fact: **rqlite manages a single SQLite database per
Raft cluster.** It doesn't expose "logical databases" or a `CREATE DATABASE` that
instantiates cheap DBs within the same cluster. The request for "multiple databases per
cluster / ability to name the database" ([issue #927](https://github.com/rqlite/rqlite/issues/927))
remains **open as a feature request**: it doesn't exist today. General guidance and third-party
analysis say it plainly: *"you have to run one rqlite cluster per SQLite database."*

This is what separates rqlite from Turso/libSQL (discarded for its BSL license in ADR-0004):
Turso creates **instant logical DBs** on top of the same service, so "DB-per-tenant" there is
cheap. In rqlite it's **not**: literal "DB-per-tenant" means **one Raft cluster (3+ nodes,
leader, quorum, election) per tenant**. At a scale of thousands of tenants that's **thousands
of Raft clusters** → operationally and economically unviable. `ATTACH DATABASE` doesn't rescue
the approach either: SQLite limits attached databases (10 default, 125 max), and rqlite
replicates a single on-disk file per cluster, so it's not a multi-tenant sharding mechanism.

**Corollary:** in rqlite, "DB-per-tenant" **is not a viable default topology**; it's, at most,
an exception reserved for a few tenants who justify it.

### Requirements this ADR must balance

1. **Isolation** — consistent with the [ctx-based isolation](../architecture/multi-tenancy.md)
   each layer already implements. The question here is how much **physical** (not logical)
   isolation is needed.
2. **Compliance / sovereignty** — some tenants (regulated, sensitive data, residency
   requirements) may require evidence of physical isolation and per-tenant backup/restore/
   deletion.
3. **Raft leader write ceiling** — ADR-0004 flagged it as a hard consequence: *"writes
   bottleneck at the leader."* Every write in a cluster goes through one node → there's a
   write-throughput ceiling **per cluster**. The topology decides how many tenants share that
   ceiling.
4. **Operability** — how many Raft clusters need to be operated, observed (OTel), backed up,
   and migrated.

## Goals / Non-goals

**Goals**

- Choose the physical topology of the facts layer (rqlite) that materializes multi-tenant
  isolation, weighing rqlite's real model (1 DB per cluster).
- Give a clear sharding rule: which tenant goes pooled and which goes to a dedicated DB, and
  why.
- Preserve `ctx`-based logical isolation as an invariant, whichever topology wins.
- Keep the Raft leader write ceiling under control, with an escape hatch (sharding).

**Non-goals**

- **Not** re-deciding the engine: rqlite is already fixed in
  [ADR-0004](0004-facts-memory-rqlite-vs-litefs.md).
- **Not** redefining **logical** isolation (`ctx`, composite key, per-layer filter): that lives
  in [multi-tenancy](../architecture/multi-tenancy.md) and doesn't change based on the
  topology.
- **Not** deciding **Qdrant's** topology (the semantic layer). Qdrant already uses a shared
  collection + payload index and has its own "tiered multitenancy" axis; the parallel is noted
  but resolved in its own component, not here.
- **Not** specifying the tenant→cluster routing mechanism at the code level (routing table,
  service discovery): that's an open implementation question.

## Options considered

### Option A — Single filtered DB (pooled)

One rqlite cluster for **all** tenants. Isolation is the mandatory `WHERE tenant_id=? AND
user_id=?` on every FTS5 statement (see [multi-tenancy](../architecture/multi-tenancy.md)).

- **Isolation:** logical, by filter. Correct as long as the filter never fails, but **a query
  bug (missing WHERE) leaks data between tenants**. The discipline is structural (the `ctx`
  builds the filter), but physical isolation is nil.
- **Blast radius:** maximum. Corruption, a long lock, a bad migration, or an abusive tenant
  affects **everyone**.
- **Write ceiling:** **all tenants share the single Raft leader.** The "noisy neighbor" is
  real: a high-write-volume tenant consumes everyone's leader throughput. Partially mitigated
  because the hot path **reads** and writes are buffered by the Outbox (ADR-0004), but the
  ceiling is shared.
- **Compliance:** weak. Producing evidence of per-tenant isolation and selective backup/
  restore/deletion on a shared DB is costly and fragile (an industry finding: audits that
  demand weeks of documentation that a siloed topology would give "for free").
- **Operability:** **the best.** One cluster to operate, observe, and back up. Cheap.
- **Viability on rqlite:** **excellent** — it's exactly the pattern rqlite favors (1 DB per
  cluster, 1 cluster total).

### Option B — DB-per-tenant (siloed)

A separate physical database per tenant. On rqlite, per the finding above, this is **one Raft
cluster per tenant**.

- **Isolation:** **maximum and physical.** Tenant B can't see tenant A's data even with a query
  bug: they're in different clusters. The filter becomes implicit (connection string).
- **Blast radius:** **minimal.** Corruption/lock/migration issues affect only one tenant.
- **Write ceiling:** **its own Raft leader per tenant** → zero noisy neighbor, dedicated
  throughput. Ideal for large tenants.
- **Compliance:** **the best.** Per-tenant backup/restore/deletion/residency are trivial: it's
  its own cluster.
- **Operability:** **the worst at scale.** Thousands of tenants = thousands of Raft clusters
  (each 3+ nodes, quorum, leader election, observability, snapshots). **Prohibitive**
  infrastructure and operational cost. Provisioning a new tenant = spinning up a cluster, not
  an `INSERT`.
- **Viability on rqlite:** **poor as a default, fine as an exception.** rqlite doesn't create
  cheap logical DBs (unlike Turso); each "silo" is full Raft infrastructure. Sustainable only
  for **dozens**, not thousands.

### Option C — Hybrid / tiered (sharded) — *recommended*

Combine A and B by **tenant class**:

- **Default pool:** the vast majority of tenants (SMB, free/standard) live in a **shared**
  rqlite cluster, with `WHERE tenant_id`. When that pool nears its leader's write ceiling,
  **another pool is added** (shard) and tenants are redistributed across pools — several
  pooled clusters, each with its own leader. This removes the global ceiling without going to
  one cluster per tenant.
- **Silo on demand:** a **large** tenant (volume that saturates a pool) or a
  **compliance-critical** one (regulation, data residency, evidence of physical isolation,
  guaranteed deletion) gets a **dedicated rqlite cluster**. Few of them, and they pay for it.
- **Tenant→cluster routing** (which pool/silo each tenant connects to) is resolved with a
  routing table; the `ctx` still carries `tenant_id`/`user_id` as it does today — only **which
  cluster the connection points to** changes.

- **Isolation:** graduated. Physical for those who need it; logical (via `WHERE`) for the
  rest.
- **Blast radius:** bounded per pool. An incident affects one shard, not the whole base;
  silos remain fully isolated.
- **Write ceiling:** **solved by design** — when a pool saturates its leader, another pool is
  added; write-hungry tenants go to a silo. The ceiling stops being global.
- **Compliance:** **covered where it matters** — regulated tenants are siloed and produce
  per-tenant evidence/backup/deletion; the rest don't pay that cost.
- **Operability:** **good and grows with demand** — a handful of pools + a few silos are
  operated, not thousands of clusters. The number of clusters grows with the business, not
  with the number of tenants.
- **Viability on rqlite:** **the best fit for rqlite's real model** — uses the sweet spot
  (1 DB per cluster) for the pools and pays the Raft cost only where the value justifies it.

## Trade-off comparison table

| Criterion | A — Pooled (single DB) | B — Siloed (DB-per-tenant) | C — Hybrid / tiered |
|---|---|---|---|
| **Isolation** | Logical (WHERE). A query bug leaks data. | **Physical and maximum.** Immune to query bugs. | Physical for silos; logical for the pool. |
| **Blast radius** | **Maximum** (all tenants). | **Minimal** (one tenant). | Bounded per pool; silos isolated. |
| **Compliance / sovereignty** | Weak; per-tenant evidence and deletion are costly. | **The best** (per-tenant backup/restore/deletion are trivial). | **Covered where it matters** (regulated → silo). |
| **Write throughput vs. Raft leader** | **Shared ceiling**; real noisy neighbor. | Own leader per tenant; no noisy neighbor. | Ceiling per pool; pools added; large tenants → silo. |
| **Operability** | **The best** (1 cluster). | **The worst at scale** (thousands of Raft clusters). | Good; grows with demand, not tenant count. |
| **Per-tenant backup / restore** | Hard (extracting 1 tenant from a shared DB). | Trivial (it's its own cluster). | Trivial for silos; per-filter for the pool. |
| **Viability on rqlite (1 DB/cluster)** | **Excellent** (native pattern). | **Poor as default** (Raft per tenant); OK as exception. | **The best fit** for the real model. |
| **New tenant provisioning** | `INSERT` (instant). | Spin up a cluster (slow, expensive). | `INSERT` for the pool; provisioning for silos. |

## Recommended decision

**Option C — hybrid / tiered is recommended.** The default is **pooled** (a single DB filtered
by `tenant_id` over an rqlite cluster, scalable to several pools/shards); the **silo (a
dedicated rqlite cluster) is reserved** for large or compliance-critical tenants.

The reasoning is direct and set by the **finding about rqlite's DB model**:

**(a) rqlite is one DB per Raft cluster → pure "DB-per-tenant" (Option B) can't be the
default.** Unlike Turso/libSQL (which creates instant logical DBs and makes the siloed pattern
cheap), rqlite requires **a full Raft cluster per database**. Thousands of siloed tenants =
thousands of Raft clusters = prohibitive operation and cost. Option B is disqualified as a
general topology and survives only as an **exception** for a few tenants.

**(b) Pooled (Option A) is rqlite's operational sweet spot, but only with an escape hatch.**
A single shared cluster is cheap and fits the native model, but it concentrates all write
throughput on **one** Raft leader — the ceiling ADR-0004 flagged as a hard consequence — and
has maximum blast radius and weak compliance. Pure pooled isn't enough for enterprise.

**(c) The hybrid takes the best of each, aligned with rqlite.** Pooled (with the ability to
**add pools/shards** when a leader saturates) covers 95% of tenants at minimal cost and uses
the pattern rqlite favors; the dedicated silo covers the 5% that demand physical isolation or
their own throughput, and those few justify the Raft cost. The leader write ceiling stops
being a global limit: it's resolved by moving tenants between pools or to silos.

**(d) Consistency with `ctx`-based isolation.** Whatever topology wins, **logical** isolation
doesn't change: the `ctx` still carries `tenant_id`/`user_id` from Huma and builds the filter.
The hybrid only adds, on top of that invariant, a **routing layer** that decides **which
cluster** each tenant's connection points to — without touching the isolation mechanism
already described in [multi-tenancy](../architecture/multi-tenancy.md). The mandatory `WHERE
tenant_id` remains in the pools (defense in depth) and becomes redundant-but-harmless in the
silos.

## Consequences

**Positive**

- **Cheap default, escape hatch for hard cases:** 95% of tenants cost an `INSERT` in a pool;
  physical isolation is paid for only where the business/regulation demands it.
- **Write ceiling resolved by design:** the Raft leader limit (ADR-0004) stops being global —
  pools are added or a tenant is promoted to a silo when it gets tight.
- **Compliance without a global overhead:** regulated tenants get per-tenant backup/restore/
  deletion and isolation evidence; the rest don't carry that weight.
- **Fits rqlite's real model:** uses the sweet spot (1 DB/cluster) for pools and pays
  Raft-per-tenant only in the few silos that warrant it.
- **Logical isolation intact:** the `ctx` and the `WHERE tenant_id` remain the base mechanism;
  the hybrid is a routing layer on top, not a replacement.

**Negative / technical debt**

- **Tenant→cluster routing layer:** a map of which tenant lives in which pool/silo must be
  built and maintained, and consulted when opening a connection. It's critical state (if
  routing fails, a tenant reads the wrong DB). Must be simple, audited, and observable.
- **Rebalancing / migration between tiers:** promoting a tenant from pool to silo (or moving
  tenants between pools) requires **migrating their data between rqlite clusters** with no
  perceptible downtime. Needs a runbook and, likely, temporary dual writes + cutover.
- **Operational heterogeneity:** there's no longer "one cluster"; there are N pools + M silos
  with different profiles. More observability surface (OTel per cluster) and more backup
  paths.
- **The WHERE bug still lurks in the pools:** within a pool, isolation remains logical.
  Mitigated by building the filter from the `ctx` and by tests, but the risk isn't zero
  (unlike a silo).
- **Tenant→tier cutoff criteria to define:** which metric triggers "this tenant needs a silo"
  (write volume, size, a compliance flag) must be explicit to avoid deciding case by case.

## Open questions

1. **Routing mechanism** — a routing table in the facts layer itself, in a separate control
   store, or derived from the tenant's JWT/plan? How is it cached without becoming a context
   leak?
2. **Silo promotion threshold** — what metric and value (writes/s, DB size, a compliance flag
   in the contract) triggers moving a tenant from pool to dedicated cluster?
3. **Pool sharding policy** — when a pool saturates its leader, how are tenants redistributed
   across pools? Hash of `tenant_id`, explicit assignment, by region?
4. **Zero-downtime migration between tiers** — a concrete procedure for moving a tenant from
   one rqlite cluster to another (snapshot + Outbox replay, dual write, cutover window).
   Crosses with the [Outbox and consistency](../architecture/consistency.md).
5. **Data residency / sovereignty** — must some silo live in a specific region/infra for
   regulatory reasons? Routing must be able to express location, not just tier.
6. **Parallel with Qdrant** — the semantic layer has the same axis (shared collection vs.
   tiered). Should the tier policy be aligned between rqlite and Qdrant per tenant, or does
   each layer decide on its own?
7. **Per-tenant pool backups** — while a tenant is in a pool, how is a selective restore/
   deletion produced if requested (e.g., GDPR "right to be forgotten") without touching its
   neighbors?

## References

- rqlite — is **one DB per Raft cluster**; feature request for multiple databases per cluster
  (**open**, not supported today): <https://github.com/rqlite/rqlite/issues/927>
- rqlite — FAQ (write performance, one cluster per database): <https://rqlite.io/docs/faq/>
- rqlite — Design (Raft, same DB on every node): <https://rqlite.io/docs/design/>
- rqlite — General guidelines (clustering): <https://rqlite.io/docs/clustering/general-guidelines/>
- Simon Willison — Replicating SQLite with rqlite (one cluster per database):
  <https://til.simonwillison.net/sqlite/replicating-rqlite>
- Multi-tenant SaaS — pooled vs siloed vs hybrid, trade-offs and compliance (2026):
  <https://www.thecloudplaybook.com/p/multi-tenant-database-architecture-tradeoffs-pooled-siloed-schema>
- Multi-tenant architecture — DB-per-tenant vs shared, pros and cons (2026):
  <https://dev.to/young_gao/multi-tenant-architecture-database-per-tenant-vs-shared-schema-1n2e>

---

Cross-links: [ADR-0004 — rqlite vs LiteFS](0004-facts-memory-rqlite-vs-litefs.md) ·
[multi-tenancy — ctx-based isolation](../architecture/multi-tenancy.md) ·
[rqlite — facts memory](../components/rqlite-facts-memory.md) ·
[tiered memory](../architecture/memory-tiers.md) ·
[consistency](../architecture/consistency.md)
