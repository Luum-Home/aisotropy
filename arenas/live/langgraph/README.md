# LangGraph — Live Arena Adapter (multi-mode)

A LIVE arena adapter for [LangGraph](https://github.com/langchain-ai/langgraph) / [LangChain](https://github.com/langchain-ai/langchain)
that drives **eleven modes**, each exercising a different LangGraph/LangChain-native capability
against **real infrastructure**: the real OpenAI-compatible model ("agnes-2.0-flash"), a real e5
embeddings server, a real Qdrant collection, a real on-disk checkpointer, and (for the scoped-memory
modes) a hand-rolled on-disk compound-scope fact store. This directory is fully additive and does
not touch the pre-existing `arenas/langgraph/` arena (a separate, unrelated benchmark harness), the
base single-mode contract in `arenas/live/SPEC.md`, or anything outside `arenas/live/langgraph/`.

The original single-shot adapter (`qa` mode) is unchanged in behavior; everything else is new.

**Every mode can also run fully offline**: setting `ARENA_STUB=1` swaps the real `ChatOpenAI`/agnes
call and any on-disk LangGraph checkpointer for real, in-process LangChain/LangGraph stand-ins — no
API key, no network, no `.data/*.sqlite` file. See "Offline mode — `ARENA_STUB=1`" below.

## Multi-tenant honesty (read this before using `memory_scoped` / `isolation_leak`)

**LangGraph provides no native multi-tenant memory isolation.** Its own checkpointer
(`langgraph.checkpoint.*`, used by `memory` mode below) keys state by a single caller-supplied
`thread_id` string — there is no tenant/user/product dimension anywhere in the framework, no
key-prefix validation, and nothing that stops one thread's state from being read under a different
thread id if the caller passes the wrong string.

`memory_scoped` and `isolation_leak` (both new) hand-roll a compound scope key
`(tenant_id, user_id, product_id[, session_id])` over a small adapter-owned SQLite store
(`modes/scoped_store.py`) — LangGraph does nothing for you here. **The dev is responsible for
getting this right; a keying mistake (forgetting `tenant_id`, using a collidable delimiter, reusing
a store across scopes without checking) is a cross-tenant memory leak, not a framework bug.**

Contrast: aisotropy enforces this by construction — `tenant_id` travels exclusively via
`context.Context` injected once at the Huma handler boundary (never trusted from body/header/query),
and Qdrant semantic-memory reads use payload filtering **during** the HNSW scan (not a post-filter,
which is an isolation leak, not a fix) — see `.claude/rules/constitutional-gates.md` ->
"Multi-tenant data isolation" in this repo. LangGraph gives an app developer the primitives to build
that; it does not give them the guarantee.

## Real infra wiring

Configuration comes entirely from environment variables — never hardcoded:

| Env var | Purpose | Default |
|---|---|---|
| `OPENAI_API_KEY` | Bearer credential for the agnes OpenAI-compatible endpoint. Never logged, printed, or written to any file by this adapter. | *(required, from repo-root `.env`)* |
| `OPENAI_BASE_URL` | Base URL of the OpenAI-compatible chat endpoint. | `https://apihub.agnes-ai.com/v1` in this repo |
| `OPENAI_MODEL` | Chat model id. Also the literal `model` field in the response envelope. | `agnes-2.0-flash` |
| `EMBED_URL` | Base URL of the real OpenAI-compatible e5 embeddings server (`arenas/live/_shared/embed-server`), used by `rag` mode. | `http://127.0.0.1:8899` |
| `EMBED_API_KEY` | Dummy credential for the embed server (it does not check auth; `OpenAIEmbeddings` just requires a non-empty string). Never a real secret. | `not-needed` |
| `QDRANT_URL` | Base URL of the real Qdrant instance, used by `rag` mode. | `http://localhost:6333` |
| `QDRANT_COLLECTION` | Qdrant collection populated by `arenas/live/_shared/populate-qdrant.py` (22 Nimbus-Rail corpus docs). | `arena_rag` |
| `CHECKPOINT_DB_PATH` | On-disk SQLite path for `memory` mode's LangGraph checkpointer. | `<this dir>/.data/checkpoints.sqlite` |
| `CRASH_CHECKPOINT_DB_PATH` | On-disk SQLite path for `crash_recovery` mode's LangGraph checkpointer. A SEPARATE file from `CHECKPOINT_DB_PATH` so a hard-crash test never touches `memory` mode's own checkpoint history. | `<this dir>/.data/checkpoints_crash.sqlite` |
| `SCOPED_STORE_DB_PATH` | On-disk SQLite path for the hand-rolled compound-scope fact store (`modes/scoped_store.py`), used by `memory_scoped` and `isolation_leak`. Independent of `CHECKPOINT_DB_PATH` — this store has nothing to do with LangGraph's own checkpointer. | `<this dir>/.data/scoped_store.sqlite` |

`OPENAI_*` are read from the repo-root `.env` (never committed, never modified by this adapter).
`run.sh` sources it automatically:

```bash
set -a; source <repo-root>/.env; set +a
```

`EMBED_URL`/`QDRANT_URL` are environment-configurable specifically so the same image works
unmodified in Docker, where `localhost`/`127.0.0.1` would resolve to the *container* instead of the
host running the real embed server and Qdrant — see "Docker" below.

## Offline mode — `ARENA_STUB=1`

Setting `ARENA_STUB=1` makes **every mode** runnable with zero real network calls and zero real
credentials — useful for CI/import smoke checks that shouldn't spend a real agnes call or depend on
`OPENAI_API_KEY`/`.env` being present at all:

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"hi"}]}' \
  | ARENA_STUB=1 bash arenas/live/langgraph/run.sh
