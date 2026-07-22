---
type: Process
title: "Arena Adapter Artifacts — four times the harness lied about the product"
description: Across one working session on the live arena, four separate "aisotropy is worse than framework X" results turned out to be artifacts of aisotropy's own arena adapter or a stale contract doc, not real capability differences — a stale SPEC.md (7 adapters), a fabricated tool-mode reply on an abstain task, a discarded cooperative-turn replay in the memory suite, and the same fabricated-reply bug breaking a chained tool-use task (since fixed — see §6). This document is the repeatable discipline that caught each one, plus the two adoption-surface holes the arena shook out along the way.
tags: [architecture, process, arena, adapters, post-mortem, code-truth]
timestamp: 2026-07-22T00:00:00Z
---

# Arena Adapter Artifacts — four times the harness lied about the product

> **Why this document exists, and why it is not softened.** This repository is about to become
> public. Four times in one working session, a live-arena result read as "aisotropy's product is
> worse than a competing framework's" and turned out, on inspection, to be a bug in aisotropy's own
> arena adapter (`arenas/live/aisotropy/`) or in the arena's contract document (`arenas/live/SPEC.md`)
> — not a defect in `internal/`, `aisotropy/`, or the LLM-OS core those packages implement. The
> operator's instruction is explicit: document this candidly as an engineering post-mortem, including
> the one instance that is diagnosed but **not yet fixed**. The point of this document is not "the
> product is fine" — it is the opposite: a benchmark harness is code, code has bugs, and a harness bug
> that goes uncaught silently mislabels a working feature as broken. The value of this session was not
> any single fix; it was noticing the SAME failure shape four times and turning it into a rule.

## 1. What actually happened, in one table

Every row below is independently checked against `git show`/`git log`/source, not copied from a prior
report. "Verdict" states where the defect actually lived — never the product path — unless stated
otherwise.

| # | Symptom (what the board showed) | What it looked like | Actual cause | Verdict | Where fixed |
|---|---|---|---|---|---|
| 1 | Seven adapters (genkit-go, pydantic-ai, mastra, ai-sdk, llamaindex, langgraph, eino) scored 0 on tool-exec or reported the wrong model name under the offline stub | Seven independent frameworks all failing the same dimension | `arenas/live/SPEC.md` documented the wrong wire contract: no `result` field in the `tool_calls` schema, rule 2 not marked mode-dependent, and rule 4 ("model echoes `OPENAI_MODEL`") not qualified for the offline stub lane | **DOC** (stale contract, then 4 adapter bugs it caused) | `8c8aaff` (SPEC fix) + `6dcf6a7` (mastra, ai-sdk) + `fccda84` (llamaindex, pydantic-ai); genkit-go's own fix is referenced inside `6dcf6a7`'s message as `adf2311`, pydantic-ai's numeric-result variant as `c510fb5` |
| 2 | `tool-h-abstain` ✗ for aisotropy: "aisotropy always calls the tool" | A framework hardwired to over-eager tool use, scoring 0 on the one negative item in the suite | The model correctly abstained (`arenas/live/RESULTS.md:601` shows `exec 6/7` — the SAME abstain rate as passing frameworks). The adapter then discarded the model's real answer ("Canberra") and substituted a canned placeholder string whenever zero tools were called | **ADAPTER** | Root cause identified and documented (`docs/architecture/arena-memory-forensics.md` §10, item 2); the adapter substitution itself (`mode_tools.go:224`) is still present — see §4 below |
| 3 | `memory` 6/8, worst of eight frameworks, at 41,292 input tokens (~2.85x the nearest competitor) | A recency/condensation defect in the working-memory tier | A read-only dump of the arena's BadgerDB store showed the model in a refusal cascade: the adapter's turn-replay (`arenas/live/aisotropy/main.go`, `userMessages`, filters `Role == "user"` only — see line ~370 today, `main.go:359-367` at fix time) discarded the suite's canned cooperative assistant turns, so the model was repeatedly asked to PERFORM an already-declined action and its own refusal got persisted and replayed | **ADAPTER** (compounded by a real, separately-tracked product gap — see §5) | `14355a7`; board republished at `8154f63` showing memory 8/8 |
| 4 | `tool-h-chain-1` ✗ for aisotropy: "aisotropy can't chain tool calls" | A framework whose tool-use loop is single-shot, unable to feed one call's result into the next | The loop DOES chain — `internal/agentloop/agentloop.go:174`'s `for range l.MaxSteps` feeds `result` from one step into `current`, the prompt for the next; `aisotropy/app.go:58` sets `agentMaxSteps = 3`, enough for a 2-call chain. `tool-h-chain-2`, the sibling 2-call-chain task, PASSES (`arenas/live/RESULTS.md:599`, "exec+correct ✓"). The adapter's reply is fabricated from the LAST tool's raw result (`mode_tools.go:230`, `fmt.Sprintf("The %s tool was executed and returned %s.", ..., lastResult)`) instead of the model's own final answer — the same `out.Reply` fabrication as #2, not a chaining defect | **ADAPTER — fixed** | `48dbf62` + `674c0e8`; validated 7/7, see §6 |

