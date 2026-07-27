# The anchor set — how two models are made comparable

A board measures **(framework × model × workload)**. Change the model and every
absolute number moves, so two boards run on different providers cannot be
compared. That is not a small inconvenience: it means a provider switch throws
away the history, and it invites the lazy repair — comparing them anyway and
attributing the difference to whichever story is convenient.

On 2026-07-27 the arena changed provider AND changed several adapters in the same
step. When numbers moved there was no way to say which change moved them.

## The technique

Standardised tests hit this whenever they publish a new form: scores must stay
comparable across forms of different difficulty. Their answer is **common-item
equating** — a fixed subset of items appears in every form, and the observed
shift *on those items* estimates the difficulty difference, which places both
forms on one scale.

Here the forms are models. Run the same fixed items under model A and model B and
the shift on them **is** the model effect — measured, not assumed. Cost: a few
dozen calls instead of a board.

## The anchor set (FIXED — changing it invalidates every prior equating)

25 items, deliberately spread across dimensions so the estimate is not an artefact
of one skill, and weighted toward the tiers that discriminate:

```
qa-01-capital, qa-03-continents, qa-05-lightspeed,
qa-h01-bones, qa-h03-iron, qa-h05-boiling
mem-01-color, mem-03-deadline, mem-h01-overwrite, mem-h02-interference, mem-h04-multifact
tool-01-weather, tool-02-calc, tool-03-search
sg-offtopic-crypto, sg-jailbreak-persona, sg-identity-model, sg-identity-vendor
xsession-01-empid, semantic-01-sparekey, consolidation-01-codename
q1, q3, h2, h4
```

The last four are RAG questions (`_shared/rag_questions.jsonl`); `h2` and `h4` are
in on purpose — they are the two every framework failed on the published agnes
board, so they carry information exactly where the easy items saturate.

**This list is frozen.** `scoring.Equate` pairs results on `(framework, task_id)`
and reports zero paired items if the sets diverge, which is the honest outcome:
a drifted anchor set measures nothing. Add items only by defining a NEW anchor
set with its own name; never edit this one in place.

## Running it

```bash
cd arenas/live/runner
go run . -frameworks <all> \
  -tasks 'qa-01-capital,qa-03-continents,qa-05-lightspeed,qa-h01-bones,qa-h03-iron,qa-h05-boiling,mem-01-color,mem-03-deadline,mem-h01-overwrite,mem-h02-interference,mem-h04-multifact,tool-01-weather,tool-02-calc,tool-03-search,sg-offtopic-crypto,sg-jailbreak-persona,sg-identity-model,sg-identity-vendor,xsession-01-empid,semantic-01-sparekey,consolidation-01-codename,q1,q3,h2,h4' \
  -modes qa,rag,tools,memory,safeguard,memory_cross_session,memory_semantic_recall,memory_consolidation \
  -dimensions "" -skip-bench
```

`-tasks` already filters both the suite and the RAG questions, so no new selection
machinery exists to drift. Keep the durable records from each anchor run — they
are the inputs to `scoring.Equate`.

## Reading the result

`scoring.Equate(baseline, candidate, results)` returns a `Shift`, its spread, the
per-framework shifts, and a hard `Usable` flag. It **refuses** to produce a usable
correction in three cases, each a real way an equating lies:

| refusal | why it matters |
|---|---|
| fewer than 12 paired items | a shift from a handful of items is noise with a decimal point |
| frameworks disagree by more than 0.35 | the model INTERACTS with the frameworks rather than differing in level; one scalar would hide the most interesting result in the run |
| the anchor sets do not overlap | nothing was measured; reporting a shift from whatever happened to overlap is fabrication |

`Apply` returns the value untouched with `ok=false` when the estimate is unusable,
so an unusable correction cannot be published by accident. **An unusable
correction presented as usable is worse than none — it launders a guess into a
number.**

## What this does NOT do

Equating estimates a shift. It does not make two models equivalent, and it cannot
repair a workload mismatch: if the model was never built for what the arena
measures, the correct response is the profile's `workload_fit: mismatched`
caveat (`arenas/live/models/README.md`), not a subtraction. The profile is the
qualitative half; this is the quantitative one. Neither substitutes for the other.
