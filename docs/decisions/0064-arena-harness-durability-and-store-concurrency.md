---
type: ADR
title: "ADR-0064 — Arena harness durability (records.jsonl + resume) and store concurrency (TEI, rqlite queued-writes, Qdrant client tuning)"
description: The concurrency-3 full board errored ~21 memory/isolation cells to shared-store timeouts (single-worker embed, Raft-serialized rqlite strong-writes, 5s-default Qdrant gRPC client), and its atomic write-at-the-very-end lost all in-flight work on any interruption. This ADR records the industry-standard fixes — a durable records.jsonl event log with a derived RESULTS.md render and resume-by-construction, HuggingFace TEI (Apache-2.0) for the embed server, rqlite Queued Writes + ensure-table-once, and Qdrant client timeout/pool/persistent-connection tuning — and phases their implementation.
tags: [decisions, adr, arena, live-arena, harness, durability, concurrency, embeddings, tei, rqlite, qdrant, checkpointing]
timestamp: 2026-07-23T00:00:00Z
supersedes: []
revisits: []
---

# ADR-0064 — Arena harness durability + store concurrency

## Status

**✅ Accepted (2026-07-23). Phased implementation.** Layer 1 (durable `records.jsonl` +
derived render + resume) is the highest-stability-impact change and lands first; layers 2–4
(rqlite queued-writes, Qdrant client tuning, embed→TEI) are each bounded harness changes that
land in sequence. **Nothing here touches the aisotropy product core except the rqlite facts-tier
optimisation (layer 2), which is gated by the existing multi-tenant isolation constitutional
gate.** All implementation is deferred until the in-flight concurrency-2 board completes — the
shared embed/rqlite/Qdrant services and data dirs it is actively using must not be restarted or
reconfigured mid-run.

## Context (the evidence, code- and log-truth)

The `arenas/live` full board is a `framework × mode × lane × form` sweep against the real
agnes-2.0-flash model. Two distinct failure classes surfaced on the 2026-07-23 runs.

### C1 — shared local stores saturate under concurrency (21 contaminated cells)

A concurrency-3 full board errored **~21 of the memory-extension / isolation cells** with
store-timeout signatures, while the identical cells passed **2/2 in a sequential calibration**.
This is an **infra artifact, not framework quality** — publishing "llamaindex 0/2 memory" when
it was really a store timeout would be a strawman (violates `.claude/rules/capability-absence-claims.md`).
Log-truth from the run:

- `memory: ensure table: rqlite POST …?level=strong: context deadline exceeded` (×14)
- `Qdrant …6334: connection error … i/o timeout` (×3)
- generic `TimeoutError: timed out` (×16, the node/JVM adapters)

