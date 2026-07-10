---
type: ADR
title: "ADR-0015 — Pinned evaluation model + Python-torch batch runtime for Bet 6 (Phase 1)"
description: The dependency-policy ratification for ADR-0014 Phase 1 — adding a Python/torch/transformers batch runtime and pinning a versioned, permissively-licensed evaluation model (distilgpt2, Apache-2.0) for the bits-per-token scorer, scoped strictly to the offline/optional subprocess and never the core binary.
tags: [decisions, adr, bet-06, dependency, license, python, torch, eval-model, sovereignty]
timestamp: 2026-07-09T00:00:00Z
---

# ADR-0015 — Pinned evaluation model + Python-torch batch runtime for Bet 6 (Phase 1)

## Status

✅ **Accepted (2026-07-09).** Operator-approved as the Phase-1 dependency for
[ADR-0014](0014-bits-per-token-reference-scorer-boundary.md). This is the dependency-policy gate the
[license policy](../../.claude/rules/cos/license-policy.md) and the sovereignty thesis require before
adding a heavy, non-Go runtime — even an offline/optional one. Explicitly time-boxed: superseded when
the Phase-2 Rust-candle scorer lands.

## Context

[ADR-0014](0014-bits-per-token-reference-scorer-boundary.md) chooses a staged scorer for Bet 6:
Phase 1 wraps the validated Python spike ([`spikes/compression/surprisal.py`](../../spikes/compression/surprisal.py))
as an offline batch subprocess. That adds Python + `torch` + `transformers` + a downloaded model
weight to the repo/CI/deploy surface. Aisotropy's thesis is explicitly *"in-process Go where latency
matters, dedicated Rust services where scale matters, **megabyte binaries not gigabytes of runtime**"*
([overview](../architecture/overview.md), [ADR-0001](0001-go-rust-in-process-first-stack.md)). Adding
a torch runtime crosses that line and therefore needs the same explicit ratification ADR-0005 got —
not a silent `pip install`.

Bet 6 also has a **determinism requirement**: bits/token are only comparable **within one
model+tokenizer**, so the evaluation model must be pinned and stamped on every measurement, or the
time series is meaningless.

## Goals / Non-goals

**Goals**
- Ratify (or bound) the Python/torch batch dependency for Phase 1 only.
- Pin a **permissively-licensed, versioned** evaluation model so bpt is comparable over time.
- Contain the dependency: offline, optional, env-gated, subprocess-only — never imported by Go, never
  in the core binary or the request-serving Distroless image.

**Non-goals**
- Not a permanent adoption of Python in the stack (Phase 2 = Rust candle removes it).
- Not choosing the model architecture forever — only the pinned Phase-1 default.

## Decision

1. **Dependency scope (hard boundary).** `torch` (BSD-3), `transformers` (Apache-2.0), and the model
   weights live **only** in the `spikes/compression/` Python environment invoked as the
   `AISOTROPY_BITRATE_BIN` subprocess. The Go module gains **no** dependency; the core `aisotropyd`
   binary and its Distroless image gain **nothing**. If `AISOTROPY_BITRATE_BIN` is unset, the runtime
   behaves exactly as before (the whole feature is dormant, mirroring `AISOTROPY_SLEEPCYCLE_BIN`).

2. **Pinned evaluation model = `distilgpt2` (Apache-2.0).** Chosen as the shipped default because its
   weight license is **confirmed permissive (Apache-2.0)**, satisfying the
   [license policy](../../.claude/rules/cos/license-policy.md) with no gray area. `DeepESP/gpt2-spanish`
   (the spike's higher-lift default) remains an **opt-in override** via `SURPRISAL_MODEL`, gated on a
   weight-license confirmation before it can become a shipped default. The scorer echoes the resolved
   `model_id` (with revision) on every NDJSON row.

3. **Determinism pinning.** Pin the model as a `{model_id, revision_sha, tokenizer}` triple; run eval
   mode + `no_grad` + CPU + fixed dtype + real BOS/EOS 1-token context (the spike already does this).
   Every stored `Measurement` carries `model_id`; a change of model **starts a new series**, never
   mixes curves. A checked-in golden-vector test (fixed `(text, context)` → expected `bits_per_token`
   within float tolerance) fails CI on accidental drift.

4. **CI cost containment.** Default `go test ./...` uses a **fake `Runner`** (no torch) — the real
   scorer runs only in an opt-in, build-tagged `bitrate_manual_test.go` (the repo's existing pattern:
   `worker_manual_test.go`, `rqlite_manual_test.go`, `gollem_live_test.go`). The spike's n-gram
   fallback backend gives a torch-free smoke path.

## License verification (recorded)

| Component | License | Verdict |
|---|---|---|
| `torch` (CPU) | BSD-3-Clause | ✅ SAFE |
| `transformers` | Apache-2.0 | ✅ SAFE |
| `distilgpt2` weights (shipped default) | Apache-2.0 | ✅ SAFE |
| `DeepESP/gpt2-spanish` weights (opt-in) | **MIT** (verified 2026-07-10, HF model card) | ✅ SAFE — now eligible as default |

## Consequences

**Positive**
- The dependency is explicit, bounded, license-clean, and reversible (unset one env var → gone).
- The pinned Apache-2.0 model keeps bpt comparable over time and inside the license policy.

**Negative / debt**
- Real, if contained: a ~gigabyte Python/torch artifact exists in the repo/CI until the Phase-2 Rust
  scorer replaces it. This ADR is the explicit, time-boxed acknowledgement of that debt.

## Open questions

1. **`DeepESP/gpt2-spanish` weight license — RESOLVED (2026-07-10): MIT** (confirmed on the HF model
   card, https://huggingface.co/DeepESP/gpt2-spanish). It is now license-clean (✅ SAFE) and therefore
   **eligible as the shipped default**. Given the product's Spanish-speaking target users and its higher
   spike lift (+1.731 vs distilgpt2's +1.469), DeepESP is the *better* default — and since no live bpt
   series exists yet (Bet 6 just shipped), switching the pinned model now carries **zero comparability
   cost** (no historical curve to invalidate). **Recommended follow-up:** flip the pinned default from
   `distilgpt2` to `DeepESP/gpt2-spanish` in `spikes/compression/bitrate_scorer.py` **and** the Phase-2
   Rust scorer together, so both implementations pin the same model (golden-vector parity stays
   meaningful). Not done in-place here to avoid diverging from the in-flight Rust port mid-parallel-work.
2. **CI weight caching** — cache the HF weights in CI or bundle a small safetensors to avoid a network
   fetch, once the opt-in integration test is wired into CI.

## References

- [ADR-0014 — bpt scorer boundary + staged scorer](0014-bits-per-token-reference-scorer-boundary.md)
- [ADR-0001 — Go+Rust, in-process-first / megabyte binaries](0001-go-rust-in-process-first-stack.md) · [license policy](../../.claude/rules/cos/license-policy.md)
- [Bet 6](../research/bet-06-bits-per-token-metric.md) · component: [bitrate bpt scorer](../components/bitrate-bpt-scorer.md)
