---
type: ADR
title: "ADR-0023 — Sovereign local embedder for the semantic tier (Bet 5, first brick)"
description: The semantic tier's embed() must be local and MIT/Apache — not the BYO external /embeddings client (ADR-0018), which sends tenant text off-machine. Decision — a Rust candle crate (crates/embed) that loads a pinned MIT BERT-class sentence model (default BAAI/bge-small-en-v1.5), mean-pools + L2-normalizes, and speaks the same NDJSON subprocess contract as bitrate-scorer/sleep-cycle (ADR-0005), behind a new env-gated semantic.LocalEmbedder implementing the existing Embedder interface. This is Bet 5's first brick — embed() only — not the full in-process-µs NN core.
tags: [decisions, adr, bet-05, sovereignty, embeddings, semantic, candle, subprocess, in-process]
timestamp: 2026-07-13T00:00:00Z
---

# ADR-0023 — Sovereign local embedder for the semantic tier (Bet 5, first brick)

## Status

✅ **Accepted — implemented and wired (status updated 2026-07-14).**
🔁 **Default model superseded 2026-07-17 by [ADR-0049](0049-multilingual-embedder.md):** the pinned
default is now **`intfloat/multilingual-e5-small`** (MIT, 384-dim, 100+ languages), a
*dimension-preserving* drop-in for `BAAI/bge-small-en-v1.5` — existing Qdrant collections stay
compatible (no re-index of vector width). This closes the cross-lingual RAG gap this ADR itself
flagged as deferred debt #4 (e5 `query:`/`passage:` prefixes are now supported per-request by the
crate). The subprocess/daemon boundary, the Go seam, and the `Embedder` interface are unchanged.
Proposed 2026-07-13; the first
brick (`crates/embed` + `semantic.LocalEmbedder`) and the `EmbedderFromEnv` wiring both landed
2026-07-13 alongside the warm-daemon path (`DaemonEmbedder`, selected by default under
`AISOTROPY_EMBED_BIN` + `AISOTROPY_EMBED_MODEL_DIR`; verified live via
`TestDaemonEmbedderLiveWiring`, `//go:build manual` — see "The Go seam" below). Still dormant by
default: without those env vars the runtime falls back to the API/hash embedders.
First brick of [Bet 5 — Sovereign NN core](../research/bet-05-sovereign-nn-core.md),
which [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md) promoted to the next-invest
substrate: the compression round measured that small-LM subword surprisal is a *lexical*, not
*semantic*, signal, so the viable path for the semantic bets (1/4) runs through **embedding-density /
semantic distance** — and the embedding-density probe separated Bet-1 novelty at **AUC 0.89**. Bet 5's
own "Next experiment" is explicit: *"Prototype only `embed()` in-process… If the numbers work out,
extend to `logprob()`."* This ADR delivers that first prototype. It does **not** claim the full
in-process-µs NN core; see Non-goals.

## Context

