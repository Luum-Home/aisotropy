# Arena benchmark methodology — how cold-start, latency, TTFT, RSS and tokens are actually measured

> **Why this doc exists:** `arenas/live/RESULTS.md` is **regenerated output** — the runner
> (`arenas/live/runner`) overwrites it on every run (`results.go:154`'s `os.WriteFile`). Its prose
> sections explain the numbers well enough to *read* a run, but as generated output they are not a
> stable place to *define* the methodology: the explanation is only as current as the last run and
> disappears from history the moment someone regenerates it differently. This document is the
> standing source — the way `arena-rate-limiting.md`, `arena-deterministic-judging.md` and
> `arena-qa-answer-key-scoring.md` are the standing sources for their topics. Every claim below cites
> the exact `file.go:line` that implements it; where the current `RESULTS.md` prose says something
> the code does not actually do, that is called out explicitly rather than silently copied forward.

## The five things measured, and where

All of it lives in `arenas/live/runner/bench.go`'s `runBenchmarks` (`bench.go:46-129`), invoked once
per run from `main.go:214` (`runBenchmarks(frameworks, benchModes, forms, tasks, rag, *benchIters,
*coldProbes, callBudget)`), unless `-skip-bench` is set. It iterates every `(framework, form)` pair
(`bench.go:50-51`) and, for each, measures — in this order — cold-start, TTFT, then warm p50/p95 per
bench mode. Every measurement is **runner-measured wall clock** (`Go`'s `time.Now()`/`time.Since()`),
never a value the adapter self-reports as "my latency" — the one exception being TTFT, which is
adapter-reported because only the adapter's own streaming loop can see the first-token event (see
below).

### 1. Cold-start — spawn-to-exit on an EMPTY request, zero model calls

`coldStart(script, n)` (`bench.go:136-155`) runs the adapter's `run.sh` (or `run-docker.sh`) `n`
times, feeding **empty stdin** (`bench.go:144`, `cmd.Stdin = bytes.NewReader(nil)`) and discarding
stdout/stderr. `n` is the `-cold-probes` flag, **default 3** (`main.go:51`). With no input to parse,
every adapter's contract loop reads stdin, gets immediate EOF, fails to parse a request, emits its
JSON error object, and exits non-zero — which the code treats as the *expected* outcome
(`bench.go:150`, `_ = cmd.Run() // non-zero exit is expected (parse failure)`). The reported value is
the **median** (p50) of the `n` probe wall times (`bench.go:154`, `percentile(walls, 50)`).

**What this isolates:** process/container spawn + interpreter or runtime boot + the adapter's own
env/import setup, down to the point where it starts reading stdin and immediately bails. **What it
does NOT isolate:** anything the model round-trip would cost — there is no model call in this path at
all (`bench.go:135`'s comment: "NO model round-trip"). It also does not capture a warm in-process
cache, a connection pool already primed, or any state a long-running server would carry between
requests — every probe is a fresh process from a cold OS-level start.

### 2. Warm latency — p50 / p95 over `-bench-iters` FULL adapter runs

For each bench mode (`-bench-modes`, default `"qa"`, `main.go:49`), `runBenchmarks` runs the adapter
`iters` times (`-bench-iters`, **default 5**, `main.go:50`) against one fixed representative request
per mode (`representativeInput`, `bench.go:159-188` — e.g. for `qa` it is the suite's first
`single_shot_qa` task; for `memory` it is a single persist-only invocation, `bench.go:170-173`, kept
deliberately single-shot "enough to time the mode's path" rather than the full persist+recall pair a
scored memory record uses). Each of the `iters` runs is a **complete adapter invocation including the
real model round-trip** (`bench.go:94`, `runAdapter(script, in, 180*time.Second)` — the same function
the correctness pass uses to make one billable call). Wall times are collected (`bench.go:100`) and
reduced to p50/p95 via nearest-rank `percentile()` (`bench.go:277-291`).

**The n=5 caveat, stated plainly:** the default `-bench-iters` is 5. `percentile()`'s rank formula is
`rank := (p * len(s)) / 100` (`bench.go:286`); for `p=95, len(s)=5` that is `475/100 = 4` (integer
division) — the **last index of a 5-element sorted slice, i.e. the maximum**. With n=5, **p95 IS the
max observed value**, not a statistical percentile in any meaningful sense. A single slow call (one
provider hiccup, one cold TCP connection, one GC pause on the adapter's runtime) becomes the entire
p95 figure for that cell. Raising `-bench-iters` narrows this, but the default run does not, and
`RESULTS.md`'s p95 column should be read with that in mind — it is closer to "worst observed of 5"
than to a true 95th-percentile estimate.

A run may also flag `-native-only` to skip the docker form entirely (`main.go:52,85-86`), or `-docker`
to run only docker (`main.go:53,87-88`); the two are mutually exclusive (`main.go:67-68`). When
`form == "docker"` and either the adapter has no `run-docker.sh` or the host has no working `docker`
daemon, that framework's `(framework, docker)` row is recorded as `Skipped` with a `Note` explaining
why (`bench.go:53-62`) — it never silently vanishes from the table.

### 3. TTFT — time-to-first-token, from ONE stream-mode run, with an honest `-1`/absent convention

`runBenchmarks` fires exactly one `stream`-mode call per `(framework, form)` (`bench.go:75-82`,
`representativeInput("stream", ...)` then a single `runAdapter`). The value stored is whatever the
adapter itself reported in its output's `ttft_ms` field (`bench.go:79`, `ttft = res.out.TTFTMs`; field
defined at `types.go:66`, `TTFTMs int64 \`json:"ttft_ms"\``) — the runner cannot observe a first-token
event inside another process's HTTP stream, so this is the one metric in this doc that is
**adapter-self-reported**, not runner-measured.

Adapters that do not support (or did not measure) TTFT report the sentinel `-1`, not `0` — see
`arenas/live/langgraph/modes/common.py:82,107` (`ttft_ms: int = -1` default, forwarded straight into
the output JSON) as one concrete example of the convention every adapter follows. This matters:
`0` would be indistinguishable from "measured and it was instant," silently making a non-streaming
framework look faster than a genuinely fast streaming one. The rendering layer enforces the same
honesty at read time two different ways:

- `msOrDash(v int64)` (`results.go:855-860`) renders any `v <= 0` — so both the `-1` sentinel and a
  buggy literal `0` — as `—`, never as `0`. This is what the benchmark table's TTFT column uses
  (`results.go:646`).
- The deterministic scorer `ScoreStream(ttftMs int64)` (`scoring/scoring.go:424-429`) treats any
  `ttftMs <= 0` as `Pass: false, Detail: "no positive TTFT reported"` — a framework that never
  reports a positive TTFT cannot pass the `stream` dimension's PRIMARY score by omission.

### 4. Peak RSS — one-shot child-process `getrusage`, native form only

`runAdapter` (`main.go:415-458`) captures `maxRSSKB(cmd.ProcessState)` (`main.go:435`) after every
run. `maxRSSKB` (`main.go:895-903` — see the function; it reads `ps.SysUsage().(*syscall.Rusage)` and
converts `Maxrss`, noting the kernel reports it in bytes on darwin and kilobytes on linux) is the
**peak** resident-set-size the OS ever recorded for that child process over its whole (short) life.
`runBenchmarks` keeps the maximum across the `iters` warm runs (`bench.go:101-103`,
`if res.maxRSSKB > peakRSS { peakRSS = res.maxRSSKB }`).

**Docker form has no RSS number** — `PeakRSSKB` is only ever populated from the native
`exec.Command`'s own `ProcessState`; a containerized run's `getrusage` reflects the `docker run`/CLI
wrapper process the runner spawned, not the containerized adapter's own memory, so the benchmark
table only renders this column meaningfully for `native` (`results.go:631,633`'s own comment:
"native form only"). Read every reported figure as **peak-not-idle**: it is a short-lived,
single-invocation process's memory ceiling during one request — interpreter/runtime startup, model
SDK import, one request/response cycle, then exit. It is **not comparable to a long-lived server's
steady-state RSS** (connection pools, warmed caches, JIT-compiled hot paths, and multiple in-flight
requests all push a real server's steady-state number in directions a one-shot CLI invocation never
reaches).

### 5. Token accounting — provider-reported `usage` only, summed per work item, `-1` excluded from sums

Token counts are **never estimated**. `runWorkItem` (`main.go:846-873`) sums `InputTokens`/
`OutputTokens` from each invocation's adapter output (`main.go:861-866`) only when
`res.out.InputTokens >= 0 && res.out.OutputTokens >= 0`; the moment any invocation in a work item
reports negative usage, `hasTok` flips to `false` (`main.go:865`, `else { hasTok = false }`) and the
whole item's `HasTok` is `false` (`types.go:133`'s comment: "true when every invocation reported
usage >= 0"). For a `memory` work item — which is genuinely two invocations, persist then a
fresh-process recall (`types.go:96-99`) — the sum spans **both** calls (`main.go:850-869`'s loop),
which is deliberately the "honest per-turn re-hydration cost" the token table's own header text
describes (`results.go:688-690`). Aggregation into the `RESULTS.md` token table only counts records
where `HasTok` is true (`results.go:93-97`, gated on `if r.HasTok`); a `-1`/unavailable run is
**excluded from the sum entirely**, never coerced to `0` (which would silently understate the total).

## Cold overhead — cold minus warm p50, floored at zero, and why it is usually zero here

The "Cold overhead" column in `RESULTS.md`'s benchmark table is computed once per bench cell as
`overhead := c.ColdMs - c.WarmP50; if overhead < 0 { overhead = 0 }` (`results.go:617-618`). It
answers "how much of the warm p50 is process/runtime spin-up, versus everything else (the model
round-trip, adapter logic)?" In this arena the answer is consistently **~0**: cold-start is typically
tens to low hundreds of ms (process spawn + import), while a warm run's p50 includes a real network
round-trip to `agnes-2.0-flash`, typically several **seconds**. The model call dominates by one to two
orders of magnitude, so `ColdMs - WarmP50` is negative for nearly every cell and gets floored to `0`.
A non-zero cold overhead would only appear for a framework whose per-call model round-trip is
unusually fast AND whose process spin-up is unusually slow relative to it — not the common case here.

## How to read this — and what this does NOT measure

Read every number in `RESULTS.md`'s benchmark section against these limits before drawing a
cross-framework conclusion from it:

- **Small n.** `-bench-iters` defaults to 5 and `-cold-probes` defaults to 3 (`main.go:50-51`). As
  shown above, p95 over 5 points is literally the maximum — a single provider hiccup dominates it.
  This is a real statistical limitation of the default run, not a rendering artifact.
- **Single host, single run, no warm-up discard.** All timings come from one process on one machine
  during one invocation of the runner; there is no discarded warm-up call before the timed iterations
  begin, so the first of the `iters` runs (which may pay a cold TCP/TLS handshake the rest reuse) is
  scored on equal footing with the rest.
- **Provider variance across the run window is real and visible in the data, not hypothesized.** Each
  aisotropy adapter mode call carries its own internal HTTP timeout of `110 * time.Second` against
  its upstream (e.g. `arenas/live/aisotropy/mode_qa.go:35`, `mode_stream.go:43`, `mode_rag.go:101`,
  `mode_structured.go:113`, `mode_tools.go:157`, `mode_memory.go:44`, `mode_middleware.go:160`,
  `mode_observability.go:337`, `mode_scoped.go:41` — all `110*time.Second`, well under the runner's
  own 180s per-invocation timeout in `bench.go:76,94` and `main.go:851`). The current `RESULTS.md`'s
  per-item detail table shows multiple recorded latencies clustered in the 100,000–106,000ms range
  across *several different frameworks* — e.g. `ai-sdk`'s `stream/qa-01-capital` at 100,858ms wall
  with a TTFT of 100,680ms (i.e. the first streamed token itself took ~100 seconds), `genkit-go`'s
  `memory` recall at 104,061ms, `langgraph`'s `structured` at 104,295ms, and `aisotropy`'s own
  `structured` item at 106,244ms. That clustering just under the 110s ceiling is the signature of a
  **degraded-provider window** (the shared `agnes-2.0-flash` upstream running unusually slow for a
  stretch of the run) inflating whichever cells happened to execute during it — not a per-framework
  property, and not reproducible on demand.
- **That variance is not distributed evenly, and the run order makes it a per-framework confound.**
  The runner executes the whole correctness pass framework-by-framework in alphabetical order, and
  only *then* runs the entire bench pass, and only then the dimensions. So two frameworks' correctness
  cells are not sampled from the same moment of provider health: in the 2026-07-20 run, every
  retry-exhaustion and 110s timeout fell on `ai-sdk`, `aisotropy` and `eino` (the first three
  alphabetically, executed while the upstream was degraded), while `genkit-go` onward ran clean after
  it recovered — and the bench pass, running last, was measured entirely under recovered conditions.
  The practical consequence: **a difference in the correctness pass's "Avg latency" column between an
  early-alphabet and a late-alphabet framework may be nothing but the hours between them.** Read that
  column within a framework, never across; for cross-framework latency use the bench p50/p95, which at
  least samples all eight within one short window. A fix would be to interleave the frameworks (round-
  robin by item instead of grouping by framework) so provider drift hits every framework equally
  instead of penalizing whoever runs first — not yet implemented.
- **Native-only unless both forms were actually run.** A `-native-only` or `-docker` run, or an
  adapter with no `run-docker.sh`, or a host with no working `docker` daemon, all produce a `Skipped`
  row for the missing form (`bench.go:53-62`) rather than a fabricated number — but that also means a
  given `RESULTS.md` may simply be missing docker-form data for some or all frameworks; check the
  `Form` column and the `Note` before comparing "native vs docker" across frameworks.
- **Peak RSS is one-shot, native-only, and not comparable to a live server's steady state** (see §4
  above) — do not read it as "this framework's server footprint."
- **The correctness/quality pass's "Avg latency" column is NOT the same measurement as the bench p50
  column, and the two must not be read as interchangeable.** Concretely, verified in the code:
  - The correctness pass defaults to `-concurrency 4` (`main.go:55`, `flag.Int("concurrency", 4, ...)`),
    i.e. **up to 4 frameworks' correctness sweeps run at once** unless a run explicitly passes
    `-concurrency 1`. `results.go:243-245` renders this exact caveat inline in `RESULTS.md` when
    `Concurrency > 1`: per-item latency in that pass "reflects mild host contention — a directional
    signal, not a clean isolated p50," and names the benchmark pass as the actual p50/p95 source of
    truth. Only when a run *was* executed with `-concurrency 1` does `results.go:252` state the
    correctness-pass latency is "uncontended."
  - Separately — and this is the part worth verifying rather than assuming — **the client-side rate
    limiter's wait is NOT included in the correctness pass's measured `WallMs`.** `runAdapterRetry`
    calls `modelRateLimiter.wait()` (`main.go:755`) **before** calling `runAdapter`; `runAdapter`'s
    own wall-clock measurement starts only at `main.go:431` (`start := time.Now()`), strictly inside
    the call it makes, after the limiter's sleep has already returned. So a `-rps`-paced run's queuing
    delay is excluded from every record's `WallMs` by construction — the limiter slows down how fast
    calls *leave the host*, but that queuing time never leaks into the number `RESULTS.md` reports as
    "Avg latency." (`arena-rate-limiting.md`'s own scope section makes the same point about the bench
    pass: "`runAdapter` times only `cmd.Run`, but a retry would still hide a 429 hole rather than
    measure it" — consistent with what the code shows here.)
  - What remains genuinely non-comparable between the two passes, independent of the limiter: (a) the
    bench pass repeats **one fixed representative request** `iters` times (`bench.go:159-188`), while
    the correctness pass averages over **many different real tasks** of varying size and complexity
    (`results.go:91-92` sums `r.WallMs` across every record for that `(framework, mode)`); (b) for a
    multi-invocation work item (memory's persist+recall), the correctness pass's `WallMs` captures
    only the **final** invocation (`types.go:130`'s comment: "wall clock of the final (scored)
    invocation"; set at `main.go:868`), not the summed round-trip time; and (c) when concurrency *is*
    greater than 1, simultaneous framework goroutines really do contend for host CPU, network sockets
    and disk, which inflates each one's measured wall time even though the rate-limiter's own sleep is
    excluded from it.
- **Docker build time is only reported when the image was built during that run** (`results.go:661,
  675`) — a blank/`—` Build-time cell means the image was already present and reused, not
  that the build was instant.

## How to read the correctness board — empty parity vs. real signal

The correctness table needs its own reading guide, because its most common misreading is taking a
perfect score as evidence of quality. Two concepts govern it.

### Empty parity: qa, structured, and stream are smoke tests, not quality signals

Every framework in this arena drives the **same model** (`agnes-2.0-flash`, held constant by
design). In `qa` mode the task is "What is the capital of France?" — the framework's entire job is
carrying a prompt to the model and carrying the answer back. That is plumbing. The model, identical
for all nine, is the one that knows it is Paris.

Picture nine mail carriers delivering **the same letter to the same address**: all nine deliver it.
You have learned nothing about which carrier is better — only that none of them lost the envelope.
That is why every adapter scores 1.0 on these modes, and a tie that nobody *can* lose is not
information. We call it **empty parity**: the column cannot rank anyone, because the thing it
exercises is not the thing that differs.

It compounds: `structured` and `stream` reuse the **identical qa task list**
(`arenas/live/runner/tasks.go` — the `"qa", "structured", "stream"` case shares one
`single_shot_qa` slice), differing only in what they measure about it (valid JSON? did it stream?).
So three of the six correctness columns are the same trivial test measured three ways.

They are kept anyway, on purpose: a smoke test that catches gross breakage is worth having. When an
adapter is miswired, qa fails loudly and immediately — that is exactly how a stale `SPEC.md` bug
that broke seven adapters at once was surfaced (see
`docs/architecture/arena-adapter-artifacts.md`, instance 1). The rendered board therefore labels
these rows `[smoke test, not a quality signal]` (`arenas/live/runner/results.go`,
`writeQASmokeTestNote`), and the modes where frameworks genuinely separate — because the framework
has work of its own to do — are `rag` (retrieve the right document), `tools` (execute and chain
calls), and `memory` (recall across turns).

The empirical proof that this distinction matters: before the suite was redesigned to discriminate,
47 of 48 framework × mode cells sat at a perfect 1.0 (`arenas/live/runner/suite_test.go`) — a
board that measured its own author, not its subjects.

### Same checkmark, different guarantee: the isolation rows

The multi-tenant isolation dimensions (`memory_scoped`, `isolation_leak`) have the opposite
subtlety: everyone passes, but the ✓ does not mean the same thing in every row.

For aisotropy, isolation is a property of the **product, by construction**: `tenant_id` travels
only via `context.Context` from the handler down, storage keys carry a mandatory `(tenant, user)`
prefix, and Qdrant filters by tenant **during** the HNSW scan (an in-scan payload filter — a
post-filter would itself be a leak; see `.claude/rules/constitutional-gates.md`). A consumer of
aisotropy cannot easily forget isolation, because it is the only code path that exists.

For the seven competitor frameworks, the framework has **no concept of a tenant**. The isolation
that passes this test was written in each ARENA ADAPTER — compound keys hand-concatenated into
store keys, one collection per scope, by convention. It passes because that adapter code is
careful, not because the framework guarantees anything. A real team adopting one of those
frameworks writes and maintains that same code themselves, and any mistake is a cross-tenant leak
with no framework backstop.

Eight lockers, all locked when tested. One is a safe whose design does not open without the right
key; seven are boxes that currently have a padlock someone remembered to put on. Today's test
cannot tell those apart — both render ✓. The difference only appears when the person assembling
the system makes a mistake, which is precisely the case a test of correct code never exercises.
The board's own "Native vs hand-rolled" caveat (rendered above the scoped-memory table,
`results.go`) records this; this section is the longer form of why it matters when comparing.

## Cross-links

- `docs/architecture/arena-rate-limiting.md` — the `-rps` adaptive pacer and retry-backoff schedules
  that gate the correctness/quality pass (and explicitly do NOT gate the benchmark pass).
- `docs/architecture/arena-deterministic-judging.md` — why the PRIMARY quality score is a pure
  function (`arenas/live/runner/scoring`), never an LLM judge; the benchmark numbers this doc covers
  are a separate, complementary signal (latency/footprint/tokens, not correctness).
- `docs/architecture/arena-qa-answer-key-scoring.md` — the deterministic QA/memory correctness scorer
  (unrelated to timing, but scored on the same records this doc's `WallMs`/token fields live on).
- `arenas/live/FULL-MATRIX.md`, `arenas/live/SPEC.md` — the full framework × dimension coverage matrix
  and the arena's overall contract, of which the benchmark pass is one automatic, model-call-bounded
  part.
