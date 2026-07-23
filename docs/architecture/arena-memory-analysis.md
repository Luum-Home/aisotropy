---
type: Process
title: "Arena Memory Analysis — is aisotropy's memory better or worse than the competitors, with reason"
description: The forensically defensible answer to the operator's core question, built to survive a hostile skeptic. It is not one answer — it is one per axis and workload. On within-session recall (the only memory workload the live board currently RUNS), aisotropy is WORSE: it pays the widest token premium on the 8/8 empty-tie board (100,644 total vs a 16,200–28,075 competitor range) for machinery raw transcript replay renders free, because replay is not merely adequate for short within-session recall — it is optimal. On cross-session / semantic / consolidation, aisotropy is ARCHITECTED to win where session-keyed replay structurally cannot, and the three dimensions are now wired for all 9 adapters (FASE 3) — but the discriminating numbers are PENDING the real run (FASE 5), so this is stated as designed-and-wired, not proven. On tenancy-under-a-forgotten-scope, aisotropy is BETTER, MEASURED, and model-independent: running the competitors' own code, langgraph LEAKS another user's memory (an L1 breach that CONTRADICTED our own doc's prediction), llamaindex silently partitions, while aisotropy/mastra/spring-ai refuse — a genuinely-better result that is not bias because it was executed not asserted, is symmetric, and the scorer can fail aisotropy too. Every "better" is axis-and-workload-qualified; the "worse" is stated at equal volume.
tags: [architecture, process, arena, memory, tenancy, forensics, code-truth, bias-audit]
timestamp: 2026-07-23T00:00:00Z
---

# Arena Memory Analysis — is aisotropy's memory better or worse than the competitors, with reason

> **The question, verbatim, and why this document refuses to answer it as one sentence.** The operator
> asked: is aisotropy's memory BETTER OR WORSE than the competitors, WITH REASON — a deep analysis,
> forensically defensible, proof against invalidation by bias. The honest answer is that "aisotropy's
> memory is better" is an **unanswerable sentence as posed**, and any document that produces it as an
> unqualified claim is marketing, not engineering. "Better or worse" is not a property of a memory
> system; it is a property of a **(memory system × workload × axis)** triple. On one workload the
> current board runs, aisotropy is measurably worse and pays for it in tokens on every turn. On a
> different workload the board does not yet run, aisotropy is architected to win and the competitors
> are structurally weaker — but that win is **not yet a number**. On a third axis — isolation under a
> forgotten scope — aisotropy is genuinely, measurably, model-independently better, and that result is
> defensible precisely because it was found by *running the competitors' own code* and it *contradicted
> our own prediction*. This document gives the per-axis, per-workload verdict, each with its reason and
> its evidence, and it states the place aisotropy LOSES at the same volume as the places it wins —
> because a comparison doc that audits itself leniently is the exact failure
> `docs/architecture/arena-fairness-protocol.md` §9 exists to prevent.

---

## 0. Verdict at a glance (the full per-axis table is §7)

| Axis / workload | Verdict | Status of the evidence |
|---|---|---|
| **Within-session recall** (the shipped `memory` dimension) | aisotropy **WORSE** — pays the widest token premium for machinery replay renders free | **MEASURED** — current board `8154f63`, aisotropy memory token spend 100,644 vs 16,200–28,075 competitor range |
| **Cross-session recall** (`memory_cross_session`) | aisotropy architected to win; session-keyed replay returns empty by construction | **WIRED, NOT YET RUN** — 9 adapters wired FASE 3; discriminating numbers pending FASE 5 |
| **Semantic long-term recall** (`memory_semantic_recall`) | aisotropy architected to win; a paraphrase defeats lexical replay | **WIRED, NOT YET RUN** — same |
| **Consolidation across a session boundary** (`memory_consolidation`) | aisotropy architected to win; most competitors have no async consolidator | **WIRED, NOT YET RUN** — same |
| **Tenancy under a forgotten scope** (`isolation_forgetful`) | aisotropy **BETTER** — refuses by construction where langgraph leaks and llamaindex silently partitions | **MEASURED, MODEL-INDEPENDENT** — executed against each framework's own code, FASE 3 commits |

