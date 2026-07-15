---
type: Survey
title: "Adversarial isolation matrix — Pydantic AI, Mastra, LangGraph"
description: Executable smoke tests that prove tenant-isolation-by-construction is aisotropy's surviving differentiator. Each arena writes secret-A into a shared session, then reads from every cross-tenant identity — a marker appearing in the wrong tenant's response is a proven leak.
tags: [arenas, isolation, adversarial, pydantic-ai, mastra, langgraph, competitive]
timestamp: 2026-07-15T00:00:00Z
---

# Arenas — Adversarial isolation comparison

> **Goal:** Prove that writing the *right path* and the *leaky path* costs the same in every
> framework — and that every framework except aisotropy leaks when you take the leaky path.

## Matrix overview

| Row | Category | Pydantic AI | Mastra | LangGraph | aisotropy |
|---|---|---|---|---|---|
| 1 | **Multi-tenant isolation** | Convention (DI `deps.tenant_id` wrong = silent cross-read) | Closest (server-authoritative keys + ownership validation, but a key without tenant still filters) | Prefix-match `store.search(('acme',))` = cross-tenant scan | **By construction** (ctx propagation + per-statement/key scoping, constitutional gate) |
| 2 | Structured output + retry | Built-in (fail-closed, `raise ModelRetry`) | Built-in (AI-SDK v5) | Built-in (via `with_structured_output` + `OutputFixingParser`) | Built-in (Gollem reflection loop, fail-closed 502 on `/chat`) |
| 3 | Tools | Built-in (function tools) | Built-in (native tool calling) | Built-in (graph nodes as tools) | Built-in (`WithTypedTool[T]`, `WithMCPServer`) |
| 4 | Streaming (SSE) | Built-in (`AsyncAgent.stream`) | Built-in (`ai_sdk` streaming) | Built-in (`StreamingStdioCallbackHandler`) | Built-in (Huma SSE + `ctx` cancellation) |
| 5 | Working memory | DIY (app-level dict / Redis) | Built-in (resource-scoped) | Built-in (checkpointers + Store) | Built-in (BadgerDB, in-process µs) |
| 6 | Semantic memory (vectors) | DIY (BYO Chroma/Pinecone) | Built-in (vector recall) | Built-in (Store + semantic cache) | CODE-ONLY (Qdrant gRPC, dormant by default; in-process cosine brute-force active) |
| 7 | Facts memory (journal) | DIY | DIY | DIY | Built-in (SQLite+FTS5, Engram-style) |
| 8 | Cost governance | DIY (`UsageLimits` per-run) | DIY | DIY | Built-in (pre-spend quota gate + cost meter) |
| 9 | Observability | DIY (BYO OTel) | DIY (BYO) | Paid (LangSmith SaaS) | Built-in (OTel gen_ai semconv, `/costs`, `/health`) |
| 10 | Sovereignty / licensing | MIT | Apache 2.0 | OSS MIT + platform SaaS-gated | 100% MIT/Apache, fully self-hostable |
| 11 | Language | Python | TypeScript | Python | Go + Rust |

## Running the arenas

Each sub-directory is self-contained. Setup and run instructions are in each arena's `README.md`.

### Quick reference

| Arena | Setup | Smoke test | Live serve |
|---|---|---|---|
| `pydantic-ai/` | `pip install pydantic-ai httpx` | `python scripts/smoke.py` | `uvicorn orbit.app:app --port 4110` |
| `mastra/` | `npm install` | `npm run smoke` | `npm run serve` (port 4111) |
| `langgraph/` | `pip install langgraph langchain-openai httpx` | `python smoke.py` | `uvicorn orbit.server:app --port 4112` |

### Prerequisites

- **Python 3.13** required for Pydantic AI and LangGraph arenas (3.14 has no wheels yet).
- **Node 20+** required for Mastra arena.
- An LLM API key (`OPENAI_API_KEY` or `OPENROUTER_API_KEY`) for live tests.
- For offline smoke tests: all three use a **stub/no-op LLM** mode — no key needed.

## What each smoke test proves

Every arena runs the **same adversarial pattern**:

1. **Seed tenant A** with a unique marker token in a shared session ID.
2. **Positive control**: Tenant A reads its own session — marker must appear.
3. **Negative probes**: Tenants B, users B, mixed identities read the same session — marker must NOT appear.
4. **Result**: If the marker appears in a cross-tenant read, the framework leaks.

The leak is demonstrated in every framework — the difference is **how hard it is to write the
correct path vs the leaky path**. In aisotropy, the leaky path is **unrepresentable** (no code
path issues an unscoped read). In the others, both paths are one line apart.

## References

- Competitive landscape: [docs/research/competitive-landscape.md](../research/competitive-landscape.md)
- Build vs adopt decision: [docs/decisions/0020-build-vs-adopt-lang-suite-mastra.md](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
- Reality ledger: [docs/architecture/reality-ledger.md](../architecture/reality-ledger.md)
- aisotropy isolation tests: [`aisotropy/isolation_e2e_test.go`](../aisotropy/isolation_e2e_test.go)
