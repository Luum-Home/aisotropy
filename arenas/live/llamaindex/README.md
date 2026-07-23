# LlamaIndex — live arena adapter (exhaustive, multi-mode)

Runs real chat turns through [LlamaIndex](https://www.llamaindex.ai/) (`llama-index-core`, MIT)
against **agnes-2.0-flash** — the same OpenAI-compatible model every other live-arena adapter
targets — and, for the modes that need it, against **real** infrastructure: a real e5
embeddings server, a real Qdrant collection, and a real on-disk SQLite store. Nothing here is
stubbed or mocked. The point of a "live arena" is comparative: same model, same uniform JSON
contract in/out, real backing services — any difference observed between frameworks is the
framework's behavior, not the model's or a fake harness's.

This adapter is additive: it lives entirely under `arenas/live/llamaindex/` and does not touch
any other arena, `internal/`, `arenas/live/runner/`, `arenas/live/SPEC.md`, or the repo-root
`.env`.

## From single-shot to exhaustive multi-mode

The original adapter did one thing: a single `llm.chat()` turn, with native `tool_calls`
captured but never executed. That is still exactly what happens when `mode` is `"qa"` or
absent — this is the arena runner's default mode (`arenas/live/runner/` globs `run.sh` and
does not send a `mode` field), so **the runner's existing behavior is unchanged**.

This upgrade adds five more modes, each exercising a different LlamaIndex-native capability
against real infrastructure, selected by a `"mode"` field on the stdin JSON:

| Mode | LlamaIndex feature under test | Real infra it touches |
|---|---|---|
| `qa` | Single-shot `llm.chat()`, native `tool_calls` captured, NOT executed | agnes only |
| `rag` | `QdrantVectorStore` + `VectorStoreIndex` + `RetrieverQueryEngine` | agnes, e5 embed server, Qdrant `arena_rag` |
| `tools` | `FunctionAgent` multi-step loop, tool ACTUALLY EXECUTED | agnes (+ Qdrant for `corpus_lookup`) |
| `memory` | `llama_index.core.memory.Memory` (SQLAlchemy-backed) | agnes, on-disk SQLite (`.memory/<session_id>.db`) |
| `structured` | `LLMTextCompletionProgram` + `PydanticOutputParser` (PydanticProgram family) | agnes only |
| `stream` | `llm.stream_chat()`, real token streaming | agnes only |

A further `observability` mode measures LlamaIndex's OWN telemetry surface (not chat quality —
see its own section below).

A second, later upgrade adds two more modes that specifically target what `memory` does **not**
test — multi-tenant scoped memory and cross-tenant isolation, aisotropy's own differentiator:

| Mode | LlamaIndex feature under test | Real infra it touches |
|---|---|---|
| `memory_scoped` | Hand-rolled compound scope key over `llama_index.core.memory.Memory` | agnes, on-disk SQLite (`.memory/<tenant>::<user>::<product>[::<session>].db`) |
| `isolation_leak` | Same hand-rolled compound-scope store, probed cross-tenant | agnes, on-disk SQLite (same files as `memory_scoped`) |

**Isolation is hand-rolled, not framework-enforced.** `llama_index.core.memory.Memory` has
exactly ONE native partition axis — `session_id` — and nothing else: no `tenant_id`, no
`user_id`, no `product_id` anywhere in `Memory.from_defaults()`'s signature. The plain `memory`
mode above only ever keys by `session_id`; it proves cross-process recall works, it does **not**
prove multi-tenant isolation, because `session_id` alone carries no tenant concept at all — two
different tenants sharing (or colliding on) the same `session_id` would silently share one
on-disk file and read each other's history. `memory_scoped` and `isolation_leak` fix that by
folding `tenant_id` + `user_id` + `product_id` (+ optional `session_id`) into one compound string
(`adapter.py::_scope_key()`) and using THAT as LlamaIndex's `session_id`. This is entirely
adapter-author code — LlamaIndex supplies no compound-key primitive, no tenant-aware store, and
no guardrail against getting the key wrong. Get the compound key wrong once — omit `tenant_id`,
typo it, let two tenants collide on the same joined string — and the isolation is gone with no
error, no warning, nothing: the mistake **is** the leak. **Contrast: aisotropy enforces isolation
by construction** — key-prefix scoping on BadgerDB/rqlite reads plus Qdrant in-HNSW-scan payload
filtering (never a post-filter), per `.claude/rules/constitutional-gates.md`
"Multi-tenant data isolation" — there is no code path that skips the filter, so there is no
"forgot the filter" mistake available to make. Here, in LlamaIndex, that mistake is one missing
function argument away.

A THIRD, later upgrade adds `crash_recovery`, which asks a question `memory` mode structurally
cannot answer: does recall survive a process that is `kill -9`ed mid-conversation, not just one
that exits cleanly?

| Mode | LlamaIndex feature under test | Real infra it touches |
|---|---|---|
| `crash_recovery` | Same `Memory` store as `memory`, driven by two real OS processes with a real `kill -9` in between | agnes, on-disk SQLite (same `.memory/<session_id>.db` file `memory` mode uses) |

A FOURTH, later upgrade adds `middleware` (LlamaIndex NATIVE interception, two honest
mechanisms — see its own section below) and an `ARENA_STUB=1` offline toggle that swaps every
real backend (agnes, e5, Qdrant) for an in-process LlamaIndex mock (`MockLLM`/`MockEmbedding`)
so the whole adapter — every mode — can run with zero network egress:

| Mode | LlamaIndex feature under test | Real infra it touches |
|---|---|---|
| `middleware` | `_MiddlewareLLM` (subclasses `llama_index.core.llms.llm.LLM`) for inbound prompt mutation + the native instrumentation dispatcher (observe-only) for outbound call counting | agnes only |

## The uniform envelope

```json
{
  "framework": "llamaindex",
  "mode": "qa|rag|tools|memory|structured|stream|memory_scoped|isolation_leak|crash_recovery",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}, "result": "string (execute-modes only)"}],
  "retrieved": ["doc title", "..."],
  "structured": {"...": "..."},
  "ttft_ms": 0,
  "error": "",
  "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true},
  "leak_detected": false,
  "role": "process_a|process_b",
  "resumed": true,
  "precrash_fact_recalled": true,
  "state_lost": false,
  "turn2_committed": false
}
```

Fields not relevant to a given mode keep their zero-value default (`retrieved: []`,
`structured: null`, `ttft_ms: -1`, etc.) — never fabricated, matching the original adapter's
"provider-reported tokens only, else `-1`" discipline. On failure the same envelope shape is
emitted with `error` set and everything else at its default; the adapter exits non-zero but
never crashes without writing valid JSON to stdout (`arenas/live/SPEC.md` rule 5: "errors are
data, not crashes") — **except** the `crash_recovery` scenario's `process_a`, which is killed by
`kill -9` and BY DESIGN never gets a chance to emit anything at all; that absence of output is
itself the evidence `test-crash.sh` checks for. `scoped_recall`/`leak_detected` are additive keys
present ONLY on `memory_scoped`'s/`isolation_leak`'s outputs; `role`/`resumed`/
`precrash_fact_recalled`/`state_lost`/`turn2_committed` are additive keys present ONLY on
`crash_recovery`'s output (`role`/`resumed`/`precrash_fact_recalled`/`state_lost` always present
for this mode, even on a validation failure — `turn2_committed` present only on `process_b`'s
successful output) — every other mode's envelope is unchanged from before this upgrade.

## Env-configurable host targets

| Env var | Default | Used by |
|---|---|---|
| `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` | *(required, from repo-root `.env`, UNLESS `ARENA_STUB=1`)* | all modes |
| `EMBED_URL` | `http://127.0.0.1:8899` | `rag`, `tools` (`corpus_lookup`), `observability` — ignored under `ARENA_STUB=1` |
| `QDRANT_URL` | `http://localhost:6333` | `rag`, `tools` (`corpus_lookup`), `observability` — ignored under `ARENA_STUB=1` |
| `ARENA_COLLECTION` | `arena_rag` | `rag`, `tools` (`corpus_lookup`), `observability` — ignored under `ARENA_STUB=1` |
| `LLAMAINDEX_MEMORY_DIR` | `<script_dir>/.memory` | `memory`, `memory_scoped`, `isolation_leak`, `crash_recovery` |
| `ARENA_STUB` | unset (`0`) | ALL modes — `1`/`true`/`yes` swaps agnes for `llama_index.core.llms.MockLLM` and the real e5/Qdrant RAG backend for `llama_index.core.embeddings.MockEmbedding` + an in-memory `VectorStoreIndex`, so the whole adapter runs offline. See "Offline stub toggle" below. |

`run-docker.sh` overrides `EMBED_URL`/`QDRANT_URL` to `http://host.docker.internal:{8899,6333}`
so the containerized adapter reaches the host-run embed server and Qdrant; `OPENAI_BASE_URL`
needs no override since agnes is already a public URL reachable from inside the container.

## Wiring — exact plumbing per mode

### `qa` — unchanged single-shot

`llama_index.llms.openai_like.OpenAILike` (package `llama-index-llms-openai-like`, MIT) — a
thin subclass of `llama_index.llms.openai.OpenAI` that lets you override `api_base`/`api_key`/
`model` (`.venv/lib/python3.13/site-packages/llama_index/llms/openai_like/base.py`). A single
`llm.chat(messages, tools=..., tool_choice="auto")` call; native `tool_calls` are parsed off the
response into `ToolCallBlock`s (`llama_index/llms/openai/utils.py::from_openai_message`) and
mapped into the uniform `tool_calls` field — **not executed**, matching the arena's single-shot
contract.

### `rag` — real retrieval, real vector store, real embeddings

1. **Embed the query** through the real e5 server via `llama_index.embeddings.openai_like.
   OpenAILikeEmbedding(model_name="multilingual-e5-small-query", api_base=EMBED_URL)` — the
   `-query` suffix makes the server apply the e5 `query:` instruction prefix
   (`arenas/live/_shared/embed-server/app.py`).
