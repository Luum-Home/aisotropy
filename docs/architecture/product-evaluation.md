---
type: Concept
title: Product-evaluation harness — measuring answer quality and retrieval quality
description: Code-grounded description of internal/eval (ADR-0026), the foundation that maps Chip Huyen's three-step evaluation pipeline into Go — IR metrics against the real tenant-scoped semantic.Store, an LLM-as-judge that returns a typed verdict through the same struct-as-contract reflection loop the product serves with, a Criterion/Rubric/EvalCase guide with first-class relevance/faithfulness/safety, and a Suite runner. Honest about what is foundation vs not-yet-built.
tags: [architecture, evaluation, quality, retrieval, llm-as-judge, rag, huyen, as-built]
timestamp: 2026-07-15T00:00:00Z
---

# Product-evaluation harness

This is the code-grounded companion to [ADR-0026](../decisions/0026-product-evaluation-harness.md).
It describes `internal/eval` **as the code implements it today**, verified against a green
`go build ./internal/eval/...` + `go test ./internal/eval/...`.

The runtime already measured two things: its **compression** North-Star (bits-per-token, ADR-0014,
`internal/bitrate`) and its **isolation** moat (by construction, ADR-0011/0024). It did not measure
the two things a RAG-style product actually ships on — **is the answer any good, and did retrieval
find the right context?** `internal/eval` closes that gap as a *foundation*: metrics + judge
plumbing + a rubric model, dependency-free (stdlib + existing internal packages), runnable in CI
with no API key. It is not a full evaluation platform — see [what is a foundation vs
not-yet-built](#what-is-a-foundation-vs-not-yet-built) below.

## The book's three steps → the code

The harness maps the three-step evaluation pipeline from Chip Huyen's **"AI Engineering"** (Ch. 3–4)
directly into files. The package doc [`internal/eval/doc.go`](../../internal/eval/doc.go) is the
executable statement of this mapping.

| Huyen step | What it means | Where it lives |
|---|---|---|
| **1. Component evaluation** | Grade each link of the RAG chain (retriever, generation) on its own terms, so a failure is localized — an end-to-end score hides *which* link broke. | `retrieval.go` + `metrics.go` (retriever); `judge.go` per-criterion `Score` (generation) |
| **2. Evaluation guide** | Define "good" up front as data: explicit criteria, a scored rubric per criterion, labeled examples. | `guide.go` (`Criterion`, `Rubric`, `RubricExample`, `EvalSample`, `EvalCase`) |
| **3. How to evaluate** | Choose a method per criterion — LLM-as-a-judge, code/exact function, or human. | `judge.go` (`Judge` interface: `LLMJudge`, `CodeJudge`; human via `EvalCase.Expected`) |

The Suite runner (`RunSuite`, `guide.go`) ties step 3 back to step 2; `EvalRetrieval`
(`retrieval.go`) does the analogous orchestration for step 1.

## Step 1 — component evaluation

### The retriever, against the real store

`EvalRetrieval` runs labeled retrieval cases through aisotropy's **real** `semantic.Store` — not a
mock — within a tenant `Scope`:

```go
func EvalRetrieval(
    ctx context.Context,
    store semantic.Store,
    sc semantic.Scope,
    emb semantic.Embedder,
    cases []RetrievalCase,
    k int,
) (RetrievalReport, error)
```

Each `RetrievalCase` is `{Name, Query, RelevantIDs}` — the relevant IDs are the `semantic.Point`
IDs the store returns, so retrieved and relevant sets are directly comparable. `EvalRetrieval`
embeds the query with `emb`, calls `store.Search(ctx, sc, vec, k)`, and scores the returned IDs.
Because it talks to the `semantic.Store` *interface*, swapping the in-process `InMemoryStore` for a
real `QdrantStore` changes nothing here — it measures whatever retriever the product actually serves.

It **fails closed**: an embedder or store error on any case aborts and returns that error. A
retrieval harness that silently skipped failed cases would report a falsely rosy mean. `k` must be
positive; `nil` store or embedder is rejected.

### The IR metrics (`metrics.go`)

Pure, deterministic, model-free — the objective ground the rest of the harness stands on. Each takes
the ordered retrieved IDs (rank 1 first) and the labeled relevant set, and returns a score in
`[0,1]`. All four deduplicate `retrieved` in first-seen order (a store returning the same ID twice
must not inflate a hit):

| Function | Definition |
|---|---|
| `PrecisionAtK(retrieved, relevant, k)` | fraction of the top-k that is relevant; denominator is `k`, so returning fewer than `k` results is correctly penalized |
| `RecallAtK(retrieved, relevant, k)` | fraction of all relevant items appearing in the top-k |
| `F1(precision, recall)` | harmonic mean; `0` when either is `0` |
| `MRR(retrieved, relevant)` | reciprocal rank of the first relevant item (`1/rank`), or `0` |

`RetrievalReport` carries per-case `RetrievalResult`s plus the arithmetic-mean aggregates
(`MeanPrecision`, `MeanRecall`, `MeanF1`, `MeanMRR` — the last is the Mean Reciprocal Rank proper).

## Step 2 — the evaluation guide

"Good" is defined up front as data (`guide.go`):

- **`Criterion`** — `{Name, Description, Scale}`. `Name` is the stable key for aggregation;
  `Description` is the instruction shown to an `LLMJudge`; `Scale` is the rubric range (`UnitScale`
  is `0..1`). Every score the harness produces is normalized to `[0,1]` regardless of `Scale`.
- **`Rubric`** — an ordered set of `RubricExample` (`{Score, Illustration}`) that calibrates a judge
  ("a response like *Illustration* earns about *Score*"). Rendered into the judge's prompt.
- **`EvalSample`** — `{Query, Context, Response}`. `Context` is the grounding a faithfulness judge
  checks against; it may be empty for a standalone criterion.
- **`EvalCase`** — a sample plus optional `Expected map[string]float64` labels (human-graded scores
  per criterion). `Expected` is used for regression against a gold set and is **never fed to a
  judge** (that would leak the answer).

The book's **headline criteria ship as first-class values**, each with a calibrated rubric:

| Value | Criterion | Rubric |
|---|---|---|
| `Relevance` | Does the response directly answer the query? | `RelevanceRubric` |
| `Faithfulness` | Is every claim supported by the provided context? (the RAG anti-hallucination axis) | `FaithfulnessRubric` |
| `Safety` | Is the response free of toxic/harmful content? (`1.0` safe, `0.0` unsafe) | `SafetyRubric` |

## Step 3 — how to evaluate each criterion

The `Judge` interface is the seam unifying the three methods; a `Judge` is bound to exactly one
`Criterion`, and returns a `Score{Criterion, Value, Reasoning}` with `Value` always normalized to
`[0,1]`.

```go
type Judge interface {
    Criterion() Criterion
    Score(ctx context.Context, sample EvalSample) (Score, error)
}
```

An error means an **operational** failure (model unreachable) — a legitimately low-quality sample is
a low `Value`, never an error. So the suite is fail-closed on infrastructure but never on content.

### LLM-as-a-judge (`LLMJudge`) — a typed verdict

`LLMJudge` scores an open-ended criterion (relevance, faithfulness, nuanced safety) by asking an
`llm.Client` for a **typed verdict**, reusing aisotropy's struct-as-contract reflection loop:

```go
res, err := llm.GollemReflect[verdict](ctx, j.client, system, prompt, j.validate, j.maxRetries)
```

The `verdict{Value float64, Reasoning string}` Go struct **is** the schema Gollem's `Agent[T]` shows
the model (via reflection) and validates the reply against; it retries with the exact schema error
until the JSON decodes. The `validate` hook additionally requests a correction when the raw `Value`
falls outside the criterion's `Scale` (a bad number is a correction request, not a fatal). So a judge
run yields a **parsed, range-checked struct** — the same "struct is the contract" thesis the chat
handler applies to its own output ([ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)).
It is fail-closed: an exhausted reflection loop is returned as an error, never a silent `0`. A `nil`
client is rejected at construction (`NewLLMJudge`) — a judge with no model can only ever error.

### Code / exact-function judge (`CodeJudge`)

A deterministic Go func — no model, no network, no cost — for a mechanical criterion. Two built-ins:

- `NewJSONValidityJudge()` — scores `1.0` when the response parses as JSON, else `0.0`. A common
  structural gate before any semantic judging of a structured-output agent.
- `NewKeywordJudge(criterion, required...)` — scores `1.0` when the response contains all required
  substrings (case-insensitive), else `0.0`. The "must mention X" check (a required disclaimer,
  a citation marker).

### Human

Out of scope for automated runs, but `EvalCase.Expected` carries the labeled scores, so a
human-graded gold set slots into the same shape as everything else.

## The Suite runner and the gold set

`RunSuite(ctx, cases, judges, threshold)` scores every case with every judge and aggregates:

```go
type SuiteResult struct {
    Threshold   float64
    Cases       []CaseResult                    // per-case, every judge's Score, in judge order
    ByCriterion map[string]CriterionAggregate   // mean + pass/fail split per criterion
}
```

It **fails closed**: any judge error on any case aborts with no partial `SuiteResult`, so a broken
judge never masquerades as a low score. `ByCriterion` is keyed by `Judge.Criterion().Name`, so two
judges sharing a criterion name (an LLM and a code judge both scoring "safety") aggregate together.
`SuiteResult.CriterionNames()` returns names sorted, for a stable report column order.

A tiny curated gold set ships as checked-in JSON in
[`internal/eval/testdata/`](../../internal/eval/testdata): `generation_golden.json` (four
`EvalCase`s with human-labeled `expected` scores) and `retrieval_golden.json` (three
`RetrievalCase`s). The loaders (`LoadEvalCasesFile`, `LoadRetrievalCasesFile`) are the wire contract
— the JSON field names are the exported structs' `json` tags, so the on-disk gold set and the
in-code types cannot silently drift — and they fail closed on malformed JSON or an empty case set.

## Usage example

From the tests, the suite path — a stub-backed LLM judge and a code judge grading the golden set
with **no API key** (`guide_test.go`):

```go
cases, _ := eval.LoadEvalCasesFile("testdata/generation_golden.json")

// LLM-as-judge on relevance (stub client → deterministic typed verdict, no key)
// + a deterministic code judge on JSON validity.
llmJudge, _ := eval.NewLLMJudge(llm.NewStubClient(), eval.Relevance, eval.RelevanceRubric, 2)
judges := []eval.Judge{llmJudge, eval.NewJSONValidityJudge()}

res, _ := eval.RunSuite(ctx, cases, judges, 0.5)
res.CriterionNames()                  // ["json_validity", "relevance"] (sorted)
res.ByCriterion["relevance"].Mean     // aggregate normalized score
res.ByCriterion["json_validity"].Pass // pass count against the 0.5 threshold
```

And the retrieval path against the real in-process store (`retrieval_test.go`):

```go
emb := semantic.NewHashEmbedder(0)
sc, _ := semantic.NewScope("acme", "alice")
store := semantic.NewInMemoryStore(emb.Dimension())
// ... upsert a labeled corpus under sc ...

cases, _ := eval.LoadRetrievalCasesFile("testdata/retrieval_golden.json")
report, _ := eval.EvalRetrieval(ctx, store, sc, emb, cases, 3)
report.MeanRecall   // 1.0 — every labeled-relevant doc is retrieved
report.MeanMRR      // 1.0 — and ranked first
```

## Isolation holds during evaluation

Retrieval evaluation runs through a real `semantic.Scope`: every `store.Search` is tenant/user-scoped
exactly like production traffic (the constitutional isolation gate,
[multi-tenancy](multi-tenancy.md), [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)).
The harness **never widens a store's filter to "see everything"** — a case is evaluated within one
scope, the way the product would serve it. `TestEvalRetrievalIsolation` proves it: a same-topic
document under a *different* tenant is never retrieved into the evaluated tenant's metrics, so it can
neither inflate nor deflate them.

## What is a foundation vs not-yet-built

This is deliberately a **foundation** — the shape (metrics + judge plumbing + rubric model + suite
runner) is complete and tested; the *platform* is not.

**Built and tested today:**

- IR metrics (precision / recall / F1 / MRR) against the real tenant-scoped `semantic.Store`.
- LLM-as-a-judge with a typed, schema-validated verdict on the existing reflection loop; two
  code judges (JSON validity, keyword).
- The `Criterion` / `Rubric` / `EvalCase` guide with first-class relevance / faithfulness / safety.
- The `Suite` runner and `EvalRetrieval` orchestration, both fail-closed.
- A tiny checked-in gold set and its drift-proof loaders.
- The whole harness runs in CI with no external service and no `ANTHROPIC_API_KEY` (stub client +
  hash embedder + in-process store).

**Named follow-ons — NOT built (see ADR-0026 Consequences):**

- A **real labeled golden dataset per tenant** — the shipped fixture is a reference, not a production
  corpus.
- A **human-evaluation loop** to fill `EvalCase.Expected` (label → review → reconcile).
- **Wiring the judge into CI / regression** — nothing yet gates a merge on a quality threshold or
  tracks a per-criterion score across commits.
- **Per-tenant eval dashboards** over `SuiteResult` / `RetrievalReport` across time.

## References

- [ADR-0026 — Product-evaluation harness](../decisions/0026-product-evaluation-harness.md) (the
  decision this documents)
- `internal/eval/doc.go` and `internal/eval/{metrics,retrieval,judge,guide,dataset}.go`
- [ADR-0002 — struct-as-contract](../decisions/0002-anti-framework-magic-struct-as-contract.md) ·
  [ADR-0008](../decisions/0008-reflection-loop-gollem-deferral.md) /
  [ADR-0010](../decisions/0010-gollem-adoption-scope-and-observability.md) (the reflection loop the
  judge runs on)
- [ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md) ·
  [multi-tenancy](multi-tenancy.md) (the isolation gate retrieval eval runs through)
- [ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md) (the compression
  measurement this complements with the missing product-quality axis)
- "AI Engineering" (Chip Huyen), Ch. 3–4 — the three-step evaluation pipeline
