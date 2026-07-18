# Arena Performance Lever 2: Per-Framework Infra Isolation

Status: design + PoC (proven). Not yet rolled out to the full 8-framework sweep.
Date: 2026-07-18

## Problem (confirmed by prior sweep evidence)

Commit `1af6a19` ran the correctness/quality pass with `-concurrency 4`: up to 4
framework adapters execute in parallel, each a fresh subprocess of its
`run.sh`/`run-docker.sh`. Commit `92e5949` re-ran the identical sweep at
`-concurrency 1` and documented the result directly in the commit message:

> Replaces the -concurrency 4 run (whose scattered errors were cross-framework
> contention on shared local rqlite/Qdrant/e5 + agnes latency variance, NOT
> framework signal)... errors collapsed from ~100+ (c=4) to 18 (c=1)... aisotropy
> went 23 errors (c=4) -> 0 errors (c=1) across ALL 6 modes — confirming its c=4
> errors were 100% concurrency artifacts, not an adapter bug.

So the operator already has a clean before/after: parallelism buys speed but
currently costs correctness. This document investigates why, and proves a fix
is mechanically simple.

## Root cause, traced to source

### 1. The runner has no per-worker isolation concept

`arenas/live/runner/main.go`:

- `runCorrectnessPass` (line ~206) bounds parallelism with a semaphore sized to
  `-concurrency`, one goroutine per **framework** (not per task). Each
  goroutine calls `runAdapter`/`execStdin` for its framework's full modes×items
  sequence.
- `runAdapter` (line ~347) and `execStdin` (line ~403) both build the adapter
  subprocess with:
  ```go
  cmd := exec.CommandContext(ctx, "bash", script)
  ...
  cmd.Env = os.Environ()
  ```
  `os.Environ()` is the **exact same environment** for every framework, every
  goroutine, every slot. There is no concept of "which of the N concurrent
  slots is this" threaded into the adapter invocation at all — no slot index,
  no per-framework env override.

### 2. Every adapter's default wiring points at the same three services

Grepping all 8 `run.sh`/`run-docker.sh` scripts confirms every adapter falls
back to the same hosts when the runner doesn't override anything:

| Service | Var(s) | Default (native) | Default (docker) |
|---|---|---|---|
| facts (rqlite) | `RQLITE_URL` | `http://localhost:4001` | `http://host.docker.internal:4001` |
| semantic (Qdrant) | `QDRANT_URL` (6 adapters) or `QDRANT_HOST`+`QDRANT_PORT`+`QDRANT_REST_URL` (aisotropy) | `http://localhost:6333` / `localhost:6334` | `http://host.docker.internal:6333` |
| embeddings (e5) | `EMBED_URL` (6 adapters) or `AISOTROPY_EMBED_URL` (aisotropy) | `http://127.0.0.1:8899` | `http://host.docker.internal:8899` |

These are the `aisotropy-rqlite-1` / `aisotropy-qdrant-1` containers from the
repo-root `docker-compose.yml` plus the standalone `_shared/start-embed-server.sh`
process — one shared instance of each, reused by all 8 frameworks regardless of
concurrency slot.

### 3. Two independent contention mechanisms, not one

- **Data collision (Qdrant):** grepping collection names shows 6 of 7
  non-aisotropy adapters (`ai-sdk`, `eino`, `llamaindex`, `mastra`,
  `pydantic-ai`, `langgraph`) default `QDRANT_COLLECTION`/`ARENA_COLLECTION` to
  the **literal same string `arena_rag`**. Under `-concurrency 4` these
  frameworks can be writing/querying the identical collection simultaneously —
  a genuine data race, not just a performance one. Only `aisotropy` uses a
  distinct name (`aisotropy_arena`), which is why its concurrency-4 errors were
  pure resource contention rather than cross-framework data corruption.
- **Resource contention (rqlite, e5):** rqlite is single-Raft-leader — all
  writes serialize through one node regardless of which framework issued them.
  Four frameworks hammering the same rqlite/e5 process concurrently under the
  bounded per-request timeout used by `runAdapter`'s `context.WithTimeout`
  produces exactly the kind of "scattered, non-deterministic" errors the
  commit message describes — a resource-starvation pattern, not a logic bug in
  any adapter.

Both mechanisms point at the same fix: **give each concurrency slot its own
store instances**, which removes data collision by construction (distinct
databases, not just distinct table names) and removes resource contention
(distinct Raft/HNSW instances, not shared ones).

## Options considered