2. **Retrieve** via `llama_index.vector_stores.qdrant.QdrantVectorStore(client=QdrantClient(url=
   QDRANT_URL, check_compatibility=False), collection_name="arena_rag")` wrapped in
   `VectorStoreIndex.from_vector_store(...).as_retriever(similarity_top_k=3)`. The `arena_rag`
   collection was populated by `arenas/live/_shared/populate-qdrant.py` with a **raw** payload
   shape `{doc_id, title, text}` — not LlamaIndex's own node-serialization format. This still
   works because `QdrantVectorStore.parse_to_query_result()` first tries
   `metadata_dict_to_node(payload)` (LlamaIndex's own format), and on failure falls back to
   `legacy_metadata_dict_to_node()` + `TextNode(text=payload.get(self.text_key), ...)` with
   `text_key` defaulting to `"text"` — exactly the field name `populate-qdrant.py` used
   (`llama_index/vector_stores/qdrant/base.py:1328-1370`). Verified end-to-end against the real
   collection (see Acceptance evidence below): correct top-1 doc, correct similarity ordering.
3. **Augment + answer** via `llama_index.core.query_engine.RetrieverQueryEngine.from_args(
   retriever=retriever, llm=llm, text_qa_template=<custom PromptTemplate>)`. The template forces
   "answer ONLY from the retrieved context; if not present, say I don't know" — this is what
   makes the two trap questions in `arenas/live/_shared/rag_questions.jsonl` catch hallucination
   instead of an invented answer.
4. **`retrieved`** is populated from `response.source_nodes[*].node.metadata["title"]`.

### `tools` — real multi-step execution, not capture

`llama_index.core.agent.workflow.FunctionAgent(tools=[calculate, corpus_lookup], llm=llm,
system_prompt=...)`. `agent.run(user_msg=...)` returns a `WorkflowHandler`; the adapter
`async for ev in handler.stream_events()` and collects every `ToolCallResult` event (`ev.
tool_name`, `ev.tool_kwargs`, `ev.tool_output` — the REAL return value of the Python function
that actually ran), then awaits the handler for the agent's final text. Two real, executable
tools (per `arenas/live/_shared/README.md`'s "never a stubbed tool" convention):

- `calculate(expr)` — an AST-walking arithmetic evaluator (`ast.parse(expr, mode="eval")` over a
  restricted operator table: `+ - * / ** % //` and unary minus). No `eval()`, no builtins, no
  names — cannot execute arbitrary code.
- `corpus_lookup(query)` — embeds the query through the real e5 server and performs a real
  top-1 Qdrant search against `arena_rag`, returning the matched passage's title + full text.

### `memory` — real, on-disk, cross-process persistence

`llama_index.core.memory.Memory.from_defaults(session_id=..., async_database_uri="sqlite+
aiosqlite:///<LLAMAINDEX_MEMORY_DIR>/<session_id>.db")` — LlamaIndex's native SQLAlchemy-backed
memory store (ships in `llama-index-core`; `aiosqlite`/`SQLAlchemy` are already transitive
deps). Each adapter invocation is one process: `await memory.aget()` loads prior turns from the
SQLite file, the new user turn is answered with that history in context via `await llm.achat(
...)`, then both the user turn and the assistant reply are persisted with `await memory.aput(...)`
before the process exits. A second, completely separate `bash run.sh` invocation with the same
`session_id` reopens the same `.db` file and sees the prior turns — this is real cross-process
persistence, not a process-local dict (verified in Acceptance evidence: a fact stated in
invocation 1 is correctly recalled in invocation 2, in a fresh Python process).

**This mode does NOT test multi-tenant isolation.** `session_id` is the only scope axis; nothing
here distinguishes one tenant's facts from another's. See `memory_scoped` and `isolation_leak`
below.

### memory-extension family — `Memory` + native long-term blocks, keyed by USER

The runner's three memory-extension dimensions — `memory_cross_session`, `memory_semantic_recall`,
`memory_consolidation` (`docs/architecture/arena-memory-extensions.md`) — all arrive over the wire
as ordinary `mode:"memory"` turns carrying a `user_id`, with `session_id` VARYING across a task's
invocations while `user_id` is HELD. A session-keyed transcript store therefore returns empty on
the fresh recall session, so only genuine USER-scoped memory can recall the fact — transcript
replay is defeated by construction (`arenas/live/runner/tasks.go` `buildMemoryExt`). Because the
wire mode is plain `memory`, no new adapter mode is dispatched: `run_memory` detects the `user_id`
and switches to LlamaIndex's NATIVE long-term memory-blocks path (`_build_user_memory_blocks` /
`_user_memory_turn`) instead of the session-keyed SQLite path above. Plain `memory` (no `user_id`)
is byte-for-byte unchanged.

| Dimension | LlamaIndex primitive (all NATIVE, in `llama-index-core==0.14.23`) | What it does here |
|---|---|---|
| `memory_cross_session` | `llama_index.core.memory.Memory` + `VectorMemoryBlock` keyed by USER | A fact written in session A recalls in session B for the same user: the persisted turns are stored in a per-USER `QdrantVectorStore` collection (`arena_mem_llamaindex__<user>`), and the recall session — whose session-keyed short-term `Memory` is empty — retrieves them from that user-scoped block. |
| `memory_semantic_recall` | `llama_index.core.memory.VectorMemoryBlock` | Retrieval is SEMANTIC (embedding similarity via the SAME shared e5 path `rag` mode uses — `OpenAILikeEmbedding` over `EMBED_URL`), not lexical: `VectorMemoryBlock.aget(messages=[question])` embeds the paraphrase and queries the user's vector collection. |
| `memory_consolidation` | `llama_index.core.memory.FactExtractionMemoryBlock` | On persist/flush an LLM extracts durable facts (LlamaIndex's native consolidation primitive). The `"consolidate"` directive invocation is itself an ordinary `mode:"memory"` turn that runs the extraction; the durable cross-process carrier of the recalled fact is the `VectorMemoryBlock` vector store (the extracted-fact list is process-local — see MUST-CHECK in the arena report). |

Maturity: **NATIVE** for all three — `Memory`, `VectorMemoryBlock`, `FactExtractionMemoryBlock`
all ship in `llama-index-core==0.14.23` (no extra package). Docs:
https://developers.llamaindex.ai/python/framework/module_guides/deploying/agents/memory/ ;
https://developers.llamaindex.ai/python/examples/memory/memory/ ;
https://www.llamaindex.ai/blog/improved-long-and-short-term-memory-for-llamaindex-agents .
The vector store binding is `llama-index-vector-stores-qdrant==0.10.2` (real lane) /
`qdrant-client==1.18.0` in-memory (offline stub); embeddings via
`llama-index-embeddings-openai-like==0.3.1`.

**Offline stub (`ARENA_STUB=1`):** the vector store is an in-process in-memory Qdrant
(`QdrantClient(location=":memory:")`) with `MockEmbedding`. This proves the memory-blocks WIRING
drives offline (verified: `VectorMemoryBlock.aput` then `.aget` retrieves the needle in one
process), but the in-memory store does NOT survive the persist→recall PROCESS boundary, so
offline cross-session RETRIEVAL is infeasible — hence the `memory_*` stub matrix cells are
`infeasible`, real cells `pending` (they need real e5 + a persistent Qdrant collection + agnes;
see the MUST-CHECKs).

### `isolation_forgetful` — the omitted-scope read at LlamaIndex's own boundary

The "forgetful integrator" negative-tenancy probe (`docs/architecture/arena-tenancy-levels.md`
§"forgetful integrator"). LlamaIndex is **L1**: its first-party multi-tenancy pattern is per-query
`MetadataFilters` (https://docs.llamaindex.ai/en/stable/examples/multi_tenancy/multi_tenancy_rag/) —
a scoping PARAMETER that partitions data only if the caller passes it correctly every time; a wrong
or omitted value silently reads another scope. The PERSIST invocation writes the fact under an
explicit user through the user-scoped memory-blocks path above; the RECALL invocation arrives as
`mode:"isolation_forgetful"` with `user_id` DELIBERATELY BLANK (`arenas/live/runner/tasks.go`
`buildForgetful`). `run_isolation_forgetful` forwards that blank scope RAW to LlamaIndex's own query
boundary — no default is backfilled (`_user_memory_turn(..., scope_user="")` → `_user_mem_collection("")`
→ a different, empty collection → `VectorMemoryBlock.aget` returns nothing). LlamaIndex has no
by-construction way to REFUSE a blank scope, so the runner's `scoring.ScoreForgetful` sees neither
the scoped answer (no leak) nor an enforcement error → `silent_partition` → **FAIL**. That FAIL is
the measured L1-vs-L3 point (aisotropy, L3, `refuses` → PASS). Primitive: `VectorMemoryBlock` /
`MetadataFilters` (`llama-index-core==0.14.23`), maturity **NATIVE** (L1). Verified offline
(stub matrix cell `fail`); the real-lane label (silent_partition vs an accidental leak) is a
MUST-CHECK.

### `crash_recovery` — kill -9 durability vs. `memory`'s clean-exit recall

`memory` mode (above) proves cross-process recall across two **clean** `bash run.sh` exits. It
does NOT prove anything about what survives a process that never gets to exit at all — no
`return`, no `finally`, no flushed buffers, no chance to log. `crash_recovery` reuses `memory`'s
EXACT on-disk wiring (same `Memory.from_defaults(session_id=..., async_database_uri="sqlite+
aiosqlite:///<MEMORY_DIR>/<session_id>.db")` call, same `.db` file) but drives it through two REAL
OS processes that `test-crash.sh` orchestrates, with a real `kill -9` in between:

- **`role=process_a`** runs 2+ sequential turns from `messages`. Each turn's user+assistant pair
  is committed with `await memory.aput(...)`, which internally does `await session.commit()`
  per call (`llama_index/core/storage/chat_store/sql.py::add_message`) — i.e. every `aput()`
  is a real, awaited SQLite commit, not a buffered write, BEFORE the adapter starts the next
  turn. The instant turn `crash_after_turn` (default 1) commits, `adapter.py` logs
  `PROGRESS: CHECKPOINT_TURN_<n>_COMMITTED` to stderr and then — before doing any further I/O —
  sleeps for `inject_delay_ms` (a documented test hook, default 0). `test-crash.sh` watches
  stderr for that exact line and sends `kill -9` partway through the sleep window: strictly
  after the last committed turn is durable, strictly before the next turn could possibly commit.
  This makes the crash's TIMING deterministic (not a race against agnes's real network latency)
  without touching the durability mechanism under test (SQLite commit-before-continue).
- **`role=process_b`** is a FRESH process — new PID, new Python interpreter, no shared state
  with `process_a` beyond the `session_id` and the on-disk file — that reopens the SAME `.db`
  and calls `await memory.aget()`, then runs one real, live agnes continuation turn grounded in
  whatever history comes back.

Verdict fields are deterministic store-level checks, the same discipline as `memory_scoped`'s
`same_scope_hit`/`cross_tenant_miss` — never inferred from the LLM's reply alone:

| Field | Meaning | How it's computed |
|---|---|---|
| `resumed` | process_b successfully picked up pre-crash state | `history` load raised no exception AND is non-empty |
| `precrash_fact_recalled` | the pre-crash fact survived AND is live-recallable | `expected_fact_substring` present in BOTH the recovered on-disk history (`fact_in_store`) AND the live agnes reply (`fact_in_reply`) |
| `state_lost` | a committed fact went missing — a real durability failure | `True` only if `expected_fact_substring` was requested but `fact_in_store` is `False`; always `False` when the fact IS in the store |
| `turn2_committed` | did the CRASHED turn leak a partial write anyway | `turn2_marker_substring` present in the recovered history — expected `False`; `True` would mean the kill landed too late (a harness timing miss, not a framework finding) |

### `memory_scoped` — hand-rolled compound-scope multi-tenant memory

Reuses `memory`'s exact on-disk mechanism (`Memory.from_defaults(session_id=..., async_database_
uri=...)`, one `.db` file per scope) but keys it with a **hand-rolled compound scope**,
`adapter.py::_scope_key(tenant_id, user_id, product_id, session_id=None)`:

```python
parts = [str(tenant_id), str(user_id), str(product_id)]
if session_id:
    parts.append(str(session_id))
raw = "::".join(parts)
return re.sub(r"[^A-Za-z0-9_.:-]", "_", raw)   # sanitized for safe use as a filename segment
```

That joined string — e.g. `tenant-a::user-x::prod-p` — is what actually gets passed as
LlamaIndex's `session_id`. LlamaIndex itself has no idea a "tenant" or "product" exists; as far
as `Memory` is concerned this is just an opaque partition string, identical in kind to the plain
`memory` mode's raw `session_id`. **The isolation guarantee comes entirely from the adapter
composing this string correctly, not from anything LlamaIndex enforces.**

One `memory_scoped` invocation performs a full three-step proof and reports the result in the
envelope's `scoped_recall` field:

1. **WRITE** `fact` into scope `(tenant_id, user_id, product_id[, session_id])` — a direct
   `await memory.aput(ChatMessage(role=USER, content=fact))`, no LLM call (deterministic disk
   write; `adapter.py::_scope_write()`).
2. **RECALL, same scope** — `await memory.aget()` against that exact scope key, then a
   deterministic substring check (`adapter.py::_history_contains()`) reports `same_scope_hit`.
   A real `await llm.achat(...)` call is then made with that scope's history as context,
   producing the envelope's `reply` — proving genuine LLM-grounded recall, not just disk I/O.
3. **RECALL, cross-tenant** — `await memory.aget()` against `(cross_tenant_id, user_id,
   product_id[, session_id])`, a DIFFERENT compound key (same user/product, different tenant).
   The same substring check reports `cross_tenant_miss` — no LLM call for this step, since the
   isolation verdict is defined as what the store itself returns, not an LLM's paraphrase of it.

### `isolation_leak` — a real cross-tenant leak probe

Same compound-scope mechanism as `memory_scoped`, restructured as an attack: tenant `tenant_id`
writes a `secret` into its own scope (`_scope_write`, no LLM call). Then a REAL agnes recall turn
runs `await llm.achat(...)` **as `leak_probe_tenant_id`** — a different compound scope, same
`user_id`/`product_id`/`session_id` — using ONLY that probe tenant's own (unrelated) history as
context. `leak_detected` is a direct substring check of the SECRET against the probe scope's
actually-returned history (`_history_contains(probe_history, secret)`) — again, the store's
return value is the authoritative signal, not the LLM's free-text reply (which is still captured
in `reply` as real corroborating evidence, since a compliant model should also say it has no
knowledge of the secret).

Both modes fail closed on a malformed request: missing `tenant_id`/`user_id`/`product_id`/
`fact`/`cross_tenant_id` (or `leak_probe_tenant_id`/`secret` for `isolation_leak`), or a
`cross_tenant_id`/`leak_probe_tenant_id` that happens to produce the SAME compound key as the
writer's own scope, raises before any store write or LLM call — zero agnes calls spent on an
invalid request (see `test-isolation.sh` assertion (4)).

### `structured` — PydanticProgram, not forced tool-calling

Initially implemented via `llm.structured_predict(OutputCls, prompt, ...)`, LlamaIndex's
function-calling-based structured-prediction path (`FunctionCallingProgram` →
`predict_and_call([tool], ..., error_on_no_tool_call=True)`). **This failed against agnes-2.0-
flash** — see Uncertainties — even with `llm_kwargs={"tool_choice": "required"}` forced, agnes
sometimes answers in plain text instead of emitting the expected tool call, which
`FunctionCallingProgram` treats as a hard error (`ValueError: Expected at least one tool call,
but got 0 tool calls.`, `llama_index/llms/openai/base.py::get_tool_calls_from_response`).

The adapter instead uses `llama_index.core.program.LLMTextCompletionProgram` +
`llama_index.core.output_parsers.PydanticOutputParser` — the completion-based member of the
same "PydanticProgram" family. `PydanticOutputParser.format(prompt_str)` appends the target
Pydantic model's JSON schema as text instructions to the prompt; the program issues one normal
`llm.chat()` call, parses the reply as JSON, and validates it against the Pydantic model,
raising if it doesn't conform. This does not depend on the provider's function-calling
reliability and worked correctly on the first real attempt (see Acceptance evidence).

### `stream` — real token streaming, measured TTFT

`llm.stream_chat(messages)` returns a generator of `ChatResponse` chunks. The adapter records
wall-clock time to the first chunk with a non-empty `.delta` as `ttft_ms`, keeps iterating for
the full latency, and returns the last chunk's accumulated `.message.content` as `reply`.

### `observability` — native instrumentation vs OTel-via-OpenInference

See the dedicated "Observability mode" section below for the full write-up — this mode is
different enough from every other mode (it measures LlamaIndex's own telemetry, not chat
quality) that it gets its own top-level section rather than a paragraph here.

### `middleware` — LlamaIndex NATIVE interception, two honest mechanisms

See the dedicated "Middleware mode" section below for the full write-up — like `observability`,
this mode emits its own JSON shape (`text` + `middleware`), not the uniform envelope.

## Token usage — captured uniformly across every mode

All six modes share one `OpenAILike` instance built once per invocation with a
`CallbackManager` wired to a `_UsageCapture` handler that reads `ChatResponse.raw.usage.
{prompt_tokens,completion_tokens}` off every `CBEventType.LLM` event and **sums** across however
many internal LLM calls that mode's run makes (e.g. `tools` makes one call to select the tool
and another to produce the final answer; both are summed into the reported total — that sum is
the mode's real cost). Never fabricated: a call with no usage in the response contributes
nothing, and if no call in a run reports usage, the field stays `-1`.

**Non-obvious gotcha (documented so it isn't rediscovered):** `RetrieverQueryEngine.from_args()`
does **not** inherit its LLM call's callback manager from the `llm=` instance you pass it — its
own default is `callback_manager or Settings.callback_manager`
(`llama_index/core/query_engine/retriever_query_engine.py`), i.e. it silently falls back to the
process-global `Settings.callback_manager` (a fresh, empty one) if you don't pass
`callback_manager=` explicitly. The fix applied here: `main()` sets `Settings.callback_manager`
to the exact same manager instance used to construct `llm`, so every mode's usage — whichever
LlamaIndex component makes the actual call — lands in the same `_UsageCapture` object with zero
per-mode plumbing.

## Observability mode — native instrumentation vs OTel-via-OpenInference

`observability` is not a chat-quality mode like every other mode above — it measures LlamaIndex's
OWN telemetry surface, and emits its OWN JSON shape on stdout (NOT the uniform envelope):

```json
{
  "framework": "llamaindex",
  "mode": "observability",
  "otel_supported": true,
  "span_count": 15,
  "span_names": ["RetrieverQueryEngine.query", "..."],
  "span_timings_ms": {"RetrieverQueryEngine.query": 10005.95, "...": 0},
  "logs_structured": false,
  "log_sample": "llama_index.core.indices.utils DEBUG > Top 3 nodes:\n...",
  "notes": "..."
}
```

### What it wires — two independent telemetry paths around ONE real turn

The representative turn is the same real, multi-op RAG wiring `run_rag()` uses (real e5 query
embedding, real Qdrant `arena_rag` retrieval, real agnes chat completion) — chosen specifically
because it fans out into several LlamaIndex-internal method calls (embed → retrieve → split →
refine → LLM chat), giving both telemetry paths more than a single span to report.

1. **Native instrumentation** (`llama_index.core.instrumentation`, ships in `llama-index-core`
   as the `llama-index-instrumentation` package — **zero new dependency**). Every LlamaIndex
   component method decorated with `dispatcher.span` (via `DispatcherSpanMixin` — retrievers,
   query engines, synthesizers, the LLM client itself) reports span-enter/span-exit calls up the
   dispatcher parent chain to the ROOT dispatcher
   (`llama_index.core.instrumentation.get_dispatcher()` with no argument —
   `llama_index_instrumentation/__init__.py::get_dispatcher()`). The adapter attaches:
   - a `SimpleSpanHandler` (`llama_index.core.instrumentation.span_handlers.SimpleSpanHandler`,
     also bundled) to `dispatcher.add_span_handler(...)` — records every completed/dropped span
     with real wall-clock `start_time`/`end_time`/`duration`.
   - a custom `BaseEventHandler` subclass to `dispatcher.add_event_handler(...)` — records every
     typed `BaseEvent` LlamaIndex dispatches (`LLMChatStartEvent`/`EndEvent`,
     `RetrievalStartEvent`/`EndEvent`, `EmbeddingStartEvent`/`EndEvent`,
     `SynthesizeStartEvent`/`EndEvent`, `QueryStartEvent`/`EndEvent`,
     `GetResponseStartEvent`/`EndEvent`, `LLMPredictStartEvent`/`EndEvent` — 14 events across 7
     paired start/end types were captured for the one real turn below).
   - Native span ids are `{ClassName}.{method_name}-{uuid4}` (`Dispatcher.span()` in
     `llama_index_instrumentation/dispatcher.py`); the adapter strips the trailing uuid4 via
     regex (`adapter.py::_strip_span_uuid()`) so repeated calls to the same method collapse onto
     one readable name in `span_names`/`span_timings_ms` (`span_timings_ms` keeps the LAST
     occurrence's duration when a name repeats, e.g. the two `OpenAILike.chat` spans below —
     documented, not silently lossy).

2. **OTel via OpenInference** (`openinference-instrumentation-llama-index`, Apache-2.0 —
   **added as a new direct dependency for this mode**, NOT bundled with `llama-index-core`).
   `LlamaIndexInstrumentor().instrument(tracer_provider=...)` patches the SAME
   dispatcher-decorated methods (it is built on top of the native instrumentation dispatcher
   above, not a separate hook point) and emits real `opentelemetry.trace.Span` objects, wired to
   `opentelemetry.sdk.trace.TracerProvider` + `SimpleSpanProcessor(InMemorySpanExporter())` —
   `InMemorySpanExporter` is OTel SDK's own in-process, zero-network exporter
   (`opentelemetry.sdk.trace.export.in_memory_span_exporter`). No collector endpoint is ever
   configured; no network export code path is exercised.

3. **Structured-logging check**: a `logging.Handler` is attached to the `llama_index` stdlib
   logger tree at `DEBUG` for the duration of the turn, to check whether LlamaIndex's own
   logging is structured (JSON/key=value) or plain prose.

### What was actually observed (not assumed)

Both telemetry paths captured the SAME **15 spans** for the SAME real turn — confirming
OpenInference's LlamaIndex integration is a thin OTel-exporting layer over the exact same native
dispatcher, not an independent instrumentation mechanism. One finding worth calling out because
it was NOT the initial assumption: **OTel span NAMES are IDENTICAL strings to the native
dispatcher's span names** (`RetrieverQueryEngine.query`, `OpenAILikeEmbedding.get_query_embedding`,
etc.) — OpenInference does not rename spans to short semantic-convention names. The semantic
layer OpenInference adds instead lives entirely in span **attributes**: `openinference.span.kind`
was found set to one of `CHAIN` / `EMBEDDING` / `LLM` / `RETRIEVER` across the captured spans
(checked directly against the real `InMemorySpanExporter` output, not read from OpenInference's
docs).

LlamaIndex's stdlib logger (`llama_index.core.indices.utils`, at `DEBUG`) emitted **plain prose**
for this turn (a formatted "Top 3 nodes" block with similarity scores) — not JSON, not
key=value pairs. `logs_structured=false` is the honest reading of the one real record captured,
not an assumption about LlamaIndex's logging in general.

### License — Apache-2.0 throughout, no copyleft

| Package | Version | License |
|---|---|---|
| openinference-instrumentation-llama-index | 4.4.3 | Apache-2.0 |
| openinference-instrumentation (transitive) | 0.1.54 | Apache-2.0 |
| openinference-semantic-conventions (transitive) | 0.1.30 | Apache-2.0 |
| opentelemetry-sdk | 1.44.0 | Apache-2.0 |
| opentelemetry-api (transitive) | 1.44.0 | Apache-2.0 |
| opentelemetry-instrumentation (transitive) | 0.65b0 | Apache-2.0 |
| opentelemetry-semantic-conventions (transitive) | 0.65b0 | Apache-2.0 |

`llama_index.core.instrumentation`'s native path (`llama-index-instrumentation` package, MIT)
was already a transitive dependency of `llama-index-core` before this mode — zero new
dependency for the native half of this mode.

## Middleware mode — LlamaIndex NATIVE interception, two honest mechanisms

`middleware` runs ONE qa turn (`system` + `messages`, same request shape as `qa`) through two
INDEPENDENT interception mechanisms and reports whether each fired. It emits its own JSON shape
on stdout (NOT the uniform envelope — chosen deliberately, see below):

```json
{
  "framework": "llamaindex",
  "mode": "middleware",
  "text": "OK",
  "middleware": {
    "mechanism": "llm-wrapper (inbound) + instrumentation-handlers (outbound)",
    "inbound_fired": true,
    "outbound_fired": true,
    "invocations": 1,
    "supported": true
  }
}
```

### Why two mechanisms, not one

LlamaIndex's `llama_index.core.instrumentation` dispatcher — the exact seam `observability`
mode above wires — is **OBSERVE-ONLY**. A `BaseEventHandler.handle(event)` receives a typed
`BaseEvent` (e.g. `LLMChatStartEvent`) as a side-effecting listener call; there is no supported
API for a handler to rewrite the request that produced the event, and no LlamaIndex-native
"middleware" or "interceptor" concept that mutates a prompt in flight. Claiming the
instrumentation dispatcher performs the inbound mutation would be dishonest, so this mode uses
the mechanism that actually CAN mutate a prompt for the inbound transform, and the observe-only
dispatcher — used honestly, for observation — for the outbound transform:

- **INBOUND (prompt mutation) → LLM wrapper.** `_MiddlewareLLM` (defined inside
  `run_middleware()` in `adapter.py`) subclasses `llama_index.core.llms.llm.LLM` — the SAME
  abstract base every real LlamaIndex LLM integration subclasses, including
  `llama_index.llms.openai_like.OpenAILike` (the class every other mode in this adapter wires
  as `llm`). It wraps the real, already-constructed `llm` and its `chat`/`achat`/`stream_chat`/
  `astream_chat` append the sentinel `MIDDLEWARE_INBOUND_SENTINEL = "\n[ARENA_MW]"` to the
  outbound system `ChatMessage` (or inject one if the request had none) before delegating to
  the wrapped LLM's own method. Every other abstract method (`complete`/`acomplete`/
  `stream_complete`/`astream_complete`/`metadata`) delegates unchanged — this is a genuine
  LlamaIndex extension point, not a monkeypatch of `OpenAILike` internals.

- **OUTBOUND (call counting) → instrumentation event handler.** A `BaseEventHandler` is
  attached to the ROOT dispatcher (`get_dispatcher()`, the same call `observability` mode
  uses) and counts every `LLMChatStartEvent` it observes. `llm_chat_callback()` — the decorator
  every `LLM.chat()`/`.achat()` implementation carries, including the real wrapped LLM's —
  fires `dispatcher.event(LLMChatStartEvent(...))` on every call
  (`llama_index/core/llms/callbacks.py::llm_chat_callback`, read directly off this adapter's
  pinned `llama-index-core==0.14.23` install, not assumed from docs). The handler is a pure
  observer of the call the wrapper above triggers — consistent with the dispatcher's own
  observe-only design, and with how `observability` mode uses the same seam.

`mechanism` reports both, honestly, rather than collapsing them into one label that would
overclaim what either piece does alone.

### Verified output (real agnes backend, no `ARENA_STUB`)

```bash
$ echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' | bash run.sh
{"framework": "llamaindex", "mode": "middleware", "text": "OK", "middleware": {"mechanism": "llm-wrapper (inbound) + instrumentation-handlers (outbound)", "inbound_fired": true, "outbound_fired": true, "invocations": 1, "supported": true}}
```

## Offline stub toggle — `ARENA_STUB=1`

Setting `ARENA_STUB=1` (also accepts `true`/`yes`, case-insensitive) swaps every real backend
this adapter can call for an in-process LlamaIndex-native mock, so the WHOLE adapter — every
mode, not just one — runs with zero network egress:

| Real backend | Stub replacement | Modes affected |
|---|---|---|
| agnes (`OpenAILike`) | `llama_index.core.llms.MockLLM` (`llama_index.core.llms.mock.MockLLM`), `is_chat_model=True` | ALL modes |
| e5 embed server (`OpenAILikeEmbedding`) | `llama_index.core.embeddings.MockEmbedding` (`llama_index.core.embeddings.mock_embed_model.MockEmbedding`), `embed_dim=8` | `rag`, `tools` (`corpus_lookup`), `observability` |
| Qdrant `arena_rag` collection (`QdrantVectorStore`) | In-memory `VectorStoreIndex.from_documents()` over a tiny fixed 2-document stub corpus (`adapter.py::_STUB_RAG_DOCS`) — no `QdrantClient` is constructed at all | `rag`, `tools` (`corpus_lookup`), `observability` |

Both mock classes ship inside `llama-index-core==0.14.23` (already a pinned direct dependency,
confirmed by import against the arena's own `.venv`) — **no new package** is added for this
toggle. `memory`/`memory_scoped`/`isolation_leak`/`crash_recovery` already persist to a local
on-disk SQLite file under `LLAMAINDEX_MEMORY_DIR`, never a network store, so they need no swap
to already be offline; `ARENA_STUB=1` only changes which `llm` they call.

`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` are NOT required under `ARENA_STUB=1` — the
envelope's `model` field reports `"mock-llm"` (never a leftover real `OPENAI_MODEL` value from
the environment, even if one happens to be set) so the output is never dishonest about what
actually served the request.

**Honest limitation:** `MockLLM` returns deterministic, non-network text (it echoes the prompt
back, or emits `"text text ..."` if `max_tokens` is set) — enough to prove every mode executes
end-to-end offline, but not a stand-in for real model quality. Two modes have a real, structural
dependency on the model actually doing something MockLLM cannot:
- `tools` mode's `FunctionAgent` requires a `FunctionCallingLLM` and raises
  `ValueError: LLM must be a FunctionCallingLLM` immediately — caught by `main()`, reported as
  `error` in a valid JSON envelope, zero network attempted.
- `structured` mode's `PydanticOutputParser` fails Pydantic validation against MockLLM's
  non-JSON echoed text — same clean, offline, caught-and-reported failure path.

### Verified output (`ARENA_STUB=1`, qa mode)

```bash
$ echo '{"mode":"qa","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' | ARENA_STUB=1 bash run.sh
{"framework": "llamaindex", "mode": "qa", "model": "mock-llm", "reply": "system: You are a rail assistant.\nuser: Say OK.\nassistant: ", "input_tokens": -1, "output_tokens": -1, "latency_ms": 0, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

`rag`, `stream`, `memory`, `memory_scoped`, and `observability` were also exercised under
`ARENA_STUB=1` during acceptance and confirmed to complete offline with valid JSON output
(`observability`'s native span/event counts and OTel span count are identical in shape to the
real-backend run, just over the 2-document stub corpus and `MockLLM`/`MockEmbedding` instead of
agnes/e5/Qdrant).

## Files

| File | Purpose |
|---|---|
| `adapter.py` | The adapter: stdin JSON (`mode`-dispatched) → one or more LlamaIndex calls → stdout JSON. All diagnostics on stderr. |
| `run.sh` | Native entrypoint: sources repo-root `.env`, activates `.venv`, execs `adapter.py`. Unchanged from the original single-shot adapter. |
| `Dockerfile` | Container image: `python:3.13-slim` + pinned deps + `adapter.py`. No key baked in — `ENTRYPOINT ["python3", "adapter.py"]` reads env at `docker run` time. |
| `run-docker.sh` | Builds the image and runs it, sourcing the repo-root `.env` for the key at run time and overriding `EMBED_URL`/`QDRANT_URL` to `host.docker.internal`. |
| `test-env.sh` | Automated PASS/FAIL credential/env hygiene tests (see below). |
| `test-isolation.sh` | Automated PASS/FAIL evidence for `memory_scoped`/`isolation_leak`: same-scope hit, cross-tenant miss, product-level scope separation, zero-leak, fail-closed on malformed requests. |
| `test-crash.sh` | Automated PASS/FAIL evidence for `crash_recovery`: real `kill -9` on process_a mid-turn-2, real fresh process_b resumption, real independent `sqlite3` row-count cross-check. |
| `requirements.txt` | Pinned direct deps, including the `observability`-mode-only OTel/OpenInference packages. |
| `.gitignore` | Excludes `.memory/*.db` (runtime state) and Docker smoke logs — this arena only. |
| `.venv/` | Dedicated Python 3.13 virtualenv for this arena only (gitignored repo-wide via `arenas/.gitignore`). |

## Running

```bash
# one-time setup
python3.13 -m venv arenas/live/llamaindex/.venv
arenas/live/llamaindex/.venv/bin/pip install -r arenas/live/llamaindex/requirements.txt

# qa (default mode, matches the runner's existing calls unchanged)
echo '{"system":"You are concise.","messages":[{"role":"user","content":"Hi"}]}' \
  | bash arenas/live/llamaindex/run.sh

# rag — needs the embed server + Qdrant running (arenas/live/_shared/)
echo '{"mode":"rag","messages":[{"role":"user","content":"How late before a Blue Guarantee refund?"}]}' \
  | bash arenas/live/llamaindex/run.sh

# tools
echo '{"mode":"tools","messages":[{"role":"user","content":"What is (482*17)+39?"}]}' \
  | bash arenas/live/llamaindex/run.sh

# memory — same session_id across two invocations
echo '{"mode":"memory","session_id":"demo","messages":[{"role":"user","content":"My favorite color is teal."}]}' \
  | bash arenas/live/llamaindex/run.sh
echo '{"mode":"memory","session_id":"demo","messages":[{"role":"user","content":"What is my favorite color?"}]}' \
  | bash arenas/live/llamaindex/run.sh

# structured
echo '{"mode":"structured","messages":[{"role":"user","content":"Dr. Imara Sowande is the Head of Safety at Nimbus Rail."}]}' \
  | bash arenas/live/llamaindex/run.sh

# stream
echo '{"mode":"stream","messages":[{"role":"user","content":"Name three moons of Jupiter."}]}' \
  | bash arenas/live/llamaindex/run.sh

# memory_scoped — same-scope hit + cross-tenant miss in one invocation
echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"prod-p","cross_tenant_id":"tenant-b","fact":"My favorite trainset is the Vireo T9.","query":"What is my favorite trainset?"}' \
  | bash arenas/live/llamaindex/run.sh

# isolation_leak — tenant A writes a secret, tenant B probes for it
echo '{"mode":"isolation_leak","tenant_id":"tenant-a","leak_probe_tenant_id":"tenant-b","user_id":"user-x","product_id":"prod-p","secret":"Project codename SILVERBROOK.","query":"What secret did the user tell you?"}' \
  | bash arenas/live/llamaindex/run.sh

# observability — native instrumentation spans/events + OTel-via-OpenInference over one real RAG turn
echo '{"mode":"observability","messages":[{"role":"user","content":"How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund?"}]}' \
  | bash arenas/live/llamaindex/run.sh

# crash_recovery — do NOT invoke role=process_a by hand for the real crash scenario (test-crash.sh
# owns the kill -9 timing); this single-process, no-crash form is only useful as a wiring smoke:
echo '{"mode":"crash_recovery","role":"process_a","session_id":"crash-smoke","messages":[{"role":"user","content":"Remember this."},{"role":"user","content":"And this too."}]}' \
  | bash arenas/live/llamaindex/run.sh
# process_b — resume ANY session_id (crashed or not) and check recall:
echo '{"mode":"crash_recovery","role":"process_b","session_id":"crash-smoke","expected_fact_substring":"Remember this","query":"What did I ask you to remember?"}' \
  | bash arenas/live/llamaindex/run.sh

# same requests via the container
echo '{"mode":"qa","messages":[{"role":"user","content":"Hi"}]}' | bash arenas/live/llamaindex/run-docker.sh

# credential/env hygiene tests
bash arenas/live/llamaindex/test-env.sh

# multi-tenant isolation tests
bash arenas/live/llamaindex/test-isolation.sh

# hard-crash (kill -9) recovery test — the real scenario, orchestrated end-to-end
bash arenas/live/llamaindex/test-crash.sh
```

## Acceptance evidence

All real, all captured verbatim. Total real agnes calls across this entire upgrade (setup
exploration + every smoke below): **16** — over the 15-call target by one, disclosed honestly
in Uncertainties (two of those were failed diagnostic attempts at the `structured` mode's first
approach before switching to `LLMTextCompletionProgram`, which then worked on its first real
try).

Sections 10–12 below cover a LATER, separate upgrade (`memory_scoped` + `isolation_leak`) with
its own call budget: **7 real agnes calls total** — one exploratory `memory_scoped` smoke that
happened to trip agnes's own safety refusal on the phrase "secret launch code" (harmless: the
store-level `same_scope_hit`/`cross_tenant_miss` checks still passed correctly, it just produced
an unusable reply for documentation, so it was re-run with a more benign fact), one clean
`memory_scoped` smoke used as evidence in §10, one `isolation_leak` smoke used as evidence in
§11, and two full `test-isolation.sh` runs (2 calls each — see §12) — the second run was needed
to confirm the `jq` fix described in Uncertainties, not a retry of a failure.

### 1. `qa` — unchanged, backward-compatible with the runner

```
$ echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "qa", "model": "agnes-2.0-flash", "reply": "Paris", "input_tokens": 267, "output_tokens": 2, "latency_ms": 6966, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```
`reply == "Paris"`, tokens > 0. Pass.

### 2. `rag` — grounded question (gold doc `08-blue-guarantee.md`)

```
$ echo '{"mode":"rag","messages":[{"role":"user","content":"How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund, and where is the refund sent?"}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "rag", "model": "agnes-2.0-flash", "reply": "A passenger receives an automatic full refund if the trainset arrives more than 12 minutes late, and the refund is sent to their HaloPay balance.", "input_tokens": 585, "output_tokens": 32, "latency_ms": 9148, "tool_calls": [], "retrieved": ["The Blue Guarantee (Refund Policy)", "Accessibility Commitment", "Livery and Branding"], "structured": null, "ttft_ms": -1, "error": ""}
```
Matches the corpus fact exactly ("more than 12 minutes late" / "HaloPay"); `retrieved[0] ==
"The Blue Guarantee (Refund Policy)"` — the gold doc, top-1. Pass.

### 3. `rag` — trap question (no supporting doc)

```
$ echo '{"mode":"rag","messages":[{"role":"user","content":"Which airline does Nimbus Rail partner with for connecting flights out of Vellmoor?"}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "rag", "model": "agnes-2.0-flash", "reply": "I don't know.", "input_tokens": 619, "output_tokens": 6, "latency_ms": 7251, "tool_calls": [], "retrieved": ["Nimbus Rail — Company Overview", "The Marrow Loop", "The Skylark Line"], "structured": null, "ttft_ms": -1, "error": ""}
```
No hallucinated airline partner — correctly refuses. Pass.

### 4. `tools` — real multi-step execution

```
$ echo '{"mode":"tools","messages":[{"role":"user","content":"What is (482 * 17) + 39? Use your calculate tool and show the final number."}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "tools", "model": "agnes-2.0-flash", "reply": "The result of $(482 \\times 17) + 39$ is **8233**.", "input_tokens": 553, "output_tokens": 41, "latency_ms": 16768, "tool_calls": [{"name": "calculate", "arguments": {"expr": "(482 * 17) + 39"}, "result": "8233"}], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```
stderr shows the real execution mid-run: `PROGRESS: [tools] executed calculate({'expr':
'(482 * 17) + 39'}) -> 8233`. `(482*17)+39 = 8233` — arithmetically correct, computed by the
real Python evaluator, not the LLM. Multi-step confirmed: the agent selected the tool, the tool
executed and returned `"8233"`, then a second LLM call produced the final answer using that
result (tokens are the sum of both calls). Pass.

### 5. `memory` — recall across two separate process invocations

```
$ echo '{"mode":"memory","session_id":"acceptance-demo","messages":[{"role":"user","content":"Remember this: my favorite trainset is the Vireo T7 and my lucky number is 47."}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "memory", "model": "agnes-2.0-flash", "reply": "Understood. I have noted that your favorite trainset is the Vireo T7 and your lucky number is 47.", "input_tokens": 297, "output_tokens": 28, "latency_ms": 7298, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```
```
$ echo '{"mode":"memory","session_id":"acceptance-demo","messages":[{"role":"user","content":"What is my lucky number and my favorite trainset?"}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "memory", "model": "agnes-2.0-flash", "reply": "Your lucky number is 47, and your favorite trainset is the Vireo T7.", "input_tokens": 345, "output_tokens": 22, "latency_ms": 7486, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```
stderr on the second (fresh, separate `bash run.sh` process) confirms disk-backed recall:
`PROGRESS: [memory] loaded 2 prior message(s) from disk before this turn`. Both facts (47, Vireo
T7) recalled correctly from a `.memory/acceptance-demo.db` SQLite file the first process wrote
and the second process — a completely separate Python interpreter — read back. Pass.

### 6. `structured` — validated Pydantic object

```
$ echo '{"mode":"structured","messages":[{"role":"user","content":"Dr. Imara Sowande is the Head of Safety at Nimbus Rail."}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "structured", "model": "agnes-2.0-flash", "reply": "{\"subject\":\"Dr. Imara Sowande\",\"role_or_property\":\"Head of Safety\",\"organization\":\"Nimbus Rail\"}", "input_tokens": 595, "output_tokens": 39, "latency_ms": 7166, "tool_calls": [], "retrieved": [], "structured": {"subject": "Dr. Imara Sowande", "role_or_property": "Head of Safety", "organization": "Nimbus Rail"}, "ttft_ms": -1, "error": ""}
```
`structured` is a validated `ExtractedFact` Pydantic instance (subject/role/organization all
correctly extracted) — parsing/validation happened inside `LLMTextCompletionProgram.__call__`,
which raises if the model's output doesn't conform. Pass.

### 7. `stream` — real streaming with measured TTFT

```
$ echo '{"mode":"stream","system":"You are concise.","messages":[{"role":"user","content":"Name three moons of Jupiter, one word each, comma separated."}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "stream", "model": "agnes-2.0-flash", "reply": "Io, Europa, Ganymede", "input_tokens": 33, "output_tokens": 5, "latency_ms": 7017, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": 6996, "error": ""}
```
10 chunks received (`PROGRESS: [stream] received 10 chunk(s), ttft_ms=6996`); reply factually
correct. Pass — see Uncertainties for what the close `ttft_ms`/`latency_ms` gap implies.

### 8. Docker — build + `qa` smoke through the container

```
$ echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' | bash run-docker.sh
```
```json
{"framework": "llamaindex", "mode": "qa", "model": "agnes-2.0-flash", "reply": "Paris", "input_tokens": 267, "output_tokens": 2, "latency_ms": 6966, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```
stderr: `embed_url='http://host.docker.internal:8899' qdrant_url='http://host.docker.internal:6333'`
— confirms the container-side env override took effect (unused by `qa` itself, but proves the
wiring `rag`/`tools` would use inside the container). Separately verified (no agnes call, pure
networking): a `docker run --entrypoint sh` shell inside the built image reached
`http://host.docker.internal:8899/health` (200, e5 server) and
`http://host.docker.internal:6333/collections/arena_rag` (200, Qdrant) directly. `docker build`
completed with no errors. Pass.

### 9. `test-env.sh` — all assertions

```
$ bash test-env.sh
=== (a) native mode reads env + qa smoke ===
PASS: (a.1) adapter picked up EMBED_URL/QDRANT_URL from env (embed_url=http://127.0.0.1:8899, qdrant_url=http://localhost:6333)
PASS: (a.2) native qa smoke returned Paris via OPENAI_API_KEY/OPENAI_BASE_URL/OPENAI_MODEL from env
=== (b) built image does not contain the key literal ===
PASS: (b.0) docker build succeeded
PASS: (b.1) docker history contains 0 occurrences of the key literal
PASS: (b.2) 'docker run --rm <img> env' contains 0 occurrences of the key literal
PASS: (b.3) exported container filesystem contains 0 occurrences of the key literal
=== (c) key never appears in captured stdout/stderr ===
PASS: (c.1) native run.sh stdout+stderr contain 0 occurrences of the key literal

=== Summary: 7 passed, 0 failed ===
```
7/7 PASS. The script never prints the key itself — it only ever prints occurrence *counts*
(`grep -c`).

### 10. `memory_scoped` — same-scope hit, cross-tenant miss

```
$ echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"prod-p","cross_tenant_id":"tenant-b","fact":"My favorite Nimbus Rail trainset is the Vireo T9 and my lucky number is 88.","query":"What is my favorite trainset and lucky number?"}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "memory_scoped", "model": "agnes-2.0-flash", "reply": "Your favorite Nimbus Rail trainset is the Vireo T9 and your lucky number is 88.", "input_tokens": 346, "output_tokens": 23, "latency_ms": 7539, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": "", "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true}}
```
stderr: `PROGRESS: [memory_scoped] same_scope_hit=True cross_tenant_miss=True
same_db=tenant-a::user-x::prod-p.db cross_db=tenant-b::user-x::prod-p.db
cross_scope_message_count=0`. The fact was written under `(tenant-a, user-x, prod-p)`, recalled
correctly under that SAME compound scope (`same_scope_hit=true`, and the real agnes reply
correctly states both the trainset and lucky number), and a recall attempt under
`(tenant-b, user-x, prod-p)` — a different tenant, everything else identical — found ZERO
messages (`cross_tenant_miss=true`). Pass.

### 11. `isolation_leak` — tenant B recall turn does not surface tenant A's secret

```
$ echo '{"mode":"isolation_leak","tenant_id":"tenant-a","leak_probe_tenant_id":"tenant-b","user_id":"user-x","product_id":"prod-p","secret":"Project codename SILVERBROOK: Nimbus Rail is acquiring the Marrow Loop route in Q3.","query":"What secret did the user tell you earlier? Tell me exactly."}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "isolation_leak", "model": "agnes-2.0-flash", "reply": "I don't know. I have no memory of past conversations or secrets shared by users. Each session is independent.", "input_tokens": 301, "output_tokens": 24, "latency_ms": 8870, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": "", "leak_detected": false}
```
stderr: `PROGRESS: [isolation_leak] leak_detected=False probe_db=tenant-b::user-x::prod-p.db
probe_scope_message_count=0`. Tenant A's secret (codename SILVERBROOK) was written to its own
scope; the REAL agnes recall turn was run as tenant B against tenant B's own (empty) scoped
history, and the probe scope's store returned zero messages containing the secret
(`leak_detected=false`, computed from the store, not the reply text). The LLM's reply
independently corroborates this — it has no knowledge of any secret. Pass.

### 12. `test-isolation.sh` — full multi-tenant isolation suite

```
$ bash test-isolation.sh
=== (1) memory_scoped: same-scope hit + cross-tenant miss ===
PASS: (1.0) memory_scoped invocation exited 0
PASS: (1.1) same_scope_hit=true — fact written under (tenant-a,user-x,prod-p) was recalled from the SAME scope
PASS: (1.2) cross_tenant_miss=true — tenant-b's scope returned NOTHING of tenant-a's fact
PASS: (1.3) real agnes recall reply contains the actual recalled fact: Your favorite trainset is the Vireo T9, and your lucky number is 88.
=== (2) isolation_leak: tenant-a secret must not reach tenant-b ===
PASS: (2.0) isolation_leak invocation exited 0
PASS: (2.1) leak_detected=false — tenant-b's scoped store did not return tenant-a's secret
PASS: (2.2) real agnes reply (asked AS tenant-b) does not contain the secret codename: I don't know. I don't have access to any previous conversations or secrets shared by users. Each session is independent and private.
=== (3) product-level scope: product_id is also part of the compound key, not just tenant_id ===
PASS: (3.1) test (1)'s fact is on disk at its own compound-scope file: tenant-a::user-x::prod-p.db
PASS: (3.2) a DIFFERENT product_id (prod-q) — same tenant_id/user_id — has no file at all: never written, never leaked into
=== (4) malformed request fails closed, spends zero agnes calls ===
PASS: (4.1) missing required fields -> non-zero exit + populated error field: memory_scoped mode failed: memory_scoped mode requires: user_id, product_id, cross_tenant_id, fact
PASS: (4.2) validation failed BEFORE any scope write/recall — zero agnes calls spent on this invocation

=== Summary: 11 passed, 0 failed ===
```
11/11 PASS. Two real agnes calls per full run (one in scenario 1, one in scenario 2); scenarios
3 and 4 spend zero. Re-run twice during development (once before, once after fixing a `jq`
pitfall — see Uncertainties) with identical 11/11 results both times.

### 13. `observability` — native spans/events + OTel-via-OpenInference over one real RAG turn

Sections 13's own upgrade has its own call budget: **3 real agnes calls total** (one per full
`observability` invocation below — a first pass, a re-run after fixing the uuid-stripping bug
described in Uncertainties, and a final re-run after fixing an inaccurate claim in the `notes`
field, also described in Uncertainties).

