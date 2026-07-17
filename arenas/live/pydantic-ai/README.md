# Pydantic AI — Exhaustive Multi-Mode LIVE Arena Adapter

Part of the **LIVE arena**: unlike `arenas/pydantic-ai/` (which classifies the framework's
isolation model offline against a stub/`TestModel`), this adapter runs **real turns against the
same live model** (`agnes-2.0-flash`, OpenAI-compatible) used by every other LIVE adapter, so
observed differences are attributable to the framework, not the model.

This adapter goes beyond the base arena's single-shot contract (`arenas/live/SPEC.md`): it is a
**multi-mode** adapter, selected by a `"mode"` key on stdin, that exercises Pydantic AI's NATIVE
features — real retrieval, real multi-step tool execution, real persisted memory, real structured
output, and real token streaming — against the **real, running local infrastructure** for this
arena (agnes chat, the e5 embedding server, Qdrant, rqlite).

- **Framework:** Pydantic AI 2.12.0 (MIT)
- **Model:** `agnes-2.0-flash` via an OpenAI-compatible endpoint
- **Modes:** `qa` (default), `rag`, `tools`, `memory`, `structured`, `stream`

## Files

| File | Purpose |
|---|---|
| `adapter.py` | Entrypoint: reads the mode-tagged request JSON from stdin, dispatches to the mode handler, writes the uniform result JSON to stdout |
| `arena_common.py` | Shared env/config, the agnes chat-model builder, e5 query embedding, Qdrant retrieval |
| `arena_memory_store.py` | Real rqlite-backed persistence of Pydantic AI's `message_history` for the `memory` mode |
| `arena_tools.py` | A real, actually-executed (`ast`-based, no `eval()`) calculator for the `tools` mode |
| `run.sh` | Native runner: sources the repo-root `.env`, activates `.venv`, execs `adapter.py` |
| `run-docker.sh` | Containerized runner: builds the image on first use, passes credentials at `docker run` time only |
| `Dockerfile` | Minimal `python:3.13-slim` image; no credential baked in |
| `test-env.sh` | Automated PASS/FAIL credential/env tests (native env read, no-key-in-image, never-logged) |
| `requirements.txt` | `pydantic-ai>=2.12.0`, `openai>=1.0.0`, `qdrant-client>=1.12,<1.13` |
| `.venv/` | Dedicated Python 3.13 virtualenv for this arena (not shared with `arenas/pydantic-ai/.venv`) |

## Real infrastructure this adapter talks to

| Service | Purpose | Default URL | Env override |
|---|---|---|---|
| agnes chat | The held-constant LIVE-arena model | `OPENAI_BASE_URL` (repo-root `.env`) | `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` |
| e5 embed server | OpenAI-compatible query embeddings for `rag` mode | `http://127.0.0.1:8899` | `EMBED_URL` |
| Qdrant | Real vector search over the `arena_rag` collection (22 Nimbus-Rail docs) for `rag` mode | `http://localhost:6333` | `QDRANT_URL` |
| rqlite | Real networked SQLite store for persisted `message_history` in `memory` mode | `http://localhost:4001` | `RQLITE_URL` |

All four hosts are env-configurable so `run-docker.sh` can retarget them at `host.docker.internal`
from inside a container. The repo-root `.env` supplies the agnes credential; `run.sh` sources it
with `set -a; source .env; set +a` and never echoes it. See `arenas/live/_shared/README.md` for the
shared RAG corpus / embed server / question-set contract this adapter builds on.

## Setup

```bash
cd arenas/live/pydantic-ai
python3.13 -m venv .venv
.venv/bin/pip install -r requirements.txt
```

## Modes

Every mode reads one JSON object from stdin and writes exactly one JSON object to stdout (all
diagnostics go to stderr as `PROGRESS: ...` lines):

