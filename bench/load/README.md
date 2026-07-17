# `bench/load` — full-runtime multi-tenant load test

Prior benchmarks in this repo measured **footprint** (binary/RSS), **transports**, and
**brokers**. None of them drove the **whole runtime** under realistic concurrent
multi-tenant chat traffic, so capacity, contention, and the breaking point were unknown.
This harness closes that gap.

It is **self-contained** and owns only `bench/load/**`. It embeds the real public builder
`aisotropy.App` in-process (the same wiring `cmd/aisotropyd` boots), drives it over real
loopback HTTP with N distinct tenants issuing concurrent `/chat` and `/chat/stream` (SSE)
requests, and measures throughput, latency, streaming TTFT, memory-tier contention,
goroutine/heap growth, the quota gate, and cross-tenant isolation under load.

## What it boots

`bootApp` constructs `aisotropy.New(...)` with:

- **The keyless stub LLM** (`internal/llm.StubClient{Delay: 1ns}`). This is deliberate:
  the stub removes model round-trip latency, so every number here is **runtime overhead**
  (HTTP validation → identity→ctx → quota gate → memory hydration → working-memory write →
  SSE framing → cost metering), **not** model latency. A real provider adds its own latency
  on top.
- **`NoAuth`** — load/isolation are orthogonal to the JWT gate; opting out lets every `/v1`
  request reach a handler (the same choice `aisotropy/isolation_e2e_test.go` makes).
- The full in-process tier stack: **BadgerDB** working memory, **SQLite+FTS5** facts,
  **in-process cosine** semantic, the per-tenant **token-bucket quota gate**, the LLM
  **circuit breaker**, the **cost meter**, and the transactional **outbox → consolidation
  worker**.

Because the server runs **in this process**, the harness reads `runtime.NumGoroutine()` and
`runtime.MemStats` directly (see caveats on what that conflates).

Each worker reuses a **per-`(tenant,user)` session id**, so working-memory persistence *and*
hydration run on every turn (distinct working-memory buffers = `tenants × users`).

## How to run

```bash
# from the repo root
go run ./bench/load                 # full sweep, ~1 min, writes bench/load/results.txt
go run ./bench/load -quick          # shorter smoke (~20s), smaller sweep
go run ./bench/load -dur 8s         # longer sustained window per scenario
go run ./bench/load -maxconc 2048   # push the concurrency ramp higher
go run ./bench/load -out mine.txt   # custom results path (relative to cwd)
```

Flags: `-dur` (per-scenario duration, default 4s), `-users` (users/tenant, default 8),
`-maxconc` (ramp cap, default 1024), `-quick`, `-out`.

The harness starts **no containers** and never touches external services
(`aisotropy-rqlite-1`, `aisotropy-qdrant-1`, etc.). Every `App` is booted on an ephemeral
loopback port and cancelled before the process exits. Pure in-process Go, stdlib-only load
generator — **no third-party dependency added**, so no license gate is triggered.

## Scenarios

| # | Name | What it isolates |
|---|------|------------------|
| A | Tenant-cardinality sweep (N ∈ {10, 100, 1000}) | how session cardinality drives hydration cost |
| B | Concurrency ramp → breaking point (conc ∈ {16…1024}) | where p99/errors blow up under saturation |
| C | Pre-spend quota gate under saturation | 429-before-spend, per-tenant, no 5xx |
| D | BadgerDB in-mem vs on-disk (`AISOTROPY_DATA_DIR`) | cost of durable working-memory writes |
| E | Cross-tenant isolation under concurrent load | the isolation gate holds under concurrency |

## Headline numbers (12-core arm64, go1.26.5, stub model, 4s/scenario)

Full table in [`results.txt`](./results.txt). Summary:

- **Throughput is governed by per-session context depth, not tenant count.** At a fixed
  offered concurrency of 64, throughput *rose* with tenant cardinality — **220 req/s @
  N=10** (p99 ~2.0s) → **1,049 req/s @ N=100** (p99 ~0.37s) → **3,874 req/s @ N=1000**
  (p99 ~0.077s). Fewer tenants concentrate the request budget into fewer, **deeper**
  sessions; each turn then hydrates and (with the stub) echoes a larger growing context, so
  latency climbs. Spread the same load across 1000 tenants (8000 sessions) and sessions stay
  shallow → fast. **The runtime scales cleanly with tenant cardinality; the real cost driver
  is hydrated-context size per session.**
