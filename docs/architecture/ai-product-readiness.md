---
type: Reference
title: "AI-product readiness map — does our AUTOMATED test surface cover the canon?"
description: A no-blank-cells audit of aisotropy's automated-test coverage against what the canonical references (Chip Huyen, "AI Engineering"; OWASP LLM Top 10 2025; OWASP Agentic 2026; industry LLMOps guidance) recommend for building customer-facing products on foundation models and AI agents. Every reference concern area appears with an enforcing component, its automated coverage TODAY (arena dimension / OWASP test / unit test / bench harness / NONE), real-vs-stub, and a COVERED / PARTIAL / GAP / N-A status. Every PARTIAL and GAP carries a concrete, minimal, NOT-yet-implemented proposed automated test plus a priority. Companion to capabilities-and-gaps.md, product-evaluation.md, and the two OWASP coverage docs.
tags: [architecture, readiness, evaluation, guardrails, rag, agents, latency, cost, drift, monitoring, owasp, huyen, testing, coverage]
timestamp: 2026-07-23T00:00:00Z
---

# AI-product readiness map — automated-test coverage vs. the canon

**The operator's frame:** *do our AUTOMATED tests cover what a customer-facing product built
on foundation models and AI agents is supposed to cover?* This document answers that with a
**no-blank-cells** table: every reference concern area appears with its enforcing component, the
automated coverage that exists **today**, whether that coverage runs against a **real model** or
the **keyless stub**, and a status. Every `PARTIAL`/`GAP` carries a concrete, minimal, **not yet
implemented** proposed test and a priority, so the operator can approve a follow-up wave.

It is a companion to — and does not duplicate — the code-grounded surfaces it audits:
[`product-evaluation.md`](product-evaluation.md) (the eval harness), [`capabilities-and-gaps.md`](capabilities-and-gaps.md)
(the honest capability snapshot), [`../security/owasp-llm-coverage.md`](../security/owasp-llm-coverage.md)
and [`../security/owasp-agentic-coverage.md`](../security/owasp-agentic-coverage.md) (the security suites).

## Grounding (public sources only — book text is NOT reproduced; topic areas are transformed)

The reference concern areas below are extracted at **table-of-contents / summary level** from
public sources and re-expressed as our own testable checklist. No copyrighted book prose is
reproduced.

