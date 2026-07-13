---
type: Research Report
title: "Bet 5 sovereign NN core — Go inference path + sovereign-weights licensing (deep-research)"
description: Deep-research output (fan-out web search + 3-vote adversarial verification, 27 primary sources) on (Q4) the license-clean Go-native low-latency NN inference landscape for going from the candle subprocess to in-process embedding/logprob, and (Q2) the MIT/Apache-clean path to sovereign weights — dodging the copyleft trap of Bellard's reference compressors. Feeds ADR-0023's parked in-process-µs phase.
tags: [research, bet-5, sovereign-nn-core, inference, licensing, go, candle, onnx, tract, llama-cpp, nncp, deep-research]
timestamp: 2026-07-13T00:00:00Z
---

# Bet 5 sovereign NN core — inference path + sovereign weights

Deep-research run (2026-07-13): 6 search angles → 27 sources fetched → 109 claims →
25 adversarially verified (3-vote, kill on 2/3 refute) → 24 confirmed, 1 refuted.
Every license claim rests on a **primary source** (repo LICENSE file, model card,
author readme), not blog hearsay. This report feeds [ADR-0023](../../decisions/0023-sovereign-local-embedder-semantic-tier.md)'s
explicitly-parked in-process-µs phase (its Options B/C).

## Q4 — Go-native low-latency inference: all four paths are license-clean

Every option stays inside the 100% MIT/Apache box ([ADR-0001](../../decisions/0001-go-rust-in-process-first-stack.md),
the sovereign-stack decision). The differentiator is **reuse + maturity + cgo cost**, not license.

| Path | Binding license | Native lib license | cgo? | Verdict |
|---|---|---|---|---|
| **(a) cgo → Rust candle** | candle dual **Apache-2.0/MIT**; vLLM `candle_binding` **Apache-2.0** (Cargo `MIT OR Apache-2.0`) | candle-core/nn/transformers, tokenizers 0.21, safetensors, hf-hub — all MIT/Apache | yes (C-FFI to `libcandle_*.dylib`) | ✅ **clean; proven reference** |
| **(b1) ONNX — yalue/onnxruntime_go** | **MIT** (Nathan Otterness) | Microsoft ONNX Runtime **MIT** (core + CPU EP; CUDA/TensorRT EPs are separate NVIDIA licenses) | yes | ✅ clean |
| **(b2) pure-Rust tract (Sonos)** | dual **Apache-2.0/MIT**, self-contained (no external C++ runtime) | — (85% Rust; loads ONNX/NNEF/TFLite) | via cgo or subprocess | ✅ clean — audit one "Unknown" vendored-subcrate license entry before adoption |
| **(c) llama.cpp/ggml GGUF** | go-skynet/go-llama.cpp **MIT** (stale); **godeps/gollama MIT** (active fork) | llama.cpp/ggml **MIT** ("The ggml authors", 2023–2026) | yes + **C++ compiler** | ✅ clean; overkill for embeddings; audit `vendor/` third-party |
| **(d) persistent Rust daemon** | inherits candle/tract clean licenses | same | **NO cgo** | ✅ clean; cgo-free middle path |

**Ranked recommendation for the next brick** (synthesis judgment, `medium` confidence):

1. **(a) cgo → Rust candle** — strongest license+maturity+reuse fit: candle *already*
   runs in aisotropy's baseline `embed()` (same crate, same weights), and vLLM's
   Apache-2.0 `candle_binding` is a working, buildable reference of exactly the
   Go-cgo→candle path. No second native toolchain.
2. **(d) persistent Rust daemon** — the pragmatic middle path if cgo build/cross-compile
   cost is unacceptable: keep the candle process warm, amortize model load, talk over a
   local socket. Sidesteps cgo entirely, inherits candle's clean license, and is the
   smallest delta from today's subprocess (it *is* today's subprocess, kept alive).
3. ONNX (b) / llama.cpp (c) — clean but no reuse advantage over candle and add a second
   engine/toolchain.

> ⚠️ **CRITICAL GAP — no latency numbers survived verification.** The entire µs-vs-ms
> premise of Q4 is **unquantified** by this research. The ranking rests on license +
> maturity + reuse, *not* measured speed. Whether cgo-candle (or a warm daemon) actually
> reaches the µs target vs. the current ms subprocess is an **empirical benchmark**, not a
> literature question — it must be measured on arm64-mac + linux-x86 before the path is
> locked. Do not claim "µs" until benchmarked.

## Q2 — sovereign weights: one assumption corrected, one trap confirmed