## 2. The Q0 discipline — the thing worth keeping

Every one of the four rows above was caught by asking one question BEFORE drawing any conclusion
about the product:

> **Q0: is the defect in the CORE (`internal/`, `aisotropy/`) or in the arena ADAPTER
> (`arenas/live/aisotropy/`) or its DOC (`arenas/live/SPEC.md`)?**

This is answered with `file:line`, not narrative, and it is answered FIRST — before any sentence is
written about aisotropy-the-product's capability. State it as a standing rule for arena work: **no
"aisotropy does/doesn't do X" claim ships from a `RESULTS.md` diff until Q0 has been asked and
answered against the code that produced the row.**

Three sub-disciplines make Q0 answerable in practice, each demonstrated by a row above:

- **Follow the number, not the score.** Row 2's `exec 6/7` count (the raw tool-call rate, buried in
  the RESULTS.md detail column) refuted "always calls the tool" before a single line of adapter code
  was read. The pass/fail score said one thing; the underlying counter said another, and the counter
  was right. A score is a derived value — read what it was derived FROM.
- **Code is truth; docs and memory are hypotheses.** Row 1 was a documentation file
  (`arenas/live/SPEC.md`) actively lying about the wire contract, and seven independent adapters
  faithfully implemented the lie. This is the same discipline `.claude/rules/prune-evidence-gate.md`
  states for deletion claims ("code + tests + `deadcode` are the truth"), extended here to contract
  claims: a SPEC is a hypothesis about the code until an adapter's actual request/response is checked
  against the scorer that reads it.
- **The asymmetry clue.** Row 4's `tool-h-chain-2` passing while `tool-h-chain-1` failed is, by
  itself, strong evidence against a capability explanation: if the core agent loop genuinely could not
  chain tool calls, BOTH 2-call-chain tasks would fail identically. A sibling task passing under the
  same code path is a standing invitation to look at what's different about the ONE input, not the
  whole subsystem — in this case, nothing about the input; the same reply-fabrication line just
  happened to produce a value `numberPresent` couldn't match for chain-1 and could for chain-2.

And the meta-lesson underneath all four: **a benchmark whose adapter is written by the same team as
the product it measures is measuring the adapter until proven otherwise.** Once the plumbing in rows
1–3 was corrected, aisotropy's scores moved to parity with the frameworks it had appeared to lose to
— which is also why the `qa` dimension was redesigned after saturating at "47/48 framework × mode
cells at a perfect 1.0" (`arenas/live/runner/suite_test.go:18`): a maximally-easy suite doesn't
discriminate between frameworks either, it just proves every adapter's plumbing works.

## 3. Doc versus adapter versus core — why the distinction matters

`arenas/live/SPEC.md` (row 1) and `arenas/live/aisotropy/*.go` (rows 2–4) are both **arena-only**
code and content: neither ships to a consumer of the `aisotropy/` package. A bug in either is
embarrassing (it produced a public-looking board with a wrong number on it) but it is not a
regression in the product. Conflating the two — reading "aisotropy scored worse at memory" as "the
aisotropy product's memory tier is worse" — is exactly the mistake this document exists to prevent
future readers of `RESULTS.md` from making. The verdict column in §1 is the load-bearing part of this
document for that reason: three of four rows are ADAPTER/DOC, meaning the product code
(`internal/memory`, `internal/agentloop`, `internal/llm`) was not at fault at all.