The semantic tier (`internal/semantic`) turns text into vectors through the `Embedder` interface:

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimension() int
}
```

Today there are exactly two implementations, and **neither is a sovereign local model**:

1. **`HashEmbedder`** (`embed.go`) — a deterministic feature-hashing placeholder. Captures lexical
   overlap only; synonyms and paraphrase do not match. It exists to run/test the vector path with zero
   dependencies, not to be good.
2. **`APIEmbedder`** (`api_embedder.go`, ADR-0018) — a BYO OpenAI-compatible `POST {base}/embeddings`
   client, selected by `AISOTROPY_EMBED_URL`. It *can* point at a local server (Ollama, TEI, llama.cpp),
   but the default and documented story sends **tenant text over the network** to an external endpoint.

The BYO client is the correct *interop* surface, but it contradicts the [sovereignty thesis](../vision/thesis.md#sovereignty)
and [ADR-0001](0001-go-rust-in-process-first-stack.md): the hot-path cognitive plumbing should run
locally, in-process where latency matters, with **zero network** and a 100% MIT/Apache stack. The
missing piece is a **sovereign local embedder**: real semantic vectors, no credentials, no egress.

The project already has a proven pattern for exactly this shape of work — a pinned MIT/Apache model run
by Rust candle as an offline subprocess behind a stable Go contract:

- [ADR-0005](0005-datafusion-go-rust-boundary.md): the Rust↔Go boundary is a **separate worker
  invoked as a subprocess** (NDJSON / files), never CGO/FFI/WASM. A Rust panic is a Go-observable
  non-zero exit with retry/backoff.
- `crates/sleep-cycle` (DataFusion dedup) and `crates/bitrate-scorer` (a candle GPT-2 surprisal scorer,
  [ADR-0014](0014-bits-per-token-reference-scorer-boundary.md)) both live on that boundary, with Go
  seams (`internal/sleepcycle`, `internal/bitrate`) that exec them, env-gated (`AISOTROPY_SLEEPCYCLE_BIN`,
  `AISOTROPY_BITRATE_BIN`) and dormant by default.

So the decision is *not* "how do we do sovereign inference from nothing" — it is "which of the on-hand
paths do we reuse for `embed()`."

## Goals

- A **local, MIT/Apache** embedder producing real (mean-pooled, L2-normalized) sentence embeddings with
  **zero network calls** — tenant text never leaves the process tree.
- Slot **under the existing `semantic.Embedder` interface** with no interface change and no disruption to
  `HashEmbedder`/`APIEmbedder` — a drop-in third implementation, env-gated and dormant by default.
- Reuse the established **ADR-0005 subprocess boundary** and mirror `crates/bitrate-scorer` so the Go
  seam is a near-copy of `internal/bitrate/worker.go`.

## Non-goals (honest scope — this is a brick, not the wall)

- **NOT** the in-process-µs NN core. This first brick runs the model as a **subprocess** (ms-scale
  process spawn + inference), not in-process µs. Closing the subprocess→in-process-µs gap (cgo candle, a
  long-lived embedder daemon, or an embedded runtime à la Bellard's libnc) is deferred to a later Bet-5
  phase — exactly as ADR-0014/`bitrate-scorer` staged Python→Rust before optimizing. **The path options
  for that phase are now researched** ([bet5 sovereign-nn-core report](../06-Daily/reports/bet5-sovereign-nn-core-inference-and-weights-2026-07-13.md),
  2026-07-13, 27 primary sources, adversarially verified): all four candidates (cgo→candle, ONNX
  `onnxruntime_go`, pure-Rust `tract`, llama.cpp/ggml) are license-clean; **cgo→candle ranks first**
  (reuses this ADR's exact crate/weights; vLLM's Apache-2.0 `candle_binding` is a working reference),
  with a **cgo-free long-lived candle daemon** as the pragmatic middle path. Caveat: **no latency numbers
  survived verification — the µs target must be benchmarked, not assumed**. The report also corrected a
  standing assumption: Bellard's NNCP is **MIT** (not GPL) but ships a **binary-only** `libnc`, so it is
  redistributable-patterns-only, not modifiable-from-source.
- **NOT** `logprob()` or `generate()`. Bet 5 is incremental: embeddings first (this ADR), then logprob,
  then generation.
- **NOT** replacing Qdrant/Gollem/the BYO client. `APIEmbedder` stays for callers who want a hosted model;
  this adds the sovereign option, it doesn't remove the interop one.

## Options considered

### Option A — Rust candle crate as a subprocess (CHOSEN)

A new `crates/embed` (candle-core + candle-nn + candle-transformers' maintained BERT + tokenizers) loads a
pinned MIT sentence model from local safetensors, mean-pools + L2-normalizes, and emits vectors over the
same NDJSON/stdio contract as `bitrate-scorer`. A new `semantic.LocalEmbedder` execs it, env-gated by
`AISOTROPY_EMBED_BIN` + `AISOTROPY_EMBED_MODEL_DIR`.

- **+** On-precedent: identical boundary to sleep-cycle/bitrate-scorer (ADR-0005); the Go seam is a
  near-copy of `internal/bitrate`. candle is dual MIT/Apache, already vendored and building offline in
  this repo.
- **+** Sovereign by construction: no network, weights are a local artifact.
- **+** Panic isolation for free (subprocess → non-zero exit → Go retry/backoff), per ADR-0005.
- **−** Subprocess spawn + batch I/O is **ms, not µs** — an intentional first-brick compromise, not the
  Bet-5 end state. **Now measured** (`cmd/embed-bench`, 2026-07-13, e5-small on arm64-mac CPU, MIT
  weights): the current per-embedding cost is **p50 423.6 ms** — and it is **~95% process-spawn +
  model-load, not inference**. A `--serve` warm daemon (same crate, model loaded once) drops it to
  **p50 22.5 ms (min 17 ms), an 18.8× speedup**, with a one-time 306 ms cold start. **Two decisions fall
  out:** (1) the next brick is the **cgo-free warm daemon** (path (d) in the [research report](../06-Daily/reports/bet5-sovereign-nn-core-inference-and-weights-2026-07-13.md)) — it captures essentially the whole win; cgo→candle would only shave the local-pipe IPC (µs) on top of a 22 ms inference floor, a marginal further gain. (2) **The "µs" target in the Bet-5 vision is not physically reachable for a BERT-class embedder on CPU** — the inference floor itself is ~22 ms; µs would require a much smaller/quantized model or a different op, not a different transport. The honest in-process target is **~20 ms warm, zero spawn/load tax**, not µs.

### Option B — Go-native ONNX runtime (onnxruntime_go / cgo)

Embed the model in-process via an ONNX runtime bound to Go.

- **+** True in-process (closer to the eventual µs goal); no subprocess.
- **−** Pulls a **cgo** dependency and a native `onnxruntime` shared library into the Go core — against
  ADR-0001's "in-process **Go** where latency matters, dedicated **Rust** where scale matters" and
  ADR-0005's deliberate avoidance of FFI at the Rust boundary. It also fractures the "one way we run
  models" story (candle everywhere else).
- **−** License/supply-chain surface of the ORT binary + Go bindings must be vetted separately; heavier
  than reusing the candle stack already in-tree.
- Verdict: **the right shape for a *later* in-process phase**, but it front-loads cgo/native-lib risk for
  a first brick whose job is to prove the embedding is sovereign and correct, not fast.

### Option C — cgo candle (in-process, same model stack)

Link candle into the Go core via cgo instead of a subprocess.

- **+** Reuses the candle model code; in-process.
- **−** cgo across the Go↔Rust line is exactly what ADR-0005 rejected (build complexity, a Rust panic
  becoming an in-process crash of the Go core rather than an observable exit code). Premature for a brick.
- Verdict: deferred; revisit when optimizing the subprocess→µs gap, and only with an explicit ADR-0005
  amendment.

## Decision

**Adopt Option A.** Ship `crates/embed`, a Rust candle sentence-embedder on the ADR-0005 subprocess
boundary, and a new env-gated `semantic.LocalEmbedder` behind the unchanged `Embedder` interface.

### Model + license

Default (as of [ADR-0049](0049-multilingual-embedder.md), 2026-07-17) **`intfloat/multilingual-e5-small`**
— a BERT-class (384-dim) sentence model under the **MIT** license, covering 100+ languages. It replaced
the original **`BAAI/bge-small-en-v1.5`** (also MIT, 384-dim, English-only) default; because both are
384-dim the change is a dimension-preserving directory swap. The crate is architecture-agnostic
(config.json-driven, like `bitrate-scorer`), so either encoder — or any other BERT-class model — is a
directory swap. Mean/average pooling (what the crate already does) is the *canonical* pooling for the e5
family, so the swap also aligns the pooling recipe with the weights. The model id/rev are env-overridable
(`AISOTROPY_EMBED_MODEL_ID`/`_REV`); nothing is hardcoded.

License table (all permissive — satisfies the ADR-0001 / license-policy 100% MIT/Apache gate):

| Component | License |
|---|---|
| `BAAI/bge-small-en-v1.5` (default weights) | MIT |
| `intfloat/multilingual-e5-small` (multilingual pin) | MIT |
| candle-core / candle-nn / candle-transformers | dual MIT / Apache-2.0 |
| tokenizers | Apache-2.0 |
| serde / serde_json | MIT / Apache-2.0 |

> **candle-transformers vs hand-rolled.** `bitrate-scorer` hand-rolled GPT-2 because candle-transformers
> shipped *no* GPT-2 model. It *does* ship a maintained BERT encoder (`models::bert`), so per
> [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) anti-magic — *reuse the maintained
> implementation rather than re-derive attention we can't fully verify offline* — the embed crate depends
> on it instead of re-implementing BERT. This is the one structural difference from `bitrate-scorer`.

### The Go seam (mirrors `internal/bitrate`)

A new `semantic.LocalEmbedder` implements `Embedder` by writing an NDJSON request file, exec'ing the
`embed` binary (`--input`/`--output`, ctx-cancellable via `exec.CommandContext`, retry/backoff on
non-zero exit exactly like `bitrate.Worker`), and reading back the vectors. It is selected only when both
`AISOTROPY_EMBED_BIN` (the built binary) and `AISOTROPY_EMBED_MODEL_DIR` (the weights) are set — dormant
by default, like `AISOTROPY_BITRATE_BIN`/`AISOTROPY_SLEEPCYCLE_BIN`. **`EmbedderFromEnv` now selects it**
(the wiring, deferred at first-brick time, landed 2026-07-13 alongside the warm-daemon path): the
sovereign local embedder outranks the BYO API and the hash placeholder when its env is set.

Selection order now installed in `EmbedderFromEnv`:

```
AISOTROPY_EMBED_BIN + AISOTROPY_EMBED_MODEL_DIR set  -> local sovereign embedder (no network)     [NEW]
    AISOTROPY_EMBED_MODE = "" | "daemon"  -> DaemonEmbedder   (one warm process, ~18.8× faster)
    AISOTROPY_EMBED_MODE = "subprocess"   -> LocalEmbedder    (fresh process per call, panic-isolated)
