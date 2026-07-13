---
type: Reference
title: Evaluation — the measured numbers, with their caveats
description: The canonical measurement node. Surfaces the real, reproducible results that live in the repo's benchmark/spike harnesses (token-economy reduction, bits-per-token memory lift, the surprisal-gate spike) INTO the knowledge graph, each with its harness, its exact figure, and the caveat that keeps it honest — per "if you haven't measured it, it's not a claim." No number here may be quoted without its caveat.
tags: [research, evaluation, benchmark, measurement, bits-per-token, token-economy, honesty]
timestamp: 2026-07-10T00:00:00Z
---

# Evaluation — the measured numbers, with their caveats

The bundle's epistemic rule is *"if you haven't measured it, it's not a claim"*
([principles](../vision/principles.md)). Two problems this node fixes: (1) the real results live
**outside** `docs/` (in `bench/token-economy/` and `spikes/compression/`), so they are absent from
the knowledge graph; (2) the headline figures are easy to quote **without** their caveats, which is
exactly how a rigorous thesis becomes an overclaim. This node is the single place a number and its
caveat travel together. It is load-bearing for [ADR-0020](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
(the positioning) and the [OKRs](../roadmap/okrs.md) North Star.

## Results at a glance

| Claim | Measured | Harness | Status | The caveat you must quote with it |
|---|---|---|---|---|
| Input-token reduction vs. full-history | **71.8% cumulative** @ 50 turns · **83.3% per-request** at depth | `bench/token-economy` (Go, offline) | ✅ reproducible | Baseline is a **naive `ConversationBufferMemory`-style full-history** flow; a competently-configured LangChain (summarization/trimming middleware) closes much of the gap. Token counts use **two heuristics** (`bytes/4`, `words×1.33`), **not a real tokenizer** (no API in the harness env). It is a **synthetic** conversation. |
| Memory reduces model surprisal (Bet 6, bits/token) | offline spike **+1.469 bits/tok**; **runtime E2E lift ≈6.64 bits/tok** (4.61 vs 11.25); **sustained descent** on a diverse probe **7.55→5.31** (slope −0.58, 76% post-anchor); **production-instrument harness** `7.55→5.29` (slope −0.586) read back through live `GET /bitrate` | `spikes/compression/bet06_memory_lift.py` + `internal/api` `TestLiveMemoryLiftEndToEnd` / `TestSustainedTrendDiverseProbe` (`//go:build manual`) + `cmd/bitrate-harness` (boots the real `aisotropy.App`) | ✅ mechanism validated E2E + in simulation + through the real HTTP surface | Runtime numbers are on **seeded** memory, not **real longitudinal tenant traffic** — the live-field `bpt(tenant,t)` curve over weeks remains **unmeasured** (deployment-gated). The harness boots the real aisotropyd + real DeepESP scorer, but on the **in-process fallback tiers** unless `RQLITE_URL`+`QDRANT_HOST` are set, and its tenant is a **simulated** authored schedule (validates the instrument, not real traffic). Absolute bits/token are tokenizer-specific. |
| Surprisal-gated memory (Bet 1) — decisive re-run | surprisal profile-conditioning **+0.10 AUC** (→0.72), gap +1.14 bits/tok; **but embedding-density separates far better — AUC 0.89, d=1.61** | `spikes/compression/bet01_rerun_context_profile.py` + `bet05_embedding_density_probe.py` (`DeepESP`, MIT `multilingual-e5-small`) | 🟠→ semantic path validated | Surprisal alone = 0.72 (no clean gap). The **semantic** signal (embedding cosine to the profile) reaches 0.89 — the viable gate is a *soft semantic weight* (Bet 5), not a hard surprisal gate. Profile-authorship bias not bounded (one profile); embedding win partly rides on topical proximity (ρ=0.73 with lexical overlap), though a low-overlap subset still separates. |
| Working-memory codec (Bet 2) | GPT-2 ladder, all lossless: **2.34 → 2.21 → 1.98 → 1.88 bits/char** (82M→124M→355M→774M); **beats gzip −42%→−54%** (gzip 4.06) | `bet02_lm_arithmetic_codec.py` + `bet02_model_ladder.py` (distilgpt2…gpt2-large + pure-Python arithmetic coder) | 🟢 method confirmed, target not hit | Bits/char falls **monotonically** with size (capacity axis validated, method not saturating) but **plateaus ~1.9 ≫ 1.0**; extrapolation implies nncp-scale models. Tokenizer confound **quantified**: DeepESP (124M, Spanish) hits **1.97 bits/char ≈ gpt2-medium (355M, English)** on the same text — a language-matched tokenizer is worth ~1.5 param-doublings. `< 1 bit/char` still unreached (1.97 ≈ 2× target); needs nncp-scale **even with** a matched tokenizer. |
| Rate-distortion selection (Bet 3) | smart selection beats naive by **+0.47 bpt** (cheap lexical proxy) / **+0.68 bpt** (oracle) at constrained token budgets (B=64–256) | `spikes/compression/bet03_rate_distortion.py` (`DeepESP`, M=22 fragments) | 🟡 **first support** | Holds only where the budget **binds**: at B=512 the whole pool fits and the gap → noise (±0.1 bpt). Distortion = probe bpt (a Bet-6 proxy), not answer quality. Lexical ranker is weak; the oracle is an upper bound, not a deployable selector — the cheap-hot-path-proxy problem stays open. |
| Difficulty routing (Bet 4) — every cheap signal | compressibility **ρ=0.41**, embedding **ρ=−0.52**, base-cascade entropy **ρ=0.56**, **instruct-cascade (Qwen2.5-0.5B) confidence ρ=0.42–0.50** — all length-confounded; raw length **ρ=0.94** wins | `bet04_compressibility_routing.py` + `bet05_embedding_density_probe.py` + `bet04b_cascade_uncertainty.py` + `bet04c_instruct_cascade.py` (30 prompts) | 🟡 **refuted (all cheap signals, base AND instruct)** | Difficulty is **computational, not textual** — no prompt-intrinsic signal, nor the cheap model's own answer-confidence (base or 0.5B-instruct), beats a plain `len()` router. The one >0.6 result was a generation-cap truncation artifact (caught, refuted). A viable router needs a **trained classifier** or a measured **frontier-vs-cheap answer-quality gap** — off compression thesis. |
| Distroless footprint (Phase 7 DoD-4) | binary **31.14 MiB** · startup **~85 ms** · idle RAM **~30 MB** | `Dockerfile` + measurement in [distroless](../deployment/distroless.md) | ✅ measured PASS | All three inside the DoD targets (<50 MB / <1 s / <50 MB). |

## Token economy — the headline, handled honestly

`bench/token-economy` compares Aisotropy's summary-buffer + surgical-context assembly against a
classic full-history baseline over a 50-turn synthetic conversation. The result **reproduces the
~70% design target** on a cumulative basis (71.8–71.9%) and reaches 83.3% per-request at depth. That
validates the **mechanism** — bounding the context window instead of re-sending everything.

What it does **not** license is "Aisotropy uses 72% fewer tokens than LangChain." The comparator is
the *naive* baseline LangChain classic used by default; the modern answer (LangGraph Store +
summarization middleware) narrows the gap materially. The defensible claim is: *"surgical context
assembly yields ~70% fewer input tokens than a full-history flow on a 50-turn synthetic benchmark,
under a bytes/4 and words×1.33 token heuristic."* Anything shorter is an overclaim
([ADR-0020](../decisions/0020-build-vs-adopt-lang-suite-mastra.md) revisit trigger #6).

## Bits per token — validated offline, not yet live

Bet 6 is the North Star's mechanism: does relevant memory-as-context reduce the bits needed to
predict a session's text? The offline spike says **yes, clearly** (+1.469 bits/token, not marginal).
But the *product* claim — that `bpt(tenant, t)` **trends downward over weeks** for a real tenant as
the system learns them — is a **live-traffic** measurement that has **not** been run (KR8.1 pending).
The spike is a green light for the mechanism, not proof of the North Star. See
[Bet 6](bet-06-bits-per-token-metric.md) (computed **offline in batch**, ADR-0014 — no serving
provider exposes the needed log-probs) and the [bitrate/bpt scorer](../components/bitrate-bpt-scorer.md).

## What is NOT measured yet (the honest gaps)

- **Live `bpt(tenant,t)` curve** on real traffic — the actual North Star (KR8.1).
- **Real-tokenizer** token counts (the economy benchmark uses heuristics).
- **Bets 1–5** beyond Bet 1's inconclusive spike (Bet 2's codec is "proven by Bellard," not
  in-product; Bets 3–5 are framework/vision).
- **Latency/TTFT under load**, and **cross-replica** quota/cost behavior.
- **End-to-end tenant-leak fuzz** across the full authenticated request path (blocked on the
  auth gap — [threat model](threat-model.md) T3).

## References

- Harnesses: `bench/token-economy/README.md` · `spikes/compression/README.md` (kept in-repo, runnable offline).
- [OKRs](../roadmap/okrs.md) (North Star + secondary metrics) · [research bets index](index.md) · [Bet 6](bet-06-bits-per-token-metric.md) · [Bet 1](bet-01-surprisal-gated-memory.md).
- [ADR-0020](../decisions/0020-build-vs-adopt-lang-suite-mastra.md) — the positioning that depends on these numbers being quoted with their caveats.
- [reality ledger](../architecture/reality-ledger.md) · [principles](../vision/principles.md).
