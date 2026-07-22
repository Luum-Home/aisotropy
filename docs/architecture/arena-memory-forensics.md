---
type: Process
title: "Arena Memory Forensics — how a 6/8 board score was misdiagnosed, then correctly diagnosed"
description: A post-mortem of the live arena's memory-mode result (aisotropy 6/8, worst of eight frameworks, 3x the nearest competitor's token spend). The first hypothesis — a recency-half-life scoring bug — was plausible, cited real constants, and was wrong. The proven cause, found by opening the arena's own BadgerDB store, is a contract asymmetry between aisotropy's per-turn replay and the suite's canned-transcript design, compounded by a real product defect in working-memory condensation. Every claim below is separated into ARENA-HARNESS (adapter-only) vs REAL PRODUCT DEFECT (ships to every aisotropy consumer), with file:line evidence and an explicit inferred-not-proven section.
tags: [architecture, process, arena, memory, forensics, post-mortem, code-truth]
timestamp: 2026-07-21T00:00:00Z
---

# Arena Memory Forensics — how a 6/8 board score was misdiagnosed, then correctly diagnosed

> **Why this document exists, and why it is not softened.** This repository is about to become
> public. The live arena's `memory` board (`arenas/live/RESULTS.md`, committed as `87eaba1`) shows
> aisotropy scoring worse on its own memory system than six of seven competing frameworks — several
> of which ship **no memory component at all**. The operator's instruction was explicit: document
> the full analysis anyway, unsanitized. That is the point of this document. An engineering
> post-mortem that only gets written when the numbers are flattering is not a post-mortem, it is
> marketing with extra steps. This one follows `.claude/rules/prune-evidence-gate.md`'s discipline —
> every "cause" claim below is checked against the code, not asserted from a prior document or a
> plausible story — because the first hypothesis in this very investigation is a case study in that
> discipline failing, then working.

## 1. What the board said