### (a) Per-slot docker-compose stack on offset ports — RECOMMENDED

One rqlite + one Qdrant per concurrency slot, each on `base_port + slot*offset`
(e.g. rqlite `4001+i`, Qdrant REST/gRPC `6333+2i`/`6334+2i`, embed
`8899+i`). The runner computes the slot index inside its existing semaphore
loop and injects the corresponding `RQLITE_URL`/`QDRANT_HOST`/`QDRANT_PORT`/
`QDRANT_REST_URL`/`EMBED_URL`/`AISOTROPY_EMBED_URL` into that goroutine's
`cmd.Env` (appended after `os.Environ()` so slot vars win).

**Pros**
- Structural isolation — not a convention any adapter can accidentally break.
  Fixes BOTH the data-collision and the resource-contention problems in one
  move, for all 8 adapters uniformly (no adapter-specific code change needed
  beyond the env vars they already read).
- Proven today: rqlite and Qdrant both boot in ~1-2s per container from a
  cached image; this PoC brought up 2 full slots (4 containers) and confirmed
  isolation in well under 10s total.
- Matches an existing, already-used escape hatch in the adapters
  (`DOCKER_RQLITE_URL`/`DOCKER_QDRANT_HOST` overrides, `?disableClusterDiscovery=true`
  for cross-network rqlite access) — the wiring surface already exists, this
  just parametrizes it by slot instead of hardcoding one shared target.
- The embed server (e5) is a stateless pure function (text -> vector); it does
  NOT need per-slot isolation for correctness, only headroom under load. It can
  stay shared, OR get replicated per-slot purely for throughput — this is a
  tunable, not a correctness requirement (see the "what does NOT need
  isolating" note below).

**Cons**
- Boot cost scales linearly with concurrency: `-concurrency 4` needs 4 rqlite +
  4 Qdrant containers running simultaneously (~230MB Qdrant + ~40MB rqlite
  resident each, see cost estimate below).
- More moving parts for the runner to manage: it must wait for each slot's
  containers to be ready before dispatching that slot's first framework, and
  must tear them down on completion/failure without leaking containers.
- Slightly diverges the arena's docker-form adapters from the reference
  `docker-compose.yml` topology used in production docs (single shared
  rqlite/Qdrant) — needs a clear "this is arena-only infra" boundary, matching
  how `arena-poc-*` naming was used in this PoC.

### (b) Ephemeral in-process stores per adapter

Aisotropy's bare-binary default already runs in-process (in-memory cosine
semantic tier, in-process SQLite+FTS5 facts tier) with zero external
dependency — the ARENA_STUB=1 path proves this works today
(`clearBackendTierEnv` in `arenas/live/aisotropy/app.go`). If every adapter had
an equivalent in-process fallback, isolation would be free (each subprocess
gets its own memory space by construction, no shared service at all).

**Pros:** zero boot cost, zero extra containers, perfect isolation for free.

**Cons — this is why it is NOT the recommendation:** it is not actually
available for the 7 competitor frameworks today. `langgraph`/`llamaindex`/
`pydantic-ai`/`mastra`/`ai-sdk`/`genkit-go`/`eino` are all wired directly at
real Qdrant/rqlite via their own client SDKs for the `rag`/`memory` modes —
none of them ship (or were adapted to use) an in-memory vector store /
in-memory SQL store swap. Building that swap per-framework is exactly the kind
of per-adapter, framework-specific code change Option (a) avoids, and it
would also stop being an apples-to-apples comparison: the whole point of the
`rag`/`memory` dimensions is to exercise each framework's REAL retrieval path
against REAL Qdrant/rqlite, matching how aisotropy is tested. Swapping
competitors to a fake/mock store for concurrency's sake would undermine the
arena's own honesty premise. This option is only free for the one framework
that already has it; it is a rewrite project for the other seven.

### (c) Namespacing within shared stores (collections/DBs per framework)

Give each framework its own Qdrant collection name and (where possible) rqlite
table prefix, but keep a single shared rqlite/Qdrant instance across all
concurrency slots.

