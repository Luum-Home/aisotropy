# eino-live-arena

An **exhaustive, multi-mode LIVE arena adapter** for [CloudWeGo Eino](https://github.com/cloudwego/eino),
run against REAL infrastructure: the real `agnes-2.0-flash` chat model, a real e5 embeddings
server, a real Qdrant collection, and a real on-disk SQLite store. Every mode below drives an
Eino **native** component (chat model, embedding, chat template + compose.Chain, ReAct agent,
streaming) — the only places this adapter reaches outside Eino are (a) the real Qdrant search
call, because the `eino-ext` Qdrant retriever component hardcodes a payload key this arena's
collection doesn't use (explained in [RAG mode](#rag-mode) below), and (b) the on-disk SQLite
persistence layer for `memory` mode, because Eino has no built-in persisted-memory component.

This is separate from `arenas/eino/` (the stub-model feature-parity arena) and extends the
original single-shot `arenas/live/eino/` adapter (`qa` mode below is that adapter, unchanged)
into six modes selected by a `"mode"` field on stdin.

## Contract

Reads **one JSON object from stdin**:

```jsonc
{
  "mode": "qa | rag | tools | memory | structured | stream",  // default "qa"
  "system": "string",                        // optional system prompt
  "messages": [{"role": "user|assistant", "content": "string"}],
  "tools": [{"name": "...", "description": "...", "parameters": {/* JSON-Schema */}}], // qa mode only
  "session_id": "string",                    // memory mode: required, correlates turns across process invocations
  "top_k": 4,                                 // rag mode: retrieval depth (optional, default 4)
  "schema": {/* JSON-Schema object */},       // structured mode: override the default schema (optional)
  "schema_name": "string"                     // structured mode: schema name (optional, default "structured_answer")
}
```

Writes **exactly one JSON object to stdout** (all logs go to stderr):

```jsonc
{
  "framework": "eino",
  "mode": "qa | rag | tools | memory | structured | stream",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}, "result": "string (tools mode only)"}],
  "retrieved": [{"doc_id": "string", "title": "string", "score": 0.0, "text": "string"}],
  "structured": null,
  "ttft_ms": 0,
  "error": "string"
}
```

`tool_calls[].result` and `retrieved`/`structured`/`ttft_ms` are populated only by the modes
that use them (see below); otherwise `retrieved`/`tool_calls` are `[]` and `structured` is
`null`. `input_tokens`/`output_tokens` are always the real provider-reported usage (summed
across every internal model call the mode makes), or `-1` if genuinely unavailable — never a
fabricated number.

## Modes

### `qa` — single-shot (original adapter, unchanged)

One `Generate` call against agnes via `eino-ext/components/model/openai`. Optional `tools`
input is bound single-shot (captured, not executed) — see `tools` mode below for real
multi-step tool execution. Implementation: `mode_qa.go`.

### `rag` mode

Real retrieval-augmented generation against the `_shared/` Nimbus-Rail corpus, driven through
an Eino `compose.Chain[string, *schema.Message]` (`mode_rag.go`):

1. **Lambda node** (`compose.InvokableLambda`): embeds the last user message with the real e5
   server (`eino-ext/components/embedding/openai`, `Model: "multilingual-e5-small-query"`,
   `BaseURL: EMBED_URL`), then searches the real Qdrant collection.
2. **ChatTemplate node** (`eino/components/prompt`, `prompt.FromMessages` with
   `schema.MessagesPlaceholder("history", true)`): builds the grounded prompt — "answer only
   from context, else say you don't know" — from the retrieved passages plus any prior turns.
3. **ChatModel node**: the real agnes chat model answers.

**Why the Qdrant search itself uses `github.com/qdrant/go-client` directly, not the
`eino-ext/components/retriever/qdrant` component:** that component's `Retrieve` hardcodes
`defaultContentKey = "content"` / `defaultMetadataKey = "metadata"` (see its `consts.go`) with
no config option to override them. This arena's `arena_rag` collection payload is
`{"doc_id", "title", "text"}` (see `../_shared/README.md`) — using the component as-is would
silently retrieve documents with an **empty** `Content` field (`content` isn't a payload key
here), breaking grounding and giving false negatives on the trap questions. Per the task's own
fallback instruction ("if a component is missing, use the qdrant-go client... directly but
still drive the flow through Eino's graph"), the retrieval call goes straight to
`qdrant.Client.Query` **inside an Eino Lambda node**, so the overall flow is still a genuine
Eino `compose.Chain` — only the leaf HTTP/gRPC call bypasses a component whose fixed schema
assumption doesn't match this arena's data.

`retrieved` is populated with every passage the chain actually retrieved (doc_id, title,
score, full text) so a grader can verify the gold document was in context.

### `tools` mode

A real Eino ReAct agent (`github.com/cloudwego/eino/flow/agent/react`) with one real,
actually-executed tool: `calculate` (a self-contained recursive-descent arithmetic evaluator in
`calc.go` — no third-party expression-eval dependency for one tool). Implementation:
`mode_tools.go`.

This is genuinely multi-step, not single-shot capture: the system prompt instructs the model to
call `calculate` for any arithmetic; the ReAct graph (`react.NewAgent`) executes the tool for
real, feeds the numeric result back to the model, and the model's **second** call produces the
final natural-language answer using that result. `runTools` returns an error if the agent
completes without ever calling the tool, so a broken tool-binding fails loudly instead of
silently degrading to a plain-text guess.

Token usage and each real tool call (arguments + actual result) are captured via Eino's native
`callbacks.Handler` (`compose.WithCallbacks`), filtered by `components.ComponentOfChatModel` /
`components.ComponentOfTool` and correlated per-call via `compose.GetToolCallID` — not
re-derived from the final message alone, which would only reflect the *last* of the (at least
two) internal model calls.

### `memory` mode

Persists multi-turn conversation state to a **real on-disk SQLite database**
(`modernc.org/sqlite`, pure Go, no CGO) keyed by `session_id` — never an in-process map.
Implementation: `mode_memory.go`.

Each invocation: load every prior turn for `session_id` from SQLite → append the new
`messages[]` from this call → `Generate` against the full history → persist the new user
turn(s) plus the assistant's reply. Because persistence is a real file on disk, a **second,
completely separate process invocation** with the same `session_id` recalls facts from the
first — see [Verified smoke tests](#verified-smoke-tests-real-agnes-calls) below for the actual
two-process proof.

The chat call itself still goes through Eino's native `ChatModel.Generate` — only the
cross-process persistence layer is custom code, since Eino has no built-in persisted-memory
component to drive natively.

DB path: `EINO_ARENA_MEMORY_DB` (default `.data/memory.db`, relative to this directory since
`run.sh` already `cd`s here; the Docker image sets it to the absolute `/data/memory.db`, see
[Packaging](#packaging)).

### `structured` mode

Binds Eino's native structured-output config (`ChatModelConfig.ResponseFormat`, forwarded to
agnes as the OpenAI `response_format: {type: "json_schema", ...}` field) so the reply is a
schema-validated JSON object, then parses it into `structured`. Implementation:
`mode_structured.go`.

Default schema (used when the request doesn't supply its own `"schema"`):

```json
{"type": "object", "properties": {"answer": {"type": "string"}, "confidence": {"type": "string", "enum": ["high","medium","low"]}}, "required": ["answer","confidence"], "additionalProperties": false}
```

Because not every OpenAI-compatible provider enforces `response_format` strictly, the system
prompt *also* spells out the schema and instructs JSON-only output (belt and suspenders), and a
provider that rejects the field outright falls back to plain generation. A JSON parse failure
degrades to `structured: null` rather than failing the call — this mode is a SHOULD, not a MUST.

### `stream` mode

Drives `BaseChatModel.Stream` (Eino's native streaming call, distinct from `Generate`) and
records `ttft_ms` — wall-clock from request start to the first received chunk. Chunks are
concatenated into `reply`; usage comes from whichever chunk carries it (the vendored OpenAI
client already requests `stream_options.include_usage=true` on every streaming call). See
`mode_stream.go`.

## Real infrastructure this adapter reaches

| Service | Reached via | Env var (default) |
|---|---|---|
| agnes-2.0-flash chat | `eino-ext/components/model/openai` | `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` (required, no defaults — see `run.sh`) |
| e5 embeddings server | `eino-ext/components/embedding/openai` | `EMBED_URL` (`http://127.0.0.1:8899`), `EMBED_MODEL` (`multilingual-e5-small-query`) |
| Qdrant (`arena_rag`) | `github.com/qdrant/go-client` (gRPC) | `QDRANT_URL` (`http://localhost:6333`, REST-style — hostname is parsed out), `QDRANT_GRPC_PORT` (derived: REST port + 1 = `6334` unless overridden), `QDRANT_COLLECTION` (`arena_rag`) |
| on-disk SQLite | `modernc.org/sqlite` | `EINO_ARENA_MEMORY_DB` (`.data/memory.db`) |

Every host is env-configurable specifically so the same binary works unchanged inside a Docker
container reaching the host via `host.docker.internal` (see [Packaging](#packaging)).

## Running (native)

```bash
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' \
  | bash arenas/live/eino/run.sh
```

`run.sh` sources the repo-root `.env` (`set -a; source ../../../.env; set +a`), validates
`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL`, then runs the adapter via `go run .`. The API
key is never printed, logged, or persisted anywhere by this adapter — see `test-env.sh`.

## Packaging

### Docker

`Dockerfile` is a two-stage build: a `golang:1.25-bookworm` builder produces a static,
CGO-free binary (every dependency, including `modernc.org/sqlite`, is pure Go — no cgo, no libc
linking), copied onto `gcr.io/distroless/static-debian12:nonroot` (no shell, minimal attack
surface, still carries CA certificates for the outbound HTTPS call to agnes). The `memory`-mode
SQLite data directory (`/data`, chowned to the distroless nonroot UID 65532) is created in the
builder stage and copied in with explicit ownership, since `WORKDIR` alone in the final
shell-less stage would create it root-owned and unwritable by the runtime user.

The agnes API key is **never baked into the image** — it is read from the environment at
container start, exactly like the native path. See `test-env.sh` for the automated proof.

```bash
bash arenas/live/eino/run-docker.sh --build   # builds (or rebuilds) eino-live-arena:local
echo '{"mode":"qa", ...}' | bash arenas/live/eino/run-docker.sh
```

`run-docker.sh` sources the repo-root `.env` for the agnes credentials (passed as **runtime**
`-e` flags, never `ARG`/`ENV` in the Dockerfile) and defaults `EMBED_URL`/`QDRANT_URL` to
`host.docker.internal` so the container reaches the real e5 server and real Qdrant already
running on the host.

### Credential/env tests

```bash
bash arenas/live/eino/test-env.sh
```

Runs 10 PASS/FAIL assertions (never prints the key value, even on failure):
- native reads `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` from the environment, and a
  real `qa` smoke call succeeds;
- the Docker image's baked `Config.Env` has no `OPENAI_API_KEY` entry;
- the image's build history contains no key literal;
- a **full `docker save` + grep over every saved layer** contains no key literal;
- a container run with no key passed in never emits it;
- neither native stdout nor stderr from a real run ever contains the key.

## Verified smoke tests (real agnes calls)

### `qa` (native)

```bash
$ echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France? One word."}]}' | bash run.sh
{"framework":"eino","mode":"qa","model":"agnes-2.0-flash","reply":"Paris","input_tokens":267,"output_tokens":2,"latency_ms":6728,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}
```

### `stream`

```bash
$ echo '{"mode":"stream","system":"You are concise.","messages":[{"role":"user","content":"Name three colors, one word each, comma separated."}]}' | bash run.sh
{"framework":"eino","mode":"stream","model":"agnes-2.0-flash","reply":"Red, Blue, Green","input_tokens":268,"output_tokens":6,"latency_ms":6791,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":6762,"error":""}
```

### `structured`

```bash
$ echo '{"mode":"structured","messages":[{"role":"user","content":"What is the capital of France?"}]}' | bash run.sh
{"framework":"eino","mode":"structured","model":"agnes-2.0-flash","reply":"{\n  \"answer\": \"Paris\",\n  \"confidence\": \"high\"\n}","input_tokens":398,"output_tokens":20,"latency_ms":6852,"tool_calls":[],"retrieved":[],"structured":{"answer":"Paris","confidence":"high"},"ttft_ms":0,"error":""}
```

### `tools` — real multi-step execution

```bash
$ echo '{"mode":"tools","messages":[{"role":"user","content":"What is (48 + 12) * 3 / 2? Use the calculator tool."}]}' | bash run.sh
{"framework":"eino","mode":"tools","model":"agnes-2.0-flash","reply":"The result of (48 + 12) * 3 / 2 is 90.","input_tokens":1340,"output_tokens":62,"latency_ms":13892,"tool_calls":[{"name":"calculate","arguments":{"expression":"(48 + 12) * 3 / 2"},"result":"{\"result\":90}"}],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}
```
`(48+12)*3/2 = 90` — the tool actually ran (real evaluator, real result `90`), and the model's
final answer correctly reports it. `input_tokens: 1340` reflects **two** internal model calls
(tool-call turn + final-answer turn), summed via the callback tracker.

### `memory` — cross-process recall (two separate `run.sh` invocations)

```bash
# process 1
$ echo '{"mode":"memory","session_id":"demo-1","system":"You are a helpful assistant. Keep replies short.","messages":[{"role":"user","content":"My Nimbus Rail member number is NR-4471. Please just acknowledge you noted it, one short sentence."}]}' | bash run.sh
{"framework":"eino","mode":"memory","model":"agnes-2.0-flash","reply":"I have noted your Nimbus Rail member number, NR-4471.","input_tokens":287,"output_tokens":17,"latency_ms":6987,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

# process 2 — a completely fresh `go run .`, the fact is NOT restated in this turn's input
$ echo '{"mode":"memory","session_id":"demo-1","messages":[{"role":"user","content":"What is my member number? Reply with just the number."}]}' | bash run.sh
{"framework":"eino","mode":"memory","model":"agnes-2.0-flash","reply":"NR-4471","input_tokens":314,"output_tokens":7,"latency_ms":6950,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}
```
Confirmed on disk: `sqlite3 .data/memory.db "SELECT turn_index, role, content FROM turns WHERE session_id='demo-1' ORDER BY turn_index"` returns all 4 persisted rows (2 user, 2 assistant) across both process invocations.

### `rag` — grounded + trap (real e5 + real Qdrant)

```bash
$ echo '{"mode":"rag","messages":[{"role":"user","content":"How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a passenger an automatic full refund, and where is the refund sent?"}]}' | bash run.sh
{"framework":"eino","mode":"rag","model":"agnes-2.0-flash","reply":"Under the Blue Guarantee, a passenger receives an automatic full refund if their trainset arrives more than 12 minutes late. The refund is sent to their HaloPay balance.","input_tokens":636,"output_tokens":36,"latency_ms":7150,"tool_calls":[],"retrieved":[{"doc_id":"08-blue-guarantee.md","title":"The Blue Guarantee (Refund Policy)","score":0.899,"text":"..."}, ...],"structured":null,"ttft_ms":0,"error":""}
```
Reply matches the reference exactly ("more than 12 minutes late" / "HaloPay balance"), and
`retrieved[0]` is the correct gold document (`08-blue-guarantee.md`, top score 0.899).

Trap question (`rag_questions.jsonl` q8 — no membership-card price exists in the corpus):
```bash
$ echo '{"mode":"rag","messages":[{"role":"user","content":"What was the price of a Nimbus Rail Lumen loyalty membership card in 2024?"}]}' | bash run.sh
{"framework":"eino","mode":"rag","model":"agnes-2.0-flash","reply":"I don'\''t know.", ...}
```
No hallucinated price — the model correctly declined despite the closest-matching passages
being about the Lumen programme (retrieved for context, but containing no price).

### Docker (containerized)

```bash
$ echo '{"mode":"qa", ...}' | bash run-docker.sh
{"framework":"eino","mode":"qa","model":"agnes-2.0-flash","reply":"Paris", ...}

$ echo '{"mode":"rag","messages":[{"role":"user","content":"How many stations does the Skylark Line have and how long is it in kilometres?"}]}' | bash run-docker.sh
{"framework":"eino","mode":"rag","model":"agnes-2.0-flash","reply":"The Skylark Line has 47 stations and is 312 kilometres long.", ...}
```
Both real calls succeeded from inside the container, reaching agnes over the public internet and
the real e5/Qdrant services on the host via `host.docker.internal`.

### `test-env.sh`

```
=== (a) native: env vars read + qa smoke ===
PASS: native reads OPENAI_API_KEY from env
PASS: native reads OPENAI_BASE_URL from env
PASS: native reads OPENAI_MODEL from env
PASS: native qa smoke call succeeds (exit=0)
=== (b) docker: no key literal in the image ===
PASS: image Config.Env has no OPENAI_API_KEY entry (hits=0)
PASS: image build history has no key literal (hits=0)
PASS: saved image layers contain no key literal (full grep) (hits=0)
PASS: container with no OPENAI_API_KEY passed in never emits it (hits=0)
=== (c) key never logged ===
PASS: native stdout never contains the key (hits=0)
PASS: native stderr never contains the key (hits=0)

=== test-env.sh summary: 10 passed, 0 failed ===
```

### Build/lint

```bash
$ gofmt -l .          # (no output — clean)
$ go vet ./...         # (no output — clean)
$ go build ./...       # (no output — clean)
$ docker build -t eino-live-arena:local .   # succeeds
```

## Dependencies and licenses

All dependencies actually compiled into the binary (`go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' .`,
cross-checked against each module's `LICENSE` file in the local module cache). Every one is
MIT, BSD (2- or 3-clause), or Apache-2.0 — no copyleft or source-available licenses. New
dependencies added by this exhaustive upgrade are marked **NEW**; everything else was already
part of the single-shot adapter.

| Module | Version | License |
|---|---|---|
| github.com/cloudwego/eino | v0.9.12 | Apache-2.0 |
| github.com/cloudwego/eino-ext/components/model/openai | v0.1.13 | Apache-2.0 |
| github.com/cloudwego/eino-ext/components/embedding/openai **(NEW)** | v0.0.0-20260716140429-9137edd89e72 (pseudo-version — no tagged release exists yet for this eino-ext subpackage) | Apache-2.0 |
| github.com/cloudwego/eino-ext/libs/acl/openai | v0.1.17 | Apache-2.0 |
| github.com/eino-contrib/jsonschema | v1.0.3 | MIT |
| github.com/qdrant/go-client **(NEW)** | v1.18.3 | Apache-2.0 |
| modernc.org/sqlite **(NEW)** | v1.54.0 | BSD-3-Clause-style |
| modernc.org/libc **(NEW)** | v1.74.1 | BSD-3-Clause-style |
| modernc.org/mathutil **(NEW)** | v1.7.1 | BSD-3-Clause-style |
| modernc.org/memory **(NEW)** | v1.11.0 | BSD-3-Clause-style |
| github.com/ncruces/go-strftime **(NEW)** | v1.0.0 | MIT |
| github.com/remyoudompheng/bigfft **(NEW)** | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause-style |
| github.com/mattn/go-isatty **(NEW)** | v0.0.20 | MIT |
| golang.org/x/net **(NEW)** | v0.53.0 | BSD-3-Clause |
| golang.org/x/text **(NEW)** | v0.37.0 | BSD-3-Clause |
| google.golang.org/grpc **(NEW)** | v1.81.1 | Apache-2.0 |
| google.golang.org/protobuf **(NEW)** | v1.36.11 | BSD-3-Clause |
| google.golang.org/genproto/googleapis/rpc **(NEW)** | v0.0.0-20260427160629-7cedc36a6bc4 | Apache-2.0 |
| github.com/bahlo/generic-list-go | v0.2.0 | BSD-3-Clause |
| github.com/buger/jsonparser | v1.1.1 | MIT |
| github.com/bytedance/gopkg | v0.1.3 | Apache-2.0 |
| github.com/bytedance/sonic | v1.15.0 | Apache-2.0 |
| github.com/bytedance/sonic/loader | v0.5.0 | Apache-2.0 |
| github.com/cloudwego/base64x | v0.1.6 | Apache-2.0 |
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/evanphx/json-patch | v0.5.2 | BSD-3-Clause |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/goph/emperror | v0.17.2 | MIT |
| github.com/json-iterator/go | v1.1.12 | MIT |
| github.com/klauspost/cpuid/v2 | v2.2.10 | MIT |
| github.com/mailru/easyjson | v0.9.0 | MIT |
| github.com/meguminnnnnnnnn/go-openai | v0.1.2 | Apache-2.0 |
| github.com/modern-go/concurrent | v0.0.0-20180306012644-bacd9c7ef1dd | Apache-2.0 |
| github.com/modern-go/reflect2 | v1.0.2 | Apache-2.0 |
| github.com/nikolalohinski/gonja | v1.5.3 | MIT |
| github.com/pelletier/go-toml/v2 | v2.2.3 | MIT |
| github.com/pkg/errors | v0.9.1 | BSD-2-Clause |
| github.com/sirupsen/logrus | v1.9.4 | MIT |
| github.com/slongfield/pyfmt | v0.0.0-20220222012616-ea85ff4c361f | BSD-3-Clause |
| github.com/twitchyliquid64/golang-asm | v0.15.1 | BSD-3-Clause |
| github.com/wk8/go-ordered-map/v2 | v2.1.8 | Apache-2.0 |
| github.com/yargevad/filepathx | v1.0.0 | MIT |
| golang.org/x/arch | v0.15.0 | BSD-3-Clause |
| golang.org/x/exp | v0.0.0-20250305212735-054e65f0b394 | BSD-3-Clause |
| golang.org/x/sys | v0.46.0 | BSD-3-Clause |
| gopkg.in/yaml.v3 | v3.0.1 | MIT AND Apache-2.0 (dual-licensed) |

None require a license-policy exception. Runtime image base (`gcr.io/distroless/static-debian12`)
and builder (`golang`) are Apache-2.0/BSD-style per their respective upstreams and are not Go
module dependencies, but are listed here for completeness of the supply chain.

## How agnes-ai is wired

Unchanged from the original single-shot adapter — Eino's real OpenAI-compatible chat model
component performs every chat HTTP call:

- Package: `github.com/cloudwego/eino-ext/components/model/openai` (`einoopenai.NewChatModel`).
- That component builds a `github.com/cloudwego/eino-ext/libs/acl/openai.Config` and calls
  `openai.DefaultConfig(config.APIKey)` from the vendored OpenAI client fork
  (`github.com/meguminnnnnnnnn/go-openai`), then **overrides `clientConf.BaseURL =
  config.BaseURL`** before constructing the client (see `eino-ext/libs/acl/openai/chat_model.go`).
  This is the exact mechanism that lets Eino talk to any OpenAI-compatible endpoint instead of
  `api.openai.com` — and the same override mechanism is reused for the e5 embedding client in
  `clients.go:newQueryEmbedder`.
- `clients.go` centralizes all client construction: `newChatModel`, `newQueryEmbedder`,
  `newQdrantClient` — every mode handler builds its Eino components through these, so
  `OPENAI_*`/`EMBED_*`/`QDRANT_*` are read from the environment exactly once per run, never
  hardcoded.

## Files

- `main.go` — request/response contract types, mode dispatch, shared parsing helpers.
- `clients.go` — env loading + Eino/Qdrant client construction, shared by every mode.
- `convert.go` — wire-format ↔ Eino `schema.Message`/`schema.ToolInfo` conversion helpers.
- `mode_qa.go` — `qa` mode (the original single-shot adapter).
- `mode_rag.go` — `rag` mode (Eino `compose.Chain` + real e5 + real Qdrant).
- `mode_tools.go` — `tools` mode (real Eino ReAct agent + real `calculate` tool).
- `calc.go` — self-contained arithmetic expression evaluator used by the `calculate` tool.
- `mode_memory.go` — `memory` mode (on-disk SQLite cross-process persistence).
- `mode_structured.go` — `structured` mode (Eino native JSON-Schema response format).
- `mode_stream.go` — `stream` mode (Eino native `Stream` + time-to-first-chunk).
- `run.sh` — sources `.env`, validates required env vars, runs the adapter natively.
- `Dockerfile` — multi-stage build, no baked credentials, distroless nonroot runtime.
- `run-docker.sh` — builds (if needed) and runs the containerized adapter.
- `test-env.sh` — automated credential/env PASS/FAIL assertions (native + Docker).
- `go.mod` / `go.sum` — module definition.
- `.go-version` — pins the local toolchain (via `goenv`) to an installed Go version compatible
  with the `go 1.25.0` directive (bumped from `1.23` by `go mod tidy` once
  `modernc.org/sqlite`'s dependency graph required it).
- `.gitignore` — excludes the runtime `.data/` SQLite store and stray local `go build` output.
