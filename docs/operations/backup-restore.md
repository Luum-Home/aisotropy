# Backup & Restore — the Aisotropy memory tiers

This document is the per-tier operator procedure for backing up and restoring the
three persistent Aisotropy stores using **each store's native mechanism**. The
tool that implements these procedures is `cmd/aisotropy-backup` (a separate
binary from `aisotropyd`). For the higher-level runbook — RPO/RTO, restore
order, verification, and known gaps — see
[`disaster-recovery.md`](./disaster-recovery.md).

> **Golden rule:** these procedures use the tier stores' own on-disk / API
> surfaces (BadgerDB `Backup`/`Load`, SQLite `VACUUM INTO`, the Qdrant snapshot
> REST API). They never hand-roll a serialization that could drift from the
> real on-disk format.

## What is (and is not) persistent

| Tier | Store | Persistent? | Backup unit |
|---|---|---|---|
| Working memory | BadgerDB (`AISOTROPY_DATA_DIR`) | Only when `AISOTROPY_DATA_DIR` is set; in-memory default is ephemeral | The BadgerDB directory |
| Facts | Embedded SQLite (`AISOTROPY_DATA_DIR/<resource>.db`) | Only when `AISOTROPY_DATA_DIR` is set; `:memory:` default is ephemeral | Each `<resource>.db` file |
| Facts (networked) | rqlite (`RQLITE_URL`) | Yes | rqlite `/db/backup` endpoint |
| Semantic | In-process cosine index (default) | **No** — no on-disk footprint at all | Nothing to back up; rebuilt by re-embedding |
| Semantic (networked) | Qdrant (`QDRANT_HOST`) | Yes | Qdrant collection snapshot |

Only stores marked persistent have anything to back up. The **in-process
semantic index has zero disk footprint** (`InMemoryStore.Close` is a no-op); its
contents are reconstructed by re-embedding the source corpus, not restored from a
file. This is called out again as a DR gap in the runbook.

## Build the tool

```bash
go build -o bin/aisotropy-backup ./cmd/aisotropy-backup
bin/aisotropy-backup -h
```

---

## 1. Working memory — BadgerDB

BadgerDB exposes a native streaming backup (`DB.Backup(w, since)`) and load
(`DB.Load(r)`). The backup is a logical key/value dump, independent of the
physical LSM layout, so it round-trips cleanly across BadgerDB tunings.

### The directory-lock constraint (read this first)

BadgerDB takes an exclusive **directory lock** while `aisotropyd` is running.
That means you **cannot** back up a live server's directory directly. You have
two honest options:

1. **Stop the server**, then back up its directory (simplest; incurs downtime).
2. **Cold copy**: while the server runs, copy `AISOTROPY_DATA_DIR` to a staging
   path (`cp -a`, `rsync`, or an LVM/filesystem/cloud-volume snapshot), then run
   the backup against the *copy* with `--bypass-lock`.

`--bypass-lock` skips the lock guard and is **only** safe on an offline or copied
directory. Never point it at the directory a running server holds — you can read
torn state.

### Backup

```bash
# Option 1 — server stopped:
aisotropy-backup badger backup \
  --data-dir "$AISOTROPY_DATA_DIR" \
  --out /backups/wm-$(date +%Y%m%dT%H%M%SZ).badgerbackup

# Option 2 — cold copy of a live directory:
cp -a "$AISOTROPY_DATA_DIR" /staging/wm-copy
aisotropy-backup badger backup --data-dir /staging/wm-copy --bypass-lock \
  --out /backups/wm-$(date +%Y%m%dT%H%M%SZ).badgerbackup
```

The tool opens the directory **read-only**, so it can never mutate the source.

### Encryption

If working memory was opened with at-rest encryption
(`AISOTROPY_DATA_ENCRYPTION_KEY` set, per `aisotropy/encryption.go` — hex or
base64, decoding to 16/24/32 bytes), you **must** export the same key when
backing up, or BadgerDB refuses to open the store:

```bash
export AISOTROPY_DATA_ENCRYPTION_KEY=<hex-or-base64 key>
aisotropy-backup badger backup --data-dir "$AISOTROPY_DATA_DIR" --out /backups/wm.badgerbackup
```

> **Warning:** the `.badgerbackup` file is written **decrypted** — `Backup`
> emits plaintext key/value pairs. Store it on encrypted media (LUKS/dm-crypt,
> cloud-EBS encryption) or wrap it (e.g. `age`, `gpg`) before it leaves the host.
> To keep the *restored* store encrypted, set the same key when restoring.

### Restore

Restore loads into a **fresh, empty** directory (the tool refuses a non-empty
target to avoid a half-merged store). Stop the server first.

```bash
aisotropy-backup badger restore \
  --in /backups/wm-20260717T000000Z.badgerbackup \
  --data-dir /var/lib/aisotropy/data-restored     # empty or non-existent
```

Then repoint `AISOTROPY_DATA_DIR` at the restored directory (or swap it into
place while the server is stopped) and start the server.

---

## 2. Facts — embedded SQLite

### Backup (online, consistent)

SQLite's `VACUUM INTO` takes a read transaction and writes a fresh, defragmented,
**self-contained** database file — safe while the source is open in WAL mode, and
without needing to carry the `-wal`/`-shm` sidecars. This is preferred over a raw
file copy of a live DB.