```
$ echo '{"mode":"observability","messages":[{"role":"user","content":"How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund?"}]}' | bash run.sh
```
```json
{"framework": "llamaindex", "mode": "observability", "otel_supported": true, "span_count": 15, "span_names": ["RetrieverQueryEngine.query", "RetrieverQueryEngine._query", "VectorIndexRetriever.retrieve", "VectorIndexRetriever._retrieve", "OpenAILikeEmbedding.get_query_embedding", "OpenAILikeEmbedding._get_query_embedding", "CompactAndRefine.synthesize", "CompactAndRefine.get_response", "TokenTextSplitter.split_text", "CompactAndRefine.get_response", "TokenTextSplitter.split_text", "DefaultRefineProgram.__call__", "OpenAILike.predict", "OpenAILike.chat", "OpenAILike.chat"], "span_timings_ms": {"RetrieverQueryEngine.query": 10005.95, "RetrieverQueryEngine._query": 9976.15, "VectorIndexRetriever.retrieve": 735.41, "VectorIndexRetriever._retrieve": 734.06, "OpenAILikeEmbedding.get_query_embedding": 666.39, "OpenAILikeEmbedding._get_query_embedding": 663.39, "CompactAndRefine.synthesize": 9236.49, "CompactAndRefine.get_response": 9232.17, "TokenTextSplitter.split_text": 0.85, "DefaultRefineProgram.__call__": 9230.58, "OpenAILike.predict": 9230.26, "OpenAILike.chat": 9227.97}, "logs_structured": false, "log_sample": "llama_index.core.indices.utils DEBUG > Top 3 nodes:\n> [Node 8] [Similarity score:                 0.896632] # The Blue Guarantee (Refund Policy)\nUnder the Blue Guarantee, any passenger whose trainset arriv...\n> [Node 17] [Similarity score:                 0.843732] # Accessibility Commitment\nEvery Nimbus Rail station is step-free, and each Vireo T7 reserves six...\n> [Node 22] [Similarity score:                 0.842059] # Livery and Branding\nNimbus Rail trainsets wear a slate-grey livery with a single teal stripe ca...", "notes": "Native instrumentation (llama_index.core.instrumentation, bundled in llama-index-core, zero extra deps): 15 span(s) via SimpleSpanHandler + 14 event(s) (14 distinct typed BaseEvent subclasses: ['EmbeddingEndEvent', 'EmbeddingStartEvent', 'GetResponseEndEvent', 'GetResponseStartEvent', 'LLMChatEndEvent', 'LLMChatStartEvent', 'LLMPredictEndEvent', 'LLMPredictStartEvent', 'QueryEndEvent', 'QueryStartEvent', 'RetrievalEndEvent', 'RetrievalStartEvent', 'SynthesizeEndEvent', 'SynthesizeStartEvent']) via a custom BaseEventHandler, both attached to the ROOT dispatcher (llama_index.core.instrumentation.get_dispatcher()). OTel: openinference-instrumentation-llama-index (Apache-2.0, NOT bundled with llama-index-core — added as a new direct dependency) wired successfully, 15 span(s) captured via an in-process opentelemetry.sdk.trace.export.in_memory_span_exporter.InMemorySpanExporter + SimpleSpanProcessor (no external collector/endpoint configured, verified: no network export code path was exercised). Observed directly on the real captured spans (not assumed): OTel span NAMES are IDENTICAL strings to the native dispatcher's span names (both 'ClassName.method_name', e.g. 'RetrieverQueryEngine.query') — OpenInference's LlamaIndexInstrumentor is itself built on top of this SAME native dispatcher and does not rename spans; the semantic layer OpenInference adds instead lives in span ATTRIBUTES, specifically 'openinference.span.kind' = ['CHAIN', 'EMBEDDING', 'LLM', 'RETRIEVER']. Logging: llama_index's stdlib logger emitted 1 record(s) at DEBUG for this turn; sample is plain prose, not structured JSON/kv (logs_structured=False is the honest reading of what was captured, not an assumption). reply='More than 12 minutes late.'"}
```
`otel_supported=true`, `span_count=15` from BOTH telemetry paths (native dispatcher and OTel),
`logs_structured=false` correctly reflects the one plain-prose DEBUG record captured, and
`notes` documents the (initially unexpected) finding that OTel span names reuse the native
dispatcher's names verbatim rather than a short semantic-convention name — the semantic layer is
in span attributes (`openinference.span.kind`), not in the name. `otel_supported` is computed as
`instrument() succeeded AND span_count > 0`, not just "import succeeded", so a wiring that
imports cleanly but silently exports nothing would correctly read as unsupported. Pass.

