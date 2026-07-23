# Live arena adapter — Vercel AI SDK (exhaustive multi-mode)

Node/TypeScript-ecosystem adapter that drives the [Vercel AI SDK](https://sdk.vercel.ai)
(`ai` + `@ai-sdk/openai`) against the same real model used by the other `arenas/live/*` adapters
(`agnes-2.0-flash`), plus REAL supporting infra: e5 embeddings, Qdrant vector search, and rqlite
persistence. Framework differences are attributable to the framework, not the model or a mock.

This is an additive arena directory: it does not modify any other `arenas/<name>/` directory,
`internal/`, `cmd/`, or the repo-root `.env`.

`qa` mode keeps the original single-shot behavior of this adapter, byte-compatible with the
`arenas/live/SPEC.md` base contract and `arenas/live/tasks/suite.jsonl` (which never sets `mode`,
so it defaults to `qa`). Ten more modes are additive, each exercising a different AI SDK **native**
feature against **real** infrastructure — no mocks, no stubs anywhere in the request path (unless
`ARENA_STUB=1`, see "Offline stub toggle" below).

## Modes

| `mode` | AI SDK feature | Real infra it touches |
|---|---|---|
| `qa` (default) | `generateText()`, single-shot, tools declared without `execute` | agnes only |
| `rag` | `embed()` + `generateText()` | agnes, the e5 embed server, Qdrant `arena_rag` |
| `tools` | `generateText()` with a real `execute`d tool + `stopWhen: stepCountIs(N)` | agnes (multi-step) |
| `memory` | `generateText()`, history loaded/appended around the call, keyed by `session_id` only | agnes, rqlite |
| `structured` | `generateObject()` against a zod schema | agnes only |
| `stream` | `streamText()`, real token streaming + `ttft_ms` | agnes only |
| `memory_scoped` | `generateText()` grounded in a **hand-rolled compound-scope** rqlite recall (`tenant_id`/`user_id`/`product_id`[/`session_id`]) | agnes, rqlite |
| `isolation_leak` | `generateText()` grounded in an attacker-scope recall, adversarially probing for a secret planted under a different (victim) scope | agnes, rqlite |
| `observability` | in-process OTel SDK (`@ai-sdk/otel` + `InMemorySpanExporter`) wired around a real, executed tool-loop `generateText()` call | agnes (multi-step) |
| `crash_recovery` | `generateText()`, turns checkpointed to rqlite one-by-one; a SIGKILL-tested `role:"run"`/`role:"resume"` pair proves survival of a hard process kill mid-conversation, not just a clean exit | agnes, rqlite |
| `middleware` | `wrapLanguageModel({ model, middleware })` — a `transformParams` (inbound) + `wrapGenerate`/`wrapStream` (outbound) pair wired around one real `generateText()` turn | agnes |

## Offline stub toggle (`ARENA_STUB=1`)

Set `ARENA_STUB=1` in the environment to run **any** mode with **zero network egress** — a
wiring/smoke lane, not a quality lane:

- `env.chatModel` becomes the AI SDK's own `MockLanguageModelV4` (from `ai/test`, see
  `lib/stub.mjs`) instead of the real agnes model. Its `doGenerate`/`doStream` inspect the actual
  call params (`tools`, `responseFormat`, `prompt`) so every mode's own code (real, local
  `calculate` tool execution, `generateObject()` JSON-schema validation, `streamText()` chunk
  assembly) still runs — only the model call itself is canned.
- `rag` mode skips the real e5 embed server + Qdrant search and instead does a keyword-overlap
  "search" over a tiny fixed local corpus (`stubSearchCorpus()` in `lib/stub.mjs`).
- `memory`/`memory_scoped`/`isolation_leak`/`crash_recovery` modes are unmodified — `lib/rqlite.mjs`
  transparently routes to a local table engine (`lib/rqlite-stub.mjs`) instead of `fetch()`-ing a
  real rqlite whenever `rqliteUrl === 'stub:local'`. That engine persists each table to a JSON file
  under the OS temp dir, so `crash_recovery`'s `role:"run"`/`role:"resume"` pair (two **separate**
  process invocations) still observes cross-process persistence with zero network egress.
- `observability` mode needs no change — its OTel pipeline is already in-process only.

```bash
ARENA_STUB=1 node index.mjs <<< '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"Say OK."}]}'
```

Quality is **not** scored under this toggle — canned replies (`"[stub reply, ARENA_STUB=1] ..."`)
only prove the AI SDK integration code path runs end to end offline.

### Honest note: multi-tenant memory isolation is NOT an AI SDK feature

`memory` mode above keys purely by `session_id` — it proves persistence across process
invocations, but it does **not** test scoped or multi-tenant memory, which is aisotropy's
differentiator (`docs/architecture/multi-tenancy.md`). The **Vercel AI SDK has no native
primitive for multi-tenant memory isolation** — `generateText()`/`streamText()` just take a flat
`messages` array; there is no "scope," "tenant," or "namespace" concept anywhere in `ai` or
`@ai-sdk/openai`.

