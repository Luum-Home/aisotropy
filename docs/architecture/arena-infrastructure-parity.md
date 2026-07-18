# Arena infrastructure parity — is the comparison fair on real infra?

> **Trigger:** the concern that "aisotropy uses real Qdrant/rqlite/e5 while the competitor
> adapters use lightweight in-memory/mock stores, so aisotropy looks slower for doing real
> work." Investigated via 8 parallel read-only research agents (one per framework + spring-ai
> candidate), each producing a code-truth wiring read + official-doc native-store research.

## Verdict (unanimous across all 8)

**The arena is already fair on infrastructure in the REAL (scored) lane.** Every competitor
adapter, in real mode, hits **real Qdrant (HNSW) + the same real e5 embed server + a real
persistent memory store** (rqlite or on-disk SQLite). None use mock/in-memory stores in the
scored lane. The mock stores (`MockEmbedding`, in-memory corpora, `MockLLM`) exist **only** in
the `ARENA_STUB=1` lane, which is offline/CI, honestly labeled, and **not** the quality number.

**The `~300 ms` vs `~10 s` gap that triggered this investigation was STUB-vs-REAL, not
real-vs-real** — a lane confusion, not an unfairness. And the one genuine anomaly runs the
*other* way: **aisotropy's own stub is the outlier** — it still hits real Qdrant/rqlite/e5 even
under `ARENA_STUB=1` (hence its 10 s latency + 502 drain errors), while every competitor's stub
is fully offline. aisotropy's stub is *more* real than the others', not less.

## aisotropy infrastructure inventory (the parity target)

### DBs / stores — the memory hierarchy by access temperature
| Tier | Tech | Role | Real-mode env seam |
|---|---|---|---|
| Working memory (L1) | **BadgerDB** | live chat + summary buffer, µs, embedded; also the transactional outbox | `AISOTROPY_DATA_DIR` (on-disk) |
| Semantic / vector | **Qdrant** | RAG, real HNSW scan **with tenant/user payload filter in-scan** | `QDRANT_HOST` |
| Facts / tasks | **rqlite / SQLite + FTS5** | Engram-style journal, tenant-scoped, full-text | `RQLITE_URL` |
| Embeddings | **e5** (`crates/embed` + APIEmbedder) | `multilingual-e5-small`, 384-dim | `AISOTROPY_EMBED_URL` |

### Beyond DBs — OS-level pieces (aisotropy differentiators)
DataFusion sleep-cycle consolidation (`crates/sleep-cycle`) · transactional outbox → consolidation
worker (`internal/consolidation`) · model gateway/router (`internal/gateway`) · per-tenant cost
meter (`internal/costmeter`) · token-bucket quota gate + circuit breaker (Phase 7 pre-spend gate) ·
fail-closed reflection loop (`internal/llm`) · gollem cognitive engine · WASM tool sandbox
(`internal/wasmtool` + wazero) · guardrail model (`internal/guardrails` + `crates/guardrail-model`) ·
response cache (`internal/responsecache`) · tenant routing (`internal/routing`) · bits-per-token
memory-value scorer (`internal/bitrate` + `crates/bitrate-scorer`) · engram fact-aggregate
(`internal/engram`) · self-improvement primitives (`internal/primitives`).

## Real-lane parity matrix (code-truth, per research agents)

| Framework | Vector (RAG) | Embeddings | Chat/working memory | Facts/scoped | Tenant isolation |
|---|---|---|---|---|---|
| **aisotropy** | Qdrant HNSW + **in-scan tenant filter** | e5 | BadgerDB on-disk | rqlite/SQLite **+ FTS5** | **by construction** (ctx) |
| pydantic-ai | real Qdrant | real e5 | real rqlite | real rqlite | hand-rolled compound key |
| langgraph | real Qdrant (no tenant filter) | real e5 (same server) | on-disk SQLite checkpointer | hand-rolled SQLite (no FTS5) | hand-rolled* |
| eino | real Qdrant (gRPC) | real e5 | on-disk SQLite | hand-rolled SQLite | hand-rolled |
| llamaindex | real Qdrant | real e5 | on-disk SQLite (`Memory`) | none (no facts tier) | hand-rolled scope key |
| ai-sdk | real Qdrant | real e5 (native `embed()`) | real rqlite | real rqlite | hand-rolled 4-col WHERE |
| mastra | real Qdrant | real e5 | native libsql `Memory` | real rqlite | hand-rolled (native 2-level) |
| genkit-go | real Qdrant (gRPC) | real e5 | real rqlite | real rqlite | hand-rolled |
| _spring-ai (candidate)_ | Qdrant `VectorStore` + in-scan `filterExpression` | e5 via `EmbeddingModel` | JDBC/Redis `ChatMemory` | none native | `filterExpression` |

