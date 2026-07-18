# Live arena adapter — Mastra

A full-parity, multi-mode **live** arena adapter for [Mastra](https://mastra.ai)
(TypeScript AI framework, **Apache-2.0**), driven against the **real**
`agnes-2.0-flash` model and the arena's **real** infrastructure by default — no
mocks, no stubs. Setting `ARENA_STUB=1` is the one opt-in exception: it swaps
every real dependency for an offline substitute so the full mode suite can run
with no network access at all (see "Offline / ARENA_STUB mode" below). It
mirrors the uniform stdin/stdout contract of the other seven live
adapters (`ai-sdk`, `llamaindex`, `pydantic-ai`, `langgraph`, `eino`,
`genkit-go`, and the `aisotropy` reference), so the shared Go runner
(`arenas/live/runner`) scores it identically.

Mastra is a batteries-included framework: it ships **agents, memory, tools,
workflows, evals, and observability in one package**. This adapter exercises the
NATIVE Mastra feature behind each arena mode rather than reimplementing it, and
documents honestly where Mastra has no native primitive (multi-tenant
compound-scope isolation) and a scope key must be hand-rolled by the developer.

---

## Contract

- **stdin**: exactly ONE JSON object, always including `mode` (defaults to `qa`
  when absent). Recognized fields: `mode`, `system`, `messages`, `tools`,
  `tenant_id`, `user_id`, `product_id`, `session_id`, `fact`, `query`,
  `cross_scope`, `victim`, `secret`, `role`, `turns`, `continuation`,
  `expected_fact`.
- **stdout**: exactly ONE JSON object (the adapter result). **All** logging goes
  to **stderr**, prefixed `PROGRESS:`. `index.mjs` additionally reroutes
  `console.log` → stderr at startup so a chatty dependency (libsql storage
  warnings, the observability exporter's flush summary) can never corrupt the
  single-JSON stdout contract.
- The `OPENAI_API_KEY` is **never** printed or logged on any path.

Output shape (superset; unused fields carry their neutral default per mode):

```
framework:"mastra", mode, model:"agnes-2.0-flash", reply,
input_tokens, output_tokens, latency_ms, tool_calls[], retrieved[],
structured, ttft_ms, recalled, scoped_recall, leak_detected,
attacker_visible_facts[], otel_supported, span_count, span_names[],
span_timings_ms, logs_structured, log_sample, notes, resumed,
state_lost, precrash_fact_recalled, text, middleware, error
```

`text` and `middleware` are additive fields populated only by `middleware` mode
(see below) — every field above is present with its neutral default
(`text:""`, `middleware:null`) on every other mode's response, so the shared Go
runner (which ignores unknown fields) scores this adapter identically either
way.

Handled errors (bad input, API/network failure, missing scope) land in the
`error` field and still exit 0 — errors are **data**, not crashes.

---

## How agnes is wired (and why `.chat`)

Mastra runs on the Vercel AI SDK under the hood; an `Agent`'s `model` is an
AI-SDK `LanguageModel`. agnes is wired via `@ai-sdk/openai`:

```js
createOpenAI({ baseURL: OPENAI_BASE_URL, apiKey: OPENAI_API_KEY }).chat(OPENAI_MODEL)
```

`.chat(...)` forces the classic `/chat/completions` wire format. The AI SDK's
default call signature would use the newer **Responses API** shape
(`/responses`, `function_call_output` items), which agnes's OpenAI-compatible
proxy does not fully support — it **500s (`'role'`)** on the Responses-style
multi-step tool-loop follow-up. agnes (like aisotropy's own
`internal/llm/openai_compat.go`) speaks classic `/chat/completions`, so every
mode uses `.chat` explicitly.

### Configuration (env)

| Variable | Default | Used by |
|---|---|---|
| `OPENAI_API_KEY` | *(required)* | chat model (never logged) |
| `OPENAI_BASE_URL` | `https://apihub.agnes-ai.com/v1` (from `.env`) | chat model |
| `OPENAI_MODEL` | `agnes-2.0-flash` (from `.env`) | chat model |
| `EMBED_URL` | `http://127.0.0.1:8899` | `rag` (real e5, 384-dim) |
| `QDRANT_URL` | `http://localhost:6333` | `rag` (real Qdrant `arena_rag`) |
| `QDRANT_COLLECTION` | `arena_rag` | `rag` |
| `RQLITE_URL` | `http://localhost:4001` | `memory_scoped`, `isolation_leak`, `crash_recovery` |
| `AISOTROPY_MASTRA_LIBSQL` | `file:/tmp/mastra_arena_memory.db` | `memory` (Mastra-native Memory store) |

Every host URL is env-configurable so the containerized adapter can reach the
same real services via `host.docker.internal` (`run-docker.sh` rewrites
`localhost`/`127.0.0.1` automatically).

---

## Modes

Each mode maps to a distinct arena task kind and drives a distinct Mastra native
feature.

### `qa` — single-shot `Agent`
One `agent.generate(messages)` call. When the arena's `tool_calling` task
supplies `tools` (JSON-schema declarations), they are built as Mastra tools with
**no `execute`** and the loop is capped at `maxSteps: 1`, so the agent surfaces
its **decided** tool call (name + arguments) single-shot — that is what the
tool_calling task kind grades (native tool *selection*, not a full loop).

### `rag` — real retrieval, Mastra-grounded answer *(MUST)*
1. Embed the last user message via the **real e5 server**
   (`multilingual-e5-small-query`, 384-dim — the same model that populated the
   corpus, so query and corpus vectors share a space).
2. Search the **real Qdrant** `arena_rag` collection (22 Nimbus-Rail docs,
   payload `text`/`title`/`doc_id`) via `@qdrant/js-client-rest`.
3. A Mastra `Agent` answers **strictly from the retrieved context** — "answer
   only from context, else say you don't know". A **trap** question (answer not
   in the corpus) surfaces as an honest `I don't know.` rather than a
   hallucination. `retrieved[]` is filled with `{doc_id,title,text,score}`.

Retrieval feeds the agent through its grounding instructions — the honest,
robust path the prompt permits ("a direct qdrant fetch feeding Mastra's
context").

### `tools` — real executed multi-step loop *(MUST)*
A Mastra tool `calculate` with a real `execute` (a recursive-descent arithmetic
evaluator in `lib/calculator.mjs`, **not `eval`**). With `maxSteps: 5`, Mastra
actually runs the tool and continues the conversation with its real result to
produce the final answer. `tool_calls[]` carries `{name, arguments, output}` with
the executed numeric result folded in.

### `memory` — Mastra NATIVE Memory across two processes *(MUST)*
Mastra's first-class `Memory` primitive, backed by an on-disk **libsql/SQLite**
file (`@mastra/libsql` `LibSQLStore`). Mastra auto-persists each turn's
user+assistant messages keyed by `(resourceId, threadId)` and auto-loads prior
turns before the model call. A second, completely fresh `node index.mjs` process
with the same `session_id` reopens the same libsql file and recalls a fact stated
in the first process — **real cross-process persistence, not an in-process Map**.

`semanticRecall` is disabled (it would require an embedder + vector index); this
mode grades durable conversational recall via Mastra's message store.

### `memory_scoped` + `isolation_leak` — hand-rolled compound scope *(MUST)*
> **HONEST NOTE.** Mastra *does* ship a native `Memory` primitive, but it scopes
> by a **two-level** key only — `resourceId` (e.g. a user) × `threadId` (e.g. a
> conversation). It has **no** native **three-level** multi-tenant scope
> (`tenant_id` × `user_id` × `product_id`) with cross-tenant isolation. So these
> two modes hand-roll a compound scope over the real, networked **rqlite** store
> (`lib/scoped-store.mjs`): the isolation boundary IS the four-column
> `WHERE tenant_id=? AND user_id=? AND product_id=? AND session_id=?` clause.
> A developer who forgets even one column creates a **real** cross-tenant leak —
> nothing in Mastra, the schema, or rqlite stops that mistake. This is
> **dev-responsible** isolation.
>
> **Contrast — aisotropy** enforces tenant scoping **by construction**:
> `tenant_id` is injected once at the Huma handler boundary and propagated via
> `context.Context`; there is no query path that *can* omit the tenant filter
> because `tenant_id` never travels as a caller-suppliable query parameter
> (`docs/architecture/multi-tenancy.md`).

- `memory_scoped`: write a `fact` under the primary scope, prove a **same-scope
  hit** and a **cross-tenant miss** (`cross_scope` probe), then a real Mastra
  agent answers grounded in the same-scope recall. Omitting `fact` tests
  recall-only from a separate process (proves rqlite persistence).
- `isolation_leak`: plant a `secret` under a **victim** scope, recall under the
  **attacker** scope, and prove no leak in **two** places — the raw store recall
  (`attacker_visible_facts`, authoritative) and the model's reply.
  `leak_detected: false` is PASS. A positive-control run (attacker == victim)
  legitimately returns `true` (a tenant sees its own data).

### `crash_recovery` — survive a HARD SIGKILL *(MUST)*
Distinct from `memory` (which only proves a **clean** exit). Persistence here is
to **rqlite** deliberately, so `test-crash.sh` can verify durability
**independently** of the adapter via rqlite's HTTP API.

- `role:"run"` (process A): runs a 2–3 turn conversation, checkpointing each turn
  to rqlite in ONE atomic 2-row INSERT **before** the next turn. `test-crash.sh`
  SIGKILLs it after turn 1's checkpoint but **while turn 2's `generate()` is in
  flight** — so turn 2 is never committed.
- `role:"resume"` (process B): a **fresh** process (new PID) reloads the
  committed rqlite state for the same `session_id` and continues with a real
  agnes call. Reports `resumed`, `state_lost` (against durable state only, not
  model behavior), and `precrash_fact_recalled` (the model actually used the
  recovered fact).

Because checkpointing is atomic per turn, a crash can only ever discard
in-flight, never-committed work (turn 2) — never corrupt a committed turn (turn
1). The honest, expected finding is `state_lost: false` even though the in-flight
turn is gone.

### `structured` — Mastra NATIVE structured output *(SHOULD)*
`agent.generate(messages, { structuredOutput: { schema } })` against a **zod**
schema. Mastra validates the object before this adapter sees it; the parsed
object is returned on `structured`.

### `stream` — Mastra NATIVE streaming *(SHOULD)*
`agent.stream(messages)` iterates `result.textStream`, recording `ttft_ms` (time
to first streamed chunk) — a latency dimension the non-streaming path cannot
expose. Usage is resolved from the stream's `usage` promise.

### `observability` — Mastra NATIVE OpenTelemetry-semantic tracing *(MUST — the strength)*
> **DIFFERENTIATOR.** Mastra ships a **first-class** observability system ("AI
> Tracing") in `@mastra/observability`. Unlike langgraph/eino (which need a
> tracer shim) or the Vercel AI SDK (which needs a separate `@ai-sdk/otel`
> integration + a globally-registered `TracerProvider`), Mastra needs **no
> bridge**: one `observability` config on the `Mastra` instance and every agent
> turn emits a structured span tree — plus **native metrics** and a
> structured-log channel — automatically.

This mode wires an **in-process** `TestExporter` (an in-memory span/metric/log
sink — **NOT** an external collector, **NOT** OTLP network egress) and runs one
real executed-tool-loop turn. It reports `otel_supported`, `span_count`,
`span_names`, `span_timings_ms`, `logs_structured`, a structured `log_sample`,
and the native metric names in `notes`.

A representative captured span tree (GenAI semantics):

```
agent run: 'arena-observability'   (agent_run)
  llm: 'agnes-2.0-flash'           (model_generation)
    step: 0                        (model_step)      ← call #1: decide to call tool
      inference: 0                 (model_inference)
      chunk: 'tool-call'           (model_chunk)
    tool: 'calculate'              (tool_call)       ← real tool execution
    chunk: 'tool-result'           (model_chunk)
    step: 1                        (model_step)      ← call #2: finish after tool
      inference: 1                 (model_inference)
      chunk: 'text'                (model_chunk)
```

Native metrics emitted in the same pass:
`mastra_model_total_input_tokens`, `mastra_model_total_output_tokens`,
`mastra_model_duration_ms`, `mastra_tool_duration_ms`, `mastra_agent_duration_ms`.

### `middleware` — AI SDK model middleware (inbound + outbound)

**Mechanism (honest disclosure): `wrapLanguageModel` from the `ai` package, NOT
Mastra's `inputProcessors`/`outputProcessors`.** Mastra ships both seams — its
own Agent-level processor pipeline, and (because an Agent's `model` is a plain
AI SDK `LanguageModel`) standard AI SDK model middleware. This mode uses the
latter: `wrapLanguageModel({ model: chatModel, middleware })` wraps the exact
model object handed to `new Agent({ model })`, giving one middleware object
both an inbound and an outbound hook against the real call:

- **Inbound** — `transformParams` runs before the model ever sees the call. It
  finds the `{ role: 'system', ... }` message Mastra compiled from
  `instructions` and appends the sentinel `"\n[ARENA_MW]"` to its content.
- **Outbound** — `wrapGenerate` runs once per real `doGenerate` call and
  increments an invocation counter before delegating to the real call
  unchanged.

One `qa`-style turn through the wrapped model returns:

```json
{"text":"OK","middleware":{"mechanism":"wrapLanguageModel","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true}}
```

(as additive top-level fields alongside this adapter's normal
`framework`/`mode`/`model`/`reply`/... envelope — see "Contract" above).
Verified against real `agnes-2.0-flash`:

```bash
echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' \
  | bash arenas/live/mastra/run.sh
# {"...","model":"agnes-2.0-flash","reply":"OK","...",
#  "text":"OK","middleware":{"mechanism":"wrapLanguageModel","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true},"error":""}
```

---

## Offline / ARENA_STUB mode

Setting `ARENA_STUB=1` runs **every mode** (including `middleware`) fully
offline — no `OPENAI_API_KEY`/`OPENAI_MODEL`, no real agnes call, no e5 embed
server, no Qdrant, no rqlite:

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"Say OK."}]}' \
  | ARENA_STUB=1 node arenas/live/mastra/index.mjs