Board committed at `87eaba1` ("arena(results): first discriminating board — aisotropy wins rag,
loses memory and tools"), model held constant at `agnes-2.0-flash` across all eight frameworks
(ai-sdk, aisotropy, eino, genkit-go, langgraph, llamaindex, mastra, pydantic-ai; spring-ai failed to
run — no local JVM). `arenas/live/RESULTS.md:227` (as committed):

```
| aisotropy | memory | 6/8 ✗ | 0.8 | recall (judged) (discriminates) | 23218 | 8 | 0 |
```

Six of the seven frameworks that produced a result scored `8/8`. aisotropy scored `6/8` — the
**worst** memory score on the board, on a dimension the project's own architecture is built around —
`.claude/rules/architecture.md`'s memory-tier hierarchy, organized by access-latency tier ("specialized
tools by access temperature").

The token cost compounds the embarrassment. `arenas/live/RESULTS.md:419-461` (the "Measured token
spend" table, `87eaba1`):

```
| ai-sdk      | memory | 14504 | ... |
| aisotropy   | memory | 41292 | ... |
| eino        | memory | 10723 | ... |
| genkit-go   | memory | 13535 | ... |
| langgraph   | memory | 10789 | ... |
| llamaindex  | memory |  9657 | ... |
| mastra      | memory |  7720 | ... |
| pydantic-ai | memory |  7568 | ... |
```

aisotropy's 41,292 input tokens sit against a competitor range of 7,568–14,504 — roughly **2.85x**
the nearest competitor (ai-sdk) and over **5x** the cheapest (pydantic-ai). Two items failed:
`mem-h01-overwrite` (`arenas/live/RESULTS.md:584`) and `mem-h03-longhaul`
(`arenas/live/RESULTS.md:586`).

## 2. The first hypothesis — plausible, cited real constants, and WRONG

A source-comparison pass inferred the cause from the facts-tier scoring constants, before running
anything. `internal/facts/sqlite.go:18` sets:

```go
const recencyHalfLife = 30 * 24 * time.Hour
```

and the ranking function (`internal/facts/sqlite.go:289-299`) computes
`score = relevance × importance × recency`, with `recency = math.Exp2(-ageHalfLives)` where
`ageHalfLives = age.Seconds() / recencyHalfLife.Seconds()`. For two facts written seconds apart in
an arena task, `age` is on the order of 1-10 seconds against a 30-day half-life — `ageHalfLives`
rounds to `0.0000...`, so `recency` is `1.0000` to four decimal places for **every** fact in the
task, not just the newest one. The inference: if recency cannot discriminate "just-written" from
"written five seconds ago," a fact-overwrite task (state a value, then overwrite it, then ask which
one is current) would look structurally unable to prefer the newer fact. That is a real property of
the formula — it was reasoned from the actual constant and the actual function, correctly labeled as
an inference, and it was internally consistent.

**It was not the cause.** Two things sink it, both established in §4 below: the facts tier held
**zero** rows at recall time in this run regardless (§4.3), and the failure mode observed in the
persisted store is not "wrong fact selected by a stale-favoring score" — it is "the model never
states the fact at all, because it is refusing." A scoring-formula bug that never gets a chance to
run cannot be the cause. The lesson this half of the investigation teaches is not "the hypothesis was
foolish" — it was a reasonable, evidence-grounded inference — the lesson is that an inference from
constants is not a substitute for reading the actual runtime artifact, which is exactly what
`.claude/rules/prune-evidence-gate.md` already generalizes as "code-claim ⇒ executable evidence."

## 3. The proven cause — read from the arena's own BadgerDB store

The arena's aisotropy adapter persists working memory on disk
(`arenas/live/aisotropy/run.sh:42`, `AISOTROPY_DATA_DIR` pinned to a stable path — see §4.4). Opening
that store read-only for the `mem-h01-overwrite` session (deterministic session ID
`arena-mem-aisotropy-mem-h01-overwrite`, per `arenas/live/runner/tasks.go:353`) shows an unbroken
chain of assistant turns, all refusals: every stored assistant message reads to the effect of "I am
unable to change your seat assignment...". The fact `9A` — the overwritten value the task expects
recalled — **was present** in the working-memory hydration window at recall time (inside the last 12
messages `Recent` would return, `internal/memory/working_memory.go:551`). The model had the fact
available and refused to state it, because the entire session context it was hydrated with is a
sequence of the model itself declining to do what it believes is a real booking action.

### 3.1 Root mechanism: `userMessages` discards the suite's canned assistant turns

`arenas/live/aisotropy/mode_memory.go:29` builds the turn list for `App.Turn` via:

```go
turns := userMessages(req.Messages)
```

`arenas/live/aisotropy/main.go:365-372`:

```go
func userMessages(msgs []inMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}
```

Only `role=="user"` survives. The suite's canned COOPERATIVE assistant lines for this task family
(e.g. "Booked: Skylark Line, seat 14C") are **silently dropped** before they ever reach
`App.Turn`. The adapter replays each remaining user turn through `App.Turn`
(`arenas/live/aisotropy/mode_memory.go:43-60`), which means the model is not shown "here is what you
already (fictionally) booked" — it is asked, turn after turn, to actually perform a live seat
booking action it has no tool for and no permission to fabricate. It declines. aisotropy's working
memory then persists that decline as the assistant's real prior turn, and the next turn in the same
session is hydrated with a growing transcript of the model refusing itself into a corner — a refusal
cascade, entirely self-inflicted by the harness, that the model has no way to escape mid-session.

### 3.2 This is spec-sanctioned, not a contract violation — the asymmetry is the bug

Critically, `userMessages` filtering to user-only is not an adapter bug against the arena's own
contract. `arenas/live/SPEC.md:158-166` explicitly documents this:

> "**aisotropy** is a per-turn, memory-first runtime: its public `App.Turn` takes one prompt plus a
> `SessionID` and carries earlier turns through its working-memory tier. The reference adapter
> therefore **replays each user turn** through `App.Turn` on a shared session and **sums** the
> provider usage across those turns. [...] `RESULTS.md` labels this explicitly."

And that constraint is real, not adapter laziness: `aisotropy/turn.go:28-54`'s `ChatInput` struct has
a `Prompt string` field and no message-history slot at all —

```go
type ChatInput struct {
	TenantID string
	UserID   string
	Prompt string       // the user message for this turn
	SessionID string
	...
}
```

`App.Turn` (`aisotropy/turn.go:89`) has nowhere to put a canned assistant line even if the adapter
wanted to hand it one. The adapter obeyed the documented, correct contract for aisotropy's actual
public surface. **The bad number is produced by a contract asymmetry the suite already knows about
and names, not by an adapter defect** — every other framework in this suite accepts a message list
and can replay the canned assistant turns verbatim (§5), so only aisotropy's per-turn design turns
"canned assistant line" into "silently discarded input, replaced by whatever the model actually says
this time." Discarding it is correct per the adapter's contract; the model then treating the missing
context as license to try to actually perform the booking is the behavior under test — and it is
this behavior, not a scoring bug, that produced the refusal cascade.

### 3.3 The facts tier contributed zero — checked, not assumed

Before crediting or ruling out §2's hypothesis, the facts tier's actual state at recall time matters.
`arenas/live/aisotropy/rqlite_reset.go:39-69`'s `resetRqliteFacts()` issues
`DROP TABLE IF EXISTS facts` and `DROP TABLE IF EXISTS facts_fts` against the configured rqlite, and
`arenas/live/aisotropy/main.go:317-327`'s `dispatch()` calls it unconditionally on **every** adapter
process boot, before the mode switch:

```go
func dispatch(req *request, out *result) error {
	...
	resetRqliteFacts()
	...
```

So the facts tier is empty at the start of every process invocation in the arena run. §2's
recency-half-life hypothesis could not have produced the observed failure even in principle, because
the tier whose scoring formula it indicts held no rows to score.

**A doc-vs-code discrepancy found while writing this document, not relayed from a prior
investigation.** `rqlite_reset.go`'s own file-header comment justifies the workaround by asserting
"`internal/facts/rqlite.go`'s `migrate()` is NOT idempotent across process restarts." That was true
when the workaround was first needed, but checking `internal/facts/rqlite.go:61-68` against the
comment shows it is no longer accurate:

```go
func (s *RqliteStore) migrate(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if err := s.execToleratingDupColumn(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
```

`execToleratingDupColumn` (`internal/facts/rqlite.go:75-88`) runs each statement as its own request
and tolerates a duplicate-column error at both the request and per-statement level — this is exactly
the "mirror the SQLite tier's per-statement tolerance" fix `rqlite_reset.go`'s comment says still
"belongs in `internal/facts/rqlite.go`." Git history shows why the comment is stale: commit `64531e0`
("fix(facts): idempotent rqlite migration — re-boot no longer fails on ALTER ADD COLUMN", 2026-07-17
20:18:14) landed the fix; `rqlite_reset.go` was created 57 minutes later in commit `4bf2c80`
(2026-07-17 21:15:47) with a comment describing a bug that, in this same repository, had already been
fixed. This does not change the empty-facts-tier finding — `resetRqliteFacts()` still runs
unconditionally regardless of whether the underlying bug exists — but it is a live instance of
exactly the failure class this document's method section (§8) is about: a code claim that was true
once, embedded in a comment, and never re-checked against the code it describes. The comment should
be corrected; the workaround itself is currently harmless-but-redundant, not wrong, since it forces a
clean schema on every boot regardless of whether that is still strictly necessary.

### 3.4 Cross-run contamination compounds the same session

The `mem-h01-overwrite` session's BadgerDB keys start at sequence 16, not 1, while this single run
only appends 14 messages worth of turns — meaning a **prior run's** turns were still present at the
start of this run and became part of the hydrated context (their content is what produced a stale
summary asserting the seat was "14C" before this run's own turns even began). This is a direct
consequence of `arenas/live/aisotropy/run.sh:42`:

```bash
export AISOTROPY_DATA_DIR="${AISOTROPY_DATA_DIR:-${SCRIPT_DIR}/.aisotropy-data}"
```

pinned to a stable, relative path that nothing in the adapter or the runner ever clears between
invocations. pydantic-ai's adapter hit the identical class of bug and defends against it explicitly —
`arenas/live/pydantic-ai/adapter.py:391-408`'s `_prune_history` bounds the replayed/persisted native
message list to `_MEMORY_MAX_HISTORY_MESSAGES = 24` entries
(`arenas/live/pydantic-ai/adapter.py:342`) specifically because the arena's session IDs are
deterministic per task (`arena-mem-<framework>-<task-id>`) and are **re-opened on every re-run**
(`arenas/live/pydantic-ai/adapter.py:322-336`'s module comment states this directly: "observed: a 28
KB row of 48 accumulated messages"). aisotropy's adapter has no equivalent guard.

### 3.5 The token gap is call count, not context bloat — verified mechanism, unverified exact multiplier

aisotropy's per-turn replay (§3.2) means the number of model calls for a memory task scales with the
number of turns in that task, not with one call per task the way a whole-history framework works.
This is a directly observed, real difference in call **volume**, confirmed by the per-task token
rows themselves: `arenas/live/RESULTS.md:584` shows aisotropy's `mem-h01-overwrite` spending **8,120
output tokens** on a single task-row (versus roughly 900-1,600 for every other framework on the same
task, e.g. `arenas/live/RESULTS.md:514` ai-sdk at 1,062, `:794` langgraph at 1,050) — consistent with
many small refusal turns accumulating output, not one large answer. `arenas/live/RESULTS.md:586`
shows `mem-h03-longhaul` at **13,430 output tokens** against competitors' 900-2,000
(`:516`, `:656`, `:726`, `:796`, `:866`, `:936`, `:1006`), consistent with the ten-turn persist
sequence (§6) each producing its own reply, plus condensation calls (§6 mechanism below).

Two concrete extra-cost sources are real and file-grounded: the working-memory summarizer is a real
model call through the same client, wired at `aisotropy/app.go:1679-1693`
(`newWorkingMemory`'s `summarizer = memory.LLMSummarizer{Client: client}` at line 1683) and executed
in `internal/memory/summarizer.go:124-159`'s `Condense`; and every `App.Turn` call runs through the
fail-closed reflection loop bounded by `chatMaxReflectionRetries = 3`
(`internal/api/chat.go:48`), invoked at `internal/api/core.go:112`'s `chatReflect(...)` call inside
`ChatCore` (`internal/api/core.go:54`) — `App.Turn` (`aisotropy/turn.go:89-118`) calls `ChatCore`
directly, so every memory-mode turn pays for the same reflection machinery the HTTP `chat` route
does, per `.claude/rules/architecture.md`'s note that the reflection loop lives on the non-streaming
route.

What is **not** independently re-derived in this document: the brief's precise "47 model calls for
aisotropy vs 16 for a competitor" and "41,292/47 ≈ 880 input tokens per call, entirely normal" figures.
`arenas/live/RESULTS.md` aggregates tokens per task-row and per framework×mode, not per individual
model call, and no raw per-invocation call log was available in the repository to re-derive an exact
call count. The **mechanism** (per-turn replay multiplies call count; competitors make roughly one
call per persist-invocation plus one per recall-invocation) is verified against
`arenas/live/runner/tasks.go:343-371`'s `buildMemory` (two `adapterInput` invocations per task,
persist + recall, at `arenas/live/runner/tasks.go:356,358`) and against aisotropy's per-user-turn loop
(§3.1). The exact arithmetic is carried into §9 as inferred, not proven.

## 4. `mem-h03-longhaul` — deterministic overflow, non-deterministic rescue

The needle fact ("Vermillion Bravo") was in **no** memory tier at recall time. The task issues 10
persist turns × 2 messages = 20 messages against a working-memory active-buffer cap of 12
(`internal/memory/working_memory.go:20`, `const defaultMaxActiveMessages = 12`;
`aisotropy/app.go:1693,1708` pass `0` for the size parameter, which
`internal/memory/working_memory.go`'s constructor path resolves to the default).

`Append` (`internal/memory/working_memory.go:251-320`) computes, on every write
(`internal/memory/working_memory.go:306`):

```go
over = len(keys) > w.maxActiveMessages
```

and when `over`, hands off to a background goroutine (`triggerCondensation`,
`internal/memory/working_memory.go:327`) that runs `condensePass`
(`internal/memory/working_memory.go:389-411`). `condensePass` recomputes the excess on every
iteration and, when positive, takes the **oldest** keys as `excessKeys` — `keys[:over]`
(`internal/memory/working_memory.go:400-403`) — since `activeKeys` returns keys in ascending
sequence order, the oldest turn (turn 1, the one carrying the needle) is exactly what gets marked for
eviction. `condenseAndPrune` (`internal/memory/working_memory.go:499-536`) reads those oldest
messages, calls `w.summarizer.Condense(ctx, existing, oldest)` (line 519) — a real LLM call
(`internal/memory/summarizer.go:124-159`) — and replaces them with the returned paragraph, deleting
the originals from the active buffer. `Recent(ctx, key, 12)` (`internal/memory/working_memory.go:551`)
then returns only what remains in the active window: the summarized filler, not the needle.

The summarizer carries **no needle-preservation contract**. Its system prompt
(`internal/memory/summarizer.go:104-109`):

> "produce a single, concise paragraph that preserves the important facts, decisions, and open
> threads. Respond with exactly one paragraph of plain text — no headers, no bullet points, no
> preamble."

is a generic condensation instruction with no named-entity-preservation guarantee. The failing run's
persisted summary reads to the effect of "a relaxed, rail-based coastal itinerary" and never mentions
"Vermillion" — a plausible, well-formed summary of the conversation's gist that happens to drop the
one fact the task grades on.

This task **passed in an earlier calibration run** and **failed here**, on the same code, because the
summarizer is a live LLM call with no determinism guarantee — same input, different paragraph on a
different day. Two further, file-grounded instability sources compound this, though which one (if
any) actually flipped the earlier pass to this run's fail was not established:

- `WorkingMemory.Close` (`internal/memory/working_memory.go:228-237`) cancels `w.shutdownCancel()`
  **before** calling `w.wg.Wait()`:
  ```go
  func (w *WorkingMemory) Close() error {
  	w.mu.Lock()
  	if !w.closed {
  		w.closed = true
  		w.shutdownCancel()
  	}
  	w.mu.Unlock()
  	w.wg.Wait()
  	return w.db.Close()
  }
  ```
  `condensePass` and `condenseAndPrune` both run against `w.shutdownCtx`
  (`internal/memory/working_memory.go:392,411`), so a condensation in flight when `Close` fires can
  abort mid-pass — neither the new summary nor the prune lands, and the outcome depends on wall-clock
  timing between the background goroutine and the calling process's shutdown. This is a
  wall-clock-dependent code path in a system component, which is a real defect independent of the
  arena.
- Cross-run state (§3.4) means which specific messages fall inside vs. outside the 12-message window
  at any given recall can shift between runs sharing the same `AISOTROPY_DATA_DIR`.

## 5. Contributing defects — ARENA-HARNESS vs REAL PRODUCT DEFECT

This separation is the spine of this document. Not everything that produced the bad number is a
product bug; conflating the two would misdirect engineering effort exactly the way a "framework
difference" that is actually an adapter bug misdirects a benchmark reader (§8).

### 5.1 ARENA-HARNESS (adapter-only; does not ship to any aisotropy consumer)

| Defect | Evidence | Why it's harness-only |
|---|---|---|
| Canned assistant turns silently dropped, producing the refusal cascade | `arena/live/aisotropy/main.go:365-372` `userMessages`; `mode_memory.go:29` | `ChatInput.Prompt` (`aisotropy/turn.go:28-54`) has no history slot — this is the correct adapter behavior for aisotropy's real contract (§3.2), not a bug the adapter could avoid while staying honest to the product's actual API |
| Facts tier forcibly emptied every boot | `arenas/live/aisotropy/rqlite_reset.go:39-69`; called unconditionally at `main.go:327` | Exists only because the arena runs many process boots against one shared rqlite; a long-lived aisotropy service boots once |
| `resetRqliteFacts`'s justifying comment is stale (the underlying `rqlite.go` bug it cites was already fixed before the workaround file was written) | `rqlite_reset.go:1-9` vs `internal/facts/rqlite.go:61-88`; commit `64531e0` predates `4bf2c80` | A documentation defect in a harness file, not a functional one — the workaround still runs, harmlessly |
| No history-length cap between arena re-runs sharing one `AISOTROPY_DATA_DIR` | `arenas/live/aisotropy/run.sh:42` | pydantic-ai's adapter (`arena/live/pydantic-ai/adapter.py:322-408`) has this exact problem and fixed it in the adapter; aisotropy's adapter has not — this is a harness gap that COULD be fixed adapter-side without touching `internal/` |
| Extra billable calls from the reflection loop and the summarizer LLM call inflate the "cost" comparison against frameworks that make one bare call per turn | `internal/api/chat.go:48`, `internal/api/core.go:54,112`; `aisotropy/app.go:1679-1693`; `internal/memory/summarizer.go:124-159` | These are real product behaviors (fail-closed reflection, LLM condensation), but the arena's token-spend table compares them against competitors that do neither — an apples-to-oranges cost comparison the harness does not caveat |

### 5.2 REAL PRODUCT DEFECT (ships to every aisotropy consumer using on-disk working memory)

| Defect | Evidence | Consumer-facing impact |
|---|---|---|
| `WorkingMemory.Close` cancels `shutdownCtx` before `wg.Wait()`, so an in-flight condensation can abort with neither summary nor prune applied | `internal/memory/working_memory.go:228-237` | A production process shutting down mid-condensation can leave the active buffer over its cap with no corresponding summary — a real state-consistency gap, not an arena artifact |
| `SummaryMemory`-equivalent condensation (`LLMSummarizer.Condense`) has no named-entity / needle-preservation contract | `internal/memory/summarizer.go:104-109,124-159` | Any consumer whose conversation exceeds `defaultMaxActiveMessages = 12` (`internal/memory/working_memory.go:20`) risks silently losing a specific fact stated early in a long session — this is the mechanism behind `mem-h03-longhaul`'s failure and is not arena-specific |
| Condensation is a live LLM call with no determinism guarantee, so the SAME session can pass or fail identical grading across runs | `internal/memory/summarizer.go:124-159` (buffers a full `StreamChat` response, no seed/temperature contract asserted) | Any consumer relying on working-memory recall for a fact near the eviction boundary has non-deterministic recall behavior by construction |

## 6. What the seven competitors actually do — none of them do semantic retrieval here

**None of the seven frameworks that ran perform any semantic retrieval, importance scoring, or
recency-weighted ranking in the arena's `memory` mode.** Every one of them does ordered transcript
replay — the entire prior conversation, or a bounded tail of it, sent back to the model verbatim on
every turn:

- **pydantic-ai** sends the whole accumulated history as one flat list every call:
  `.venv/lib/python3.13/site-packages/pydantic_ai/_agent_graph.py:1246`,
  `messages=ctx.state.message_history[:]` (a full-list copy, no bound applied by the library itself —
  the adapter's own `_prune_history`, §3.4, is what bounds it).
- **langgraph**'s state-merge primitive is explicitly append-only: `add_messages`
  (`.venv/lib/python3.14/site-packages/langgraph/graph/message.py:61-70`) — "By default, this ensures
  the state is 'append-only', unless the new message has the same ID as an existing message." The
  installed langgraph package contains **no** `trim_messages` function at all (verified: zero matches
  for `def trim_messages` anywhere under the installed `langgraph` package tree) — recency for
  langgraph is free because the newest message is simply later in an unbounded array, not because
  anything in the library ranks it.
- **llamaindex** has the best-engineered token budget of the seven — `Memory.token_limit`
  (`.venv/lib/python3.13/site-packages/llama_index/core/memory/memory.py:204`) with a flush threshold
  computed from `chat_history_token_ratio` and checked against the live queue
  (`memory.py:693-699`, `tokens_in_current_queue > token_limit` triggers eviction) — but the arena
  adapter configured `token_limit=8000` against a workload of roughly 1-2k tokens per task, so the
  flush logic never actually fires in this suite; the budget machinery exists and is unused here.
- **mastra** ships the only framework-level default bound among the seven:
  `memoryDefaultOptions = { lastMessages: 10, semanticRecall: false, ... }`, compiled into
  `arenas/live/mastra/node_modules/@mastra/core/dist/chunk-77JDY5O7.js:7526-7527` from the package's
  `MastraMemory` base class (`threadConfig = { ...memoryDefaultOptions }` at
  `chunk-77JDY5O7.js:7557`). `semanticRecall` defaults off (`Boolean(config.semanticRecall)` with no
  fallback-true anywhere in the merge path) — the arena adapter
  (`arenas/live/mastra/lib/modes/memory.mjs:27-38`) leaves it explicitly disabled too. **Note on
  citation:** the brief's suggested location for this default (`memory.ts:83-84`) could not be
  located — the `@mastra/memory`/`@mastra/core` packages are vendored as compiled bundles with no
  `.ts` source checked in; the equivalent default was independently found and cited above in the
  compiled artifact instead.
- **ai-sdk** ships **no memory package at all** in this arena and scored `8/8` anyway — its adapter
  necessarily does its own ordered-history replay, with no library-level memory primitive to fail.
- **eino** has no memory component; its adapter's `runMemory`
  (`arenas/live/eino/mode_memory.go:24-75`) hand-rolls a SQLite-backed history store keyed by
  session_id (comment at `mode_memory.go:22-23`: "Eino has no built-in persisted-memory component to
  drive natively") and replays `priorTurns` plus the current invocation's messages verbatim
  (`mode_memory.go:43-48`) — including any canned assistant lines the invocation carries, since
  nothing in the adapter filters by role the way aisotropy's `userMessages` does.
- **genkit-go**'s session/memory support lives in an `exp/` (experimental) package, not the stable
  surface; the adapter does its own history handling rather than driving a genkit-go-native memory
  primitive.

Stated plainly: **a benchmark where six of seven frameworks pass by not having a memory system to
fail is measuring transcript replay, not memory.** aisotropy is the only framework in this arena that
attempts recency-aware retrieval, importance scoring, and bounded-buffer condensation instead of
"send everything back" — and every one of those extra moving parts is a place this run's failures
came from. That is a real trade-off this board makes visible, not an excuse for the 6/8: a system
that does more work to be smarter about memory and loses to "reply with the whole transcript" on the
tasks graded here has a real problem to fix (§7), not a framing problem to correct.

## 7. The gollem inventory — what would be reuse vs. what is genuinely additive

Per `.claude/rules/dependency-reuse-first.md`, before any fix is proposed, gollem's own capability
surface (module `github.com/fugue-labs/gollem@v0.5.1`, `core/`) is inventoried:

| Capability | gollem primitive | Would it have prevented the failures here? |
|---|---|---|
| Keep the first message verbatim while summarizing the middle | `SummaryMemory` (`core/memory/strategy.go:118-208`); `toSummarize := messages[1 : len(messages)-keepLast]` explicitly excludes `messages[0]`, re-appended untouched at `strategy.go:200` | **Yes, for the needle-loss failure specifically** — `mem-h03-longhaul`'s needle was in the FIRST persisted turn; a strategy that always preserves message 0 verbatim would have kept "Vermillion Bravo" out of the summarizer's input entirely |
| Sliding-window bound with system-prompt preservation | `SlidingWindowMemory(windowSize int)` (`core/memory/strategy.go:14-40`) | Partially — it is a hard drop, not a summary, so it would avoid non-determinism but would not preserve mid-window facts either |
| Token-budget-bounded history | `TokenBudgetMemory(maxTokens int)` (`core/memory/strategy.go:46-74`) | Not directly relevant to the needle-loss mechanism, but a real alternative to raw message-count bounding |
| A stable instruction/system-prompt pin | `BuildInstructionPin` (`core/instruction_pin.go:22`) | Not applicable to this failure |
| A pluggable point to install any of the above | `WithHistoryProcessor[T]` (`core/agent.go:216`), `type HistoryProcessor` (`core/agent.go:30`) | This is the actual missing seam (see below) |

**Why aisotropy cannot use these directly.** `internal/llm.Client`'s contract
(`internal/llm/llm.go:91-95`):

```go
type Client interface {
	StreamChat(ctx context.Context, system, prompt string) (<-chan Token, error)
}
```

is a flat `(system, prompt)` pair, not a message list. `GollemModel.Request`
(`internal/llm/gollem.go:109-136`) calls `buildGollemPrompt(messages, params)`
(`internal/llm/gollem.go:342-377`) which **flattens** every `core.ModelMessage` — system parts,
user parts, prior responses, tool results — into two strings before the underlying `Client` ever sees
them. `WithHistoryProcessor` operates on `[]core.ModelMessage` **before** that flattening; wiring it
in would require restructuring `GollemModel`/`internal/llm.Client`'s seam, not just calling a
function gollem already exports. This is a genuine architectural gap, not a missed one-line call.

**Do not overstate gollem's own contract either.** `SummaryMemory`'s summarization prompt
(`core/memory/strategy.go:165`, `"Summarize this conversation concisely:\n" + summaryText`) is
exactly as generic and needle-agnostic as aisotropy's own
(`internal/memory/summarizer.go:104-109`). Adopting `SummaryMemory` wholesale would fix the
"first message always survives" property but would **not**, by itself, fix "an arbitrary
mid-buffer fact can still be summarized away" — gollem does not ship a needle-preservation contract
either. Per `dependency-reuse-first.md`'s required framing: the persisted, multi-tenant-isolated
working-memory tier itself, the transactional-outbox consolidation, and the untrusted-context fence
(`internal/api/chat_stream.go:51-63`) are genuinely aisotropy's own additive work — gollem has no
equivalent to any of the three.

## 8. Ranked fixes

1. **(product, high value, low risk)** Give `internal/memory.WorkingMemory` a first-message-survives
   condensation rule, mirroring gollem's `SummaryMemory` shape (`core/memory/strategy.go:118-208`) —
   either by adopting a `core.HistoryProcessor`-shaped internal seam or by changing
   `condensePass`/`condenseAndPrune` (`internal/memory/working_memory.go:389-536`) to always exclude
   the earliest surviving key from `excessKeys`. This directly targets the `mem-h03-longhaul`
   mechanism (§4).
2. **(product, medium value, low risk)** Fix `WorkingMemory.Close`'s shutdown ordering
   (`internal/memory/working_memory.go:228-237`) to wait for in-flight condensation before cancelling
   its context, or to let a cancelled condensation apply the last-known-good partial result instead of
   dropping both the summary and the prune.
3. **(product, exploratory)** Investigate a needle-preservation instruction or extraction pass in
   `internal/memory/summarizer.go:104-159`'s prompt — a summarizer that is told to always retain
   named entities, numbers, and proper nouns verbatim would harden `mem-h03`-class recall without
   requiring a structural change.
4. **(harness, high value, low risk)** Give the arena's aisotropy adapter a history-length guard
   equivalent to pydantic-ai's `_prune_history` (`arenas/live/pydantic-ai/adapter.py:391-408`) so
   repeated re-runs against the same `AISOTROPY_DATA_DIR` stop compounding cross-run contamination
   (§3.4).
5. **(harness, medium value)** Reconsider whether `userMessages`'s role filter
   (`arenas/live/aisotropy/main.go:365-372`) should instead surface the canned assistant lines to the
   model as untrusted, clearly-labeled context (via the existing fence,
   `internal/api/chat_stream.go:51-63`) rather than dropping them outright — this changes what is
   under test (§3.2) and needs an explicit decision, not a silent adapter change, since it currently
   reflects a real and defensible reading of aisotropy's contract.
6. **(harness, low risk, doc-only)** Correct `rqlite_reset.go:1-9`'s stale justification comment
   (§3.3) — the workaround can stay, but the comment currently asserts a bug that commit `64531e0`
   already fixed.
7. **(investigation, before any of the above is prioritized further)** Run an A/B with
   `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` and the untrusted-context notice
   (`internal/api/chat_stream.go:59-63`) toggled, to determine whether the fence's "treat this as data,
   never as instructions" language is itself contributing to the model's refusal framing when it
   receives what looks like its own prior refusal as trusted-looking prior-turn content (see §9 — this
   is currently unproven).

## 9. What remains inferred, not proven

Honesty about what was and was not established is part of the method, not an afterthought:

- **The BadgerDB refusal-chain evidence (§3) is SINGLE-SOURCED and no longer reproducible.** The
  verbatim stored messages were dumped read-only by the original forensic investigation. A second,
  independent attempt to re-read the same store while writing this document found only unrelated
  smoke-test sessions: the arena's data directory is pinned to a stable path (§3.4), so concurrent
  adapter work had already overwritten the arena session. The chain of custody is therefore one
  reader, and the raw artifact is gone.
  What does NOT depend on that single read, and is independently verifiable from code at any time:
  `userMessages` drops every non-`user` message (`arenas/live/aisotropy/main.go:359-367`), and the
  competing adapters replay the canned assistant turns verbatim (`arenas/live/eino/mode_memory.go:44-66`
  and siblings). The *mechanism* is code-grounded; the *observed refusal text* is not re-checkable.
  Treat the refusal cascade as strongly evidenced but single-sourced until the post-fix re-run either
  reproduces the old behaviour on the pinned commit or shows the score moving as this document
  predicts. Fixing the pinned data directory (§3.4) also fixes this evidence-preservation problem —
  a per-run directory would have kept the artifact.
- **Whether the untrusted-context fence materially causes the refusals is INFERRED, not proven.** The
  fence (`internal/api/chat_stream.go:51-63`) marks retrieved/prior-turn content as untrusted data the
  model must not treat as instructions. It is plausible that a model shown its own prior refusals
  through this fence is primed to keep refusing, and equally plausible the refusals are driven purely
  by the missing canned-booking context (§3.1) with the fence playing no material role. No A/B
  isolating the fence was run in this investigation.
- **Which of the three `mem-h03-longhaul` non-determinism sources (§4) actually flipped the earlier
  pass to this run's fail is INFERRED, not proven.** All three (summarizer non-determinism, the
  `Close`/`wg.Wait()` ordering bug, cross-run window drift) are independently real and file-grounded;
  which one (or combination) produced this specific run's outcome versus the earlier calibration
  run's pass was not isolated.
- **The exact per-call token split is NOT independently re-derived.** §3.5 verifies the mechanism
  (per-turn replay multiplies call count; reflection retries and the summarizer add billable calls
  the whole-history frameworks never make) but does not re-derive the brief's specific "47 calls
  aisotropy vs. 16 for a competitor" or "≈880 input tokens/call" figures — no raw per-invocation call
  log was available in this repository to check them against. The byte-to-token approximation implied
  by such a figure (~4 bytes/token) is a standard rule of thumb, not something measured here, and it
  ignores whatever fixed schema/prompt overhead gollem's `buildGollemPrompt`
  (`internal/llm/gollem.go:342-377`) adds per call, which was not separately measured.
- **Whether `mem-h02-interference`, `mem-h04-multifact`, and `mem-h05-derived` passed despite or
  because of the same mechanisms is INFERRED, not proven.** All three passed for aisotropy
  (`arenas/live/RESULTS.md:585,587,588`), but this document did not open their BadgerDB sessions the
  way it did for `mem-h01`/`mem-h03` — it is not established whether they passed because the same
  refusal/condensation mechanisms simply did not trigger for their specific fact patterns, or because
  the mechanisms triggered and the model recovered anyway.
- **The `rqlite_reset.go` staleness finding (§3.3) is new to this document** — it was not part of the
  original brief and was found by directly checking the file's own justifying comment against the
  code it describes, in the course of writing this report. It has not been independently re-reviewed.

## 10. Method — code-truth, and why this is the third time this session

`.claude/rules/prune-evidence-gate.md`'s discipline — a code claim needs executable evidence, not a
document read or a plausible inference — is not an abstract principle in this investigation. It is
the difference between §2 (wrong) and §3 (right) in the same document. Three times in this session, a
"framework A does X differently than framework B" observation turned out, on inspection, to be an
adapter or contract artifact rather than a genuine capability difference:

1. **A stale `arenas/live/SPEC.md` caused 7 adapter bugs** (tool-result reporting and stub-model
   labeling), fixed in commit `8c8aaff` ("arena(SPEC): fix the stale contract that caused 7 adapter
   bugs (tool result + stub model)").
2. **`tool-h-abstain`** (`arenas/live/runner/tasks.go:62`, "What is the capital city of Australia?" /
   expected answer `Canberra`, expecting the model to answer from its own knowledge rather than call
   a tool) failed for aisotropy not because the model lacked the answer, but because
   `RunToolAgent` (`internal/llm/toolagent.go:82-95`) discards the agent's final free-text answer —
   `steps, _, err := RunToolAgentWithOutput(...)` at line 93 throws away the third return value — and
   the arena adapter substitutes a canned placeholder whenever zero tools were called
   (`arenas/live/aisotropy/mode_tools.go:220-227`, `"No tool was executed for this request; the agent
   answered directly."`) instead of the model's real answer. The model correctly abstained from
   calling a tool; the adapter then threw away the evidence that it also knew the answer.
3. **This document's `mem-h01`/`mem-h03` investigation** (§2 vs §3): a plausible, constants-grounded
   scoring-formula hypothesis that was wrong, superseded by opening the actual persisted store.

The pattern across all three: a difference that looks like "framework A's capability is worse than
framework B's" is, on inspection, "the harness treats A and B asymmetrically" or "a specific code path
discards information before it reaches the scored output." Reading the code that actually ran, not
the documentation or the plausible story about what should have run, is what surfaced the real cause
in all three cases. This document's own §3.3 finding (a stale justifying comment, found while writing
this very report) is a fourth, smaller instance of the same thing happening in real time.

**Correction (this is now the fourth time, not the third):** a fourth full instance surfaced after
this document was written — `tool-h-chain-1` "failing" (read as "aisotropy can't chain tool calls")
was, like item 2 above, the adapter fabricating `out.Reply` from a raw tool result instead of the
model's real answer, not a capability gap (the sibling `tool-h-chain-2` passes on the same code path).
That instance is diagnosed but, unlike items 1–3, **not yet fixed**. The full list of four, with the
verdict/fix status for each, now lives in `docs/architecture/arena-adapter-artifacts.md` — treat that
document as the up-to-date index of this pattern rather than renumbering this section further.