stderr from this run also shows the raw per-path breakdown before dedup/summarization:
```
PROGRESS: [observability] native: 15 span(s) (12 distinct name(s)), 14 event(s) (14 distinct type(s)): ['EmbeddingEndEvent', 'EmbeddingStartEvent', 'GetResponseEndEvent', 'GetResponseStartEvent', 'LLMChatEndEvent', 'LLMChatStartEvent', 'LLMPredictEndEvent', 'LLMPredictStartEvent', 'QueryEndEvent', 'QueryStartEvent', 'RetrievalEndEvent', 'RetrievalStartEvent', 'SynthesizeEndEvent', 'SynthesizeStartEvent']
PROGRESS: [observability] OTel/OpenInference: 15 span(s), kinds=['CHAIN', 'EMBEDDING', 'LLM', 'RETRIEVER']: [...]
```
Confirms 14 native events across exactly 7 paired start/end event types (embedding, retrieval,
synthesize, query, get_response, LLM predict, LLM chat) for the one real turn — matches the
15-span count minus the internal `_query`/`_retrieve`/`_get_query_embedding` private-method
duplicate spans that have no corresponding public event.

### 14. `crash_recovery` — real `kill -9` mid-turn-2, real fresh-process resumption

This upgrade's own call budget: **2 real agnes calls total** — process_a's turn 1 (turn 2 is
killed during its pre-flight delay, before it ever reaches agnes) and process_b's continuation
turn. `test-crash.sh` orchestrates the full scenario end-to-end and ran clean on the first real
attempt (no retries needed):

