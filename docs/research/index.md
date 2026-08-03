---
type: Section Index
title: Research bets — compression as a cognitive layer
description: The six actionable bets that bring the compression thesis down to experiments with a success criterion in bits.
tags: [research, compression, bellard, bets]
timestamp: 2026-07-08T00:00:00Z
---

# Research bets — compression as a cognitive layer

These six bets bring the [compression thesis](../vision/compression-thesis.md) manifesto
down to concrete experiments. Each doc follows the same template: **Hypothesis · Rationale · How it works ·
Success criterion (in bits) · Architectural fit · Risk and maturity · Dependencies · Next
experiment**. The Bellard rule: nothing is declared "better" without a number in bits.

> **The measured numbers live in [evaluation](evaluation.md)** — the canonical node that surfaces
> the real, reproducible results (token-economy reduction, Bet 6's +1.469 bits/token offline lift,
> Bet 1's inconclusive spike, the distroless footprint) into the graph, each with the caveat that
> keeps it honest. Read a bet's hypothesis here; read what has actually been measured there.

## The ledger

| # | Bet | Maturity | Success criterion (summary) |
|---|---|---|---|
| 1 | [Surprisal-gated memory](bet-01-surprisal-gated-memory.md) | 🟠 mechanism partial via surprisal (0.72 AUC, no clean gap) — but **embedding-density separates it far better (AUC 0.89, d=1.61)**; the viable gate is the *semantic* soft-weight (Bet 5), not surprisal | ↓ memory size at equal retrieval quality |
| 2 | [Working memory as a codec](bet-02-working-memory-codec.md) | 🟢 method confirmed — lossless + beats gzip (−42%→−54%); ladder **2.34→1.88 bits/char (82M→774M)**, plateaus **~1.9 ≫ 1.0**. Tokenizer confound quantified: DeepESP 124M-Spanish = **1.97 ≈ gpt2-medium 355M-English** (matched tokenizer ≈ 1.5 doublings). < 1 bit/char needs nncp-scale even with a matched tokenizer | < 1 bit/char, 100% lossless reconstruction |
| 3 | [Context as rate-distortion](bet-03-rate-distortion-context.md) | 🟡 **first support measured**: smart selection beats naive by **+0.47/+0.68 bpt** at constrained budgets (B=64–256); gap→noise once budget ≥ total content. Selection > volume in the budget-bound regime | lower distortion at fixed token budget |
| 4 | [Compressibility routing](bet-04-compressibility-routing.md) | 🟡 **every intrinsic/self-confidence signal refuted (base, 0.5B & 1.5B instruct)** — compressibility ρ=0.41, embedding ρ=−0.52, base-cascade ρ=0.56, instruct-cascade ρ=0.42–0.50, competence ladder 0.5B→1.5B all sub-0.6 (all length; raw length ρ=0.94) → *mechanism* failure, not a 0.5B ceiling. **But the trained-classifier hatch is now measured & holds:** supervised e5-embedding, length regressed out, ρ=0.672 (p<1e-4) — off-thesis, N=30 caveat. Answer-quality gap (0.5B vs 1.5B) refuted (ρ=0.330 n.s.) | bits↔difficulty correlation; ↓ cost without ↓ quality |
| 5 | [Sovereign NN core](bet-05-sovereign-nn-core.md) | 🔴 ambitious — **next-invest substrate with evidence** (ADR-0021): embedding-density (borrowed MIT model) separates Bet-1 novelty at **AUC 0.89**; the `embed()`-first path is now measured, not just a vision | cognitive hot path in µs, 0 network calls |
| 6 | [Bits-per-token-per-tenant](bet-06-bits-per-token-metric.md) | 🟢 solid | downward curve over time per tenant |

**Legend:** 🟢 start now · 🟡 research phase · 🟠 spiked, inconclusive (needs a re-run with a
changed method before trusting either direction) · 🔴 convergence vision.

**Why Bet 1 is 🟠, not 🟢:** the [compression spike](../../spikes/compression/README.md) ran
this bet twice, against two different causal LMs (`distilgpt2`, then the Spanish-fluent
`DeepESP/gpt2-spanish`, the spike's definitive model). Neither run found a clean surprisal gap
between the "redundant" and "surprising" fact groups: binary classification against the naive
midpoint threshold τ scored 2/7 redundant + 6/8 surprising correct with `distilgpt2`, and
scored *worse* with the Spanish-fluent model — 1/10 redundant + 3/10 surprising correct with
`DeepESP/gpt2-spanish`. Moving to a model that actually understands Spanish ruled out "the
model doesn't know the language" as the cause but did not close the gap; the spike's revised
diagnosis is that sentence-level bits/token measured with `context=""` (surprisal "in a
vacuum") captures **syntactic/lexical predictability**, not the **semantic novelty** the bet
needs to separate the two groups. Bet 6, spiked in the same session, came back positive with
both models (+1.469 and +1.731 bits/token memory lift) — today only Bet 6 has supporting
evidence; Bet 1 does not. A decisive re-run, per the spike's own conclusion and
[bet-01's "Next experiment"](bet-01-surprisal-gated-memory.md#next-experiment-the-minimum-that-validatesrefutes)
note, needs `context` = the user's real profile instead of `context=""`, and/or normalizing for
lexical rarity instead of using raw bits — not just a bigger or better-fit model.

## Recommended order

1. **Bet 6 first:** it builds on the already-decided [sleep cycle Rust worker](../decisions/0005-datafusion-go-rust-boundary.md), is 🟢 solid, has a positive spike result with two models, and instruments the North Star metric.
2. **Bet 1's re-run:** before treating it as "start now," re-spike with `context` = real user profile (not `context=""`) and/or lexical-rarity normalization, per the spike's own diagnosis above. Do not build the sleep-cycle surprisal gate on the current inconclusive evidence.
3. **Yellows (2, 3, 4):** research with bounded prototypes; each has its own "minimum experiment."
4. **Red (5):** the convergence — the in-process NN core that everything else points to. Built once the greens/yellows prove their value.

## Research notes & audits

Dated research write-ups and audits (2026-07/08). These are point-in-time investigations that fed
decisions, ADRs, and arena work; they are not part of the six-bet compression ledger above.

### Gollem exploitation audit (2026-07-17)

Per-subsystem ledger classifying each aisotropy construct as REUSE-CORRECT / REINVENTION-ADOPT-GOLLEM
/ ADDITIVE-JUSTIFIED / EXPLOIT-MORE — the first full application of the dependency-reuse-first premise.

- [SYNTHESIS](gollem-exploitation-audit-SYNTHESIS-2026-07-17.md) — The cross-slice synthesis of the audit.
- [Agent loop / middleware / interceptors / tools / hooks](gollem-exploitation-audit-agent-loop-2026-07-17.md)
- [Control gates (quota / cost / circuit-breaker / degrade)](gollem-exploitation-audit-control-gates-2026-07-17.md)
- [Memory slice](gollem-exploitation-audit-memory-2026-07-17.md)
- [Orchestration / multi-agent graph slice](gollem-exploitation-audit-orchestration-2026-07-17.md)
- [Reflection / self-repair / structured output / schema](gollem-exploitation-audit-reflection-2026-07-17.md)
- [Model/transport abstraction, stub, streaming, MCP, contrib](gollem-exploitation-audit-transport-mcp-2026-07-17.md)

### Competitive & prior-art

- [Archestra vs aisotropy — security-surface gap analysis](archestra-vs-aisotropy-2026-07-17.md)
- [Archestra security architecture — code-level review](archestra-security-arch-2026-07-17.md)
- [Archestra frontend review](archestra-frontend-2026-07-17.md) — Patterns for a future security/observability UI.
- [The full "Lang*" suite map](langchain-suite-map-2026-07-17.md) — How the LangChain ecosystem, the arena, and aisotropy relate.
- [Footprint: aisotropy vs ADK-Go](footprint-aisotropy-vs-adk-go-2026-07-17.md) — Is "bigger than the bare loop" good or bad?
- [Arena prior-art sweep](arena-prior-art-sweep-2026-07-28.md) — Prior art on agent-framework benchmarking.
- [Inspect AI as our trajectory layer, evaluated](inspect-ai-evaluation-2026-07-28.md)

### Arena mechanisms & infra

- [Arena extensibility / plugins](arena-extensibility-plugins-2026-07-17.md)
- [Arena extension mechanisms (beyond MCP) & aisotropy candidates](arena-extension-mechanisms-and-aisotropy-candidates-2026-07-17.md)
- [Arena performance lever 2 — per-framework infra isolation](arena-infra-isolation-2026-07-18.md)

### Isolation, sandbox & protocols

- [WASM vs Docker for aisotropy isolation](wasm-vs-docker-sandbox-2026-07-17.md)
- [WASM/WASI feasibility for the arena](wasm-arena-frameworks-2026-07-17.md) — Can competitors make real outbound LLM calls in-sandbox?
- [Inter-service protocols for aisotropy](inter-service-protocols-2026-07-17.md)
- [OpenAI-compatible is a spectrum](openai-compat-boundary-2026-07-17.md) — Where agnes and gateways like it break.
- [Prompt-injection defense — 2026 state of the art](prompt-injection-defense-soa-2026-2026-07-17.md)

### Readiness (Phase 0)

- [i18n / multilingual readiness](i18n-multilingual-readiness-2026-07-17.md)
- [Multi-agent graph / swarm orchestration readiness](multi-agent-graph-readiness-2026-07-17.md)

## Relationship to the rest of the KB

- Rationale: [compression manifesto](../vision/compression-thesis.md) and [principles](../vision/principles.md).
- Execution substrate: [tiered memory](../architecture/memory-tiers.md) and the [sleep cycle](../components/datafusion-sleep-cycle.md).
- Metric: the [OKRs](../roadmap/okrs.md) (objective O8).

## Related research

- [Competitive landscape](competitive-landscape.md) — where aisotropy sits vs. LangChain/LangGraph,
  Mastra, Letta, and the Go-native contenders (Eino, ADK Go, Genkit Go), with the memory-window
  time-to-threat judgment (mid-2026 snapshot).
- [Frontend / agent-UI integration landscape](frontend-ui-integration-landscape.md) — what adapter
  aisotropy's service must emit to power in-app copilots (AG-UI/CopilotKit, the Vercel AI SDK "UI
  Message Stream", assistant-ui, A2A), ranked by coverage/effort. Conclusion: adapters over the
  existing SSE stream, not a rewrite (mid-2026 snapshot).
- [Voice pipeline landscape](voice-pipeline-landscape-2026-07-23.md) — can aisotropy speak?
  (Core: no audio, by design.) The STT → `chat/stream` → TTS sandwich with aisotropy as the brain:
  Pipecat/Whisper/Kokoro/Chatterbox/F5-TTS/ElevenLabs/Asterisk, each license-verified against the
  repo gate. Conclusion executed same-day: the OpenAI-compat shim shipped as ADR-0066 and the
  hardware-free WER-gated voice test lane as ADR-0069 (see the report's outcome addendum).
- [MIT vs Apache-2.0 — the project's OWN license](license-mit-vs-apache-2026-08-03.md) — the first
  license review pointing *inward* instead of at dependencies. Verdict: switch to Apache-2.0 before
  publication (patent grant §3, trademark reservation §6, inbound terms §5, a NOTICE channel §4(d))
  against one real loss, GPLv2-only combination. Includes a defect that stands under either license:
  the LICENSE file appends 32.3% non-MIT text, so detectors report "Other", not MIT.