```

Two seams in `modes/common.py` are what every mode actually routes through — this is centralized,
not a per-mode reimplementation:

| Real (default) | `ARENA_STUB=1` | Seam |
|---|---|---|
| `langchain_openai.ChatOpenAI` against the real agnes endpoint | `langchain_core.language_models.fake_chat_models.FakeListChatModel` — a REAL LangChain-native fake chat model (cycles a fixed list of canned string responses), never a hand-rolled mock. Subclassed only to override `bind_tools()` as a no-op passthrough (`FakeListChatModel` inherits `BaseChatModel`'s default `bind_tools()`, which raises `NotImplementedError` — verified against this arena's pinned `langchain-core==1.4.9`), so tool-binding modes (`tools`, `observability`, `middleware`) stay runnable offline. The stub never simulates real tool-call reasoning — `tool_calls` is empty under the stub even for those modes; this is documented, not fabricated. | `common.build_chat_model()` |
| `langgraph.checkpoint.sqlite.SqliteSaver` against an on-disk file | `langgraph.checkpoint.memory.InMemorySaver` — LangGraph's own real in-memory checkpointer, not a hand-rolled dict. No `.data/*.sqlite` file is created or touched. Does **not** preserve state across separate OS processes (that cross-process guarantee is the entire point of `memory`/`crash_recovery` in the real path) — the stub only proves those modes' code paths run end-to-end offline. | `common.checkpointer_context()` |

Two modes have their own additional stub-specific fallback, documented inline where they diverge
from "swap the model, keep the same code path":

- **`rag`**: no real embed-server/Qdrant HTTP call under the stub — `retrieve()` returns a fixed
  two-document canned `context` list in the exact shape a real retrieval hit has (see
  `modes/rag.py`'s `_stub_retrieve()`), so `generate()` and the envelope's `retrieved` field are
  otherwise unaffected by which path ran.
- **`structured`**: `FakeListChatModel` inherits `BaseChatModel`'s default
  `with_structured_output()`, which also raises `NotImplementedError` (it builds on top of
  `bind_tools()`/native structured-output support neither of which the stub implements). Under the
  stub, `modes/structured.py` invokes the stub plainly and best-effort JSON-parses its canned reply
  instead of exercising the real `method="json_schema"` native path — an honest, documented
  reduction in fidelity, not a claim that real provider-side schema validation occurred.

`memory_scoped` and `isolation_leak`'s hand-rolled `ScopedStore` (`modes/scoped_store.py`) was
already 100% local/on-disk SQLite with no dependency on Qdrant/rqlite/any external service, so
`ARENA_STUB` does not change it — it was offline by construction from the start.

`crash_recovery` is worth calling out explicitly: its entire point is proving survival of a real
`kill -9`, which is meaningless against an in-memory checkpointer (there is nothing on disk to
survive a kill). Under `ARENA_STUB=1` this mode's `op=converse`/`op=resume` code paths still run
end-to-end without error — useful for a smoke/import check — but the crash-durability property
itself does not, and is not claimed to, hold in stub mode.

## How to run (native)

```bash
# One-time setup
cd arenas/live/langgraph
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt

# Run a turn (from the repo root)
set -a; source .env; set +a
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' \
  | bash arenas/live/langgraph/run.sh
```

`run.sh` sources the repo-root `.env`, activates this arena's dedicated `.venv`, pipes stdin
through to `adapter.py` unchanged, and passes stdout through unchanged. It does not touch any
other venv or the global Python environment. `run.sh` itself is unchanged from the original
single-mode adapter — only `adapter.py` (now a thin dispatcher) and the new `modes/` package
changed.

## Multi-mode JSON contract

### Request (stdin, one JSON object)

```json
{
  "mode": "qa | rag | tools | memory | crash_recovery | structured | stream | memory_scoped | isolation_leak | observability | middleware",
  "system": "string",
  "messages": [{"role": "user" | "assistant", "content": "string"}],
  "tools": [{"name": "string", "description": "string", "parameters": {"...": "JSON Schema"}}]
}
```

`mode` defaults to `"qa"` when absent, so any caller still speaking the original base contract
(`arenas/live/SPEC.md`) keeps working unmodified. Mode-specific extra fields are documented per
mode below.

### Response (stdout, exactly one JSON object, nothing else on stdout)

```json
{
  "framework": "langgraph",
  "mode": "string",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}}],
  "retrieved": [{"doc_id": "string", "title": "string", "score": 0.0}],
  "structured": null,
  "ttft_ms": 0,
  "scoped_recall": null,
  "leak_detected": null,
  "error": "string"
}
```

Shared rules (all modes):

- `input_tokens`/`output_tokens`/`ttft_ms` are **provider-reported only**; `-1` means genuinely
  unavailable/not measured for that mode, never a fabricated number.
- `retrieved` is `[]` and `structured` is `null` for modes that don't produce them.
- `scoped_recall` (`{"same_scope_hit": bool, "cross_tenant_miss": bool | null}`) is populated only
  by `memory_scoped`; `null` for every other mode.
- `leak_detected` (`bool`) is populated only by `isolation_leak`; `null` for every other mode.
- All diagnostics (`PROGRESS:` lines, tracebacks) go to **stderr**; stdout carries exactly one JSON
  object.
- On any error, the error is caught, described in `error`, and a valid envelope is still emitted
  (exit code `0`) — errors are data, not crashes, per the base contract.

## Modes

### `qa` (unchanged base contract)

One `StateGraph(MessagesState)`, one `agent` node, `START -> agent -> END`. If `tools` are present
they are bound via `bind_tools()` and a tool call is captured **single-shot, never executed** — the
original base-contract behavior (SPEC.md rule 2). For real tool execution, use `tools` mode.

### `rag` (real retrieval-augmented generation)

A two-node LangGraph retrieval graph, `START -> retrieve -> generate -> END`:

- **retrieve**: embeds the last user message via `langchain_openai.OpenAIEmbeddings(base_url=EMBED_URL,
  model="multilingual-e5-small-query")` against the real embed server, then
  `langchain_qdrant.QdrantVectorStore.similarity_search_with_score(...)` against the real Qdrant
  `arena_rag` collection (22 Nimbus-Rail docs). `top_k` is configurable via the request (`"top_k"`,
  default 3).
- **generate**: calls agnes with a hard-enforced system instruction — "answer only from context,
  else say you don't know" — that is appended regardless of any caller-supplied `system`, so the
  grounding contract cannot be silently dropped.

`retrieved` is filled with `{doc_id, title, score}` for every retrieved chunk, naming the gold doc.

One quirk worth documenting (see "Gotchas" below): `QdrantVectorStore`'s `Document.metadata` only
comes from a nested `metadata_payload_key` (default `"metadata"`), which does not exist in this
collection's flat `{doc_id, title, text}` payload — so after the vector search, the adapter does one
extra `vector_store.client.retrieve(...)` by point id to recover `doc_id`/`title` for the `retrieved`
field. This is a small supplementary payload read, not a bypass of the vector search itself (the
embedding + Qdrant HNSW search is 100% real, done through `QdrantVectorStore`).

### `tools` (real multi-step tool execution)

Unlike `qa` mode's single-shot capture, this builds the classic ReAct loop with LangGraph's own
primitives — `StateGraph` + `ToolNode` + `tools_condition` (the same building blocks
`langgraph.prebuilt.create_react_agent` composes internally, used directly here to avoid that
helper's V1.0 deprecation warning): `agent -> (tools_condition) -> tools -> agent -> ... -> END`.

The tool offered is `calculate(expression: str)` — arithmetic evaluated through a restricted
`ast.parse(..., mode="eval")` walk over an allowlist of numeric/operator node types, **never**
Python's `eval`/`exec`, so an untrusted model-produced expression cannot execute arbitrary code.

`tool_calls` reports every call **actually executed**, each with its real result:
`{"name": "calculate", "arguments": {"expression": "..."}, "result": "8585"}`. `input_tokens`/
`output_tokens` are the **sum** of provider usage across every real model call in the loop (at least
two: the call that decides to invoke the tool, and the call that uses the tool's result to answer).

### `memory` (real cross-process checkpointer persistence)

Each adapter invocation is one process, one stdin/stdout turn. To prove real persistence *across*
processes (not just in-memory state within one), this mode uses
`langgraph.checkpoint.sqlite.SqliteSaver` against an on-disk file (`CHECKPOINT_DB_PATH`) keyed by a
caller-supplied `thread_id`. Two separate `run.sh` invocations that reuse the same `thread_id` share
the same checkpointed message history even though they are different OS processes.

**Contract addition for this mode only**: the request JSON MUST include a non-empty string
`"thread_id"`. `"messages"` should contain only the **new** turn(s) since the last checkpoint for
that thread — prior history is recovered from the checkpoint file, not replayed by the caller (that
is the entire point of the mode). Example — two separate shell invocations, same thread:

```bash
echo '{"mode":"memory","thread_id":"t1","messages":[{"role":"user","content":"Remember: my favorite line is the Skylark Line. Say OK."}]}' | bash run.sh
echo '{"mode":"memory","thread_id":"t1","messages":[{"role":"user","content":"What is my favorite line? One word."}]}' | bash run.sh
```

### `crash_recovery` (does the checkpointer survive a HARD `kill -9`, not just a clean exit?)

**Honest framing, read first**: `memory` mode above proves recall across two *cleanly exiting*
processes — each `run.sh` invocation reads stdin, finishes its work, and exits normally. That is a
meaningfully weaker claim than "this survives a crash." `crash_recovery` mode exists to answer the
harder, distinct question directly: **if the process is SIGKILL'd mid-flight — not given any chance
to run cleanup code, flush buffers, or close connections gracefully — does the checkpoint written
*before* the kill still survive, and can a brand-new process resume from it?**

Two ops, dispatched by the request's `"op"` field (see `modes/crash_recovery.py` for the full
contract docstring):

- **`"op":"converse"`** (process A — the one meant to be killed): drives a short multi-turn
  conversation on one `thread_id` from a list of plain-string `"turns"`. Each turn opens its OWN
  `SqliteSaver.from_conn_string(...)` context manager, so turn *N*'s checkpoint is durably committed
  (block closed, file flushed) strictly BEFORE turn *N+1* starts. It logs
  `PROGRESS: crash_recovery converse turn N/M checkpointed` to stderr right after each commit — the
  synchronization signal `test-crash.sh` polls for before sending `kill -9`.
- **`"op":"resume"`** (process B — the fresh, un-killed one; also the default, so a plain
  `{"mode":"crash_recovery","thread_id":...,"messages":[...]}` request behaves exactly like `memory`
  mode): a brand-new process opens the SAME on-disk file for the SAME `thread_id` and answers one new
  turn, with zero in-memory knowledge of process A.

**Contract additions**: `"thread_id"` (non-empty string) is always required. `op="converse"` also
requires a non-empty list of non-empty strings `"turns"`. `op="resume"` (default) requires
`"messages"` exactly like `memory` mode.

### `structured` (native structured output)

`ChatOpenAI.with_structured_output(schema, method="json_schema", include_raw=True)` inside a
one-node LangGraph graph. `method="json_schema"` uses the provider's native structured-output
machinery (not a prompt-only convention); `include_raw=True` keeps the underlying `AIMessage` around
so real provider token usage is still available (the plain `.invoke()` shortcut discards it, which
would force fabricating token counts — forbidden by the base contract).

The request may supply its own `"schema"` (a JSON Schema object); if omitted, a small default
`{answer: string, confidence: number}` schema is used. The validated object is returned in the
envelope's `structured` field; on a schema-validation failure, `error` explains why and `structured`
stays `null`.

### `stream` (real token streaming, ttft_ms)

`graph.stream({"messages": messages}, stream_mode="messages")` — LangGraph's message-level streaming
mode, which yields real provider token chunks as `ChatOpenAI` streams them (not a post-hoc chunking
of an already-complete response). `ttft_ms` is measured from just before `graph.stream(...)` is
called to the very first chunk the generator actually yields — a genuine time-to-first-token number
for the real network call. `stream_usage=True` asks the provider for usage on the final chunk
(OpenAI-compatible `stream_options.include_usage`); if unsupported, tokens fall back to `-1`/`-1`
rather than being fabricated.

### `memory_scoped` (hand-rolled compound-scope memory — tests the STORE, not the model)

See "Multi-tenant honesty" above first. `modes.memory`'s checkpointer keys purely by `thread_id` —
no tenant/user/product dimension. This mode proves scoped, isolated memory keyed by
`(tenant_id, user_id, product_id[, session_id])` over `modes/scoped_store.py`'s hand-rolled
on-disk SQLite key/value store. It makes **no LLM call** — it is a pure test of whether the
hand-rolled scope key isolates correctly.

**Contract additions**: required `"tenant_id"`, `"user_id"`, `"product_id"` (non-empty strings).
Optional `"session_id"`, `"fact"` (value to write; default is a canned fact), `"fact_key"` (default
`"note"`), `"cross_tenant_id"` (default `"<tenant_id>-cross-tenant"`), `"scoped_store_db_path"`.

**Default scenario** (no `"op"` field — the full 3-step scenario in one invocation):

```bash
echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","fact_key":"renewal_date","fact":"The plan renews on the 3rd of every month."}' \
  | bash run.sh
```

1. WRITEs the fact under scope `(tenant-a, user-x, product-p)`.
2. RECALLs under the SAME scope -> `scoped_recall.same_scope_hit` (must be `true`).
3. RECALLs under `cross_tenant_id` (different tenant, same user/product) -> `scoped_recall.cross_tenant_miss` (must be `true`).

**Cross-process variant** (`"op": "write" | "recall"`): mirrors `modes.memory`'s
cross-process pattern — reproduces the same scenario across TWO or more separate `run.sh`
invocations sharing the same on-disk `SCOPED_STORE_DB_PATH` file, useful for proving the store
genuinely persists and isolates across OS processes, not just within one Python object's lifetime.

### `isolation_leak` (real agnes turn — proves, or exposes, a cross-tenant leak)

Tenant A writes a SECRET fact into the same `ScopedStore` under its own scope. A **real agnes
turn** then answers a question **as tenant B** (a different `tenant_id`, same
`user_id`/`product_id`). The isolation assertion is on what the scoped store returns for tenant
B's cross-tenant recall — that recall result is the ONLY thing about the secret's key that is ever
allowed to reach the system prompt built for tenant B's turn. This is deliberately not a test of
"is the model honest" — it tests whether the retrieval layer ever hands tenant A's secret to
tenant B's prompt in the first place. If the hand-rolled scope key were built incorrectly (e.g. the
adapter forgot `tenant_id` on write or read), the cross-tenant recall itself would return the
secret, it would appear verbatim in the prompt handed to the real model, and `leak_detected` would
flip to `true` — a leak at the **keying** layer, which LangGraph does nothing to prevent.

**Contract additions**: required `"tenant_id"` (the WRITER tenant), `"user_id"`, `"product_id"`,
`"secret_key"`, `"secret_value"` (non-empty strings). Optional `"cross_tenant_id"` (default
`"<tenant_id>-cross-tenant"`), `"question"`, `"scoped_store_db_path"`.

```bash
echo '{"mode":"isolation_leak","tenant_id":"tenant-a","user_id":"user-x","product_id":"product-p","secret_key":"wifi_password","secret_value":"Xk9-Nebula-Vault-42"}' \
  | bash run.sh
```

Real LLM calls: exactly 1 (the cross-tenant turn). `leak_detected` is `true` if EITHER the
store-level cross-tenant recall returned the secret OR the model's reply contains the secret
literal — `false` means isolation held at both layers.

### `observability` (measures REAL OSS OTel auto-instrumentation around LangGraph — NOT the shared envelope)

**This mode returns a different JSON shape than every other mode** — it is a diagnostic/report
mode, not a chat turn, so it does not use `modes.common.envelope()`:

```json
{
  "framework": "langgraph",
  "mode": "observability",
  "otel_supported": false,
  "span_count": 0,
  "span_names": ["..."],
  "span_timings_ms": {"name": 0.0},
  "logs_structured": false,
  "log_sample": "string",
  "notes": "string",
  "error": "string"
}
```

**This mode was rebuilt to stop hand-rolling a callback→OTel bridge and instead use the REAL
open-source auto-instrumentation the community already ships.** An earlier version of this mode
wrote its own ~80-line `_OTelBridgeHandler` (a `BaseCallbackHandler` translating LangChain's
`on_*_start`/`on_*_end` callbacks into `opentelemetry-sdk` spans by hand) and used that to argue
LangGraph "needs custom OTel plumbing." That framing overstated the effort: a real developer adding
OTel to LangChain/LangGraph writes **2-3 lines**, not a bridge class.

**LangGraph/LangChain still ship no OpenTelemetry exporter of their own** — no `opentelemetry`
import anywhere in the installed `langgraph`/`langchain-core` packages, no `OTEL_*` env var read
anywhere in either, and `graph.invoke(...)` alone produces zero spans (verified by grep against
this arena's own `.venv`, not assumed). What LangChain/LangGraph DO provide natively is the
**callback system** (`langchain_core.callbacks.BaseCallbackHandler`): every chain, chat-model, and
tool invocation inside a graph fires `on_chain_start/end`, `on_chat_model_start`/`on_llm_end`,
`on_tool_start/end` (or the matching `*_error` variant) with a `run_id`/`parent_run_id` pair that
already forms a real execution tree — this is the exact same hook **LangSmith** (LangChain's own
native, hosted observability platform) and every third-party LangChain tracer integration is built
on; nothing more privileged is available to any of them.

`modes/observability.py` now wires **OpenInference** (Apache-2.0, Arize AI —
`openinference-instrumentation-langchain`, the same OpenInference family this repo's
`arenas/live/llamaindex` adapter already uses for its own `observability` mode), the real OSS
package that does exactly this translation:

```python
from openinference.instrumentation.langchain import LangChainInstrumentor
LangChainInstrumentor().instrument(tracer_provider=provider)  # provider = in-process TracerProvider + InMemorySpanExporter
```

That is the **entire** "add OTel" step — **1 line** to instrument, 1 line to build the
`TracerProvider`, 1 line to attach the `InMemorySpanExporter` via a `SimpleSpanProcessor`
(synchronous export on `span.end()`, no OTLP collector, no network hop, nothing leaves the
process). Zero changes to the graph/agent code under test: no custom handler class, and no
`callbacks=[...]` argument on `graph.invoke()` — OpenInference's instrumentor wraps
`langchain_core.callbacks.BaseCallbackManager.__init__` so every callback manager LangChain/
LangGraph constructs automatically carries the OTel tracer. Per-span attributes include
`openinference.span.kind` (`AGENT`/`CHAIN`/`LLM`/`TOOL`, OpenInference's semantic-convention
layer, close to OTel GenAI conventions) and real provider token usage on LLM spans.

**Alternatives in the same OSS category** (documented, not installed — one instrumentor family is
enough to measure this and avoids double-patching the same callback-manager seam):
- **OpenLLMetry** — `opentelemetry-instrumentation-langchain` (Apache-2.0, Traceloop). Same
  integration shape: wraps the callback-manager construction path, one `.instrument()` call.
- **OpenLIT** — another OSS, OTel-native LLM observability SDK with LangChain auto-instrumentation
  support, same category.

**Two honest, separate axes — do not conflate them:**
1. **Native observability platform**: LangChain/LangGraph's own product answer is **LangSmith**
   (hosted SaaS; already a transitive `.venv` dependency per the license table below, but never
   activated by this adapter — no `LANGCHAIN_TRACING_V2`/`LANGCHAIN_API_KEY` is set, so zero data
   leaves this process).
2. **Measured OTel emission**: what this mode actually reports — real spans produced by wiring a
   real OSS auto-instrumentation package around a real turn, in-process, no collector. `otel_supported`
   reflects axis (2) only (`true` when the instrumented turn produced at least one real span).

The representative turn instrumented is the same real ReAct `tools`-mode loop
(`modes/tools.py`'s `calculate` tool, duplicated inline here to avoid importing another mode's
private helpers): agent decides to call `calculate` -> the tool genuinely executes -> agent
answers from the real result — at least two real `ChatOpenAI` calls plus one real tool execution,
each producing its own span through OpenInference's instrumentation. If the request has no
`"messages"`, a default arithmetic prompt is used so the mode is runnable standalone.

**Logging**: `logs_structured` and `log_sample` come from a `_SafeLogCapture` that attaches a
`logging.Handler` (INFO+) to `langchain`/`langgraph`-prefixed loggers **only** — `openai`/`httpx`
loggers are deliberately excluded from capture (they can log request headers, including
`Authorization`, at `DEBUG`) and any captured record containing a credential-shaped marker
(`authorization`, `bearer `, `api_key`) is dropped outright, never surfaced. In the verified run
below, zero LangChain/LangGraph log records were emitted at all for the turn — LangChain does not
route its own diagnostics through Python's stdlib `logging` module by default; the OTel spans
above are the primary observable instrumentation surface for this turn.

**Contract addition**: `"messages"` is optional for this mode (falls back to a canned arithmetic
prompt); every other field is the same request shape as every other mode.

```bash
echo '{"mode":"observability","messages":[{"role":"user","content":"Use the calculate tool to compute (482 * 17) + 391, then state the result plainly."}]}' \
  | bash run.sh
```

### `middleware` (real LangGraph/LangChain-native model middleware — NOT the shared envelope)

**This mode returns a different JSON shape than every other mode** — like `observability`, it is a
diagnostic/report mode, not a chat turn:

```json
{
  "framework": "langgraph",
  "mode": "middleware",
  "text": "string",
  "middleware": {
    "mechanism": "pre_model_hook",
    "inbound_fired": true,
    "outbound_fired": true,
    "invocations": 1,
    "supported": true
  },
  "error": "string"
}
```

**Contract**: request shape is identical to `qa` mode's base contract (`"system"`, `"messages"`) —
no new required fields.

This mode exercises TWO independently observable transforms around one real (or `ARENA_STUB=1`
offline — see "Offline mode" above) chat-model turn:

- **INBOUND**: appends the sentinel `"\n[ARENA_MW]"` to the system prompt BEFORE the model call.
- **OUTBOUND**: counts real model invocations.

Three LangChain/LangGraph-native "model middleware" mechanisms exist; `modes/middleware.py`'s module
docstring documents all three so the choice made here is verifiable against the pinned dependency
set, not asserted:

1. **A `RunnableLambda` wrapper** (LCEL composition) around the chat model — CAN mutate the outbound
   prompt, since a `Runnable` transforms its own input before delegating to the wrapped model.
2. **`langchain_core.callbacks.BaseCallbackHandler`** — OBSERVE-ONLY. Its
   `on_chat_model_start(serialized, messages, **kwargs)` hook receives the messages the model is
   about to see, but LangChain never reads a callback method's return value to rewrite the call —
   verified directly against the pinned `langchain-core==1.4.9` source. Callbacks CANNOT do inbound
   mutation; this mode uses them correctly, below, for OUTBOUND observation only.
3. **`langgraph.prebuilt.create_react_agent`'s `pre_model_hook` parameter** — a real graph node
   inserted before the `agent` node, contractually returning `{"llm_input_messages": [...]}`
   (confirmed directly against this arena's pinned `langgraph==1.2.9`/`langgraph-prebuilt==1.1.0`
   installed source, `langgraph/prebuilt/chat_agent_executor.py` lines ~396-424), which supplies the
   model's ACTUAL input for that turn WITHOUT overwriting the graph's persisted `"messages"` state —
   a genuine, framework-native inbound mutation seam, not a hand-rolled workaround.

This mode uses **mechanism 3 for INBOUND** (`"mechanism": "pre_model_hook"` in the response) — of the
three, it is the only one that genuinely rewrites what the model sees, and it is a first-class
LangGraph-native primitive, not this adapter's own bridge. `create_react_agent` itself is deprecated
since LangGraph v1.0 in favor of `langchain.agents.create_agent` (fires a runtime
`LangGraphDeprecatedSinceV10` warning, suppressed here from stdout/stderr noise but documented, never
hidden) — but that successor lives in the separate top-level `langchain` package, which is confirmed
**NOT installed** in this arena's pinned `.venv` (only `langchain-core`/`langchain-openai`/`langgraph`
are pinned in `requirements.txt`; `python -c "import langchain"` fails with `ModuleNotFoundError`).
`create_react_agent`'s `pre_model_hook` is therefore the mechanism actually available and used
here — fully functional against the pinned version, deprecation notwithstanding (verified live
below). For **OUTBOUND**, a `BaseCallbackHandler.on_chat_model_start` override (mechanism 2, used as
designed) counts real chat-model invocations, attached via `config={"callbacks": [...]}` on
`.invoke(...)` with zero change to the graph/agent under test.

```bash
echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' \
  | bash run.sh
```

## Docker

```bash
# Build + run one turn in a container, against the real agnes endpoint and
# the real host-run embed server / Qdrant (reached via host.docker.internal).
echo '{"mode":"qa","messages":[{"role":"user","content":"What is 2+2?"}]}' \
  | bash arenas/live/langgraph/run-docker.sh
```

`run-docker.sh` sources the repo-root `.env`, builds `aisotropy-arena-langgraph:latest` from the
`Dockerfile` in this directory, and runs it with the key injected **only** as a `docker run -e`
runtime environment variable. The `Dockerfile` has **no** `ARG`/`ENV` for the key anywhere, does
**not** `COPY` the repo-root `.env`, and does not write the key into any layer — see
`test-env.sh` for the automated proof. `EMBED_URL`/`QDRANT_URL` default to
`http://host.docker.internal:8899` / `http://host.docker.internal:6333` inside the image so `rag`
mode can reach the real host-run embed server and Qdrant from inside the container; override with
`-e EMBED_URL=... -e QDRANT_URL=...` on non-Docker-Desktop hosts (e.g. Linux CI without the
`host-gateway` alias support that `run-docker.sh` requests via `--add-host`).

`memory` mode's checkpoint file lives at `/data/checkpoints.sqlite` inside the container by default
— ephemeral per container unless a host directory is bind-mounted at `/data`
(`-v $(pwd)/.data:/data`). `crash_recovery` mode's checkpoint file lives alongside it at
`/data/checkpoints_crash.sqlite` (`CRASH_CHECKPOINT_DB_PATH`), a separate file, same volume-mount
rule. `memory_scoped`/`isolation_leak`'s compound-scope fact store lives alongside both at
`/data/scoped_store.sqlite` (`SCOPED_STORE_DB_PATH`), same volume-mount rule.

## Automated credential/env tests — `test-env.sh`

```bash
bash arenas/live/langgraph/test-env.sh
```

Runs three independent, PASS/FAIL-reported assertions and never prints the key itself:

| Assertion | What it proves |
|---|---|
| **(a)** native | `run.sh` reads `OPENAI_*` from the sourced repo-root `.env` and a real `qa`-mode smoke succeeds end-to-end. |
| **(b)** docker, no key literal | The built image's `docker history --no-trunc` has zero hits for the key literal, **and** `docker run --rm --entrypoint env <image>` has zero hits — the key is injected only at `docker run` time, never baked in. |
| **(c)** logs | The key literal appears zero times in the native adapter's stdout **and** stderr from assertion (a). |

Verified run (2026): **`RESULT: ALL PASS`** — see "Verified live runs" below for the actual PASS
lines.

## Scoped-memory isolation tests — `test-isolation.sh`

```bash
bash arenas/live/langgraph/test-isolation.sh
```

Runs three independent, PASS/FAIL-reported assertions against `memory_scoped` and
`isolation_leak`, plus a cross-process reproducibility check, and never prints the key itself:

| Assertion | What it proves |
|---|---|
| **(1)** same-scope recall hit | `memory_scoped`: a fact written under `(tenant_id, user_id, product_id)` IS recalled under the exact same scope (`scoped_recall.same_scope_hit == true`). |
| **(2)** cross-tenant recall miss | `memory_scoped`: the same fact is NOT recalled under a different `tenant_id` (`scoped_recall.cross_tenant_miss == true`). |
| **(3)** zero leak | `isolation_leak`: a real agnes turn answered as a different tenant than the one that wrote a secret does not expose it (`leak_detected == false`), checked both at the store layer and in the model's reply. |

(1)/(2) are also reproduced across THREE separate `run.sh` OS processes sharing the same on-disk
`SCOPED_STORE_DB_PATH` file (via `"op":"write"`/`"op":"recall"`), proving the isolation genuinely
holds cross-process, not just within one Python object's lifetime.

Verified run (2026): **`RESULT: ALL PASS`** — see "Verified live runs" below for the actual PASS
lines and JSON evidence.

## Hard-crash recovery test — `test-crash.sh`

```bash
bash arenas/live/langgraph/test-crash.sh
```

Orchestrates the full `crash_recovery` scenario against real infra and never prints the key itself:

| Assertion | What it proves |
|---|---|
| **(1)** genuine mid-turn kill | Process A is confirmed dead (`kill -0` fails) AND its captured stdout is exactly 0 bytes — it never reached its own final envelope `print()`, i.e. this was a real interruption, not a clean exit that merely produced an empty reply. |
| **(2)** resumed | Process B (fresh OS process, same on-disk file, same `thread_id`) returns a clean envelope with no `error`. |
| **(3)** pre-crash fact recalled | Process B's reply contains the exact secret fact stated in turn 1, recovered purely from what `SqliteSaver` had durably flushed to disk before the kill. |
| **(4)** state not lost | `state_lost` (`= !precrash_fact_recalled`) is `false`. |

`test-crash.sh` prints one final JSON object shaped exactly as this build's acceptance contract
requires: `{"framework":"langgraph","mode":"crash_recovery","resumed":bool,
"precrash_fact_recalled":bool,"state_lost":bool,"reply":str,"thread_id":str,"precrash_fact":str,
"error":str,"process_a_confirmed_killed":bool,"process_a_stdout_bytes":int}`.

**A real bug this build's own testing caught (and fixed) — worth documenting because it is easy to
repeat**: the first working version of `test-crash.sh` launched process A as
`( echo "$REQ" | bash run.sh >out 2>err ) & PID_A=$!` and then `kill -9 "$PID_A"`. That looked
correct and even reported `PASS` on every assertion — but decoding the on-disk checkpoint chain
afterward (`saver.serde.loads_typed(...)` against the raw sqlite rows) revealed turn 2's `AIMessage`
reply WAS fully committed to disk, meaning the "killed" process had actually run turn 2 to
completion in the background. The reason: `$!` after `( pipeline ) &` is the PID of the **subshell
wrapper**, not the real work — the pipeline forks its own `echo` and `bash run.sh` children, and
`bash run.sh`'s own last line is `exec ".../python" adapter.py` (replacing itself in place). Killing
only the outer subshell PID leaves that already-`exec`'d python process — a *different* PID —
running untouched. The fix: background `bash run.sh` directly with stdin redirected **from a file**
(no pipe, no subshell): `bash run.sh <req.json >out 2>err & PID_A=$!` — now `$!` names the exact
process that `exec`s into python, so `kill -9 "$PID_A"` reaches the real, in-flight agnes HTTP call.
The re-run's checkpoint chain (see "Verified live runs" below) confirms turn 2 never got an
`AIMessage` committed anywhere in the thread's history after the fix — a genuine mid-flight kill.

## Verified live runs

All runs below are real calls against the real infrastructure described above (agnes-2.0-flash,
the real e5 embed server at `127.0.0.1:8899`, the real Qdrant `arena_rag` collection at
`localhost:6333`). This adapter's original 6-mode build-and-verify session used **15 real agnes
calls total** (the ≤15 budget for that build), broken down per mode below. The later
`memory_scoped`/`isolation_leak` build (adding the two multi-tenant scoped-memory modes) used
**2 more real agnes calls total** (the ≤8 budget for that build: `memory_scoped` makes zero LLM
calls by design — it tests the store, not the model — and `isolation_leak` was exercised twice,
once directly and once inside `test-isolation.sh`). The `crash_recovery` build (adding the hard-crash
mode + `test-crash.sh`) used **7 real agnes calls total** against its own ≤8 budget: 1 smoke-test
`qa` call, then two full `test-crash.sh` runs (3 calls each: converse-turn-1, converse-turn-2 [the
one interrupted mid-flight by `kill -9` — its request was sent but its response was never received
or processed], resume) — the first run caught the subshell/`$!` bug documented above (which had to
be fixed and re-verified live, hence two full runs).

### `crash_recovery` — real SIGKILL mid-turn-2, real recovery by a fresh process (post-fix run)

Full `test-crash.sh` output:

```text
=== process A (op=converse): 2-turn conversation, thread_id=crash-thread-1784330204-76378 ===
    process A pid=76388 launched
    waiting for turn 1/2 checkpoint confirmation on stderr...
    turn 1/2 checkpoint confirmed on disk (process A still running, now inside turn 2's real agnes call)
=== sending SIGKILL to process A (pid=76388) mid turn-2 ===
PASS (1): process A confirmed dead (kill -0 fails) and emitted ZERO stdout (never reached its final envelope print) — a genuine mid-turn kill, not a clean exit

=== process B (op=resume): fresh process, same thread_id, same on-disk checkpoint file ===
    {"framework": "langgraph", "mode": "crash_recovery", "model": "agnes-2.0-flash", "reply": "FALCON-77-VERDE", "input_tokens": 335, "output_tokens": 10, "latency_ms": 7146, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "scoped_recall": null, "leak_detected": null, "error": ""}
PASS (2): process B resumed cleanly (error empty, non-empty reply)
PASS (3): process B's reply recalls the pre-crash (turn 1) fact (FALCON-77-VERDE)
PASS (4): state_lost=false — turn 1's committed state survived the kill -9 intact

=== FINAL JSON ===
{
  "framework": "langgraph",
  "mode": "crash_recovery",
  "resumed": true,
  "precrash_fact_recalled": true,
  "state_lost": false,
  "reply": "FALCON-77-VERDE",
  "thread_id": "crash-thread-1784330204-76378",
  "precrash_fact": "FALCON-77-VERDE",
  "error": "",
  "process_a_confirmed_killed": true,
  "process_a_stdout_bytes": 0
}

RESULT: ALL PASS
```

Independent confirmation, decoded straight from the on-disk `checkpoints_crash.sqlite` after the
run (`saver.serde.loads_typed(...)` against the raw rows — not inferred from the JSON above):

```text
[0] step=-1 source=input n_msgs=0 -> []
[1] step=0  source=loop  n_msgs=1 -> ["HumanMessage:'Remember this exactly: the secret l'"]
[2] step=1  source=loop  n_msgs=2 -> ["HumanMessage:'...'", "AIMessage:'OK'"]
[3] step=2  source=input n_msgs=2 -> ["HumanMessage:'...'", "AIMessage:'OK'"]
[4] step=3  source=loop  n_msgs=3 -> [..., "HumanMessage:'Now, in one short sentence, tell me'"]   # turn 2 queued
[5] step=4  source=input n_msgs=3 -> [..., "HumanMessage:'Now, in one short sentence, tell me'"]   # turn 2 about to run its LLM call — this is the LAST checkpoint process A ever wrote
[6] step=5  source=loop  n_msgs=4 -> [..., "HumanMessage:'What was the secret launch code I t'"]   # process B's own new question, appended on top of the SAME n=3 state
[7] step=6  source=loop  n_msgs=5 -> [..., "AIMessage:'FALCON-77-VERDE'"]                          # process B's real, correct answer
```

Turn 2's `AIMessage` (the anglerfish fact) **never appears anywhere in the checkpoint chain** —
process A was killed strictly between checkpoint `[5]` (entering turn 2's step, before the real
agnes call) and whatever checkpoint would have recorded its result. `ps aux | grep adapter.py`
immediately after the test and again after an 8-second grace period found zero matching processes —
no orphaned python silently finishing turn 2 in the background. Process B started from checkpoint
`[5]`'s state (3 messages, turn 2's question present but unanswered), appended its own new question
as checkpoint `[6]`, and its real agnes call correctly recalled the turn-1 secret in checkpoint `[7]`.

**Answering the mode's own honest question directly: yes — for this specific pattern (each turn's
`SqliteSaver` context manager closed, i.e. its transaction committed, before the next turn begins),
LangGraph's on-disk `SqliteSaver` checkpoint IS durable across a real `kill -9`, not just a clean
shutdown.** This is standard SQLite durability (the file is fsync'd on commit at `with` block exit),
not a LangGraph-specific guarantee — LangGraph does nothing extra to protect data written mid-step
that hasn't reached a commit boundary yet (see checkpoint `[5]` above: turn 2's *pending* human
message DID get committed as its own checkpoint before the LLM call, which is why process B could
see it, but that pending state carries no answer — a caller must not assume a turn "happened" just
because its request checkpoint exists). The dev is responsible for the same discipline `memory` mode
already documents: don't hold the `SqliteSaver` connection open across a long-lived process if
crash-durability of each individual turn matters.

### `qa`

```json
{"framework": "langgraph", "mode": "qa", "model": "agnes-2.0-flash", "reply": "Paris", "input_tokens": 267, "output_tokens": 2, "latency_ms": 6785, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

### `rag` — grounded question (top-1 retrieval names the gold doc, answer matches the source fact)

Q: *"How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an
automatic full refund, and where is the refund sent?"*

```json
{"framework": "langgraph", "mode": "rag", "model": "agnes-2.0-flash", "reply": "Under the Blue Guarantee, a passenger receives an automatic full refund if their trainset arrives more than **12 minutes late**. The refund is sent to their **HaloPay balance**.", "input_tokens": 572, "output_tokens": 38, "latency_ms": 7169, "tool_calls": [], "retrieved": [{"doc_id": "08-blue-guarantee.md", "title": "The Blue Guarantee (Refund Policy)", "score": 0.8993131}, {"doc_id": "17-accessibility.md", "title": "Accessibility Commitment", "score": 0.8469959}, {"doc_id": "22-branding-colors.md", "title": "Livery and Branding", "score": 0.84084755}], "structured": null, "ttft_ms": -1, "error": ""}
```

Gold doc `08-blue-guarantee.md` is top-1 (score 0.899); reference fact (12 minutes, HaloPay) matches
exactly (see `arenas/live/_shared/rag_questions.jsonl` q1).

### `rag` — trap question (no supporting doc exists; correct behavior is "I don't know")

Q: *"Which airline does Nimbus Rail partner with for connecting flights out of Vellmoor?"*

```json
{"framework": "langgraph", "mode": "rag", "model": "agnes-2.0-flash", "reply": "I don't know. The provided context does not contain information about any airline partnerships for Nimbus Rail.", "input_tokens": 606, "output_tokens": 21, "latency_ms": 7016, "tool_calls": [], "retrieved": [{"doc_id": "01-company-overview.md", "title": "Nimbus Rail — Company Overview", "score": 0.8942678}, {"doc_id": "03-marrow-loop.md", "title": "The Marrow Loop", "score": 0.8526732}, {"doc_id": "02-skylark-line.md", "title": "The Skylark Line", "score": 0.84395397}], "structured": null, "ttft_ms": -1, "error": ""}
```

Correctly refuses to invent a partner (matches `rag_questions.jsonl` q9, `"trap": true`).

### `tools` — real multi-step execution ((482 × 17) + 391 = 8585, computed by the executed tool)

```json
{"framework": "langgraph", "mode": "tools", "model": "agnes-2.0-flash", "reply": "The result is 8585.", "input_tokens": 1170, "output_tokens": 49, "latency_ms": 13850, "tool_calls": [{"name": "calculate", "arguments": {"expression": "(482 * 17) + 391"}, "result": "8585"}], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

`8585` is arithmetically correct (482×17=8194, +391=8585) — the tool genuinely ran, and the final
answer genuinely used its result (two real model calls: decide-to-call-tool, then answer-from-result
— `input_tokens`/`output_tokens` are the sum across both).

### `memory` — recall across TWO separate OS processes, same `thread_id`

Process 1 (`bash run.sh` invocation #1 — states a fact):

```json
{"framework": "langgraph", "mode": "memory", "model": "agnes-2.0-flash", "reply": "OK", "input_tokens": 271, "output_tokens": 2, "latency_ms": 6698, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

Process 2 (fresh `bash run.sh` invocation #2, same `thread_id`, no history in this process's own
stdin — recalls the fact purely from the on-disk checkpoint):

```json
{"framework": "langgraph", "mode": "memory", "model": "agnes-2.0-flash", "reply": "Skylark", "input_tokens": 293, "output_tokens": 4, "latency_ms": 6865, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

### `structured` and `stream` — mechanism verified live pre-integration, not re-run through the final adapter in this session

Both mechanisms (`with_structured_output(..., method="json_schema", include_raw=True)` for
`structured`; `graph.stream(..., stream_mode="messages")` + `stream_usage=True` for `stream`) were
verified against the real agnes endpoint with the exact same LangChain/LangGraph calls
`modes/structured.py` and `modes/stream.py` now make, **before** those files were assembled:

```text
structured: {'answer': 'Buenos Aires', 'confidence': 1.0}  (raw usage: input=265, output=24)
stream:     ttft_ms=1096, 17 chunks, full="1, 2, 3, 4, 5"  (usage: input=268, output=14)
```

Given the hard `≤15 real agnes calls` budget for this build (spent entirely on the base `qa`
verification, both `rag` questions, `tools`, both `memory` processes, the Docker `qa` smoke, and
`test-env.sh`'s own live smoke), these two modes were **not** re-run live through the assembled
`adapter.py` in this session. Every non-network code path in both — env validation, message
building, schema defaulting, error handling — was exercised with zero live calls (see the "Dry
error-path checks" below) and both `.py` files are otherwise structurally identical to the
mode-specific graphs (`qa`, `rag`) that *were* verified live. This is the one honestly-flagged gap
in this session's live evidence; a follow-up session re-running `structured`/`stream` through
`adapter.py` directly costs exactly 2 more calls against the budget.

### Docker `qa` smoke (full build + real container run)

```json
{"framework": "langgraph", "mode": "qa", "model": "agnes-2.0-flash", "reply": "4", "input_tokens": 265, "output_tokens": 2, "latency_ms": 6932, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

### `test-env.sh` (full output)

```text
=== (a) native: env-var read + real qa smoke ===
PASS (a): native qa smoke succeeded
    {"framework": "langgraph", "mode": "qa", ..., "reply": "ack", "input_tokens": 259, "output_tokens": 2, ..., "error": ""}

=== (c) key never appears in native stdout/stderr ===
PASS (c): key literal hits in native stdout=0 stderr=0

=== (b1) docker image build + layer history contains no key literal ===
PASS (b0): docker build succeeded
PASS (b1): key literal hits in 'docker history' = 0

=== (b2) 'docker run --rm --entrypoint env <image>' contains no key literal ===
PASS (b2): key literal hits in 'docker run ... env' = 0

RESULT: ALL PASS
```

### `memory_scoped` — full 3-step scenario, one invocation, zero LLM calls

```json
{"framework": "langgraph", "mode": "memory_scoped", "model": "agnes-2.0-flash", "reply": "The plan renews on the 3rd of every month.", "input_tokens": -1, "output_tokens": -1, "latency_ms": 0, "tool_calls": [], "retrieved": [], "structured": {"recalled_same_scope": "The plan renews on the 3rd of every month.", "recalled_cross_tenant": null, "scope": {"tenant_id": "tenant-a", "user_id": "user-x", "product_id": "product-p", "session_id": null}, "cross_tenant_id": "tenant-a-cross-tenant", "fact_key": "renewal_date"}, "ttft_ms": -1, "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true}, "leak_detected": null, "error": ""}
```

`scoped_recall.same_scope_hit=true`, `scoped_recall.cross_tenant_miss=true` — the fact written
under `(tenant-a, user-x, product-p)` is recalled verbatim under the same scope, and returns
nothing under a different `tenant_id` with the same `user_id`/`product_id`. Also reproduced across
three separate `run.sh` OS processes via `"op":"write"`/`"op":"recall"` sharing the same on-disk
`SCOPED_STORE_DB_PATH` file (see `test-isolation.sh` output below).

### `isolation_leak` — real agnes turn, cross-tenant secret probe

```json
{"framework": "langgraph", "mode": "isolation_leak", "model": "agnes-2.0-flash", "reply": "I do not know.", "input_tokens": 329, "output_tokens": 6, "latency_ms": 6874, "tool_calls": [], "retrieved": [], "structured": {"writer_tenant_id": "tenant-a", "reader_tenant_id": "tenant-a-cross-tenant", "secret_key": "wifi_password", "store_level_cross_tenant_recall": null, "store_level_leak": false, "model_leaked_secret_in_reply": false}, "ttft_ms": -1, "scoped_recall": null, "leak_detected": false, "error": ""}
```

`leak_detected=false`: tenant A wrote `secret_value="Xk9-Nebula-Vault-42"` under `wifi_password`;
tenant B's cross-tenant recall returned `null` (`store_level_cross_tenant_recall`), so the system
prompt built for the real agnes call never contained the secret, and the model's real reply ("I do
not know.") does not contain it either — isolation held at both the store layer and the model
layer, checked independently.

### `observability` — real ReAct turn instrumented via real OSS OpenInference auto-instrumentation (2026-07-17 rebuild, 2 real agnes calls)

Rebuilt to replace the hand-rolled `_OTelBridgeHandler` callback→OTel bridge with the real,
community-maintained, Apache-2.0 `openinference-instrumentation-langchain` package (see mode
docs above). Verified live against the same real ReAct `tools`-style turn:

```json
{"framework": "langgraph", "mode": "observability", "otel_supported": true, "span_count": 9, "span_names": ["LangGraph", "agent", "ChatOpenAI", "tools_condition", "tools", "calculate", "agent", "ChatOpenAI", "tools_condition"], "span_timings_ms": {"LangGraph": 13945.148, "agent": 7172.862, "ChatOpenAI": 7170.95, "tools_condition": 0.293, "tools": 4.127, "calculate": 0.83, "agent#2": 6764.224, "ChatOpenAI#2": 6762.024, "tools_condition#2": 0.361}, "logs_structured": false, "log_sample": "", "error": ""}
```

(`notes` field omitted above for brevity — full text is in the mode docs above and matches the
live run verbatim.) `otel_supported=true` — real spans were emitted through OpenInference's
`LangChainInstrumentor` for one `tools`-style turn: 1 root `LangGraph` span, 2 `agent` rounds, 2
real `ChatOpenAI` calls (the decide-to-call-tool call and the answer-from-result call), 2
`tools_condition` routing decisions, 1 `tools` node, and 1 real `calculate` tool execution —
matching `modes/tools.py`'s documented "at least two real model calls" shape exactly. Per-span
`openinference.span.kind` attributes captured directly off the real spans: `['AGENT', 'CHAIN',
'LLM', 'TOOL']`. Per-span timings show the real cost breakdown: the two real network calls to
agnes dominate (7171ms and 6762ms) while the routing/tool-node spans are sub-millisecond
(`tools_condition`: 0.29–0.36ms, `calculate`: 0.83ms — genuinely local computation).
`logs_structured=false` and `log_sample=""`: zero LangChain/LangGraph log records were emitted at
INFO+ for this turn — confirms LangChain does not route its own diagnostics through Python's
stdlib `logging` module by default. The API key literal was verified to appear zero times in this
run's stdout or stderr (same `grep`-based proof pattern as `test-env.sh`'s assertion (c)); a
`qa`-mode regression call was also re-verified passing in the same session (1 more real agnes
call, 3 total for this rebuild).

`openinference-instrumentation-langchain==0.1.67` installed clean into this arena's `.venv`
(Python 3.14.6) with no dependency conflicts against the existing pinned stack (`pip install -r
requirements.txt` after the addition resolves fully via "Requirement already satisfied" for every
transitive package). License, verified via `pip show` against the installed package (not
assumed): **`License-Expression: Apache-2.0`** — see the updated dependency tables below for the
four new packages this pulls in (`openinference-instrumentation`, `openinference-semantic-
conventions`, `opentelemetry-instrumentation`, all Apache-2.0; `wrapt`, BSD-2-Clause).

### `middleware` — real `pre_model_hook` inbound mutation + real callback outbound counter (1 real agnes call)

Real run against the live agnes endpoint (`bash run.sh`), zero new dependencies (`langgraph.prebuilt`
and `langchain_core.callbacks` are both already-pinned transitive surface):

```json
{"framework": "langgraph", "mode": "middleware", "text": "OK", "middleware": {"mechanism": "pre_model_hook", "inbound_fired": true, "outbound_fired": true, "invocations": 1, "supported": true}, "error": ""}
```

`inbound_fired=true`: the `pre_model_hook` node ran and appended `"\n[ARENA_MW]"` to the system
message before the real agnes call — confirmed directly in a pre-integration offline prototype (not
just inferred from the boolean) by inspecting the exact `llm_input_messages` list handed to the
model, which showed `"You are a rail assistant.\n[ARENA_MW]"`. `outbound_fired=true`,
`invocations=1`: the `BaseCallbackHandler.on_chat_model_start` override fired exactly once, matching
the one real `ChatOpenAI` call this turn makes (`tools=[]`, so no ReAct tool round-trip). API key
literal verified to appear zero times in this run's stdout/stderr (same `grep`-based proof pattern as
`test-env.sh`'s assertion (c)). `ARENA_STUB=1` regression (same request, zero real calls) also
verified passing in the same session, confirming both mechanisms fire identically offline:

```json
{"framework": "langgraph", "mode": "middleware", "text": "OK (ARENA_STUB offline stub reply)", "middleware": {"mechanism": "pre_model_hook", "inbound_fired": true, "outbound_fired": true, "invocations": 1, "supported": true}, "error": ""}
```

A same-session regression pass (offline, `ARENA_STUB=1`) also confirmed all ten pre-existing modes
still return `error: ""` end-to-end after routing through `common.build_chat_model()`/
`common.checkpointer_context()`, and a follow-up REAL (non-stub) regression pass re-verified `qa`
(single call), `tools` (real `calculate(12*7+3)=87` executed by the ReAct loop), and `memory`
(cross-process recall of "Teal" purely from the on-disk checkpoint written by a separate `run.sh`
process) — 3 more real agnes calls, all still passing after the refactor that centralized every
mode's `ChatOpenAI(...)`/`SqliteSaver(...)` construction through the two new `common.py` seams.

### `test-isolation.sh` (full output)

```text
=== (1)+(2) memory_scoped: full 3-step scenario in one invocation ===
PASS (1): same-scope recall hit (same_scope_hit=true)
PASS (2): cross-tenant recall missed (cross_tenant_miss=true)

=== (1)+(2) reproducibility: same scenario across THREE separate run.sh processes ===
PASS (1)/(2) cross-process: same-tenant hit + cross-tenant miss reproduced across 3 processes

=== (3) isolation_leak: real agnes turn, zero leak ===
PASS (3): zero leak (leak_detected=false) — secret never reached tenant B's store recall or reply

RESULT: ALL PASS
```

### Dry error-path checks (zero live calls — env validation / input validation fails before any HTTP request)

```text
structured, missing OPENAI_API_KEY -> {"error": "OPENAI_API_KEY is not set", ...}
stream,     missing OPENAI_API_KEY -> {"error": "OPENAI_API_KEY is not set", ...}
mode="bogus"                       -> {"error": "unknown mode 'bogus'; supported: [...]", ...}
rag,    messages=[]                -> {"error": "no user message found in 'messages'", ...}
memory, missing thread_id          -> {"error": "memory mode requires a non-empty string 'thread_id' field", ...}
crash_recovery, missing thread_id  -> {"error": "crash_recovery mode requires a non-empty string 'thread_id' field", ...}
crash_recovery, missing turns      -> {"error": "crash_recovery mode op='converse' requires a non-empty list of non-empty strings 'turns'", ...}
crash_recovery, unknown op         -> {"error": "crash_recovery mode: unknown op 'bogus'; supported: converse, resume", ...}
memory_scoped, missing product_id  -> {"error": "memory_scoped mode requires non-empty string fields: ['product_id']", ...}
isolation_leak, missing fields     -> {"error": "isolation_leak mode requires non-empty string fields: ['product_id', 'secret_key', 'secret_value']", ...}
middleware,     missing OPENAI_API_KEY -> {"error": "OPENAI_API_KEY is not set", "middleware": {"supported": false, ...}, ...}
middleware,     messages=[]        -> {"error": "no messages in request", "middleware": {"supported": false, ...}, ...}
```

## Dependencies and licenses

All **direct** dependencies are MIT or Apache-2.0. License data below is transcribed verbatim from
`pip show <pkg>` / installed package metadata against this arena's `.venv` (not assumed).

`memory_scoped` and `isolation_leak` add **zero new dependencies** — `modes/scoped_store.py` uses
only `sqlite3` (Python standard library, PSF License) and stdlib `os`/`time`; `requirements.txt`
is unchanged by this build. `crash_recovery` also adds **zero new dependencies** — it reuses the
same `langgraph-checkpoint-sqlite` package `memory` mode already depends on, against a separate
on-disk file. `middleware` and the `ARENA_STUB=1` offline toggle likewise add **zero new
dependencies** to `requirements.txt` — `langgraph.prebuilt.create_react_agent`,
`langchain_core.callbacks.BaseCallbackHandler`,
`langchain_core.language_models.fake_chat_models.FakeListChatModel`, and
`langgraph.checkpoint.memory.InMemorySaver` are all already-installed surface of the pinned
`langgraph`/`langchain-core`/`langgraph-checkpoint-sqlite` packages (verified via
`python -c "import ..."` against this arena's `.venv`, not assumed).

### LangChain suite: what this entrant uses vs the rest of the ecosystem

This adapter is ONE competing framework (**LangGraph**), using five MIT LangChain-ecosystem packages:
`langgraph` (the runtime), `langchain-core` (base abstractions + the callback system bridged to OTel),
`langchain-openai` (`ChatOpenAI` → agnes), `langchain-qdrant` (RAG retriever), and
`langgraph-checkpoint-sqlite` (the `SqliteSaver` checkpointer behind `memory` + `crash_recovery`).

The rest of the Lang\* suite is **deliberately out of scope**: **LangSmith** and **LangGraph Platform /
Studio** are hosted SaaS observability/deploy products, not in-process runtimes — they cannot be an
apples-to-apples stdin→stdout adapter and would export arena data off-box; **LangServe** is deprecated;
**Deep Agents** is an OSS layer *on top of* LangGraph (LangGraph is the fair base to test); **LangFlow**
is a separate project (not LangChain, Inc.). Full ecosystem map + rationale:
`docs/research/langchain-suite-map-2026-07-17.md`.

### Direct dependencies

| Package | Version | License | Added for |
|---|---|---|---|
| `langgraph` | 1.2.9 | MIT | base (all modes) |
| `langchain-openai` | 1.3.5 | MIT | base (all modes) |
| `langchain-core` | 1.4.9 | MIT | base (all modes) |
| `langchain-qdrant` | 1.1.0 | MIT | `rag` |
| `qdrant-client` | 1.18.0 | Apache-2.0 | `rag` |
| `langgraph-checkpoint-sqlite` | 3.1.0 | MIT | `memory` |
| `opentelemetry-api` | 1.44.0 | Apache-2.0 | `observability` |
| `opentelemetry-sdk` | 1.44.0 | Apache-2.0 | `observability` |
| `openinference-instrumentation-langchain` | 0.1.67 | Apache-2.0 | `observability` — real OSS OTel auto-instrumentation (replaces the earlier hand-rolled bridge) |

### New transitive dependencies (pulled in by the three new direct deps above)

| Package | Version | License | Note |
|---|---|---|---|
| `aiosqlite` | 0.22.1 | MIT (verified via PyPI classifier; `pip show` License field was blank) | |
| `grpcio` | 1.82.1 | Apache-2.0 | qdrant-client's gRPC transport |
| `h2` | 4.3.0 | MIT | httpx HTTP/2 support (qdrant-client REST) |
| `hpack` | 4.2.0 | MIT | h2 dependency |
| `hyperframe` | 6.1.0 | MIT | h2 dependency |
| `numpy` | 2.5.1 | BSD-3-Clause AND 0BSD AND MIT AND Zlib AND CC0-1.0 (all permissive) | qdrant-client |
| `ormsgpack` | 1.12.2 | Apache-2.0 OR MIT | qdrant-client |
| `portalocker` | 3.2.0 | BSD-3-Clause | qdrant-client local-mode file locking |
| `protobuf` | 7.35.1 | BSD-3-Clause | grpcio |
| `sqlite-vec` | 0.1.9 | MIT License, Apache License 2.0 (dual) | langgraph-checkpoint-sqlite, unused by this adapter's own code |
| `opentelemetry-semantic-conventions` | 0.65b0 | Apache-2.0 | pulled in by `opentelemetry-sdk`; not directly imported by this adapter's code |
| `openinference-instrumentation` | 0.1.54 | Apache-2.0 | pulled in by `openinference-instrumentation-langchain` |
| `openinference-semantic-conventions` | 0.1.30 | Apache-2.0 | pulled in by `openinference-instrumentation-langchain`; defines `openinference.span.kind` and related attribute keys |
| `opentelemetry-instrumentation` | 0.65b0 | Apache-2.0 | pulled in by `openinference-instrumentation-langchain` (OTel's own base instrumentor class) |
| `wrapt` | 2.2.2 | BSD-2-Clause | pulled in by `openinference-instrumentation-langchain`; used to wrap `BaseCallbackManager.__init__` |

### Transitive dependencies already present in the original single-mode adapter (unchanged)

| Package | Version | License | Note |
|---|---|---|---|
| `langgraph-checkpoint` | 4.1.1 | MIT | |
| `langgraph-prebuilt` | 1.1.0 | MIT | |
| `langgraph-sdk` | 0.4.2 | MIT | |
| `langchain-protocol` | 0.0.18 | MIT | |
| `langsmith` | 0.10.6 | MIT | |
| `openai` | 2.46.0 | Apache-2.0 | |
| `pydantic` | 2.13.4 | MIT | |
| `pydantic_core` | 2.46.4 | MIT | |
| `httpx` | 0.28.1 | BSD-3-Clause | |
| `httpcore` | 1.0.9 | BSD-3-Clause | |
| `idna` | 3.18 | BSD-3-Clause | |
| `uuid_utils` | 0.17.0 | BSD-3-Clause | |
| `websockets` | 15.0.1 | BSD-3-Clause | |
| `zstandard` | 0.25.0 | BSD-3-Clause | |
| `xxhash` | 3.8.1 | BSD-2-Clause | |
| `distro` | 1.9.0 | Apache-2.0 | |
| `requests` | 2.34.2 | Apache-2.0 | |
| `requests-toolbelt` | 1.0.0 | Apache-2.0 | |
| `packaging` | 26.2 | Apache-2.0 OR BSD-2-Clause | |
| `urllib3` | 2.7.0 | MIT | |
| `anyio` | 4.14.2 | MIT | |
| `h11` | 0.16.0 | MIT | |
| `sniffio` | 1.3.1 | MIT OR Apache-2.0 | |
| `jiter` | 0.16.0 | MIT | |
| `jsonpatch` | 1.33 | Modified BSD (BSD-3-Clause) | |
| `jsonpointer` | 3.1.1 | Modified BSD (BSD-3-Clause) | |
| `PyYAML` | 6.0.3 | MIT | |
| `tiktoken` | 0.13.0 | MIT | |
| `tenacity` | 9.1.4 | Apache-2.0 | |
| `annotated-types` | 0.7.0 | MIT (verified via PyPI classifier; `pip show` License field was blank) | |
| `typing_extensions` | 4.16.0 | PSF-2.0 (Python Software Foundation License — permissive) | |
| `typing-inspection` | 0.4.2 | MIT | |
| `charset-normalizer` | 3.4.9 | MIT | |
| `regex` | 2026.7.10 | Apache-2.0 AND CNRI-Python (CNRI-Python is the permissive, OSI-approved historical Python 1.6 license) | |

### Flagged: weak-copyleft (MPL-2.0) transitive dependencies

Per the repo's `.claude/rules/cos/license-policy.md`, MPL-2.0 is **"use with caution"**, not
blocked (only AGPL/SSPL/BSL/ELv2/Commons-Clause/FSL are hard blockers). These three transitive
packages carry MPL-2.0 in their license expression and are flagged explicitly rather than silently
proceeding:

| Package | Version | License-Expression | Why it's safe here |
|---|---|---|---|
| `certifi` | 2026.6.17 | MPL-2.0 | Unmodified CA-bundle data package (pulled in by `httpx`/`requests`); no source file of it is modified or vendored by this adapter. |
| `orjson` | 3.11.9 | MPL-2.0 AND (Apache-2.0 OR MIT) | Unmodified, used only as an optional fast-JSON dependency of the LangChain stack; no source file is modified or vendored. |
| `tqdm` | 4.69.0 | MPL-2.0 AND MIT | Unmodified, pulled in as a transitive progress-bar dependency; not directly invoked by this adapter's code. |

MPL-2.0's copyleft obligation is file-level ("changes to MPL-covered files must be published");
since none of these files are modified or redistributed in modified form, using them unmodified as
installed PyPI packages does not trigger any obligation. **No AGPL/SSPL/BSL/ELv2/Commons-Clause/FSL
license was found anywhere in the dependency tree**, including the new `rag`/`memory` dependencies
added in this build (`langchain-qdrant`, `qdrant-client`, `langgraph-checkpoint-sqlite`, and their
transitive deps above).

## Gotchas / API quirks (see also Engram `arenas/live/langgraph-exhaustive` topic)

- `MessagesState` (from `langgraph.graph`) is a `TypedDict` with a `messages` key using the
  `add_messages` reducer; a node function returning `{"messages": [response]}` appends rather than
  replaces the list — the last message in `result["messages"]` after `graph.invoke(...)` is the
  model's response.
- `AIMessage.tool_calls` is already a normalized list of `{"name", "args", "id", "type"}` dicts
  regardless of the underlying provider's raw tool-call wire format — no manual OpenAI
  `tool_calls[].function.arguments` JSON-string parsing is needed.
- Token usage should be read from `AIMessage.usage_metadata` first (LangChain's
  provider-agnostic standardized shape: `{"input_tokens", "output_tokens", "total_tokens"}`);
  `response_metadata["token_usage"]` (raw OpenAI shape: `prompt_tokens` / `completion_tokens`) is
  the fallback. Both were observed populated against the agnes endpoint in this adapter's testing.
- `bind_tools()` accepts tool specs already in `{"type": "function", "function": {...}}` form —
  no need to wrap them in LangChain's `@tool` decorator or a Pydantic model for a single-shot,
  never-executed tool-calling turn like the base `qa` contract requires.
- **`QdrantVectorStore` metadata gotcha**: `Document.metadata` is populated *only* from a nested
  `metadata_payload_key` (default `"metadata"`). A flat Qdrant payload like
  `{"doc_id": ..., "title": ..., "text": ...}` (this repo's `populate-qdrant.py` schema) has no
  `"metadata"` key, so `Document.metadata` comes back as just `{"_id": ..., "_collection_name": ...}`
  — `doc_id`/`title` are silently dropped unless you re-fetch the full payload by point id via
  `vector_store.client.retrieve(...)` afterward (see `modes/rag.py::retrieve`).
- The e5 embed server resolves the "query" vs. "passage" instruction prefix from the `model` id
  suffix (`...-query` / `...-passage`) — `rag` mode must request `"multilingual-e5-small-query"`
  (matching how `populate-qdrant.py` indexed the corpus with `"...-passage"`); using the wrong side
  degrades retrieval quality without erroring.
- `qdrant-client` 1.18.0 against a Qdrant server on an older minor (1.12.x here) prints a
  `UserWarning` about version skew on every request unless `QdrantClient(..., check_compatibility=False)`
  is passed — the REST contract used here is stable across that skew, so the adapter disables the
  check rather than let this warning fire on every single `rag` call to stderr.
- `langgraph.prebuilt.create_react_agent` emits a `LangGraphDeprecatedSinceV10` warning as of
  langgraph 1.2.x (moved to `langchain.agents.create_agent`, removal in V2.0). `modes/tools.py`
  builds the equivalent graph manually from `StateGraph` + `ToolNode` + `tools_condition` (still
  fully supported, not deprecated) instead of depending on the helper.
- `llm.with_structured_output(schema)` (no `include_raw`) returns just the parsed object with **no**
  access to the underlying `AIMessage` — real provider token usage is unavailable that way. Pass
  `include_raw=True` to get `{"raw": AIMessage, "parsed": ..., "parsing_error": ...}` and extract
  usage from `raw` — otherwise usage would have to be fabricated, which the base contract forbids.
- `SqliteSaver.from_conn_string(path)` is a context manager (`Iterator[SqliteSaver]`), not a plain
  constructor — the graph must be compiled and invoked *inside* the `with` block; the checkpoint
  is durably written to disk by the time the block exits, so a second, fully separate process can
  open the same file and see it.
- **Bash gotcha, not a LangGraph one, but load-bearing for `test-crash.sh`**: `PID=$!` after
  `( echo "$x" | bash run.sh ) &` captures the subshell wrapper's PID, not the real work — the
  pipeline forks its own `echo` and `bash run.sh` children. Since `run.sh`'s last line is
  `exec ".../python" adapter.py` (same PID, image replaced in place), the ACTUAL long-running
  process is a *different* PID than the subshell's. `kill -9 "$PID"` on the subshell leaves that
  already-`exec`'d python process running to completion in the background — a false-negative "crash"
  that will still report all assertions PASS if you only check the reply, not the checkpoint chain.
  Fix: background `bash run.sh` directly (stdin from a file, no pipe, no subshell wrapper) so `$!` is
  the exact process that `exec`s into python.
- `ChatOpenAI(..., stream_usage=True)` plus `graph.stream(..., stream_mode="messages")` yields
  `(AIMessageChunk, metadata)` pairs; only the *final* chunk in the OpenAI-compatible stream
  reliably carries `usage_metadata` (`stream_options.include_usage` semantics) — earlier chunks have
  `usage_metadata=None`, so the adapter keeps overwriting its usage variables as chunks arrive and
  the last non-`None` value wins.
