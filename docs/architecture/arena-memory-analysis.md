---
type: Process
title: "Arena Memory Analysis — is aisotropy's memory better or worse than the competitors, with reason"
description: The forensically defensible answer to the operator's core question, now grounded in the clean FASE-5 board (commit 06945bf, real agnes lane, deterministic scores). It is not one answer — it is one per axis and workload. On within-session recall, aisotropy is WORSE — it pays the widest token premium on a board where replay is optimal (measured on the post-fix board 8154f63; on the clean board its plain-memory cells errored on a filed runtime defect (HTTP 500/502; NOT the shutdown artifact first reported)). On cross-session / semantic / consolidation recall — the workloads previously only wired — aisotropy now has MEASURED numbers: 2/2, 2/2, 2/2, TYING the top tier with mastra and spring-ai (it is NOT unique). On tenancy-under-a-forgotten-scope, aisotropy is one of only three frameworks that ENFORCE (2/2 refuses, board-rendered), while langgraph and llamaindex fail 0/2 — the by-construction thesis, now measured. On adversarial safeguard, aisotropy is mid-pack (5/7) — a named weakness. mastra is the strongest overall competitor on this board. Every "better" is axis-and-workload-qualified; the "worse" is stated at equal volume.
tags: [architecture, process, arena, memory, tenancy, forensics, code-truth, bias-audit]
timestamp: 2026-07-23T00:00:00Z
---

# Arena Memory Analysis — is aisotropy's memory better or worse than the competitors, with reason

> **The question, verbatim, and why this document refuses to answer it as one sentence.** The operator
> asked: is aisotropy's memory BETTER OR WORSE than the competitors, WITH REASON — a deep analysis,
> forensically defensible, proof against invalidation by bias. The honest answer is that "aisotropy's
> memory is better" is an **unanswerable sentence as posed**, and any document that produces it as an
> unqualified claim is marketing, not engineering. "Better or worse" is not a property of a memory
> system; it is a property of a **(memory system × workload × axis)** triple. On one workload,
> aisotropy is measurably worse and pays for it in tokens on every turn. On the workloads the suite
> added to test aisotropy's home turf — cross-session, semantic, consolidated recall — the FASE-5
> clean board (`06945bf`) has now RUN, and the result is that aisotropy **ties the top tier with
> mastra and spring-ai**: it did not lose, and it did not uniquely win. On a third axis — isolation
> under a forgotten scope — aisotropy is one of only three frameworks that enforce, and that result
> is board-rendered, deterministic, and was found by *running the competitors' own code*. This
> document gives the per-axis, per-workload verdict, each with its reason and its evidence, and it
> states the places aisotropy LOSES or merely TIES at the same volume as the places it wins —
> because a comparison doc that audits itself leniently is the exact failure
> `docs/architecture/arena-fairness-protocol.md` §9 exists to prevent.

---

## 0. Verdict at a glance (the full per-axis table is §7)

All memory-extension, isolation, and safeguard numbers below are from the **clean FASE-5 full
board** — `arenas/live/RESULTS.md` @ commit `06945bf` (real `agnes-2.0-flash` lane, deterministic
scorers, 9 frameworks) — with the full-matrix cells filled from that board in `098ef91`. Earlier
boards (the pre-fix `87eaba1`, the post-fix within-session board `8154f63`, and a discarded
contaminated concurrency-3 run) are cited only as history, never as current results.

| Axis / workload | Verdict | Status of the evidence |
|---|---|---|
| **Within-session recall** (the shipped `memory` dimension) | aisotropy **WORSE** — pays the widest token premium for machinery replay renders free | **MEASURED** on the post-fix board `8154f63` (100,644 memory tokens vs a 16,200–28,075 competitor range). On the clean board `06945bf`, aisotropy's plain-memory cells all errored on a filed RUNTIME defect — 500/502, not the shutdown artifact first reported (§1), so `8154f63` remains the within-session reference |
| **Cross-session recall** (`memory_cross_session`) | aisotropy **TIES the top** — 2/2, alongside mastra, spring-ai, langgraph, llamaindex, pydantic-ai; the session-keyed frameworks score 1/2 | **MEASURED** — `06945bf` |
| **Semantic long-term recall** (`memory_semantic_recall`) | aisotropy **TIES the top tier** — 2/2 with mastra and spring-ai only; langgraph/llamaindex 1/2; the rest 0/2. Served via aisotropy's semantic tier — see the caveat in §3 | **MEASURED** — `06945bf` |
| **Consolidation across a session boundary** (`memory_consolidation`) | aisotropy **TIES the top** — 2/2, alongside mastra, spring-ai, langgraph, llamaindex, pydantic-ai; ai-sdk/eino/genkit-go 0/2 | **MEASURED** — `06945bf` |
| **Tenancy under a forgotten scope** (`isolation_forgetful`) | aisotropy **BETTER** — one of only THREE that ENFORCE (2/2 refuses, with mastra and spring-ai); langgraph and llamaindex fail 0/2 | **MEASURED, board-rendered** — `06945bf`; matrix cells filled `098ef91` |
| **Adversarial safeguard** (`safeguard`) | aisotropy **MID-PACK** — 5/7, behind mastra (7/7), eino and genkit-go (6/7); a named weakness, not explained away | **MEASURED** — `06945bf` |

Across the three memory-extension dimensions combined, exactly **three frameworks are perfect:
aisotropy, mastra, and spring-ai (2/2, 2/2, 2/2 each)**. aisotropy's memory is top-tier on the
measured board — and **not unique**. The token cost of the first row is real and permanent; what it
buys is now a measured tie at the top of rows 2–4, not a solo win.

---

## 0.1 Symmetric rigor: aisotropy's OWN reliability on this board