- **Breaking point ≈ CPU saturation, expressed as a latency cliff — not errors, not a tier
  failure.** At N=100, throughput plateaued at **~1,000 req/s across the entire ramp**
  (conc 16→1024) while p99 climbed **137ms → 403ms → 1.48s → 3.15s → 4.61s** and peak
  goroutines **488 → 7,368**. This is textbook saturation (Little's Law: at fixed
  throughput, latency grows linearly with concurrency). By the harness heuristic (p99 > 10×
  the lowest-concurrency p99) the **breaking point is conc=256** (p99 1.48s). **Zero errors
  and zero rejections even at conc=1024** — the runtime degrades by queueing latency, it
  does not fall over.
- **The quota gate holds hard and cheap.** Hammering one tenant with 64 concurrent writers
  under a 5/s (burst 5) bucket: **~275k requests in 4s, only 25 got 200** (≈ burst + refill),
  **~275k clean 429s, zero 5xx**, and a second un-hammered tenant still got 200 — confirming
  the bucket is **per-tenant** and rejects **before** any model spend.
- **Durable on-disk working memory imposes no throughput penalty at this scale.** Writer-heavy
  load (N=50, conc 64): in-memory Badger **1,219 req/s** vs on-disk Badger **1,281 req/s**
  (on-disk p99 was actually *lower*; the spread is session-depth variance, not a disk cost).
  The working-memory write path is **not** the bottleneck — the hydration/echo CPU is.
- **Isolation holds under concurrency.** 10 tenants seeded a distinct secret marker into a
  *deliberately shared* session id; 64 workers then streamed interleaved neutral reads.
  Every one of the ~290 responses carried **only its own** tenant's marker (positive control:
  hydration was live) and **never another** tenant's — **0 cross-tenant leaks**.

**Where it degrades first: the CPU-bound request pipeline (hydration + response framing),
surfacing as p99 latency growth.** The memory tiers (Badger on-disk = no penalty) and the
quota gate (cheap, correct) are *not* the first thing to give.

## Caveats (read these before quoting a number)

1. **Stub model = runtime overhead, not end-to-end latency.** Every latency here excludes
   provider time. Real p50/p99 in production = these numbers **+ model latency** (typically
   hundreds of ms to seconds), which would dominate and *lower* the effective req/s ceiling.
2. **Single box, in-process.** Server and load generator share one 12-core machine and the
   Go scheduler. The ~1,000 req/s deep-session / ~3,900 req/s shallow-session ceiling is this
   host's CPU limit for this workload, **not** an architectural limit. A dedicated server box
   and separate load box would report higher.
3. **The stub echoes the hydrated system prompt.** This makes sustained single-session
   conversations grow the stored working-memory value **quadratically**, and past ~64KB it
   exceeds **BadgerDB's default per-value cap** — appends then fail (logged ERROR, suppressed
   by the harness; the request still returns 200). A real bounded-output model would not do
   this. It does, however, surface a genuine edge: **working-memory values are not size-capped
   before the Badger write**, so a very long real conversation would eventually hit the same
   64KB wall. Worth a follow-up (truncate/summarize before persisting).
4. **`gobase`/`gopk`/`goset` interpretation.** `gobase` is server-only goroutines post-boot
   (~28). `gopk` (peak) includes the harness's own worker + client-transport goroutines, so it
   is **server + load-generator**, not server alone. `goset` (post-load settle) stays elevated
   roughly in proportion to concurrency because **HTTP keep-alive connections are still open**
   (one server read-goroutine per idle conn) at measurement time — they close at shutdown.
   This is normal server behavior, **not a leak**: `goset` does not grow unbounded across
   scenarios, and each scenario's `App` is fully torn down.
5. **Heap is whole-process** (`HeapAlloc`), harness + server, sampled every 50ms; treat it as
   an order-of-magnitude pressure signal (peaked ~270MB at conc=1024), not an exact server RSS.
6. **Numbers vary run to run** (±20-30% on latency tails) with scheduler/GC timing and,
   especially in Scenario A/D, with how deep sessions happened to grow in the window.
