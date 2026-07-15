---
type: ADR
title: "ADR-0026 — Product-evaluation harness: measure answer quality and retrieval quality, not just compression and isolation"
description: Aisotropy measured its own compression thesis (bits-per-token, ADR-0014) and its isolation moat (ADR-0011/0024) but never the two things a RAG-style product actually ships on — answer quality and retrieval quality. Chip Huyen's "AI Engineering" names evaluation the first discipline ("you can't improve what you can't measure"). Decision — ship internal/eval as the foundation: information-retrieval metrics (precision/recall/F1/MRR) run against the REAL semantic.Store within a tenant Scope, plus an LLM-as-judge that emits a TYPED verdict through the same struct-as-contract reflection loop the product serves with (ADR-0002/0008), over a Criterion/Rubric/EvalCase guide with first-class relevance/faithfulness/safety criteria and a Suite runner. A foundation (metrics + judge plumbing + rubric model), not a full eval platform.
tags: [decisions, adr, evaluation, quality, retrieval, llm-as-judge, rag, huyen, foundation]
timestamp: 2026-07-15T00:00:00Z
---

# ADR-0026 — Product-evaluation harness: measure answer quality and retrieval quality, not just compression and isolation

## Status

✅ **Accepted — 2026-07-15.** The `internal/eval` package ships as the first brick of an
evaluation pillar the runtime had been missing. The book this ADR is written against is Chip
Huyen's **"AI Engineering"** (Ch. 3–4), whose thesis is blunt: evaluation is the discipline that
gates everything else, because **you cannot improve — and must not ship — what you cannot
measure.** The package doc (`internal/eval/doc.go`) is the executable statement of the mapping
this ADR decides.

## Context

Aisotropy had two things it could measure and one large thing it could not.

- **Compression** — the framework's own North-Star thesis — is measured offline as bits-per-token
  by a reference model (ADR-0014/0015, `internal/bitrate`).
- **Isolation** — the surviving, verified differentiator — is measured by construction and by an
  adversarial cross-tenant suite (ADR-0011/0024, the `scopecheck` analyzer, ~146 `NewScope` sites).

Neither of those answers the question a consumer building a bot on Aisotropy actually asks:
**is the answer any good, and did retrieval find the right context?** A framework can have a
perfect isolation moat and a beautiful compression number and still return irrelevant, hallucinated
answers — and nothing in the codebase would catch it. An analysis pass over "AI Engineering"
identified precisely this as aisotropy's biggest gap versus the canonical LLM-application stack:
**product evaluation was absent.**

Huyen frames the discipline as a **three-step pipeline**, and the gap maps cleanly onto it:

1. **Evaluate each component, not just the end-to-end answer.** A RAG-style agent is a chain
   (retriever → context assembly → generation). A single end-to-end score hides *which* link
   failed — a great generator cannot rescue a retriever that fetched the wrong documents, and the
   two failures demand different fixes.
2. **Have an evaluation guide** — explicit criteria, a scored rubric per criterion, and labeled
   examples — so "good" is defined up front and a score is reproducible, not an ad-hoc vibe.
3. **Decide how to evaluate each criterion** — the three methods being *LLM-as-a-judge* (for
   open-ended axes a code check cannot capture), *code / exact function* (for mechanical criteria),
   and *human* (a labeled gold set).

The runtime already had the raw materials to implement this honestly without new dependencies: a
real `semantic.Store` with tenant-scoped search (`internal/semantic`), a struct-as-contract
reflection loop over a provider-agnostic `llm.Client` (`GollemReflect`, ADR-0002/0008/0010), and
an in-process stub client + hash embedder that let the whole thing run in CI with no API key.

## Goals

- **Make each pipeline component measurable on its own terms** — the retriever with
  information-retrieval metrics, the generation with per-criterion scoring — so a regression is
  localized to the link that caused it (Huyen step 1).
- **Define "good" as data, up front** — a `Criterion` / `Rubric` / `EvalCase` model with the
  book's headline quality axes shipped as first-class values (Huyen step 2).
- **Reuse the struct-as-contract discipline for the judge** — an LLM-as-judge that returns a
  *typed, validated* verdict through the same reflection loop the chat handler uses, not free text
  the caller must hope parses (ADR-0002).
- **Preserve the isolation gate during evaluation** — retrieval eval runs through a real
  `semantic.Scope`, so a case is measured within one tenant/user exactly as production serves it;
  the harness never widens a store's filter to "see everything" (constitutional isolation gate).
- **Zero new dependency, runs in CI keyless** — stdlib + existing internal packages only; the
  stub client and hash embedder drive the whole harness with no external service and no
  `ANTHROPIC_API_KEY` (100% MIT/Apache gate, sovereignty posture).

## Non-goals

- **NOT a full evaluation platform.** This is metrics + judge plumbing + a rubric model — a
  foundation. It is explicitly *not* a labeled golden dataset at production scale, not a human-eval
  loop, not a CI regression gate, and not a per-tenant dashboard (all named as follow-ons below).