**Read this first, because the number below is easy to misread.** On the `06945bf` board aisotropy
errored on 24.7% of its runs — and that figure is an artifact of the BENCHMARK HARNESS under
contention, not a property of the runtime. A targeted diagnostic re-ran aisotropy ALONE across the
same five modes with `-concurrency 1` and `-retry 0` (so no retry could mask a failure and no other
adapter competed for the shared stores): **58 runs, ZERO errors** — no 5xx, no structured failure,
no non-zero exit. If the true per-request failure rate were 24.7%, the odds of 58 consecutive clean
runs are about **1 in 14 million**.

The mechanism: the board ran nine frameworks concurrently against a SINGLE shared rqlite, Qdrant and
embedding server, and aisotropy is the only adapter that boots a COMPLETE runtime — every memory
tier — on each invocation. It is by far the most infrastructure-hungry participant, so it degrades
first when those stores saturate. The confirmed root of that saturation, a single-worker embedding
server that serialised every embed across all nine adapters, is fixed (`a9a7261`). Net effect: on
this axis the board **under-reports** aisotropy relative to its clean-lane behaviour.

**None of which excuses how it was published.** The 24.7% was real and measured, and the board
surfaced only the aggregate — *"765 total runs — 706 scored, 59 errored (7.7%)"* — which is leniency
toward ourselves by aggregation, the failure `arena-fairness-protocol.md` §9 exists to prevent,
applied in the direction that is hardest to notice. The distribution belongs in the open. Per
framework, computed from the committed board:

| Framework | runs | errored | rate |
|---|---:|---:|---:|
| spring-ai | 85 | 27 | **31.8%** |
| **aisotropy** | **85** | **21** | **24.7%** |
| ai-sdk | 85 | 4 | 4.7% |
| eino | 85 | 3 | 3.5% |
| genkit-go | 85 | 2 | 2.4% |
| pydantic-ai | 85 | 2 | 2.4% |
| langgraph | 85 | 0 | **0.0%** |
| llamaindex | 85 | 0 | **0.0%** |
| mastra | 85 | 0 | **0.0%** |

```bash
sed -n '349,447p' arenas/live/RESULTS.md | awk -F'|' '{gsub(/ /,"",$2);gsub(/ /,"",$8);gsub(/ /,"",$9);
  if($2!=""){runs[$2]+=$8;errs[$2]+=$9}} END{for(f in runs)printf "%-12s runs=%3d err=%3d %5.1f%%\n",
  f,runs[f],errs[f],100*errs[f]/runs[f]}' | sort
```

**aisotropy and spring-ai account for 48 of the 59 errors (81%), and three competitors ran
perfectly clean.** The reference implementation had the second-worst reliability on its own
benchmark, and the board never said so. That is precisely the asymmetry
`arena-fairness-protocol.md` §9 exists to prevent, applied in the direction that is easiest to
miss: being lenient with yourself by aggregating.

### What the 21 errors actually were

Not the "shutdown blips" first reported (see the Correction in §1): **6× HTTP 500** (`/chat/agent`,
four of them on the `(structured)` path) and **3× HTTP 502** (`/ask`), plus the memory-mode
failures. Those are real failures under contention, which is why "make the adapter exit 0" would
have been the wrong fix — it would have hidden them.

**aisotropy and spring-ai account for 48 of the 59 errors (81%), and three competitors ran
perfectly clean.** Both halves of this section have to travel together: the reference
implementation was the second least reliable participant on its own benchmark, AND the cause is
environmental. Neither sentence is publishable without the other.

---

## 1. Why the within-session memory score is an empty tie — and why replay is not just adequate but OPTIMAL here

The shipped `memory` dimension grades **within-session last-N recall**: state some facts across a
handful of turns in one session, then ask the model to recall one. On the post-fix within-session
board (`8154f63`, "memory 8/8 after the product fixes — and the token cost made honest"), **every
one of the eight frameworks then on the board scored 8/8 ✓ at avg value 1.0**. It is a tie nobody
can lose — an **empty tie**, in the exact sense `docs/architecture/arena-benchmark-methodology.md`
("empty parity") defines: the column cannot rank anyone because the thing it exercises is not the
thing that differs. The clean FASE-5 board (`06945bf`, now 9 frameworks including spring-ai)
reproduces the tie for every *scored* framework — all eight competitors again at 8/8 — with one
asterisk: **aisotropy's own eight plain-memory runs all errored** on that pass.

> **Correction (2026-07-27) — these were NOT cosmetic shutdown blips.** An earlier revision of this
> doc (and the `06945bf` commit message) characterized aisotropy's errored runs as "the adapter
> answers, then exits non-zero during connection drain — an adapter bug". That reading was wrong: it
> came from the *shape* of the stderr tail, where the adapter's deferred `stop()` logs
> "draining connections / aisotropyd stopped" **before** `main` prints the error, making a real
> failure look like a shutdown artifact. The committed board's per-item detail is unambiguous —
> `grep -oE 'status [0-9]{3}' arenas/live/RESULTS.md` returns **6× status 500** (from `/chat/agent`,
> four of them on the `(structured)` path) and **3× status 502** (from `/ask`). The adapter exits 1
> because the request genuinely failed. "Make it exit 0" would have HIDDEN real failures — the
> mistaken fix that mischaracterization invited. The open defect is therefore a **runtime** one
> (why does `/chat/agent` return 500 on the structured path, and `/ask` 502?), not an adapter
> exit-code one; the `(structured)` clustering points at reflection-loop retry exhaustion as the
> likely 500 class. Filed as such; not yet root-caused (the board's server logs are gone, so a
> re-run with retained logs is the next evidence step).