Root causes, one per store (each corroborated by the vendor's own docs — see Research):

1. **Embed server — single worker.** `arenas/live/_shared/start-embed-server.sh` ran
   `uvicorn app:app` with **one worker**. e5 encode is CPU-bound and holds the GIL, so one
   worker **serialises every embed** across all 9 adapters. The 30-message `semantic_recall`
   persists queued behind one another, exhausted each adapter's per-turn budget, and the
   exhaustion **cascaded**: the aisotropy adapter embeds *then* writes rqlite in one turn, so a
   slow embed left no budget for the rqlite call → its "context deadline exceeded". This is why
   the *slow* adapters (node/JVM/python) errored and the fast ones scored — it was queueing
   latency, not capability.
2. **rqlite — Raft-serialised strong writes.** Every write on the leader uses the same SQLite
   connection and the Raft log serialises all writes with an `fsync()` per write. Our adapters
   ran `ensure table … level=strong` on **every** invocation — the worst case. Under
   concurrency the strong writes queued past the client timeout.
3. **Qdrant — client defaults, not the server.** Qdrant is Rust/async and multiplexes hundreds
   of RPCs over one HTTP/2 connection; it was never the real bottleneck. The Python/gRPC client
   defaults are **5s timeout** and **pool size 3** — the `i/o timeout`s were half embed-cascade
   and half that 5s default under load.

**Interim mitigation already shipped (commit `a9a7261`):** embed server → `--workers 4`
(verified: 8 concurrent `/embeddings` all 200 in <1s warm, was 4/8 timing out at 15s), board
dropped to `-concurrency 2`, and the docker-bench `${VAR:-default}` tag bug fixed. This is a
**band-aid** — it reduces contention rather than making the stores genuinely concurrent. This
ADR records the durable fixes.

### C2 — atomic write-at-the-very-end (the instability *feeling*)

The runner holds every record in memory across all three passes (correctness ~9h → bench →
dimensions) and calls `writeResults` **once, at the very end** (`main.go` builds `reportData`
then a single `os.WriteFile`). Consequences observed repeatedly this session:

- **No checkpoint.** Any interruption (matrix-gate exit, a deliberate kill to fix a stale note,
  machine/session churn) discards *all* in-flight work — a 7h board leaves nothing.
- **No cheap re-render.** There is no persisted record stream, so RESULTS.md cannot be
  regenerated (e.g. to fill matrix cells or merge clean rows) without **re-spending the whole
  board** — the exact bind that forced `-skip-matrix-gate` producing-runs.
- **Reads as "unstable".** Long paced runs with a single terminal write feel like a hang even
  when progressing; a run that persisted partial results would feel stable at the same speed.

## Decision

Four layers. Layer 1 is the structural fix for C2 and lands first; layers 2–4 are the
per-store durable fixes for C1.

### Layer 1 — durable `records.jsonl` event log + derived render + resume (fixes C2)

Adopt the **event-log + derived-view** pattern (the same shape rqlite/Turso/TEI use internally):

- **Append-on-complete.** Each correctness/bench/dimension record is appended to
  `arenas/live/RESULTS.records.jsonl` (one JSON object per line) **the moment its cell
  finishes** — durable, crash-survivable, line-atomic.
- **RESULTS.md becomes a pure render** of the accumulated records — a separate `render` step
  with **zero model calls**. `writeResults` reads `records.jsonl` (+ the matrix.json manifests)
  and renders; it no longer depends on holding everything in memory.
- **Resume by construction.** On start the runner reads any existing `records.jsonl` and
  **skips cells already present** (keyed by `framework|mode|lane|form|task-id`). A killed board
  resumes where it stopped; the kills/relaunches this session suffered would have been free.
- **Kills `-skip-matrix-gate` as a workaround.** With a durable record stream, matrix.json cells
  are filled from the **real** records and RESULTS.md is re-rendered for free — the gate can run
  normally on the completed manifests, no producing-run bypass needed.

Contract notes: the record schema is versioned (`v` field) so a schema change invalidates a
stale resume; `Date.now()`-style non-determinism stays out of the render (the deterministic
scoring purity gate, `.claude/rules/arena-deterministic-judging.md`, is unaffected — the render
is pure over the records).

### Layer 2 — rqlite Queued Writes + ensure-table-once + weak reads (fixes C1.2)

- **Queued Writes** (`/db/execute?queue`): batch writes, return on enqueue. rqlite documents
  ~**15× write throughput** with no client-logic change. Trade-off: a small data-loss window on
  node crash before the queue flushes — acceptable for the arena (re-runnable) and gated behind
  an env flag for the product facts-tier.
- **`ensure table` once at boot**, not per-invocation — the per-turn idempotent `CREATE TABLE …
  level=strong` was pure waste under contention.
- **Reads at `level=weak`/`none`** where linearizability is not required (arena recall reads).

This layer touches the aisotropy **product** facts-tier (`internal/facts`), so it lands as a
proper change under the multi-tenant isolation constitutional gate (never widen a tenant scope;
queued writes must preserve the `tenant_id`/`user_id` key prefix), with a test.

### Layer 3 — Qdrant client: 30s timeout + persistent client + larger pool (fixes C1.3)

Config-only, no server change: raise the gRPC client timeout 5s→30s, **reuse one persistent
gRPC client** at process start (HTTP/2 multiplexes hundreds of in-flight RPCs over one socket),
and raise `pool_size` above the default 3. Applies to `internal/semantic` (product) and any
adapter that constructs its own Qdrant client per call.

### Layer 4 — embed server → HuggingFace TEI (fixes C1.1, the root cause)

Replace the single-process FastAPI e5 server behind `:8899` with **HuggingFace Text Embeddings
Inference (TEI)** — Rust, **token-based dynamic batching**, documented ~**10× throughput**,
OpenAI-compatible `/embeddings`. **License: Apache-2.0** (verified against the repo `LICENSE`,
"Apache License Version 2.0", Copyright 2022 Hugging Face) — clean under the repo's
100%-MIT/Apache constitutional gate, **no exception ADR needed**. (Note: this is *TEI*,
text-*embeddings*-inference; the HFOIL source-available saga was *TGI*, text-*generation*-inference.)

**Not a pure drop-in — one integration caveat (must not be silently skipped):** TEI does **not**
honour our current `model: "…-query"` / `"…-passage"` suffix convention that
`arenas/live/_shared/embed-server/app.py` uses to prepend the e5 instruction prefixes
(`query:` / `passage:`). e5 degrades badly without the correct prefix. TEI's mechanism is
`--default-prompt-name` (one prompt per instance) or a per-request `prompt_name` referencing the
sentence-transformers `prompts` dict. Options, to be decided at implementation:
(a) callers send TEI-native `prompt_name` per request; (b) callers prepend the prefix
client-side before calling `/embeddings`; (c) two TEI instances (query-default, passage-default).
**Whichever is chosen, an embedding-parity check against the current server is a gate** before
TEI becomes the arena default — a wrong prefix corrupts every vector silently.

## Consequences

- **Stability, not just speed.** Layer 1 makes a slow run *feel* stable (partial results,
  crash-survivable, resumable) and removes the re-render/re-spend trap that shaped this whole
  session. It is the 80% fix for the "everything is unstable" perception.
- **Genuine concurrency.** Layers 2–4 make the stores handle concurrent load by construction
  (TEI batching, rqlite queued writes, Qdrant multiplexing) — so the board can go back to
  `-concurrency 3+` without contamination, instead of being throttled to 2 as a band-aid.
- **Harness-scoped, product-safe.** Only layer 2 reaches product code, under an existing gate.
- **Evidence discipline preserved.** The deterministic scoring purity/reproducibility gate is
  untouched (the render is pure); the no-blank-cells matrix law is *better* served (cells filled
  from real records, no bypass).

### Implementation order (each validated before the next)

1. `records.jsonl` + derived render + resume — runner-only, testable offline.
2. rqlite queued-writes + ensure-once — product facts-tier, with an isolation-preserving test.
3. Qdrant client timeout/pool/persistent — config, near-free.
4. embed → TEI — largest surface; behind an embedding-parity gate.

All four are deferred until the in-flight board completes (they touch the shared services /
data dirs it is using). The current board's results are salvaged via the existing
matrix-fill-from-RESULTS.md path, since it ran on the pre-layer-1 binary and produced no
`records.jsonl`.

## Research (industry corroboration — we are not the first to hit this)

- HuggingFace TEI — Rust embedding server, token dynamic batching, ~10× throughput, Apache-2.0:
  <https://github.com/huggingface/text-embeddings-inference>,
  <https://huggingface.co/docs/text-embeddings-inference/index>
- TEI e5 prompt handling (`--default-prompt-name` / per-request prompt):
  <https://huggingface.co/docs/text-embeddings-inference/index>
- rqlite Queued Writes (~15×) + Raft/fsync write-serialisation:
  <https://rqlite.io/docs/api/queued-writes/>, <https://rqlite.io/docs/guides/performance/>
- Qdrant gRPC client (5s default timeout, pool 3, persistent-client / HTTP/2 multiplexing):
  <https://markaicode.com/errors/qdrant-timeout-error-fix/>,
  <https://medium.com/@fazlehasan110/from-rest-to-grpc-the-one-line-change-that-unlocks-5x-concurrent-users-on-qdrant-f218927e89f0>

---

## Implementation log — layers 2 and 3 (2026-07-27)

### Layer 2 — rqlite: implemented, but NOT as specified

The ADR proposed **Queued Writes** (`/db/execute?queue`) for a ~15× write throughput gain. On
inspection that is the wrong instrument for the failure we actually observed. The 14
`ensure table: context deadline exceeded` errors did not come from a high-rate write workload that
needed batching — they came from **schema migration on process boot**. `RqliteStore.migrate`
issues each of the 9 canonical schema statements as its own request (deliberately: rqlite runs one
request in one transaction, and the idempotent `ALTER TABLE ADD COLUMN` must be allowed to fail
per-statement). Every one of those is a Raft-committed, fsync'd write. A long-lived server pays it
once; the **live arena boots a fresh aisotropyd per adapter invocation**, so a board paid it
hundreds of times against a shared node.

Implemented instead: a **sentinel fast path**. One weak-consistency READ (`SELECT 1 FROM
sqlite_master WHERE type='trigger' AND name='facts_au'`) answers "is the schema already here?".
The sentinel is the LAST statement's object and `schemaStatements` is applied in order, so its
presence implies every prior statement succeeded. Boot cost on an already-migrated store drops
from **9 Raft writes to 1 query**. It **fails open**: any error checking the sentinel falls through
to the full idempotent migration, so the check can never become a new failure mode.

Queued Writes remains available as a future option for a genuinely write-heavy path (the outbox),
where its data-loss window would need its own analysis. It is not needed for this defect.

Evidence (`internal/facts/rqlite_migrate_fastpath_test.go`, deterministic, offline):
already-migrated store → **0 writes**; fresh store → **9 statements** (the fast path cannot skip a
migration that has not happened); failing sentinel query → **9 statements and a successful boot**.

### Layer 3 — Qdrant: NOT APPLICABLE as written (verified, not implemented)

The ADR prescribed "raise the gRPC client timeout 5s→30s, reuse one persistent gRPC client, raise
`pool_size` above the default 3". Checked against the client this repo actually uses —
`github.com/qdrant/go-client` v1.18.3, not the Python client the source material described:

- **"Reuse one persistent client" — already done.** `OpenQdrant` constructs one `*qdrant.Client`
  and stores it on `QdrantStore{client: ...}` (`internal/semantic/qdrant.go`); every operation
  reuses it. Nothing to change.
- **"Timeout 5s→30s" — no such field.** `qdrant.Config` exposes `Host`, `Port`, `APIKey`,
  `UseTLS`, `TLSConfig`, `GrpcOptions` — and no timeout. The 5-second default is a **Python
  client** behavior; the Go client takes its deadlines from the caller's `context.Context`, which
  our call sites already pass.
- **"`pool_size` above 3" — a Python-client concept.** gRPC-Go multiplexes many in-flight RPCs
  over one HTTP/2 connection; there is no pool to size.

The observed `error reading server preface: read tcp …: i/o timeout` was therefore not a client
misconfiguration but a **dial under an overloaded server** — the same saturation the embed-worker
fix addressed at its root (contention errors went from 30+ to 1 on the following board). No Go-side
change is warranted; `GrpcOptions` remains the escape hatch if connection-level tuning is ever
needed.

This entry is recorded rather than silently dropped because the prescription came from a real
source that described a **different implementation**. Porting a recommendation across languages
without checking the target's API is the same failure class this repo's other corrections document:
a claim about code that was never verified against the code.
