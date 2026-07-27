# Model profiles — what the board was measured WITH

A board is a measurement of **(framework × model × workload)**. Naming the model
is not enough: `poolside/laguna-s-2.1` tells a reader which model ran, and
nothing about whether that model was *built for the workload this arena
measures*. That gap is not hypothetical.

> **The mistake this directory prevents.** On 2026-07-27 the arena ran a full
> board on `poolside/laguna-s-2.1` and produced a table that looked ordinary. It
> was not. Laguna S 2.1 is a **coding-specialised** open-weight model — its
> published results are Terminal-Bench, SWE-Bench Multilingual and DeepSWE — and
> this arena measures a **general assistant**: QA, retrieval, memory, tool
> orchestration, structured output, safeguards. Worse, Laguna's headline numbers
> are reported *with thinking enabled* (Terminal-Bench 70.2 with, 60.4 without;
> DeepSWE 40.4 with, 16.5 without), and the arena calls plain
> `chat/completions` with no thinking parameter at all. Several results were
> nearly written up as provider or framework weaknesses before anyone checked
> what the model actually is.
>
> A header that names a model without naming its fit invites exactly that
> misreading — and the misreading flatters whoever wrote the board.

## The contract

Every model the arena is pointed at MUST have a profile here, named for its id
with `/` replaced by `_` (so `poolside/laguna-s-2.1` →
`poolside_laguna-s-2.1.json`). The runner loads it **before any billable call**
and refuses to start without one, in the same family as the matrix-completeness
and determinism gates: a board that cannot say what it measured with is not a
board. Writing a profile costs a few minutes of reading the vendor's own
announcement; discovering the mismatch afterwards costs the board.

## Fields

| field | meaning |
|---|---|
| `model` | exact id sent as `OPENAI_MODEL` — must match, or the gate fails |
| `vendor` | who serves it |
| `specialisation` | what the model is BUILT for, in the vendor's own words |
| `workload_fit` | `aligned` \| `partial` \| `mismatched` — against THIS arena's general-assistant workload |
| `fit_note` | one sentence a reader needs before reading any number |
| `reasoning_mode` | `{ "supported": bool, "enabled_by_arena": bool, "note": "..." }` |
| `published_benchmarks` | what the vendor reports, so a reader can see the workload gap |
| `sources` | URLs — every claim above must be attributable |

`workload_fit` is deliberately blunt. `mismatched` does not mean the board is
invalid; it means every cross-model comparison must carry the caveat, and a low
score is at least as likely to be workload mismatch as framework weakness.

## Relationship to the anchor set

A profile explains *why* two models differ. The anchor set
(`arenas/live/tasks/anchors.md`) **measures** it: the same fixed items run on
both models, so the shift is a number rather than a hunch. Profiles are the
qualitative half, anchors the quantitative one — neither substitutes for the
other.