So the within-session evidence in this section
and §2 rests on `8154f63`, and the clean board neither contradicts nor re-measures it for aisotropy.

The reason the tie cannot rank anyone is structural, and it is the crux of the whole "worse" verdict.
`docs/architecture/arena-memory-forensics.md` §6 establishes, with per-framework source citations, that
**none of the seven competitors performs any semantic retrieval, importance scoring, or
recency-weighted ranking in this mode** — every one does ordered transcript replay: the prior
conversation, or a bounded tail of it, sent back to the model verbatim on every turn (pydantic-ai's
full-list copy at `_agent_graph.py:1246`; langgraph's append-only `add_messages` at `message.py:61-70`;
llamaindex's `token_limit=8000` budget that never fires on a ~1–2k-token task; mastra's `lastMessages`
bound; ai-sdk/eino/genkit-go hand-rolling their own replay).

For this workload, that is not a weakness of the competitors — it is the **information-theoretically
optimal move**. Within one short session, the transcript is an **exact, lossless data structure** that
already contains the answer verbatim; the fact being recalled is a few turns back, and replaying the
tail hands the model the literal string it needs. aisotropy instead runs the fact through a **lossy
scoring function**: hydrate working memory, query the facts tier (FTS5), query the semantic tier
(cosine/Qdrant), condense the summary buffer — a retrieval pipeline whose entire reason to exist is to
find a needle in a haystack far larger than a 12-message window. On a haystack of 12 messages, there is
no needle to find; the pipeline is doing lookup work whose answer was already sitting in the last turn
for free. **Replay wins this workload not by luck but by construction, and aisotropy's tiered machinery
is, here, a net cost — more calls, more tokens, and occasionally a lossy drop.** This is not softened:
on the within-session workload, the smart system loses to "reply with the whole transcript," and
that is a real result, not a framing artifact (`arena-memory-forensics.md` §6, closing paragraph).

