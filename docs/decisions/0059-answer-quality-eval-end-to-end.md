---
type: ADR
title: "ADR-0059 — Answer quality, measured: an end-to-end, provider-agnostic answer-quality eval over the full turn pipeline"
description: ADR-0058 measured RETRIEVAL quality (recall/MRR/nDCG) but "does the AGENT actually answer well with a REAL model" stayed unmeasured — every prior test ran the keyless stub. Decision — ship bench/answer-quality, a runnable harness that runs each question through the FULL turn pipeline (embeds aisotropy.App, calls App.Turn — real isolation, quota, guardrails, memory hydration, fail-closed reflection, cost accounting), captures the answer, and grades it with an LLM-as-a-judge on correctness / faithfulness / helpfulness (1–5), plus refusal rate, hallucination flag, latency, and measured token spend. Both the serving model AND the judge are PROVIDER-AGNOSTIC via llm.RegistryFromEnv / eval.JudgeClientFromEnv (OPENROUTER > OPENAI > ANTHROPIC > stub) — the hardcoded ANTHROPIC-only gate in bench/rag-quality/judge.go is fixed to the same seam. Bounded spend (-n default 20, -cap 200, pre-run estimate, token budget). Key handling — the operator exports their own key; the harness reads it from env and never logs it. Honest gap — the bundled generic set is a wiring smoke; the ONLY numbers that reflect production come from the operator's own domain Q&A.
tags: [decisions, adr, evaluation, quality, answer-quality, llm-as-judge, provider-agnostic, faithfulness, hallucination, refusal, token-spend, rag]
timestamp: 2026-07-17T00:00:00Z
supersedes: none — completes the generation-quality half of the eval pillar ADR-0026 opened and ADR-0058 advanced for retrieval
---

# ADR-0059 — Answer quality, measured: an end-to-end, provider-agnostic answer-quality eval over the full turn pipeline

## Status

✅ **Accepted — implemented 2026-07-17.** `bench/answer-quality` is a runnable harness
(`go run ./bench/answer-quality`) that boots the real `aisotropy.App`, runs each question through
`App.Turn`, and grades the produced answer with a provider-agnostic LLM-as-a-judge. It moves
**answer quality** from **unmeasured** to **measurable-by-the-operator** on a real model. It is a
measurement instrument the operator runs on their own key and their own domain set — not a CI gate
(see Consequences).

## Context

The eval pillar arrived in three moves. **ADR-0026** built the *foundation* (`internal/eval`:
IR metrics + an LLM-as-a-judge that emits a typed verdict through the same reflection loop the
product serves with) and named its own gap: "no gold dataset, no CI gate." **ADR-0058** closed the
*retrieval* half — `bench/rag-quality` measures recall/MRR/nDCG on the real `semantic.Store` with a
licensed multilingual gold set (real e5: macro-avg nDCG@10 = 0.97). What remained unmeasured is the
other half of a RAG agent's quality chain:

> retrieval returns the right passages — but does the **agent** produce a **good answer** with a
> **real model**?

Every automated test to date exercised the **keyless stub** (`internal/llm.StubClient`), by design:
the stub emits a deterministic, schema-satisfying stream so the *pipeline* (SSE, reflection,
cost wiring) can run in CI with zero credentials. That proves the plumbing — and proves nothing about
generation quality. Answer correctness, groundedness (faithfulness / hallucination), helpfulness,
refusal behavior, latency, and token spend under a **real** model were all unmeasured. That is the
**#1 launch-readiness gap**: a system can retrieve perfectly and still answer badly.

Two constraints shaped the design:

1. **No provider key in the build/CI environment.** The operator holds a key
   (OpenRouter/OpenAI/Anthropic); the harness author does not. So the tool must be **turnkey and
   operator-run**: one command, the operator exports their own key, the harness reads it from the
   environment and **never logs it**, and with **no** key it degrades cleanly to the stub and says so
   loudly.
