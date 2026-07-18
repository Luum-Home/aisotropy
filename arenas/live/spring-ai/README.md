# Spring AI — live-arena adapter (Java) — SCAFFOLD

A competitor adapter for the live arena (`arenas/live/`) built on **Spring AI**
(Java), honoring the uniform stdin→stdout contract in
[`../SPEC.md`](../SPEC.md). It drives Spring AI's **native** primitives against
the **same real infra** every adapter shares — `agnes-2.0-flash` (held constant
for the arena), the shared e5 embed server, the shared per-framework Qdrant
collection, and the shared networked rqlite node.

> **Status: partial scaffold, honestly labeled.** The code is authored and
> **compiles + packages** (`mvn package` → a 50 MB uber-jar, Spring AI 1.0.0
> GA) and the **offline stub lane runs end-to-end** for all six modes. The
> **real lane is unverified** this session: agnes free-tier quota is exhausted,
> so no real agnes/e5/Qdrant/rqlite round-trip was exercised. GraalVM
> `native-image` is not installed, so the native/Docker images were not built.
> See [What remains to verify](#what-remains-to-verify). No agnes call was made.

## Parity fit — why Spring AI maps cleanly onto this arena

Spring AI ships first-party abstractions for every dimension the arena tests, so
each mode uses a native primitive rather than a hand-rolled shim:

| Arena dimension | Spring AI native primitive used |
|---|---|
| `qa` / `stream` | `OpenAiChatModel` + `ChatResponse` (provider `Usage`); streaming `ChatModel.stream(...)` `Flux<ChatResponse>` with `streamUsage(true)` for TTFT. |
| `rag` | `OpenAiEmbeddingModel` (e5, `query:` role) + **`QdrantVectorStore`** `similaritySearch(...)`. Multi-tenant parity: Qdrant's in-scan `filterExpression` is the native equivalent of aisotropy's HNSW payload filter (not wired for the shared single-tenant corpus, noted below). |
| `tools` | Native tool-calls **captured, not executed**: `OpenAiChatOptions.internalToolExecutionEnabled(false)` returns the model's `AssistantMessage.getToolCalls()` for capture (Spring AI auto-executes `ToolCallback`s by default — this flag is the correct single-shot seam). |
| `memory` | The native **`ChatMemoryRepository`** SPI, implemented over real rqlite (`RqliteChatMemoryRepository`) for cross-process persistence. |
| `structured` | **`BeanOutputConverter`** — a JSON-Schema-constrained, fail-closed decode (a schema violation throws; it is not coerced to free text). |

## Contract

Reads **one** JSON object on stdin (a `"mode"` field selects behavior; default
`qa`), writes **exactly one** JSON object on stdout (all logs to stderr):

```json
{"framework":"spring-ai","mode":"<mode>","model":"agnes-2.0-flash","reply":"…",
 "input_tokens":0,"output_tokens":0,"latency_ms":0,"tool_calls":[…],
 "retrieved":[…],"structured":<obj|null>,"ttft_ms":0,"error":""}
```

Provider-reported token usage only (or `-1` when unavailable). Errors are data,
not crashes (rule 5). `OPENAI_API_KEY` is never printed, logged, or baked into
the image.

## Modes

| mode | Spring AI capability | seam |
|---|---|---|
| `qa` | Single-shot chat; tools (if present) captured single-shot. | `OpenAiChatModel.call` |
| `rag` | e5 query-embed → `QdrantVectorStore.similaritySearch` → grounded agnes turn. | `QdrantVectorStore` + `OpenAiEmbeddingModel` |
| `tools` | Native `tool_calls` captured, NOT executed. | `internalToolExecutionEnabled(false)` |
| `memory` | Cross-process chat memory over real rqlite via the `ChatMemoryRepository` SPI. | `RqliteChatMemoryRepository` |
| `structured` | Schema-constrained, fail-closed structured output. | `BeanOutputConverter` |
| `stream` | Real token streaming; `ttft_ms` measured; stream usage enabled. | `ChatModel.stream` |

## Offline stub lane (`ARENA_STUB=1`)

`ARENA_STUB=1` swaps the real agnes chat model for an in-process
`StubChatModel`, the real e5 embedder for `StubEmbeddingModel` (deterministic
384-dim vectors), the Qdrant search for a canned corpus, and the rqlite
repository for an in-process map — **zero network calls**. Verified this session
(offline, against bogus unreachable infra endpoints):

```bash
# built once: mvn -DskipTests package
env -u OPENAI_API_KEY ARENA_STUB=1 java -jar target/spring-ai-live-arena.jar \
  <<< '{"mode":"tools","messages":[{"role":"user","content":"What is 47*89?"}],
        "tools":[{"name":"calculate","description":"do math",
        "parameters":{"type":"object","properties":{"expression":{"type":"string"}}}}]}'
# -> {"framework":"spring-ai","mode":"tools",...,
#     "tool_calls":[{"name":"calculate","arguments":{"expression":"47*89"}}],"error":""}
```

Honest stub limitation (same as the sibling adapters): the stub `memory` map is
within-process only, so it cannot prove the **cross-process** recall that real
rqlite gives — the stub lane is a wiring/smoke lane, not a quality lane.

## Real backends (env-configurable — identical names to the sibling adapters)

`run.sh` sources the repo-root `.env` and exports these (defaults target the
local live-arena infra; `run-docker.sh` points them at `host.docker.internal`):

| Env | Tier | Default |
|---|---|---|
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` | agnes chat | — / — / `agnes-2.0-flash` |
| `EMBED_URL` | e5 embed server (OpenAI-compatible) | `http://127.0.0.1:8899` |
| `QDRANT_URL` (+ optional `QDRANT_GRPC_PORT`) | Qdrant (gRPC = REST port + 1) | `http://localhost:6333` → gRPC `6334` |
| `QDRANT_COLLECTION` / `ARENA_COLLECTION` | per-framework collection (runner sets it) | `arena_rag_spring_ai` |
| `RQLITE_URL` | facts / chat-memory tier | `http://localhost:4001` |

`OPENAI_BASE_URL` ending in `/v1` is normalized (stripped) so Spring AI's
`OpenAiApi`, which appends `v1/chat/completions` / `v1/embeddings` itself, does
not produce a doubled `/v1/v1/...` path.

## JVM / GraalVM cold-start + footprint caveat

The other adapters are Go (static binary) or Python. This adapter is a **JVM
uber-jar** (~50 MB + a JRE), so its **cold start** (JVM boot + Spring AI class
init, ~hundreds of ms) and **image footprint** are heavier than the Go/Python
adapters. A **GraalVM `native-image`** build would close that gap (a small
native binary, ~ms cold start) and is the intended packaging for footprint
parity — but a Spring AI native build needs GraalVM plus reachability metadata
for Spring's reflection/proxy usage (the `native-maven-plugin` + AOT hints). No
GraalVM is installed in this session, so the `Dockerfile` ships the **JVM-jar**
build as the honest runnable default and documents the native-image path as the
follow-up (see the comment block at the top of the `Dockerfile`).

## License note

Every dependency is **Apache-2.0** (Spring AI, Spring Framework, Qdrant Java
client, gRPC) or Apache-2.0/BSD-3 (Jackson, Reactor, protobuf) — passes the
repo's constitutional license gate (`../SPEC.md` "Licensing"). **No
GPL/AGPL/SSPL/BSL.**

## What remains to verify

Externally blocked or deferred — NOT skipped silently (per
`.claude/rules/arena-full-matrix.md`, every unverified cell is `pending` in
`matrix.json`, never a fake `pass`):

1. **Real lane (quota-blocked).** Every `real` cell is `pending`. A follow-up
   with agnes quota runs all six modes against real agnes + e5 + Qdrant + rqlite
   and fills `pass`/`fail`. Expect one likely fix here:
   **RAG payload-schema compatibility** — the shared corpus is seeded by
   `_shared/populate-qdrant.py` with a `{doc_id,title,text}` payload, whereas
   Spring AI's `QdrantVectorStore` has its own payload convention (a
   `doc_content` field). The `rag` reader already extracts `text`/`title`
   defensively from both the Document content and metadata, but the mapping must
   be confirmed against a real seeded collection (or the collection re-seeded via
   Spring's own `QdrantVectorStore.add(...)`).
2. **Docker cells (`pending`).** `docker build` + a smoke run per mode were not
   executed (Docker not exercised this session).
3. **GraalVM native-image.** Add the `native` Maven profile
   (`org.graalvm.buildtools:native-maven-plugin`) + AOT/reflection hints and
   build the native binary for footprint parity.
4. **Real `memory`/`tools` numbers.** Cross-process rqlite recall and real
   provider tool-call capture are wired but unmeasured against a live model.

## Files

| Path | Role |
|---|---|
| `pom.xml` | Maven project (Spring AI 1.0.0 BOM, OpenAI + Qdrant + JDBC-chat-memory starters, gRPC, Jackson, shade uber-jar). |
| `src/main/java/arena/springai/Adapter.java` | Main: stdin→dispatch→stdout envelope. |
| `.../Ctx.java` | Parsed request + resolved real-infra endpoints. |
| `.../Models.java` | Factory: real `OpenAiChatModel`/`OpenAiEmbeddingModel`/`QdrantVectorStore` or the offline stubs. |
| `.../Modes.java` | The six mode handlers + `StructuredAnswer` schema + captured-tool callback. |
| `.../StubChatModel.java` / `.../StubEmbeddingModel.java` | Offline, deterministic model + embedder (zero network). |
| `.../RqliteChatMemoryRepository.java` | Spring AI `ChatMemoryRepository` over real rqlite HTTP. |
| `run.sh` / `run-docker.sh` / `Dockerfile` | Native runner, docker runner, JVM-jar image (GraalVM native path documented). |
| `matrix.json` | Full-matrix manifest (stub.native = pass; real + docker = pending; wasm = infeasible). |