AISOTROPY_EMBED_URL set                              -> APIEmbedder  (BYO /embeddings, ADR-0018)
otherwise                                            -> HashEmbedder (deterministic placeholder)
```

The daemon (`internal/semantic/daemon_embedder.go`) starts the binary once in `--serve` mode, serializes
calls over its stdin/stdout pipe under a mutex, and tears down + respawns on any I/O error or ctx
cancellation so a shared pipe is never left half-drained. Throughput is bounded by one daemon; a daemon
**pool** is the documented next follow-up for high concurrency. Verified live: `EmbedderFromEnv` selects
`DaemonEmbedder`, real e5-small embeddings with cos(similar)=0.897 > cos(dissimilar)=0.819
(`TestDaemonEmbedderLiveWiring`, `//go:build manual`).

### Contract (NDJSON/stdio — identical shape to `bitrate-scorer`)

```
argv:   --input <requests.ndjson> --output <results.ndjson>
input:  {"id":"...","text":"..."}
output: {"id":"...","embedding":[F,...],"dim":N,"model_id":"<model>@<rev>","normalized":true}
stdout: {"embedded":N,"model_id":"...","dim":N,"backend":"BERT_REAL"}   (final line)
exit:   0 = ok; non-zero = failure (reason on stderr)
model:  AISOTROPY_EMBED_MODEL_DIR -> {config.json, model.safetensors, tokenizer.json}
```

