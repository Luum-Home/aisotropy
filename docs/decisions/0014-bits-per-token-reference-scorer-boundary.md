---
type: ADR
title: "ADR-0014 — Bits-per-token reference scorer: the Go↔subprocess boundary and the staged Python→Rust scorer"
description: The decision for instrumenting Bet 6 (bits-per-token-per-tenant, the compression North Star) — because no serving provider exposes log-probs, bpt is computed by a fixed reference model in an offline batch subprocess (the ADR-0005 pattern), behind a stable Go contract, with a staged scorer: Python (validated spike) now, Rust (candle) later, both interchangeable behind the same boundary.
tags: [decisions, adr, bet-06, compression, bits-per-token, scorer, subprocess, north-star, observability]
timestamp: 2026-07-09T00:00:00Z
---

# ADR-0014 — Bits-per-token reference scorer: the Go↔subprocess boundary and the staged Python→Rust scorer

## Status

✅ **Accepted (2026-07-09).** Operator-approved (staged Python→Rust). Instruments
[Bet 6 — bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md), the North Star /
fitness function of the [compression thesis](../vision/compression-thesis.md) and OKR O8. The
Python-runtime dependency it introduces in Phase 1 is ratified separately in
[ADR-0015](0015-bpt-eval-model-and-python-batch-runtime.md). **Phase 2 (sovereign Rust candle
scorer) delivered (2026-07-10):** `crates/bitrate-scorer` (candle-core/candle-nn, no Python/torch),
NDJSON-contract-identical to the Phase-1 scorer, with golden-vector parity vs the Python scorer within
~3e-5 bits on real distilgpt2 weights — the staged Python→Rust swap is now a binary-path change
(`AISOTROPY_BITRATE_BIN`), Go side unchanged. Component design:
[bitrate bpt scorer](../components/bitrate-bpt-scorer.md).

## Context

Bet 6 defines the objective measure of "the agent learned the user":

```
bpt(tenant, t) = − (1/N) Σ log₂ P(token_i | hydrated_context, memory(tenant, t))
memory_lift    = bpt_without_memory − bpt_with_memory
```

Success = **positive lift** (memory reduces bits) **and a downward trend** over time. The spike
[`spikes/compression/bet06_memory_lift.py`](../../spikes/compression/bet06_memory_lift.py) already
validated it: **+1.469 / +1.731 bits/token** memory lift with `distilgpt2` and `DeepESP/gpt2-spanish`.

**The load-bearing constraint.** No serving provider exposes per-token log-probs. The serving path
([`internal/llm/llm.go`](../../internal/llm/llm.go)) is deliberately narrow: `Client.StreamChat`
yields `Token{Text, Done, Err, *Usage}`, and `Usage` is only `{InputTokens, OutputTokens}`. A stale
note in [`internal/llm/otel.go`](../../internal/llm/otel.go) assumed bpt would be "a future
enrichment on this span once logprobs are available from the provider" — **that assumption is wrong**
and is corrected by this ADR. Consequently:

