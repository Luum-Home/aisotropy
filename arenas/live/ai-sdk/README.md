# Live arena adapter — Vercel AI SDK (exhaustive multi-mode)

Node/TypeScript-ecosystem adapter that drives the [Vercel AI SDK](https://sdk.vercel.ai)
(`ai` + `@ai-sdk/openai`) against the same real model used by the other `arenas/live/*` adapters
(`agnes-2.0-flash`), plus REAL supporting infra: e5 embeddings, Qdrant vector search, and rqlite
persistence. Framework differences are attributable to the framework, not the model or a mock.

This is an additive arena directory: it does not modify any other `arenas/<name>/` directory,
`internal/`, `cmd/`, or the repo-root `.env`.

`qa` mode keeps the original single-shot behavior of this adapter, byte-compatible with the
`arenas/live/SPEC.md` base contract and `arenas/live/tasks/suite.jsonl` (which never sets `mode`,
so it defaults to `qa`). Five more modes are additive, each exercising a different AI SDK **native**
feature against **real** infrastructure — no mocks, no stubs anywhere in the request path.

## Modes

| `mode` | AI SDK feature | Real infra it touches |
|---|---|---|
| `qa` (default) | `generateText()`, single-shot, tools declared without `execute` | agnes only |
| `rag` | `embed()` + `generateText()` | agnes, the e5 embed server, Qdrant `arena_rag` |
| `tools` | `generateText()` with a real `execute`d tool + `stopWhen: stepCountIs(N)` | agnes (multi-step) |
| `memory` | `generateText()`, history loaded/appended around the call | agnes, rqlite |
| `structured` | `generateObject()` against a zod schema | agnes only |
| `stream` | `streamText()`, real token streaming + `ttft_ms` | agnes only |

## Model + infra wiring (all via environment, never hardcoded)

```
OPENAI_API_KEY=<secret, never printed>       # agnes chat, repo-root .env
OPENAI_BASE_URL=https://apihub.agnes-ai.com/v1
OPENAI_MODEL=agnes-2.0-flash
EMBED_URL=http://127.0.0.1:8899              # optional, this default if unset
QDRANT_URL=http://localhost:6333              # optional, this default if unset
QDRANT_COLLECTION=arena_rag                   # optional, this default if unset
RQLITE_URL=http://localhost:4001              # optional, this default if unset
```

`lib/env.mjs` resolves all of the above once per run and builds every client the mode handlers
need: the agnes chat model (via `.chat(modelName)` — see the "AI SDK quirks" section below for
why), the e5 embedding model, a `QdrantClient`, and the rqlite base URL. No credential is ever
logged; `run.sh` / `run-docker.sh` source/forward `OPENAI_API_KEY` without echoing it.

## Usage

Native:

```bash
echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Hello"}]}' \
  | bash arenas/live/ai-sdk/run.sh
```

Containerized (builds the image on first run, reaches the same real local infra via
`host.docker.internal`):

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"Hello"}]}' \
  | bash arenas/live/ai-sdk/run-docker.sh
