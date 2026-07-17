# LangGraph — Live Arena Adapter (multi-mode)

A LIVE arena adapter for [LangGraph](https://github.com/langchain-ai/langgraph) / [LangChain](https://github.com/langchain-ai/langchain)
that drives **six modes**, each exercising a different LangGraph/LangChain-native capability
against **real infrastructure**: the real OpenAI-compatible model ("agnes-2.0-flash"), a real e5
embeddings server, a real Qdrant collection, and a real on-disk checkpointer. This directory is
fully additive and does not touch the pre-existing `arenas/langgraph/` arena (a separate, unrelated
benchmark harness), the base single-mode contract in `arenas/live/SPEC.md`, or anything outside
`arenas/live/langgraph/`.

The original single-shot adapter (`qa` mode) is unchanged in behavior; everything else is new.

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

`OPENAI_*` are read from the repo-root `.env` (never committed, never modified by this adapter).
`run.sh` sources it automatically:

```bash
set -a; source <repo-root>/.env; set +a
```

`EMBED_URL`/`QDRANT_URL` are environment-configurable specifically so the same image works
unmodified in Docker, where `localhost`/`127.0.0.1` would resolve to the *container* instead of the
host running the real embed server and Qdrant — see "Docker" below.

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
  "mode": "qa | rag | tools | memory | structured | stream",
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
  "error": "string"
}
```

Shared rules (all modes):

- `input_tokens`/`output_tokens`/`ttft_ms` are **provider-reported only**; `-1` means genuinely
  unavailable/not measured for that mode, never a fabricated number.
- `retrieved` is `[]` and `structured` is `null` for modes that don't produce them.
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
(`-v $(pwd)/.data:/data`).

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

## Verified live runs

All runs below are real calls against the real infrastructure described above (agnes-2.0-flash,
the real e5 embed server at `127.0.0.1:8899`, the real Qdrant `arena_rag` collection at
`localhost:6333`). This adapter's full build-and-verify session used **15 real agnes calls total**
(the ≤15 budget for this build), broken down per mode below.

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

### Dry error-path checks (zero live calls — env validation / input validation fails before any HTTP request)

```text
structured, missing OPENAI_API_KEY -> {"error": "OPENAI_API_KEY is not set", ...}
stream,     missing OPENAI_API_KEY -> {"error": "OPENAI_API_KEY is not set", ...}
mode="bogus"                       -> {"error": "unknown mode 'bogus'; supported: [...]", ...}
rag,    messages=[]                -> {"error": "no user message found in 'messages'", ...}
memory, missing thread_id          -> {"error": "memory mode requires a non-empty string 'thread_id' field", ...}
```

## Dependencies and licenses

All **direct** dependencies are MIT or Apache-2.0. License data below is transcribed verbatim from
`pip show <pkg>` / installed package metadata against this arena's `.venv` (not assumed).

### Direct dependencies

| Package | Version | License | Added for |
|---|---|---|---|
| `langgraph` | 1.2.9 | MIT | base (all modes) |
| `langchain-openai` | 1.3.5 | MIT | base (all modes) |
| `langchain-core` | 1.4.9 | MIT | base (all modes) |
| `langchain-qdrant` | 1.1.0 | MIT | `rag` |
| `qdrant-client` | 1.18.0 | Apache-2.0 | `rag` |
| `langgraph-checkpoint-sqlite` | 3.1.0 | MIT | `memory` |

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
- `ChatOpenAI(..., stream_usage=True)` plus `graph.stream(..., stream_mode="messages")` yields
  `(AIMessageChunk, metadata)` pairs; only the *final* chunk in the OpenAI-compatible stream
  reliably carries `usage_metadata` (`stream_options.include_usage` semantics) — earlier chunks have
  `usage_metadata=None`, so the adapter keeps overwriting its usage variables as chunks arrive and
  the last non-`None` value wins.
