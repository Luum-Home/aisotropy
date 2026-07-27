# ADR-0076: As-built deltas — parallel hydration, a shutdown join, and one delta kept on purpose

**Status:** Accepted (2026-07-27 — implementation landed with the ADR: `internal/api/chat_stream.go`,
`aisotropy/app.go`, `internal/api/hydration_parallel_test.go`, `aisotropy/shutdown_inflight_test.go`,
`manifests/capability-claims.yaml`. Every number below is real output from the command named beside
it; the one measurement that came out *against* the change is reported as such.)

**Delivers:** a verdict on the three remaining deltas between the aspirational
[request lifecycle](../architecture/request-lifecycle.md) and
[`runtime-as-built.md`](../architecture/runtime-as-built.md) — **two closed in code, one closed as a
documented decision.** Parallel memory hydration and the graceful-shutdown in-flight window are
implemented and pinned by tests that fail on the pre-change code. The streaming reflection loop is
**deliberately not built**, and the docs now carry the reason rather than the bare fact.

**Related:** [ADR-0074](0074-gap-claims-guard.md) (the claims registry both closed deltas update),
[ADR-0029](0029-followons-rs256-streaming-signals-judge.md) (the buffered output guardrail — the
precedent that prices the buffering trade-off), [ADR-0010](0010-gollem-adoption-scope-and-observability.md) (the
reflection engine itself), [ADR-0006](0006-reranker-cohere-vs-bge-local.md) /
[ADR-0019](0019-tenant-wide-document-sharing.md) (the semantic tier the hydration fans out to),
[`.claude/rules/constitutional-gates.md`](../../.claude/rules/constitutional-gates.md) (the isolation
gate parallel hydration had to clear),
[`.claude/rules/prune-evidence-gate.md`](../../.claude/rules/prune-evidence-gate.md) (code-truth over
doc-truth), [`gap-claims-verification-2026-07-27.md`](../06-Daily/reports/gap-claims-verification-2026-07-27.md)
(claims 7, 8, 10 — the re-verification these three deltas came from).

## Context

Three deltas were re-verified TRUE against the code on 2026-07-27. They were dispatched together,
with an explicit instruction *not* to force all three into implementations. That framing turned out
to matter: the three are not the same kind of thing.

| Delta | What the code said | What it turned out to be |
|---|---|---|
| Hydration is sequential | `hydrateSystemPrompt` had zero concurrency primitives | Real debt — closed |
| Reflection is non-streaming only | `chat_stream.go` has zero `Reflect` references | A **contract difference**, not debt — kept, documented |
| Shutdown has a best-effort window | `http.Server` never cancels in-flight request contexts | Real, cheap to close — closed |

## Decision 1 — Parallel memory hydration: IMPLEMENT

The three memory tiers are independent reads of one `(tenant, user, session)`: working memory
(`Summary` + `Recent`), facts FTS5 (`store.Search`), semantic RAG (`searchSemanticContext`). None
consumes another's output. They now run concurrently.

**Clearing the constitutional gate was the design constraint, not an afterthought.** Every goroutine
inherits the **same request `ctx`**, which carries exactly one tenant's identity injected once at the
Huma boundary. No goroutine derives, widens, or re-scopes it; none is handed a second tenant's
key/scope. This is intra-request concurrency over a single tenant, never cross-request fan-in — so
"no code path holds two tenants' data in one goroutine scope" holds by construction. Each goroutine
writes **only its own result slot** (disjoint variables, never a shared map or slice), so the join
needs no mutex and the block is race-free structurally rather than by lock discipline.

**The degradation contract is byte-identical, and that was the harder half.**
`hydration_degradation_test.go` is a contract, not a smoke test:

- a working-memory failure stays **FATAL**, with the same wrapped message
  (`read working-memory summary: …`);
- a facts failure stays **NON-FATAL** (logged, degrades to "no facts");
- a semantic failure degrades inside `searchSemanticContext`;
- **error precedence is preserved** — `Summary`'s error still wins over `Recent`'s, because the
  working-memory task still runs those two reads in that order and short-circuits.

Concurrency changed *when* the reads happen, never *what* a failure means. A new test
(`TestHydrateSystemPrompt_WorkingMemoryErrorStaysFatal`) pins the fatal half with a simultaneously
failing facts store, so precedence is asserted rather than assumed.

