---
title: The authorship lens — sweeping every arena dimension for adapter-decided outcomes
date: 2026-07-28
status: in progress — 7 of 16 dimensions verified, 5 defects + 2 clean
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

## Verdicts so far — 7 of 16

| dimension | verdict | evidence |
|---|---|---|
| `memory_cross_session` | **DEFECT** — no framework has a native user axis; 3 adapters synthesized it and passed, 2 refused and failed | eino `internal/core/interrupt.go` (CheckPointStore = opaque blob under one id); genkit `ai/exp/session.go` (keys on snapshotID/sessionID) |
| `memory_scoped` | **DEFECT** — 8/8 competitors hand-roll a compound scope key; aisotropy enforces by construction; all nine rendered one identical ✓ | ai-sdk `lib/scoped-store.mjs:1`; mastra `lib/scoped-store.mjs:1`; eino `mode_scoped_memory.go:22`; genkit-go `rqlite_client.go:130`; langgraph `modes/memory_scoped.py:21`; llamaindex `adapter.py:98`; pydantic-ai `arena_memory_store.py:185`; ours `mode_scoped.go:23` |
| `isolation_leak` | **DEFECT** — same mechanism, same eight, same label | eino `main.go:46` and the same set above |
| `memory` | **DEFECT** — the base case, and split 5-4. FIVE drive a framework memory primitive: spring-ai the native `ChatMemoryRepository` SPI, llamaindex `llama_index.core.memory.Memory`, langgraph the `SqliteSaver` checkpointer, mastra `@mastra/memory` over libsql, aisotropy its tiered memory through `App.Turn`. FOUR assemble history around the call from adapter code: ai-sdk ("history loaded/appended around the call"), genkit-go, pydantic-ai (`message_history` persisted by the adapter), eino — whose own comment says it plainly: "no built-in persisted-memory component to drive natively". Nine identical ✓. | spring-ai README `memory` row; llamaindex README `memory` row; eino `mode_memory.go:26`; pydantic-ai `adapter.py:23`; ai-sdk README `memory` row |
| `crash_recovery` | **DEFECT** — durability comes from the FRAMEWORK for three and from the ADAPTER for five, and all eight rendered one identical ✓. langgraph drives LangGraph's native `SqliteSaver` checkpointer; llamaindex uses the framework's own on-disk `Memory` store; aisotropy persists through its real seam (`App.Turn` → `chatPersistUserTurn` → on-disk BadgerDB). ai-sdk, mastra, eino, genkit-go and pydantic-ai check-point turn-by-turn from adapter code. Labelled L2-native vs L1-adapter. | langgraph `modes/memory.py:5` (`SqliteSaver`); ai-sdk `index.mjs:22`; pydantic-ai `adapter.py:700`; eino `main.go:56`; ours `mode_crash.go` |
| `middleware` | **CLEAN** — every adapter drives a framework-NATIVE primitive, so the uniform pass is real rather than eight people building the same thing: ai-sdk `wrapLanguageModel({model, middleware})`, genkit-go `ai.ModelMiddleware` via `ai.WithMiddleware`, pydantic-ai `ArenaMiddlewareModel(WrapperModel)`, llamaindex native interception, langgraph LangChain-native model middleware (three mechanisms exist; the adapter documents which it picked), eino no single primitive but TWO documented native seams, aisotropy `WithModelMiddleware` (ADR-0062). One nuance worth a label rather than a fail: **mastra** wraps via the **AI SDK's** `wrapLanguageModel`, i.e. native to the stack it is built on, not a Mastra-specific middleware API. | `arenas/live/langgraph/modes/middleware.py:1`; `arenas/live/mastra/index.mjs:30`; each adapter README's `middleware` row |
| `observability` | **CLEAN** — the results table already declares the mechanism per framework (`native`, `callback→OTel bridge`, `via-SDK-registration`), which is the provenance labelling the other dimensions were missing. Frameworks genuinely differ here and the board says how. **But our OWN row carried a stale falsehood**: `dimensions.go:158` read "n/a — adapter exposes no observability mode" long after `mode_observability.go` and the `modeObservability` constant existed, so the board printed that sentence beside a measured span count from the very mode it denied. Fixed. | `dimensions.go:155`; `arenas/live/aisotropy/mode_observability.go` |

Fixed by labelling: 32 cells now carry `pass:L1 adapter-supplied compound key` or
`pass:L3 by construction (prefix + in-scan filter)`.

**Five defects in seven.** `observability` is the first clean one — and clean for the
instructive reason: it already did what the broken dimensions did not, declaring the
mechanism beside the result. It is the shape the others are being fixed into, which
means the fix is not an invention, it is a pattern the arena already had in one place
and had never generalised.

`middleware` is the second clean one, and it sharpens what the lens is actually
detecting. Both clean dimensions came out clean because the FRAMEWORKS genuinely
provide the primitive — so a uniform pass is a real finding, not a hidden one. The
three defects were all cases where no framework provided it and every adapter built
it, which is when uniformity becomes camouflage.

`crash_recovery` then confirmed the discriminator by landing on the other side of it:
uniform ✓, but three adapters get durability from a framework primitive and five
rebuild it in adapter code. Exactly the memory_scoped shape, found by asking the same
question rather than by noticing anything unusual in the numbers — the numbers were
identical, which was the point.

`memory` is the sharpest instance so far because it is the BASE CASE the whole
memory family builds on: four adapters were being credited with a framework memory
capability while assembling the history themselves, and eino's own comment states
the framework has no persisted-memory component at all. Every memory-family
dimension inherits that, which is why the extensions kept coming out defective —
they were extending something that was already mislabelled.

That is the discriminator to carry into the remaining nine: uniform-because-native
is fine; uniform-because-everyone-hand-built-it is the defect. Only reading the
adapters tells them apart, which is why the coarse grep in step 2 must always be
followed by the per-dimension context — it reported "hand-rolled x7" for BOTH clean
dimensions, matching text about a different mode in the same README.

## Remaining — 9, with the leads

| dimension | spread | why it is suspicious |
|---|---|---|
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