`memory_scoped` and `isolation_leak` below **hand-roll** a compound scope key
(`tenant_id`, `user_id`, `product_id`, `session_id`) over the same real rqlite table shape
`memory` mode uses (see `lib/scoped-store.mjs`), and every isolation guarantee comes from
manually including **all four** of those columns in every `WHERE` clause. **The developer is
responsible for that discipline — the SDK will not catch a mistake.** Forget one column (e.g.
filter by `session_id` alone, or by `tenant_id` + `product_id` but not `user_id`) and you have a
real cross-tenant data leak, not a hypothetical one — `isolation_leak` mode exists specifically to
make that failure mode observable (see "Verification runs" below for a captured leak-positive
control run). **Contrast: aisotropy enforces tenant scoping by construction** — `tenant_id`
travels exclusively via `context.Context`, injected once at the Huma handler boundary from the
validated path param, so there is no query path in aisotropy's memory tiers that *can* omit the
tenant filter in the first place (`.claude/rules/constitutional-gates.md` → "Multi-tenant data
isolation (hard gate)").

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
  "mode": "qa | rag | tools | memory | structured | stream | memory_scoped | isolation_leak | observability | crash_recovery | middleware",
  "system": "string, optional",
  "messages": [{"role": "user" | "assistant", "content": "string"}],
  "tools": [{"name": "string", "description": "string", "parameters": {"...JSON-Schema..."}}],
  "session_id": "string, REQUIRED for mode=memory|crash_recovery",

  "tenant_id": "string, REQUIRED for mode=memory_scoped|isolation_leak",
  "user_id": "string, REQUIRED for mode=memory_scoped|isolation_leak",
  "product_id": "string, REQUIRED for mode=memory_scoped|isolation_leak",
  "fact": "string, optional -- memory_scoped: write this fact under the primary scope above",
  "query": "string, optional -- memory_scoped/isolation_leak: the question asked of agnes (a default is used if omitted)",
  "cross_scope": {"tenant_id": "string", "user_id": "string", "product_id": "string", "session_id": "string, optional"},
  "victim": {"tenant_id": "string", "user_id": "string", "product_id": "string", "session_id": "string, optional"},
  "secret": "string, REQUIRED for mode=isolation_leak -- planted under \"victim\" scope",

  "role": "run | resume, REQUIRED for mode=crash_recovery",
  "turns": ["string, 2 or 3 REQUIRED for role=run -- user turns run sequentially in-process, checkpointed to rqlite after each"],
  "continuation": "string, REQUIRED for role=resume -- the follow-up turn a fresh process asks after loading rqlite state",
  "expected_fact": "string, optional for role=resume -- substring checked against recovered history (state_lost) and the new reply (precrash_fact_recalled)"
}
```

- `mode` defaults to `"qa"` when absent — the original single-shot arena task suite
  (`arenas/live/tasks/suite.jsonl`) runs unchanged against this adapter.
- `tools` is only read by `qa` mode (single-shot tool-call capture, no execution — see below).
  `tools` mode defines its own real `calculate` tool internally; it ignores `input.tools`.
- `rag` mode treats the last `user` message as the query.
- `memory` mode requires a non-empty `session_id`; `messages` are the NEW turn(s) for this
  invocation only (prior turns are loaded from rqlite, not replayed by the caller).
- `memory_scoped` requires non-empty top-level `tenant_id`/`user_id`/`product_id` (the "primary"
  scope this request acts under; `session_id` is optional, defaults to `''`). `fact` is optional —
  when present it is written into the store under the primary scope before the recall; omit it to
  test recall-only (e.g. from a separate process invocation, to prove persistence). `cross_scope`
  is optional — when present, it is recalled as the "different scope" probe used to compute
  `cross_tenant_miss`.
- `isolation_leak` requires non-empty top-level `tenant_id`/`user_id`/`product_id` (the
  **attacker** scope actually making this request) plus a nested `victim` object (same shape) and
  a non-empty `secret` string. The secret is planted under `victim`'s scope, then recalled under
  the top-level (attacker) scope — that recall result is what a real agnes turn is grounded in.
- `observability` ignores `tools`/`session_id`/scope fields — it always runs its own real
  `calculate` tool loop (same tool as `tools` mode) with `experimental_telemetry: { isEnabled: true }`
  wired to an in-process OTel pipeline; `messages`/`system` behave exactly as in `tools` mode.
- `crash_recovery` requires a non-empty `session_id` plus `role: "run"` or `role: "resume"`.
  `role: "run"` ("process A") additionally requires 2 or 3 non-empty `turns` strings, run
  sequentially IN THIS SAME PROCESS, checkpointed to rqlite after each one completes — it is meant
  to be killed (`kill -9`) partway through, by a caller like `test-crash.sh`. `role: "resume"`
  ("process B") additionally requires a non-empty `continuation` string and ignores `turns`; it
  loads whatever rqlite has for `session_id` (in a brand-new process) and asks one more real
  question. `expected_fact` is optional but required in practice to get non-`null`
  `state_lost`/`precrash_fact_recalled` values.
- `middleware` requires at least one message in `messages`; `system` is optional (defaults to
  `undefined`, exactly like `qa` mode). Runs ONE `generateText()` turn through a model wrapped via
  `wrapLanguageModel()` — see "`middleware`" below.

### Output contract (stdout, exactly one JSON object; all logs go to stderr)

```json
{
  "framework": "ai-sdk",
  "mode": "qa",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "text": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}, "output": null}],
  "retrieved": [{"doc_id": "string", "title": "string", "text": "string", "score": 0.0}],
  "structured": null,
  "ttft_ms": -1,
  "recalled": "string",
  "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true, "same_scope_facts": [], "cross_scope_facts": []},
  "leak_detected": null,
  "attacker_visible_facts": [],
  "otel_supported": null,
  "span_count": 0,
  "span_names": [],
  "span_timings_ms": {},
  "logs_structured": null,
  "log_sample": "string",
  "notes": "string",
  "resumed": null,
  "state_lost": null,
  "precrash_fact_recalled": null,
  "middleware": null,
  "error": "string"
}
```

`mode`, `retrieved`, `structured`, `ttft_ms`, `recalled`, `scoped_recall`, `leak_detected`,
`attacker_visible_facts`, `otel_supported`, `span_count`, `span_names`, `span_timings_ms`,
`logs_structured`, `log_sample`, `notes`, `resumed`, `state_lost`, `precrash_fact_recalled`, `text`,
and `middleware` are **additive** to the base `arenas/live/SPEC.md` contract: the shared Go runner
(`arenas/live/runner`)
decodes adapter stdout into a struct that silently ignores unknown JSON fields, so this stays
backward compatible. Fields not populated by a given mode keep their zero value (`[]`, `{}`, `null`,
`''`, `0`, or `-1`).

- `otel_supported`/`span_count`/`span_names`/`span_timings_ms`/`logs_structured`/`log_sample`
  (populated by `observability` only): whether the AI SDK's native OTel integration produced any
  finished spans for the turn, how many, their names in emission order, a `{name: duration_ms}`
  map (duplicate names get a ` #<n>` suffix so no timing is dropped), whether the closest thing to
  "logs" (span attributes) is structured JSON, and one representative span's
  `{name, kind, attributes}` payload as a JSON string.

