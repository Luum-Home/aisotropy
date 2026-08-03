---
type: Concept
title: "Arena rate-limiting — surviving the agnes FREE-TIER 429 under concurrency"
description: >
  How the arena survives the agnes free-tier request-rate limit (429s) when the sweep runs under concurrency.
tags: [architecture, arena, rate-limiting, agnes, concurrency]
timestamp: 2026-08-03T00:00:00Z
---

# Arena rate-limiting — surviving the agnes FREE-TIER 429 under concurrency

## The problem this solves (code-truth, from a real run)

The live-arena sweep holds the model constant at `agnes-2.0-flash`. The agnes
account in use is **free-tier**, which enforces a hard request-rate limit. Under
`-concurrency 1` (fully sequential) a full sweep is clean; under `-concurrency
4` the same sweep produced **111 errors**, dominated by:

```
429 Too Many Requests
{"message":"You've reached the API rate limit for free users. Upgrade to a
Token Plan to unlock higher limits and continue using the API without
interruption.","type":"AgnesAI_error"}
```

plus downstream `502`s (aisotropyd's own upstream call to agnes 429s → it drains
the connection → the runner sees a 502 on `/chat`) and timeouts/deadlines. The
c=1 vs c=4 split is the tell: **sequential stays under the free-tier request
rate; four frameworks bursting their calls at once blow it.** This is not a
framework, adapter, or local-infra bug — it is the agnes free-tier rate limit.

The pre-existing retry-on-transient (`-retry`, default 1) *did* fire and *did*
classify the 429 as transient, but retry-once with a 1s backoff cannot clear a
**sustained** throttle window — the retry re-hits the still-open limit and burns
another billable 429.

## The two levers (both in `arenas/live/runner/main.go`)

### 1. Client-side global ADAPTIVE pacing — `-rps`

`-rps N` installs a process-**global** `rateLimiter` (a reservation-style token
bucket) that every billable model call passes through in `runAdapterRetry`
before spending. It is global because the resource it protects — the shared
agnes endpoint's rate limit — is itself global: all `-concurrency` framework
goroutines draw from one upstream quota, so one shared pacer is the correct
model (mirrors the existing process-global `retrySleep`).

**Why fixed pacing isn't enough.** A single `-rps` number is a *guess* at the
free-tier ceiling. Empirically `-rps 1.5` (fixed) still tripped the limit **3×**
in one run — it *reacts* (retry) instead of *avoiding*. To truly avoid crossing
an *undocumented* limit you must not need to know it. So the limiter self-tunes.

