---
title: The authorship lens — sweeping every arena dimension for adapter-decided outcomes
date: 2026-07-28
status: in progress — 3 of 16 dimensions verified, 3 defects found
---

# The authorship lens

## The question this sweep asks

For each arena dimension:

> **Does pass/fail depend on what the FRAMEWORK provides, or on what the ADAPTER
> AUTHOR decided to build?**

If the answer is "the author", the dimension is not measuring frameworks. It is
measuring which integrator had more time, more scruples, or a different reading
of the fairness protocol — and publishing that difference as a capability gap in
someone else's product.

## Where the question came from

`memory_cross_session` split five adapters three-against-two. All five frameworks
lack a native user axis (verified in source). Three adapters synthesized one at
app level and PASSED; two refused on fairness grounds and FAILED. Same capability,
opposite board outcome, decided by adapter authorship.

Fixing that one raised the obvious question: **how many of the other fifteen have
the same defect, and nobody looked?**

## Method (reproducible, cheap)

1. **Map the spread.** Read every `arenas/live/*/matrix.json`, tabulate
   pass/fail/infeasible per dimension. Both extremes are suspicious:
   - *divergence* — some pass, some fail: check whether the failures cite
     framework absence (defect) or a genuine framework limitation (fine);
   - *uniformity* — everyone passes: check whether everyone passes because every
     adapter hand-built the same thing, which means the dimension discriminates
     nothing and hides a real difference in HOW.
2. **Read the adapters, not the docs.** `git grep -i "hand-rolled|compound key|
   adapter-side|synthes"` across the adapter sources. Adapters are unusually
   honest about this in their own comments — seven of eight self-declared their
   hand-rolled scope key.
3. **Get file:line per finding.** A verdict without a citation is the doc-truth
   failure this repo already has three rules against.
4. **Classify with the L0–L3 ladder** (`.claude/rules/capability-absence-claims.md`):
   L0 no concept · L1 parameter/convention (the padlock) · L2 enforced at a
   boundary · L3 enforced by construction (the safe).
5. **Fix by LABELLING, not by hiding.** `pass:<provenance>` (see
   `arenas/live/MATRIX-SCHEMA.md`) keeps every adapter running the dimension while
   the cell declares where the capability came from. Deleting the dimension or
   silently equalising it both destroy information.
6. **Apply symmetric rigor.** Our own cell gets the same label and the same
   stress-test. A sweep that audits eight competitors and exempts the house is
   marketing.

## Verdicts so far — 3 of 16

| dimension | verdict | evidence |
|---|---|---|
| `memory_cross_session` | **DEFECT** — no framework has a native user axis; 3 adapters synthesized it and passed, 2 refused and failed | eino `internal/core/interrupt.go` (CheckPointStore = opaque blob under one id); genkit `ai/exp/session.go` (keys on snapshotID/sessionID) |
| `memory_scoped` | **DEFECT** — 8/8 competitors hand-roll a compound scope key; aisotropy enforces by construction; all nine rendered one identical ✓ | ai-sdk `lib/scoped-store.mjs:1`; mastra `lib/scoped-store.mjs:1`; eino `mode_scoped_memory.go:22`; genkit-go `rqlite_client.go:130`; langgraph `modes/memory_scoped.py:21`; llamaindex `adapter.py:98`; pydantic-ai `arena_memory_store.py:185`; ours `mode_scoped.go:23` |
| `isolation_leak` | **DEFECT** — same mechanism, same eight, same label | eino `main.go:46` and the same set above |

Fixed by labelling: 32 cells now carry `pass:L1 adapter-supplied compound key` or
`pass:L3 by construction (prefix + in-scan filter)`.

**Three for three.** Not one dimension examined under this lens has been clean.
That rate is the finding — more than any individual dimension — and it is the
reason the remaining thirteen matter.

## Remaining — 13, with the leads

| dimension | spread | why it is suspicious |
|---|---|---|
| `crash_recovery` | 8 pass, 0 fail | uniform: does every adapter hand-build durability, or does the framework provide it? |
| `observability` | 8 pass, 0 fail | uniform, and our own README calls ours "NATIVE OTel, no bridge" — implying others bridge. If so, that is L1-vs-L3 again |
| `middleware` | 8 pass, 0 fail | uniform; ADR-0062 says gollem ships middleware natively, so who else does |
| `memory` | 9 pass | uniform, and the base case the extensions build on |
| `qa` / `structured` / `stream` | 9 pass | uniform; least likely to hide authorship, verify cheaply and move on |
| `rag` | 8 pass, spring-ai fails | divergence: is spring-ai's failure a framework limit or an adapter gap? |
| `tools` | 8 pass, spring-ai fails | same question, same adapter — a pattern worth one look |
| `isolation_forgetful` | 3 pass, 2 fail, 4 infeasible | divergence: langgraph and llamaindex fail. Check whether the failure is the framework or the wiring |
| `memory_semantic_recall` | 3 pass, 1 fail, 5 infeasible | llamaindex fails; five infeasible is itself worth auditing |
| `memory_consolidation` | 4 pass, 5 infeasible | more infeasible than pass — are those reasons still true? |
| `safeguard` | 1 pass, 8 fail | almost certainly STALE: predates the declineRe/apostrophe fix. Re-read after the current board lands |
| `wasm_tool` | 1 pass (ours only) | already swept — see `arena-wasm-tool-dimension.md`. The criterion is shaped around our implementation; restatement is an open operator decision |

## How to continue

The sweep needs no board run and no model spend — it is source reading plus
`matrix.json` edits. Start from the leads above, produce a verdict with file:line
per dimension, and append it to the table. When a defect is found, fix it the same
way: label with provenance and the L-level, never by equalising or deleting.

## Related

- `docs/architecture/arena-fairness-protocol.md` §4a — the inverted reading that
  made two adapters fail for refusing to hand-roll
- `docs/architecture/arena-wasm-tool-dimension.md` — the sweep that retracted our
  own uniqueness claim
- `arenas/live/MATRIX-SCHEMA.md` — the `pass:<provenance>` form
- `.claude/rules/capability-absence-claims.md` — the L0–L3 ladder and symmetric rigor
