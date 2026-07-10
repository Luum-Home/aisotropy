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

2. **Pinned evaluation model = `DeepESP/gpt2-spanish` (MIT).** *(Flipped from `distilgpt2` on
   2026-07-10.)* Its weight license is **confirmed MIT** (HF model card), and it is Spanish-fluent for
   the target users with a higher measured lift than `distilgpt2`; switching now carries **zero
   comparability cost** (no live bpt series exists yet). `distilgpt2` (Apache-2.0) remains a supported
   override via `SURPRISAL_MODEL` (Python) / `AISOTROPY_BITRATE_MODEL_ID` (Rust). The scorer echoes the
   resolved `model_id` on every NDJSON row.
   - **Both scorers pin the same model (the swap invariant).** Python (`bitrate_scorer.py`) defaults to
     `DeepESP/gpt2-spanish`; the Phase-2 Rust scorer's `model_id()` is now env-driven (default the
     same) and its `unit` label is derived from the model, not hardcoded.
   - **Sovereign-Rust provisioning caveat.** DeepESP ships `pytorch_model.bin` but **no**
     `model.safetensors`/`tokenizer.json`, which the Rust scorer requires. One-time offline
     conversion: `AutoModelForCausalLM.from_pretrained("DeepESP/gpt2-spanish").save_pretrained(dir,
     safe_serialization=True)` + `AutoTokenizer…save_pretrained(dir)`, then point
     `AISOTROPY_BITRATE_MODEL_DIR` at `dir`. (Python needs no conversion — `transformers` loads
     DeepESP natively.)
   - **Parity re-verified on DeepESP (2026-07-10):** Python vs Rust `bits_per_token` within **≤4.1e-3
     bits** with **identical `n_tokens`** — larger than distilgpt2's ~3e-5 (expected f32 divergence in
     the 12-layer model), still well inside the 1e-2 golden tolerance.

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

1. **`DeepESP/gpt2-spanish` weight license — RESOLVED (2026-07-10): MIT** (HF model card) → ✅ SAFE.
   **Flip DONE (2026-07-10):** both scorers now default to `DeepESP/gpt2-spanish` (Python
   `bitrate_scorer.py` + the env-driven Rust `model_id()`), pinning the same model so golden-vector
   parity stays meaningful (re-verified: Δ ≤4.1e-3 bits, identical `n_tokens`). See Decision §2 for the
   sovereign-Rust safetensors/tokenizer conversion recipe. `distilgpt2` remains a supported override.
2. **CI weight caching** — cache the HF weights in CI or bundle a small safetensors to avoid a network
   fetch, once the opt-in integration test is wired into CI.

## References

- [ADR-0014 — bpt scorer boundary + staged scorer](0014-bits-per-token-reference-scorer-boundary.md)
- [ADR-0001 — Go+Rust, in-process-first / megabyte binaries](0001-go-rust-in-process-first-stack.md) · [license policy](../../.claude/rules/cos/license-policy.md)
- [Bet 6](../research/bet-06-bits-per-token-metric.md) · component: [bitrate bpt scorer](../components/bitrate-bpt-scorer.md)