**Scheduling detail that came out of the measurements.** Only tiers actually wired contribute, and
the **last live tier runs inline** on the caller's goroutine — which would otherwise just block on
`Wait`. A lean wiring with a single live tier (`ask.go`'s semantic-only hydrator, the Bet 6 bitrate
scorer's working-memory-only one) therefore spawns **zero** goroutines and costs exactly what it did
before. The first cut spawned one goroutine per tier and was measurably worse; this is the fix.

### Measured — including the number that argues against the change

`TestHydrateSystemPrompt_TiersRunConcurrently` models the **networked** deployment the architecture
targets (rqlite over HTTP, Qdrant over gRPC) with per-scan latency. `searchSemanticContext` issues
two scoped scans, so facts (120 ms) and semantic (2 × 60 ms) are deliberately balanced:

```
GOMAXPROCS=2 nice -n 19 go test -run TestHydrateSystemPrompt_TiersRunConcurrently ./internal/api/ -count=1
  pre-change (git show HEAD:internal/api/chat_stream.go):  FAIL — hydration took 243.6ms, want < 180ms
  post-change:                                             PASS — 0.12s
```

**~2.0x, −124 ms of hydration latency.** That is the case for the change.

The case *against* it, reported with the same weight
(`BenchmarkHydrateSystemPrompt_InProcess`, paired runs, same machine load, `-benchtime 3000x -count=3`):

| Implementation | ns/op |
|---|---|
| sequential (HEAD) | 4425 / 4983 / 4116 |
| parallel (landed) | 7126 / 7512 / 8791 |

With the **bare-binary in-process defaults** (embedded BadgerDB, in-process cosine) every tier is a
few microseconds and the fan-out costs **more than it saves — roughly +3 µs per request**. This is a
real regression on the default path and it is recorded here rather than omitted.

**Why the trade is still right:** single-digit microseconds sit against a turn that spends >100 ms in
the model — ~0.005% of a turn — while the networked saving is ~124 ms of the user-visible
time-to-first-token. The inline-last-tier scheduling keeps single-tier wirings at exactly their old
cost, so the toll is paid only by deployments that actually have multiple tiers wired. Anyone
optimising the in-process bare binary for microseconds is measuring the wrong layer.

**Registry:** ADR-0074's `hydration-sequential` claim was **inverted, not deleted** — it is now
`hydration-parallel` with `expect: {op: ge, value: 2}`. A refactor that quietly re-serialises the
tiers must turn the guard red; deleting the claim would have made that regression invisible. The
`on_close` note records how to flip it back if parallel hydration is ever reverted.

## Decision 2 — Reflection loop on streaming: KEEP AS-IS (and say why)

**Rejected as a permanent, documented asymmetry — not carried as debt.**

The dispatch framed this as "once bytes are on the wire the 200 is committed; a retry cannot un-send
them." True, but it is the *second* reason, and stopping there implies the delta is a latency
trade-off waiting for someone brave enough to take it. The first reason is more basic:

> **The two routes have different response contracts.** `chat` returns a validated
> `chatStructuredReply` — `ReflectAuto[T]` has a `T` to enforce and a typing error to feed back on
> retry. `chat/stream`'s contract **IS** unstructured token deltas (`StreamTokenEvent{Token}`).
> There is no schema to violate, so there is nothing for a reflection loop to enforce and nothing to
> retry against.

Forcing the streaming route to emit JSON so a schema could exist would stream JSON fragments to
clients that want incremental prose — defeating the route's purpose to add enforcement it was never
contracted to provide.

**The buffering alternative is already priced in this repo, which is why we can reject it on
evidence rather than instinct.** ADR-0029 added exactly that mechanism for the *output guardrail*:
`AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` makes `chat/stream` buffer the entire response and hard-block
before the first byte. It is **opt-in and default-off**, precisely because it destroys
time-to-first-token — the property the streaming route exists to deliver. That decision established
the price of buffering for a check that *does* apply to raw text (toxicity, leaks). Reflection does
not even clear that bar: it does not apply to raw text at all.

**What was rejected, explicitly:**

- *Buffer-then-emit reflection* — inherits ADR-0029's TTFT cost for a check with no schema to check.
- *A bounded pre-flight* — a non-streaming validation call before streaming would double model spend
  per turn and violate "reject/degrade before spending tokens" in spirit: it spends *more* to gain a
  guarantee the contract never promised.
- *Silently leaving the docs saying only "streaming has no reflection"* — the failure mode this ADR
  exists to prevent. A bare fact reads as an unfinished TODO and invites a future session to
  "close the gap".

**The honest boundary:** callers that need schema guarantees must use `chat`, not `chat/stream`. That
sentence now appears in both `runtime-as-built.md` and `capabilities-and-gaps.md`. The
`reflection-loop-nonstreaming-only` claim stays green and its registry entry carries an `on_close`
note pointing any future author back at this rationale before flipping it.

## Decision 3 — Graceful-shutdown in-flight window: IMPLEMENT

`capabilities-and-gaps.md` described this as "the same deliberate best-effort posture documented for
workers … closing it fully would mean wiring request-context cancellation into shutdown." The second
half is correct and turned out to be roughly twenty lines.

`http.Server.Shutdown` waits for connections to go idle but **never cancels an in-flight request's
context**, and never tells a handler its grace period expired. A long-lived SSE stream is exactly the
handler that outlives the window: past the deadline `Shutdown` has returned but the handler is still
running, so its next working-memory write can land after `rt.close()` closed BadgerDB.

**The close, in three parts:**

1. **`http.Server.BaseContext`** roots every request context at a context this process owns. Without
   it, `net/http` roots them at `context.Background()` and nothing short of the client hanging up can
   cancel a handler mid-stream.
2. A `sync.WaitGroup` middleware registered **first** on the chi router (so it is the outermost
   wrapper, counting even short-circuited CORS preflights) tracks in-flight requests.
3. After the polite drain, `cancelInFlight()` then `waitWorkers(teardownCtx, &inFlightWG)` — reusing
   the *same* bounded-join helper the background workers already use — strictly before `rt.close`.

**The bug inside the fix, found by the test and worth recording.** The first cut passed the drain's
`shutdownCtx` to the join. But `server.Shutdown` blocks until that deadline, so on the pathological
path `shutdownCtx` was **already expired** when the join ran: `waitWorkers` returned instantly and
the stores closed under a live handler anyway. The test caught it (`Run returned while the in-flight
handler was still parked`). The fix is a **separate `teardownTimeout` budget**, not a remainder of
the drain's — *the case where the drain consumed its whole window is exactly the case that most needs
the join to have time*. The same reasoning applies to `rt.close`'s worker join and tracer flush,
which now also run on the fresh teardown budget instead of a possibly-exhausted one.

**Still bounded, deliberately.** A handler that ignores cancellation costs the teardown deadline, not
the process — the identical best-effort posture already documented for workers. `shutdownTimeout`
became a `var` so the test can shrink the grace period rather than sleep through it (the same pattern
`cmd/bitrate-harness` already uses). Honoring the full grace period for a live stream remains
correct behavior; what is under test is what happens when it expires with a handler still running.

## Consequences

- Hydration latency on networked deployments drops ~2x; the in-process default pays ~3 µs. Both
  numbers are in this ADR and in `runtime-as-built.md`.
- A shutdown can no longer close a store under a live handler within the teardown budget.
- The streaming/non-streaming reflection asymmetry is now a **stated contract difference** with a
  registry `on_close` guard, so it stops being re-discovered as a gap.
- The parallel-hydration claim is inverted rather than removed, so re-serialisation is caught.

### Verification

```
GOMAXPROCS=2 nice -n 19 go build ./...                                       # exit 0
bash scripts/with-heavy-lane.sh --need-mb 1024 -- env GOMAXPROCS=2 nice -n 19 \
  go test -race ./internal/api/ -count=1                                     # ok  24.632s
bash scripts/with-heavy-lane.sh --need-mb 1024 -- env GOMAXPROCS=2 nice -n 19 \
  go test -race ./aisotropy/   -count=1                                      # ok  48.139s
gofmt -l <touched files>                                                     # empty
GOMAXPROCS=2 nice -n 19 go vet ./internal/api/ ./aisotropy/                  # clean
bash scripts/gap-claims-guard.sh                                             # before: 14 ok / 2 drifted; after: 16 ok / 0 drifted
```

Both new tests were verified to **FAIL on the pre-change code** by temporarily restoring the file
from `HEAD` (`git show HEAD:<path>`), running the test, and restoring the working copy:

| Test | On pre-change code | On landed code |
|---|---|---|
| `TestHydrateSystemPrompt_TiersRunConcurrently` | FAIL — 243.6 ms > 180 ms budget | PASS — 0.12 s |
| `TestRun_ShutdownCancelsAndJoinsInFlightHandlers` | FAIL — "Run returned while the in-flight handler was still parked" | PASS |

**Honest boundaries.** The guard's before/after must not be read as "this change fixed four rows".
Before this work it reported `14 ok / 2 drifted`; after, `16 ok / 0 drifted`. **Only two of those
four deltas are ours** (`hydration-parallel` inverted, `reflection-loop-nonstreaming-only` unchanged
and still green). The two DRIFTED rows — `crossframework-benchmark-executed` and
`docker-footprint-benchmark-executed`, both arena-documentation drift, not runtime claims — were red
before this work started and were fixed **by a concurrent session** (`123b374`, `1fda25a`, `7b1dbf6`)
while it was in progress. They are credited here so the green verdict is not silently attributed to
this ADR. The
hydration benchmark measures a machine under concurrent load, so the absolute ns/op figures are
noisy; the *direction* is consistent across three paired runs each and that is all this ADR claims
from them. The shutdown fix bounds the join — it does not make teardown unconditional, and a handler
that ignores its context still loses its writes.
