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
| `isolation_forgetful` | Omitted-scope read routed through Spring AI's own `ChatMemory` boundary — it REFUSES (throws) on a blank conversationId. | `MessageWindowChatMemory.get` |

## Memory-extension dimensions + tenancy (`memory_cross_session` / `memory_semantic_recall` / `memory_consolidation` / `isolation_forgetful`)

These RUNNER-INTERNAL dimensions (`docs/architecture/arena-memory-extensions.md`,
`docs/architecture/arena-tenancy-levels.md`) arrive on the wire as ordinary `memory` turns plus the
additive `user_id` field (and a distinct `isolation_forgetful` recall mode), with the runner VARYING
`session_id` while HOLDING `user_id` constant. Before wiring, the **capability-absence sweep** was run
against Spring AI 1.0.0's actual jars — not docs — to avoid both a strawman (understating Spring AI) and a
faked pass (overstating it). Per-dimension primitive, maturity, version, and verdict:

| Dimension | Spring AI primitive | Maturity | Version | Arena verdict |
|---|---|---|---|---|
| `memory_cross_session` | native `ChatMemory` (`ChatMemoryRepository` SPI / `MessageWindowChatMemory`), `CONVERSATION_ID` = user-derived | **NATIVE, single-axis** (one memory key; user mapped onto conversationId) — https://docs.spring.io/spring-ai/reference/api/chat-memory.html | spring-ai 1.0.0 | **real: pending** (mechanism proven offline; scoring needs real rqlite + real model) |
| `memory_semantic_recall` | — none — | **NONE** (jar-verified) | — | **infeasible: no native semantic-recall chat-memory primitive** |
| `memory_consolidation` | — none — | **NONE** (jar-verified) | — | **infeasible: no native consolidation primitive** |
| `isolation_forgetful` | native `ChatMemory.get` refuses a blank `conversationId` | **L1** (loud-refuse, single-axis) — https://docs.spring.io/spring-ai/reference/api/chat-memory.html | spring-ai 1.0.0 | **pending** (refusal proven offline; blocked on a runner signature — below) |

**`memory_cross_session` — NATIVE, single-axis.** Spring AI's `ChatMemory` has exactly ONE key
(`CONVERSATION_ID`); it has no native second (user/resource) axis. So `Modes.conversationId(ctx)` maps
`user_id` onto that single axis (`user:<id>`) when present, else keys by `session_id` (plain `memory`,
byte-for-byte unchanged). A recall in a DIFFERENT `session_id` for the SAME user re-hydrates the persisted
turns via the native `RqliteChatMemoryRepository` (the `ChatMemoryRepository` SPI). Verified offline that
persist and recall both key `user:<id>`; `ARENA_STUB`'s per-JVM static map cannot cross the persist→recall
PROCESS boundary and the stub model cannot echo the recalled fact, so the SCORED cell is `real: pending`,
stub `infeasible` — same convention as every wired competitor. **This is declared single-axis, honestly:
user and session collapse onto one conversationId, which is the real shape of Spring AI's memory, not a
workaround.**

**`memory_semantic_recall` / `memory_consolidation` — infeasible (jar-verified, not a strawman).** Spring
AI 1.0.0's `org.springframework.ai.chat.memory` package ships **only** `ChatMemory`,
`MessageWindowChatMemory`, `ChatMemoryRepository`, `InMemoryChatMemoryRepository` — a conversationId-keyed
**last-N window**, nothing semantic and nothing consolidating (verified by unzipping `spring-ai-model-1.0.0.jar`;
the chat-memory advisors are only `MessageChatMemoryAdvisor` / `PromptChatMemoryAdvisor`, both last-N). Spring
AI *does* have `VectorStore` + `QuestionAnswerAdvisor`, but those are **document RAG**, not conversational
memory recall — bolting the RAG retriever onto conversation turns would score adapter-invented code, not a
Spring AI memory primitive. So both cells are `infeasible: no native primitive`, the honest label, rather
than a contrived pass.

**`isolation_forgetful` — REFUSES, and the exact string the runner needs.** Spring AI is **L1** with a
*loud* failure mode (`docs/architecture/arena-tenancy-levels.md`): `CONVERSATION_ID` is required and
Spring AI throws if it is omitted. The `isolation_forgetful` recall (`Modes.isolationForgetful`) routes the
omitted (blank) conversationId through Spring AI's OWN `ChatMemory` boundary —
`MessageWindowChatMemory.get(conversationId)` — where `Assert.hasText` throws **before any model call**.
The omission lands at the *framework's* boundary, not adapter glue (fairness). Verified offline
(`env -u OPENAI_API_KEY ARENA_STUB=1`, mode `isolation_forgetful`, blank `user_id`), the adapter's error
envelope carries the exact Spring AI message:

```
conversationId cannot be null or empty
```

This is a model-independent **refuses → PASS** verdict. But `arenas/live/runner/main.go`'s
`forgetfulEnforcementSignatures` does not yet recognize it, so the cell currently scores run-and-fail. Held
at **`pending`** (never a faked `pass`), pending this **runner follow-up** (NOT edited by this adapter — the
runner is out of scope): add a signature such as

```go
{name: "spring-ai-conversationid-required", all: []string{"conversationid cannot be null or empty"}},
```

(matching case-insensitively, all-substrings, exactly like the existing `mastra-scope-required` entry).
Once that lands, `softenForgetfulRefusal` classifies Spring AI's throw as enforcement=PASS — the L2/L3-ish
"refuses" behavior it earns. An even more robust signature is `all: []string{"conversationid", "null or empty"}`.

Enforcement nuance, surfaced honestly: Spring AI's conversationId assertion lives in the `ChatMemory`
FACADE (`MessageWindowChatMemory`), **not** in the `ChatMemoryRepository` SPI that the `memory` handler
writes through. An integrator who drops to the repository loses even that guarantee — a real L1 property,
not hidden. The `isolation_forgetful` handler deliberately routes the omission through the enforcing facade,
which is what a Spring AI user of `ChatMemory` actually gets.

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