| Reference | What we took from it | Public source (accessed 2026-07-23) |
|---|---|---|
| Chip Huyen, *AI Engineering* (O'Reilly, 2025) | Chapter/topic areas: Ch3 Evaluation Methodology (exact eval, **AI-as-a-judge**, comparative eval), Ch4 Evaluate AI Systems (evaluation **criteria**, model selection, evaluation **pipeline**), Ch5 Prompt Engineering (**defensive prompt engineering**, tool eval), Ch6 RAG and Agents (retrieval, tools, planning, memory), Ch9 Inference Optimization (latency + cost **performance metrics**), Ch10 Architecture and **User Feedback** (monitoring, feedback, drift). | Author's open companion repo <https://github.com/chiphuyen/aie-book> ; TOC via <https://deepwiki.com/chiphuyen/aie-book/2.1-table-of-contents> ; O'Reilly listing <https://www.oreilly.com/library/view/ai-engineering/9781098166298/> |
| OWASP Top 10 for LLM Applications **2025** (v4.2.0a, published 2024-11-18) | LLM01–LLM10 category list | <https://genai.owasp.org/resource/owasp-top-10-for-llm-applications-2025/> |
| OWASP Top 10 for **Agentic** Applications **2026** (released 2025-12-09) | ASI01–ASI10 category list | <https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/> |
| Industry LLMOps / production guidance (cross-reference, to catch what one book misses) | Continuous-evaluation loop; **guardrail-drift** re-measurement; monitoring **latency, token cost, hallucination rate, output drift, user-satisfaction signals**; weekly resampling of production inputs through eval. | Microsoft Learn, "Safeguarding LLM security & safety evaluations" <https://learn.microsoft.com/en-us/ai/playbook/technology-guidance/generative-ai/mlops-in-openai/security/operationalize-security-safety-evaluations> ; Datadog, "LLM guardrails: best practices" <https://www.datadoghq.com/blog/llm-guardrails-best-practices/> |

### How to read the status column

- **COVERED** — an automated test asserts the property today (in CI or a runnable harness).
- **PARTIAL** — a real mechanism + some automated coverage exists, but a named gap remains
  (usually: the assertion runs only on the stub, or measures without a pass/fail gate).
- **GAP** — no automated test asserts it today.
- **N/A** — the reference recommends something that genuinely does not apply to an **in-process,
  multi-tenant runtime that calls a provider model it does not host or train**; reason given.

**Real-vs-stub** is load-bearing: much of the eval surface is exercised end-to-end in CI against
`internal/llm.StubClient` (deterministic, keyless — proves the *wiring*), while the **real-model**
quality numbers come from opt-in bench harnesses that need a provider key and are **run manually**,
not gated in CI. A row can be `COVERED` for wiring and still have a real-model regression gap — the
"Real/stub" column and the proposed tests make that explicit.

---

## A. Evaluation methodology (Huyen Ch3–4)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Evaluation harness exists & maps a real pipeline | `internal/eval/doc.go`, `guide.go:132` (`RunSuite`) | `internal/eval/*_test.go` (CI unit) | stub | **COVERED** |
| AI-as-a-judge (typed verdict, not free text) | `internal/eval/judge.go:53` (`LLMJudge`), `:104` routes through `llm.GollemReflect` | `internal/eval/judge_test.go` | stub | **COVERED** (wiring); real-model judge quality is opt-in bench only |
| Code / exact-function judge | `internal/eval/judge.go:189` (`CodeJudge`, JSON-validity/keyword) | `internal/eval/judge_test.go` | stub (model-free) | **COVERED** |
| Evaluation criteria as first-class data (relevance / faithfulness / safety + rubrics) | `internal/eval/guide.go:198` (`Relevance`), `:214` (`Faithfulness`), `:229` (`Safety`) | `internal/eval/guide_test.go` | n/a (definitions) | **COVERED** |
| Answer-correctness on **open-ended** output | arena `scoring.ScoreQA` (`scoring.go:83`, deterministic answer-key) + `bench/answer-quality` LLM-judge correctness (`bench/answer-quality/judge.go`) | arena scoring unit-tested; bench harness self-tested | arena=deterministic; bench=**real** but **manual** | **PARTIAL** — no CI gate on real correctness |
| Faithfulness / groundedness (anti-hallucination) | `eval.Faithfulness` criterion; `bench/answer-quality/judge.go:18` (`hallucinationThreshold`, derived hallucination %) | bench harness self-tested; criterion unit-tested | **real** but **manual** | **PARTIAL** — measured on manual runs, no offline unit assertion, no CI gate |
| Comparative / pairwise-preference evaluation | — | — | — | **GAP** — book Ch3 lists comparative eval; no A/B or pairwise harness exists |
| Regression / eval-in-CI on a checked-in golden set | `bench/rag-quality` (nDCG 0.969 on XQuAD), `bench/answer-quality` | harnesses exist; **not** wired into `.github/workflows/ci.yml` | stub in CI; real manual | **PARTIAL** — see Top Gap #1 |

## B. RAG & retrieval quality (Huyen Ch6)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Retrieval IR metrics (Precision/Recall/F1/MRR/**nDCG**@k) against the **real** store | `internal/eval/metrics.go:52`/`:112`/`:147`; `internal/eval/retrieval.go:66` (`EvalRetrieval`, tenant-scoped) | `internal/eval/metrics_test.go`, `retrieval_test.go` | stub embedder (Hash) in CI; **real** in `bench/rag-quality` (manual) | **COVERED** (wiring); real-model gate is manual |
| Retrieval hit@1 on grounded questions | arena `scoring.ScoreRAG` (`scoring.go:135`) | `scoring_test.go`; arena run | deterministic scorer; real retrieval in arena run | **COVERED** |
| Multi-hop retrieval recall (all gold docs present) | arena `scoring.ScoreRAGMultiHop` (`scoring.go:173`) | `scoring_test.go` | deterministic | **COVERED** |
| RAG trap-refusal (refuse instead of inventing) | arena `scoring.ScoreRAG` trap branch (`scoring.go:137`), `LooksLikeRefusal` (`:126`) | `scoring_test.go`; arena `safeguard`/`rag` | deterministic + real arena | **COVERED** |
| Reranking maintains relevance | `internal/semantic/rerank.go:18` (`LexicalReranker`) | `internal/semantic/rerank_extra_test.go` | stub | **COVERED** (lexical; cross-encoder reranker is ADR-0006-open, not a test gap) |
| Embedding-scope isolation (no cross-tenant retrieval) | `internal/semantic/memory_store.go:60` (in-scan filter) | `internal/semantic/owasp_llm08_embedding_test.go`, `isolation_test.go` | stub embedder | **COVERED** (strength) |

## C. Agents & tool use (Huyen Ch6; OWASP LLM06/ASI02/ASI10)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Tool selection: right tool, right args, actually executed | arena `scoring.ScoreTools` (`scoring.go:239`), `ScoreToolsNumeric` (`:303`) | `scoring_test.go`; arena `tools` mode | deterministic scorer; **real** tool exec in arena | **COVERED** |
| Tool **abstention** (don't over-call when a direct answer is right) | arena `scoring.ScoreToolsAbstain` (`scoring.go:333`) | `scoring_test.go` | deterministic | **COVERED** |
| Excessive agency: scoped-out tool unreachable from the loop | `internal/tools/tools.go:43` (`Permitted`); `internal/agentloop/agentloop.go:117` | `internal/tools/owasp_llm06_agency_test.go`, `internal/agentloop/owasp_asi02_tool_misuse_test.go` | stub | **COVERED** |
| Bounded autonomy (step cap, no self-spawn) | `internal/agentloop/agentloop.go` (`MaxSteps`) | `owasp_asi02_tool_misuse_test.go` | stub | **COVERED** (capability containment); misalignment detection is a model property → out of unit scope (ASI10 `partial`) |
| Agent multi-turn memory carry | arena `memory` / `multi_turn_memory`, `scoring.ScoreMemory` (`scoring.go:115`); `internal/memory` | `scoring_test.go`; `internal/memory/*_test.go`; arena run | deterministic + real arena | **COVERED** |
| Sandboxed code execution (powerless-by-construction) | `internal/sandbox/wasm.go` (wazero, no net/fs); `hardening.go:25` (`--network=none`) | `internal/sandbox/owasp_asi05_sandbox_test.go`, `wasm_test.go`; `bench/wasm-vs-docker` | stub | **COVERED** |

## D. Prompt engineering & defensive prompting (Huyen Ch5; OWASP LLM01/LLM04/ASI01/ASI06)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Prompt-injection **detection** (direct) | `internal/guardrails/injection.go:150` (`Detector.Inspect`); evasion folding `normalize.go:93` | `internal/guardrails/owasp_llm01_injection_test.go` (28-payload corpus) | stub | **COVERED** |
| Prompt-injection **enforcement** posture (annotate vs block) | `internal/api/guard.go:79`/`:98` | `internal/api/owasp_llm01_guard_modes_test.go` | stub | **COVERED** (both postures) |
| Indirect injection: fence untrusted context as **data** | `internal/api/chat_stream.go:108` (`sanitizeUntrusted`), `:399` (re-scan on hydration) | `internal/api/owasp_llm04_fence_test.go` | stub | **COVERED** |
| Dual-LLM quarantine of untrusted content (fail-closed) | `internal/guardrails/quarantine.go:137` | `internal/guardrails/owasp_llm04_quarantine_test.go` | stub | **COVERED** |
| Model-side **decline** of jailbreak / identity-reveal (real behaviour) | `internal/api/persona.go`, `internal/llm/gollem.go` (ADR-0068 hardening, concurrent) | `owasp_llm07_persona_test.go` locks current behaviour; arena `safeguard_decline`/`safeguard_nocontain` | stub tests + **real** arena probes | **PARTIAL** — arena shows current failure; hardening not merged (ADR-0068) |
| Structured-output contract holds (schema-following, fail-closed repair) | reflection loop `internal/llm/reflect.go` + `gollem.go` (`GollemReflect`); arena `scoring.ScoreStructured`/`ScoreStructuredWithSchema` (`scoring.go:541`/`:582`) | `internal/llm/reflect_test.go`, `schema_test.go`; `internal/guardrails/owasp_llm05_output_handling_test.go`; `scoring_test.go` | stub | **COVERED** |

## E. Guardrails: input, output, PII (OWASP LLM02/LLM05/LLM07; Datadog guidance)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Input PII/secret **redaction** (secret never reaches memory/model) | `internal/guardrails/redact.go` (email, credit-card, API-key, bearer, private-key, IP, phone, postal, street) | `internal/guardrails/redact_test.go`; `internal/llm/owasp_llm02_redaction_test.go` | stub | **COVERED** for regex-matchable classes |
| PII: **names / national-IDs (SSN/DNI/passport) / health identifiers** | `redact.go` doc explicitly scopes these OUT (needs NER) | — | — | **GAP** — documented follow-on; no NER pass, no test |
| Sensitive-info non-disclosure on **telemetry** (spans/logs) | `internal/llm/otel.go:172` (`redactingExporter`); `internal/o11y/handler.go` (`CtxHandler`) | `owasp_llm02_redaction_test.go`, `internal/o11y/owasp_llm02_ctxhandler_test.go` | stub | **COVERED** |
| Output toxicity / harmful-content check | `internal/guardrails/output.go`; `eval.Safety` judge via `internal/api/eval_output_check.go:81` (`EvalOutputCheck`) | `internal/guardrails/output_test.go`; `internal/api/*` judge tests | stub | **COVERED** (judge is opt-in `AISOTROPY_GUARDRAILS_LLM_JUDGE`, `app.go:905`) |
| Output system-prompt / identity leak detection | `internal/guardrails/output.go:168` (`Leak`); `internal/api/persona.go:161` | `internal/guardrails/owasp_llm07_output_leak_test.go`, `owasp_llm07_persona_test.go` | stub | **COVERED** (detection); real-model non-leak → arena (`partial`, ADR-0068) |
| Guardrail **drift** re-measurement (do guardrails still block as code evolves?) | the OWASP deterministic suite IS the regression net (`scripts/owasp-llm-scan.sh`) | `scripts/owasp-llm-scan.sh` (10/10 PASS), but **not** wired into CI | stub | **PARTIAL** — see Top Gap #2 |

## F. Safety & security surface (OWASP LLM Top 10 2025 + Agentic 2026)

The full no-blank-cells security map lives in the two dedicated docs; this row is a pointer so the
readiness map stays complete without duplicating them.

| Concern area | Enforcing component | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| OWASP LLM Top 10 2025 (LLM01–LLM10) | see [`owasp-llm-coverage.md`](../security/owasp-llm-coverage.md) | `scripts/owasp-llm-scan.sh` → 10/10 PASS; per-category deterministic tests | stub (offline) + real arena probes | **COVERED** (LLM07 two rows `partial`, ADR-0068; LLM03/LLM09 `N/A`-by-process) |
| OWASP Agentic 2026 (ASI01–ASI10) | see [`owasp-agentic-coverage.md`](../security/owasp-agentic-coverage.md) | same scan (ASI02+ASI05 direct; ASI01/03/06/08 via cross-ref tests) | stub | **COVERED** (ASI04/ASI07 `N/A`; ASI09/ASI10 misalignment-half `partial`) |
| Unbounded consumption / per-tenant quota (pre-spend gate) | `internal/memory/quota.go:74` (`Limiter.Allow`) | `internal/memory/quota_test.go`, `owasp_llm10_consumption_test.go` (concurrent multi-tenant) | stub | **COVERED** (constitutional gate) |
| Multi-tenant isolation by construction | `context.Context` propagation; `semantic/memory_store.go:60`; `internal/memory/key.go` | `owasp_llm08_embedding_test.go`; arena `isolation_forgetful` (`scoring.ScoreForgetful`, `scoring.go:510`); `aisotropy/isolation_e2e_test.go` | stub + real arena | **COVERED** (strength / moat) |
| Cascading-failure containment (circuit breaker) | `internal/llm/breaker.go` | `internal/llm/breaker_test.go`; `internal/api/hydration_degradation_test.go` | stub | **COVERED** |

## G. Inference optimization: latency & cost (Huyen Ch9)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Time-to-first-token measured | arena `scoring.ScoreStream` (`scoring.go:782`); `bench/streaming` | `scoring_test.go`; `bench/streaming` harness | deterministic + stub-rate transport | **COVERED** (measurement) |
| End-to-end latency percentiles (p50/p95/p99) under load | `bench/load/main.go:517` (`pct`), TTFT p50/p95/p99 | `bench/load` harness (self-tested) | stub model (isolates runtime overhead) | **COVERED** (measurement) |
| Latency **SLO** assertion / perf-regression gate | — (bench prints numbers; no threshold assert, not in CI) | — | — | **GAP** — see Top Gap #4 |
| Token-cost accounting (provider-reported usage, per-tenant) | `internal/llm/usagemeter.go`; `internal/costmeter/costmeter.go` (`GET /costs`) | `usagemeter_test.go`, `costmeter_test.go` | stub token counting | **COVERED** (accounting) |
| Context-token efficiency vs. full-history baseline | `bench/token-economy` (surgical context vs. buffer-memory) | `bench/token-economy` harness | stub (offline) | **COVERED** (measurement) |
| Cost **budget** enforcement per request ($ ceiling / max-cost gate) | quota is a **token-count** bucket, not a $-budget; no per-request cost ceiling | — | — | **PARTIAL** — see Top Gap #5 |
| Service optimization: response/semantic cache | `internal/responsecache/` | `internal/responsecache/*_test.go` | stub | **COVERED** |
| Deployment footprint (image/RSS/cold-start) | `bench/footprint` | `bench/footprint` harness | offline stub | **COVERED** (measurement) |

## H. Feedback, monitoring, drift (Huyen Ch10 + industry LLMOps)

| Concern area | Enforcing component (file) | Automated coverage TODAY | Real/stub | Status |
|---|---|---|---|---|
| Telemetry / observability (OTel `gen_ai` spans, tenant-native) | `internal/llm/otel.go` (`WithOTel`); `GET /costs`; `/health` (`internal/health/health.go`) | `internal/llm/otel_test.go`; `internal/health/health_test.go`; arena `observability` dim | stub | **COVERED** (emission) |
| **User-feedback capture** (thumbs / rating → dataset → eval loop) | AG-UI floor `internal/api/agui.go` (HITL events declared **out of scope**); no feedback endpoint | — | — | **GAP** — see Top Gap #3 |
| Production **drift** detection (input-distribution, output-quality, guardrail-effectiveness drift over live traffic) | — (the "drift" references in code are *config-drift* comments, not data/quality drift) | — | — | **GAP** — see Top Gap #6 |
| Continuous-evaluation loop (periodic resample of prod inputs through eval) | eval harnesses exist offline; no scheduled/prod-sampled loop | — | — | **GAP** — folded into Top Gap #1/#6 |
| Hallucination-**rate** monitoring signal | `bench/answer-quality` derives hallucination % on a manual run | bench harness | **real**, manual | **PARTIAL** — computed, not monitored/gated |

## I. Not applicable to an in-process runtime that calls a provider model (declared with reason)

| Reference concern | Why N/A for aisotropy | Status |
|---|---|---|
| Finetuning (Huyen Ch7) | aisotropy is a **runtime**, not a training pipeline; it serves BYO provider models and BYO embedders (`AISOTROPY_EMBED_URL`). Finetuning happens upstream, outside the product boundary. | **N/A** |
| Dataset engineering for training (Huyen Ch8: dedup, tokenization, data pipelines) | Same boundary — the product ingests *knowledge* (scoped RAG, `App.IngestFile`), it does not build *training* datasets. (Consolidation dedup in the sleep-cycle is a memory-hygiene path, not training-data engineering.) | **N/A** |
| Model-level inference optimization (Ch9: accelerators, quantization, kernel/serving optimization) | aisotropy does not host or serve model weights; it calls a provider's OpenAI-compatible/Anthropic endpoint. Latency/cost we CAN influence (context assembly, caching, streaming transport) is covered in §G. | **N/A** for the model-hosting half |
| OWASP ASI07 Insecure Inter-Agent Communication | No autonomous agent-to-agent message bus exists in the runtime (tools are host-executed, not peer agents). Reserved if A2A is added. | **N/A** |
| OWASP LLM03 Supply Chain | Covered by process/CI (license gate + `go.sum` pinning + `scripts/license-audit.sh`), not a runtime unit — see the OWASP LLM doc. | **N/A**-by-process |

---

## Top gaps — ranked, highest automated-test value first

Each is a **not-yet-implemented** proposal (one-line test that would close it). Priority: **P1**
directly protects a shippable customer-facing quality/safety property; **P2** hardens an existing
strength; **P3** completeness.

1. **[P1] No CI gate on real answer/retrieval quality regressions.**
   The eval harnesses (`bench/answer-quality`, `bench/rag-quality`) run **real** numbers only on
   manual, key-bearing runs; CI exercises them on the stub (wiring only). A quality regression can
   merge unnoticed.
   *Proposed test:* check in a small frozen golden set (~15 Q/A + relevant-doc labels) and add a
   `internal/eval`-driven test (or a `-short`-skippable job) that fails when `EvalRetrieval` mean
   nDCG@k or the deterministic `ScoreQA` pass-rate over the golden set drops below a pinned floor.
   Runs on the stub embedder/model deterministically → real CI gate, no key needed.

2. **[P1] The OWASP deterministic suite is not wired into CI.**
   `scripts/owasp-llm-scan.sh` is the guardrail-drift regression net (Datadog/Microsoft both name
   guardrail-drift re-measurement as core), currently run by hand.
   *Proposed test:* add an `owasp-scan` job to `.github/workflows/ci.yml` running
   `bash scripts/owasp-llm-scan.sh` (exit 0 = clean). One CI line converts an ad-hoc scan into an
   enforced gate.

3. **[P1] No user-feedback capture path (thumbs/rating → eval dataset).**
   Ch10's feedback loop and the industry "user-satisfaction signal" have no landing surface; AG-UI
   HITL is explicitly out of scope.
   *Proposed test:* add a minimal `POST /v1/tenant/{tid}/user/{uid}/feedback` handler (turn-id +
   score + optional text, tenant-scoped) with an `internal/api` test asserting the record is
   persisted under the correct scope and rejected cross-scope. (Feature + test together; specify
   before building.)

4. **[P2] Latency is measured but never asserted.**
   `bench/load` prints p95/p99; nothing fails when they regress.
   *Proposed test:* a `bench/load` (or CI-`-short`) assertion that stub-model p99 for `/chat` stays
   under a pinned ceiling and does not exceed 10× the lowest-concurrency p99 — the harness already
   computes both, this only adds the `t.Fatalf`.

5. **[P2] Quota is token-count, not a cost budget.**
   The pre-spend gate bounds *tokens per tenant*, not *dollars per request*; a customer-facing
   product typically needs a max-cost ceiling.
   *Proposed test:* extend `internal/costmeter` with a per-request/per-tenant $-ceiling and a test
   proving a request whose projected cost exceeds the ceiling is degraded/refused **before** the
   LLM call (mirrors the `quota_test.go` pre-spend pattern).

6. **[P2] No production-drift detection.**
   Nothing samples live traffic to detect input-distribution or output-quality drift (Ch10 +
   industry "output drift / weekly resample").
   *Proposed test:* a `internal/eval` drift primitive that, given two score distributions
   (baseline vs. recent window), flags a drop beyond a threshold; unit-tested on synthetic
   distributions. Deterministic, no model needed; the sampling wiring is a follow-on.

7. **[P2] Faithfulness/hallucination has no offline unit assertion or CI gate.**
   Only the manual `bench/answer-quality` run derives hallucination %.
   *Proposed test:* a `CodeJudge`-style offline faithfulness check over a frozen (context, answer,
   is-grounded) fixture set that fails when a known-hallucinated fixture scores as faithful —
   folds into the Top Gap #1 golden-set job.

8. **[P3] No comparative / pairwise-preference eval.**
   Ch3 lists comparative evaluation; only absolute scoring exists.
   *Proposed test:* a `RunPairwise(a, b, judge)` helper in `internal/eval` returning a
   win/lose/tie verdict, unit-tested with the stub judge — enables A/B prompt/model comparison the
   absolute scorers can't express.

---

## Verification

Re-derive the repo-side claims (component existence + which tests run):

```bash
# Deterministic security suite (the guardrail-drift net referenced throughout §D–F)
bash scripts/owasp-llm-scan.sh                       # expect: RESULT CLEAN, 10/10 PASS

# The eval harness + arena scorers exist and pass on the stub (CI-equivalent, no key)
go test ./internal/eval/... ./arenas/live/runner/scoring/...

# Arena determinism gate (primary scores are pure functions)
bash scripts/arena-determinism-guard.sh

# Confirm the real-model bench harnesses exist but are NOT in CI (the PARTIAL rows)
ls bench/answer-quality bench/rag-quality bench/load bench/token-economy
grep -n "owasp-llm-scan\|bench/" .github/workflows/ci.yml || echo "not gated in CI (expected)"
```

_Last authored 2026-07-23. Book topic areas grounded at TOC/summary level from the public sources
in the grounding table; OWASP baselines and per-category evidence are carried by the two dedicated
security docs. Re-check the OWASP resource pages for newer releases before trusting a baseline._
