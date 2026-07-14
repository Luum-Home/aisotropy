---
type: Component
title: "Embedder — the semantic-tier embedding stack"
description: How the semantic tier turns text into vectors — the env-driven selection ladder (sovereign local candle daemon > BYO /embeddings API > deterministic hash placeholder), the warm-daemon process model and its teardown-on-fault invariant, and the BYO embedder seam.
tags: [components, semantic, embedder, bet-05, sovereignty, candle, subprocess]
timestamp: 2026-07-14T00:00:00Z
---

# Embedder — the semantic-tier embedding stack

Everything that writes to or reads from the [semantic tier](qdrant-semantic-memory.md) —
turn consolidation, fact dual-indexing ([engram facade](engram-facts-journal.md)), knowledge
ingestion (ADR-0018), RAG hydration — goes through one `semantic.Embedder` interface. This
document covers how that embedder is selected and what each implementation actually does.
Status (2026-07-14): **REAL** — all three implementations exist and are tested; which one is
active depends entirely on environment configuration (the sovereign local path is dormant
until its binary is built and configured).

## Selection ladder (`EmbedderFromEnv`)

[`internal/semantic/embedder_env.go`](../../internal/semantic/embedder_env.go) `EmbedderFromEnv()`
mirrors `llm.FromEnv`'s provider-selection pattern: a real backend when configured via env, a
credential-less deterministic fallback otherwise. Precedence, first match wins:

1. **`AISOTROPY_EMBED_BIN` set → the sovereign LOCAL embedder** (Bet 5,
   [ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md)): a Rust candle
   subprocess ([`crates/embed`](../../crates/embed)) that keeps tenant text on-machine (zero
   network). This is the thesis default and **outranks the API path when both are configured**.
   `AISOTROPY_EMBED_MODE` selects the form: unset or `"daemon"` → `DaemonEmbedder` (one warm
   process); `"subprocess"` → `LocalEmbedder` (a fresh process per call, panic-isolated).
   Requires `AISOTROPY_EMBED_MODEL_DIR` and `AISOTROPY_EMBED_DIM`; a config error logs a
   warning and **falls through** to the next rung rather than failing closed.
2. **`AISOTROPY_EMBED_URL` set → `APIEmbedder`** ([`internal/semantic/api_embedder.go`](../../internal/semantic/api_embedder.go)):
   the BYO-endpoint path speaking the OpenAI-compatible `POST {base}/embeddings` protocol —
   OpenAI, OpenRouter, or a fully local server (Ollama, text-embeddings-inference, llama.cpp).
3. **Neither → `NewHashEmbedder(0)`** ([`internal/semantic/embed.go`](../../internal/semantic/embed.go)):
   the deterministic, credential-less placeholder that wires the ingestion/retrieval path
   without a real backend. It wires and tests the path but does not rank semantically.

One level above, the public builder's `ensureSemantic`
([`aisotropy/knowledge.go`](../../aisotropy/knowledge.go)) adds the BYO seam from
[ADR-0018](../decisions/0018-knowledge-ingestion-surface.md): an explicit **`WithEmbedder`
option always wins** over `EmbedderFromEnv()`. Full precedence:
`WithEmbedder > local candle (BIN) > APIEmbedder (URL) > HashEmbedder`. The store and embedder
are built exactly once per `App` and shared by the chat routes' RAG hydration, the
consolidation sink, and the knowledge-ingestion surfaces — never two independently evolving
pairs. (Store selection — in-process cosine vs. networked Qdrant via `QDRANT_HOST` — is a
separate axis, [`internal/semantic/factory.go`](../../internal/semantic/factory.go).)

## The warm daemon (`DaemonEmbedder`)

[`internal/semantic/daemon_embedder.go`](../../internal/semantic/daemon_embedder.go) is the
default form of the sovereign local embedder. Where `LocalEmbedder` spawns a fresh candle
subprocess per `Embed` call — paying process spawn + model load every time — `NewDaemonEmbedder`
starts the same `embed` binary **once** in `--serve` mode (model loaded once, lazily on the
first `Embed`, not at wiring time) and exchanges NDJSON requests/responses over its
stdin/stdout pipe.

**Measured latency** ([ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md),
`cmd/embed-bench`, 2026-07-13, e5-small on arm64-mac CPU): **p50 22.5 ms/embedding warm**
(min 17 ms) vs. 423.6 ms for the per-call subprocess — an **18.8× win**, with a one-time
~306 ms cold start. ~95% of the per-call cost is spawn + model load, which the warm process
pays only once.