```

### Input contract (stdin, one JSON object)

```json
{
  "mode": "qa | rag | tools | memory | structured | stream",
  "system": "string, optional",
  "messages": [{"role": "user" | "assistant", "content": "string"}],
  "tools": [{"name": "string", "description": "string", "parameters": {"...JSON-Schema..."}}],
  "session_id": "string, REQUIRED for mode=memory"
}
```

- `mode` defaults to `"qa"` when absent — the original single-shot arena task suite
  (`arenas/live/tasks/suite.jsonl`) runs unchanged against this adapter.
- `tools` is only read by `qa` mode (single-shot tool-call capture, no execution — see below).
  `tools` mode defines its own real `calculate` tool internally; it ignores `input.tools`.
- `rag` mode treats the last `user` message as the query.
- `memory` mode requires a non-empty `session_id`; `messages` are the NEW turn(s) for this
  invocation only (prior turns are loaded from rqlite, not replayed by the caller).

### Output contract (stdout, exactly one JSON object; all logs go to stderr)

```json
{
  "framework": "ai-sdk",
  "mode": "qa",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}, "output": null}],
  "retrieved": [{"doc_id": "string", "title": "string", "text": "string", "score": 0.0}],
  "structured": null,
  "ttft_ms": -1,
  "error": "string"
}
```

`mode`, `retrieved`, `structured`, and `ttft_ms` are **additive** to the base
`arenas/live/SPEC.md` contract: the shared Go runner (`arenas/live/runner`) decodes adapter
stdout into a struct that silently ignores unknown JSON fields, so this stays backward compatible.
Fields not populated by a given mode keep their zero value (`[]`, `null`, or `-1`).

On any error (network, API, malformed stdin, bad mode), the adapter catches it, fills in `error`,
and still writes one valid JSON object to stdout with exit code 0 — the `error` field is the
signal, not a crash. A non-zero exit is used only when stdin itself is unparsable or the mode name
is unknown; even then a best-effort JSON object is written to stdout first.

## Per-mode notes

### `rag`

1. Embeds the query with the AI SDK's native embedding path:
   `createOpenAI({baseURL: EMBED_URL}).embedding('multilingual-e5-small-query')` + `embed()`.
2. Searches the real Qdrant `arena_rag` collection via `@qdrant/js-client-rest`
   (`QdrantClient.search()`, `limit: 4`, `with_payload: true`).
3. Calls `generateText()` with the retrieved passages stuffed into `system` as CONTEXT, instructed
   to answer **only** from that context and say `"I don't know."` otherwise — this is what makes
   the corpus's trap questions (see `arenas/live/_shared/rag_questions.jsonl`) surface as an honest
   refusal instead of a hallucination.
4. `retrieved` carries every retrieved passage (`doc_id`, `title`, `text`, `score`).

### `tools`

`calculate` is a real tool: `lib/calculator.mjs` is a small recursive-descent arithmetic parser
(`+ - * / ()`, unary +/-) with a real `execute` function — deliberately **not** `eval()`/
`new Function()` on model-controlled input. `stopWhen: stepCountIs(5)` lets the AI SDK run the
tool and continue the conversation with the tool's real output to produce a final natural-language
answer: a genuine multi-step agent loop, not single-shot capture (that's what `qa` mode's
`tool_calling` task kind measures instead — see `lib/modes/qa.mjs`).

### `memory`

Turns are persisted to a real table (`ai_sdk_arena_memory`) in the running, networked rqlite
service at `RQLITE_URL` (default `http://localhost:4001`) — created idempotently
(`CREATE TABLE IF NOT EXISTS`) on first use via plain HTTP (`lib/rqlite.mjs`, built on Node's
native `fetch`, no extra dependency). A **second, completely separate** `node index.mjs` process
invocation with the same `session_id` loads that history back out of rqlite and prepends it before
calling the model, so a fact stated in invocation #1 is recalled in invocation #2 purely via the
persisted store — never an in-process `Map` that would die with the process.

### `structured`

`generateObject()` against a fixed zod schema (`summary`, `sentiment`, `action_items`) — the AI
SDK validates the model's output against the schema before this adapter ever sees it; a schema
violation throws `NoObjectGeneratedError` rather than returning malformed JSON. `structured` in the
output carries the validated object; `reply` carries its JSON-stringified form for callers that
only look at `reply`.

### `stream`

`streamText()`, iterating `result.textStream` chunk by chunk. `ttft_ms` is set on the first chunk
received (`process.hrtime.bigint()` delta from call start) — a latency dimension a non-streaming
`generateText()` call cannot expose. `latency_ms` covers the full stream to completion.

## AI SDK version quirks handled

The installed generation is `ai@7.0.31` + `@ai-sdk/openai@4.0.16` (both resolve to
`@ai-sdk/provider-utils@5.0.11`). Quirks this adapter accounts for:

1. **Chat Completions vs. Responses API.** `@ai-sdk/openai`'s default call signature
   (`provider(modelId)`) speaks the newer **Responses API** wire format (`/responses`,
   `function_call_output`/`item_reference` items). agnes's OpenAI-compatible proxy does not fully
   support that shape: it 500s (`{"error":{"message":"'role'", ...}}`) on the Responses-style
   multi-step tool-loop follow-up request specifically — confirmed by intercepting the raw request
   body the SDK sent, and by reproducing the working shape with a manual `curl` against
   `/chat/completions` using classic `role: "tool"` / `tool_call_id` messages. **Fix:**
   `lib/env.mjs` builds the chat model via `chatProvider.chat(modelName)`, which forces the classic
   `/chat/completions` format agnes actually speaks (the same wire format
   `internal/llm/openai_compat.go` uses natively in this repo). This affects every mode uniformly,
   not just `tools`.
2. **Token usage field names.** This generation reports `result.usage.inputTokens` /
   `result.usage.outputTokens` (not the older `promptTokens`/`completionTokens`). `extractTokenCounts()`
   in `lib/output.mjs` checks both key names, falling back to `-1` for a token count if neither is
   present or finite — it never fabricates a count.
3. **Tool call field names.** `result.toolCalls[].input` (not `.args`) carries tool call
   arguments; a tool definition's schema field is `inputSchema` (not `parameters`). `qa` mode's
   single-shot tool capture reads `call.input` and declares tools with
   `inputSchema: jsonSchema(rawJsonSchema)`; `tools` mode's real tool uses a zod `inputSchema`.
