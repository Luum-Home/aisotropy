---
type: Concept
title: OKRs — Objectives and Key Results
description: Measurable objectives per phase to build Aisotropy MVP-first, with verifiable Key Results and success metrics.
tags: [roadmap, okr, metrics, mvp]
timestamp: 2026-07-08T00:00:00Z
---

# OKRs — Objectives and Key Results

Each objective is a *demonstrable* increment of the [walking skeleton](index.md). Key Results are
**verifiable** (a test, a metric, a binary that runs) — not aspirations.

## O1 — The single contract works (Phase 0-1)
> *Prove that a Go struct governs HTTP + OpenAPI + LLM with no magic.*

- **KR1.1** — A Huma endpoint with one struct generates a valid `/openapi.json` and validates input automatically.
- **KR1.2** — Gollem receives the same struct, forces structured output, and returns valid JSON against the schema.
- **KR1.3** — Reflection loop demonstrated: given an injected invalid JSON, it re-requests and corrects within ≤ N retries.
- **KR1.4** — SSE streaming with measurable TTFT; cancelling the request cuts the LLM call (verified by token logs).

## O2 — Working memory compresses without losing coherence (Phase 2)
> *The context window never grows unbounded.*

- **KR2.1** — BadgerDB stores/reads `tenant:user:session` in < 1 ms (benchmark).
- **KR2.2** — Summary buffer triggers past N messages and reduces input tokens ≥ 50% over a long session.
- **KR2.3** — A 30-turn conversation test maintains coherence with bounded context.

## O3 — The system remembers across sessions (Phase 3)
> *Engram pattern operational: stable facts retrievable via FTS5.*

- **KR3.1** — Async fact extraction when the session closes (post-response goroutine).
- **KR3.2** — FTS5 retrieves the top 3-4 engrams by `Relevance × Importance × Recency`.
- **KR3.3** — Contradiction detection: a new fact archives/degrades the old one (supersede test).

## O4 — RAG isolated per tenant (Phase 4)
> *Zero context leakage between tenants.*

- **KR4.1** — Qdrant with a `tenant_id`/`user_id` payload filter; isolation test: tenant A never sees tenant B's vectors.
- **KR4.2** — Reranking reduces injected context to ≤ 4 fragments while maintaining relevance.
- **KR4.3** — Deterministic UUID v5: reprocessing the same message doesn't duplicate vectors (idempotency).

## O5 — Consistency under failure (Phase 5)
> *If a node goes down mid-way, memory converges instead of corrupting.*

- **KR5.1** — Transactional Outbox: message + event in the same local ACID transaction in BadgerDB.
- **KR5.2** — Worker with exponential backoff; chaos test: killing the process mid-sync loses no data and creates no duplicates.
- **KR5.3** — Cold-boot reconciliation: on startup, orphaned `outbox:pending:*` entries are drained.

## O6 — Offline self-improvement (Phase 6)
> *The agent is smarter the next day, without touching the core.*

- **KR6.1** — DataFusion runs in the sleep cycle over the `.db`/Parquet files and produces dedup + noise cleanup.
- **KR6.2** — Friction detection (high retry counts) triggers a rewrite of a primitive's description per `user_id`.
- **KR6.3** — An `agent.optimization.*` OTel event is emitted for every primitive mutation (auditable).

## O7 — Ready for enterprise (Phase 7)
> *Granular control: security × cost × behavior.*

- **KR7.1** — Per-tenant rate limiting with model degradation at 95% quota (or a clean 429).
- **KR7.2** — Tools injected according to JWT scopes: without permission → the primitive doesn't exist for the LLM.
- **KR7.3** — Every interaction emits an OTel Span with `gen_ai.usage.*` → cost per token per tenant on the dashboard.
- **KR7.4** — Distroless binary < 50 MB, startup < 1 s, idle RAM < 50 MB (everything except Qdrant in one container).

## O8 — Compression as a cognitive layer (cross-cutting, Bellard)
> *The [compression thesis](../vision/compression-thesis.md) made measurable. Start with the green bets.*