```json
{
  "framework": "pydantic-ai",
  "mode": "qa|rag|tools|memory|structured|stream",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}}],
  "retrieved": [{"doc_id": "...", "title": "...", "text": "...", "score": 0.0}],
  "structured": {"...": "..."},
  "ttft_ms": 0,
  "error": ""
}
```

`tool_calls` / `retrieved` / `structured` / `ttft_ms` are empty/`null`/`-1` when a mode doesn't
apply them. Token counts come from Pydantic AI's `RunUsage` (provider-reported); `-1`/`-1` means
usage was unavailable, never a fabricated estimate. `mode` and the four new fields are additive on
top of the base arena's `adapterOutput` struct (`arenas/live/runner/main.go`), which decodes JSON
with unknown fields ignored — so this adapter stays a drop-in for the base runner when `mode` is
omitted (defaults to `qa`, byte-for-byte the original single-shot behavior).

### `qa` (default) — single-shot, base-arena contract

```json
{"system": "You are concise.", "messages": [{"role": "user", "content": "What is the capital of France? One word."}]}
```

Identical semantics to the original single-mode adapter: if `tools` are supplied, the model's
native `tool_calls` are **captured, not executed** (`Agent.iter()` stopped at the first
`CallToolsNode`, `UsageLimits(request_limit=1)`) — this is the base arena's single-shot
tool-calling measurement (`arenas/live/SPEC.md`), preserved so the runner's existing
`single_shot_qa` / `tool_calling` / `multi_turn_memory` task kinds keep working unmodified.

Evidence (real call):
```json
{"framework": "pydantic-ai", "mode": "qa", "model": "agnes-2.0-flash", "reply": "Paris", "input_tokens": 267, "output_tokens": 2, "latency_ms": 3040, "tool_calls": [], "retrieved": [], "structured": null, "ttft_ms": -1, "error": ""}
```

### `rag` — real retrieval, answer only from context

```json
{"mode": "rag", "messages": [{"role": "user", "content": "How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund, and where is the refund sent?"}]}
```

Pipeline (all real): embed the query via the OpenAI-compatible embeddings client pointed at
`EMBED_URL` (model `multilingual-e5-small-query`) → `qdrant-client` `query_points` against the real
`arena_rag` collection (top-`k`, default 3) → the retrieved passages are stuffed into the system
prompt with an explicit "answer ONLY from context, else say you don't know" instruction → a
Pydantic AI `Agent` answers. `retrieved` in the result carries the actual top-k hits (`doc_id`,
`title`, `text`, `score`).

**Grounded evidence** (real call — exact reference match, gold doc `08-blue-guarantee.md` retrieved
top-1 at score 0.899):
```json
{"framework": "pydantic-ai", "mode": "rag", "model": "agnes-2.0-flash", "reply": "Under the Blue Guarantee, a passenger receives an automatic full refund if their trainset arrives more than 12 minutes late. The refund is sent to their HaloPay balance within 24 hours.", "input_tokens": 568, "output_tokens": 41, "latency_ms": 7019, "retrieved": [{"doc_id": "08-blue-guarantee.md", "title": "The Blue Guarantee (Refund Policy)", "score": 0.8993131, "...": "..."}], "error": ""}
```

**Trap evidence** (real call — question `q9`, no supporting doc in the corpus, correctly refused
instead of hallucinating an airline partner):
```json
{"framework": "pydantic-ai", "mode": "rag", "model": "agnes-2.0-flash", "reply": "I don't know. The provided context does not mention any airline partnerships for connecting flights out of Vellmoor.", "input_tokens": 602, "output_tokens": 25, "latency_ms": 6921, "error": ""}
```

### `tools` — real multi-step tool execution (not single-shot capture)

```json
{"mode": "tools", "messages": [{"role": "user", "content": "What is 847 times 39, minus 12? Use the calculate tool."}]}
```