**The brief's `nncp-is-GPL` assumption is WRONG (corrected against primary source).**
Bellard's own `nncp/readme.txt`: *"The source code is released under the MIT licence."*
BUT the same readme: NNCP depends on **LibNC**, *"provided in binary form"* — its source
is *"Contact the author if access to its source code is required."* So NNCP source is MIT
yet the **full stack is not buildable/modifiable from source** — redistribution/patterns
only, not modifiable-code adoption. Not adoptable, but for a **binary-dependency** reason,
not a copyleft one. (Lesson: brief assumptions can be wrong — verify every license.)

**The real copyleft trap, confirmed:**
- `cmix` — **GPL-3.0** (GitHub License API: `{key:gpl-3.0}`) → patterns-only.
- `lstm-compress` — **GPL-3.0** (COPYING = GPLv3; reuses cmix code) → patterns-only.
- `ts_zip` — **no license declared** at bellard.org/ts_zip/ (binary tarballs only) →
  status unverifiable from its page; treat as non-adoptable until a bundled LICENSE is found.

**Target model licenses — found MIT at primary source, but NOT triple-verified.** The
extraction pass quoted MIT directly from each model card, but these claims did not pass
through the 3-vote adversarial gate (only 25 of 109 claims did), so they are
**single-pass-primary, not adversarially confirmed** — a middle tier, *not* "unknown":
- `BAAI/bge-small-en-v1.5` — card: FlagEmbedding **MIT**, *"can be used for commercial purposes free of charge."*
- `intfloat/multilingual-e5-small` — card YAML `license: "mit"`; lineage from Microsoft
  MiniLM + XLM-RoBERTa, both MIT → no copyleft in the weight lineage; 109 languages incl. ES+EN.
- `DeepESP/gpt2-spanish` — card: **License: MIT**; GPT-2-small scale (~124M) → consistent
  with the ~1.9 bits/char plateau (< 1.0 needs nncp-scale).

> Given the NNCP surprise, **re-confirm each model's LICENSE directly before pinning in
> production** — the quotes are direct but were not put through the adversarial gate.

**From-scratch training — license-clean building blocks exist (a costed plan is possible):**
- **FineWeb2** (HuggingFaceFW) — **ODC-By 1.0**, per-language reproducible web corpus (~20TB,
  Spanish included), ablation-validated recipe. Note: ODC-By is a *data* license (attribution),
  not MIT — clean to use, but a different obligation class than code.
- The "two trillion tokens open" permissive corpus + `sentence-transformers` training docs +
  `all-MiniLM-L6-v2` (Apache-2.0) as a recipe reference give an end-to-end small-embedder path.
- Smallest-useful-scale and GPU cost were **not** quantified — the next step to make weight
  sovereignty "a costed plan, not a vibe."

## Open questions (carried forward)

1. **Measured latencies** — single 384-d embedding + one short-prompt logprob, CPU, arm64-mac
   + linux-x86, across candle-cgo / warm-daemon / ONNX / tract. The performance dimension that
   would firm up the option-(a) recommendation. **Benchmark, not research.**
2. **Re-confirm** bge / e5 / DeepESP LICENSE files directly (single-pass-primary → gate them).
3. **ts_zip** actual license (bundled LICENSE in the tarball, or inherited from LZMA/nncp).
4. **Smallest-useful-scale + GPU cost** for training a sovereign ES+EN embedder from FineWeb2.

## One refuted claim (kept for honesty)

*"go-llama.cpp pulls llama.cpp as upstream Apache-2.0"* — **refuted 0-3**: llama.cpp is **MIT**,
not Apache. (Doesn't change the verdict — still inside the box — but the license label was wrong.)

## Sources (primary)

candle `github.com/huggingface/candle` · vLLM `candle_binding` (`pkg.go.dev/.../semantic-router/candle-binding`) ·
`github.com/yalue/onnxruntime_go` · `github.com/microsoft/onnxruntime/blob/main/LICENSE` ·
`github.com/sonos/tract` · `github.com/ggml-org/llama.cpp/blob/master/LICENSE` · `github.com/godeps/gollama` ·
`bellard.org/nncp/readme.txt` · `bellard.org/libnc/libnc.html` · `github.com/byronknoll/cmix` ·
`github.com/byronknoll/lstm-compress` · `bellard.org/ts_zip/` · `huggingface.co/BAAI/bge-small-en-v1.5` ·
`huggingface.co/intfloat/multilingual-e5-small` · `huggingface.co/DeepESP/gpt2-spanish` ·
`huggingface.co/datasets/HuggingFaceFW/fineweb-2` · `sbert.net/docs/sentence_transformer/training_overview.html` ·
`huggingface.co/sentence-transformers/all-MiniLM-L6-v2` · `arxiv.org/pdf/2402.05672` (mE5 paper).