2. **A latent provider-lock bug.** `bench/rag-quality/judge.go` gated its optional LLM-judge on a
   **hardcoded `ANTHROPIC_API_KEY`** check — contradicting the runtime's provider-agnostic posture
   (ADR-0009: `OPENROUTER_API_KEY > OPENAI_API_KEY > ANTHROPIC_API_KEY > stub`). An operator with an
   OpenRouter key would have seen the judge silently skip. Any new answer-quality judge had to avoid
   repeating that mistake — and the existing one had to be fixed to the same seam.

## Decision

Ship **`bench/answer-quality`**, a runnable, bounded, provider-agnostic answer-quality harness, and
extract the provider-agnostic judge-selection into a **shared seam** in `internal/eval` that both
this harness and `bench/rag-quality` use.

### 1. End-to-end over the REAL turn pipeline

For each question the harness embeds `aisotropy.App` and calls **`App.Turn`** — full parity with
`POST /v1/tenant/{tenant_id}/user/{user_id}/chat`: identity→ctx isolation, the pre-spend quota gate,
input/output guardrails, gateway model selection, memory hydration (working memory + facts FTS5 +
semantic RAG), the fail-closed reflection loop, and cost accounting. Questions may carry
`context_docs`, which are seeded into the **real memory tier** (`App.IngestKnowledge`, tenant/user
scoped) before the run, so the turn's own retrieval hydrates the answer. This measures the whole
chain the way production serves it, not a mocked slice of it.

### 2. LLM-as-a-judge on the axes that matter, reusing `internal/eval`

The captured answer is graded with `internal/eval`'s existing typed-verdict `LLMJudge` (the same
struct-as-contract reflection loop the product uses), on:

| Axis | Method | Notes |
|---|---|---|
| **correctness** (1–5) | LLM judge | compares to `ideal_answer` when present (substance, not wording), else judges vs. the query/context. Enabled by a new optional `EvalSample.Reference` field so the gold answer never contaminates the faithfulness axis. |
| **faithfulness** (1–5) | LLM judge (`eval.Faithfulness`) | scored only when the question carried grounding context |
| **helpfulness** (1–5) | LLM judge | complete, clear, actionable, appropriately concise |
| **refusal rate** (%) | model-free heuristic | declined / empty answers — a first-class launch metric, cheap, no tokens |
| **hallucination** (%) | derived | grounded question scoring faithfulness < 0.5 |
| **latency** (ms) | wall-clock per turn | |
| **token spend** | provider-reported `Usage` via a metering `llm.Client` decorator | split turn vs. judge + grand total |

Token spend is captured **without touching the App's internal cost meter or `aisotropy/app.go`**: the
harness builds the provider client itself, wraps it in a transparent metering decorator that reads
`llm.Token.Usage` off the terminal token as it streams through, and hands that to
`aisotropy.WithLLM`. The judge client is metered the same way, so the report shows the honest **full**
cost of the eval — which is what bounds it.

### 3. Provider-agnostic — one seam, two harnesses

A new `eval.JudgeClientFromEnv` mirrors `llm.RegistryFromEnv` precedence
(`OPENROUTER > OPENAI > ANTHROPIC > stub`) and returns `{Client, Provider, Real}`. `Real == false`
means the keyless stub — the signal a harness uses to label a run as wiring-only. `AISOTROPY_JUDGE_MODEL`
swaps only the model id on the selected provider (serve cheap, judge strong, on one key).
`bench/answer-quality` selects its serving client via `llm.FromEnv` and its judge via this seam;
**`bench/rag-quality/judge.go` is fixed** to use the same seam, deleting its hardcoded
`ANTHROPIC_API_KEY` gate.

### 4. Bounded spend + honest labeling

- `-n` (default **20**), `-cap` (default **200**, a hard maximum a run can never exceed silently),
  a **pre-run estimate** (provider calls + rough token total) printed before any spend, the **exact
  measured** totals after, and `AISOTROPY_EVAL_MAX_TOKENS` as a runaway guard.