The token cost of the first row is **real and permanent**; the capability it buys only pays off on the
workloads of rows 2–4, which the suite has now *added* (FASE 3 wiring) but has *not yet run* (FASE 5).

---

## 1. Why the current memory score is an empty tie — and why replay is not just adequate but OPTIMAL here

The shipped `memory` dimension grades **within-session last-N recall**: state some facts across a
handful of turns in one session, then ask the model to recall one. On the current board
(`arenas/live/RESULTS.md:237-279`, committed `8154f63`, "memory 8/8 after the product fixes — and the
token cost made honest"), **every one of the eight frameworks scores 8/8 ✓ at avg value 1.0**. It is a
tie nobody can lose — an **empty tie**, in the exact sense `docs/architecture/arena-benchmark-methodology.md`
("empty parity") defines: the column cannot rank anyone because the thing it exercises is not the thing
that differs.

The reason it cannot rank anyone is structural, and it is the crux of the whole "worse" verdict.
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
on the workload the board runs today, the smart system loses to "reply with the whole transcript," and
that is a real result, not a framing artifact (`arena-memory-forensics.md` §6, closing paragraph).

There was, additionally, a *since-fixed adapter artifact* that once made this look far worse than the
structural truth: the pre-fix board (`87eaba1`) showed aisotropy at **6/8, worst of eight, 41,292
input tokens**, because the adapter's `userMessages` filter (`arenas/live/aisotropy/main.go:365-372`,
`mode_memory.go:29`) dropped the suite's canned cooperative assistant turns — aisotropy's `App.Turn`
takes `ChatInput.Prompt` with no message-history slot (`aisotropy/turn.go:28-54`) — and the model
refused itself into a cascade, spending 8,120 + 13,430 output tokens on two failed tasks
(`arena-memory-forensics.md` §3.1, §3.5). That was a **harness bug, not a product defect**, corrected in
commit `14355a7` ("the 6/8 was our harness, not the product"), after which memory moved to the 8/8 tie.
**The load-bearing "worse" claim in this document deliberately does NOT rest on the 41,292 figure** — a
hostile reviewer would rightly destroy a standing claim built on a number inflated by an
already-fixed adapter bug. It rests on the *post-fix* board, §2.

---

## 2. The token-delta decomposition — feature vs waste is WORKLOAD-DEPENDENT (the spine)

Take the empty tie as given: every framework returns the right answer, 8/8. The only thing that still
differs is **cost**. On the current post-fix board (`arenas/live/RESULTS.md:435-441`, token-spend table,
`8154f63`), the memory dimension's total token spend is:

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

- **On the current within-session workload: it is WASTE.** The answer is in the last turn; replay finds
  it for free; the facts-tier FTS5 query, the semantic cosine scan, and the condensation pass are all
  paying tokens to retrieve a fact that never needed retrieving. aisotropy is doing more work to be
  smarter about memory and *losing to "send everything back"* on the tasks graded here
  (`arena-memory-forensics.md` §6).
- **On a long-haul / cross-session / semantic workload: it is a FEATURE.** Replay's optimality is
  bounded by the context window and by the session. Send back a 40-turn transcript and you pay 10–20×
  the input tokens *and* eventually blow the window; open a fresh session and a transcript store recalls
  **nothing** — the data structure that was lossless within the session is empty across it. The tiered
  retrieval that is dead weight on 12 messages is the *only thing that works* when the haystack is
  40 turns or a second session (`docs/architecture/arena-memory-extensions.md` §3.2–3.3).

So "better or worse" resolves to: **the current suite runs the one workload where aisotropy's
architecture is a net cost, and does not yet run the workloads where it is the point.** That is not an
accident of framing to be explained away — it is disclosed as the primary home-turf asymmetry in
`docs/architecture/arena-fairness-protocol.md` §5, and it currently runs **against** aisotropy.

---

## 3. Measuring aisotropy's home turf — WIRED (FASE 3), NOT YET RUN (FASE 5)