- **NOT a new model or scoring service.** The LLM-as-judge reuses whatever `llm.Client` the product
  is already configured with; it does not introduce a dedicated grader model or its own inference
  path.
- **NOT a benchmark of aisotropy versus other frameworks.** It measures *this* runtime's own
  retriever and generation against *this* runtime's own gold cases.
- **NOT a replacement for the compression (ADR-0014) or isolation (ADR-0011/0024) measurements** —
  it *complements* them by adding the third axis (product quality) they never covered.

## Options considered

The load-bearing question was **how much to build now** — the risk being either a token gesture
that measures nothing real, or an over-built platform that outruns the single consumer that exists.

### Option A — Defer; keep measuring only compression and isolation

Document product evaluation as future work; ship nothing.

- **+** Zero code.
- **−** Leaves the biggest measured gap open. "You can't improve what you can't measure" cuts both
  ways: with no answer-quality or retrieval-quality signal, every change to hydration, retrieval, or
  the reflection loop is a blind edit. Rejected.

### Option B — Adopt an external eval framework (e.g. a Python eval harness)

Pull in an off-the-shelf evaluation library.

- **+** Batteries included (many metrics, dataset tooling).
- **−** Every mature option is a Python/JS dependency that would violate the in-process Go posture
  and, in several cases, the 100% MIT/Apache gate; it would also measure a *toy* retriever, not
  aisotropy's real tenant-scoped `semantic.Store`. The whole point is to grade the retriever the
  product actually serves. Rejected.

### Option C — Ship a dependency-free foundation that maps Huyen's three steps into code (CHOSEN)

Build `internal/eval` from stdlib + existing internal packages: IR metrics against the real store,
an LLM-as-judge on the existing reflection loop, a `Criterion`/`Rubric`/`EvalCase` guide with the
book's headline criteria, and a `Suite` runner — with the stub client + hash embedder making it run
keyless in CI.

- **+** Measures the **real** retriever (through the `semantic.Store` interface, so swapping the
  in-process store for Qdrant changes nothing in the harness) and the **real** generation contract
  (the same typed reflection loop).
- **+** No new dependency, no new license surface, runs in CI with no API key — consistent with the
  sovereignty and supply-chain posture (ADR-0001, license-policy).
- **+** Honest about its scope: a foundation whose follow-ons are named, not a platform pretending
  to be finished.
- **−** It is *only* a foundation — no real gold dataset, no CI gate, no dashboard yet. Accepted
  cost: the shape is right and the follow-ons are explicit, so the foundation is extensible without
  rework.

## Decision

**Adopt Option C.** Ship `internal/eval` as the evaluation foundation, mapping Huyen's three-step
pipeline directly into code.

### Step 1 — Component evaluation

Two components are graded on their own terms:

- **The retriever** (`retrieval.go`, `metrics.go`). `EvalRetrieval(ctx, store, scope, emb, cases, k)`
  runs each labeled `RetrievalCase` (query → relevant `semantic.Point` IDs) through the **real**
  `store.Search` within a tenant `Scope`, and scores the returned IDs with pure, model-free IR
  metrics: `PrecisionAtK`, `RecallAtK`, `F1`, and `MRR` (per-query reciprocal rank; the `MeanMRR`
  aggregate over cases is the Mean Reciprocal Rank proper). It **fails closed** — an embedder or
  store error on any case aborts, because a harness that silently skips failed cases reports a
  falsely rosy mean. The scope is threaded into every `Search`, so isolation holds during
  evaluation exactly as in production.
- **The generation** (`judge.go`, per-criterion `Score`). Each `Judge` grades one `EvalCase` on one
  `Criterion` and returns a `Score` normalized to `[0,1]`.

### Step 2 — The evaluation guide

An explicit, data-first definition of "good" (`guide.go`):

- `Criterion` (name + description + `Scale`), `Rubric` (an ordered set of scored `RubricExample`
  illustrations that calibrate a judge), `EvalSample` (query + context + response), and `EvalCase`
  (a sample plus optional human-labeled `Expected` scores — used for regression, never fed to a
  judge, which would leak the answer).
- The book's **headline criteria ship as first-class values**: `Relevance`, `Faithfulness`
  (grounding / factuality to the provided context — the anti-hallucination axis for RAG), and
  `Safety` (toxicity), each with a short calibrated rubric (`RelevanceRubric`, `FaithfulnessRubric`,
  `SafetyRubric`).

### Step 3 — How to evaluate each criterion

The `Judge` interface is the seam unifying the three methods:

- **LLM-as-a-judge** (`LLMJudge`): scores an open-ended criterion by asking an `llm.Client` for a
  **typed verdict**. It reuses `llm.GollemReflect[verdict]` — the model is shown the Go struct as
  the schema and Gollem's `Agent[T]` retries with the exact schema error until it validates, with a
  `validate` hook that requests a correction when the raw value is out of the criterion's `Scale`.
  So a judge run yields a parsed, range-checked struct, **the same "struct is the contract" thesis
  the chat handler applies to its own output** (ADR-0002). It is fail-closed: an exhausted reflection
  loop is returned as an error, never a silent `0`.