4. **Aggregated multi-step usage.** `generateText()`'s `result.usage` (and `result.toolCalls` /
   `result.toolResults`) already sum/collect across **all** steps of a `stopWhen`-bounded loop, not
   just the final step — `tools` mode reads them directly rather than manually summing
   `result.steps[]`.

## Docker packaging

`Dockerfile` builds a `node:22-alpine` image (`npm ci --omit=dev`, then just `index.mjs` + `lib/`
copied in — no `.env`, no host `node_modules`, no test artifacts). It declares **no** `ENV`/`ARG`
for `OPENAI_API_KEY` or any other credential; every one of them is a `docker run -e` **runtime**
value only. `run-docker.sh` builds the image on first use, sources the repo-root `.env` the same
way `run.sh` does, and rewrites any `localhost`/`127.0.0.1` host segment in
`EMBED_URL`/`QDRANT_URL`/`RQLITE_URL` to `host.docker.internal` (with `--add-host` for Linux
compatibility) so the container reaches the exact same real local services the native adapter
does, without a second config file.

## Automated credential/env tests (`test-env.sh`)

```bash
bash arenas/live/ai-sdk/test-env.sh
```

Runs and PASS/FAILs six assertions, never printing the key value itself (only match counts):

1. **native-env-and-qa-smoke** — native adapter reads `OPENAI_*` from the sourced `.env` and a
   real `qa` call succeeds (`error` field empty, non-empty `reply`).