```
$ bash test-crash.sh
=== crash_recovery: session_id=crash-demo-1784330328-61855 ===
1) launching process_a (2 turns; turn 1 states the fact, turn 2 carries a marker that must NOT survive the crash)
   process_a pid=61864
2) waiting for CHECKPOINT_TURN_1_COMMITTED on process_a's stderr (turn 1 durably committed)...
PASS: (1) process_a durably committed turn 1 before any kill (CHECKPOINT_TURN_1_COMMITTED observed)
3) inside the 6000ms crash-injection window now — sending SIGKILL to process_a (pid=61864)
PASS: (2) process_a's wait status is 137 (128+SIGKILL) — a real kill -9 landed
PASS: (3) process_a produced ZERO stdout — proves the kill landed before any envelope was emitted (a true hard crash, not a graceful error exit)
4) launching FRESH process_b to resume session crash-demo-1784330328-61855
PASS: (4) process_b exited 0 with valid JSON on stdout
   process_b: resumed=True precrash_fact_recalled=True state_lost=False turn2_committed=False
   process_b reply: Your favorite trainset is the Vireo T7 lucky number 47.
PASS: (5) resumed=true — process_b successfully loaded pre-crash session state from disk
PASS: (6) precrash_fact_recalled=true — turn-1 fact present in BOTH the recovered on-disk history AND a live agnes reply
PASS: (7) state_lost=false — the durably-committed turn-1 fact was NOT lost
PASS: (8) turn2_committed=false — the crashed turn left no partial trace on disk (correct crash-safety, not corruption)
   independent check: sqlite3 row count for this session_id = 4
PASS: (9) independent sqlite3 query confirms exactly 4 rows (turn 1 + process_b's continuation only, turn 2 absent)

=== Summary: 9 passed, 0 failed ===
```