```

| Real infra | Offline substitute | File |
|---|---|---|
| agnes chat model (`createOpenAI(...).chat(...)`) | `MockLanguageModelV4` from `ai/test` | `lib/stub-model.mjs` |
| rqlite (`memory_scoped`, `isolation_leak`, `crash_recovery`) | file-backed JSON store under `os.tmpdir()` (survives across separate `node index.mjs` processes, matching what those modes test) | `lib/stub-store.mjs` |
| e5 embed server (`rag`) | deterministic hash-based pseudo-vector (no semantic meaning) | `lib/embed.mjs` |
| Qdrant `arena_rag` collection (`rag`) | small hardcoded corpus + keyword-overlap search (not vector similarity — a hash embedding has none) | `lib/stub-qdrant.mjs` |
| on-disk libsql `memory` store | unchanged — already a local file (`file:/tmp/...`), no network involved either way | `lib/modes/memory.mjs` |

**Why `MockLanguageModelV4`, not `MockLanguageModelV2`.** Checked against the
installed packages rather than assumed: `createOpenAI({...}).chat(modelName)`
from the pinned `@ai-sdk/openai@4.0.16` reports
`model.specificationVersion === 'v4'` at runtime, backed by
`@ai-sdk/provider@4.0.3` — exactly what `ai@7.0.31` (added as a new direct
dependency here, matching the "provider-v7" generation `@mastra/core@1.51.0`
already vendors internally) depends on. `ai@7.0.31`'s `ai/test` subpath does
not even export a `MockLanguageModelV2` — only `MockLanguageModelV3`/`V4` mock
classes exist at this pin — so `MockLanguageModelV4` is the correct offline
stand-in, not a looser interpretation of the ask.

**Honest limitations of the stub** (documented, not hidden):
- The mock model does not reason: `qa`/`rag`/`memory`/etc. get a fixed canned
  reply text regardless of history or context, so `crash_recovery`'s
  `precrash_fact_recalled` is legitimately `false` under the stub (durable
  *state* recovery — `resumed`/`state_lost` — still works correctly, because
  that check is against what was loaded from the file-backed store, not
  against model output).
- The mock DOES branch correctly on tool declarations (emits a real
  `calculate` tool call with a valid arithmetic expression so the real,
  executing `calculate` tool in `tools`/`observability` modes still runs) and
  on `structuredOutput` JSON-schema requests (fabricates a schema-shaped
  placeholder object), so those modes exercise their real Mastra wiring even
  offline.
- `lib/stub-store.mjs` is not a SQL engine — it pattern-matches the exact
  `CREATE TABLE`/`INSERT`/`SELECT` shapes this adapter's own code emits (see
  its header comment) and throws on anything else, rather than silently
  returning wrong results.

---

## Running

**Native** (sources the repo-root `.env`):

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"What is the capital of France?"}]}' \
  | bash arenas/live/mastra/run.sh
```