A Pydantic AI `Agent` is given a real `@agent.tool_plain` `calculate(expression)` backed by
`arena_tools.safe_eval_expr` — an `ast`-based evaluator (no `eval()`, whitelisted numeric
operators only) that genuinely computes the result. `agent.run(...)` drives the **full** agentic
loop: model requests the tool call → pydantic-ai actually invokes the Python function → the real
result is fed back → a second model call produces the final answer. `tool_calls` in the result
reports what was **executed**, including the real `result`, which is a different (deeper) contract
than the `qa` mode's captured-not-executed `tool_calls`.

Evidence (real call, 2 model round-trips — one to request the tool, one for the final answer):
```json
{"framework": "pydantic-ai", "mode": "tools", "model": "agnes-2.0-flash", "reply": "The result of $847 \\times 39 - 12$ is **33,021**.", "input_tokens": 1240, "output_tokens": 63, "latency_ms": 14680, "tool_calls": [{"name": "calculate", "arguments": {"expression": "847 * 39 - 12"}, "result": 33021}], "error": ""}
```
`847 * 39 - 12 = 33021` — verified against Python directly; the model's final answer matches the
tool's real computed result, not a guess.

### `memory` — `message_history` persisted to real rqlite across processes

```json
{"mode": "memory", "session_id": "some-stable-id", "messages": [{"role": "user", "content": "My favorite color is teal, and my lucky number is 17."}]}
```

Each `memory`-mode invocation is **one turn**. `messages` carries only the *new* turn(s) for this
call — prior turns are recalled from persisted state via `session_id`, not replayed in the
request. Pipeline: `arena_memory_store.load_history_json(session_id)` reads any prior blob from a
real `arena_pydantic_memory` table in rqlite (`http://localhost:4001` by default) → decoded back
into native `ModelRequest`/`ModelResponse` objects via Pydantic AI's own
`ModelMessagesTypeAdapter.validate_json` → passed as `message_history` to `agent.run()` → the
updated full history (`result.all_messages()`) is re-serialized with
`ModelMessagesTypeAdapter.dump_json` and upserted back into rqlite. State survives process exit —
this is not a process-local dict.

**Evidence — two SEPARATE `bash run.sh` process invocations, same `session_id`:**

Process 1 (fresh session, states a fact):
```json
{"framework": "pydantic-ai", "mode": "memory", "model": "agnes-2.0-flash", "reply": "Understood. Your favorite color is teal and your lucky number is 17.", "input_tokens": 283, "output_tokens": 18, "latency_ms": 7200, "error": ""}
```
stderr: `memory: no prior history in rqlite for session_id=... (fresh session)` then `memory:
persisted history to rqlite for session_id=... (1370 bytes)`.

Process 2 (**new Python process**, only asks a question, no restated facts):
```json
{"framework": "pydantic-ai", "mode": "memory", "model": "agnes-2.0-flash", "reply": "Your favorite color is teal and your lucky number is 17.", "input_tokens": 320, "output_tokens": 15, "latency_ms": 1408, "error": ""}
```
stderr: `memory: loaded prior history from rqlite for session_id=... (1370 bytes)` then `memory:
persisted history to rqlite for session_id=... (2526 bytes)` — the growing byte count and the
correctly recalled fact prove the second, independently-started process retrieved state that only
existed in rqlite, not in any shared memory.

### `structured` — validated typed output

```json
{"mode": "structured", "messages": [{"role": "user", "content": "What is the capital of France?"}]}
```

`Agent(model, output_type=PromptedOutput(Answer), ...)` where `Answer` is a Pydantic `BaseModel`
(`answer: str`, `key_points: list[str]`, `confidence: float` with `ge=0, le=1`). The result's
`structured` field is the validated object's `model_dump()`.