2. **docker-image-builds** — `docker build` exits 0.
3. **docker-image-env-has-no-credential-vars** — `docker run --rm <img> env` (no `-e` flags) shows
   zero `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `OPENAI_MODEL` entries.
4. **docker-image-layers-have-no-key-literal** — every layer tar of the built image, decompressed
   and grepped for the literal key value, matches zero times.
5. **key-never-logged** — the key value's match count is zero across native stdout/stderr,
   containerized stdout/stderr, the image env dump, and the docker build log.
6. **docker-qa-smoke** — a real containerized `qa` call succeeds (`error` field empty).

Last verified run: `SUMMARY: 6 passed, 0 failed`.

## Verification runs (evidence)

Real `agnes-2.0-flash` calls, run against the real local infra described above.

**`qa`** (backward-compat, no `mode` field — matches `arenas/live/tasks/suite.jsonl` shape):

```json
{"framework":"ai-sdk","mode":"qa","model":"agnes-2.0-flash","reply":"The chemical symbol for gold is Au.","input_tokens":275,"output_tokens":9,"latency_ms":4114,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":-1,"error":""}
```

**`rag`**, grounded question (`08-blue-guarantee.md` is the gold doc and is the top-scored hit):

```json
{"framework":"ai-sdk","mode":"rag","model":"agnes-2.0-flash","reply":"\n\nA Nimbus Rail trainset must arrive more than 12 minutes late to trigger the refund. The automatic full refund is sent directly to the passenger's HaloPay balance.","retrieved":[{"doc_id":"08-blue-guarantee.md","title":"The Blue Guarantee (Refund Policy)","score":0.8993131}, "...3 more"],"error":""}
```

**`rag`**, trap question (no supporting doc in the corpus — correct refusal, not a hallucination):

```json
{"framework":"ai-sdk","mode":"rag","model":"agnes-2.0-flash","reply":"\n\nI don't know.","error":""}
```

**`tools`** (real multi-step execution — `calculate` actually ran and returned `12880185.529411765`,
then the model rounded it correctly in its final answer):

```json
{"framework":"ai-sdk","mode":"tools","model":"agnes-2.0-flash","reply":"The final number rounded to 2 decimal places is 12880185.53.","tool_calls":[{"name":"calculate","arguments":{"expression":"(23847 * 9182) / 17"},"output":{"expression":"(23847 * 9182) / 17","result":12880185.529411765}}],"error":""}
```

**`memory`**, invocation #1 (fresh process, states two facts):

```json
{"framework":"ai-sdk","mode":"memory","model":"agnes-2.0-flash","reply":"Understood. I have noted that your favorite color is teal and your lucky number is 47.","error":""}
```

**`memory`**, invocation #2 (a *second, completely separate* `node index.mjs` process, same
`session_id`, new question only — recalls both facts purely from rqlite):

```json
{"framework":"ai-sdk","mode":"memory","model":"agnes-2.0-flash","reply":"Your favorite color is teal, and your lucky number is 47.","error":""}
```

**`structured`** (validated zod object):

```json
{"framework":"ai-sdk","mode":"structured","model":"agnes-2.0-flash","structured":{"summary":"The customer is dissatisfied with three consecutive late deliveries and requests both refunds and an adjustment to delivery estimates.","sentiment":"negative","action_items":["Process refunds for the last three late orders.","Review and correct delivery estimates."]},"error":""}
```

**`stream`** (real streaming; `ttft_ms` < `latency_ms`):

```json
{"framework":"ai-sdk","mode":"stream","model":"agnes-2.0-flash","reply":"Maglev trains utilize powerful electromagnets to levitate above a guideway, eliminating physical contact with the track. This frictionless design allows them to achieve exceptionally high speeds while operating quietly and efficiently.","latency_ms":7094,"ttft_ms":6728,"error":""}
```

**Docker**, `qa` and `rag` both verified end-to-end against the real local infra over
`host.docker.internal` (see "Docker packaging" above) — same grounded answer and gold-doc
retrieval as the native `rag` run.

## Licenses (direct + meaningful transitive dependencies)

All packages actually installed (`npm install`, `package-lock.json`), checked against this repo's
license policy (MIT/BSD/Apache-2.0/ISC/CC0 = safe; AGPL/SSPL/BSL/ELv2/Commons Clause/FSL = blocked):

| Package | Version | License | New in this change? |
|---|---|---|---|
| `ai` | 7.0.31 | Apache-2.0 | no (pre-existing) |
| `@ai-sdk/openai` | 4.0.16 | Apache-2.0 | no (pre-existing) |
| `@ai-sdk/provider` | 4.0.3 | Apache-2.0 | no (transitive, pre-existing) |
| `@ai-sdk/provider-utils` | 5.0.11 | Apache-2.0 | no (transitive, pre-existing) |
| `@ai-sdk/gateway` | 4.0.23 | Apache-2.0 | no (transitive, pre-existing) |
| `@vercel/oidc` | 3.2.0 | Apache-2.0 | no (transitive, pre-existing) |
| `@workflow/serde` | 4.1.0 | Apache-2.0 | no (transitive, pre-existing) |
| `@standard-schema/spec` | 1.1.0 | MIT | no (transitive, pre-existing) |
| `eventsource-parser` | 3.1.0 | MIT | no (transitive, pre-existing) |
| `typescript` | 7.0.2 | Apache-2.0 | no (transitive, pre-existing) |
| `@typescript/typescript-darwin-arm64` | 7.0.2 | Apache-2.0 | no (transitive, pre-existing) |
| `undici` | 6.27.0 | MIT | no (transitive, pre-existing) |
| `zod` | 4.4.3 | MIT | **promoted to a direct dependency** (was already an installed transitive dep; now imported directly by `tools`/`structured` modes) |
| `json-schema` | 0.4.0 | AFL-2.1 OR BSD-3-Clause (dual; BSD-3-Clause satisfies policy) | no (transitive, pre-existing) |
| `@qdrant/js-client-rest` | 1.18.0 | Apache-2.0 | **yes — new, added for `rag` mode** |
| `@qdrant/openapi-typescript-fetch` | 1.2.6 | MIT | **yes — new, transitive of `@qdrant/js-client-rest`** |

**No restrictive licenses found** (no AGPL/SSPL/BSL/ELv2/Commons Clause/FSL anywhere in the
install tree). This adapter's own `package.json` is licensed MIT, consistent with the rest of
this repo's stack policy.

## Files

| Path | Role |
|---|---|
| `index.mjs` | stdin/stdout contract, mode dispatch |
| `lib/env.mjs` | env-var + client resolution (chat model, embed provider, Qdrant URL, rqlite URL) |
| `lib/output.mjs` | shared output shape + small utilities (`buildMessages`, `extractTokenCounts`, `msSince`) |
| `lib/calculator.mjs` | real, `eval`-free arithmetic evaluator backing the `tools` mode's `calculate` tool |
| `lib/rqlite.mjs` | minimal rqlite HTTP client (native `fetch`, no extra dependency) backing `memory` mode |
| `lib/modes/qa.mjs` | original single-shot mode |
| `lib/modes/rag.mjs` | real RAG: embed → Qdrant search → grounded `generateText()` |
| `lib/modes/tools.mjs` | real multi-step tool execution loop |
| `lib/modes/memory.mjs` | rqlite-persisted conversational memory across process invocations |
| `lib/modes/structured.mjs` | `generateObject()` against a zod schema |
| `lib/modes/stream.mjs` | `streamText()` with time-to-first-token |
| `run.sh` | native entrypoint (sources repo-root `.env`) |
| `Dockerfile` | container image, no baked-in credentials |
| `run-docker.sh` | build + run the container against real local infra via `host.docker.internal` |
| `test-env.sh` | automated credential/env PASS/FAIL assertions |
| `.dockerignore` | keeps `.env`, `node_modules`, and test artifacts out of the build context |