**Docker** (agnes key passed as a RUNTIME env, never baked into the image;
local-infra hosts rewritten to `host.docker.internal`):

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"How many continents are there?"}]}' \
  | bash arenas/live/mastra/run-docker.sh
```

> The `memory` mode's on-disk libsql store is **container-local** by default
> (`/tmp` inside the container) — cross-process recall works within the same
> container, but the host and a container don't share the file unless you mount
> one. The rqlite-backed modes share state across host+container (they hit the
> real networked rqlite service).

Install: `npm install` (in `arenas/live/mastra/`). Node ≥ 20.9.

---

## Tests (acceptance evidence)

All three suites drive the real model + real infra and never print the key.

```bash
bash arenas/live/mastra/test-env.sh        # native env + qa; docker build; no key baked in any layer; key never logged; container qa smoke
bash arenas/live/mastra/test-isolation.sh  # memory_scoped (same-scope hit + cross-tenant miss + cross-process persistence) + isolation_leak (zero-leak + positive control)
bash arenas/live/mastra/test-crash.sh      # SIGKILL mid-turn-2, fresh process resumes, pre-crash fact recalled — verified independently via rqlite HTTP
```

Latest run on real infra:

| Suite | Result |
|---|---|
| `test-env.sh` | **6/6 PASS** — native qa smoke; docker builds; 0 credential vars in image env; 0 key literals across all layer tars; key never logged; container qa smoke |
| `test-isolation.sh` | **6/6 PASS** — same-scope hit; cross-tenant miss; cross-process persistence; zero-leak (`leak_detected=false`); positive control (`true`); key never logged |
| `test-crash.sh` | **8/8 PASS** — turn-1 checkpoint; SIGKILL mid-turn-2; exactly turn-1's 2 rows in rqlite; in-flight turn absent; resume no-error; `resumed=true`; `state_lost=false`; `precrash_fact_recalled=true` |

### Per-mode smoke (real agnes + real infra)

| Mode | Evidence |
|---|---|
| `qa` | `reply:"The capital of France is Paris."` |
| `rag` (grounded) | gold doc `Nimbus Rail — Company Overview` top hit (score 0.897); grounded answer |
| `rag` (trap) | `reply:"I don't know."` |
| `tools` | `calculate((23847*9182)/17)` executed → `12880185.529…` folded into reply |
| `memory` | P1 wrote "4287"; fresh P2 process recalled `"4287"` from libsql |
| `memory_scoped` | `same_scope_hit=true`, `cross_tenant_miss=true`, cross-process recall |
| `isolation_leak` | `leak_detected=false` (attacker sees nothing of the victim secret) |
| `structured` | valid object `{summary, sentiment, action_items[]}` |
| `stream` | `ttft_ms` recorded (first streamed chunk) |
| `observability` | `otel_supported=true`, `span_count=10`, native span tree + metrics, `logs_structured=true` |

---

## Dependency licenses — 100% permissive

Verified across the full installed tree (280 packages): **zero** copyleft
(GPL/AGPL/LGPL), **zero** source-available (BSL/ELv2/SSPL/Commons Clause/FSL). No
ADR exception needed; consistent with aisotropy's MIT/Apache-2.0-only stack
policy (`.claude/rules/cos/license-policy.md`).

| Dependency | License |
|---|---|
| `@mastra/core` | Apache-2.0 |
| `@mastra/memory` | Apache-2.0 |
| `@mastra/libsql` | Apache-2.0 |
| `@mastra/observability` | Apache-2.0 |
| `@ai-sdk/openai` | Apache-2.0 |
| `ai` | Apache-2.0 |
| `@qdrant/js-client-rest` | Apache-2.0 |
| `@opentelemetry/api` | Apache-2.0 |
| `@opentelemetry/sdk-trace-node` | Apache-2.0 |
| `zod` | MIT |

Full-tree tally: MIT 218, Apache-2.0 36, ISC 11, BSD-3-Clause 7, BlueOak-1.0.0 3,
BSD-2-Clause 3, `(AFL-2.1 OR BSD-3-Clause)` 1, 0BSD 1 — all permissive.

---

## Layout

```
arenas/live/mastra/
  index.mjs                 dispatcher (mode → handler; stdout hardening)
  lib/
    env.mjs                 env + Mastra-compatible model resolution
    output.mjs              base output shape, token/toolCall/message helpers
    embed.mjs               real e5 embedding client (fetch); ARENA_STUB=1 offline pseudo-vector
    calculator.mjs          recursive-descent arithmetic (no eval)
    calculate-tool.mjs      the executed `calculate` Mastra tool
    rqlite.mjs              minimal rqlite HTTP client; ARENA_STUB=1 routes to stub-store.mjs
    scoped-store.mjs        hand-rolled compound-scope store (honest isolation note)
    observability.mjs       in-process Mastra AI-tracing wiring (TestExporter)
    stub-model.mjs          ARENA_STUB=1 offline chat model (MockLanguageModelV4)
    stub-store.mjs          ARENA_STUB=1 offline rqlite substitute (file-backed JSON)
    stub-qdrant.mjs         ARENA_STUB=1 offline Qdrant substitute (keyword search)
    modes/                  qa, rag, tools, memory, memory_scoped, isolation_leak,
                            structured, stream, observability, crash_recovery, middleware
  run.sh / run-docker.sh    native / containerized entrypoints
  Dockerfile                no baked credentials; runtime -e only
  test-env.sh / test-isolation.sh / test-crash.sh
```