- `recalled` (populated by `memory_scoped`): the same-scope recalled facts, newline-joined.
- `scoped_recall` (populated by `memory_scoped` only): `same_scope_hit` — the just-written `fact`
  (or any fact, if none was written this call) was found under the primary scope; `cross_tenant_miss`
  — the `cross_scope` probe (if supplied) did NOT return that fact.
- `leak_detected` (populated by `isolation_leak` only): `false` = good, no cross-tenant leak;
  `true` = the attacker scope's recall (or the model's reply) contained the victim's `secret`.
- `attacker_visible_facts` (populated by `isolation_leak` only): every fact the attacker's scope
  legitimately recalled — the raw evidence `leak_detected` is computed from.
- `resumed`/`state_lost`/`precrash_fact_recalled` (populated by `crash_recovery` `role: "resume"`
  only; stay `null` for `role: "run"` since the question doesn't apply to process A itself):
  `resumed` — the fresh process B loaded ANY persisted history for `session_id` from rqlite;
  `state_lost` — computed directly against what was loaded (not the model's behavior): `true` only
  if nothing survived, or `expected_fact` is missing from the recovered rows; `precrash_fact_recalled`
  — the live agnes reply to `continuation` actually contains `expected_fact` (a functional check,
  distinct from `state_lost`'s storage-level check). See "`crash_recovery`" below for why
  `state_lost=false` is the expected, honest outcome of this design even though the in-flight turn
  process A was killed during is genuinely gone.
- `text` (populated by `middleware` only; other modes leave it `""`): the same reply text also
  carried in `reply`, matching the mode's documented `{"text": "...", "middleware": {...}}` shape.
- `middleware` (populated by `middleware` only): `mechanism` — always `"wrapLanguageModel"`, the AI
  SDK native API used; `inbound_fired` — `transformParams` ran before the model call;
  `outbound_fired` — `wrapGenerate` (or `wrapStream`) ran around the model call; `invocations` — how
  many times the outbound wrapper fired (1 for this mode's single `generateText()` turn);
  `supported` — always `true` here (this is the reference implementation of a mechanism the AI SDK
  documents natively — see "`middleware`" below).

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

### Memory-extension dimensions — the honest verdict (`memory_cross_session` / `memory_semantic_recall` / `memory_consolidation`)

These runner-internal dimensions (`docs/architecture/arena-memory-extensions.md`) probe cross-session,
semantic, and consolidated memory. The verdict for the AI SDK is **honest infeasibility, not a
faked capability**: the AI SDK has **no official first-party memory primitive of any kind**. The
plain `memory` mode above passes only because THIS adapter hand-rolls rqlite persistence — that
adapter-invented store is legitimate to demonstrate session persistence, but wiring it into a
cross-session/semantic/consolidation probe would benchmark adapter glue, not the framework, so those
cells are declared `infeasible` rather than run against invented code. Pinned version: **`ai`
v7.0.31**, **`@ai-sdk/openai` v4.0.16** (`package.json`).

| Dimension | Verdict | Framework primitive (maturity) | Matrix cell |
|---|---|---|---|
| `memory_cross_session` | **infeasible** | NONE first-party — mem0/Letta are COMMUNITY providers (substitute an external service for the framework's own capability). https://ai-sdk.dev/providers/community-providers/mem0 ; https://github.com/letta-ai/vercel-ai-sdk-provider | `infeasible` all lanes (probe would benchmark the provider, not the AI SDK). |
| `memory_semantic_recall` | **infeasible** | NONE first-party — semantic recall only via the same community providers. | `infeasible` all lanes. |
| `memory_consolidation` | **infeasible** | NONE first-party — consolidation only via the same community providers. | `infeasible` all lanes. |

#### mem0-OSS exhibition row — decision: **DESIGNED, NOT BUILT**

`docs/architecture/arena-memory-extensions.md` §4 allows an OPTIONAL, non-competing, clearly-labeled
exhibition row running self-hosted OSS mem0 (Apache-2.0) — rendered OUTSIDE the primary table, the
same secondary/opt-in discipline `.claude/rules/arena-deterministic-judging.md` mandates for an
LLM-judge signal (putting `ai-sdk+mem0` in the primary table would measure mem0, the exact
judge-poisoning failure that rule prevents). **This row is NOT built.** It is gated on open
question #2 — whether `@mem0/vercel-ai-provider` can target a **self-hosted OSS** mem0 instance or
only the **hosted platform API** (the provider docs show only a platform API key). That question is
**unverifiable offline** (it needs a network/provider check this work is barred from making), and
per the task's own rule "do NOT build the exhibition row unless you can confirm offline it targets
self-hosted OSS; if uncertain, declare it infeasible and note the open question." Building it against
the hosted platform API would violate infra-parity (`docs/architecture/arena-infrastructure-parity.md`)
and measure a cloud service, not an in-process capability. So it is declared not-built here, with the
open question recorded — the honest choice over a faked or infra-unequal row.

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

### `memory_scoped`

Backed by `lib/scoped-store.mjs`, a hand-rolled compound-scope store over a new rqlite table
(`ai_sdk_arena_scoped_memory`, `CREATE TABLE IF NOT EXISTS`, columns `tenant_id`/`user_id`/
`product_id`/`session_id`/`fact`/`created_at`). One invocation:

1. **Writes** `fact` (if provided) under the primary `(tenant_id, user_id, product_id, session_id)`
   scope.
2. **Recalls** under that SAME scope (`SELECT ... WHERE tenant_id=? AND user_id=? AND
   product_id=? AND session_id=?`) → `same_scope_hit`.
3. **Recalls** under `cross_scope` (typically a different `tenant_id`) → `cross_tenant_miss`.
4. Grounds a real `generateText()` call in the same-scope recalled facts (system-prompt CONTEXT,
   same pattern as `rag` mode) and answers `query`.

Omitting `fact` on a later call with the same scope (a **separate** `node index.mjs` process
invocation) recalls what an earlier invocation wrote — proving REAL rqlite persistence, not an
in-process cache, the same property `memory` mode proves for `session_id`-only scoping.

### `isolation_leak`

Also backed by `lib/scoped-store.mjs` (same table). Plants `secret` under `victim`'s scope, then
recalls under the attacker's (top-level) scope and grounds a real `generateText()` call in
whatever that recall returned. `leak_detected` is `true` if the secret appears in the attacker's
recall (`attacker_visible_facts`) **or** in the model's reply text (defense-in-depth) — but per the
task, the authoritative isolation assertion is the store-level recall, not the model's behavior: a
correctly-scoped store never lets the secret reach the model's context in the first place. A
positive-control run (attacker scope == victim scope) legitimately returns `leak_detected: true`,
since self-access to one's own data is expected, not a leak — this proves the detector isn't
trivially always `false` (see `test-isolation.sh`).

### `observability`

Measures the AI SDK's **native** OpenTelemetry emission, not a bolted-on logger. `lib/otel.mjs`
starts a fresh, in-process `NodeTracerProvider` (`@opentelemetry/sdk-trace-node`) wired to a
`SimpleSpanProcessor` + `InMemorySpanExporter` (synchronous export on every `span.end()`, spans
kept in a plain JS array in this process — **no external collector, no OTLP network egress**),
registers it as the global OTel provider, then registers the AI SDK's own `@ai-sdk/otel`
`OpenTelemetry` integration via `registerTelemetry(new OpenTelemetry())` (both from the `ai`
package and the new `@ai-sdk/otel` package respectively). It then runs the same real, executed
`calculate` tool loop as `tools` mode (`stopWhen: stepCountIs(5)`, `experimental_telemetry: {
isEnabled: true, functionId: 'arena-observability-tools-turn' }`) — 2 real agnes chat calls
(decide-to-call-tool, then finish-after-tool-result) plus 1 real tool execution, so the emitted
span tree has more than one node.

**Key finding: `experimental_telemetry: { isEnabled: true }` alone is NOT sufficient** in this AI
SDK generation (`ai@7.0.31`). That flag only opts a given call INTO telemetry collection (or, set
to `false`, opts it out) — collection requires a `Telemetry` integration to already be registered
globally via `registerTelemetry()`. Without that registration, `isEnabled: true` is silently a
no-op: zero spans, zero errors. This is a real version-specific gotcha this adapter had to
discover empirically (confirmed by running with `experimental_telemetry` alone, no registration,
and observing `span_count: 0`) — the docs describe registration as step 1 and enabling as step 2,
but it is easy to skip step 1 and get no failure signal, only silent non-instrumentation.

Once registered, every `generateText`/`streamText`/`embed` call in the process emits spans
automatically per the [OTel GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
(`gen_ai.*`-prefixed attributes) — a real run against agnes produced 6 spans for one tool-loop turn:
a root `invoke_agent {modelId}` span (covers the whole operation), 2 `step N` spans, 2 `chat
{modelId}` spans (one per real provider call — `gen_ai.client.operation.duration`,
`gen_ai.usage.input_tokens`/`output_tokens`, full `gen_ai.input.messages`/`gen_ai.output.messages`
payloads), and 1 `execute_tool calculate` span (real tool args + result, `gen_ai.execute_tool.duration`).
Per-span wall-clock timing is native (`span.duration`, an OTel `HrTime` tuple converted to ms in
`lib/otel.mjs`'s `spanDurationMs()`) — no manual instrumentation needed anywhere in this adapter's
own code. There is no separate "structured logging" facility in the AI SDK: the closest equivalent
is the span attribute payload itself, which IS fully structured JSON key/value data (not a
plain-text log line) — `log_sample` in the output is exactly that payload for one representative
span.

### `crash_recovery`

Backed by a new rqlite table (`ai_sdk_arena_crash_recovery`, `CREATE TABLE IF NOT EXISTS`, same
shape `memory` mode's table uses). `memory` mode already proves persistence across TWO process
invocations, but only ever across a **clean** exit — the adapter always runs its single stdin/stdout
turn to completion and exits 0 before anything else touches that `session_id`. This mode proves the
harder claim: survival of a **hard `kill -9` while a turn is actually in flight**.

`role: "run"` ("process A") loops over 2-3 `turns` IN ONE PROCESS. For each turn it (1) calls
`generateText()`, then (2) checkpoints that turn's user+assistant messages to rqlite in a single
`/db/execute` call — rqlite executes a multi-statement request as one implicit transaction, so the
checkpoint is all-or-nothing: either both rows land, or (if killed before the call returns) neither
does. `test-crash.sh` starts this process, waits for turn 1's `PROGRESS: ... turn 1/2 committed`
stderr marker, then waits again for turn 2's `PROGRESS: ... turn 2/2 calling agnes` marker (proving
the second real agnes network call has started), then immediately sends `SIGKILL` — deliberately
inside the window between "turn 2 started" and "turn 2 committed."

`role: "resume"` ("process B") is a **completely fresh `node index.mjs` process** (new PID, new
process heap — not a retry, not a caught signal, not a restart of the same process) invoked
separately by `test-crash.sh` after process A is confirmed dead. It loads whatever rqlite has for
the same `session_id`, computes `resumed` and `state_lost` directly from that loaded data, then
makes one more real agnes call continuing the conversation and checks whether the reply actually
uses the recovered fact (`precrash_fact_recalled`).

**HONEST note — what actually survives a SIGKILL here, and what doesn't:**

- **Durable, per-turn checkpoints survive a hard kill.** Turn 1's commit happens strictly before
  turn 2 is even attempted, so nothing about killing the process during turn 2 can touch turn 1's
  already-committed rows — proven directly against rqlite in `test-crash.sh` (`rqlite-has-exactly-
  turn1-checkpoint`), not just inferred from the adapter's own output. This is why `state_lost`
  correctly comes back `false`: no *durable* state was lost, only work that was never durable to
  begin with.
- **The in-flight turn is genuinely, permanently gone — not delayed, not replayed.** Turn 2's
  content (`"...dolphins..."` in the test) never reaches rqlite; `test-crash.sh` independently
  confirms zero matching rows exist (`turn2-in-flight-data-absent`). There is no WAL/journal replay,
  no "resume the exact in-flight LLM call," no idempotency key that would let process B pick turn 2
  back up — process B simply never knows turn 2 was attempted. If the caller cared about that turn,
  it has to re-ask it explicitly (which is exactly what `continuation` does here).
- **This is a consequence of the adapter's OWN checkpoint-after-each-turn design, not an AI SDK
  feature.** `generateText()` itself holds no state between calls and offers no crash-recovery
  primitive at all — every bit of the durability behavior described above (atomic per-turn commit,
  load-on-resume, a completely separate process proving it) is hand-rolled in
  `lib/modes/crash-recovery.mjs` over the same real, networked rqlite `memory` mode already uses.
  Durability is bounded by checkpoint granularity: crash-safety here means "as of your last
  completed turn," never "as of the exact byte the process was killed at."
- **Answering the framework's own question directly: durable across SIGKILL, or only clean
  shutdown?** Both, but at different granularities. Clean shutdown (like `memory` mode) preserves
  every turn, because every turn gets to finish and checkpoint. A hard SIGKILL preserves every turn
  that had ALREADY finished and checkpointed before the kill — and unconditionally discards the one
  turn that was in flight at the moment of the kill. The AI SDK contributes nothing to either
  guarantee; both come entirely from this adapter's rqlite checkpoint discipline.

### `middleware`

The REFERENCE demonstration of the AI SDK's native `wrapLanguageModel({ model, middleware })`
mechanism (`ai` package — [docs](https://ai-sdk.dev/docs/ai-sdk-core/middleware)), exactly the
pattern the AI SDK itself documents for intercepting a language model call. `lib/modes/middleware.mjs`
builds a plain `LanguageModelMiddleware` object with two hooks and wraps `env.chatModel` in it:

- **INBOUND — `transformParams`**: runs before the underlying model ever sees the call. It appends
  the sentinel `"\n[ARENA_MW]"` to the `role: 'system'` entry of `params.prompt` — the AI SDK folds
  the high-level `system` string option into that entry before handing params to
  `transformParams`, so mutating it there is a real, provable "the model call itself changed"
  interception, not a side-channel.
- **OUTBOUND — `wrapGenerate`/`wrapStream`**: runs around the real `doGenerate`/`doStream` call.
  Both simply call through to the real function, but first increment a counter and set a fired flag
  — proving the middleware actually executed around the call, rather than being a middleware object
  that's declared but never invoked. `wrapStream` is wired for completeness (fires if the same
  wrapped model is ever driven through `streamText()`), but this mode's own turn uses
  `generateText()`, so `wrapGenerate` is what's expected to fire.

One real `generateText()` turn runs through the wrapped model. `middleware.inbound_fired` and
`middleware.outbound_fired` are read directly off the counters set by the two hooks above — not
inferred from the reply text — so a regression that silently stopped `wrapLanguageModel()` from
actually wrapping the model (e.g. an AI SDK version bump that changed the mechanism's shape) would
show up as `false`/`false` here, not a false positive. Contrast with `qa` mode: it calls
`generateText()` directly against the unwrapped `env.chatModel`, with no middleware at all — the
two modes are the honest A/B for "does `wrapLanguageModel()` change anything."

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

## Automated scoped-memory/isolation tests (`test-isolation.sh`)

```bash
bash arenas/live/ai-sdk/test-isolation.sh
```

Runs 4 real agnes calls (well under the 8-call budget) and PASS/FAILs 6 assertions, never printing
the key value itself:

1. **memory-scoped-same-scope-hit** — write a fact under `(tenant_id=test-tenant-a-<run>,
   user_id=alice-<run>, product_id=widgets-pro-<run>, session_id=s1)`, recall the SAME scope in the
   same call → `same_scope_hit == true`.
2. **memory-scoped-cross-tenant-miss** — the same call's `cross_scope` (a different `tenant_id`)
   recall does NOT contain the fact → `cross_tenant_miss == true`.
3. **memory-scoped-cross-process-persistence** — a SECOND, separate `node index.mjs` process
   invocation, same scope, recall-only (no `fact`) → `recalled` matches the fact written by
   invocation #1, proving real rqlite persistence, not an in-process cache.
4. **isolation-leak-zero-leak** — tenant B (attacker) queries after tenant A's (victim) secret is
   planted → `leak_detected == false`.
5. **isolation-leak-positive-control** — attacker scope == victim scope (self-access) →
   `leak_detected == true`, proving the detector legitimately fires and isn't hardcoded to `false`.
6. **key-never-logged** — the key value's match count is zero across all 4 calls' stdout/stderr.

Last verified run: `SUMMARY: 6 passed, 0 failed`.

## Automated crash-recovery test (`test-crash.sh`)

```bash
bash arenas/live/ai-sdk/test-crash.sh
```

Runs 3 real agnes calls (2 attempted by process A — the second deliberately interrupted mid-flight
— plus 1 by process B; well under the 8-call budget) and PASS/FAILs 8 assertions, never printing the
key value itself:

1. **turn1-checkpoint-committed** — process A's stderr shows turn 1's checkpoint committed before
   it is killed.
2. **sigkill-delivered-mid-turn2** — process A was still alive and mid-turn-2 (not yet exited on its
   own) at the exact moment `kill -9` was sent.
3. **rqlite-has-exactly-turn1-checkpoint** — independent evidence pulled directly from rqlite (not
   from the adapter): exactly 2 rows exist for the session (turn 1's user+assistant pair only).
4. **turn2-in-flight-data-absent** — zero rqlite rows mention turn 2's topic, proving the in-flight
   turn was truly never persisted (not a lucky timing race that happened to look right).
5. **process-b-no-error** — the fresh resume process's `error` field is empty.
6. **resumed-true** — `resumed == true`.
7. **state-lost-false** — `state_lost == false`.
8. **precrash-fact-recalled-true** — `precrash_fact_recalled == true`.

Last verified run: `SUMMARY: 8 passed, 0 failed`.

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

**`memory_scoped`**, invocation #1 (fresh process, writes a fact under tenant `acme-corp` and
proves a different tenant, `globex-inc`, cannot see it in the same call):

```json
{"framework":"ai-sdk","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"I do not have access to information about \"your\" favorite deploy region. The only fact I have in memory is that Alice's favorite deploy region is eu-west-1.","recalled":"Alice's favorite deploy region is eu-west-1.","scoped_recall":{"same_scope_hit":true,"cross_tenant_miss":true,"same_scope_facts":["Alice's favorite deploy region is eu-west-1."],"cross_scope_facts":[]},"error":""}
```

**`memory_scoped`**, invocation #2 (a *second, completely separate* `node index.mjs` process,
same scope, no `fact` — recalls what invocation #1 wrote, purely from rqlite):

```json
{"framework":"ai-sdk","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"I do not have access to information about \"your\" favorite deploy region. The only fact I have in memory is that Alice's favorite deploy region is eu-west-1.","recalled":"Alice's favorite deploy region is eu-west-1.","scoped_recall":{"same_scope_hit":true,"cross_tenant_miss":true,"same_scope_facts":["Alice's favorite deploy region is eu-west-1."],"cross_scope_facts":[]},"error":""}
```

**`isolation_leak`**, negative test (attacker `globex-inc`/`bob` queries after victim
`acme-corp`/`alice`'s secret is planted — zero leak):

```json
{"framework":"ai-sdk","mode":"isolation_leak","model":"agnes-2.0-flash","reply":"I do not know any secrets about your account. I have no information stored regarding your personal details or account status.","recalled":"","scoped_recall":null,"leak_detected":false,"attacker_visible_facts":[],"error":""}
```

**`isolation_leak`**, positive control (attacker scope == victim scope — self-access legitimately
detected, proving the detector is real, not hardcoded `false`):

```json
{"framework":"ai-sdk","mode":"isolation_leak","model":"agnes-2.0-flash","reply":"I have no information about your account.","leak_detected":true,"attacker_visible_facts":["Alice's favorite deploy region is eu-west-1.","The acme production DB password is Tr0ub4dor-and-3.","The self-access verification code is XQ-4471."],"error":""}
```

**`test-isolation.sh`** full run:

```
PASS  memory-scoped-same-scope-hit -- error='' same_scope_hit=true
PASS  memory-scoped-cross-tenant-miss -- error='' cross_tenant_miss=true
PASS  memory-scoped-cross-process-persistence -- error='' recalled(fresh process) matches Test 1's fact
PASS  isolation-leak-zero-leak -- error='' leak_detected=false (want false)
PASS  isolation-leak-positive-control -- error='' leak_detected=true (want true)
PASS  key-never-logged -- key-literal match count across all 4 tests' stdout/stderr = 0 (want 0)

SUMMARY: 6 passed, 0 failed
```

**Docker**, `qa` and `rag` both verified end-to-end against the real local infra over
`host.docker.internal` (see "Docker packaging" above) — same grounded answer and gold-doc
retrieval as the native `rag` run.

**`observability`** (real tool-loop turn, `experimental_telemetry: { isEnabled: true }` +
`registerTelemetry(new OpenTelemetry())`; 6 real spans captured by the in-process
`InMemorySpanExporter`, zero external collector):

```json
{"framework":"ai-sdk","mode":"observability","model":"agnes-2.0-flash","reply":"The result of $(23847 \\times 9182) / 17$, rounded to two decimal places, is **12880185.53**.","input_tokens":1395,"output_tokens":85,"latency_ms":14093,"tool_calls":[{"name":"calculate","arguments":{"expression":"(23847 * 9182) / 17"},"output":{"expression":"(23847 * 9182) / 17","result":12880185.529411765}}],"otel_supported":true,"span_count":6,"span_names":["chat agnes-2.0-flash","execute_tool calculate","step 1","chat agnes-2.0-flash","step 2","invoke_agent agnes-2.0-flash"],"span_timings_ms":{"chat agnes-2.0-flash":7042.617,"execute_tool calculate":0.425,"step 1":7044.726,"chat agnes-2.0-flash #2":7033.672,"step 2":7034.037,"invoke_agent agnes-2.0-flash":14083.245},"logs_structured":true,"error":""}
```

(`log_sample` and `notes` omitted above for brevity — see "Per-mode notes -> `observability`" for
what they contain; `log_sample` is one representative span's `{name, kind, attributes}` payload,
~1.4KB of structured `gen_ai.*` JSON.)

**Negative-control run** (`experimental_telemetry: { isEnabled: true }` set on the call, but
`registerTelemetry(new OpenTelemetry())` deliberately NOT called first — confirms registration,
not the per-call flag, is what actually turns telemetry on):

```json
{"reply":"hi","span_count":0,"span_names":[]}
```

**`crash_recovery`**, process B (`role: "resume"`) — a completely fresh process, loaded rqlite state
after process A was `kill -9`'d mid-turn-2, and recalled the pre-crash fact in a real completion:

```json
{"framework":"ai-sdk","mode":"crash_recovery","model":"agnes-2.0-flash","reply":"Zebra-9142","input_tokens":355,"output_tokens":8,"latency_ms":6739,"recalled":"user: Please remember this important fact and just acknowledge it in one short sentence, nothing else: The secret vault code is Zebra-9142.\nassistant: I have noted that the secret vault code is Zebra-9142.","resumed":true,"state_lost":false,"precrash_fact_recalled":true,"error":""}
```

**`test-crash.sh`** full run:

```
PASS  turn1-checkpoint-committed -- process A stderr shows 'turn 1/2 committed' before it is killed
PASS  sigkill-delivered-mid-turn2 -- process A was still alive and mid-turn-2 at the moment kill -9 was sent
PASS  rqlite-has-exactly-turn1-checkpoint -- raw rqlite row count for session = 2 (want 2)
PASS  turn2-in-flight-data-absent -- rows mentioning turn 2's topic ('dolphin') for this session = 0 (want 0)
PASS  process-b-no-error -- error field = '' (want empty)
PASS  resumed-true -- resumed = true (want true)
PASS  state-lost-false -- state_lost = false (want false)
PASS  precrash-fact-recalled-true -- precrash_fact_recalled = true (want true)

SUMMARY: 8 passed, 0 failed
```

## Licenses (direct + meaningful transitive dependencies)

`memory_scoped` and `isolation_leak` add **zero new npm dependencies**: `lib/scoped-store.mjs`
reuses `lib/rqlite.mjs`'s existing native-`fetch` HTTP client (same pattern `memory` mode already
uses), and both new mode files only import `generateText` from the already-installed `ai` package.

`observability` adds **3 new direct dependencies** (`@ai-sdk/otel`, `@opentelemetry/api`,
`@opentelemetry/sdk-trace-node`) plus their transitive OTel packages — all Apache-2.0, all resolved
via a real `npm install` (`package.json` / `package-lock.json` updated accordingly).

`crash_recovery` adds **zero new npm dependencies**: `lib/modes/crash-recovery.mjs` reuses
`lib/rqlite.mjs`'s existing native-`fetch` HTTP client (same pattern `memory` mode already uses)
and only imports `generateText` from the already-installed `ai` package. `package.json` /
`package-lock.json` are unchanged by this addition.

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
| `@ai-sdk/otel` | 1.0.31 | Apache-2.0 | **yes — new, added for `observability` mode** |
| `@opentelemetry/api` | 1.9.1 | Apache-2.0 | **yes — new, added for `observability` mode** |
| `@opentelemetry/sdk-trace-node` | 2.9.0 | Apache-2.0 | **yes — new, added for `observability` mode** |
| `@opentelemetry/sdk-trace-base` | 2.9.0 | Apache-2.0 | **yes — new, transitive of `@opentelemetry/sdk-trace-node`** |
| `@opentelemetry/context-async-hooks` | 2.9.0 | Apache-2.0 | **yes — new, transitive of `@opentelemetry/sdk-trace-node`** |
| `@opentelemetry/core` | 2.9.0 | Apache-2.0 | **yes — new, transitive of `@opentelemetry/sdk-trace-node`** |
| `@opentelemetry/resources` | 2.9.0 | Apache-2.0 | **yes — new, transitive of `@opentelemetry/sdk-trace-node`** |
| `@opentelemetry/semantic-conventions` | 1.43.0 | Apache-2.0 | **yes — new, transitive of `@opentelemetry/sdk-trace-node`** |

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
| `lib/scoped-store.mjs` | hand-rolled compound-scope (`tenant_id`/`user_id`/`product_id`/`session_id`) rqlite store backing `memory_scoped` and `isolation_leak` |
| `lib/otel.mjs` | in-process OTel wiring (`NodeTracerProvider` + `InMemorySpanExporter` + `@ai-sdk/otel` registration) backing `observability` |
| `lib/stub.mjs` | `ARENA_STUB=1` offline doubles: `MockLanguageModelV4`-backed chat model + a local keyword-search corpus for `rag` |
| `lib/rqlite-stub.mjs` | local, file-persisted table engine `lib/rqlite.mjs` routes to when `rqliteUrl === 'stub:local'` |
| `lib/modes/qa.mjs` | original single-shot mode |
| `lib/modes/rag.mjs` | real RAG: embed → Qdrant search → grounded `generateText()` |
| `lib/modes/tools.mjs` | real multi-step tool execution loop |
| `lib/modes/memory.mjs` | rqlite-persisted conversational memory across process invocations, keyed by `session_id` only |
| `lib/modes/structured.mjs` | `generateObject()` against a zod schema |
| `lib/modes/stream.mjs` | `streamText()` with time-to-first-token |
| `lib/modes/memory-scoped.mjs` | write/recall a fact under a compound scope + prove cross-tenant miss, grounded in a real agnes turn |
| `lib/modes/isolation-leak.mjs` | plant a secret under a victim scope, recall under an attacker scope, prove (or disprove) a real leak |
| `lib/modes/observability.mjs` | wires the in-process OTel SDK, runs a real tool-loop turn, reports native span count/names/timings |
| `lib/modes/crash-recovery.mjs` | `role:"run"` checkpoints a short conversation to rqlite turn-by-turn (meant to be SIGKILLed mid-turn); `role:"resume"` is a fresh process proving recovery + recall |
| `lib/modes/middleware.mjs` | reference `wrapLanguageModel()` demo: inbound `transformParams` + outbound `wrapGenerate`/`wrapStream` around one real turn |
| `run.sh` | native entrypoint (sources repo-root `.env`) |
| `Dockerfile` | container image, no baked-in credentials |
| `run-docker.sh` | build + run the container against real local infra via `host.docker.internal` |
| `test-env.sh` | automated credential/env PASS/FAIL assertions |
| `test-isolation.sh` | automated PASS/FAIL evidence: same-scope hit, cross-tenant miss, cross-process persistence, zero leak, positive control |
| `test-crash.sh` | automated PASS/FAIL evidence: SIGKILL mid-turn-2, independent rqlite proof of partial commit, fresh-process resume + recall |
| `.dockerignore` | keeps `.env`, `node_modules`, and test artifacts out of the build context |
