# `bench/answer-quality` — measured END-TO-END answer quality

This harness answers the launch-readiness question that `bench/rag-quality`
(ADR-0058) does **not**: retrieval quality is measured (nDCG 0.969 on XQuAD), but
**"does the agent actually answer well with a real model"** was **unmeasured** —
every prior test used the keyless stub. This binary measures it.

For each question it runs the **full turn pipeline** (embeds `aisotropy.App` and
calls `App.Turn` — same isolation, quota gate, guardrails, gateway model
selection, memory hydration, fail-closed reflection, and cost accounting as
`POST /v1/.../chat`), captures the answer, then grades it with an
**LLM-as-a-judge**.

## What it measures

| Metric | How | Scale |
|---|---|---|
| **correctness** | LLM judge; compares to `ideal_answer` when present, else judges vs. the query/context | 1–5 |
| **faithfulness** | LLM judge; is every claim grounded in the retrieved context? (scored only when the question has `context_docs`) | 1–5 |
| **helpfulness** | LLM judge; complete, clear, actionable? | 1–5 |
| **refusal rate** | model-free heuristic (declined / empty answers) | % |
| **hallucination** | derived: grounded question scoring faithfulness < 0.5 | % |
| **latency** | wall-clock per turn | ms |
| **token spend** | provider-reported usage, split turn vs. judge, plus grand total | tokens |

## Provider-agnostic (ADR-0009) — no hardcoded Anthropic

The serving model **and** the judge are both selected via
`llm.RegistryFromEnv` / `eval.JudgeClientFromEnv` precedence:

```
OPENROUTER_API_KEY  >  OPENAI_API_KEY  >  ANTHROPIC_API_KEY  >  stub
```

There is **no** hardcoded `ANTHROPIC_API_KEY` gate. With **no** provider key,
everything runs on the deterministic **stub** and the report is loudly labeled
`STUB RUN — proves wiring, NOT real answer quality`.

## Key handling

**You** export **your own** provider key into the environment. The harness reads
it from the environment and **never logs, stores, or prints it**. This process
never asks for a key.

## The one command

```bash
# Real run — your key, your model, 20 questions (bundled generic smoke set):
export OPENROUTER_API_KEY=…  OPENROUTER_MODEL=…      # never committed, never logged
go run ./bench/answer-quality -n 20
```

Point it at **your own domain Q&A** (the only measure that reflects production):

```bash
cp bench/answer-quality/testdata/domain.example.json ./my-domain.json
# …fill my-domain.json with your real questions, ideal answers, context docs…
go run ./bench/answer-quality -data ./my-domain.json -n 50
```

Or use the launcher (prints the resolved provider without printing the key):

```bash
./bench/answer-quality/run.sh -n 20
```

Judge with a stronger/independent model than the one serving (reduces
self-preference bias), on the same key:

```bash
export AISOTROPY_JUDGE_MODEL=anthropic/claude-3.7-sonnet
go run ./bench/answer-quality -n 20
```

## Bounded spend

- **`-n`** (default `20`) — how many questions to run.
- **`-cap`** (default `200`) — hard maximum for `-n`; a run can never
  accidentally balloon past it. Raise it explicitly if you really mean to.
- A **pre-run estimate** (provider calls + rough token total) prints **before**
  any spend; the **exact measured** totals print after.
- **`AISOTROPY_EVAL_MAX_TOKENS`** — optional runaway guard: abort the loop as
  soon as cumulative measured spend crosses this many tokens.

## Optional cost estimate (USD)

Pricing is provider-specific, so cost is shown only when you supply prices
(per 1M tokens):

```bash
export AISOTROPY_EVAL_IN_PRICE_PER_1M=0.20 AISOTROPY_EVAL_OUT_PRICE_PER_1M=0.80
go run ./bench/answer-quality -n 20
```

## Generic ≠ your domain (read this)

The bundled `testdata/generic.json` is a **wiring smoke** — general-knowledge Q&A
whose only job is to prove the pipeline + judge run end-to-end. **It is not a
measure of your answer quality.** The **only** numbers that reflect your
production system come from running this harness on **your own** domain Q&A
(`testdata/domain.example.json` is the template). The report repeats this at the
bottom of every run.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-n` | `20` | number of questions to run |
| `-cap` | `200` | hard maximum for `-n` (bounded-spend guard) |
| `-data` | bundled generic | path to a question set JSON |
| `-out` | `results.txt` (next to the source) | where to write the report |
| `-quiet` | `false` | suppress runtime/embedder log noise |

See ADR-0059 (`docs/decisions/0059-*`) for the design and the honest
generic-vs-domain gap.