\* LangGraph *does* ship a native Store API (namespaces + semantic search); the adapter README's
"no native memory primitive" is imprecise — the accurate gap is "not **enforced** by construction."

## What the differences actually are

Every residual difference is a tier where **aisotropy does MORE real work** — so competitors are
**not** unfairly advantaged; if anything aisotropy is under-credited:

1. **In-scan tenant payload filter.** aisotropy filters DURING the HNSW scan (constitutional
   isolation). Most competitors do an unfiltered top-k (the RAG corpus is single-tenant, so it
   doesn't change RAG scores) — aisotropy pays extra work they skip. langgraph/llamaindex/mastra/
   spring-ai support the native filter and *could* match it (optional refinement).
2. **FTS5 facts tier.** aisotropy has a full-text facts/tasks journal. Most competitors have no
   facts tier at all — it's an aisotropy-additive capability, not a competitor shortcut.
3. **Isolation by construction.** aisotropy's `tenant_id` rides `context.Context` and cannot be
   omitted; competitors hand-roll a compound scope key (a forgotten column = a real leak). This
   is the exact framework difference the `isolation_leak` dimension exists to measure — a truthful
   comparative result, not an infra mismatch to "fix."

These OS-level pieces (consolidation, quota gate, reflection, WASM sandbox, guardrails, cost meter)
are aisotropy's thesis. The honest arena position is **the dimensions TEST whether a competitor has
them** — not to replicate them into competitors (which would erase the measured advantage).

## Action items (small — the arena is largely already fair)

1. **P0 — reporting discipline (the real risk):** RESULTS.md must only ever compare
   **real-vs-real**, never aisotropy-real against a competitor's stub. Add a guard/label so a
   mismatched-lane comparison can't be rendered silently.
2. **P0 — fix aisotropy's own stub:** under `ARENA_STUB=1`, `clearBackendTierEnv()` should make it
   fully offline like the others (it currently still reaches Qdrant/rqlite/e5 → 10 s + 502 drain).
   Either make the stub truly in-process, or document that aisotropy's stub is a real-infra lane.
3. **P1 — doc-truth fixes:** correct adapter READMEs that overstate framework gaps (langgraph
   "no memory primitive" → "primitive exists, not enforced"; eino/genkit-go "no Qdrant plugin" is
   accurate for Go and should stay).
4. **P2 — optional parity refinements (all favor aisotropy, so low urgency):** add a tenant payload
   filter to competitor RAG paths (langgraph/llamaindex/mastra/spring-ai support it natively); swap
   direct clients for native abstractions where it's pure polish (mastra `QdrantVector`, genkit-go
   `compat_oai` embedder).
5. **P3 — candidate:** add **spring-ai** as a competitor. Strong infra-parity fit (first-class
   `VectorStore`/`ChatMemory`/`EmbeddingModel`, Apache-2.0), but a JVM cold-start/footprint outlier
   unless shipped as a GraalVM native image (~2–4× the effort of an existing adapter).

## Sources

Per-framework research reports (langgraph, llamaindex, eino, pydantic-ai, ai-sdk, mastra, genkit-go,
spring-ai) with official-doc URLs are captured in the session transcript; key native-store docs:
Qdrant integrations for LangChain/LlamaIndex/Mastra/Spring-AI, LangGraph Store API, Vercel AI SDK
embeddings, Genkit Go plugin catalog, Pydantic-AI embeddings guide.
