---
type: Process
title: "Arena Fairness Protocol — the anti-bias audit for a self-authored benchmark"
description: The live arena (arenas/live/) compares aisotropy against eight competitor frameworks, but it is authored BY the aisotropy team, with adapters they wrote, on their infrastructure. Before this repository goes public, this document enumerates every way such a benchmark could be biased toward its author, and for each vector states the specific mechanism that prevents it, the code file:line / gate script / committed doc / rule that enforces it, and whether the defense is COMPLETE or PARTIAL — with the open fairness debts disclosed, not hidden. A protocol that claimed total defense would itself be a red flag.
tags: [architecture, process, arena, fairness, bias, audit, verification, code-truth]
timestamp: 2026-07-22T00:00:00Z
---

# Arena Fairness Protocol — the anti-bias audit for a self-authored benchmark

> **Why this document exists.** The live arena (`arenas/live/`) is a benchmark that ranks aisotropy
> against eight competitor frameworks (ai-sdk, eino, genkit-go, langgraph, llamaindex, mastra,
> pydantic-ai, and a spring-ai candidate). It was **authored by the aisotropy team**: the runner is
> ours, every competitor adapter was written by us, the scoring rules are ours, and the whole thing
> runs on our infrastructure against a model we chose. That is a structural conflict of interest. A
> reader is right to assume a thumb is on the scale until shown otherwise. This document is the
> "otherwise": it names each bias a self-authored benchmark can carry, points at the specific,
> executable mechanism that neutralizes it, and — where a mechanism is only partial — says so plainly.
> The credibility of the arena is not the claim "we are unbiased." It is the claim "here is every place
> we could have cheated, here is what stops it, and here are the two places the defense is not yet
> complete."
>
> **A load-bearing fact about where the defenses live.** The enforcement RULES an in-repo agent inherits
> (`.claude/rules/arena-deterministic-judging.md`, `capability-absence-claims.md`, `prune-evidence-gate.md`,
> `arena-full-matrix.md`, `dependency-reuse-first.md`) live under `.claude/`, which is **gitignored** and
> **does not reach the public repository** — verified: `git check-ignore .claude` returns `.claude`. Those
> rules govern how the benchmark is built, but a public reader never sees them. That is precisely why the
> substantive fairness content is restated here and in the sibling `arena-*.md` docs under
> `docs/architecture/` (which IS tracked and public — `git ls-files docs/architecture/`). If the only
> statement of a defense lived in `.claude/`, the public benchmark would be asking to be trusted on
> invisible evidence. This document is part of moving the anti-bias argument into git-tracked, public view.

## The audit table — bias vector × defense × enforcement point × status

Every row is a way the benchmark's author could bias it toward the author, the mechanism that prevents
it, where that mechanism is enforced (with a citation), and whether the defense is **COMPLETE** (the
vector is closed) or **PARTIAL** (a real, disclosed gap remains).