The `Dimension()` the Go side reports must equal the model's `hidden_size` (384 for bge-small /
e5-small) and the semantic `Store`'s configured dimension, or `Upsert`/`Search` fail with
`ErrDimensionMismatch` — the same constraint `AISOTROPY_EMBED_DIM` documents for `APIEmbedder`.

## Consequences

- **Sovereignty delivered for `embed()`:** the semantic tier can now vectorize tenant text with a local
  MIT model and no egress — the first concrete Bet-5 capability, on the axis (embedding density) that
  measurement (AUC 0.89) justified investing in.
- **No interface churn, no default change:** `Embedder` is unchanged; `HashEmbedder`/`APIEmbedder` and the
  default (hash) path are untouched. The new path is opt-in via two env vars and dormant otherwise, so
  `go build`/tests are unaffected.
- **Consistent operational model:** one more crate on the ADR-0005 boundary; ops reason about
  `AISOTROPY_EMBED_BIN` exactly as they do `AISOTROPY_BITRATE_BIN`. Panic → non-zero exit → retry/backoff
  is inherited.
- **Known debt (explicitly deferred):** (1) subprocess spawn is ms, not the Bet-5 µs target — a later
  phase closes the gap (daemon / cgo / embedded runtime) with its own ADR; (2) **weight provenance is
  not fully sovereign** — we run MIT-licensed weights we did not train (Bellard's libnc end state is a
  from-scratch model; borrowing weights is ADR-0005-style "borrowed local models" incrementalism); (3)
  the model must be fetched once out-of-band into `AISOTROPY_EMBED_MODEL_DIR` (no in-repo weights), the
  same operator step as `bitrate-scorer`; (4) e5 models expect `query:`/`passage:` prefixes for best
  retrieval — that belongs in the Go seam/caller, not the pure crate, and is a follow-up.

## References

- [Bet 5 — Sovereign NN core](../research/bet-05-sovereign-nn-core.md) · [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md) (why embedding-density is the next-invest axis)
- [ADR-0005](0005-datafusion-go-rust-boundary.md) (the subprocess boundary) · [ADR-0014](0014-bits-per-token-reference-scorer-boundary.md) / `crates/bitrate-scorer` (the precedent this mirrors)
- [ADR-0018](0018-knowledge-ingestion-surface.md) (the BYO embedder this complements) · [ADR-0001](0001-go-rust-in-process-first-stack.md) / [ADR-0002](0002-anti-framework-magic-struct-as-contract.md)
- candle — <https://github.com/huggingface/candle> · bge-small-en-v1.5 (MIT) · multilingual-e5-small (MIT)