## 4. Row 2 in more depth — where the placeholder actually lives

`arenas/live/aisotropy/mode_tools.go:223-224`:

```go
if len(out.ToolCalls) == 0 {
    out.Reply = "No tool was executed for this request; the agent answered directly."
```

This literal string replaces whatever the model actually said. The model's real free-text answer was,
until `2232d37` (see §5), not even retrievable: `internal/llm/toolagent.go:91-94`'s `RunToolAgent`
discarded it by design —

```go
// Thin, stable API: RunToolAgent is RunToolAgentWithOutput with the final
// free-text answer discarded (callers that only need the []ToolStep + error).
steps, _, err := RunToolAgentWithOutput(ctx, client, tracer, system, prompt, tools, exec, maxSteps)
```

So row 2's fix is really two bugs stacked: the core discarded the answer, and the adapter papered over
the resulting gap with a placeholder instead of surfacing the absence. §5 covers the core fix; the
adapter side (making `mode_tools.go` consume the now-available answer) is not yet done — see §6.

## 5. The two adoption-surface holes this shook out — these ARE fixed and committed

The arena is not only a competitive benchmark; in this session it worked as a debugging instrument
that exercised the `aisotropy/` public adoption surface no unit test covered. `go test ./...` was
green the entire time both of these holes existed — neither was a test failure, both were an absent
capability a third-party integrator would hit on day one.

- **`POST /chat/agent` could not return the model's answer when no tool was called.** Both dispatch
  paths were affected. The native path (`ToolCaller`) had gollem's own `result.Output` computed and
  then discarded — `internal/llm/toolagent.go:91-94` above. The fallback path (`internal/agentloop`,
  used when the client is not a `ToolCaller`) had a deeper, distinct defect: its model is a pure
  tool-selector forced to emit `{"tool":"","args":{}}` with nothing to return AT ALL — there was no
  answer to surface even if the plumbing had kept it. Fixed additively in `2232d37`
  ("feat(adoption): /chat/agent returns the model's final answer, both dispatch paths"): the response
  body gains an optional `final_answer` field (`json:"final_answer,omitempty"`, required stays
  `[tenant_id,steps]` — no breaking OpenAPI change), the native path switches to the
  output-capturing call, and the fallback path's structured contract grows a `Decision.Answer` field
  surfaced via `agentloop.FinalAnswer`. A separate latent bug was found and fixed in the same commit:
  `SelectTool` was returning a fresh `Decision` that dropped the parsed answer.