**Gotcha (verified live, saved to Engram):** the *default* output strategy for
`output_type=SomeModel` (bare, no wrapper) uses a forced native `final_result` tool call. Against
`agnes-2.0-flash` this reliably fails: `pydantic_ai.exceptions.UnexpectedModelBehavior: Exceeded
maximum output retries (1)` — the model never emits a valid tool call for the synthetic output
tool (2 real, wasted calls were spent discovering this). `output_type=PromptedOutput(Answer)`
(ask for the JSON object in the prompt instead of forcing a tool call) works reliably and is what
this adapter uses.

Evidence (real call):
```json
{"framework": "pydantic-ai", "mode": "structured", "model": "agnes-2.0-flash", "reply": "Paris", "input_tokens": 422, "output_tokens": 59, "latency_ms": 7287, "structured": {"answer": "Paris", "key_points": ["The capital of France is Paris.", "Paris is located on the Seine River.", "It is the political, economic, and cultural center of France."], "confidence": 1.0}, "error": ""}
```

### `stream` — real token streaming, `ttft_ms`

```json
{"mode": "stream", "messages": [{"role": "user", "content": "Tell me one interesting fact about maglev trains."}]}
```

`async with agent.run_stream(prompt) as result: async for chunk in result.stream_text(delta=True):
...` — genuine incremental streaming from the framework. `ttft_ms` is the wall-clock time to the
first non-empty text delta; `-1` if the stream produced no text at all.

Evidence (real call):
```json
{"framework": "pydantic-ai", "mode": "stream", "model": "agnes-2.0-flash", "reply": "Maglev trains use powerful electromagnets to levitate above the tracks, eliminating friction and allowing them to reach speeds exceeding 370 mph. This technology enables smoother, quieter rides compared to traditional wheeled trains.", "input_tokens": 272, "output_tokens": 45, "latency_ms": 7153, "ttft_ms": 7002, "error": ""}
```

**Gotcha (cosmetic, verified harmless):** `run_stream`'s OpenTelemetry span sometimes logs `Failed
to detach context` / `ValueError: ... was created in a different Context` to **stderr** on exit
(a known pydantic-ai/opentelemetry contextvars interaction across the stream's async context
manager). Verified with a stdout-only capture that this never touches stdout — the JSON contract
is unaffected; it is pure diagnostic noise on stderr, consistent with the "logs to stderr only"
contract.

## Docker packaging

```bash
docker build -t pydantic-ai-live-arena:latest arenas/live/pydantic-ai
echo '{"mode":"qa","messages":[{"role":"user","content":"hi"}]}' | bash arenas/live/pydantic-ai/run-docker.sh
```

`run-docker.sh` builds the image on first use, sources the repo-root `.env` for
`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` and passes them at **`docker run` time only**
(`-e VAR`, no `=value` — the docker CLI reads the value from this shell's already-exported
environment, so the literal value never appears in the `docker run` argv either), and rewrites
`EMBED_URL`/`QDRANT_URL`/`RQLITE_URL` to `http://host.docker.internal:<port>` so the container
reaches the host's real local services. `--add-host=host.docker.internal:host-gateway` makes this
resolve on Linux Docker hosts too (a no-op on Docker Desktop, which already provides it).