| # | Bias vector | Defense mechanism | Enforced at | Status |
|---|---|---|---|---|
| 1 | **Judge bias** — an LLM judge of the same model family scoring its own family generously | PRIMARY quality score is a pure deterministic function per dimension; no LLM in the primary score; LLM-judge demoted to opt-in SECONDARY, default off | `arenas/live/runner/scoring/scoring.go` purity header; gate `scripts/arena-determinism-guard.sh` (purity + 5×100 reproducibility, exit 3 on fail); `arenas/live/runner/main.go` `runDeterminismGate`; `-llm-judge` default false | **COMPLETE** |
| 2 | **Scorer bias** — a scoring rule tuned to reward aisotropy's output shape | Answer-key / negative-containment / hit@1 are output-shape-agnostic; the answer-key redesign REMOVED a verbosity bias that had HURT terse repliers (incl. aisotropy) — symmetric | `arenas/live/runner/scoring/scoring.go:83` `ScoreQA(reply, reference, answers)`; `docs/architecture/arena-qa-answer-key-scoring.md`; `arenas/live/runner/suite_test.go` (47/48 saturation) | **COMPLETE-ish** (residual: see §2) |
| 3 | **Adapter-artifact bias** — our own adapter bug read as a framework weakness | Q0 discipline: core-vs-adapter-vs-doc verdict, `file:line`, BEFORE any product claim; documented firing AGAINST aisotropy 4→5 times | `docs/architecture/arena-adapter-artifacts.md` §1–2; `docs/architecture/arena-memory-forensics.md`; `.claude/rules/prune-evidence-gate.md` (Q0 lineage) | **COMPLETE** for the found direction; **PARTIAL** for reverse (→ #4) |
| 4 | **Idiomatic-adapter bias / strawman competitors** — a competitor adapter that does NOT use the framework's best official primitive understates that competitor | Each adapter must wire its framework's best OFFICIAL primitive, declared with URL + maturity label + last-release date, before a dimension's numbers are trusted | `docs/architecture/arena-idiomaticity-and-dx.md` (Q1 scorecard, ranked SHOULD-FIX list); `docs/architecture/arena-infrastructure-parity.md` action items P1–P4 | **PARTIAL / OPEN** — primary fairness debt, see §4 + §Debts |
| 5 | **Home-turf / workload-selection bias** — choosing the workload where our architecture wins, avoiding where it loses | Symmetric disclosure: the shipped `memory` mode is the competitors' home turf (within-session recall, which raw transcript replay wins) and aisotropy is WORSE there on token cost — disclosed, not hidden | `docs/architecture/arena-memory-forensics.md` §1 (41,292 vs 7,568–14,504 tokens); `docs/architecture/arena-benchmark-methodology.md` "empty parity" | **COMPLETE** as disclosure; **PARTIAL** as measurement (aisotropy's home-turf dimensions designed, not built — see §5) |
| 6 | **Infrastructure-parity bias** — giving ourselves better infrastructure | Shared e5 embedder; per-framework Qdrant collections (collision fix); cloud-only equalizers disqualified (Zep); self-hostable-only (mem0) | `docs/architecture/arena-infrastructure-parity.md` (unanimous real-vs-real verdict); `arenas/live/runner/main.go` `frameworkCollectionEnv`; `arenas/live/_shared/populate-qdrant.py:10-14`; `docs/architecture/arena-memory-extensions.md` §2 | **COMPLETE** (with one disclosed anomaly: aisotropy's OWN stub is more-real, not less — §6) |
| 7 | **Silent-blank / cherry-pick bias** — omitting cells where we lose | No-blank-cells law: every `(adapter×dimension×lane×form)` cell is a real result or an explicit `infeasible:<reason>`; `pending` fails the completeness gate; `(N err)` denominator honesty | `arenas/live/FULL-MATRIX.md`; `arenas/live/MATRIX-SCHEMA.md:66-67`; `arenas/live/runner/matrix.go` completeness gate; `arenas/live/runner/results.go:432-465` `(N err)` marker | **COMPLETE** |
| 8 | **Single-run / variance bias** — reporting one lucky run | A 3-run variance experiment established cell-level stability; single-run numbers are not trusted at cell resolution | `docs/architecture/arena-adapter-artifacts.md` §6 (622/630 = 98.7% stable across three identical runs; memory 8/8 stable) | **COMPLETE** for the run measured; must be RE-RUN per board — see §8 |
| 9 | **Symmetric-rigor meta-requirement** — auditing competitors strictly, ourselves leniently | Every standard above applied to aisotropy's OWN claims at the same severity, in the same document | `docs/architecture/arena-tenancy-levels.md` (aisotropy L0–L3 self-placement + NoAuth/facade caveats); `docs/architecture/arena-safeguard-dimension.md:164` (aisotropy guardrails DETECT-ONLY in arena); `.claude/rules/capability-absence-claims.md` §"Symmetric rigor" | **COMPLETE** as a stated principle; this document IS its enforcement |

The rest of this document expands each row: the exact attack, the exact mechanism, the exact citation,
and — for the PARTIAL rows — precisely what is still open.

---

## 1. Judge bias — the primary score is pure, not an LLM (COMPLETE)

**The attack.** The arena drives every framework with the same model (`agnes-2.0-flash`, an
aisotropy-family model). If that same model family also *judges* the outputs, it rates its own family's
prose generously and non-discriminatingly. This is not hypothetical: the arena's original signal WAS an
LLM-as-judge (`arenas/live/runner/eval.go`'s `judgeClient`), and its observed behavior was a flat
Correctness/Helpfulness/Faithfulness of roughly **5.0** across frameworks that behave very differently
on objective signals — a judge that cannot tell a working framework from a broken one
(`docs/architecture/arena-deterministic-judging.md`, origin note).

**The defense.** The PRIMARY quality score is a pure, deterministic function of its inputs — no LLM
call, no network, no randomness, no wall-clock decision. One scorer per dimension, each returning
`Score{Value float64, Pass bool, Detail string}` (`arenas/live/runner/scoring/scoring.go`). The
package's own source files import only `encoding/json`, `fmt`, `regexp`, `sort`, `strconv`, `strings`
— the purity contract stated verbatim in the package header (`scoring.go` lines 16-24).

**Where enforced.** `scripts/arena-determinism-guard.sh` checks two things and BLOCKS (exit 3) on
either failure (`arena-determinism-guard.sh:18`):
1. **Purity** — the scoring package's direct imports contain none of
   `FORBIDDEN_EXACT=("net/http" "math/rand" "time" "internal/llm")` nor anything matching
   `agnes|anthropic|openai|/llm` (`arena-determinism-guard.sh:83`).
2. **Reproducibility** — `TestDeterministic_Reproducible` re-run 5 fresh processes ×100 in-loop
   iterations, diffed byte-for-byte (`arena-determinism-guard.sh:111`, `go test ./scoring/... -run
   '^TestDeterministic_Reproducible$' -count=5`).

The gate is wired into the runner before any billable model call (`arenas/live/runner/main.go`
`runDeterminismGate`) unless `-skip-determinism-gate` is passed. The LLM-judge still exists but is
demoted: `-llm-judge` defaults to **false** (`main.go`), the deterministic `rec.Det` is ALWAYS computed
regardless (`eval.go` `scoreRecord`), and the judge's axes render in a separate `## LLM-as-judge —
SECONDARY, opt-in` section with a self-judge caveat repeated inline (`results.go`). An agent proposing
an LLM as the PRIMARY judge is rejected by construction — the guard will not pass an impure scoring
package. **Status: COMPLETE.**

**Symmetric check.** The determinism guard makes no exception for aisotropy — the same purity applies to
`ScoreRAG`, `ScoreTools`, and every dimension aisotropy scores well on. A pure function cannot be
family-biased because it never sees which framework produced the string it scores.

---

## 2. Scorer bias — output-shape-agnostic rules, and a verbosity fix that HURT us first (COMPLETE-ish)

**The attack.** Even a deterministic scorer can be rigged: write the scoring rule to reward the exact
output shape aisotropy happens to produce (e.g. reward terseness if we are terse, or reward a specific
JSON envelope only we emit).

**The defense — and the proof it cuts against us.** The QA/memory scorer was CHANGED away from a shape
bias, and the shape it removed was one that had been HURTING aisotropy. The old `ScoreQA(reply,
reference)` scored the fraction of the *reference's* tokens present in the reply — which silently
measured verbosity, not correctness. A terse-correct "Paris" scored 0.33 and FAILED, while a padded
"The capital of France is Paris" scored 1.0 (`docs/architecture/arena-qa-answer-key-scoring.md`, "The
bug"). In the observed sweep this failed aisotropy (qa 1/6, memory 0/3) while verbose adapters scored
0.9 — but a concise ai-sdk or mastra reply would have failed identically. The fix scores against an
explicit `answer` key (`arenas/live/runner/scoring/scoring.go:83`, `ScoreQA(reply, reference, answers
[]string)`): exact match → 1.0, else pass iff the reply fully contains at least one acceptable key.
Symmetric by table: terse-correct passes, verbose-correct passes, a question-echo ("France") fails, a
wrong answer ("Berlin") fails — for **any** framework
(`docs/architecture/arena-qa-answer-key-scoring.md`, "Why this is fair"). The answer keys are the plain
correct answers (Paris, Au, seven, teal, …), drawn from reference and question, not tuned to any
adapter's output.

**The saturation evidence.** Before the suite was made discriminating, **47 of 48** framework×mode cells
sat at a perfect 1.0 (`arenas/live/runner/suite_test.go`) — a board that measured its own author, not
its subjects. A suite that hands everyone a 1.0 cannot secretly favor anyone; the work of §2 was making
it discriminate at all, which is the opposite of tuning it to a favorite.

**Where enforced.** The change kept the scorer pure (`scripts/arena-determinism-guard.sh` still green),
and fairness is regression-tested: `TestScoreQA_AnswerKey_ConciseAndVerboseBothPass`,
`TestScoreQA_AnswerKey_WrongAndQuestionEchoFail`, `TestScoreMemory_AnswerKey_TerseRecallPasses`
(`docs/architecture/arena-qa-answer-key-scoring.md`, "Deterministic-judging compliance").

**Residual (why "-ish").** `ScoreRAG`'s trap arm depends on `LooksLikeRefusal(reply)`
(`scoring.go:126,135`) — a regex heuristic for "did the model refuse to answer a trap question."
Regex-refusal detection is objective and applied identically to all frameworks, but a heuristic is a
place where output shape (how a given framework phrases a refusal) could in principle interact with the
pass rule. It is not tuned to aisotropy — the same regex judges every framework's refusal — but it is
the one scorer where a shape interaction is conceivable, so it is named here rather than hidden. The
1-of-8 unstable cell in the variance run was exactly a refusal-trap judgment flip
(`langgraph/rag/q9`, §8), which is model stochasticity, not a scorer favoring us. **Status:
COMPLETE-ish.**

---

## 3. Adapter-artifact bias — the Q0 discipline, documented firing AGAINST us (COMPLETE / PARTIAL)

**The attack.** The competitor adapters are written by the aisotropy team. A benchmark whose adapter is
written by the same team as the product it measures is **measuring the adapter until proven otherwise**
(`docs/architecture/arena-adapter-artifacts.md` §2). The bias can run either way: our own adapter bug
read as a *framework weakness*, or — more dangerously for fairness — a competitor's adapter written
sub-optimally, read as *their* weakness.

**The defense (the direction actually found).** The Q0 discipline: before any "aisotropy does/doesn't
do X" claim ships from a `RESULTS.md` diff, answer with `file:line` —

> **Q0:** is the defect in the CORE (`internal/`, `aisotropy/`), the arena ADAPTER
> (`arenas/live/aisotropy/`), or its DOC (`arenas/live/SPEC.md`)?

This fired FOUR times in one session, then a fifth, each time turning an apparent aisotropy weakness into
an adapter/doc bug — documented candidly because the repo is going public
(`docs/architecture/arena-adapter-artifacts.md` §1 table):
- A stale `arenas/live/SPEC.md` wire contract broke **seven** adapters at once (verdict: DOC).
- `tool-h-abstain` "aisotropy always calls the tool" — the adapter discarded the model's real answer
  ("Canberra") for a placeholder (verdict: ADAPTER; fixed `674c0e8`).
- `memory` 6/8 at 41,292 tokens — the adapter's turn-replay discarded canned cooperative turns, causing
  a refusal cascade (verdict: ADAPTER; fixed `14355a7`).
- `tool-h-chain-1` "can't chain tool calls" — the loop DOES chain (`internal/agentloop/agentloop.go:174`);
  the reply was fabricated from the last tool result (verdict: ADAPTER; fixed `48dbf62`+`674c0e8`).

The meta-lesson is the fairness one: once rows 1–3 were corrected, aisotropy's scores moved to parity
with the frameworks it had appeared to lose to (`arena-adapter-artifacts.md` §2). Q0's lineage is
`.claude/rules/prune-evidence-gate.md`'s code-truth rule (a claim about code is verified against the
code, not asserted from a doc).

**Where PARTIAL.** Q0 is exhaustively demonstrated for *our own* adapter (an aisotropy bug mislabeled as
a framework weakness). The **reverse** — a competitor adapter written sub-optimally, understating that
competitor — is a distinct vector and is only partially closed. That is vector #4, the primary open
fairness debt. **Status: COMPLETE for the self-direction; PARTIAL for the competitor-direction.**

---

## 4. Idiomatic-adapter bias — the operator's explicit concern (PARTIAL / OPEN)

**The attack (stated by the operator directly).** If a competitor's adapter does not use that framework's
best official primitive, the benchmark understates the competitor — a strawman. Example: scoring
LangGraph's long-term memory against a *hand-rolled SQLite store* the adapter wrote, when LangGraph
ships a native `BaseStore` with namespaces and semantic search, measures the adapter author's choice,
not LangGraph.

**The current state — honestly, this is not fully closed.** The idiomaticity audit
(`docs/architecture/arena-idiomaticity-and-dx.md`, Q1 scorecard) classifies every adapter's tier
adoption as NATIVE-IDIOMATIC / DEVIATES-JUSTIFIED / DEVIATES-SHOULD-FIX, and the SHOULD-FIX items are
**not all closed**:
1. **langgraph → native `BaseStore`** for scoped/facts memory (currently hand-rolled SQLite) — "the
   single highest-value fix" (`arena-idiomaticity-and-dx.md`, ranked SHOULD-FIX #1; docs:
   https://docs.langchain.com/oss/python/langchain/long-term-memory).
2. **mastra → native `QdrantVector` (`@mastra/qdrant`)** (currently raw `@qdrant/js-client-rest`)
   (SHOULD-FIX #2; docs: https://mastra.ai/reference/vectors/qdrant).
3. **genkit-go & pydantic-ai → native embedder** instead of raw `net/http` / raw `openai` SDK
   (SHOULD-FIX #3).

The audit already separates these from **justified** deviations that must NOT be "fixed" (eino's Qdrant
direct-client, because the `eino-ext` retriever hardcodes a `content` payload key that mismatches the
corpus `text` key — verified in source; mastra `semanticRecall:false`, because the `memory` task grades
durable last-N recall, not semantic recall). So the fairness debt is specifically the open SHOULD-FIX
list, not every deviation.

**The bar to close it.** Each adapter must wire its framework's best OFFICIAL primitive, and DECLARE it
in the adapter README with a URL + maturity label (NATIVE / OFFICIAL-EXTENSION /
OFFICIAL-THIRD-PARTY-INTEGRATION / COMMUNITY-ONLY / NONE) + last-release date — the exact discipline
`.claude/rules/capability-absence-claims.md` mandates — BEFORE that dimension's numbers are trusted as a
framework signal rather than an adapter signal. A DX/simplicity dimension (`ScoreDX`) is designed to
depend on this: measuring "lines to implement RAG" is only honest once langgraph uses `BaseStore`
instead of a hand-rolled store, or the number measures adapter authorship, not framework DX
(`arena-idiomaticity-and-dx.md`, "The Q1↔Q2 bridge"). **Status: PARTIAL / OPEN — this is the primary
disclosed fairness debt; see §Known open fairness debts.**

---

## 5. Home-turf / workload-selection bias — disclosed even where we lose (COMPLETE disclosure / PARTIAL measurement)

**The attack.** An author picks the workload where their architecture wins and avoids the one where it
loses.

**The defense — we ship and report the mode where we currently LOSE.** The shipped `memory` dimension is
the competitors' home turf: it grades within-session last-N recall, which raw transcript replay wins on
token cost. aisotropy is **worse** there — the first discriminating board showed aisotropy at 41,292
input tokens against a competitor range of 7,568–14,504, roughly 2.85× the nearest competitor and over
5× the cheapest (`docs/architecture/arena-memory-forensics.md` §1). This was not buried; a full
unsanitized forensic post-mortem was written *because* the repo is going public
(`arena-memory-forensics.md`, header). The reason aisotropy pays more is architectural (per-turn
re-hydration of a tiered store vs. a flat transcript replay), and it is reported as a real cost, not
explained away.

**Where PARTIAL.** Symmetric disclosure requires reporting BOTH turfs. aisotropy's OWN home turf —
cross-session recall, semantic long-term recall, async consolidation — is **designed but not
implemented**: the three-dimension design (`memory_cross_session`, `memory_semantic_recall`,
`memory_consolidation`) is explicitly marked **DESIGNED, NOT IMPLEMENTED**
(`docs/architecture/arena-memory-extensions.md` §3). Critically, the same document runs the
capability-absence sweep FIRST and concludes the naive "cross-session/semantic/consolidation is unique
to aisotropy" claim **does not survive** — langgraph, llamaindex, and mastra each have an official path
to all or most of it (`arena-memory-extensions.md`, Verdict). So the honest position is: the mode where
we lose is measured and reported; the mode where we would win is designed, fairness-reviewed against
competitors' best official extensions, and NOT YET a number. Until it is built, the board under-reports
aisotropy's memory story — an asymmetry that currently runs against us, and is disclosed as such.
**Status: COMPLETE as disclosure; PARTIAL as measurement.**

---

## 6. Infrastructure-parity bias — real-vs-real, with our own stub as the disclosed anomaly (COMPLETE)

**The attack.** Give aisotropy better infrastructure (real Qdrant/rqlite/e5) while competitors run
lightweight in-memory mocks, so aisotropy "looks slower for doing real work" — or the reverse.

**The defense.** Investigated via eight parallel read-only research agents (one per framework), the
verdict was unanimous: **the arena is already fair on infrastructure in the REAL (scored) lane**
(`docs/architecture/arena-infrastructure-parity.md`, Verdict). Every competitor adapter, in real mode,
hits real Qdrant (HNSW) + the same real e5 embed server + a real persistent store (rqlite or on-disk
SQLite). Mock stores exist ONLY in the `ARENA_STUB=1` lane, which is offline/CI, honestly labeled, and
not the quality number. The `~300 ms` vs `~10 s` gap that triggered the concern was
STUB-vs-REAL, a lane confusion, not an unfairness. Where residual differences exist, they are tiers
where aisotropy does MORE real work (in-scan tenant payload filter; FTS5 facts tier), so if anything
aisotropy is under-credited (`arena-infrastructure-parity.md`, "What the differences actually are").

**Supporting mechanisms.**
- **Shared embedder, embedded once.** `arenas/live/_shared/populate-qdrant.py` embeds each corpus doc
  ONCE through the running embed server and reuses the vectors across collections
  (`populate-qdrant.py:10-14`) — identical vectors for every framework, no per-framework embedding
  advantage.
- **Per-framework Qdrant collections (collision fix).** `arenas/live/runner/main.go`'s
  `frameworkCollectionEnv` sets a unique `arena_rag_<framework>` collection per framework so adapters
  sharing one Qdrant instance never bleed vectors; 6 adapters previously defaulted to a shared
  `arena_rag` collection, and two did not read the env var at all — both fixed
  (`docs/architecture/arena-deterministic-judging.md`, "Collision fix").
- **Equalizer eligibility rules.** A third-party memory service may only equalize a competitor if it is
  self-hostable and does not become the subject: **Zep is DISQUALIFIED** (cloud-API-only violates infra
  parity), **Letta is DISQUALIFIED** (it replaces the framework's agent loop — the cell would measure
  Letta), **mem0 is eligible** (Apache-2.0, self-hostable, multiple official in-tree integrations)
  (`docs/architecture/arena-memory-extensions.md` §2).

**The disclosed anomaly (runs against us).** aisotropy's OWN stub is the outlier — it still reaches real
Qdrant/rqlite/e5 under `ARENA_STUB=1` (hence its 10 s latency + 502 drain errors), while every
competitor's stub is fully offline. aisotropy's stub is *more* real than the others', not less
(`arena-infrastructure-parity.md`, Verdict + action item P0). **Status: COMPLETE**, with the one
anomaly disclosed and running against aisotropy, not for it.

---

## 7. Silent-blank / cherry-pick bias — the no-blank-cells law (COMPLETE)

**The attack.** Omit the cells where aisotropy loses, so absence reads as "not applicable" instead of
"lost."

**The defense.** The full-matrix no-blank-cells law: every `(adapter × dimension × lane × form)` cell is
EITHER a real result OR an explicit `infeasible:<reason>` — a silently missing cell is an arena defect
(`arenas/live/FULL-MATRIX.md`, "The no-blank-cells law"). The vocabulary is enforced by schema:
`pending` is a placeholder that "must never survive into a manifest an agent claims is complete," and
the runner's completeness gate (`arenas/live/runner/matrix.go`) treats any `pending` cell as a hard
failure (`arenas/live/MATRIX-SCHEMA.md:66-67`). The only escape is a top-level `"candidate": true` for a
genuine WIP adapter blocked on something external — and a candidate is logged loudly and rendered
`CANDIDATE`, never `pass` (`MATRIX-SCHEMA.md:69-79`), so it is not a silent gap.

**Denominator honesty.** A cell that ran but had errored invocations renders a trailing `(N err)` marker:
`3/3 ✓ (3 err)` reads as "the 3 runs that completed all passed; 3 more errored and are EXCLUDED from the
scored subset" (`arenas/live/runner/results.go:432-465`). A framework cannot look perfect by having its
failures silently dropped from the denominator — the excluded count is printed next to the score.
**Status: COMPLETE.**

---

## 8. Single-run / variance bias — the 3-run variance experiment (COMPLETE for the run; re-run per board)

**The attack.** Report one lucky run where the dice fell aisotropy's way.

**The defense.** A variance experiment ran three identical full boards and measured cell-level stability:
**622 of 630 cells (98.7%) returned the same verdict across all three runs**
(`docs/architecture/arena-adapter-artifacts.md` §6). Of the 8 unstable cells, 4 were the aisotropy tools
surface — traced to a real adapter defect (a hand-rolled client that lost the `ToolCaller` interface
upgrade, silently downgrading to the JSON fallback with 3× the model calls), since fixed in `48dbf62` +
`674c0e8` and validated 7/7 with exactly 1 model call per task. 3 were competitor transients (ERR in one
run, PASS in others) and 1 was a refusal-trap judgment flip (`langgraph/rag/q9`) — model stochasticity,
not code. Memory was 8/8 stable across all three runs, which is what justified NOT building a speculative
structural fix. The variance experiment is itself an application of symmetric rigor: the instability it
surfaced was aisotropy's, and it was fixed rather than reported as a framework difference.

**The limit.** This establishes stability for the run measured. It is NOT a standing property of every
future board — provider variance is real and visible in the data (a degraded-provider window inflated
whichever cells ran during it, and run-order makes it a per-framework confound;
`docs/architecture/arena-benchmark-methodology.md`, "How to read this"). A forensic reviewer should
re-run the variance harness against any board they intend to cite at cell resolution. **Status: COMPLETE
for the run measured; must be re-run per board.**

---

## 9. Symmetric-rigor meta-requirement — every standard applied to us too (COMPLETE as principle)

The rule that makes the other eight credible: every standard applied to a competitor's claim is applied
to aisotropy's OWN claim, at the same severity, in the same document
(`.claude/rules/capability-absence-claims.md` §"Symmetric rigor"). Concrete self-audits that do NOT read
as flattering:

- **Tenancy L0–L3 self-placement with edge caveats.** aisotropy is placed on the same L0–L3 ladder as
  every competitor (`docs/architecture/arena-tenancy-levels.md`, "aisotropy's honest placement"). The
  honest reading: **L3 storage discipline downstream of an identity that is L2-verified at the HTTP edge
  (only when auth is configured) and caller-trusted at the library facade.** In NoAuth mode the edge
  degrades to L1 — "the path is trusted, and identity provenance becomes whatever the caller puts in the
  URL, exactly the parameter-trust failure mode every L1 competitor shares" (`arena-tenancy-levels.md`,
  same section). The guarantee is "given the identity, downstream cannot escape it," NOT "the identity
  cannot be wrong." The corrected "no concept of a tenant" claim (which had overstated the gap against
  four competitors that DO ship scoping primitives) landed in commit `7743d51`.
- **Guardrails DETECT-ONLY in the arena.** aisotropy's own guardrails run detect-only in the safeguard
  dimension — `run.sh:47` sets `AISOTROPY_GUARDRAILS_BLOCK_INJECTION=false`, so the arena measures the
  same prompt-behavior surface every other framework exposes, NOT aisotropy's optional hard gate
  (`docs/architecture/arena-safeguard-dimension.md:164`). Enabling enforcement would be a declared
  adapter change, not the default — the honest baseline does not credit aisotropy for a gate it turned
  off for parity.
- **Memory token cost.** Covered in §5 — reported as a real loss, not explained away.

A comparison doc that audits others leniently toward itself is marketing, not engineering
(`.claude/rules/capability-absence-claims.md`). **Status: COMPLETE as a stated principle; this document
is its enforcement mechanism.**

---

## How to audit this benchmark yourself

The "evidencia ejecutable" discipline, made external-facing. A forensic reviewer runs these
reproducible steps — no claim below asks to be trusted without a command behind it.

1. **Confirm the primary score has no LLM in it.**
   ```
   bash scripts/arena-determinism-guard.sh
   ```
   Expect exit 0. It fails (exit 3) if the scoring package imports `net/http`, `math/rand`, `time`,
   `internal/llm`, or any `agnes|anthropic|openai|/llm` client, and separately if the same fixtures do
   not produce byte-identical scores across 5 processes × 100 iterations. Inspect the forbidden list at
   `scripts/arena-determinism-guard.sh:83` and the purity header at
   `arenas/live/runner/scoring/scoring.go` lines 16-24.

2. **Read the scorers — they are short and pure.** Every scoring rule is one function in
   `arenas/live/runner/scoring/scoring.go` (`ScoreQA:83`, `ScoreMemory:115`, `ScoreRAG:135`,
   `ScoreStream:712`, and the safeguard `ScoreNoContain`/`ScoreDecline`). Check that none reference a
   framework name. Confirm the answer keys in `arenas/live/tasks/suite.jsonl` are the plain correct
   answers, not tuned to any adapter's phrasing.

3. **Re-run the full board and diff it.** Run the runner three times with the same seed/config and diff
   the per-cell verdicts; expect ≥98% cell-level agreement (the measured figure was 622/630). Divergent
   cells are provider stochasticity or an adapter bug — apply the Q0 question
   (`docs/architecture/arena-adapter-artifacts.md` §2) to each before reading it as a framework
   difference. Read latency columns WITHIN a framework, not across (run-order provider-drift confound,
   `docs/architecture/arena-benchmark-methodology.md`).

4. **Check each adapter's declared primitive.** For every competitor adapter, read its README's
   native-vs-hand-rolled declaration against the idiomaticity scorecard
   (`docs/architecture/arena-idiomaticity-and-dx.md`, Q1). A dimension whose adapter hand-rolls something
   the framework ships natively (the open SHOULD-FIX list) is an adapter signal, not a framework signal —
   discount it until the SHOULD-FIX lands. This is the vector most worth a hostile reviewer's time (§4).

5. **Verify infra parity is real-vs-real.** Confirm no `RESULTS.md` comparison mixes an aisotropy-real
   number against a competitor-stub number (`docs/architecture/arena-infrastructure-parity.md`, action
   item P0). Confirm each framework gets a unique `arena_rag_<fw>` Qdrant collection
   (`arenas/live/runner/main.go` `frameworkCollectionEnv`) and the same e5 vectors
   (`arenas/live/_shared/populate-qdrant.py:10-14`).

6. **Check for silent blanks.** Read every adapter's `matrix.json`: every cell must be `pass` / `fail` /
   `infeasible:<reason>`, never `pending`, unless the adapter is a declared `candidate`
   (`arenas/live/MATRIX-SCHEMA.md`). The runner's completeness gate (`arenas/live/runner/matrix.go`)
   enforces this; run it and confirm it passes.

7. **Hold us to our own standard.** For every aisotropy claim (L3 isolation, guardrails, memory), find
   the self-audit that states its caveat: `docs/architecture/arena-tenancy-levels.md` (NoAuth/facade
   edge), `arena-safeguard-dimension.md:164` (detect-only), `arena-memory-forensics.md` (token cost). If
   a positive aisotropy claim lacks a co-located caveat, that is a fairness defect — report it.

---

## Known open fairness debts

A protocol that claimed zero open debts would itself be a red flag. These are the real, current
limitations, stated so a reviewer does not have to find them:

1. **Idiomatic-adapter parity is not fully closed (vector #4 — the primary debt).** The SHOULD-FIX
   idiomaticity items are open: langgraph's scoped/facts memory should use native `BaseStore` (currently
   hand-rolled SQLite); mastra's vector path should use native `QdrantVector`; genkit-go and pydantic-ai
   should use native embedders (`docs/architecture/arena-idiomaticity-and-dx.md`, ranked SHOULD-FIX). Until
   each adapter wires its framework's best OFFICIAL primitive and declares it (URL + maturity label +
   last-release date) in its README, those dimensions' numbers partly measure adapter authorship, not
   framework capability. This is the single most attackable point of the benchmark.

2. **aisotropy's home-turf memory dimensions are designed, not built (vector #5).** The mode currently
   shipped (`memory`, within-session recall) is the one aisotropy LOSES on token cost. The modes aisotropy
   would win — `memory_cross_session`, `memory_semantic_recall`, `memory_consolidation`, and the
   `isolation_forgetful` negative dimension that would turn the L0–L3 tenancy prose into a measured result
   — are DESIGNED, NOT IMPLEMENTED (`docs/architecture/arena-memory-extensions.md` §3,
   `docs/architecture/arena-tenancy-levels.md`, "forgetful integrator"). Until they land, the board
   under-reports aisotropy's memory/isolation story — an asymmetry that currently runs AGAINST aisotropy,
   and is disclosed rather than fixed by silence.

3. **The variance guarantee is per-run, not standing (vector #8).** 622/630 stability was measured on one
   set of three runs; provider drift can move cells on any future board. Cite cell-level results only
   after re-running the variance harness on the specific board in question.

4. **A few `L2` competitor claims are docs-only, not source-verified.** e.g. Mastra's server-side 403
   ownership check lives in `@mastra/server`, which was not installed during the tenancy research
   (`docs/architecture/arena-tenancy-levels.md`, Mastra-with-server row) — labeled unverified in place
   rather than asserted.

## Cross-links

- `docs/architecture/arena-deterministic-judging.md` — the pure primary scorer (vector #1).
- `docs/architecture/arena-qa-answer-key-scoring.md` — the verbosity-bias fix (vector #2).
- `docs/architecture/arena-adapter-artifacts.md` — the Q0 discipline + the variance experiment (vectors #3, #8).
- `docs/architecture/arena-idiomaticity-and-dx.md` — the idiomaticity scorecard (vector #4, the primary debt).
- `docs/architecture/arena-memory-forensics.md`, `arena-memory-extensions.md` — memory home-turf (vector #5).
- `docs/architecture/arena-infrastructure-parity.md` — real-vs-real infra (vector #6).
- `docs/architecture/arena-tenancy-levels.md`, `arena-safeguard-dimension.md` — aisotropy self-audits (vector #9).
- `docs/architecture/arena-benchmark-methodology.md` — how latency/tokens/TTFT/RSS are measured and their limits.
- `arenas/live/FULL-MATRIX.md`, `arenas/live/MATRIX-SCHEMA.md`, `arenas/live/SPEC.md` — the coverage law and wire contract (vector #7).
