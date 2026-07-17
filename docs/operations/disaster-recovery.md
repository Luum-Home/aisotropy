# Disaster Recovery Runbook — Aisotropy

This runbook is the operator-facing procedure for recovering the Aisotropy
runtime after data loss, corruption, or a host/region failure. It complements
[`backup-restore.md`](./backup-restore.md), which documents the per-tier backup
and restore *mechanics*; this document covers **when, in what order, and to what
target** you restore, plus the honest limits of what DR can and cannot do today.

The tool referenced throughout is `cmd/aisotropy-backup`.

---

## 1. Scope and objectives (RPO / RTO framing)

Aisotropy is a stateful LLM-OS runtime with three memory tiers of very different
value and volatility. DR objectives differ per tier — a one-size RPO/RTO would
either over-invest in ephemeral data or under-protect durable facts.

| Tier | Data value | Volatility | Suggested RPO* | Suggested RTO* | Recovery source |
|---|---|---|---|---|---|
| Facts (SQLite / rqlite) | **High** — durable preferences, rules, task journal | Low-moderate | ≤ 24h (scheduled) or ≤ minutes (rqlite hot backup) | Minutes | Native backup file / rqlite `/db/backup` |
| Working memory (BadgerDB) | Medium — active chat buffer + condensed summary | High (rewritten every turn) | ≤ 24h | Minutes | `Backup`/`Load` |
| Semantic (Qdrant) | Medium — associative recall, RAG | Low (append-mostly) | ≤ 24h | Minutes–hours | Qdrant snapshot **or** re-embed |
| Semantic (in-process default) | Derived — no source of truth | N/A | **N/A — not backed up** | Rebuild time | Re-embed from facts/turns |

\* *Suggested* starting points, not guarantees. The delivered mechanisms are
scheduled/manual backups; there is **no continuous/point-in-time capability**
today (see §6). Your effective RPO is the age of your most recent backup.

**RPO (Recovery Point Objective)** — how much data you can afford to lose,
expressed as backup age. With scheduled backups, RPO = backup interval.

**RTO (Recovery Time Objective)** — how long recovery may take. For these tiers,
restore is dominated by data volume; the working-memory and facts tiers restore
in minutes at typical sizes.

---

## 2. Restore order

Restore tiers in **dependency order** so the server comes up against a coherent
picture, and so derived data is rebuilt from restored sources rather than stale
ones:

1. **Facts first.** Facts are the highest-value, most-authoritative tier and are
   the source from which semantic vectors are (re-)derived. Restore facts before
   anything that depends on them.
2. **Semantic second.** Either restore the Qdrant snapshot, **or** — if the
   snapshot is missing/stale, or you are on the in-process default — plan to
   re-embed from the restored facts/turns (see §4).
3. **Working memory last.** It is the most volatile and lowest-dependency tier.
   Restoring a recent BadgerDB backup returns in-flight session summaries; if it
   is unavailable, the system degrades gracefully — sessions simply start with an
   empty working buffer and rehydrate facts/semantic context on the next turn.
4. **Start `aisotropyd`** pointed at the restored directories / services, then
   run the verification checks in §5.

Rationale: a running server with restored facts + semantic but empty working
memory is fully functional (it rehydrates per request). The reverse — working
memory present but facts missing — silently serves an impoverished context. So
never start the server before facts are in place.

---

## 3. Full restore vs. tenant-scoped restore

### Full restore

The default: restore each tier's backup wholesale (BadgerDB directory, facts
`.db` file(s), Qdrant collection snapshot) as in `backup-restore.md`, then start
the server. Use this for host loss, volume corruption, or migrating to new
infrastructure.

### Tenant-scoped restore

Restoring **one tenant** without rolling everyone back to the backup timestamp is
**not a first-class operation** in the delivered tooling, because tenant
isolation is enforced at the *key/row/payload* level within shared physical
stores (BadgerDB key prefixes, SQLite `tenant_id` columns, Qdrant payload
filters) — not by a per-tenant physical file you can restore in isolation. Honest
options:

- **Dedicated-tenant tiers (ADR-0007):** a tenant provisioned as *dedicated* has
  its own physical store — its own `<resource>.db` facts file and its own Qdrant
  collection. These **can** be restored independently: back up / restore just
  that tenant's `.db` file and that tenant's collection snapshot. This is the
  supported path for single-tenant recovery.
- **Shared-pool tenants:** to recover a single shared-pool tenant, restore the
  full backup into a **side/staging** instance, then export that tenant's rows
  (facts filtered by `tenant_id`) and points (Qdrant filtered by payload) and
  re-import them into the live store. This is a manual, per-incident procedure,
  not an `aisotropy-backup` subcommand today (see §6 gaps).

> GDPR **erasure** (already shipped) is the inverse operation and *is*
> tenant/user-scoped — but erase is a delete, not a restore. A restore that
> re-introduces previously-erased data is a compliance regression: after a
> full-store restore, re-apply any erasure requests that were honored **after**
> the backup timestamp.

---

## 4. Semantic reconstruction (re-embed) — the no-snapshot path

When there is no usable Qdrant snapshot, or the deployment runs the in-process
semantic default (which is never backed up), the semantic tier is **rebuilt by
re-embedding the source corpus** rather than restored from a file:

1. Restore facts first (§2).
2. Re-embed and re-ingest the corpus into the semantic tier using the same
   embedder configuration the deployment normally runs. Because the vectors are
   derived, a rebuild yields an equivalent index (modulo embedder version — see
   §5 schema migration).
3. Working memory does not need re-embedding; it is restored (or starts empty).

This is slower than a snapshot restore (embedding cost scales with corpus size)
but requires no separate backup artifact — it is the fallback that always exists
as long as facts survive.

---

## 5. Schema migration during recovery

A restore can coincide with a **schema/format change** in a tier — either because
you are restoring an old backup into a newer binary, or because a migration is
the reason you are recovering. Handle per tier:

### Facts (SQLite / rqlite) schema

The facts schema is applied idempotently at open (`migrate()` in
`internal/facts/sqlite.go`, guarded by `IF NOT EXISTS` and duplicate-column
tolerance). Restoring an **older** facts `.db` into a **newer** binary is
therefore safe for additive changes: the binary re-applies additive DDL (new
columns, the FTS5 external-content index) on first open. Caveats:

- An **FTS index change** (tokenizer, external-content schema) may require
  rebuilding the FTS index after restore. If FTS queries return nothing after a
  restore-across-versions, rebuild the FTS table (`INSERT INTO facts_fts(facts_fts) VALUES('rebuild')`
  or the equivalent for the current schema) — check `internal/facts/schema.go`
  for the FTS table name at the target version.
- A **destructive** schema change (dropped/renamed column) is not auto-migrated
  on restore. Restore into the *matching* binary version first, then upgrade
  forward with that version's migration path.

### Semantic (Qdrant / in-process) — vector-dimension / embedder change

This is the important one and cross-references **[ADR-0054](../decisions/0054-e5-query-passage-prefixes.md)**
(and [ADR-0049](../decisions/0049-multilingual-embedder.md), which it builds on).
A vector index is only valid for the **embedder that produced it**: dimension,
model, and — for the e5 family — the `query:`/`passage:` instruction prefixes all
have to match between the stored corpus and live queries.

Consequences for DR:

- **Same embedder, same dim:** a Qdrant snapshot restores as-is. No migration.
- **Embedder swap or dimension change** (e.g. the bge → `multilingual-e5-small`
  swap, or the e5 prefix change): an old vector corpus is **not** forward-
  compatible. Per ADR-0054, re-embedding an old corpus under a new embedder is a
  **documented operator data-migration (re-ingest)**, *not* a code break — the
  local embedder is opt-in and the crate contract is unchanged. So the recovery
  procedure for an embedder change is the **re-embed** path of §4, not a snapshot
  restore: restore facts, then re-ingest the corpus so every vector is produced
  by the new embedder (and, for e5, carries the correct `passage:` prefix on
  stored documents / `query:` on searches). Mixing old-embedder vectors with
  new-embedder queries in one collection silently degrades retrieval — do not do
  it; rebuild the collection.