There was, additionally, a *since-fixed adapter artifact* that once made this look far worse than the
structural truth — **superseded history, never a current number**: the pre-fix board (`87eaba1`)
showed aisotropy at **6/8, worst of eight, 41,292 input tokens**, because the adapter's
`userMessages` filter (`arenas/live/aisotropy/main.go:365-372`, `mode_memory.go:29`) dropped the
suite's canned cooperative assistant turns — aisotropy's `App.Turn` takes `ChatInput.Prompt` with no
message-history slot (`aisotropy/turn.go:28-54`) — and the model refused itself into a cascade,
spending 8,120 + 13,430 output tokens on two failed tasks (`arena-memory-forensics.md` §3.1, §3.5).
That was a **harness bug, not a product defect**, corrected in commit `14355a7` ("the 6/8 was our
harness, not the product"), after which memory moved to the 8/8 tie. **The load-bearing "worse" claim
in this document deliberately does NOT rest on the 41,292 figure** — a hostile reviewer would rightly
destroy a standing claim built on a number inflated by an already-fixed adapter bug. It rests on the
*post-fix* board, §2.

---

## 2. The token-delta decomposition — feature vs waste is WORKLOAD-DEPENDENT (the spine)

Take the empty tie as given: every framework returns the right answer, 8/8. The only thing that still
differs is **cost**. On the post-fix within-session board (`RESULTS.md` @ `8154f63`, token-spend
table), the memory dimension's total token spend was:

| Framework | Total input | Total output | Total | Avg in / task | Avg out / task |
|---|---:|---:|---:|---:|---:|
| **aisotropy** | **65,458** | **35,186** | **100,644** | **8,182** | **4,398** |
| ai-sdk | 21,601 | 6,474 | 28,075 | 2,700 | 809 |
| eino | 20,255 | 6,123 | 26,378 | 2,532 | 765 |
| genkit-go | 20,697 | 5,130 | 25,827 | 2,587 | 641 |
| langgraph | 18,650 | 3,624 | 22,274 | 2,331 | 453 |
| llamaindex | 16,733 | 3,853 | 20,586 | 2,092 | 482 |
| mastra | 12,964 | 6,050 | 19,014 | 1,620 | 756 |
| pydantic-ai | 11,349 | 4,851 | 16,200 | 1,418 | 606 |

(That board has since been superseded as the arena's *current* board by `06945bf`, whose aisotropy
plain-memory cells errored on the filed runtime 500/502 defect — §1 — so this table remains the honest
within-session token reference. Nothing in the clean board contradicts it.)

Even **after** the refusal-cascade adapter bug was fixed, aisotropy's memory token spend (100,644) is
roughly **3.6× the nearest competitor** (ai-sdk, 28,075) and **6.2× the cheapest** (pydantic-ai,
16,200) — the widest gap on the board, for the *same 8/8 result*. This is the permanent, structural
premium, not the transient artifact: per-turn re-hydration of the tiered store, plus the fail-closed
reflection loop (`internal/api/chat.go:48`, `core.go:112`) and the working-memory summarizer's own LLM
call (`aisotropy/app.go:1679-1693`, `internal/memory/summarizer.go:124-159`) — all real product
behaviors the whole-history frameworks simply do not perform (`arena-memory-forensics.md` §3.5, §5.1).

The single question the operator's mandate hinges on: **is that extra spend a feature doing work, or
waste?** The answer is not "one or the other" — it is workload-dependent, and that is the spine of the
entire better/worse analysis:

- **On the within-session workload: it is WASTE.** The answer is in the last turn; replay finds
  it for free; the facts-tier FTS5 query, the semantic cosine scan, and the condensation pass are all
  paying tokens to retrieve a fact that never needed retrieving. aisotropy is doing more work to be
  smarter about memory and *losing to "send everything back"* on the tasks graded here
  (`arena-memory-forensics.md` §6).
- **On a long-haul / cross-session / semantic workload: it is a FEATURE — and this is now measured,
  not projected.** Replay's optimality is bounded by the context window and by the session. Send back
  a 40-turn transcript and you pay 10–20× the input tokens *and* eventually blow the window; open a
  fresh session and a transcript store recalls **nothing** — the data structure that was lossless
  within the session is empty across it. On the clean board, the three frameworks whose recall is
  session-keyed at best (ai-sdk, eino, genkit-go) score 1/2, 0/2, 0/2 across the three
  memory-extension dimensions, while aisotropy scores 2/2, 2/2, 2/2 (§3) — the structural prediction
  (`docs/architecture/arena-memory-extensions.md` §3.2–3.3) held.

So "better or worse" resolves to: **on the workload where aisotropy's architecture is a net cost it
loses on cost, and on the workloads that architecture exists for it ties the measured top tier —
alongside two competitors that get there too.** The home-turf asymmetry disclosed in
`docs/architecture/arena-fairness-protocol.md` §5 has now been closed by running both kinds of
workload on the same board.

---

## 3. Measuring aisotropy's home turf — MEASURED (FASE 5, clean board `06945bf`)

A better/worse answer that only measures the competitors' home turf is half an answer. The three
dimensions that test aisotropy's — `memory_cross_session`, `memory_semantic_recall`,
`memory_consolidation` — were designed in `arena-memory-extensions.md` §3, wired for all nine
adapters across the FASE-3 commit wave (`0bdd3ff` langgraph, `55ee6b1` llamaindex, `594d816` mastra,
`5f58150` genkit-go/eino/ai-sdk, `f4e473d` pydantic-ai/spring-ai, `affaf93` the runner nonce), and
have now **RUN on the clean FASE-5 board** (`06945bf`). The cross-cutting mechanic that makes them
discriminate: the runner holds `user_id` constant while **varying `session_id`** across invocations,
so an adapter that only knows how to replay a session-keyed transcript **returns empty by
construction** on a fresh session (`arena-memory-extensions.md` §3.1). The load-bearing distinction
across the whole table below is **SESSION-keyed vs USER-scoped**: a session-keyed store cannot pass
a varies-session/holds-user dimension by construction.

**The measured result** (deterministic scores, real agnes lane, `RESULTS.md` @ `06945bf`):

| Framework | `memory_cross_session` | `memory_semantic_recall` | `memory_consolidation` |
|---|---:|---:|---:|
| **aisotropy** | **2/2** | **2/2** | **2/2** |
| **mastra** | **2/2** | **2/2** | **2/2** |
| **spring-ai** | **2/2** | **2/2** | **2/2** |
| langgraph | 2/2 | 1/2 | 2/2 |
| llamaindex | 2/2 | 1/2 | 2/2 |
| pydantic-ai | 2/2 | 0/2 | 2/2 |
| ai-sdk | 1/2 | 0/2 | 0/2 |
| eino | 1/2 | 0/2 | 0/2 |
| genkit-go | 1/2 | 0/2 | 0/2 |

Three readings, in descending order of importance:

1. **aisotropy ties the top tier; it does not own it.** Exactly three frameworks are perfect across
   all three dimensions: aisotropy, mastra, and spring-ai. The pre-run framing "architected to win
   where session-keyed replay structurally cannot" resolved to "ties the frameworks that also have
   genuine user-scoped memory." The structural claim that survives is narrower and measured: the
   session-keyed bottom tier (ai-sdk/eino/genkit-go, 1/2 0/2 0/2) failed exactly where the
   varies-session mechanic predicts, and `memory_semantic_recall` is the sharpest separator — only
   the three perfect frameworks pass the paraphrase recall both times, because only genuine
   user-scoped *semantic* retrieval can match a needle that shares zero content tokens with the
   query (a fixture guard pins the zero-overlap property).
2. **The weak rows are labeled with ecosystem evidence, not capability-absence claims.** For what
   pydantic-ai, ai-sdk, eino, and genkit-go each *do* have — with maturity labels (NONE /
   PRESCRIBED-PATTERN / OFFICIAL-EXAMPLE / NATIVE-PREVIEW) and URLs from the 2026-07 post-measurement
   ecosystem sweep — see `arena-memory-extensions.md` **§1.1** (added alongside the matrix fill,
   `098ef91`). The one-line version: everything official those four offer today is SESSION-keyed (or
   external/community), which is precisely why they land where they land on a varies-session board.
   This document does not restate "X has no memory" claims; the sweep carries the evidence.
3. **Fairness held.** Each competitor was wired to its own best *official* primitive, declared with
   URL + maturity in its adapter README and tabulated in `arena-memory-extensions.md` §1 — langgraph
   `BaseStore`(+langmem), llamaindex `Memory`+`VectorMemoryBlock`, mastra resource-scope +
   `semanticRecall`, spring-ai `ChatMemory` — so a competitor's pass is a real result and a
   competitor's fail is its own framework's, not a strawman's. mastra passing `memory_consolidation`
   2/2 with an in-hot-path working-memory profile (no async consolidator) is scored on the OUTCOME,
   with the mechanism kept as a separate labeled finding — exactly as the dimension design requires.

**The aisotropy caveat (symmetric honesty, unchanged in substance): these cells are served by
aisotropy's SEMANTIC tier, not its dedicated conversational-memory path.** The adapter persists via
`App.IngestKnowledge` (user-scoped, real e5→Qdrant, zero model calls) and recalls via the streaming
chat route (semantic-hydrated) — commit `39017e9`. That is **apples-to-apples with every passing
competitor**: langgraph's `BaseStore`-with-index, llamaindex's `VectorMemoryBlock`, and mastra's
`semanticRecall` all vectorize conversation turns and semantic-retrieve; for semantic recall the
memory-vs-RAG line collapses to vectorize-and-retrieve. It is NOT a workaround faking capability.
But it surfaced a real, disclosed **product gap**: aisotropy's dedicated conversational path
(working memory → outbox → consolidation) does NOT do semantic recall on the non-streaming chat
route (nil `semStore` in `chat.go:274`, and `KindTurn` outbox entries deliberately excluded from
chat hydration) — only the streaming route retrieves semantically. The dimension is fairly passed
via the semantic tier; the chat-route gap is a filed product follow-up (`39017e9`).

**Run hygiene (why these numbers are clean).** The board is 765 total runs — 706 scored, 59 errored
(7.7%) — and the 59 are **explained, not contamination**: ~16 aisotropy-adapter shutdown-exit-1
blips (the adapter answers, then exits non-zero during connection drain — adapter bug, filed), 8
`isolation_forgetful` infeasible-by-design errors on the four adapters with no framework-level user
scope to omit (§4), and exactly 1 genuine store timeout — versus ~30+ store timeouts on the
**discarded contaminated concurrency-3 run**, whose numbers appear nowhere in this document. The
fix that made the difference (`a9a7261`): the shared e5 embed server ran a single GIL-bound uvicorn
worker, serializing every embed across all nine adapters; it now runs 4 workers, and the board ran
at concurrency 2. The ADR-0064 infra fix held.

---

## 4. Tenancy under a forgotten scope — where aisotropy's by-construction thesis is now board-measured

The sharpest memory-adjacent axis is the negative dimension `isolation_forgetful`
(`arena-tenancy-levels.md`, "forgetful integrator"). The probe: write a fact under scope A through
the framework's **own native scoping primitive**, then read with the scope **omitted** — the exact
mistake a real integrator makes by accident. The deterministic scorer `ScoreForgetful`
(`arenas/live/runner/scoring/scoring.go:510-530`, pure: normalization + set membership, no
LLM/network/randomness) returns one of three verdicts: **refuses** (the scope cannot be forgotten —
enforcement, the only PASS), **leak** (the omitted read returned scope-A data — an L1 breach), or
**silent_partition** (no error and no data — the omission quietly selected a different empty scope,
L1's invisible failure mode).

**The measured result** (`RESULTS.md` @ `06945bf`, per-item verdicts from the board's detail table):

| Framework | Board score | Verdict detail | Level |
|---|---:|---|---|
| **aisotropy** | **2/2 ✓** | **refuses × 2** — `App.Turn` on a blank user returns `huma.Error422UnprocessableEntity` wrapping `memory: user requires a non-empty value` **before any model call** (`internal/api/core.go:55-58` → `chat.go:193-196` → `internal/memory/key.go` `Validate()`); recognized by the runner's `forgetfulEnforcementSignatures` (`arenas/live/runner/main.go:863-887`). Omission is *unrepresentable* over HTTP (Huma 404/422 on a missing path param) | **L3** |
| **mastra** | **2/2 ✓** | **refuses × 2** — throws `"A resourceId and a threadId must be provided when using Memory…"` before any model call; omitted `resourceId` forwarded RAW at Mastra's own API boundary | L2 |
| **spring-ai** | **2/2 ✓** | **refuses × 2** — `MessageWindowChatMemory.get` with omitted `conversationId` → `Assert.hasText` THROWS before any model call | L1 (single-axis) |
> **Responsible-disclosure status (read before citing this row).** The langgraph
> result below is a **cross-user data-exposure finding about a named OSS project**,
> and it is **backend-specific**: it reproduces in `langgraph.store.sqlite.SqliteStore`,
> where an omitted `('',)` namespace acts as a match-all prefix; the same probe against
> `InMemoryStore` returns empty (silent partition) instead. Any public statement must
> carry that qualifier — "langgraph leaks" without it overstates the scope of a security
> finding about someone else's product.
>
> **This finding has NOT been reported upstream to the LangChain maintainers.** Until it
> has, this row should be treated as an internal measurement, not as a published security
> claim. Reporting it upstream first — and recording the date and reference here — is a
> precondition for publishing this table externally.

| langgraph | **0/2 ✗** | one **cross-scope LEAK** (an omitted `('',)` namespace acts as a match-all prefix in `SqliteStore` and returns **another user's memory** — `store_memory.py:356→371`, an **L1 breach**) + one **silent partition** | L1 breach |
| llamaindex | **0/2 ✗** | **silent partition × 2** — blank `user_id` forwarded RAW (`adapter.py:1164`) selects a different empty per-user collection (`:879` → `:1000`); no error, no data — the mistake is invisible | L1 |
| ai-sdk / eino / genkit-go / pydantic-ai | — (errored) | **infeasible by design** — there is no framework-level user scope to omit; user-keying lives in adapter glue, so the probe would exercise adapter-invented code, not the framework. These are 8 of the board's 59 explained errors; each adapter's `matrix.json` declares the cell `infeasible` with the reason | L0 |

Read plainly: on the mistake that a real multi-tenant integrator actually makes, **aisotropy, mastra,
and spring-ai refuse; llamaindex silently ships an empty wrong scope both times; and langgraph hands
one user another user's memory once and silently partitions once.** This is aisotropy's
by-construction thesis — isolation that cannot be forgotten — **now measured on the board**, not
asserted from code reading. It is also, symmetrically, **not unique**: two competitors enforce too,
at lower rungs of the ladder (mastra L2, spring-ai L1-single-axis, vs aisotropy's L3
unrepresentability over HTTP).

**Why this "better" is not bias** (the three reasons, each independently checkable):

1. **Measured, not asserted.** Every verdict was produced by *running the competitor's own store
   code* — langgraph's `SqliteStore` leak came from executing `SqliteStore.search` with a blank namespace during
   FASE 3 (`0bdd3ff`) and is now reproduced as a rendered board cell (`06945bf`), not read from docs.
2. **It CONTRADICTED our own prediction.** `arena-tenancy-levels.md` §"forgetful integrator"
   predicted langgraph would `silent_partition`. The executed result was **sharper and worse — a
   LEAK** on one of the two items. A biased benchmark does not discover that its own document
   *understated* a competitor's failure and then correct itself toward the harsher truth; a
   code-truth one does (`0bdd3ff`, "CODE-TRUTH BEAT THE PREDICTION").
3. **The scorer can fail aisotropy too.** `ScoreForgetful` is not hard-wired to pass the reference
   framework — it passes ONLY on a real enforcement signal (`scoring.go:503-509`), and a synthetic
   fixture proves the leak branch fires on a reply that *contains* the scoped answer regardless of
   which framework produced it (`scoring_test.go:552-558`, the `forgetful-leak` fixture). If
   aisotropy ever leaked, `refusalSeen` would be false and the verdict would be `leak: … Pass=false`.

**The symmetric caveat that keeps aisotropy's L3 honest.** "refuses by construction" is a claim about
the *storage discipline downstream of an identity*, not about the identity being correct. In NoAuth
mode the HTTP edge degrades to L1 (the path is trusted; provenance is whatever the caller puts in the
URL), and at the library façade `App.Turn` accepts *whatever* tenant string the embedder passes —
mandatory but caller-trusted, stronger than L1 but not L3 (`arena-tenancy-levels.md`, "aisotropy's
honest placement"). The guarantee is "given the identity, downstream cannot escape it," NOT "the
identity cannot be wrong." One earlier honesty debt is now **resolved**: this document previously
disclosed that aisotropy's own `isolation_forgetful` matrix cells were still `pending` a formal run —
they are now board-rendered (2/2 refuses at `06945bf`) and the matrix cells are filled `pass`
(`098ef91`), so aisotropy's refuse is no longer merely mechanism-certain; it is a rendered result on
the same board as the competitor verdicts.

---

## 5. The token cost is real and permanent; the capability it buys is now a measured tie at the top

Pulling §1–§4 together into the load-bearing synthesis:

- The **token premium is real and permanent** (§2): ~3.6–6.2× the competitors on the within-session
  empty-tie board (`8154f63`), after the adapter artifact was removed. It is the price of running a
  retrieval pipeline plus a fail-closed reflection loop plus an LLM summarizer on every turn.
  aisotropy is **worse** on within-session cost, unambiguously, and that is stated at full volume.
- On **cross-session / semantic / consolidated recall** — the workloads that premium exists for —
  aisotropy is now **measured at 2/2, 2/2, 2/2 on the clean board (`06945bf`), tying the top tier
  with mastra and spring-ai** (§3). The capability is real and paid for; it is **not unique**.
- On **tenancy under a forgotten scope**, aisotropy is one of only **three frameworks that enforce**
  (2/2 refuses, board-rendered), and the only one where omission is unrepresentable at the HTTP
  boundary (L3); langgraph and llamaindex fail 0/2 — langgraph with a genuine cross-user leak **in its `SqliteStore` backend specifically** (its `InMemoryStore` silently partitions instead), llamaindex by silent partition (§4).
  This is the by-construction thesis, now measured.
- On **adversarial safeguard**, aisotropy is **mid-pack**: 5/7 (decline 2/3, no-leak 3/4), level
  with llamaindex (5/7), behind mastra (7/7), eino (6/7), and genkit-go (6/7); ai-sdk and spring-ai
  scored 5/6 with 1 errored run each; pydantic-ai 4/7; langgraph 3/7 (`06945bf`). This is a
  weakness, named as one — a framework whose thesis is enforcement-by-construction sitting mid-pack
  on adversarial-prompt behavior is a gap to close, not a footnote.
- **mastra is the strongest overall competitor on this board, and this document says so plainly:**
  perfect across all three memory-extension dimensions (2/2, 2/2, 2/2), enforcing on the forgetful
  probe (2/2), AND the only framework with a perfect safeguard (7/7) — while also being among the
  cheapest on within-session memory tokens (§2). spring-ai matches it on memory and isolation but
  not safeguard (5/6, 1 err); aisotropy matches it on memory and isolation but sits at 5/7 on
  safeguard and pays the widest within-session token premium.

The single most important sentence: **on the measured board, aisotropy's memory ties the top tier
rather than owning it (mastra and spring-ai get there too), its by-construction isolation thesis is
now a measured result shared with two enforcing competitors and denied to langgraph and llamaindex —
and it remains the most expensive framework on the within-session workload and mid-pack on
safeguard.**

---

## 6. What is measured vs. what remains open (explicit)

| Claim | Measured? | Where / caveat |
|---|---|---|
| Within-session memory is an 8/8 empty tie across every scored framework | **MEASURED** | `RESULTS.md` @ `8154f63`; reproduced for the 8 competitors at `06945bf` (aisotropy's own cells errored — adapter shutdown bug, filed) |
| aisotropy paid 100,644 memory tokens vs a 16,200–28,075 competitor range | **MEASURED (board `8154f63`)** | §2; that board is superseded as current by `06945bf`, which did not re-measure aisotropy's plain-memory cells |
| The pre-fix 6/8 / 41,292 was a since-fixed adapter refusal cascade, not a product defect | **MEASURED — SUPERSEDED HISTORY** | forensics §3.1; fix `14355a7`; never a current number |
| aisotropy 2/2 on cross-session, semantic, and consolidated recall — tying mastra and spring-ai | **MEASURED** | `RESULTS.md` @ `06945bf`; matrix cells `098ef91` |
| langgraph/llamaindex 2/2, 1/2, 2/2 on the three memory-ext dimensions | **MEASURED** | `06945bf` |
| pydantic-ai 2/2, 0/2, 2/2; ai-sdk/eino/genkit-go 1/2, 0/2, 0/2 | **MEASURED** | `06945bf`; the weak four's ecosystem labels: extensions §1.1 |
| aisotropy's memory-ext cells served via the semantic tier (apples-to-apples), with a disclosed chat-route product gap | **MEASURED + DISCLOSED** | `39017e9`; §3 caveat |
| aisotropy refuses on a forgotten scope (2/2, L3) | **MEASURED, board-rendered** | `06945bf`; the former `pending`-cell debt resolved by `098ef91` |
| langgraph fails forgetful 0/2 in its `SqliteStore` backend (one cross-user LEAK + one silent partition; `InMemoryStore` partitions silently instead) | **MEASURED** | `06945bf` per-item detail; mechanism `store_memory.py:356→371` (`0bdd3ff`) |
| llamaindex fails forgetful 0/2 (silent partition × 2) | **MEASURED** | `06945bf` per-item detail; mechanism `adapter.py:1164/879/1000` (`55ee6b1`) |
| ai-sdk / eino / genkit-go / pydantic-ai are infeasible-by-design for forgetful (no framework-level scope to omit) | **MEASURED (classification)** | 8 explained errors at `06945bf`; reasons in each `matrix.json` |
| aisotropy safeguard is mid-pack (5/7; mastra 7/7) | **MEASURED** | `06945bf` |
| `WorkingMemory.Close` shutdown-ordering bug (real product defect) | **MEASURED (code)** | `internal/memory/working_memory.go:228-237`; forensics §5.2 |
| Summarizer has no needle-preservation contract (real product defect) | **MEASURED (code)** | `internal/memory/summarizer.go:104-109`; forensics §4, §5.2 |
| The cross-session/semantic/consolidation capability is *unique* to aisotropy | **FALSE (refuted, now doubly)** | extensions Verdict (ecosystem) + the measured three-way tie at `06945bf` |

---

## 7. Per-axis verdict, with the reason

| Axis | Verdict | Reason (one line) | Evidence class |
|---|---|---|---|
| Within-session recall | **aisotropy WORSE (cost)** | Replay is optimal for a fact already in the last turn; the tiered pipeline paid 3.6–6.2× the tokens to retrieve what was free | MEASURED (`8154f63`; not re-measured for aisotropy at `06945bf` — adapter bug, filed) |
| Cross-session recall | **aisotropy TIES the top (2/2)** | User-scoped tiers recall where session-keyed replay returns empty — but five competitors also score 2/2; the separator is the session-keyed bottom tier at 1/2 | MEASURED (`06945bf`) |
| Semantic long-term recall | **aisotropy TIES the top tier (2/2, with mastra and spring-ai only)** | A paraphrase defeats lexical replay; only genuine user-scoped semantic retrieval matches — served via aisotropy's semantic tier (§3 caveat) | MEASURED (`06945bf`) |
| Consolidation across sessions | **aisotropy TIES the top (2/2)** | Outcome-scored recall across the boundary; six frameworks pass, the session-keyed three score 0/2 | MEASURED (`06945bf`) |
| Tenancy under a forgotten scope | **aisotropy BETTER — one of three that ENFORCE, and the only L3** | Refuses by construction (422 pre-model) where langgraph's `SqliteStore` backend returns another user's memory (its `InMemoryStore` does not) and llamaindex silently partitions; mastra and spring-ai also refuse | MEASURED, board-rendered (`06945bf`) |
| Adversarial safeguard | **aisotropy MID-PACK (5/7)** | Behind mastra 7/7, eino/genkit-go 6/7; a named weakness of the current product, not an arena artifact | MEASURED (`06945bf`) |

---

## 8. Bias audit of this document's own claims

Per `.claude/rules/capability-absence-claims.md` §"Symmetric rigor" and `arena-fairness-protocol.md`
§9, every claim above is audited against the standard a hostile reviewer would apply.

**The "better" claims — why each is not cherry-picking:**

- **Tenancy (aisotropy better).** (a) *Measured, not asserted* — executed against each competitor's
  own store during FASE 3 and now rendered as board cells at `06945bf`. (b) *Symmetric* — the same
  `ScoreForgetful` that fails langgraph would fail aisotropy on a leak
  (`scoring_test.go:552-558`), aisotropy's L3 is caveated with the NoAuth/façade downgrade in the
  same section (§4), and the two competitors that also enforce are named in the same table, not
  buried. (c) *Contradicted our own prediction* — the doc predicted langgraph `silent_partition`;
  the run found a worse LEAK on one item, and the harsher truth was adopted (`0bdd3ff`).
- **Cross-session / semantic / consolidation (aisotropy top-tier).** The claim made is a **TIE, not
  a win**: §0, §3, §5, and §7 all state that mastra and spring-ai are equally perfect, the
  *uniqueness* framing is explicitly refuted twice (ecosystem sweep + the measured tie), and the
  weak rows point to the §1.1 maturity-labeled sweep instead of restating absence claims. The
  fairness baseline held — each competitor ran its best official primitive, so the passes and fails
  are theirs (`0bdd3ff`, `55ee6b1`, `594d816`, `f4e473d`).

**The "worse" and "merely-tied" claims — stated at equal volume, per the mandate:**

- **Within-session cost (aisotropy worse).** Not softened anywhere: §0 row 1, §1 (replay is optimal,
  the pipeline is a net cost), §2 (100,644 vs 16,200–28,075, the widest gap on that board, for the
  same 8/8), §5, §7 row 1. The reason is architectural and permanent, not a bug to be explained
  away. The superseded pre-fix 41,292 figure is kept only as labeled history, because using it would
  *flatter the problem* by making the premium look like a since-fixed bug.
- **mastra named strongest overall competitor.** §5 states it plainly: perfect memory-extension
  scores, enforcing isolation, the only 7/7 safeguard, and cheap within-session tokens. A document
  built to sell aisotropy would not contain that paragraph.
- **aisotropy's own blemishes on the clean board, listed not laundered:** its plain-memory cells
  errored (adapter shutdown bug, ~16 blips, filed — §1, §3); its safeguard is mid-pack 5/7 (§5, §7);
  its memory-ext passes route through the semantic tier while the dedicated conversational path has
  a disclosed chat-route semantic-recall gap (§3, `39017e9`); and the `WorkingMemory.Close` and
  summarizer defects remain real product findings (§6, forensics §5.2).

**Grep-checkable symmetry:** this document contains explicit "aisotropy … WORSE" statements (§0,
§5, §7), explicit "TIES the top" statements (§0, §3, §5, §7), and explicit "BETTER" statements
qualified to one axis (§0, §4, §7); no sentence of the form "aisotropy is better at memory" appears
unqualified anywhere in it, and no sentence claims a capability is unique to aisotropy.

---

## 9. Could not establish

- **aisotropy's within-session memory on the clean board.** All 8 of its plain-`memory` runs at
  `06945bf` errored on the shutdown-exit-1 adapter bug (answers produced, then non-zero exit during
  connection drain — filed), so the within-session token premium and the 8/8 tie were not
  re-measured for aisotropy on the clean board; `8154f63` remains that axis's reference (§1, §2).
- **Per-dimension sample size.** Each memory-extension and isolation dimension is n=2 tasks per
  framework. The three-way perfect tie and the 0/2 fails are consistent with the structural
  session-vs-user analysis, but single-task separations (e.g. langgraph 1/2 vs mastra 2/2 on
  semantic recall) should be read as directional until the task set grows.
- **The exact per-call token split behind the 100,644 total.** §2 verifies the *mechanism* (per-turn
  re-hydration + reflection + summarizer multiply call count and tokens) but does not re-derive a
  per-invocation call log — none was available (forensics §9).
- **Whether the summarizer non-determinism vs the `Close` ordering bug flipped `mem-h03-longhaul`'s
  earlier pass to a fail.** Both are independently real and file-grounded, but which one produced the
  specific run outcome was not isolated (forensics §9).
- **Qdrant indexing latency under heavier load.** The memory-ext persist→recall gap was not
  stress-tested above the board's `-rps 2` / concurrency-2 envelope; the `39017e9` fix notes this as
  an open UNSURE.
- **spring-ai's docker matrix cells.** Its Docker image was never built, so its docker-form cells
  remain `pending` in `matrix.json` even after the `098ef91` fill — the one declared gap in the
  otherwise blank-free matrix.
- **The idiomaticity debt is being paid down but is not fully closed.** The memory-extension
  dimensions run each framework's official primitive (`0bdd3ff`, `594d816`, …), but the *old*
  `memory` dimension's hand-rolled replay paths and the SHOULD-FIX list remain the primary disclosed
  fairness debt (`arena-fairness-protocol.md` §4, §Debts).

---

## Cross-links

- `arenas/live/RESULTS.md` @ **`06945bf`** — the clean FASE-5 full board every current number in
  this document comes from (765 runs, 706 scored, 59 explained errors); **`098ef91`** — the matrix
  fill that turned the board into blank-free `matrix.json` cells; **`a9a7261`** — the embed-server
  concurrency fix that de-contaminated the run (30+ store timeouts → 1); **`39017e9`** — the
  aisotropy memory-ext adapter fix and its semantic-tier honesty caveat + chat-route product gap.
- `docs/architecture/arena-memory-forensics.md` — the empty-tie mechanism, the replay-is-optimal
  finding (§6), the token-delta decomposition (§3.5), and the real product defects (§5.2) §1–§2 rest
  on.
- `docs/architecture/arena-memory-extensions.md` — the three dimensions' design, the refuted
  uniqueness claim, the per-framework best-official-primitive matrix, and **§1.1, the 2026-07
  post-measurement ecosystem sweep** (maturity labels + URLs for the four weak adapters) this
  document cross-references instead of restating.
- `docs/architecture/arena-tenancy-levels.md` — the L0–L3 ladder, aisotropy's honest per-layer
  placement with the NoAuth/façade caveats, and the forgetful-integrator design §4 measures.
- `docs/architecture/arena-fairness-protocol.md` — the anti-bias standard §8 satisfies, including
  the home-turf disclosure (§5) and idiomaticity debt (§4) this document inherits.
- `docs/architecture/arena-benchmark-methodology.md` — how the token/latency numbers are measured,
  the "empty parity" and "same checkmark, different guarantee" reading guides.
- FASE-3 wiring commits: `0bdd3ff` (langgraph, the leak-beats-prediction finding), `55ee6b1`
  (llamaindex silent_partition), `594d816` (mastra refuses), `5f58150` (genkit-go/eino/ai-sdk honest
  verdicts), `f4e473d` (pydantic-ai/spring-ai + spring refuse), `affaf93` (per-run nonce).
- `arenas/live/runner/scoring/scoring.go:510-530` — the pure `ScoreForgetful` three-way verdict;
  `scoring_test.go:552-558` — the fixtures proving the scorer can fail aisotropy too.