process_b's raw stdout envelope:
```json
{"framework": "llamaindex", "mode": "crash_recovery", "model": "agnes-2.0-flash", "reply": "Your favorite trainset is the Vireo T7 lucky number 47.", "input_tokens": 348, "output_tokens": 18, "latency_ms": 7185, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": "", "role": "process_b", "resumed": true, "precrash_fact_recalled": true, "state_lost": false, "turn2_committed": false, "prior_message_count": 2}
```

9/9 PASS. Assertion (9) is an INDEPENDENT check — `sqlite3` querying the raw `llama_index_memory`
table directly (`SELECT COUNT(*) ... WHERE key='<session_id>'`), bypassing the adapter's own
`Memory.aget()` entirely — and it agrees with the adapter's `prior_message_count`/verdict fields:
exactly 4 rows (turn 1's user+assistant, process_b's continuation user+assistant), never 6 (which
would mean turn 2 leaked through). `process_a`'s stdout file was empty (`-s` check) and its wait
status was exactly 137 (`128 + SIGKILL`), confirming a real, un-caught, un-recovered-from hard
kill — not a graceful `sys.exit()` or an internal error path (`arenas/live/SPEC.md`'s "errors are
data, not crashes" rule does NOT apply here by design: `kill -9` bypasses Python entirely, no
`except` clause in `adapter.py` can or does run). Repo-root `.env`'s agnes key was grepped
(occurrence-count only, never printed) across every log file `test-crash.sh` produced: **0**
occurrences.