- **No key ⇒ stub run**, and every such report is loudly stamped *"STUB RUN — proves wiring, NOT real
  answer quality."*
- The operator exports **their own** key; the harness reads it from the environment and **never
  logs, stores, or prints it**.

### 5. Two input sets, and a loud generic-vs-domain warning

- `testdata/generic.json` — a small, self-authored, general-knowledge **smoke** set (some with
  `context_docs`/`ideal_answer`) that proves the pipeline + judge end-to-end.
- `testdata/domain.example.json` — a **template** the operator fills with THEIR real domain Q&A.

The report footer states plainly that the generic set is a wiring smoke and the **only** numbers that
reflect production quality come from the operator's own domain set.

## Consequences

**Positive.**
- The launch-readiness question is now **answerable** with one operator command on a real model —
  no key ever reaches the build environment.
- The provider-lock bug is fixed at a **shared seam**; both eval harnesses are now genuinely
  provider-agnostic, and the pattern is reusable for any future judge.
- Token spend and latency are **measured**, not asserted, and the run is bounded by construction.
- The `EvalSample.Reference` extension is backward-compatible (empty by default) and cleanly separates
  the correctness axis (vs. a gold answer) from the faithfulness axis (vs. retrieved context).

**Negative / limits (stated honestly).**
- This is a **measurement instrument, not a CI gate.** It requires a provider key and real spend, so
  it does not run in keyless CI (the stub path runs in CI only to prove wiring). Gating a PR on it
  would either need a budgeted CI key or a recorded-baseline mechanism — out of scope here.
- The **generic set proves nothing about an adopter's domain.** Generic ≠ domain is repeated in the
  README, the dataset description, and every report; the honest measure is the operator's own set.
- **LLM-as-a-judge is itself imperfect** (self-preference bias, calibration drift). `AISOTROPY_JUDGE_MODEL`
  mitigates by allowing an independent judge model, but the scores are a strong signal, not ground
  truth. Human review of low-scoring cases remains the backstop.
- The **hallucination flag is a threshold on faithfulness**, not an independent detector; and the
  **refusal heuristic** is recall-biased (a genuine, appropriate refusal also trips it) — both are
  presented alongside, not instead of, the graded axes.

## Alternatives considered

- **Read the App's internal cost meter for token spend.** Rejected: it would require touching
  `aisotropy/app.go` (out of scope) or the `/costs` HTTP path. The metering `llm.Client` decorator is
  self-contained, provider-agnostic, and attributes spend per turn and per judge call directly.
- **A single combined multi-criterion judge call** (score all axes in one request) to cut spend.
  Deferred: it would fork the shared `internal/eval` `LLMJudge` machinery; per-criterion calls reuse
  it unchanged and keep each axis independently calibrated. Spend is already bounded by `-n`/`-cap`.
- **Bundle a licensed public QA set** (e.g. reuse the XQuAD slice) as the generic set. Rejected for the
  bundled smoke to avoid re-attribution overhead; the generic set is self-authored public-domain
  facts. The operator's domain set is the real measure regardless.

## Implementation

- New: `bench/answer-quality/` (`main.go`, `dataset.go`, `criteria.go`, `meter.go`, `judge.go`,
  `pipeline.go`, `report.go`, `run.sh`, `README.md`, `testdata/generic.json`,
  `testdata/domain.example.json`, tests).
- New: `internal/eval/provider.go` (`JudgeClientFromEnv`, `JudgeClient`, `JudgeModelEnv`) + tests;
  `EvalSample.Reference` (guide.go) surfaced in the judge prompt (judge.go).
- Fixed: `bench/rag-quality/judge.go` — hardcoded `ANTHROPIC_API_KEY` gate replaced with
  `eval.JudgeClientFromEnv`; README wording updated.
- Verified without a provider key: `go build ./...`, `go vet` and `gofmt -l` clean on the touched
  packages, `go test ./internal/eval/... ./bench/answer-quality/...` passing, and a stub end-to-end
  run of the harness (labeled "stub — proves wiring, NOT real answer quality").