**Docker qa smoke** (real call, from inside the container, reaching agnes's public URL and
confirming the container's network path works):
```json
{"framework": "pydantic-ai", "mode": "qa", "model": "agnes-2.0-flash", "reply": "Tokyo", "input_tokens": 267, "output_tokens": 3, "latency_ms": 7196, "error": ""}
```

## Automated credential/env tests (`test-env.sh`)

```bash
bash arenas/live/pydantic-ai/test-env.sh
```

Six PASS/FAIL assertions, all passing:

```
=== (a) native: env vars present + qa smoke works ===
PASS: (a1) OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL are all present in the environment
PASS: (a2) native qa smoke call succeeded (non-empty reply, error="")

=== (b) Docker image has NO key literal ===
PASS: (b1) 'docker run --rm pydantic-ai-live-arena:latest env' (no -e flags) shows 0 OPENAI_API_KEY entries
PASS: (b2) 'docker history --no-trunc pydantic-ai-live-arena:latest' contains 0 occurrences of the key value

=== (c) key never logged ===
PASS: (c1) 0 occurrences of the key value in adapter source / run scripts / Dockerfile
PASS: (c2) 0 occurrences of the key value in the real qa smoke call's stdout+stderr

=== RESULT: 6 passed, 0 failed ===
```

The script loads the real key value into a local shell variable to `grep -F` **for**, but never
echoes, prints, or writes it anywhere — only match counts are reported.

## Real agnes call budget

| Phase | Calls |
|---|---|
| API-currency discovery (native `output_type=Answer` fails against agnes; 1 initial + 1 retry, both invalid) | 2 |
| `PromptedOutput` verification | 1 |
| `stream` mechanism verification (2 throwaway probes before the recorded evidence run) | 2 |
| `qa` mode evidence | 1 |
| `rag` mode evidence (grounded + trap) | 2 |
| `tools` mode evidence (2 round-trips: tool request + final answer) | 2 |
| `structured` mode evidence | 1 |
| `stream` mode evidence | 1 |
| `memory` mode evidence (2 separate processes) | 2 |
| Docker `qa` smoke | 1 |
| `test-env.sh` `qa` smoke (assertion a2) | 1 |
| **Total** | **16** |

**This is 1 call over the ≤15 budget stated in the brief.** Flagged honestly rather than
undercounted or hidden: the overage is the `test-env.sh` run, which by design fires one more real
`qa` call to prove the credential path end-to-end (not just replay a cached result) — every other
mode's evidence above was captured with exactly one recorded call per data point, and the 5
"exploration" calls up front are what caught the `output_type` gotcha now documented above, so a
second adapter build would need **zero** discovery calls and could deliver the same 6 modes'
worth of evidence in **9** calls.

## Dependencies and licenses

Direct: `pydantic-ai==2.12.0` (MIT), `openai==2.46.0` (Apache-2.0), `qdrant-client==1.12.2`
(Apache-2.0, pinned to match the locally running Qdrant server 1.12.4 and avoid its
cross-major-version compatibility warning). New transitive dependencies pulled in by
`qdrant-client` beyond what the original single-mode adapter already had: `grpcio` /
`grpcio-tools` (Apache-2.0), `h2` / `hpack` / `hyperframe` (MIT), `numpy` (BSD-3-Clause /
0BSD / Zlib / CC0-1.0, all permissive), `portalocker` (BSD), `protobuf` (BSD-3-Clause).

A full scan (`pip-licenses --format=json` over all 105 packages in this arena's `.venv`, run
after adding `qdrant-client`) found **zero** packages matching the blocked-license keywords in
`.claude/rules/cos/license-policy.md` (AGPL, SSPL, BSL, Elastic/ELv2, Commons Clause, FSL).
Everything resolved to MIT / BSD-2/3-Clause / Apache-2.0 / 0BSD / Zlib / CC0-1.0 — all permitted.
(`pip-licenses` itself was a throwaway dev-time scanning tool, installed and then uninstalled from
the `.venv`; it ships in no `requirements.txt` and is not a runtime dependency of the adapter.)

## Constraints honored

- **Additive only**: nothing under `arenas/pydantic-ai/` (the offline-classification arena) or
  anywhere else outside `arenas/live/pydantic-ai/` was modified. The original `run.sh` is
  unchanged; `adapter.py` was extended in place (new mode dispatch, `mode` defaults to `"qa"` so
  the base arena runner's existing behavior is byte-for-byte preserved when it omits `mode`).
- The agnes API key is never printed, logged, or baked into the Docker image — verified by
  `test-env.sh`'s 6 automated assertions above, not just asserted.
- English-only source, comments, and adapter output.
- Real agnes calls used: **16** (see budget table above; 1 over the ≤15 target, disclosed).