- **Code / exact function** (`CodeJudge`): a deterministic Go func, no model, no network, no cost —
  the right tool for a mechanical criterion. Two built-ins ship: `NewJSONValidityJudge` (a
  structural gate for structured-output agents) and `NewKeywordJudge` (a "must mention X" check,
  e.g. a required disclaimer).
- **Human**: out of scope for automated runs, but `EvalCase.Expected` carries labeled scores so a
  human-graded gold set slots into the same shape.

### The Suite runner and the gold set

`RunSuite(ctx, cases, judges, threshold)` ties step 3 back to step 2: it scores every `EvalCase`
with every `Judge`, aggregates the mean normalized value and the pass/fail split per criterion
(against `threshold`), and **fails closed** — any judge error on any case aborts with no partial
result, so a broken judge never masquerades as a low score. Two aggregate criterion names collide
intentionally (an LLM and a code judge both scoring "safety" aggregate together). A tiny curated
gold set ships as checked-in JSON (`testdata/generation_golden.json`,
`testdata/retrieval_golden.json`), loaded via `LoadEvalCasesFile` / `LoadRetrievalCasesFile` whose
wire contract is the exported structs' JSON tags, so the on-disk gold set and the in-code types
cannot silently drift.

## Consequences

- **Each pipeline component is now measurable.** A retrieval regression surfaces in
  precision/recall/MRR before it ever reaches a judge; a generation regression surfaces per
  criterion (relevance / faithfulness / safety). The blind spot between the compression number and
  the isolation moat is now instrumented.
- **The struct-as-contract thesis extends to evaluation.** The judge's output is a validated struct
  from the *same* reflection loop the product serves with — evaluation is not a bolt-on with its own
  parsing rules; it inherits the runtime's core discipline (ADR-0002/0008).
- **Isolation holds during evaluation.** Retrieval eval runs through a real `semantic.Scope`, so the
  harness measures within one tenant exactly as production does (`TestEvalRetrievalIsolation` proves
  a same-topic doc under a different tenant is never retrieved into the evaluated tenant's metrics).
  The isolation gate is not weakened to "see everything" for measurement.
- **No new dependency, no new license surface, runs keyless in CI.** The stub client + hash
  embedder + in-process store drive the whole harness with no external service and no API key — the
  100% MIT/Apache gate and the sovereignty posture are preserved (ADR-0001).
- **This is a foundation, and is labeled as one.** The shape (metrics + judge plumbing + rubric
  model + suite runner) is complete and tested; the *platform* is not. Named follow-ons:
  1. **A real labeled golden dataset per tenant** — the shipped gold set is a tiny fixture, not a
     production reference corpus. A gold set is a reference, not a training corpus, and building a
     trustworthy one per tenant is the next investment.
  2. **A human-evaluation loop** — `EvalCase.Expected` is the slot; the workflow to fill it (label,
     review, reconcile against judge scores) is not built.
  3. **Wiring the judge into CI / regression** — nothing yet gates a merge on a quality threshold or
     tracks a per-criterion score across commits (the analog of the bpt series for ADR-0014).
  4. **Per-tenant eval dashboards** — surfacing `SuiteResult` / `RetrievalReport` over time, per
     tenant, is unbuilt.

## References

- **"AI Engineering" (Chip Huyen), Ch. 3–4** — the evaluation discipline and the three-step pipeline
  (component eval → evaluation guide → how-to-evaluate) this ADR maps into code.
- `internal/eval/doc.go` — the package doc that states the three-step mapping executably.
- `internal/eval/{metrics,retrieval,judge,guide,dataset}.go` + `testdata/{generation,retrieval}_golden.json`
  — the shipped foundation.
- [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract — the thesis the
  typed judge verdict reuses) · [ADR-0008](0008-reflection-loop-gollem-deferral.md) /
  [ADR-0010](0010-gollem-adoption-scope-and-observability.md) (the `GollemReflect` reflection loop
  the `LLMJudge` runs on)
- [ADR-0011](0011-tenant-scoped-statement-builder.md) / [ADR-0024](0024-authenticated-identity-seam.md)
  (isolation by construction — the gate retrieval eval runs through)
- [ADR-0014](0014-bits-per-token-reference-scorer-boundary.md) /
  [ADR-0015](0015-bpt-eval-model-and-python-batch-runtime.md) (the compression measurement this
  complements with the missing product-quality axis)
- [ADR-0009](0009-provider-agnostic-llm-layer.md) (the provider-agnostic `llm.Client` the judge
  scores through)
- [product evaluation harness](../architecture/product-evaluation.md) (the architecture doc for this
  package)
