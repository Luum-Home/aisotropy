---
type: ADR
title: "ADR-0004 — Facts memory: rqlite vs LiteFS"
description: Which engine distributes the facts/episodic layer (networked SQLite+FTS5) — rqlite (Raft/MIT) is recommended over LiteFS for consistency under failure and maintenance risk.
tags: [decisions, adr, memory, rqlite, litefs, consistency]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0004 — Facts memory: rqlite vs LiteFS

## Status

**✅ Accepted (ratified by the user, 2026-07-08).** The engine for the facts layer is **rqlite**.

## Context

Aisotropy's [tiered memory](../architecture/memory-tiers.md) separates storage by access
temperature. **Layer 2 — facts / episodic memory** (the "Engram-style journal") is the
disk/SSD of the [LLM OS model](../architecture/overview.md#llm-os): it stores tasks, rules,
hard preferences, and summarized episodes, and is retrieved **by keyword (FTS5)**, not by
vectors — a direct application of the KB-over-RAG principle.

Unlike working memory (BadgerDB, in-process, µs), this layer is **networked**: it's shared by
several replicas of the Aisotropy binary and must survive restarts. The substrate is already
decided: **SQLite + FTS5**. What this ADR resolves is **how that SQLite is distributed and
replicated** to have high availability without losing consistency.

Hard requirements for this layer:

1. **Native FTS5** — hot-path retrieval is a `MATCH` over text (see the conceptual query in
   [tiered memory](../architecture/memory-tiers.md)). The engine cannot degrade or wrap FTS5.
2. **Tenant/user isolation** — every query carries `tenant_id`/`user_id` from the `ctx`.
   Crosses with [ADR-0007](index.md) (single filtered DB vs. DB-per-tenant).
3. **Low-latency local-first reads** — memory hydration (step 3 of the
   [request lifecycle](../architecture/overview.md#llm-os)) runs in parallel and on the hot
   path; a network hop to the leader on every request is unacceptable.
4. **Consistency under failure** — if a node dies, already-confirmed facts cannot be lost, nor
   can deleted facts come back to life. This **crosses with Aisotropy's Transactional Outbox
   pattern** (see [consistency](../architecture/consistency.md)): the write to this layer is
   done by an async worker from the BadgerDB Outbox, with idempotent retries. The facts engine
   is the **sink** of that Outbox, and its durability guarantee defines whether the Outbox can
   trust an ACK.
5. **Permissive license** — a hard project requirement: **MIT/Apache-2.0 only**. Zero BSL, zero
   commercial licenses, zero gray areas (the same rule that ruled out Turso and GlareDB).

## Goals / Non-goals

**Goals**

- Choose the replication/distribution engine for the SQLite+FTS5 facts layer.
- Prioritize consistency under failure and medium-term maintenance health over features.
- Keep the hot read path in the millisecond range and local where possible.
- Preserve the stack's license purity (100% MIT/Apache-2.0).

**Non-goals**

- **Not** deciding the physical multi-tenant model here (single filtered DB vs.
  DB-per-tenant): that's [ADR-0007](index.md). This ADR picks the engine; both engines support
  both topologies.
- **Not** redesigning the Engram pattern (distillation, scoring, supersede): that lives in
  [tiered memory](../architecture/memory-tiers.md) and is engine-agnostic.
- **Not** addressing the sleep cycle / DataFusion (that's [ADR-0005](index.md)); it's only
  noted that the batch consolidator must be able to read the `.db` files the chosen engine
  produces.

## Options considered

### Option A — rqlite (github.com/rqlite/rqlite)

SQLite wrapped in a service that replicates **the SQL command log** via **Raft**. It's a
consensus system: there's a leader and followers; every write goes through the leader and is
replicated synchronously to the quorum before being confirmed. There's an authoritative,
consistent copy at all times — Raft prevents divergent copies.

- **FTS5:** supported (it's standard SQLite underneath; exposes full-text search and JSON).
- **Reads:** **per-query selectable** consistency levels — `none`, `weak` (default),
  `linearizable`, `strong`. Supports **read-only nodes** that serve local reads with
  `level=none` + a `freshness` parameter (bounded staleness): exactly the local-first,
  low-latency pattern memory hydration needs, without a hop to the leader.
- **Writes:** all through the leader, confirmed by Raft quorum (strong durability).
- **License:** **MIT**.
- **Maturity/maintenance (2026):** active. FAQ updated in May 2026; v10 with CRC32 checksums to
  detect FS corruption, a non-blocking snapshot store, and internal protobuf. Committed
  maintainer (Philip O'Toole) for years. Real but bounded risk: **bus factor** of a project
  heavily centered on one maintainer.

### Option B — LiteFS (github.com/superfly/litefs, Fly.io)

A **FUSE** filesystem that intercepts writes to SQLite `.db` files at the transaction level and
replicates **bytes/pages** to replicas. **Primary/replica** model with **asynchronous**
replication: there's a primary that writes and replicas that receive changes with lag; replicas
forward writes to the primary and can serve **stale reads**.

- **FTS5:** supported (real SQLite underneath, transparent file-level replication).
- **Reads:** local and very fast on any replica, but **potentially stale** due to asynchronous
  replication.
- **Writes:** primary only; async to replicas → a loss window during failover.
- **License:** **Apache-2.0** (permissive, OK).
- **Maturity/maintenance (2026):** **red flag.** The project is still pre-1.0 and in beta; the
  latest release is **v0.5.14 (April 2025)**. Fly.io officially stated: *"LiteFS was not
  discontinued, but we refocused effort on other parts of the company, so it gets limited
  updates"* and that **there's no plan for significant further development**. The official docs
  literally warn: *"we cannot provide support or guidance for this product. Use with caution."*
  The managed **LiteFS Cloud service was shut down on 10/15/2024**. The docs also warn of
  **data loss** when combining LiteFS with autostop/autostart (a stale machine can "win" the
  lease and LiteFS discards newer changes → rollback).

### Option C — Turso / libSQL (DISCARDED due to license)

libSQL is an MIT fork of SQLite, but Turso's replication server **`sqld`** adopted a
**BSL** (Business Source License). BSL is **not** a permissive open-source license: it imposes
commercial-use restrictions and a future conversion date. It violates the project's hard
license requirement (MIT/Apache-2.0 only). **Discarded outright**, the same way it was
discarded in the original design — its technical merits aren't evaluated because it doesn't
pass the license filter.

### Option D — Physical topology (deferred to ADR-0007)

Orthogonal to the engine: **one SQLite DB filtered by `tenant_id`** vs. **one DB per tenant**.
Worth noting because **rqlite** and **LiteFS** enable different strategies (rqlite scales
better with a single filtered DB and a Raft cluster; LiteFS tends toward DB-per-tenant due to
its file model). Not decided here — see [ADR-0007](index.md) — but choosing rqlite **doesn't
close off** either option.

## Trade-off comparison table

| Criterion | rqlite (Option A) | LiteFS (Option B) |
|---|---|---|
| **Consistency** | Configurable strong consistency per query (Raft: `none`/`weak`/`linearizable`/`strong`). Always one authoritative copy. | Eventual/async. Stale replicas; documented rollback window on failover (**possible data loss**). |
| **Read latency** | ms; **local** on read-only nodes (`level=none`+`freshness`) with bounded staleness. | Very low/local on any replica, but **no freshness guarantee**. |
| **Operational complexity** | Medium: run and observe a Raft cluster (quorum, leader). | Medium-high: FUSE + lease management (Consul/lease); autoscaling pitfalls. |
| **Write model** | All through the leader, synchronous quorum → strong durability. | Primary only, **asynchronous** replication → a loss window. |
| **Maturity / maintenance (2026)** | **Active** (FAQ 05/2026, v10). Risk = bus factor of one maintainer. | **At risk.** Pre-1.0 beta, last release 04/2025, Fly.io "no significant development plan," **no support**. Parallel to GlareDB. |
| **License** | **MIT** | **Apache-2.0** (OK) |
| **Fit with the Outbox** | The leader gives a durable ACK after quorum → the Outbox worker trusts the ACK. | The primary's ACK is local; replicas converge later → weaker durability semantics for the sink. |

## Recommended decision

**rqlite is recommended.** (Ratified by the user.)

The two engineering factors the project weighs above everything else point to rqlite:

**(a) Consistency under failure — crossing with the Transactional Outbox.** The facts layer is
the BadgerDB Outbox's **sink**: an async worker drains events and writes them here with
idempotent retries. For that design to be correct, the sink must give a **durable ACK**: when
the worker receives confirmation, the fact must actually be persisted and cannot disappear in a
failover. rqlite guarantees this — the leader confirms **after** Raft has replicated to the
quorum. LiteFS's async model does **not**: its documented rollback window (with the risk of
discarding newer changes) can "undo" a fact the Outbox already considered delivered, breaking
the pipeline's at-least-once/idempotent guarantee. Configurable strong consistency beats async
replication for a transactional sink.

**(b) Medium-term maintenance risk.** This is the decisive finding of the research, and it's
the **same pattern that led to abandoning GlareDB**. Fly.io officially stated that LiteFS gets
"limited updates," that **there's no plan for significant further development**, and that
**they provide no support** ("use with caution"); the latest release is from **April 2025** and
it's still pre-1.0. Betting memory's disk on a project its own owner already put into
maintenance mode is exactly the debt Aisotropy avoids by design. rqlite, by contrast, is
**active in 2026** (v10, FAQ May 2026); its risk is the bus factor of a single maintainer —
real, but **far lower** than a project its sponsor has already disinvested from.

Technical tie on FTS5 (both are real SQLite) and on license (both permissive; rqlite's MIT is
even cleaner). **Consistency** and **project health** break the tie, and both fall on rqlite's
side. Local-first latency — the one spot where LiteFS shines — rqlite covers with read-only
nodes + `freshness`, without giving up the durability guarantee.

## Consequences

**Positive**

- **Durability aligned with the Outbox:** the Raft leader's ACK is a solid confirmation point
  for the consolidation worker. Simplifies reasoning about [consistency](../architecture/consistency.md).
- **Local hot reads without sacrificing correctness:** read-only nodes with `freshness` give
  low latency and **bounded, explicit** staleness (not "hope it's fresh").
- **Consistency as a dial, not an article of faith:** every query picks its level. Memory
  hydration can use `weak`/`none`; a sleep-cycle contradiction reconciliation can require
  `linearizable`.
- **MIT license** — zero legal friction, a Go binary that compiles alongside the rest of the
  stack (Distroless, few MB), consistent with the "everything in-process compiles together"
  principle from the [overview](../architecture/overview.md).
- **Doesn't close off ADR-0007:** rqlite supports both a single filtered DB and DB-per-tenant.

**Negative / technical debt**

- **Bus factor:** rqlite depends heavily on one maintainer. Mitigation: it's MIT and the code
  is readable; if it disappeared, it could be forked/maintained (unlike a BSL project). Watch
  activity.
- **Operating a Raft cluster:** requires understanding quorum, leader election, and
  partitions. It's more operational overhead than a single local SQLite. Needs a runbook and
  observability (OTel).
- **Writes bottleneck at the leader:** every write goes through one node. Acceptable for
  Aisotropy because the hot path **reads**; writes are buffered by the Outbox (not on the
  user's request path). But it sets a write-throughput ceiling per cluster → relevant to
  ADR-0007.
- **Misused `level=none` returns stale data:** the team must choose the right level per case;
  document the convention (which query uses which level) to avoid introducing staleness bugs.

## Open questions

1. **Consistency level per use case** — does memory hydration run with `weak` or
   `none`+`freshness`? What `freshness` is tolerable (seconds?) for the "journal"? Define the
   convention before coding.
2. **Physical topology (ADR-0007)** — a single DB filtered by `tenant_id` over an rqlite
   cluster, or DB-per-tenant? Impacts write throughput (single leader) and isolation.
3. **Reading from DataFusion (ADR-0005)** — does the sleep cycle read rqlite's `.db` files
   directly (snapshot/backup) or export to Parquet? Confirm rqlite's on-disk format is
   consumable cold, without touching the live cluster.
4. **Failover and the Outbox** — formalize the exact semantics: does the worker retry if the
   leader changes mid-batch? Idempotency by event key. Detail in
   [consistency](../architecture/consistency.md).
5. **Off-site backups** — cluster snapshot policy (rqlite supports it) to satisfy
   local-first/sovereignty without depending on the live cluster.

## References

- rqlite — repo and project: <https://github.com/rqlite/rqlite> · <https://rqlite.io>
- rqlite — Read Consistency (`none`/`weak`/`linearizable`/`strong`, read-only nodes, `freshness`):
  <https://rqlite.io/docs/api/read-consistency/>
- rqlite — Features & Use Cases (FTS5 / full-text search, JSON):
  <https://rqlite.io/docs/features/>
- rqlite — Design (Raft, single authoritative copy): <https://rqlite.io/docs/design/>
- LiteFS — repo (Apache-2.0, beta, last release v0.5.14 April 2025):
  <https://github.com/superfly/litefs>
- LiteFS — docs (pre-1.0, "we provide no support, use with caution," data-loss risk with
  autostop/autostart): <https://fly.io/docs/litefs/>
- LiteFS — maintenance status (Fly.io: "limited updates, no significant development plan"):
  <https://community.fly.io/t/what-is-the-status-of-litefs/23883>
- LiteFS Cloud — managed service shutdown (10/15/2024):
  <https://community.fly.io/t/sunsetting-litefs-cloud/20829>
- Turso `sqld` — BSL license (reason for discard): <https://github.com/tursodatabase/libsql>

---

Cross-links: [tiered memory](../architecture/memory-tiers.md) ·
[rqlite component](../components/rqlite-facts-memory.md) ·
[consistency](../architecture/consistency.md) ·
[ADR-0007 — physical multi-tenancy](index.md)