- **KR8.1** — [Bet 6](../research/bet-06-bits-per-token-metric.md) instrumented: `bits/token/tenant` with and without memory. Positive and measurable memory lift.
  - **Design accepted** ([ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md) / [ADR-0015](../decisions/0015-bpt-eval-model-and-python-batch-runtime.md)): an **offline** fixed-reference-model scorer (`internal/bitrate`, [component](../components/bitrate-bpt-scorer.md)) — **not** a serving-span OTel attribute (no provider exposes log-probs). Lift = `bpt_without − bpt_with` over a held-out sample, surfaced via `GET /bitrate` + the OTel `agent.compression.bpt` span event. Scorer staged Python→Rust.
  - **Spike-validated** offline: +1.469 / +1.731 bits/token lift (distilgpt2 / DeepESP-gpt2-spanish), see [spike](../../spikes/compression/README.md).
  - **✅ Runtime lift verified end-to-end** (2026-07-10) on seeded consolidated memory through the actual coordinator→hydrator→scorer→store path: **lift ≈ 6.64 bits/token** (`bpt_with 4.61` vs `bpt_without 11.25`, distilgpt2 `LM_REAL`), with the `Measurement` round-tripping through the durable store. Locked by `internal/api` `TestLiveMemoryLiftEndToEnd` (`//go:build manual`).
  - **✅ Sustained gradual descent demonstrated (controlled simulation)** (2026-07-10): two manual tests (`//go:build manual`) drive the real coordinator→hydrator→scorer→store path.
    - `TestLongitudinalDownwardTrend` (fixed narrow probe): `bpt_with` 8.10→2.83 (slope −1.08) but the drop is anchor-dominated — a *fixed* probe saturates after its one relevant fact appears. Proves the series/slope **mechanism**.
    - `TestSustainedTrendDiverseProbe` (fixed **diverse** K=5-topic probe, coverage grown one topic per step) closes that gap: aggregate `bpt_with` falls **7.55 → 5.31** *monotonically* (slope −0.58), with **76% of the descent occurring after the first step** (`postAnchorFraction 0.76`, max per-step rise 0.0) — i.e. it keeps learning as coverage broadens, **not** an empty-anchor artifact. Series persists + round-trips. Reproduced under DeepESP and distilgpt2.
  - **✅ Production-instrument harness** (`cmd/bitrate-harness`, 2026-07-13): the same coordinator→hydrator→scorer→store descent, but driven end-to-end against the **real `aisotropy.App`** and read back through the **live `GET /bitrate` HTTP surface** — not just an in-test store. Real DeepESP scorer subprocess (`LM_REAL`): aggregate `bpt_with` **7.55 → 5.29** monotonically (slope **−0.586**, drop 2.26 bpt, `postAnchorFraction 0.76`, max per-step rise 0.0); `/healthz`, `/health`, `/bitrate` all HTTP 200, the "acme" trend served with `trend_slope 0.586`. Runs on the **in-process fallback tiers** (SQLite/cosine) by default; setting `RQLITE_URL`+`QDRANT_HOST` at the docker-compose rqlite+Qdrant makes the *identical* harness exercise the real networked substrate (no code change). This is the last engineering rung before field data — but still a **simulated evolving tenant** (authored accumulation schedule): it validates the *production instrument*, not real traffic.
  - **⏳ Still pending (real-field, cannot be simulated):** the same downward curve on **real tenant traffic over time** — a genuinely diversifying, longitudinal per-tenant interaction stream. The simulations prove the metric *captures* gradual learning; only real deployment data closes this last half of the KR.
- **KR8.2** — [Bet 1](../research/bet-01-surprisal-gated-memory.md) in the sleep cycle: the surprisal gate reduces the facts store ≥ 30% without recall loss (quality vs. τ curve).
- **KR8.3** — [Bet 2](../research/bet-02-working-memory-codec.md) PoC: history compressed with a local model + arithmetic coder to < 1 bit/char, 100% lossless reconstruction, beating gzip.
- **KR8.4** — [Bet 4](../research/bet-04-compressibility-routing.md): the compressibility signal correlates with difficulty and lowers routing cost without lowering quality.
  - **Refuted for every intrinsic/self-confidence signal** (2026-07-10→13): compressibility ρ=0.41, embedding ρ=−0.52, base-cascade ρ=0.56, instruct-cascade + competence ladder 0.5B→1.5B all sub-0.6 — a plain `len()` router (ρ=0.94) beats them; it's a *mechanism* failure, not a 0.5B ceiling (only a frontier rung is untested).
  - **Off-thesis escape hatch validated in principle:** a **trained** classifier on the e5 embedding, with length regressed out, recovers difficulty at **residual ρ=0.672, p<1e-4** (`bet04e_trained_router.py`) — supervised, N=30 caveat. The cheap-vs-cheap answer-quality gap (0.5B vs 1.5B) is refuted (ρ=0.330 n.s.). So Bet 4's *cost* payoff, if pursued, is a small trained head on Bet 5's embedding, not a raw bits signal.
- **KR8.5** *(vision)* — [Bet 5](../research/bet-05-sovereign-nn-core.md): first sovereign NN core operation (`embed()`) in-process, zero network — validated against the external API.
  - **✅ First brick shipped + measured** ([ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md)): a candle subprocess `embed()`, sovereign (local MIT weights, zero network), real semantic separation (cosine sim 0.913 > 0.710). Latency measured (`cmd/embed-bench`, e5-small, arm64-mac CPU): current subprocess **p50 423.6 ms**, ~95% spawn+load; a warm `--serve` daemon drops it to **p50 22.5 ms (18.8×)**.
  - **⚠️ "in µs" corrected:** the embedding *inference floor* on CPU is **~22 ms**, not µs — µs is unreachable for a BERT-class model on CPU regardless of transport (it would need a much smaller/quantized model or a different op). The realistic in-process target is **~20 ms warm, zero spawn/load tax** (the daemon path); cgo→candle is only a marginal IPC-shaving gain on top. See the [inference-path report](../06-Daily/reports/bet5-sovereign-nn-core-inference-and-weights-2026-07-13.md).

## Cross-cutting guiding metrics (North Star)

| Metric | Objective | Why |
|---|---|---|
| **`bits/token/tenant` (memory lift)** | **Lift > 0 and a downward curve** | **How well the system knows the user, measured in bits ([Bet 6](../research/bet-06-bits-per-token-metric.md)).** |
| Input tokens per request vs. naive baseline | **−72% measured** (cumulative/50 turns; −83% per-request at depth) | The efficiency thesis. |
| Time-to-First-Token (TTFT) | < 500 ms (excl. provider latency) | Interactive UX. |
| Context leakage between tenants | 0 (automated test) | Non-negotiable. |
| Cost per token attributable to tenant | 100% | Governance. |

**On the −70% target:** now a measured Key Result, not just an objective. The reproducible harness
in [`bench/token-economy/`](../../bench/token-economy/) reports **−71.8% cumulative through 50 turns
(−83.3% per-request at depth)** against a LangChain-style full-history baseline, under both token
heuristics. Caveats are honest and documented in that README: offline, deterministic, an approximate
tokenizer, a single synthetic conversation distribution, and a bounded deterministic summarizer
standing in for a real LLM one — so it is a benchmark result of that setup, not a universal
guarantee. Consistent with [principles](../vision/principles.md#derived-operating-principles) ("if
you haven't measured it, it's not a claim"): re-run `go run ./bench/token-economy` to reproduce.