- bpt **cannot** be a hot-path OTel attribute on the `gen_ai` chat span (there is no logprob to read,
  and the serving model's self-surprisal is not a stable cross-time measure anyway);
- bpt **must** be computed offline/batch by a **fixed, versioned reference model** over a held-out
  sample of a tenant's traffic — precisely the profile [ADR-0005](0005-datafusion-go-rust-boundary.md)
  built the sleep cycle for.

"With memory" vs "without memory" map onto existing code:
[`hydrateSystemPrompt`](../../internal/api/chat_stream.go) returns the bare persona base prompt as the
"without" context, and — called with a **nil working memory** — persona + **consolidated** memory
(facts + semantic) as the "with" context. The scorer reuses this one code path (no parallel eval-mode
hydration, ADR-0002) but excludes the live session buffer: bpt measures *learned/consolidated* memory
(the signal that trends down over time), and the live buffer would still hold the held-out turn being
scored (leakage, see Consequences).

## Goals / Non-goals

**Goals**
- Instrument bpt and memory-lift as a durable, per-tenant time series, off the request path.
- Define the Go↔scorer boundary **once** so the engine behind it can evolve without re-deciding
  (the ADR-0005 playbook: "only the transport changes; the decision isn't redone").
- Preserve tenant isolation (ADR-0011) and anti-magic (ADR-0002); keep the core Go binary and its
  Distroless image free of the scorer's heavy runtime.

**Non-goals**
- Not a hot-path metric (it is batch). Not the sovereign in-process NN core (that is Bet 5, 🔴).
- Not choosing the *final* scorer engine forever — only the boundary and the first two stages.

## Options considered

All three share the ADR-0005 shape — a Go coordinator invoking an **offline batch subprocess** over a
file/stdio contract. The subprocess is a pure, tenant-agnostic surprisal calculator
(`bits(text | context)`); Go owns sampling, with/without-context construction, tenant scoping,
storage, and OTel. The choice is only *what runs inside the subprocess* — and because it is a
subprocess (never in-process, never hot-path), **none of these touch the core static binary or the
Distroless image**, the same reasoning that kept DataFusion's Rust crates outside the core.

### Option A — Python scorer subprocess (reuse the validated spike)
Wrap [`spikes/compression/surprisal.py`](../../spikes/compression/surprisal.py) in an NDJSON CLI.
Ships the *already-validated* number immediately. Cost: introduces Python + `torch` + `transformers`
(~gigabytes of runtime) into the repo/CI/deploy — in tension with the sovereignty thesis, defensible
only because it is offline, optional (env-gated), and temporary.

### Option B — Rust scorer subprocess (candle)
A `crates/bitrate-scorer` crate using **candle** (MIT/Apache) over a pinned safetensors GPT-2-class
model — the Rust port of `surprisal.py`'s surprisal loop. Strongest sovereignty fit, aligns with the
existing `crates/sleep-cycle` precedent, and is the natural on-ramp to Bet 5. Cost: more engineering;
re-derives the spike math in Rust; slower first ship.

### Option C — Defer to Bet 5 (do nothing in-process now) — discarded
Blocks the North Star on the 🔴 convergence bet and inverts the roadmap
([research index](../research/index.md): "Bet 6 first"). The metric must exist *before* Bets 1–4 can
be judged. Rejected.

## Trade-off comparison

| Criterion | A — Python | B — Rust (candle) | C — Defer |
|---|---|---|---|
| Ships the validated metric | **Now** | Later (re-derive in Rust) | Never |
| Sovereignty fit | Weak (torch runtime) | **Strong** (no Python) | Perfect but vacuous |
| Touches core binary / image | No (subprocess) | No (subprocess) | — |
| License | Clean (BSD/Apache; verify model weights) | **Clean** (candle MIT/Apache) | — |
| Determinism/pinning | Solved in spike | Strong (Cargo.lock + weight hash) | — |
| Dev/CI cost | High (torch, ~500 MB weights) | Medium (Rust toolchain already present) | None |
| Bet 5 on-ramp | No | **Yes** | No |

## Recommended decision

**Adopt the staged combination behind one stable Go boundary.** Define the
`internal/bitrate.Runner` interface + NDJSON subprocess contract now; the subprocess is a pure
surprisal calculator. **Phase 1 scorer = Option A** (Python spike wrapped as an `AISOTROPY_BITRATE_BIN`
subprocess, env-gated, offline/optional) to ship the validated metric immediately. **Phase 2 scorer =
Option B** (Rust candle) behind the *identical* contract — swapping the binary path is a non-event
for the Go side, the storage, the endpoint, and the tests (which prove contract-conformance against a
fake `Runner`, model-agnostically). Option C rejected.

The Phase-1 Python dependency is ratified in [ADR-0015](0015-bpt-eval-model-and-python-batch-runtime.md).

## Consequences

**Positive**
- The North Star metric exists and is durable/queryable (`GET /bitrate` + OTel `agent.compression.bpt`
  span events), giving Bets 1–4 a fitness function.
- The boundary is decided once; the Python→Rust swap needs no re-decision (ADR-0005 precedent).
- Corrects the stale "logprobs on the serving span" assumption in `otel.go` and `bet-06`.

**Negative / debt**
- Phase 1 carries a heavy, non-sovereign Python runtime — offline/optional/temporary, but real debt
  until the Rust scorer (Phase 2) lands. Tracked by ADR-0015.
- **Leakage** (scoring text that is itself in the "with" context) is closed by two layers, both
  correctness-critical: (1) a held-out sampler excludes the sample from facts/semantic consolidation,
  and (2) the scorer hydrates with a nil working memory so the sample's live session buffer cannot leak
  in. Locked by `internal/api` `TestHydrateNilWMExcludesLiveBuffer`.

## Open questions

1. **Cross-model comparability.** Any change of reference model resets the series (bpt is only
   comparable within one model+tokenizer). Do we re-baseline historical tenants under a new model, or
   just start a fresh curve? (See ADR-0015's model-pin triple.)
2. **Phase-2 timing.** What triggers the Rust-candle port — a fixed date, or the first sign the Python
   artifact is a real ops burden?
3. **Multi-replica scheduling.** Per-replica coordinators each see only their own traffic; the store
   is durable (shared). Confirm the tenant-partitioned scheduling matches the sleep cycle (ADR-0007).

## References

- [Bet 6 — bits-per-token-per-tenant](../research/bet-06-bits-per-token-metric.md) · [compression thesis](../vision/compression-thesis.md) · [OKR O8](../roadmap/okrs.md)
- [ADR-0005 — Go↔Rust subprocess boundary](0005-datafusion-go-rust-boundary.md) — the precedent this reuses.
- [ADR-0011 — tenant isolation by construction](0011-tenant-scoped-statement-builder.md) · [ADR-0002 — anti-magic](0002-anti-framework-magic-struct-as-contract.md) — invariants preserved.
- [ADR-0015 — eval model + Python batch runtime](0015-bpt-eval-model-and-python-batch-runtime.md) — the dependency ratification.
- Component design: [bitrate bpt scorer](../components/bitrate-bpt-scorer.md).