A better/worse answer that only measures the competitors' home turf is half an answer. The three
dimensions that test aisotropy's — `memory_cross_session`, `memory_semantic_recall`,
`memory_consolidation` — were **designed** in `arena-memory-extensions.md` §3 and have now been
**wired for all nine adapters** across the FASE-3 commit wave (`0bdd3ff` langgraph, `55ee6b1`
llamaindex, `594d816` mastra, `5f58150` genkit-go/eino/ai-sdk, `f4e473d` pydantic-ai/spring-ai,
`affaf93` the runner nonce). The cross-cutting mechanic that makes them discriminate: the runner holds
`user_id` constant while **varying `session_id`** across invocations, so an adapter that only knows how
to replay a session-keyed transcript **returns empty by construction** on a fresh session — which is
exactly the point (`arena-memory-extensions.md` §3.1).

Where replay structurally cannot compete, and why:

- **`memory_cross_session`** — persist under session A, recall under session B, same user, separate
  processes. A session-keyed transcript store has nothing to return for session B. genkit-go and eino
  are wired **run-and-fail**: their own experimental primitives (genkit's `core/x/session`, eino's adk
  `CheckPointStore`) are session-keyed, so a fresh-session recall genuinely returns empty — the
  *measured finding* that they have no user-scoped memory, not a bug (`5f58150`). ai-sdk is `infeasible`
  (no first-party primitive at any maturity).
- **`memory_semantic_recall`** — persist ~30–40 turns with a needle stated semantically; recall with a
  **paraphrase** that shares no content tokens (a pure fixture guard pins zero non-stopword overlap,
  `arena-memory-extensions.md` §3.3). Lexical replay cannot match a paraphrase; only user-scoped
  semantic retrieval can. eino, genkit-go, pydantic-ai, ai-sdk are `infeasible` here (no semantic
  primitive); langgraph/llamaindex/mastra are wired to their native semantic paths over the shared e5
  embedder.
- **`memory_consolidation`** — persist a conversational fact, run the framework's consolidation to
  completion, recall across a session boundary. mastra's own wiring declares invocation 2 a **no-op** —
  itself a finding: mastra has an in-hot-path working-memory profile but **no async consolidator**
  (`594d816`). aisotropy drives its outbox → sleep-cycle drain.

**What must NOT be overstated, and is the hard honesty constraint of this section.** The naive claim
"cross-session / semantic / consolidation is unique to aisotropy" **does not survive** the ecosystem
sweep: langgraph, llamaindex, and mastra each have an *official* path to all or most of the three
(`arena-memory-extensions.md`, Verdict + §1 matrix). aisotropy's differentiator is that the tiers are
**default-on, integrated, and tenant-scoped by construction** — not that the capability is unobtainable
elsewhere. And critically: **the discriminating NUMBERS for these three dimensions do not exist yet.**
The adapters are wired; aisotropy's own matrix cells for all three are `pending` in every real/native/
docker slot (`arenas/live/aisotropy/matrix.json`, `memory_cross_session`/`memory_semantic_recall`/
`memory_consolidation` → `"native": "pending"`, `"docker": "pending"`; stub is `infeasible` because the
offline in-memory tier cannot cross a process boundary). **This document claims no result for rows 2–4
of §0.** It claims a wired, fairness-reviewed *design* whose numbers are pending the real run (FASE 5).
Anyone who reads §3 as "aisotropy proven to win cross-session memory" is reading a claim this document
explicitly does not make.

---

## 4. Tenancy under a forgotten scope — where a better/worse answer IS already measured, model-independently