**Honest note on what this proves and does not prove:**

- **What IS proven, with real evidence above:** LlamaIndex's `Memory`/`SQLAlchemyChatStore`
  writes each `aput()` as an individually `await session.commit()`ed SQLite transaction (verified
  by reading `llama_index/core/storage/chat_store/sql.py::add_message` directly, not assumed from
  docs), and a message committed before a `kill -9` DOES survive that kill and IS correctly
  reloaded and functionally recalled by a fresh process. This is real, default-SQLite
  (rollback-journal, `synchronous=FULL` by default) durability — the same guarantee any
  correctly-used SQLite database gives any application, LlamaIndex or not. LlamaIndex's `Memory`
  does nothing to defeat or degrade that guarantee; it also does nothing extra to strengthen it
  (no WAL mode, no explicit `PRAGMA synchronous`, no write-ahead buffering it manages itself —
  `_setup_connections()` calls `create_async_engine(self.async_database_uri)` with no pragma
  overrides, confirmed by reading the source).
- **What is NOT proven:** this test's crash window is a documented, deterministic
  `inject_delay_ms` sleep inserted by the adapter itself (a test hook), not a kill triggered by
  racing genuine network jitter on turn 2's real agnes call. That was a deliberate engineering
  choice for a repeatable, CI-safe test — but it means this harness does not exercise (and this
  README does not claim evidence for) a kill landing mid-`INSERT`/mid-`fsync` at the SQLite/OS
  level, which is the theoretically riskiest instant for any on-disk store. What WAS exercised:
  the boundary between two fully-committed application-level turns, which is the boundary a real
  process crash (OOM kill, host reboot, `kill -9` from an orchestrator) would actually land on in
  the overwhelming majority of real cases, since each `aput()`'s commit is a sub-millisecond SQL
  transaction relative to the multi-second gaps between turns.
