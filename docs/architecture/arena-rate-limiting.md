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
free-tier ceiling. Empirically `-rps 1.5` still tripped the limit **3×** in one
run — it *reacts* to the limit (retry) instead of *avoiding* it. To truly avoid
crossing the limit you must not need to know it, because it's undocumented and
can shift. So the limiter is **adaptive (AIMD — additive-increase /
multiplicative-decrease)**, the same control law TCP uses to find a link's
capacity without being told it:

- **`observe(failed, rateLimited)`** feeds every call's outcome back:
  - a **429** → the admitted rate is **halved** (down to a floor `minRate =
    rps/10`, ≥0.1/s). Because the limiter is global, one 429 seen by *any*
    framework slows down *all* of them — the coordination the shared limit needs.
  - a **non-429 failure** (502/timeout) → rate unchanged (a downstream error is
    no reason to speed up).
  - a **clean success** → extends a streak; every 8 in a row, the rate steps up
    additively (`+rps/8`) toward the ceiling.
- It **starts at half the `-rps` ceiling** and climbs, so it approaches the real
  limit **from below** — minimizing the warm-up overshoot a start-at-ceiling
  policy would eat before it learns.
- `wait()` reserves the next slot at the *current* adaptive rate **under a
  mutex** but sleeps **outside** it, so N concurrent callers each get a distinct,
  monotonically increasing slot without serializing on the lock while they wait.
- `rps<=0` disables it entirely (a nil / zero-rate limiter's `wait()`/`observe()`
  are immediate no-ops — exact old behavior, the default).

**Net effect vs. fixed pacing:** a fixed 1.5/s keeps tripping the limit all run
long (it never learns → 3 scattered 429s). The adaptive limiter eats at most a
couple of 429s while converging, then **backs off and stays under the ceiling
for the rest of the run** — approaching zero 429s in steady state, which is the
actual "avoid the limit" goal. `-rps` is now a **ceiling** (the fastest it may
ever go), not a fixed rate: set it generously (e.g. `-rps 3`) and let AIMD find
the safe rate underneath.

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
go run ./arenas/live/runner -concurrency 4 -rps 3 -retry 4
```

- `-concurrency 4` — parallel across frameworks (each framework internally
  sequential and race-free; see the merge-by-slot design in
  `runCorrectnessPass`).
- `-rps 3` — the pacing **ceiling** (fastest allowed). The adaptive limiter
  starts at 1.5/s and backs off on any 429, converging to whatever the free tier
  actually allows — so the exact number is a cap, not a guess that must be right.
- `-retry 4` — enough attempts that a call caught during convergence (or a
  closing throttle window) rides the 3s/6s/12s/24s backoff to success instead of
  failing the cell.

The RESULTS.md header records the `-rps` value so a paced run is never mistaken
for an unpaced one.

## Scope / non-goals

- The pacer gates the **correctness/quality pass** (the concurrent pass where
  the 429 storm occurred). The benchmark and new-dimension passes run
  sequentially/uncontended by design, so they do not burst and are not paced —
  pacing them would only slow them without preventing a real problem.
- The definitive fix for *high*-concurrency throughput is an agnes **Token
  Plan** (a higher upstream limit). `-rps` + long 429 backoff make the free-tier
  usable under moderate concurrency; they do not raise the underlying ceiling.
- Infra isolation (unique per-framework Qdrant collections, separate stores)
  addresses **local** store contention, a *secondary* contributor — it does
  **not** fix the agnes 429, which is the dominant cause of the c=4 error count.

## Tests

`arenas/live/runner/ratelimit_test.go` (all under `go test -race`):
- `TestRateLimiter_Disabled` — `-rps 0` and a nil receiver are no-ops (wait + observe).
- `TestRateLimiter_PacesSuccessiveCalls` — frozen-clock reservation spacing (starts at half the ceiling).
- `TestRateLimiter_AdaptiveBackoff` — AIMD: 429 halves the rate (floored), a success streak steps it up (capped), a non-429 failure leaves it alone.
- `TestRateLimiter_ConcurrentNoRace` — many concurrent waiters + observers, race-free.
- `TestRetryBackoff_Schedules` — both schedules + the overflow clamp.
- `TestIsRateLimited_Classification` — 429 vs generic-transient split.
- `TestRunAdapterRetry_RateLimitedUsesLongBackoff` — a persistent 429 rides the
  3s/6s schedule, not 1s/2s.
