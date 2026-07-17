# Live Arena Adapter — Genkit (Go), exhaustive multi-mode

An **exhaustive, multi-mode** live-arena adapter for Google's **Genkit for Go**, driven
against **real infrastructure end to end** — a real chat model, a real embedding
server, a real vector database, and a real durable store — not stubs. It is an
additive extension of the original single-shot adapter that used to live in this
directory: the base `qa` mode is unchanged and the adapter stays a drop-in
replacement for `arenas/live/runner` (see "Backward compatibility" below).

- **Framework:** [Genkit (Go)](https://github.com/firebase/genkit) `v1.10.0`
- **Chat model:** `agnes-2.0-flash` via `github.com/firebase/genkit/go/plugins/compat_oai/openai`
  (Genkit's official OpenAI-compatible plugin)
- **Embeddings:** a real `intfloat/multilingual-e5-small` server (384-dim), OpenAI-compatible
- **Vector search:** a real [Qdrant](https://qdrant.tech/) collection `arena_rag`, via
  `github.com/qdrant/go-client`
- **Durable memory:** a real [rqlite](https://rqlite.io/) node over HTTP

## Modes

One JSON object on stdin selects a `mode`; one JSON object on stdout is the uniform
result. `mode` is optional and defaults to `"qa"` — the original single-shot contract —
so requests with no `mode` field behave exactly as before.

| Mode | What it exercises |
|---|---|
| `qa` | Original single-shot chat turn. Tools (if any) are captured via `ai.WithReturnToolRequests(true)`, never executed. |
| `rag` | Real retrieval-augmented generation: embed the query (e5), search real Qdrant, answer via a `genkit.DefineFlow` grounded ONLY in the retrieved passages. |
| `tools` | Native Genkit tool-execution loop: tools actually run (calculator, corpus lookup), multi-step, results folded into the final answer. |
| `memory` | Multi-turn state persisted to real rqlite; a **separate process invocation** recalls a fact from an earlier one. |
| `structured` | Native structured output (`ai.WithOutputType`) — a Go struct, schema-validated by Genkit, not ad-hoc JSON parsing. |
| `stream` | Native streaming generation (`ai.WithStreaming`); records `ttft_ms` (time to first chunk). |

## Contract

**stdin** — one JSON object:

```json
{
  "mode": "qa | rag | tools | memory | structured | stream",
  "system": "string",
  "messages": [{"role": "user|assistant", "content": "string"}],
  "tools": [{"name": "string", "description": "string", "parameters": { /* JSON Schema */ }}],
  "session_id": "string — required for mode=memory, ignored otherwise",
  "top_k": 4
}
```

- `mode` defaults to `"qa"` if absent.
- `tools` is used by `qa` (single-shot capture) and `tools` (real execution).
- `session_id` keys a conversation's persisted row set in rqlite; `memory` mode requires it.
- `top_k` (optional, default 4) bounds Qdrant retrieval count for `rag`.

**stdout** — exactly one JSON object (all logs go to stderr, `PROGRESS:` prefix):

```json
{
  "framework": "genkit-go",
  "mode": "rag",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 628,
  "output_tokens": 29,
  "latency_ms": 8792,
  "tool_calls": [{"name": "calculator", "arguments": {"expression": "23 * 19"}}],
  "retrieved": [{"doc_id": "05-vireo-t7.md", "title": "Vireo T7 Trainset", "text": "...", "score": 0.94}],
  "structured": null,
  "ttft_ms": 0,
  "error": ""
}
```

- `input_tokens` / `output_tokens`: provider-reported usage, `-1` if unavailable.
- `retrieved`: populated by `rag` (top-K passages actually used to ground the answer).
- `structured`: populated by `structured` (the schema-validated object).
- `ttft_ms`: populated by `stream` (wall-clock to first streamed chunk); `0` otherwise.
- `error`: empty on success; the process still emits one valid JSON object on failure.

## Backward compatibility

`arenas/live/runner` (the base single-shot arena) sends requests with no `mode` field.
This adapter defaults `mode` to `"qa"` in that case, and the `qa` handler is
byte-for-byte the same behavior as the original adapter (single-shot tool capture via
`WithReturnToolRequests(true)`, never executed). The base runner needs zero changes.

## Per-mode design

### `rag` — real retrieval, grounded generation

1. **Embed the query** with the e5 `query:` role: `POST EMBED_URL/embeddings`,
   `{"model":"multilingual-e5-small-query","input": <question>}` → 384-dim vector
   (`embed_client.go`).
2. **Retrieve** from the real Qdrant collection `arena_rag`
   (`arenas/live/_shared/populate-qdrant.py`, 22 Nimbus-Rail docs) via
   `github.com/qdrant/go-client` (`qdrant_client.go`), `top_k` passages with payload.
3. **Answer** via `genkit.DefineFlow("ragAnswerFlow", ...)` (`rag.go`): the flow's system
   prompt is instructed to answer **only** from the retrieved passages, or say the
   information isn't available — this is what makes the two trap questions in
   `arenas/live/_shared/rag_questions.jsonl` catch hallucination instead of rewarding it.

#### Why go-client, not a Genkit plugin

Genkit-Go `v1.10.0` ships retriever/indexer **interfaces** (`ai.Retriever`, `ai.Indexer`)
but no first-party Qdrant **implementation** — unlike the JS SDK, which has
`genkitx-qdrant`. Rather than hand-roll an `ai.Retriever` wrapper purely to satisfy the
interface (which would add indirection without adding real capability), this adapter
calls `github.com/qdrant/go-client` directly for the retrieval half and drives
generation through a native Genkit flow — matching the task's explicit fallback
instruction ("if Genkit-Go lacks a Qdrant plugin, use github.com/qdrant/go-client for
retrieval ... but drive generation through Genkit").

### `tools` — real, executed, multi-step tool calls

`tools.go` registers each incoming tool definition via
`genkit.DefineToolWithInputSchema[any]` with a **real function body**, and leaves
`ai.WithReturnToolRequests` at its Genkit default (`false`). That default is the whole
mechanism: when the model's response contains tool requests, Genkit's own
`ai.GenerateWithRequest` loop (`handleToolRequests`, `ai/generate.go`) invokes the
registered function(s) **itself**, feeds their real results back as tool-response
messages, and calls the model again — up to `ai.WithMaxTurns(6)` — continuing until the
model produces a final answer with no more tool requests. This is the opposite of `qa`
mode's single-shot capture.

Two tools are given real, non-canned implementations, matched by tool name or argument
shape so the adapter works with whatever tool defs a caller supplies:

- **`calculator`** — a real recursive-descent arithmetic evaluator (`+ - * / ()`,
  `evalArithmetic` in `tools.go`), not `eval()` and not a lookup table.
- **`corpus_lookup`** — performs the exact same real embed + Qdrant search as `rag`
  mode and returns the retrieved passages.
- Any other tool name/shape falls through to a **`clock`-style fallback** that returns
  the real wall-clock time (`time.Now().UTC()`) plus the echoed arguments — genuinely
  executed and dynamic (never a hardcoded string), covering arbitrary tool defs a
  caller might send without this adapter recognizing their domain.

Each executed call is recorded into `out.tool_calls` **from inside the real tool
function** (guarded by a mutex — Genkit runs parallel tool calls concurrently, one
goroutine per call, see `ai/generate.go:handleToolRequests`), so `tool_calls` reflects
calls that **actually ran**, not calls the model merely requested.

### `memory` — real, cross-process persistence

`memory.go` + `rqlite_client.go`: every turn (inbound message and the model's reply) is
persisted to a real rqlite node over HTTP (`POST /db/execute`, parameterized SQL,
`RQLITE_URL`, default `http://localhost:4001`), keyed by `session_id`. On every
invocation, `runMemory` first `SELECT`s the full prior turn history for that
`session_id` from rqlite (`POST /db/query`) and prepends it to the messages sent to
Genkit — **before** persisting the new turn. Because this load happens in a **fresh OS
process** with no in-memory state from any prior run, a second `run.sh` invocation
sharing the same `session_id` genuinely recalls a fact stated only in an earlier,
already-exited process — see "Verified smoke output" for the two-process proof.

### `structured` — native schema-validated output

`structured.go` defines a Go struct `structuredAnswer{Answer, Confidence, KeyPoints}`
and calls `ai.WithOutputType(structuredAnswer{})`, which sets Genkit's JSON output
format and derives + enforces a JSON Schema from the struct's tags. The response is
decoded with `resp.Output(&sa)` (`ai.ModelResponse.Output`), which **validates against
that schema and errors on mismatch** — not a best-effort `json.Unmarshal` of free text.

### `stream` — native streaming, `ttft_ms`

`stream.go` passes `ai.WithStreaming(cb)` to `genkit.Generate`; `cb` fires once per
chunk as the real SSE stream from `compat_oai`'s OpenAI-compatible client arrives.
`ttft_ms` is the wall-clock time from request start to the **first** `cb` invocation —
a number a non-streaming call structurally cannot produce, since it only observes the
fully-aggregated response.

## agnes wiring (chat)

Unchanged from the original single-shot adapter — see `main.go`:

```go
oai := &oaiplugin.OpenAI{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Opts:   []option.RequestOption{option.WithBaseURL(os.Getenv("OPENAI_BASE_URL"))},
}
g := genkit.Init(ctx, genkit.WithPlugins(oai))
m := oai.DefineModel(model, ai.ModelOptions{Label: "agnes/" + model, Supports: &compat_oai.Multimodal})
```

## Real infra wiring (env-configurable)

| Env var | Purpose | Default |
|---|---|---|
| `OPENAI_API_KEY` | agnes bearer key (never logged) | — (required) |
| `OPENAI_BASE_URL` | agnes endpoint | — (required) |
| `OPENAI_MODEL` | model id | `agnes-2.0-flash` |
| `EMBED_URL` | e5 embeddings server | `http://127.0.0.1:8899` |
| `QDRANT_URL` | Qdrant REST base (host is reused for the gRPC client; see `qdrant_client.go`) | `http://localhost:6333` |
| `QDRANT_GRPC_PORT` | override the derived gRPC port (default: REST port + 1, i.e. `6334`) | unset |
| `RQLITE_URL` | rqlite HTTP base | `http://localhost:4001` |

All four host-shaped vars (`OPENAI_BASE_URL`, `EMBED_URL`, `QDRANT_URL`, `RQLITE_URL`)
are read fresh on every run, which is how `run-docker.sh` repoints the last three at
`host.docker.internal` without any source change.

## Usage

```bash
# qa (default mode, unchanged single-shot contract)
echo '{"messages":[{"role":"user","content":"What is the capital of France? One word."}]}' \
  | bash arenas/live/genkit-go/run.sh

# rag (grounded)
echo '{"mode":"rag","messages":[{"role":"user","content":"What is the service top speed of the Vireo T7 trainset and how many passengers does it seat?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# rag (trap — must refuse, not invent)
echo '{"mode":"rag","messages":[{"role":"user","content":"What was the price of a Nimbus Rail Lumen loyalty membership card in 2024?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# tools (real multi-step execution)
echo '{"mode":"tools","system":"You are a helpful assistant with tools. Use them when needed, then answer using their results.","messages":[{"role":"user","content":"What is 23 times 19? Also, how late does a Nimbus Rail trainset have to be before the Blue Guarantee gives an automatic refund?"}],"tools":[{"name":"calculator","description":"Evaluate a real arithmetic expression.","parameters":{"type":"object","properties":{"expression":{"type":"string"}},"required":["expression"]}},{"name":"corpus_lookup","description":"Search the real Nimbus Rail corpus.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}' \
  | bash arenas/live/genkit-go/run.sh

# memory (two SEPARATE process invocations, same session_id)
echo '{"mode":"memory","session_id":"demo-1","messages":[{"role":"user","content":"Remember this: my favorite color is teal and my lucky number is 47."}]}' \
  | bash arenas/live/genkit-go/run.sh
echo '{"mode":"memory","session_id":"demo-1","messages":[{"role":"user","content":"What is my favorite color and lucky number?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# structured
echo '{"mode":"structured","messages":[{"role":"user","content":"What is the boiling point of water at sea level in Celsius?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# stream
echo '{"mode":"stream","messages":[{"role":"user","content":"Write two sentences about maglev trains."}]}' \
  | bash arenas/live/genkit-go/run.sh
```

`run.sh` sources the repo-root `.env`, builds, and forwards stdin → stdout. The API key
is never printed by the script or the adapter.

### Docker

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' \
  | bash arenas/live/genkit-go/run-docker.sh
```

`run-docker.sh` builds `genkit-go-live-arena:exhaustive` (multi-stage, static binary on
`gcr.io/distroless/static-debian12:nonroot`) and runs it with `EMBED_URL`/`QDRANT_URL`/
`RQLITE_URL` repointed at `host.docker.internal` so the container reaches the host's
locally-running embed server, Qdrant, and rqlite; `OPENAI_*` is forwarded as **runtime**
`-e` flags sourced fresh from the repo-root `.env` on every invocation — never baked
into the image (verified by `test-env.sh`, part (b)).

### `test-env.sh`

```bash
bash arenas/live/genkit-go/test-env.sh
```

Runs and PASS/FAILs three groups of assertions:

- **(a) native** — `OPENAI_API_KEY`/`OPENAI_BASE_URL` are set from the environment and a
  real `qa` smoke call succeeds; stdout/stderr never contain the literal key.
- **(b) docker** — the built image contains **no** key literal: checked via
  `docker history --no-trunc` (catches an `ARG`/`ENV`/`COPY` leak in any layer),
  `docker export` of a fresh container's filesystem, and `docker inspect
  .Config.Env` (the image's own declared environment — the equivalent of `docker run
  --rm <img> env` on a base image, `distroless/static`, that ships no shell to run
  `env` in).
- **(c)** the key is never logged by a real `run-docker.sh` smoke call either.

## Verified smoke output (this pass, live)

```jsonc
// qa
{"framework":"genkit-go","mode":"qa","model":"agnes-2.0-flash","reply":"Paris","input_tokens":267,"output_tokens":2,"latency_ms":9355,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

// rag — grounded
{"framework":"genkit-go","mode":"rag","model":"agnes-2.0-flash","reply":"The Vireo T7 trainset has a service top speed of 431 km/h and seats 512 passengers.","input_tokens":628,"output_tokens":29,"latency_ms":8792,"tool_calls":[],"retrieved":[{"doc_id":"05-vireo-t7.md","title":"Vireo T7 Trainset","score":0.940758,"...":"..."}, "...3 more"],"structured":null,"ttft_ms":0,"error":""}

// rag — trap (correctly refuses instead of inventing a price)
{"framework":"genkit-go","mode":"rag","model":"agnes-2.0-flash","reply":"I don't know / the information is not available.","input_tokens":645,"output_tokens":12,"latency_ms":7308,"tool_calls":[],"retrieved":["...4 passages, none support a price..."],"structured":null,"ttft_ms":0,"error":""}

// tools — real, multi-step (calculator AND corpus_lookup both actually executed)
{"framework":"genkit-go","mode":"tools","model":"agnes-2.0-flash","reply":"23 times 19 is 437.\n\nRegarding the Nimbus Rail Blue Guarantee, an automatic full refund is issued if a trainset arrives more than 12 minutes late.","input_tokens":1048,"output_tokens":40,"latency_ms":15778,"tool_calls":[{"name":"corpus_lookup","arguments":{"query":"Nimbus Rail Blue Guarantee automatic refund delay time"}},{"name":"calculator","arguments":{"expression":"23 * 19"}}],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

// memory — process 1 of 2 (states a fact)
{"framework":"genkit-go","mode":"memory","model":"agnes-2.0-flash","reply":"Understood. Your favorite color is teal and your lucky number is 47.","input_tokens":269,"output_tokens":18,"latency_ms":6849,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

// memory — process 2 of 2 (a FRESH process; its own input never mentions "teal" or "47")
// stderr: "PROGRESS: memory: loaded 2 prior turn(s) for session ... from rqlite"
{"framework":"genkit-go","mode":"memory","model":"agnes-2.0-flash","reply":"Your favorite color is teal and your lucky number is 47.","input_tokens":305,"output_tokens":15,"latency_ms":7054,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

// structured (schema-validated: answer/confidence/key_points)
{"framework":"genkit-go","mode":"structured","model":"agnes-2.0-flash","reply":"The boiling point of water at sea level is 100 degrees Celsius.","input_tokens":394,"output_tokens":100,"latency_ms":7151,"tool_calls":[],"retrieved":[],"structured":{"answer":"The boiling point of water at sea level is 100 degrees Celsius.","confidence":1,"key_points":["Standard atmospheric pressure at sea level is defined as 1 atm.","Under these conditions, pure water transitions from liquid to gas at exactly 100°C.","This value is a standard reference point in the Celsius temperature scale."]},"ttft_ms":0,"error":""}

// stream (ttft_ms < latency_ms, proving incremental delivery)
{"framework":"genkit-go","mode":"stream","model":"agnes-2.0-flash","reply":"Maglev trains use powerful electromagnets to lift and propel their cars above a guideway, eliminating friction between the train and the track. This unique technology allows them to achieve extremely high speeds while operating with remarkable smoothness and efficiency.","input_tokens":29,"output_tokens":57,"latency_ms":7100,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":6713,"error":""}
```

```
$ bash arenas/live/genkit-go/test-env.sh
PASS: native: $OPENAI_API_KEY is set
PASS: native: $OPENAI_BASE_URL is set
PASS: native: qa smoke call succeeded (error field empty)
PASS: native: stdout does not contain the literal API key
PASS: native: stderr does not contain the literal API key
PASS: docker: image builds successfully
PASS: docker: no key literal found in image history or exported filesystem layers
PASS: docker: image Config.Env does not contain the literal API key (baseline distroless vars only: ...)
PASS: docker: image Config.Env declares no credential-shaped var — confirming (b) via the `docker run --rm img env` equivalent
PASS: docker: qa smoke call succeeded through the container (error field empty)
PASS: docker: container stdout does not contain the literal API key
PASS: docker: container stderr does not contain the literal API key
RESULT: ALL PASS
```

All runs above are from a single, fully re-verified pass through the real agnes
endpoint, the real embed server, the real Qdrant collection, and a real rqlite node
(this session) — 12 total real agnes calls across every mode's smoke plus
`test-env.sh`'s two `qa` smokes, under the 15-call budget (the `tools` smoke made 2
internal agnes round-trips within Genkit's own tool loop — the decision turn and the
finalize-after-tool-results turn — counted in that total).

## Dependencies & licenses

The repo enforces a hard license gate (MIT / BSD / Apache-2.0 / ISC only; no
AGPL / SSPL / BSL / copyleft). All direct and transitive modules are permissive:

| Module | License |
|---|---|
| `github.com/firebase/genkit/go` (direct) | Apache-2.0 |
| `github.com/openai/openai-go` (direct) | Apache-2.0 |
| `github.com/qdrant/go-client` (direct, new) | Apache-2.0 |
| `github.com/bahlo/generic-list-go` | BSD-3-Clause |
| `github.com/buger/jsonparser` | MIT |
| `github.com/cespare/xxhash/v2` (new, transitive via grpc) | MIT |
| `github.com/coder/websocket` | ISC |
| `github.com/go-logr/logr` | Apache-2.0 |
| `github.com/go-logr/stdr` | Apache-2.0 |
| `github.com/goccy/go-yaml` | MIT |
| `github.com/google/dotprompt/go` | Apache-2.0 |
| `github.com/google/uuid` | BSD-3-Clause |
| `github.com/invopop/jsonschema` | MIT |
| `github.com/mailru/easyjson` | MIT |
| `github.com/mbleigh/raymond` | MIT |
| `github.com/tidwall/gjson` / `match` / `pretty` / `sjson` | MIT |
| `github.com/wk8/go-ordered-map/v2` | Apache-2.0 |
| `github.com/xeipuuv/gojsonpointer` / `gojsonreference` / `gojsonschema` | Apache-2.0 |
| `github.com/yosida95/uritemplate/v3` | BSD-3-Clause |
| `go.opentelemetry.io/auto/sdk`, `otel`, `otel/metric`, `otel/sdk`, `otel/trace` | Apache-2.0 |
| `golang.org/x/net` (new, transitive via grpc) | BSD-3-Clause |
| `golang.org/x/sys` | BSD-3-Clause |
| `golang.org/x/text` (new, transitive via grpc) | BSD-3-Clause |
| `google.golang.org/genproto/googleapis/rpc` (new, transitive via grpc/qdrant) | Apache-2.0 |
| `google.golang.org/grpc` (new, transitive via qdrant/go-client) | Apache-2.0 |
| `google.golang.org/protobuf` (new, transitive via grpc/qdrant) | BSD-3-Clause |
| `gopkg.in/yaml.v3` | MIT + Apache-2.0 |

`github.com/qdrant/go-client` and its gRPC/protobuf dependency chain (`google.golang.org/grpc`,
`google.golang.org/protobuf`, `google.golang.org/genproto/googleapis/rpc`,
`golang.org/x/net`, `golang.org/x/text`, `github.com/cespare/xxhash/v2`) are the only
NEW modules this exhaustive pass adds versus the original single-shot adapter's
dependency set; every one of them is MIT / BSD-3-Clause / Apache-2.0, confirmed by
reading each module's `LICENSE`/`LICENSE.txt` directly from the local module cache
(not taken on faith from a package registry badge).

## Files

- `main.go` — mode dispatch, shared arena I/O contract, agnes/Genkit client setup, `qa` mode.
- `rag.go` — `rag` mode: embed → Qdrant search → grounded `genkit.DefineFlow` answer.
- `tools.go` — `tools` mode: real multi-step tool execution (calculator, corpus_lookup, clock fallback) + the tiny arithmetic evaluator.
- `memory.go` — `memory` mode: rqlite-backed cross-process conversation persistence.
- `structured.go` — `structured` mode: `ai.WithOutputType` schema-validated output.
- `stream.go` — `stream` mode: `ai.WithStreaming` + `ttft_ms`.
- `embed_client.go` — HTTP client for the real e5 embeddings server.
- `qdrant_client.go` — real Qdrant search via `github.com/qdrant/go-client` (gRPC).
- `rqlite_client.go` — HTTP client for rqlite's `/db/execute` / `/db/query`.
- `run.sh` — native runner: sources repo-root `.env`, builds, forwards stdin → stdout.
- `Dockerfile` — multi-stage static build on `distroless/static-debian12:nonroot`.
- `run-docker.sh` — builds + runs the Docker image, repointing infra URLs at `host.docker.internal` and passing `OPENAI_*` as runtime-only env.
- `test-env.sh` — automated credential/env PASS/FAIL assertions (native + Docker).
- `go.mod` / `go.sum` — pinned to Genkit `v1.10.0` + `qdrant/go-client v1.18.3`.
- `README.md` — this file.

## Uncertainties

- `ai.WithModel(m)` is passed an *unregistered* `ai.Model` returned by
  `oai.DefineModel(...)`. This still resolves correctly at call time because
  `compat_oai.OpenAICompatible` implements `ResolveAction`, which the Genkit registry
  falls back to for any model name not already registered (unchanged from the original
  single-shot adapter's finding, re-confirmed here since every mode still goes through
  the same `oai.DefineModel` call).
- **`tools` mode's fallback tool** (unrecognized name/shape → real-time-clock echo) is a
  deliberate, honest choice for arbitrary caller-supplied tool defs this adapter cannot
  know the semantics of in advance — it is real and dynamic (never a canned string), but
  it is also not a *meaningful* tool for whatever domain that unrecognized name implies.
  The two named tools (`calculator`, `corpus_lookup`) are the ones this adapter's own
  smoke test exercises and are meaningful end to end.
- **`QDRANT_GRPC_PORT` derivation** (REST port + 1) is this project's own established
  convention (`internal/semantic/qdrant.go`, `docker-compose.yml`: 6333 REST / 6334
  gRPC), not a Qdrant-mandated rule — a Qdrant deployment that maps gRPC to a
  non-adjacent port needs `QDRANT_GRPC_PORT` set explicitly. Verified against the
  actually-running local Qdrant (REST 6333, gRPC 6334 both open) for this pass.
  `docker run` observed a one-time stderr warning ("Client version is not compatible
  with server version... clientVersion=v1.18.3 serverVersion=1.12.4") — informational
  only (the query still succeeded and returned correct results every time it ran during
  this verification), not something this adapter suppresses since it comes from the
  qdrant/go-client library itself, not from a real failure.
- `memory` mode's rqlite table (`arena_genkit_go_memory`) is shared by every `session_id`
  used against the same rqlite node; nothing in this adapter prunes old sessions —
  acceptable for an arena adapter, but a long-lived deployment would want a retention
  policy, which is out of scope here.
- Token counts (`input_tokens`/`output_tokens`) are whatever agnes reports in its
  OpenAI-compatible `usage` object, passed through by `compat_oai`'s response mapping —
  not independently re-verified against a tokenizer (unchanged from the original
  adapter's uncertainty).