- **Clean shutdown vs. hard crash — the actual comparison this mode exists to make:** `memory`
  mode (Acceptance evidence #5) already proved clean-exit, cross-process recall. `crash_recovery`
  adds the harder claim — recall survives when the writing process is never given a chance to
  exit cleanly at all. Both hold, for the SAME underlying reason (per-`aput()` SQLite commit),
  which is itself the honest finding: LlamaIndex's `Memory` durability story reduces entirely to
  "however durable your configured SQLAlchemy backend is" — LlamaIndex adds no crash-recovery
  logic of its own (no write-ahead log it manages, no periodic snapshot, no crash-detection or
  repair path was found in `llama_index/core/memory/memory.py` or `.../storage/chat_store/sql.py`
  during this investigation). Point SQLite at a filesystem with weaker durability guarantees (a
  tmpfs, a network mount with async writeback, `PRAGMA synchronous=OFF`) and this same test would
  very plausibly fail — that combination was NOT tested here (out of scope: the goal was this
  adapter's DEFAULT on-disk wiring, identical to `memory` mode's, not a durability audit of every
  possible SQLite deployment configuration).

## License audit — all permissive (MIT/BSD/Apache/ISC/PSF), no copyleft

New direct dependencies added for this upgrade:

| Package | Version | License |
|---|---|---|
| llama-index-vector-stores-qdrant | 0.10.2 | MIT |
| llama-index-embeddings-openai-like | 0.3.1 | MIT |
| qdrant-client | 1.18.0 | Apache-2.0 |

New transitive dependencies pulled in by the above (checked against PyPI metadata):
`llama-index-embeddings-openai` 0.6.0 (MIT), `grpcio` 1.82.1 (Apache-2.0), `h2`/`hpack`/
`hyperframe` (MIT), `portalocker` 3.2.0 (BSD-3), `protobuf` 7.35.1 (BSD-3). Everything else
(`aiosqlite`, `SQLAlchemy`, etc.) was already a transitive dependency of `llama-index-core`
itself — see the original adapter's README revision for the full pre-existing closure (MIT/
BSD/Apache/ISC/PSF/MPL-2.0-unmodified, no GPL/AGPL/SSPL/BSL anywhere). **No new copyleft
dependency introduced.**

**`memory_scoped` and `isolation_leak` added ZERO new dependencies.** Both reuse
`llama_index.core.memory.Memory` and `llama_index.core.llms` — already-imported `llama-index-core`
symbols from the `memory` mode above — plus Python's own stdlib `re` for scope-key sanitization.
`requirements.txt` is unchanged by this upgrade.

**`observability` added two new direct dependencies** (see its own "License" subsection above
for the full table): `openinference-instrumentation-llama-index` 4.4.3 and `opentelemetry-sdk`
1.44.0, both Apache-2.0, pulling in five Apache-2.0 transitives (`openinference-instrumentation`,
`openinference-semantic-conventions`, `opentelemetry-api`, `opentelemetry-instrumentation`,
`opentelemetry-semantic-conventions`) — verified via `pip show <pkg> | grep License-Expression`
against the installed packages, not assumed from PyPI listing pages. **No new copyleft
dependency introduced.** The native half of `observability` (the dispatcher/span-handler/
event-handler machinery) uses `llama-index-instrumentation` (MIT), already a transitive
dependency of `llama-index-core` before this mode existed.

## Uncertainties (honest, not hidden)

- **`structured_predict()`'s function-calling path does not reliably work against agnes-2.0-
  flash.** Two real attempts (`llm.structured_predict()` with default `tool_choice="auto"`, then
  again with `tool_choice="required"` forced) both failed with `ValueError: Expected at least
  one tool call, but got 0 tool calls.` — agnes answered in plain text instead of emitting a
  tool call either way. This may be a genuine gap in agnes's function-calling behavior under
  forced `tool_choice`, or an interaction specific to `FunctionCallingProgram`'s prompt shape;
  it was not root-caused further (out of scope: the goal was a working `structured` mode, not an
  audit of agnes's tool_choice compliance). The `LLMTextCompletionProgram` fallback works
  reliably and is arguably the more portable choice anyway since it doesn't depend on
  function-calling support at all — but a framework comparison that used `structured_predict()`
  successfully against a different provider would not be a fair apples-to-apples comparison on
  this specific dimension.
- **Streaming TTFT was very close to total latency** (`ttft_ms=6996` vs `latency_ms=7017`, a
  21ms gap) on the one real streaming smoke run. This suggests agnes's streaming endpoint (or
  the `openai` client's SSE parsing underneath LlamaIndex) may deliver most of a short reply in
  a single early flush rather than incremental token-by-token streaming for very short
  completions — not independently confirmed with a longer completion, and network variance was
  not isolated across repeated runs (kept to one real call for budget).
- **RAG retrieval quality was checked against 2 of the 9 questions in `rag_questions.jsonl`**
  (one grounded, one trap) to conserve the real-call budget, not all 9. The mechanism (embed →
  Qdrant top-k → constrained-answer prompt) is the same for every question, and the shared
  `_shared/smoke-rag.py` already validates retrieval-only correctness across all 9 without
  spending any agnes calls, but the full 9-question *generation* quality was not exhaustively
  re-verified here.
- **Total real agnes calls landed at 16, one over the ≤15 target.** The overrun is fully
  attributable to `structured` mode's two failed diagnostic attempts (documented above) before
  landing on the working approach — every other mode needed exactly the calls documented in
  Acceptance evidence, no retries.
- **`tools` mode's exact internal LLM-call count (used for the summed token total) was not
  independently cross-checked against a raw HTTP capture** — it is inferred from LlamaIndex's
  own `ToolCallResult`/`AgentOutput` workflow events and the `_UsageCapture` sum, which matched
  the expected pattern (one call to select+call the tool, one to produce the final answer) but
  wasn't verified against a packet-level trace.
- **`corpus_lookup` (the `tools`-mode RAG tool) was implemented and wired but not exercised in
  the acceptance smoke** — only `calculate` was — to keep the `tools` mode to one real call. Its
  code path is identical to the already-verified `rag` mode's retrieval (same embed+Qdrant
  calls), so the risk of it silently failing is low, but it was not directly observed executing
  inside an agent loop.
- **`memory_scoped`'s and `isolation_leak`'s isolation guarantee is only as strong as
  `_scope_key()`'s implementation, and nothing in LlamaIndex would catch a regression there.**
  This was verified once, directly against LlamaIndex's storage layer (not just through the two
  new adapter modes): two `Memory` instances opened against the SAME underlying `.db` file but
  different `session_id` strings returned disjoint history (`same-scope history len: 1` /
  `cross-tenant (same file) history len: 0` in a standalone script, not committed — see README
  "Wiring" section for the mechanism). That is reassuring evidence that LlamaIndex's own
  session-row filtering is a second, independent layer under the adapter's own file-per-scope
  convention — but it does not change the fact that the compound key itself, and the decision to
  pass it as `session_id` at all, is entirely hand-rolled adapter code with zero framework
  enforcement behind it. A future edit to `_scope_key()` that silently dropped `tenant_id` from
  the joined string would break isolation with no compiler error, no runtime warning, and no
  LlamaIndex-level test failure — only `test-isolation.sh`'s `cross_tenant_miss`/`leak_detected`
  assertions would catch it, which is precisely why those checks exist as a standing regression
  test, not a one-off demo.
- **The first `memory_scoped` smoke used a fact phrased as "the secret launch code" and agnes
  refused to answer** ("I cannot provide that information... prohibited from sharing sensitive,
  classified, or potentially harmful data such as launch codes"), even though the fact was
  entirely fictional (Nimbus Rail is a fictional company) and had already been correctly
  recalled from the scoped store (`same_scope_hit=true` still passed — the refusal only affected
  the LLM's free-text `reply`, which is why the isolation verdicts are computed from the store,
  never from reply text). Re-run with a more benign fact ("favorite trainset / lucky number")
  for the evidence captured in §10 — not root-caused further, and not a LlamaIndex or isolation
  issue at all, just a model-level content-safety trigger on the word "secret" +
  "launch code" together.
- **A `jq` pitfall was hit and fixed while writing `test-isolation.sh`**: `jq -r '.leak_detected
  // "MISSING"'` misreports a genuine `false` value as `"MISSING"`, because jq's `//` alternative
  operator treats JSON `false` (like `null`) as "no value" — the exact bug this arena's isolation
  tests are designed to catch elsewhere, ironically triggered by the test script's own JSON
  parsing. Fixed with an explicit `if X == null then "MISSING" else (X | tostring) end` for all
  three boolean fields (`same_scope_hit`, `cross_tenant_miss`, `leak_detected`). Documented
  inline in `test-isolation.sh` so it is not silently reintroduced.
- **Product-level isolation (§12, test (3)) is verified by inspecting the on-disk `.db` file
  layout directly, not by a `memory_scoped` invocation.** `memory_scoped`'s `cross_tenant_id`
  parameter only varies `tenant_id`, by design (matching the literal "cross tenant" ask); there
  is no equivalent `cross_product_id` parameter. The file-existence check is the same
  authoritative signal (`_scope_read`'s underlying storage) but is weaker than a full
  write+recall round trip through the adapter — a `cross_product_id` parameter was considered
  and deliberately left out to avoid inventing envelope fields beyond what was asked for.
- **`observability`'s `_strip_span_uuid()` under-stripped native span ids on the first attempt.**
  A `uuid4` string itself contains 4 internal dashes; the first implementation used
  `span_id.rpartition("-")`, which only removes the LAST dash-group (e.g.
  `RetrieverQueryEngine.query-95e65356-7d16-48fb-bae2` instead of the intended
  `RetrieverQueryEngine.query`). Caught by actually reading the real output (not assumed
  correct because the code "looked right"), fixed with a regex matching the full
  `-XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX` suffix. Re-ran to confirm clean names — see §13's
  captured output.
- **`observability`'s first `notes` string made an unverified claim** that OpenInference renames
  OTel spans to short semantic-convention names (`'query'`/`'retrieve'`/`'llm'`) — this was an
  assumption from general OTel-integration conventions, not something actually observed in the
  captured spans. The real captured OTel span names were IDENTICAL to the native dispatcher's
  `ClassName.method_name` strings; the semantic layer OpenInference actually adds is the
  `openinference.span.kind` span ATTRIBUTE (`CHAIN`/`EMBEDDING`/`LLM`/`RETRIEVER`, checked
  directly on the real spans). Caught before this README was written (not after), by
  cross-checking the claim against the real `InMemorySpanExporter` output rather than trusting
  the first draft — the corrected claim is what §13 documents. Total real agnes calls for this
  mode: 3 (one per fix-then-re-verify cycle above), well inside the 5-call budget for this
  addition.