The one place a better/worse memory answer is **already a measured result** — and needs no model call to
be one — is the negative dimension `isolation_forgetful` (`arena-tenancy-levels.md`, "forgetful
integrator"). The probe: write a fact under scope A through the framework's **own native scoping
primitive**, then read with the scope **omitted** — the exact mistake a real integrator makes by
accident. The deterministic scorer `ScoreForgetful` (`arenas/live/runner/scoring/scoring.go:510-530`,
pure: normalization + set membership, no LLM/network/randomness) returns one of three verdicts:
**refuses** (the scope cannot be forgotten — enforcement, the only PASS), **leak** (the omitted read
returned scope-A data — an L1 breach), or **silent_partition** (no error and no data — the omission
quietly selected a different empty scope, L1's invisible failure mode).

Running each framework's own code during FASE 3 produced the following, **model-independently** (each
verdict below was observed offline, before any model call):

| Framework | Verdict | Level | Evidence (executed against the framework's OWN boundary) |
|---|---|---|---|
| **aisotropy** | **refuses** | **L3** | `App.Turn` on a blank user returns `huma.Error422UnprocessableEntity` wrapping `memory: user requires a non-empty value` **before any model call** — `internal/api/core.go:55-58` → `chat.go:193-196` → `internal/memory/key.go` `Validate()`; recognized by the runner's `forgetfulEnforcementSignatures` (`arenas/live/runner/main.go:863-887`). Omission is *unrepresentable* over HTTP (Huma 404/422 on a missing path param). |
| **mastra** | **refuses** | **L2** | Throws `"A resourceId and a threadId must be provided when using Memory…"` before any model call; omitted `resourceId` forwarded RAW at Mastra's own API boundary (`isolation-forgetful.mjs:76-78,84`). Verified offline (`594d816`). |
| **spring-ai** | **refuses** | **L1** (single-axis) | `MessageWindowChatMemory.get` with omitted `conversationId` → `Assert.hasText` THROWS before any model call. The design *predicted* spring-ai refuses where others partition — confirmed (`f4e473d`). |
| **langgraph** | **LEAK** | **L1 breach** | An omitted `('',)` namespace acts as a **match-all prefix** in `SqliteStore` and returns **another user's memory** — `store_memory.py:356` (blank namespace) → `:371` (`store.search`). At the framework's own `BaseStore` boundary, not adapter glue. Verified against the real on-disk backend (`0bdd3ff`). |
| **llamaindex** | **silent_partition** | **L1** | Blank `user_id` forwarded RAW (`adapter.py:1164`) → a different empty per-user collection (`:879`) → `VectorMemoryBlock.aget` (`:1000`) returns nothing, no error. Verified offline (`55ee6b1`). |
| **ai-sdk** | **infeasible** | **L0** | No framework-owned data plane — no native scope to forget (`5f58150` / FASE-2 `5ceb742`). |
| **eino** | **infeasible** | **L0** | `CheckPointStore` keyed by an opaque id with no tenant semantics — nothing to omit at a framework boundary (`5f58150`). |
| **genkit-go** | **infeasible** | **L0** | No framework-owned data plane; session-ID transcript only (`5f58150`). |
| **pydantic-ai** | **infeasible** | **L0** | User-keying is adapter glue; there is no framework scope boundary to omit — refused to credit aisotropy's construction-refusal to a competitor that lacks the concept (`f4e473d`). |

Read plainly: on the mistake that a real multi-tenant integrator actually makes, **aisotropy refuses by
construction, mastra and spring-ai refuse, llamaindex silently ships wrong data, and langgraph hands one
user another user's memory.** This is a place aisotropy is genuinely, measurably **better** — and it is
not the shipped `memory` dimension's empty tie, because the empty tie tests *correct* code and this tests
the *forgotten-scope* case a correct-code test never exercises (`arena-benchmark-methodology.md`, "Same
checkmark, different guarantee").

**Why this "better" is not bias** (the three reasons, each independently checkable):

1. **Measured, not asserted.** Every verdict was produced by *running the competitor's own store code*,
   not by reading its docs — langgraph's leak came from executing `SqliteStore.search` with a blank
   namespace, not from a claim about it (`0bdd3ff`).
2. **It CONTRADICTED our own prediction.** `arena-tenancy-levels.md` §"forgetful integrator" predicted
   langgraph would `silent_partition`. The executed result was **sharper and worse — a LEAK**. A biased
   benchmark does not discover that its own document *understated* a competitor's failure and then
   correct itself toward the harsher truth; a code-truth one does (`0bdd3ff`, "CODE-TRUTH BEAT THE
   PREDICTION").
3. **The scorer can fail aisotropy too.** `ScoreForgetful` is not hard-wired to pass the reference
   framework — it passes ONLY on a real enforcement signal (`scoring.go:503-509`), and a synthetic
   fixture proves the leak branch fires on a reply that *contains* the scoped answer regardless of which
   framework produced it (`scoring_test.go:552-558`, the `forgetful-leak` fixture). If aisotropy ever
   leaked, `refusalSeen` would be false and the verdict would be `leak: … Pass=false`.

**The symmetric caveat that keeps aisotropy's L3 honest.** "refuses by construction" is a claim about
the *storage discipline downstream of an identity*, not about the identity being correct. In NoAuth mode
the HTTP edge degrades to L1 (the path is trusted; provenance is whatever the caller puts in the URL),
and at the library façade `App.Turn` accepts *whatever* tenant string the embedder passes — mandatory
but caller-trusted, stronger than L1 but not L3 (`arena-tenancy-levels.md`, "aisotropy's honest
placement"). The guarantee is "given the identity, downstream cannot escape it," NOT "the identity
cannot be wrong." And one honesty debt on the measurement itself: **aisotropy's own `isolation_forgetful`
matrix cells are still `pending` a formal run** (`arenas/live/aisotropy/matrix.json`), even though the
422-refuse path is offline-deterministic and wired into the runner's signature list — so aisotropy's
refuse is *mechanism-certain* (code-cited, offline-observable) but its arena cell is not yet a rendered
result the way the four competitor verdicts above are. That asymmetry is disclosed, not hidden.

---

## 5. The token cost is real and permanent; the capability it buys is workload-gated

Pulling §1–§4 together into the load-bearing synthesis:

- The **token premium is real and permanent** (§2): ~3.6–6.2× the competitors on the empty-tie board,
  after the adapter artifact was removed. It is the price of running a retrieval pipeline plus a
  fail-closed reflection loop plus an LLM summarizer on every turn.
- On the **workload the suite runs today** (within-session recall), that premium buys **nothing** — the
  answer was free in the transcript (§1). aisotropy is **worse** here, unambiguously, and that is stated
  at full volume, not buried.
- On the **workloads the suite has now added but not yet run** (cross-session, semantic, consolidation),
  the same premium is what makes recall *possible at all* where session-keyed replay returns empty (§3)
  — but this is **designed-and-wired, not measured**; the numbers are pending FASE 5.
- On **tenancy under a forgotten scope**, aisotropy is **better, measured, and model-independent** (§4)
  — the one axis where the better/worse question already has a defensible, executed answer.

The single most important sentence: **aisotropy's memory is worse on the workload the board runs and
architected-to-be-better on the workloads it does not yet run — and the only memory-adjacent axis where
"better" is already proven is isolation, not recall.**

---

## 6. What is measured vs. what is pending FASE 5 (explicit)

| Claim | Measured? | Where / caveat |
|---|---|---|
| Within-session memory is an 8/8 empty tie across all 8 frameworks | **MEASURED** | `RESULTS.md:237-279` (`8154f63`) |
| aisotropy pays 100,644 memory tokens vs 16,200–28,075 competitor range | **MEASURED** | `RESULTS.md:435-441` (`8154f63`) |
| The pre-fix 6/8 / 41,292 was a since-fixed adapter refusal cascade, not a product defect | **MEASURED** | forensics §3.1; fix `14355a7` |
| `WorkingMemory.Close` shutdown-ordering bug (real product defect) | **MEASURED (code)** | `internal/memory/working_memory.go:228-237`; forensics §5.2 |
| Summarizer has no needle-preservation contract (real product defect) | **MEASURED (code)** | `internal/memory/summarizer.go:104-109`; forensics §4, §5.2 |
| langgraph LEAKS on a forgotten scope (L1 breach) | **MEASURED, model-independent** | `0bdd3ff`, `store_memory.py:356→371` |
| llamaindex silently partitions on a forgotten scope | **MEASURED, model-independent** | `55ee6b1`, `adapter.py:1164/879/1000` |
| mastra / spring-ai refuse on a forgotten scope | **MEASURED, model-independent** | `594d816`, `f4e473d` |
| ai-sdk / eino / genkit-go / pydantic-ai are L0-infeasible for forgetful | **MEASURED (classification)** | `5f58150`, `f4e473d`, `5ceb742` |
| aisotropy refuses on a forgotten scope (L3) | **MECHANISM-CERTAIN; arena cell PENDING** | `main.go:863-867` (offline-observable); `matrix.json` cells `pending` |
| aisotropy wins cross-session recall | **PENDING FASE 5** | wired `0bdd3ff`+8; `matrix.json` `pending` |
| aisotropy wins semantic recall of a paraphrase | **PENDING FASE 5** | wired; needs real e5 + Qdrant run |
| aisotropy wins consolidation across a session boundary | **PENDING FASE 5** | wired; `matrix.json` `pending` |
| The cross-session/semantic/consolidation capability is *unique* to aisotropy | **FALSE (refuted)** | extensions Verdict — langgraph/llamaindex/mastra have official paths |

---

## 7. Per-axis verdict, with the reason

| Axis | Verdict | Reason (one line) | Evidence class |
|---|---|---|---|
| Within-session recall (today) | **aisotropy WORSE** | Replay is optimal for a fact already in the last turn; the tiered pipeline pays 3.6–6.2× the tokens to retrieve what was free | MEASURED (`8154f63`) |
| Cross-session recall | **aisotropy architected to win; UNMEASURED** | A fresh session id makes a transcript store return empty; only user-scoped tiers recall — but numbers pending | WIRED, PENDING FASE 5 |
| Semantic long-term recall | **aisotropy architected to win; UNMEASURED** | A paraphrase defeats lexical replay; only semantic retrieval matches — but numbers pending | WIRED, PENDING FASE 5 |
| Consolidation across sessions | **aisotropy architected to win; UNMEASURED** | Most competitors have no async consolidator (mastra's is declared a no-op); aisotropy drains an outbox — but numbers pending | WIRED, PENDING FASE 5 |
| Tenancy under a forgotten scope | **aisotropy BETTER** | Refuses by construction (422 pre-model) where langgraph leaks another user's memory and llamaindex silently partitions | MEASURED, model-independent |

---

## 8. Bias audit of this document's own claims

Per `.claude/rules/capability-absence-claims.md` §"Symmetric rigor" and `arena-fairness-protocol.md` §9,
every claim above is now audited against the standard a hostile reviewer would apply.

**The "better" claims — why each is not cherry-picking:**

- **Tenancy (aisotropy better).** (a) *Measured, not asserted* — executed against each competitor's own
  store, not read from docs. (b) *Symmetric* — the same `ScoreForgetful` that fails langgraph would fail
  aisotropy on a leak (`scoring_test.go:552-558`), and aisotropy's own L3 is caveated with the NoAuth/
  façade downgrade in the same section (§4). (c) *Contradicts our own prediction* — the doc predicted
  langgraph `silent_partition`; the run found a worse LEAK, and the harsher truth was adopted (`0bdd3ff`).
  (d) *Disclosed weakness in the same breath* — aisotropy's own forgetful arena cell is `pending`, not a
  rendered result; stated in §4, not hidden.
- **Cross-session / semantic / consolidation (aisotropy architected to win).** Guarded three ways: it is
  labeled **UNMEASURED / PENDING FASE 5** in §0, §3, §6, and §7 (no number claimed); the *uniqueness*
  framing is explicitly **refuted** (§3, extensions Verdict — three competitors have official paths);
  and the fairness sweep gave each competitor its *best official primitive* before wiring, not a
  strawman (`0bdd3ff` uses BaseStore+langmem, `55ee6b1` uses VectorMemoryBlock, `594d816` uses native
  resource-scoped Memory) — the §4 idiomaticity debt `arena-fairness-protocol.md` names as the primary
  open fairness debt is *being paid down* by exactly these commits.

**The "worse" claims — stated at equal volume, per the mandate:**

- **Within-session recall (aisotropy worse).** Not softened anywhere: §0 row 1, §1 (replay is optimal,
  the pipeline is a net cost), §2 (100,644 vs 16,200–28,075, the widest gap on the board, for the same
  8/8), §5 ("worse here, unambiguously"), §7 row 1. The reason is architectural and permanent, not a bug
  to be explained away.
- **Real product defects that ship to consumers.** The `WorkingMemory.Close` shutdown-ordering bug
  (`working_memory.go:228-237`) and the summarizer's missing needle-preservation contract
  (`summarizer.go:104-109`) are *aisotropy* defects, not arena artifacts — carried into §6 as MEASURED,
  because a document that only listed competitors' defects would be the marketing this repo's rules
  forbid (forensics §5.2).
- **The token premium is presented as the honest post-fix number**, not the pre-fix 41,292 that would
  have flattered the *problem* (making it look like a since-fixed bug rather than a permanent structural
  cost). Using the smaller-looking fixed number would have understated aisotropy's real, standing
  disadvantage — so the larger, current, structural 100,644 is the one this document stands on.

**Grep-checkable symmetry:** this document contains explicit "aisotropy … WORSE" statements (§0, §7) and
explicit "aisotropy … BETTER" statements (§0, §4, §7); neither direction is stated without an
axis-and-workload qualifier, and there is no unqualified sentence of the form "aisotropy is better at
memory" anywhere in it.

---

## 9. Could not establish

- **The discriminating numbers for cross-session / semantic / consolidation.** The adapters are wired
  (FASE 3) but the real run (FASE 5) has not happened; aisotropy's own matrix cells are `pending`
  (`arenas/live/aisotropy/matrix.json`). Every "architected to win" in §3/§7 is a design-and-wiring
  claim, never a measured one. The FASE-3 commit messages themselves carry MUST-CHECK lists of exactly
  what the real run must confirm (e.g. `55ee6b1`: whether `VectorMemoryBlock` actually ranks the
  paraphrased needle top-k across the process boundary; `594d816`: whether mastra's `semanticRecall`
  retrieves the needle across threads).
- **aisotropy's forgetful arena cell as a rendered result.** The 422-refuse is offline-deterministic and
  code-cited (`main.go:863-867`), but the matrix cell is `pending`, so aisotropy's refuse is
  mechanism-certain rather than board-rendered like the four competitor verdicts (§4).
- **Whether the summarizer non-determinism vs the `Close` ordering bug flipped `mem-h03-longhaul`'s
  earlier pass to a fail.** Both are independently real and file-grounded, but which one produced the
  specific run outcome was not isolated (forensics §9).
- **Cross-run store contamination for the competitor stores in the new dimensions.** The runner nonce
  (`affaf93`) fixes it for the memory-ext/forgetful scopes, but the FASE-3 agents flagged that no
  per-run reset exists for every competitor's on-disk store the way `resetRqliteFacts` exists for
  aisotropy — a real-run prerequisite, not yet fully closed (`0bdd3ff`, `55ee6b1` MUST-CHECKs).
- **The exact per-call token split behind the 100,644 total.** §2 verifies the *mechanism* (per-turn
  re-hydration + reflection + summarizer multiply call count and tokens) but does not re-derive a
  per-invocation call log — none was available (forensics §9, "exact per-call token split NOT
  independently re-derived").
- **The idiomaticity debt is being paid down but is not fully closed.** langgraph/mastra now use their
  native primitives in the *new* dimensions (`0bdd3ff`, `594d816`), but the *old* `memory` dimension's
  hand-rolled paths and the full SHOULD-FIX list remain the primary disclosed fairness debt
  (`arena-fairness-protocol.md` §4, §Debts).

---

## Cross-links

- `docs/architecture/arena-memory-forensics.md` — the empty-tie mechanism, the replay-is-optimal finding
  (§6), the token-delta decomposition (§3.5), and the real product defects (§5.2) this analysis rests on.
- `docs/architecture/arena-memory-extensions.md` — the three new dimensions' design, the refuted
  uniqueness claim, and the per-framework best-official-primitive matrix (§3's fairness baseline).
- `docs/architecture/arena-tenancy-levels.md` — the L0–L3 ladder, aisotropy's honest per-layer placement
  with the NoAuth/façade caveats, and the forgetful-integrator design §4 measures.
- `docs/architecture/arena-fairness-protocol.md` — the anti-bias standard §8 satisfies, including the
  home-turf disclosure (§5) and idiomaticity debt (§4) this document inherits.
- `docs/architecture/arena-benchmark-methodology.md` — how the token/latency numbers are measured, the
  "empty parity" and "same checkmark, different guarantee" reading guides.
- FASE-3 wiring commits: `0bdd3ff` (langgraph, the leak-beats-prediction finding), `55ee6b1`
  (llamaindex silent_partition), `594d816` (mastra refuses), `5f58150` (genkit-go/eino/ai-sdk honest
  verdicts), `f4e473d` (pydantic-ai/spring-ai + spring refuse), `affaf93` (per-run nonce).
- `arenas/live/runner/scoring/scoring.go:510-530` — the pure `ScoreForgetful` three-way verdict;
  `scoring_test.go:552-558` — the fixtures proving the scorer can fail aisotropy too.