**Pros:** cheapest to implement — mostly config (`QDRANT_COLLECTION=arena_rag_<fw>`
passed into each adapter's env), no new containers, no boot-time cost at all.
Directly fixes the concrete data collision found in this investigation (6
adapters sharing the literal `arena_rag` collection name).

**Cons:** does NOT fix the resource-contention half of the problem. rqlite's
single-Raft-leader serialization and Qdrant's single-process HNSW index are
still shared across all `-concurrency N` slots — under load this still
produces exactly the "scattered errors, not framework signal" symptom the
`92e5949` commit documented, just without the (worse) risk of one framework
reading another's vectors. It is a correct, cheap, complementary fix for the
collision half of the problem, but insufficient alone to make `-concurrency 4+`
clean.

## Recommendation

**Adopt (a) per-slot docker-compose stack, layered with (c) as a defense in
depth.** Concretely:

1. Give every concurrent slot its own rqlite + Qdrant container pair on offset
   ports (this PoC proves the mechanism end-to-end).
2. ALSO fix the `QDRANT_COLLECTION`/`ARENA_COLLECTION` default so no two
   frameworks share a literal collection name, independent of slot (already
   worth doing regardless of (a), since two frameworks can land in the SAME
   slot across different runner phases/dimensions, e.g. the correctness pass
   vs. the new-dimensions pass).
3. Leave the embed server shared (option b's argument applies narrowly here:
   it is genuinely stateless, so sharing it is correctness-safe) unless
   profiling later shows it as a throughput bottleneck, at which point
   replicating it per-slot is a one-line addition to the same mechanism.

Option (b) — true in-process stores for every framework — is the "ideal
long-term" outcome (matches aisotropy's own bare-binary design point) but is a
multi-adapter rewrite, not a lever-2 fix; it is out of scope for this PoC and
noted as a longer-horizon follow-up, not a near-term recommendation.

## How this wires into the runner's `-concurrency` slots

`runCorrectnessPass` (`arenas/live/runner/main.go`) already has the exact
insertion point — the semaphore-guarded goroutine body:

```go
for i, fw := range frameworks {
    wg.Add(1)
    sem <- struct{}{}           // <- slot acquired here; NOT the same as a fixed slot index
    go func(i int, fw adapter) {
        defer wg.Done()
        defer func() { <-sem }()
        ...
        rec := runWorkItem(fw, it, budget)   // <- eventually calls runAdapter/execStdin
```

Two implementation details the runner change must get right (flagged here so
the follow-up implementation doesn't have to re-derive them):

- **The semaphore does not currently hand back which of the N slots was
  acquired** — `sem <- struct{}{}` just blocks until there is room; there is no
  slot *index* available at the acquire point, only "a slot is now free."
  Giving each concurrent worker a **stable, numbered** slot (not just a
  counted one) requires switching the semaphore to a small pool of slot
  tokens (e.g. `chan int` pre-loaded with `0..concurrency-1`) so the goroutine
  can `slot := <-slotPool` / `defer func() { slotPool <- slot }()` and use
  `slot` to compute its ports.
- **The env injection point is `runAdapter`/`execStdin`**, which both do
  `cmd.Env = os.Environ()`. That line needs to become `cmd.Env = append(os.Environ(), slotEnv(slot)...)`
  where `slotEnv` returns the `RQLITE_URL`/`QDRANT_HOST`/`QDRANT_PORT`/
  `QDRANT_REST_URL`/`EMBED_URL`/`AISOTROPY_EMBED_URL` overrides for that slot
  — appended last so they win over any pre-set value in `os.Environ()`.
- The runner needs a pre-flight step (before `runCorrectnessPass`) that brings
  up `concurrency` slot stacks and a matching teardown after `wg.Wait()` —
  this PoC's docker commands are the template for that step; production
  wiring would move them into a `docker-compose -p arena-slot-<i>` per-slot
  project (compose already supports `-p`/`COMPOSE_PROJECT_NAME` for exactly
  this kind of ephemeral parallel stack) rather than raw `docker run`, so
  teardown is a single `docker compose -p arena-slot-<i> down` per slot.

## PoC: proof the mechanism works (docker + local only, zero billable calls)

All commands run against ad hoc containers uniquely named `arena-poc-*`,
never touching the operator's existing `aisotropy-rqlite-1` /
`aisotropy-qdrant-1`. No `ANTHROPIC_API_KEY`/`OPENAI_API_KEY` calls were made —
verification used `GET /health` (a constitutionally token-free, read-only
readiness probe per `internal/health`), not any LLM-calling arena mode.

### 1. Booted two fully isolated slot pairs

```
docker run -d --name arena-poc-rqlite-slot1 -p 14001:4001 rqlite/rqlite:8.36.3 \
  -http-addr 0.0.0.0:4001 -raft-addr 0.0.0.0:4002
docker run -d --name arena-poc-rqlite-slot2 -p 14011:4001 rqlite/rqlite:8.36.3 \
  -http-addr 0.0.0.0:4001 -raft-addr 0.0.0.0:4002
docker run -d --name arena-poc-qdrant-slot1 -p 16333:6333 -p 16334:6334 qdrant/qdrant:v1.12.4
docker run -d --name arena-poc-qdrant-slot2 -p 16343:6333 -p 16344:6334 qdrant/qdrant:v1.12.4
```

Health, both slots green within 1 polling attempt:

```
rqlite slot1 /readyz -> [+]node ok [+]leader ok [+]store ok
rqlite slot2 /readyz -> [+]node ok [+]leader ok [+]store ok
qdrant  slot1 /       -> {"title":"qdrant...","version":"1.12.4",...}
qdrant  slot2 /       -> {"title":"qdrant...","version":"1.12.4",...}
```

### 2. Proved store-level data isolation directly

Wrote a distinct marker row into each rqlite slot and read it back — each slot
returns only its own value:

```
rqlite slot1: INSERT poc_marker(slot)="slot1-only"  -> SELECT returns "slot1-only"
rqlite slot2: INSERT poc_marker(slot)="slot2-only"  -> SELECT returns "slot2-only"
```

Created a collection on Qdrant slot1 only:

```
qdrant slot1 collections -> ["poc_slot1_only"]
qdrant slot2 collections -> []   (absent, as expected)
```

### 3. Proved the SAME env-var mechanism `run.sh` uses actually routes a real
   binary to the correct slot, with zero LLM calls

Built `cmd/aisotropyd` (the project's real runtime — same `RQLITE_URL`/
`QDRANT_HOST`/`QDRANT_PORT` contract the arena adapters use) and ran two
instances, each pointed at a different slot:

```
# instance A -> slot1
AISOTROPY_ADDR=127.0.0.1:18081 AISOTROPY_ALLOW_NOAUTH=1 \
RQLITE_URL="http://localhost:14001/?disableClusterDiscovery=true" \
QDRANT_HOST=localhost QDRANT_PORT=16334 QDRANT_COLLECTION=aisotropy_poc_slot1 \
./aisotropyd-poc

# instance B -> slot2
AISOTROPY_ADDR=127.0.0.1:18082 AISOTROPY_ALLOW_NOAUTH=1 \
RQLITE_URL="http://localhost:14011/?disableClusterDiscovery=true" \
QDRANT_HOST=localhost QDRANT_PORT=16344 QDRANT_COLLECTION=aisotropy_poc_slot2 \
./aisotropyd-poc
```

Both came up clean and both readiness-probed OK against their OWN slot:

```
instance A /health -> {"status":"ok","checks":[{"name":"facts","status":"ok","latency_ms":4},
                                                 {"name":"semantic","status":"ok","latency_ms":7}]}
instance B /health -> {"status":"ok","checks":[{"name":"facts","status":"ok","latency_ms":3},
                                                 {"name":"semantic","status":"ok","latency_ms":3}]}
```

Then the decisive cross-check: each instance's own collection landed ONLY in
its own slot's Qdrant, never the other's:

```
qdrant slot1 collections -> [poc_slot1_only, aisotropy_poc_slot1]
qdrant slot2 collections -> [aisotropy_poc_slot2]                (slot1's collections absent)
```

One gotcha surfaced and resolved during the PoC: rqlite's Go client
auto-discovers cluster peers by the address the node advertises internally
(the Docker container ID/hostname), which is unreachable from the host — this
produced `dial tcp: lookup <container-id>: no such host` until
`?disableClusterDiscovery=true` was added to `RQLITE_URL`. This is not a new
problem: the arena's own `run-docker.sh` scripts already carry this exact
query param for the analogous host-to-container boundary, so the production
runner change inherits a known-good pattern rather than a new one.

### Cleanup

All 4 PoC containers and both `aisotropyd` PoC processes were stopped/removed
by their unique `arena-poc-*` names / PIDs. Verified after cleanup:

```
$ docker ps --format '{{.Names}}\t{{.Status}}' | grep aisotropy
aisotropy-rqlite-1   Up 4 days (healthy)
aisotropy-qdrant-1   Up 4 days
```

The operator's pre-existing containers were never touched (not in the removal
command's argument list, never referenced by any command other than this
verification).

## Estimated per-slot boot cost

Measured/derived from this PoC and the images' published sizes:

| Resource | Per slot | At `-concurrency 4` (4 slots) |
|---|---|---|
| Boot time (rqlite + Qdrant, cold image cached) | ~1-2s to healthy (`/readyz` passed on first 1s poll in this PoC) | Parallelizable — bring up all N slots concurrently, bounded by the slowest one (~2s), not N×2s |
| Qdrant resident memory (idle, empty collections) | ~150-230MB (Rust binary + gRPC/REST servers; consistent with the single shared instance's known footprint) | ~600MB-920MB for 4 slots |
| rqlite resident memory (idle, single node, no cluster) | ~30-50MB (small Go binary + embedded SQLite) | ~120-200MB for 4 slots |
| Disk (empty volumes) | Low tens of MB per store until data is written; this PoC used ephemeral (no volume) containers, so zero persistent disk | Same, ×4 |
| Docker container count added | 2 (1 rqlite + 1 Qdrant) | 8 (vs. today's 2 shared) |
| e5 embed server | Not slot-scoped in the recommendation (stateless, shared) — $0 additional | Same |

Net: at `-concurrency 4`, the isolated topology adds roughly **6 extra
containers and under 1.2GB of resident memory** versus today's 2 shared
containers — cheap relative to a laptop/CI runner's available headroom, and
far cheaper than re-running the sweep at `-concurrency 1` (the current
workaround), which trades away 4x wall-clock speed to buy back correctness
that per-slot isolation gets for ~1GB of RAM instead.

## What does NOT need isolating (an explicit non-goal, to bound scope)

- **The e5 embed server** is a pure stateless function (text -> vector); two
  concurrent frameworks embedding different text through it cannot corrupt
  each other's results — it only becomes a *throughput* concern under heavy
  load, not a *correctness* concern. Do not spend the per-slot-replication
  budget here unless profiling shows contention.
- **The judge client** (agnes `/chat/completions`, `arenas/live/runner/eval.go`)
  is already a single external HTTP call per scoring request with its own
  `callCounter` mutex in the runner process — it is not a locally-shared
  store and is out of scope for this lever.

## Uncertainties (trust-report honesty)

- This PoC used ephemeral (no-volume) containers and empty stores; the real
  sweep pre-loads corpus data via ingestion before the correctness pass runs
  (`AISOTROPY_ARENA_CORPUS_DIR` etc.) — per-slot ingestion cost (re-embedding /
  re-indexing the corpus once per slot instead of once total) was NOT measured
  here and could be non-trivial if the corpus is large; this is the main open
  question before a full rollout.
- I did not verify the actual behavior of the 7 non-aisotropy adapters' Qdrant
  clients under the collision scenario (e.g., whether `arena_rag` being shared
  produces silently wrong retrieval results or explicit errors) — the
  `QDRANT_COLLECTION` collision was confirmed by source grep, not by
  reproducing a live collision.
- Auth (rqlite `-auth`, Qdrant `QDRANT__SERVICE__API_KEY`) was deliberately
  skipped in this PoC for simplicity; a production per-slot compose stack
  should decide whether per-slot auth is needed (arena-only, local, ephemeral
  infra makes a stronger case for skipping it than the reference
  `docker-compose.yml` does).
- I have not load-tested whether 4-8 concurrent Qdrant+rqlite pairs fit
  comfortably alongside the operator's other running containers
  (`luum-woocommerce-*` stack was also running during this PoC) — the memory
  estimate above is derived from published image characteristics, not a
  peak-load measurement under this PoC.

## Verification commands run (for reproducibility)

```bash
docker run -d --name arena-poc-rqlite-slot1 -p 14001:4001 rqlite/rqlite:8.36.3 -http-addr 0.0.0.0:4001 -raft-addr 0.0.0.0:4002
docker run -d --name arena-poc-rqlite-slot2 -p 14011:4001 rqlite/rqlite:8.36.3 -http-addr 0.0.0.0:4001 -raft-addr 0.0.0.0:4002
docker run -d --name arena-poc-qdrant-slot1 -p 16333:6333 -p 16334:6334 qdrant/qdrant:v1.12.4
docker run -d --name arena-poc-qdrant-slot2 -p 16343:6333 -p 16344:6334 qdrant/qdrant:v1.12.4
curl -s http://localhost:14001/readyz && curl -s http://localhost:14011/readyz
curl -s http://localhost:16333/ && curl -s http://localhost:16343/
go build -o aisotropyd-poc ./cmd/aisotropyd
# (env-scoped runs per slot — see §3 above)
docker rm -f arena-poc-rqlite-slot1 arena-poc-rqlite-slot2 arena-poc-qdrant-slot1 arena-poc-qdrant-slot2
```