**Teardown-on-fault invariant** (the load-bearing one): any I/O error, a daemon crash, or a
ctx cancellation mid-call **tears down the process and clears it**, so the next `Embed`
respawns a fresh daemon. A shared pipe cannot be left half-drained — if a cancelled call
abandoned an in-flight response, the *next* call would read that stale row and every
subsequent result would be shifted by one. Killing on any fault guarantees the pipe is always
in a known state (empty + fresh) at the start of every successful exchange; the respawn cost
(~one cold start) is paid only on the rare fault, not per call. Shutdown is clean by
construction: closing stdin makes the Rust serve loop read EOF and exit.

**Concurrency**: one daemon owns one pipe and the Rust serve loop is single-threaded, so
`Embed` calls are serialized under a mutex. Throughput is bounded by a single daemon; a daemon
*pool* is the documented follow-up for high-concurrency deployments.

## Code map

| Path | What it is |
|---|---|
| [`internal/semantic/embedder_env.go`](../../internal/semantic/embedder_env.go) | `EmbedderFromEnv()` selection ladder + env parsing |
| [`internal/semantic/daemon_embedder.go`](../../internal/semantic/daemon_embedder.go) | `DaemonEmbedder` — warm `--serve` process, NDJSON over stdin/stdout |
| [`internal/semantic/local_embedder.go`](../../internal/semantic/local_embedder.go) | `LocalEmbedder` — fresh subprocess per call, panic-isolated (ADR-0005 boundary) |
| [`internal/semantic/api_embedder.go`](../../internal/semantic/api_embedder.go) | `APIEmbedder` — OpenAI-compatible `POST /embeddings` |
| [`internal/semantic/embed.go`](../../internal/semantic/embed.go) | `Embedder` interface + `HashEmbedder` placeholder |
| [`crates/embed`](../../crates/embed) | The Rust candle binary (batch + `--serve` modes) |
| [`aisotropy/knowledge.go`](../../aisotropy/knowledge.go) | `ensureSemantic` — `WithEmbedder` seam, one store/embedder pair per App |

## Environment variables

| Variable | Effect |
|---|---|
| `AISOTROPY_EMBED_BIN` | Path to the built `crates/embed` binary; opts into the sovereign local embedder (highest-precedence env rung) |
| `AISOTROPY_EMBED_MODE` | `""`/`"daemon"` → `DaemonEmbedder` (default); `"subprocess"` → `LocalEmbedder` |
| `AISOTROPY_EMBED_MODEL_DIR` | Pinned model dir (`config.json` + `model.safetensors` + `tokenizer.json`); required with `AISOTROPY_EMBED_BIN` |
| `AISOTROPY_EMBED_URL` | OpenAI-compatible API base URL; opts into `APIEmbedder` (when no local embedder is active) |
| `AISOTROPY_EMBED_MODEL` | API embedding model id; defaults to `text-embedding-3-small` when unset |
| `AISOTROPY_EMBED_API_KEY` | Optional bearer token — most local servers need none; never logged |
| `AISOTROPY_EMBED_DIM` | Embedding vector width; MUST match both the model's hidden size and the semantic Store's dimension (`ErrDimensionMismatch` otherwise). Unset/invalid falls back to 128 with a warning |

## Gotchas

- **The dimension is configured, not inferred.** Most embedding APIs do not report their
  dimension in the response, and the Store and Embedder must not silently drift — set
  `AISOTROPY_EMBED_DIM` explicitly for whichever model is active.
- **Local-embedder misconfiguration degrades, it does not block.** `AISOTROPY_EMBED_BIN`
  without `AISOTROPY_EMBED_MODEL_DIR` (or a constructor error) logs a warning and falls
  through to the API/Hash path — check the `semantic embedder selected` startup log line to
  confirm which implementation actually won.
- **HashEmbedder ranks nothing.** Retrieval quality is only meaningful with a real embedder;
  the placeholder exists so the path is buildable and testable with zero credentials
  (ADR-0018).

## Related decisions

- [ADR-0023](../decisions/0023-sovereign-local-embedder-semantic-tier.md) — sovereign local
  embedder (Bet 5): candle subprocess + warm daemon, the measured 18.8× latency win.
- [ADR-0018](../decisions/0018-knowledge-ingestion-surface.md) — BYO embedder seam
  (`WithEmbedder`), `HashEmbedder` as the dev/test default.
- [ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md) — the Go↔Rust subprocess
  boundary pattern the local embedder reuses.

## References

- [Qdrant — Semantic Memory](qdrant-semantic-memory.md) — the store these vectors land in.
- [Engram facade](engram-facts-journal.md) — Fact-kind dual-indexing through this embedder.
- [Consolidation outbox](consolidation-outbox.md) — Turn-kind embedding on the async path.
