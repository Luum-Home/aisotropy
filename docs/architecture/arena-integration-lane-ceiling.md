---
type: Concept
title: The integration lane measures plumbing, and 6/8 on memory is its ceiling
date: 2026-07-28
status: active — read before interpreting any integration-lane memory number
---

# 6/8 is a pass, not a failure

On the integration lane (`ARENA_STUB_MODEL=1`: **real stores, mock model**) the
`memory` dimension's best achievable score is **6 of 8**, for every framework.
A run that reports 6/8 is a run where everything measurable worked.

Anyone reading the raw number without this page will read a red ✗ and conclude
the framework's memory is broken. It is not.

## Why 6, and why the other 2 must fail

The eight `memory` items split into two kinds:

| kind | items | what answering requires |
|---|---|---|
| **pure recall** (6) | `mem-01-color`, `mem-02-pets`, `mem-h01-overwrite`, `mem-h02-interference`, `mem-h03-longhaul`, `mem-h04-multifact` | the fact was STATED in an earlier turn; answering means re-hydrating it from the store |
| **derived** (2) | `mem-03-deadline` (March 14 − March 1 = **13**), `mem-h05-derived` (1200 − 315 − 208 = **677**) | the answer appears NOWHERE in the conversation; it must be COMPUTED |

The mock model echoes the assembled prompt. That is exactly enough to prove a
fact came back out of the store — and structurally incapable of arithmetic.

So the two derived items failing is **the lane working correctly**. A mock that
"passed" them would be reporting a computation nobody performed.

## The trap this page exists to prevent

That is not hypothetical. It happened here on 2026-07-28, and the wrong number
was reported before the right one:

- five adapters scored **8/8** on the mock lane, derived items included;
- an echo cannot compute 677, so the number was impossible;
- the store held **four rows containing "677"** — written by a REAL model in
  earlier runs and persisted, because `memory`'s session id was fixed
  (`arena-mem-<framework>-<task>`, no run scope) and every run appended to the
  same rows;
- the echo replayed a stored answer and it scored as recall.

`mastra` scored 6/8 on identical code and was reported as the worst of the six.
It was the only correct one; its table simply lacked the contaminated rows.

Fixed by giving the base session the `runSuffix()` its own extension modes
already used (`buildMemory`, `tasks.go`). After the fix every adapter converges
on 6/8 — which is what a real ceiling looks like.

**The reading rule:** on this lane, a memory score ABOVE 6/8 is not a better
result, it is a bug report. Check the store for rows left by an earlier run.

## What the lane does and does not measure

MEASURES — everything the model is a passenger for:
- did the store persist a turn, and return it in a separate process;
- did a compound scope keep tenants apart;
- did state survive `kill -9`;
- did a leak surface where it should not.

DOES NOT MEASURE — anything requiring the model to think:
- arithmetic or any derivation (the 2 items above);
- answer quality, phrasing, faithfulness;
- refusal behaviour.

Those need the real lane. The split is the point: the plumbing dimensions were
paying for model calls that introduced error in BOTH directions — a real model
can fail to repeat a fact it WAS given (a false negative blaming the store) or
produce a plausible answer without one (a false positive crediting a store that
failed).

## Running it

```bash
ARENA_STUB_MODEL=1 arena-runner -live arenas/live -modes memory \
  -frameworks <fw> -concurrency 1 -results-out /tmp/val.md
```

`-results-out` keeps the published board untouched. `-concurrency 1` matters
more than it looks: four adapters hitting one rqlite simultaneously produced
HTTP 503s and timeouts that render as item ERRORS (`—`), not failures. Those are
infrastructure, not plumbing — distinguishing them is the same discipline the
service gate applies to DOWN vs INCONCLUSIVE.

## Related

- `docs/architecture/arena-authorship-lens-sweep.md` — the sweep that led here
- `arenas/live/runner/subject.go` — what each dimension measures
- `.claude/rules/arena-deterministic-judging.md` — why the primary score is pure