**Why plain AIMD isn't enough either.** The first cut used classic AIMD
(additive-increase / multiplicative-decrease — TCP's law). But AIMD's job is
throughput *maximization*: it deliberately **probes above** the limit to
discover capacity, producing the familiar sawtooth. Measured with `-rps 3`:
**~10 429s scattered evenly across the run**, each ridden out by retry but *none
avoided* — worse than the fixed run. Wrong objective. Our objective is
limit-*avoidance*.

**The controller is asymmetric with a LEARNED ceiling.** It backs off hard and
climbs back only up to a ceiling that *ratchets down* and never re-probes a
known-bad rate — so it **latches** onto a safe rate and converges to zero:

- **`observe(failed, rateLimited)`** feeds every call's outcome back:
  - a **429** → drop the **learned ceiling** to `0.75×` the rate that just broke
    (ratchets down only, floored at `minRate = rps/10 ≥ 0.1/s`), then **halve**
    the admitted rate. Global, so one 429 seen by *any* framework slows *all* of
    them — the coordination the shared limit needs.
  - a **non-429 failure** (502/timeout) → rate unchanged.
  - a **clean success** → extends a streak; every 8 in a row, rate steps up
    `+rps/8` but **capped by the learned ceiling**, never the hard `-rps` ceiling.
- It **starts at a quarter of the `-rps` ceiling** and climbs, approaching the
  limit **from well below** — minimal warm-up overshoot.
- `wait()` reserves the next slot at the *current* rate **under a mutex** but
  sleeps **outside** it, so N concurrent callers each get a distinct,
  monotonically increasing slot without serializing on the lock.
- `rps<=0` disables it (a nil / zero-rate limiter's `wait()`/`observe()` are
  immediate no-ops — exact old behavior, the default).

**Net effect:** plain AIMD sawtooths (re-climbs to the ceiling, re-breaks,
forever). The learned-ceiling controller eats **a few front-loaded 429s while it
discovers the breaking point, then stays below it for the rest of the run** —
converging to zero, which is the actual "avoid the limit" goal. `-rps` is an
**upper bound**, not a fixed rate that must be guessed right: set it generously
(e.g. `-rps 2`) and let the controller find the safe rate underneath.

### 2. Rate-limit-aware backoff — `retryBackoff(attempt, rateLimited)`

`isRateLimited(res)` separates the free-tier **429** (markers: `429`, `too many
requests`, `rate limit`) from generic transients (502/timeout/reset). The two
get different schedules:

| Class | Schedule | Cap |
|---|---|---|
| generic transient (502/timeout) | 1s, 2s, 4s, 8s | 8s |
| **rate-limited (429)** | **3s, 6s, 12s, 24s** | **30s** |

A 429 needs the longer schedule because the throttle window is
seconds-to-tens-of-seconds; a 1s retry just re-hits the open window. The attempt
count is clamped before the left-shift so a pathological `-retry` can't overflow
the duration into a negative/tiny value (the cap dominates well before then).

## Recommended recipe for a clean concurrent sweep

```bash
# parallel but ADAPTIVELY paced + patient — self-tunes under the free-tier
# limit, rides out any convergence-phase 429 with the long backoff
go run ./arenas/live/runner -concurrency 4 -rps 2 -retry 4
```

- `-concurrency 4` — parallel across frameworks (each framework internally
  sequential and race-free; see the merge-by-slot design in
  `runCorrectnessPass`).
- `-rps 2` — the pacing **upper bound** (fastest allowed). The controller starts
  at 0.5/s, and its learned ceiling ratchets down on any 429 to converge on
  whatever the free tier actually allows — so the number is a cap, not a guess
  that must be right.
- `-retry 4` — enough attempts that a call caught during the (short) convergence
  phase rides the 3s/6s/12s/24s backoff to success instead of failing the cell.

The RESULTS.md header records the `-rps` value so a paced run is never mistaken
for an unpaced one.

## Uniformity / fairness (same treatment for every framework)

Both levers live in the **runner**, not in any adapter — so they are applied
**identically to all frameworks** (aisotropy and every competitor). The
correctness/quality pass drives every framework through the *same*
`runWorkItem → runAdapterRetry` path, which consults the *same* process-global
`modelRateLimiter` and the *same* `-retry` policy. There is **no per-adapter
pacing or retry**: no framework is sped up, slowed down, or given extra retries
that another doesn't get. The limiter being global is also what makes the
back-off fair — a 429 provoked by any one framework throttles the whole field
equally, rather than penalizing the framework that happened to draw the slot.

(Caveat worth knowing: an adapter's *own* SDK may retry a 429 internally before
the runner ever sees it — that is outside the runner's control and differs by
framework. The runner's global pacer still bounds how fast calls leave the host,
but adapter-internal retries are a per-SDK behavior, not something the arena
normalizes.)

## Scope / non-goals

- The pacer gates the **correctness/quality pass** (the concurrent pass where
  the 429 storm occurred). The benchmark and new-dimension passes run
  **sequentially/uncontended by design** — one call at a time, ~10–25s each, an
  effective rate far below any free-tier limit (the same c=1 regime that was
  always clean) — so they cannot burst and are deliberately **not** paced;
  pacing them would only slow them and would also corrupt the p50/p95 latency
  measurement (`runAdapter` times only `cmd.Run`, but a retry would still hide a
  429 hole rather than measure it). This is uniform across frameworks too.
- The definitive fix for *high*-concurrency throughput is an agnes **Token
  Plan** (a higher upstream limit). `-rps` + long 429 backoff make the free-tier
  usable under moderate concurrency; they do not raise the underlying ceiling.
- Infra isolation (unique per-framework Qdrant collections, separate stores)
  addresses **local** store contention, a *secondary* contributor — it does
  **not** fix the agnes 429, which is the dominant cause of the c=4 error count.

## Tests

`arenas/live/runner/ratelimit_test.go` (all under `go test -race`):
- `TestRateLimiter_Disabled` — `-rps 0` and a nil receiver are no-ops (wait + observe).
- `TestRateLimiter_PacesSuccessiveCalls` — frozen-clock reservation spacing (starts at a quarter of the ceiling).
- `TestRateLimiter_AdaptiveBackoff` — learned ceiling: a 429 ratchets the ceiling down (0.75× the break) + halves the rate; a success streak re-climbs only to the learned ceiling (not the hard one); a non-429 failure leaves it alone; floors hold under many 429s.
- `TestRateLimiter_ConcurrentNoRace` — many concurrent waiters + observers, race-free.
- `TestRetryBackoff_Schedules` — both schedules + the overflow clamp.
- `TestIsRateLimited_Classification` — 429 vs generic-transient split.
- `TestRunAdapterRetry_RateLimitedUsesLongBackoff` — a persistent 429 rides the
  3s/6s schedule, not 1s/2s.