- **No way to preload a prior conversation into working memory without a model call per turn.** A
  third party doing a support handoff or "resume this conversation" flow had no seam for it — the
  facade exposed `IngestKnowledge`/`IngestFile`, which write only the SEMANTIC tier
  (`knowledge.go:205`), never working memory. This is the same wall the arena's memory adapter hit in
  row 3: no seam to replay canned history meant the adapter fell back to re-generating each assistant
  turn through the model, which is what produced both the refusal cascade and the inflated token
  count. Fixed additively in `71e9ee9` ("feat(adoption): App.SeedHistory — preload a prior
  conversation with no model call"): `App.SeedHistory(ctx, tenant, user, session, msgs...)` performs N
  pure `WorkingMemory.Append` writes (`internal/memory/working_memory.go:317`), zero model calls,
  scoped to the caller-named `(tenant, user, session)` with `memory.Key.Validate` rejecting
  empty/reserved identity — no cross-tenant write path. The documented limit
  (`aisotropy/seed_history.go:71-78`): working memory bounds its active buffer to roughly 12 messages
  (`defaultMaxActiveMessages`); seeding more than that into one session trips the SAME background
  condensation the live pipeline uses, which under the default summarizer is an async LLM call. Seed
  within the active window, or register a deterministic `WithSummarizer`, to keep seeding a genuine
  zero-model-call operation.

Both fixes are the same meta-point stated twice: the arena is a debugging instrument for the adoption
surface, and both holes were invisible to `go test ./...` because no existing test exercised a
third-party integrator's actual call pattern.

## 6. What was closed after this document was first written

Both items this section originally listed as open are now closed. The original text is preserved
in git history; the closure evidence is recorded here because a doc that says "pending" forever
is its own kind of stale.

- **Row 4 is FIXED, and the fix went deeper than consuming `final_answer`.** The variance
  experiment (below) showed the entire aisotropy tools surface flipping verdicts across identical
  runs (chain-1 FAIL/FAIL/PASS, chain-2 PASS/FAIL/FAIL, coerce PASS/ERR/FAIL, abstain
  FAIL/ERR/ERR) while all seven competitors were stable. The root cause was one level below the
  fabricated reply: the adapter's hand-rolled usage-counting client implemented only `StreamChat`,
  so the `ToolCaller` assertion in `internal/api/chat_agent.go` failed and the real lane silently
  downgraded to the `agentloop` JSON fallback — 3x the model calls (the observed 500s) and
  free-text JSON parsing (the observed flips) — the Go interface-upgrade-loss trap
  (cf. `http.ResponseController`, Go 1.20). Fixed in three additive layers: `48dbf62` (product:
  `Usage` on `ToolCallResult`, the public `aisotropy.NewOpenAICompatLLM` constructor, a boot-time
  WARN when the composed client lacks `ToolCaller`) and `674c0e8` (adapter: a
  ToolCaller-preserving usage decorator replacing ~120 lines of hand-rolled SSE, plus
  `mode_tools.go` consuming `final_answer` on both branches, with a compile-time
  `var _ aisotropy.ToolCaller` guard). Real-lane validation: **7/7 tools including all four
  previously-unstable tasks, exactly 1 model call per task (was up to 3), zero downgrade WARNs,
  positive provider usage on the native tool path, and `final_answer` carrying the chained
  `30826914`** — settling every residual the diagnosis had left open.
- **The variance experiment completed: 622 of 630 cells (98.7%) returned the same verdict across
  three identical full runs.** Of the 8 unstable cells, 4 were the aisotropy tools surface (root
  cause above, since fixed), 3 were competitor transients (ERR in one run, PASS in the others),
  and 1 was a refusal-trap judgment flip (`langgraph/rag/q9`) — model stochasticity, not code.
  Memory was 8/8 stable across all three runs, which is what justified NOT building the
  structural needle-extraction fix: the summarizer's preservation contract holds under repetition,
  not just once.

## Uncertainties

- Row 4's `numberPresent` mismatch (why `lastResult`'s value specifically fails to match `30826914`
  in `out.Reply` for chain-1 but the equivalent construction succeeds for chain-2) was not traced
  character-by-character — the row's verdict rests on the code-level fact that `out.Reply` is
  fabricated from a raw tool result rather than the model's answer, not on a byte-level replay of the
  exact string mismatch.
- The `2232d37`/`71e9ee9` commit messages are the primary source for the adoption-hole descriptions in
  §5; their test coverage was not independently re-run in the course of writing this document (no
  build or test command was executed — this is a documentation-only pass, per the assignment scope).
- Row 1's per-adapter fix attribution for genkit-go (`adf2311`) and pydantic-ai's numeric-result
  variant (`c510fb5`) is taken from `6dcf6a7`'s own commit message, not independently verified against
  `git show adf2311`/`git show c510fb5` in this pass.

## See also

- `docs/architecture/arena-memory-forensics.md` — the full row-3 investigation (recency-hypothesis
  dead end, then the BadgerDB dump that found the refusal cascade).
- `docs/architecture/arena-deterministic-judging.md` — why the arena's primary score is a pure
  function, not an LLM judge (a related but distinct discipline: that document guards against a
  non-deterministic SCORER; this one guards against a broken ADAPTER feeding a correct scorer bad
  input).
- `.claude/rules/prune-evidence-gate.md` — the code-truth discipline this document's Q0 rule is a
  direct application of.
- `arenas/live/SPEC.md` — the corrected wire contract (row 1).