```bash
aisotropy-backup sqlite backup \
  --db "$AISOTROPY_DATA_DIR/facts.db" \
  --out /backups/facts-$(date +%Y%m%dT%H%M%SZ).db
```

`--out` must not already exist (SQLite refuses to overwrite with `VACUUM INTO`).

> **Multiple facts files:** with dedicated-tenant routing (ADR-0007), each
> dedicated tenant has its own `<resource>.db` under `AISOTROPY_DATA_DIR`. Back
> up each file — loop over the `*.db` files in the directory (excluding
> `bitrate.db`, which is the Bet-6 store, and any `-wal`/`-shm` sidecars).

**Cold alternative:** with the server stopped, a plain file copy of
`facts.db` (plus any `-wal`/`-shm`) is also valid. `VACUUM INTO` is the online,
sidecar-free path.

### Restore (file swap)

The server **must be stopped** for a facts restore — an open handle to the old
file makes the swap unsafe. The tool integrity-checks the snapshot
(`PRAGMA integrity_check`) before installing it, and removes any stale
`-wal`/`-shm` sidecars of the old target so a leftover WAL can't shadow the
restored data.

```bash
aisotropy-backup sqlite restore \
  --in /backups/facts-20260717T000000Z.db \
  --db "$AISOTROPY_DATA_DIR/facts.db" --force
```

### Networked facts — rqlite

When facts run on networked rqlite (`RQLITE_URL` set), use **rqlite's own**
hot-backup HTTP endpoint instead of the SQLite path — the file on any single
node is not authoritative for the Raft cluster:

```bash
# Consistent SQLite-format backup of the whole rqlite database:
curl -s "$RQLITE_URL/db/backup" -o /backups/facts-rqlite-$(date +%Y%m%dT%H%M%SZ).sqlite

# Restore by loading it back (rqlite replays it through the Raft log):
curl -s -XPOST "$RQLITE_URL/db/load" -H 'Content-Type: application/octet-stream' \
  --data-binary @/backups/facts-rqlite-20260717T000000Z.sqlite
```

(`/db/backup` streams a consistent SQLite file; `/db/load` restores it. This is
rqlite's native mechanism — `aisotropy-backup` does not wrap it because it is a
one-line `curl` and cluster-topology dependent.)

---

## 3. Semantic — Qdrant

### In-process default — nothing to back up

With no `QDRANT_HOST`, the semantic tier is the in-process cosine index. It has
**no disk footprint** and is not part of any backup. After a disaster it is
rebuilt by re-embedding the source corpus (facts / consolidated turns). See the
DR runbook's "Semantic reconstruction" section.

### Networked Qdrant — snapshot API

Qdrant provides a native snapshot REST API. The tool creates a snapshot and
downloads it in one command; restore uploads it back.

> **Port note:** the semantic tier talks **gRPC on 6334**, but the snapshot API
> is **REST on 6333**. The tool defaults `--port` to 6333 (override with
> `--port` or `$QDRANT_REST_PORT`).

```bash
# Create + download a collection snapshot:
aisotropy-backup qdrant snapshot \
  --host "$QDRANT_HOST" --collection aisotropy_knowledge \
  --api-key "$QDRANT_API_KEY" \
  --out /backups/qdrant-aisotropy_knowledge-$(date +%Y%m%dT%H%M%SZ).snapshot

# Restore (upload) into a collection, snapshot data taking priority:
aisotropy-backup qdrant restore \
  --host "$QDRANT_HOST" --collection aisotropy_knowledge \
  --api-key "$QDRANT_API_KEY" \
  --in /backups/qdrant-aisotropy_knowledge-20260717T000000Z.snapshot
```

Under the hood: `POST /collections/{c}/snapshots?wait=true` (create) →
`GET /collections/{c}/snapshots/{name}` (download) →
`POST /collections/{c}/snapshots/upload?priority=snapshot` (restore). The
`api-key` header is sent when `--api-key`/`$QDRANT_API_KEY` is set.

**Per-collection:** with dedicated-tenant routing each dedicated tenant is its
own Qdrant collection; snapshot each collection you need.

---

## Verification after restore

Always confirm a restore before declaring success — see the DR runbook's
"Verification after restore" section. Quick checks:

- **BadgerDB:** the tool's restore loads into a fresh dir; reopen it read-only
  and spot-check a known key, or just start the server against it and issue a
  chat that should hit an existing session summary.
- **SQLite facts:** the tool already runs `PRAGMA integrity_check` before
  installing. After swap, `SELECT count(*) FROM facts;` against the restored file
  should match the expected order of magnitude.
- **Qdrant:** `GET /collections/{c}` should report the expected `points_count`.

## What is tested

The `cmd/aisotropy-backup` package ships a round-trip test
(`roundtrip_test.go`) that seeds a BadgerDB directory and a facts-shaped SQLite
DB, backs each up, **wipes** the source, restores into a fresh location, and
asserts the data is intact (and that `integrity_check` passes for SQLite). The
BadgerDB and SQLite paths are therefore exercised end-to-end in CI. The Qdrant
and rqlite paths are **not** covered by an in-repo test because neither service
is provisioned in the test environment — they compile and `go vet` clean but are
validated against a live service manually (see the runbook's honest-gaps
section).