### Working memory (BadgerDB)

Working-memory entries are small JSON messages/summaries keyed by
tenant/user/session; there is no relational schema to migrate. A format change to
the stored JSON would be handled by the reader in `internal/memory`, not by the
backup tool. Restoring across a BadgerDB *major* version is the only structural
concern; the `Backup`/`Load` logical stream is the version-tolerant path (it does
not depend on the physical LSM format), which is exactly why the tool uses it.

---

## 6. Honest gaps — what DR does NOT provide today

State these plainly so no one plans around a capability that does not exist:

- **No point-in-time recovery (PITR).** There is no continuous WAL archiving or
  transaction-log shipping for any tier. Recovery granularity is a **discrete
  backup**, so your worst-case data loss is the interval since the last backup
  (RPO = backup age). The facts SQLite tier's WAL is a concurrency mechanism, not
  an archived recovery log.
- **No cross-region / geo replication.** Backups are files you must ship
  off-host yourself. A region loss is only survivable if backups were already
  replicated to another region (your storage layer's job, not this tool's).
- **No automated, scheduled backups built in.** `aisotropy-backup` performs a
  single backup per invocation. Scheduling (cron/systemd-timer/k8s CronJob),
  retention/rotation, and off-host shipping are **operator responsibilities**.
  There is no retention policy or GC of old backups in the tool.
- **No live-server BadgerDB backup.** The directory lock means backups require a
  stopped server or a cold copy (see `backup-restore.md`). There is no online
  hot-backup for the working-memory tier equivalent to SQLite's `VACUUM INTO`.
- **No first-class shared-pool single-tenant restore.** Only dedicated-tenant
  tiers restore independently; shared-pool single-tenant recovery is a manual
  staging-and-reimport procedure (§3).
- **In-process semantic index is never backed up.** By design it has no disk
  footprint; it is only ever rebuilt via re-embed (§4).
- **Backup files are not encrypted by the tool.** The BadgerDB `Backup` stream is
  plaintext even for an encrypted source; encrypt at the storage layer or wrap
  the file yourself (see `backup-restore.md`).
- **Qdrant / rqlite paths are not covered by an in-repo automated test** (neither
  service is provisioned in CI). They compile and `go vet` clean and are verified
  manually against a live service. The BadgerDB and SQLite paths **are**
  round-trip tested (`cmd/aisotropy-backup/roundtrip_test.go`).

Closing any of these (scheduled backups, off-host shipping, PITR, cross-region)
is future work and should be tracked as its own change, not assumed.

---

## 7. Verification after restore

A restore is not complete until verified. Run these before returning the service
to production:

1. **Facts integrity.** The SQLite restore already runs `PRAGMA integrity_check`
   before installing. Post-restore, confirm row counts are in the expected range
   (`SELECT count(*) FROM facts;`) and that an FTS query for a known term returns
   results (rebuild the FTS index if not — §5).
2. **Semantic reachability.** `GET /collections/{c}` (Qdrant) reports the
   expected `points_count`; or, on the re-embed path, confirm ingest completed
   for the full corpus.
3. **Working memory.** Reopen the restored BadgerDB directory read-only and
   spot-check a known session-summary key, or start the server and issue a chat
   for a session that should have an existing summary.
4. **End-to-end health.** Start `aisotropyd` and hit the readiness endpoint:
   `GET /health` runs read-only, `_health`-tenant-scoped probes against the facts
   and semantic tiers and returns `200 {"status":"ok",...}` when both respond
   (`503` / `"degraded"` otherwise). `GET /healthz` is liveness only. A green
   `/health` after restore confirms the tiers are wired and reachable.
5. **Tenant isolation smoke.** Issue a chat as one tenant and confirm no other
   tenant's facts/summary leak into the context — a restore must not have merged
   stores across the key/row/payload isolation boundary.
6. **Erasure reconciliation (compliance).** Re-apply any GDPR erasure requests
   honored **after** the backup timestamp, since a full-store restore
   re-introduces data those requests removed (§3).

Record the backup timestamp restored, the tiers restored, and the verification
results for the incident log.
