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
into eleven modes selected by a `"mode"` field on stdin: the original six (`qa`, `rag`, `tools`,
`memory`, `structured`, `stream`), two multi-tenant scoped-memory modes, `memory_scoped`
and `isolation_leak` — see [Multi-tenant scoped memory](#multi-tenant-scoped-memory-memory_scoped-isolation_leak-modes)
below for why these exist and what they prove — `crash_recovery`, which proves whether the
app survives a **hard `kill -9` mid-conversation**, distinct from `memory` mode's clean-exit
recall proof — see [`crash_recovery` mode](#crash_recovery-mode) below — `observability`,
which measures Eino's own tracing/logging surface (native `callbacks.Handler` vs. OTel-via-
`eino-ext`) by actually running a multi-step turn through it — see
[`observability` mode](#observability-mode) below — and `middleware`, which proves whether Eino
lets you intercept a chat call inbound and outbound, using its two native seams
(`callbacks.Handler` for outbound observation, a `model.BaseChatModel` decorator for inbound
mutation) — see [`middleware` mode](#middleware-mode) below. `observability` and `middleware`
each write their OWN JSON contract, distinct from the shared one in [Contract](#contract) below.

Separately, setting `ARENA_STUB=1` swaps every mode's chat model for a local, network-free fake
(`stub.go`) — see [`stub` toggle](#stub-toggle-arena_stub1) below.

## Contract

Reads **one JSON object from stdin**:

```jsonc
{
  "mode": "qa | rag | tools | memory | structured | stream | memory_scoped | isolation_leak | crash_recovery | observability | middleware",  // default "qa"
  "system": "string",                        // optional system prompt
  "messages": [{"role": "user|assistant", "content": "string"}],
  "tools": [{"name": "...", "description": "...", "parameters": {/* JSON-Schema */}}], // qa mode only
  "session_id": "string",                    // memory/crash_recovery mode: required, correlates turns across process invocations; memory_scoped/isolation_leak: optional extra scope layer
  "top_k": 4,                                 // rag mode: retrieval depth (optional, default 4)
  "schema": {/* JSON-Schema object */},       // structured mode: override the default schema (optional)
  "schema_name": "string",                    // structured mode: schema name (optional, default "structured_answer")
  "tenant_id": "string",                      // memory_scoped/isolation_leak: required
  "user_id": "string",                        // memory_scoped/isolation_leak: required
  "product_id": "string",                     // memory_scoped/isolation_leak: required
  "fact": "string",                           // memory_scoped/isolation_leak: write/seed payload — store this under the scope above
  "query": "string",                          // memory_scoped/isolation_leak: recall/probe payload — ask this against the scope above
  "crash_turns": ["string"],                  // crash_recovery mode, producer call: ordered user turns to run+persist one at a time
  "expect_substring": "string"                // crash_recovery mode, resumer call: substring the reply must contain to count as pre-crash recall
}
```

Exactly one of `fact` (write/seed) or `query` (recall/probe) must be set for `memory_scoped` and
`isolation_leak`; both or neither is a validation error. Exactly one of `crash_turns` (producer)
or `messages` (resumer) must be set for `crash_recovery`; both or neither is a validation error.

Writes **exactly one JSON object to stdout** (all logs go to stderr):

```jsonc
{
  "framework": "eino",
  "mode": "qa | rag | tools | memory | structured | stream | memory_scoped | isolation_leak | crash_recovery",
  // NOTE: "observability" mode does NOT use this contract at all — it writes a completely
  // different JSON shape, documented in "observability mode" below.
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}, "result": "string (tools mode only)"}],
  "retrieved": [{"doc_id": "string", "title": "string", "score": 0.0, "text": "string"}],
  "structured": null,
  "ttft_ms": 0,
  "error": "string",
  "scoped_recall": null,   // memory_scoped recall calls only: {"same_scope_hit": bool, "cross_tenant_miss": bool, "recalled_text": "string"}
  "leak_detected": null,   // isolation_leak probe calls only: bool (false = no leak, good)
  "resumed": null,               // crash_recovery resumer calls only: bool — prior persisted turns were found on disk
  "precrash_fact_recalled": null,// crash_recovery resumer calls only: bool — the reply actually contains expect_substring
  "state_lost": null             // crash_recovery resumer calls only: bool — negation of resumed (no persisted turns survived at all)
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

### `crash_recovery` mode

`memory` mode above only proves recall across a **clean process exit** — the process finishes,
returns, and a later invocation reads what it wrote. `crash_recovery` proves something strictly
stronger and different: does the app survive a **hard `kill -9` mid-conversation**, with no
graceful shutdown, no `defer`, no `Close()` ever running? Implementation: `mode_crash.go`, driven
end-to-end by `test-crash.sh`.

It deliberately **reuses the exact same on-disk SQLite `memoryStore` that `memory` mode uses**
(`mode_memory.go`'s `openMemoryStore`/`load`/`append` — the same `turns` table, the same file) —
not a second, crash-only store — because the point is to test the durability of the one real
persistence layer this adapter has, not to build a differently-tuned store that might pass a
crash test for reasons that don't generalize.

The scenario is two REAL, separate OS processes talking to the real agnes model:

1. **Process A (producer)** runs a 2-turn conversation on one `session_id`
   (`"crash_turns": ["turn 1 text", "turn 2 text"]`). Turn 1 states a fact; the model's Generate
   call returns, and **only then** is the turn committed to SQLite (`mode_memory.go`'s
   `store.append` opens no transaction until the Generate call it depends on has already
   succeeded — so there is never a half-written turn on disk to begin with). Turn 2's real
   network call is then dispatched. `test-crash.sh` waits for a stderr synchronization line
   (`"turn 2 starting"`) that proves turn 1 is committed AND turn 2 is genuinely in-flight, then
   sends `SIGKILL` directly to the process (a real standalone binary, not a `go run .` wrapper,
   so the signal lands on the process actually holding the SQLite connection and the open HTTP
   call — a `go run` parent can shield its child from a misdirected signal).
2. **Process B (resumer)** is a completely fresh process (`"messages": [...]`, `crash_turns`
   omitted) started after process A is confirmed dead. It loads whatever `session_id` already
   has on the SAME on-disk file and answers a question only answerable by recalling the turn-1
   fact, reporting three signals: `resumed` (prior turns were found on disk at all), `state_lost`
   (the negation of `resumed` — nothing survived), and `precrash_fact_recalled` (the reply
   actually contains `expect_substring`, i.e. the fact was truly recalled, not just that some
   history existed).

**HONEST durability note — read this before trusting `resumed: true` for anything beyond what it
actually tests:** this on-disk SQLite store is durable across a hard `kill -9` **of the
application process** for the reason demonstrated above — each turn is one committed SQLite
transaction, and no transaction is ever opened for a turn whose model call hasn't already
succeeded, so a mid-flight kill can only ever lose the *one* turn that was truly in-flight, never
corrupt an already-committed one. It is **not** proof of durability across every failure class:
this repo runs SQLite with its default rollback-journal/sync settings (no WAL, no
`PRAGMA synchronous` override), so it relies on the OS's own fsync-on-commit guarantee — a kill
of the *application process* (what this test does) leaves the OS page cache and disk intact, but
an actual **host power-loss or OS crash** at the exact instant of an uncommitted fsync is a
different, unverified failure mode this test does not exercise. In short: **durable across
`kill -9` of the process, verified here; durability across power loss/host crash is a separate
claim this test makes no assertion about.**

DB path: same `EINO_ARENA_MEMORY_DB` env var as `memory` mode (see above) — `test-crash.sh`
overrides it to an isolated temp file per run so the scenario never touches the shared
`.data/memory.db` used by manual `memory`-mode smoke tests.

### Multi-tenant scoped memory (`memory_scoped`, `isolation_leak` modes)

**Honest note: Eino has no native multi-tenant memory isolation.** There is no key-prefixing
primitive, no payload filter, nothing in Eino's component model that scopes stored state by
tenant/user/product. Both modes below hand-roll a compound scope key —
`tenant_id\x1ftenant_id\x1fuser_id\x1fproduct_id[\x1fsession_id]` (ASCII Unit Separator, joined
in `scopeKey()`, `mode_scoped_memory.go`) — over the same kind of real, on-disk SQLite store
`memory` mode already uses (`mode_memory.go`, new `scoped_facts` table in the same
`.data/memory.db`). **The developer is responsible for getting this key right; a mistake here —
forgetting to fold `tenant_id` into the key, swapping argument order, reusing a key across
tenants — is a silent isolation leak, not a compile error.** `isolation_leak` mode exists
specifically to catch that failure mode with a real, adversarial probe rather than trusting the
implementation by inspection.

**Contrast: aisotropy enforces multi-tenant isolation by construction**, not by developer
discipline — see `docs/architecture/multi-tenancy.md` and
`.claude/rules/constitutional-gates.md` in the aisotropy repo root. `tenant_id` travels
exclusively via `context.Context`, injected once at the Huma handler boundary from the validated
path param; every memory-tier read/write is required to filter by tenant (key-prefix in
BadgerDB/rqlite, and for Qdrant a **payload filter applied during the HNSW scan itself**, not a
post-filter — a post-filter is treated as an isolation leak, not a fix, per the constitutional
gate). There is no code path where a forgotten field silently mixes two tenants' data — the
isolation is structural, checked by the same gate that would block this arena's hand-rolled
`scopeKey()` if it shipped inside aisotropy itself. This arena exists precisely to make that
contrast concrete: Eino gives you a chat model and a graph; multi-tenant isolation is 100% on
you.

#### `memory_scoped` mode

Stores or recalls a fact under the hand-rolled compound scope `(tenant_id, user_id, product_id[,
session_id])`. Which operation runs is inferred from the payload: a non-empty `"fact"` writes (a
pure store operation — no LLM call), a non-empty `"query"` recalls (a **real agnes `Generate`
call**, grounded in a system prompt built from exactly what the scoped store returned for that
scope; see `scopedGenerate` in `mode_scoped_memory.go`).

The recall response's `scoped_recall` object reports two complementary views of the same store
lookup:
- `same_scope_hit`: `true` when the recall's own scope matched at least one stored fact — the
  case that proves "recall under the same scope I wrote to" works.
- `cross_tenant_miss`: `true` when it matched nothing — the case that proves "recall under a
  scope I never wrote to" (e.g. a different `tenant_id`) correctly finds nothing.

Both fields are always populated; whichever is `true` depends on what the caller is testing.
`recalled_text` is the raw joined fact text the store returned (before the model ever sees it),
so a grader can verify what was actually fed into the prompt, not just the model's paraphrase.

#### `isolation_leak` mode

Seeds a secret under one tenant's scope (`"fact"`, e.g. tenant A) or probes a scope for leakage
(`"query"`, e.g. as tenant B), reusing the **exact same `scoped_facts` store** `memory_scoped`
writes to (`openScopedMemoryStore`, same DB file, same table). The `leak_detected` boolean is
**not** derived from the model's phrasing — it is computed in Go from what the store actually
returned for the probing scope: every row is re-checked against the probing request's
`tenant_id`, and `leak_detected` is `true` only if a row belonging to a *different* tenant came
back. Given the store query filters by `scope_key` (which embeds `tenant_id`), this can only
happen if `scopeKey()` itself was computed without `tenant_id` — i.e. the exact hand-rolled
mistake this mode exists to catch.

`false` (no leak) is the expected, verified result — see
[Verified smoke tests](#isolation--multi-tenant-scoped-memory-real-agnes-calls) below for the
real seed-under-A / probe-as-B run.

### Memory-extension dimensions — the honest verdict (`memory_cross_session` / `memory_semantic_recall` / `memory_consolidation`)

These runner-internal dimensions (`docs/architecture/arena-memory-extensions.md`) arrive on the wire
as ordinary `memory` turns plus a second scope axis, `user_id`, HELD constant while `session_id`
VARIES across invocations — defeating session-keyed transcript replay by construction. The verdict is
**honest, not inflated**: Eino has no user-scoped (indeed no official persisted) memory component, so
this adapter's SQLite store stays keyed by `session_id` (the level Eino's own primitives reach) and
it does **not** synthesize a user-keyed store from `user_id` — that would be a hand-rolled strawman
the fairness protocol forbids (`docs/architecture/arena-fairness-protocol.md`). Pinned version:
**`github.com/cloudwego/eino` v0.9.12** (`go.mod`); the research checked the `adk` subpackage at
v0.9.13 on pkg.go.dev.

| Dimension | Verdict | Framework primitive (maturity) | Evidence / matrix cell |
|---|---|---|---|
| `memory_cross_session` | **run-and-fail** (wired) | `adk` in-process ctx KV + `CheckPointStore` (interrupt/resume only) — session/interrupt-keyed, NOT user-scoped; `MemoryStore` exists only in `eino-examples`. Maturity: NONE in core (example-only). https://pkg.go.dev/github.com/cloudwego/eino/adk ; https://github.com/cloudwego/eino/issues/203 | `mode_memory.go` reads `user_id` and logs that Eino has no user-scoped store, keying `session_id`; a fresh-session recall loads zero turns → honest FAIL. Matrix: real=`pending` (scored on the real lane), stub=`infeasible` (`:memory:` SQLite can't cross the persist→recall process boundary), wasm=`infeasible` (sockets). |
| `memory_semantic_recall` | **infeasible** | NONE — no semantic-memory component in Eino core; ranked recall would be adapter-invented. | Matrix: `infeasible` all lanes. |
| `memory_consolidation` | **infeasible** | NONE — no fact-extraction/consolidation component in Eino core. | Matrix: `infeasible` all lanes. |

Ecosystem sweep basis: `docs/architecture/arena-memory-extensions.md` §1. Eino's `.cn` docs were
DNS-unreachable during the research (open question #4); the pkg.go.dev `adk` API surface (no
memory-store types) is the substitute evidence.

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

### `observability` mode

Measures Eino's own observability surface instead of answering a question. Unlike every other
mode, `observability` writes a **different JSON contract**, not the shared one above:

```jsonc
{
  "framework": "eino",
  "mode": "observability",
  "otel_supported": true,
  "span_count": 4,
  "span_names": ["eino.ChatModel", "eino.calculate", "eino.ReActAgent", "eino.ChatModel"],
  "span_timings_ms": {"eino.ChatModel": 6949.31, "eino.ChatModel#2": 6763.64, "eino.ReActAgent": 6949.51, "eino.calculate": 0.08},
  "logs_structured": false,
  "log_sample": "{\"time\":\"...\",\"level\":\"INFO\",\"msg\":\"eino_observability_turn\",...}",
  "notes": "string — full write-up of what was measured and how"
}
```

Implementation: `observability.go` (the OTel/callbacks plumbing) + `mode_observability.go` (the
scenario + JSON contract). Ignores `mode`-contract fields like `tenant_id`; if `messages` is
given it uses the last user message as the question, otherwise it defaults to the same
arithmetic question `tools` mode's smoke test uses (`"What is (48 + 12) * 3 / 2? Use the
calculator tool."`) — a genuine multi-step ReAct turn (tool-decision call → real tool execution
→ final-answer call), not a single-shot call, so the trace is representative.

**Native mechanism — `callbacks.Handler`.** Eino's *only* built-in observability primitive is
`github.com/cloudwego/eino/callbacks` — the same `OnStart`/`OnEnd`/`OnError`/
`OnStartWithStreamInput`/`OnEndWithStreamOutput` seam every other mode's `usageToolTracker`
already uses (see `mode_tools.go`) to capture token usage and real tool-call arguments/results.
It fires once per component invocation (`ChatModel`, `Tool`, the `Tools` node, and the graph
itself) and hands the handler a `*callbacks.RunInfo{Name, Type, Component}` plus a
component-typed input/output. There is **no built-in aggregation, span concept, or exporter** in
`eino` core — a callback handler is a raw hook, not a tracing system. `eino` itself does not
import `go.opentelemetry.io/otel` at all; any OTel story has to come from something built *on
top of* `callbacks.Handler`.

**Three community OTel options were evaluated** — the decision below is evidence-based (source
read directly, not inferred from docs), per this repo's `.claude/rules/prune-evidence-gate.md`
code-truth discipline:

| Option | What it is | In-process/offline? |
|---|---|---|
| (a) `github.com/cloudwego/eino-ext/callbacks/apmplus`, built on `github.com/cloudwego/eino-ext/libs/acl/opentelemetry` | eino-ext's own `callbacks.Handler` → OTel bridge, `gen_ai.*` attributes + token/TTFT/TPOT metrics | **No.** `opentelemetry.NewOpenTelemetryProvider` only constructs a `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` exporter — no in-memory/stdout option exists in that package, so it cannot even construct without a live OTLP collector endpoint (`Host`/`AppKey`). |
| (b) `github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino` (Apache-2.0) | A standalone, importable Go module — `traceeino.Handler` implements `callbacks.Handler` and turns ChatModel/Tool/Embedding callback firings into real OTel spans (`gen_ai.*` attributes + a `braintrust.*` attribute set) | **Yes.** `HandlerOptions.TracerProvider` is an *optional* `go.opentelemetry.io/otel/trace.TracerProvider` — verified by reading `traceeino.go` directly: it makes zero network calls and needs no Braintrust API key to construct or emit spans. Handing it an in-memory-exporter-backed provider (as this mode does) captures real spans fully offline. |
| (c) `github.com/alibaba/loongsuite-go-agent` | OTel **compile-time** instrumentation for Go — you build your binary through an `otel go build` wrapper, not an importable library | **Not applicable as a Go module dependency.** It's a build-toolchain plugin, and per its own docs, Eino support is being "released sequentially" framework-by-framework — not confirmed as a stable, generally-available integration at the time of writing. |

This mode adopted **(b)** — the only one of the three that is both a plain `go get`-able
community package and capable of in-process/offline span capture. `observability.go`'s
`obsRecorder` builds a real `go.opentelemetry.io/otel/sdk/trace.TracerProvider` backed by
`go.opentelemetry.io/otel/sdk/trace/tracetest.InMemoryExporter` (part of the OTel Go SDK itself,
not a third-party mock) wired via `sdktrace.WithSyncer` (synchronous export, no batching
goroutine, zero network I/O), then hands that provider to `traceeino.NewHandlerWithOptions`. Eino's
native `callbacks.Handler` seam drives the packaged handler exactly the same way `mode_tools.go`'s
`usageToolTracker` drives its own bookkeeping — **this mode writes zero span-construction code of
its own**, unlike its previous iteration (a hand-rolled ~30-line `tracer.Start()`/`span.End()`
bridge) — the packaged module owns that entirely now. A second, minimal `callbacks.Handler`
(`obsRecorder.counterHandler()`, still hand-rolled, ~15 lines) is registered alongside it purely to
count raw callback firings for the `raw_callback_events` figure in `notes`/`log_sample` — Eino
copies the callback stream independently per registered handler
(`github.com/cloudwego/eino/callbacks/aspect_inject.go`), so the two handlers never interfere.

**Honest scope note, from a real run, not speculation.** The packaged handler only creates a span
when the callback payload is shaped like `model.CallbackInput`/`Output`, `tool.CallbackInput`/
`Output`, or `embedding.CallbackInput`/`Output` — a narrower net than "one span per `RunInfo`",
which is what this mode's *previous* hand-rolled bridge did. Empirically (see
[smoke test](#observability---real-in-memory-otel-spans-from-a-live-turn) below) this still
captures the ReAct agent's own root span (Eino's `react.Agent` apparently satisfies the
tool-shaped callback contract) plus both `ChatModel` calls and the `calculate` tool call — **4
spans**, not 5: the one span the packaged handler does NOT produce, that the old hand-rolled
bridge did, is the `Tools`-node graph wrapper (its callback payload isn't Model/Tool/Embedding
-shaped, so the packaged handler's `OnStart` type-switch falls through and skips it). That is the
real, measured trade-off of preferring the packaged integration: **less exhaustive
per-graph-node coverage, in exchange for zero bespoke span-construction code** and an
externally-maintained mapping from Eino callbacks to OTel semantic-convention attributes.

**What was actually observed** (one real run, `agnes-2.0-flash`, see
[smoke test](#observability---real-in-memory-otel-spans-from-a-live-turn) below): **10 raw
`callbacks.Handler` firings** (`OnStart`+`OnEnd` × 5 component invocations — the same 5 Eino
fires internally regardless of which OTel bridge is listening) converted by the packaged handler
into **4 real OTel spans** — `eino.ReActAgent` (the whole turn, ~6.95s), `eino.ChatModel` ×2
(tool-decision call, ~6.95s; final-answer call, ~6.76s — real agnes network latency, not
synthetic), `eino.calculate` (the actual evaluator execution, ~0.08ms). Chat-model spans
additionally carry OTel `gen_ai.*` semantic-convention attributes (`gen_ai.request.model`,
`gen_ai.finish_reason`) plus a `braintrust.metrics` JSON attribute containing Eino's native
`TokenUsage` — the packaged handler's own attribute shape, not this adapter's.

**Logs.** `eino` core has no logging component and emits nothing structured on its own — there is
no `eino.Logger` interface, no log line per component call, nothing to capture. `logs_structured`
is honestly `false`. `log_sample` is a **DIY** example: this mode builds one `log/slog` JSON line
on top of the *same* raw callback data (`raw_callback_events`, `span_count`, `total_ms`) to show
what a hand-rolled structured log would look like — clearly not something Eino or the packaged
OTel handler emits by itself.

### `middleware` mode

Proves whether Eino lets an integrator intercept a chat call — inbound (mutate what the model
sees) and outbound (observe what came back) — and, unlike `observability` above, DOES answer the
user's question: it runs one real qa-shaped turn AND reports what interception actually fired.
Like `observability`, it writes its own JSON contract:

```jsonc
{
  "text": "OK",
  "middleware": {
    "mechanism": "eino callbacks (outbound, native OnStart/OnEnd — observe-only by Eino's own contract) + model-wrapper (inbound, since callbacks cannot mutate shared Input)",
    "inbound_fired": true,
    "outbound_fired": true,
    "invocations": 1,
    "supported": true
  }
}
```

Implementation: `mode_middleware.go`.

**Eino has no single "middleware" primitive.** It has two distinct native seams, and this mode
uses BOTH — for a documented, evidence-based reason, not a guess:

1. **`github.com/cloudwego/eino/callbacks`** — the same `OnStart`/`OnEnd`/`OnError` seam
   `tools`/`observability` modes already drive (`mode_tools.go`'s `usageToolTracker`,
   `observability.go`'s `obsRecorder`). Its own doc comment
   (`callbacks/interface.go`) states plainly: *"do NOT mutate the Input or Output values. All
   downstream nodes and handlers share the same pointer ... Mutations cause data races in
   concurrent graph execution."* Verified by reading that source directly, not inferred: Eino's
   callbacks are **observe-only**. They cannot be used to inject the inbound sentinel.
2. **A `model.BaseChatModel` decorator** (`mwModel` in `mode_middleware.go`) — any type
   implementing `Generate`/`Stream` can wrap a real one, exactly the shape
   `ToolCallingChatModel.WithTools` itself already uses ("returns a new instance, does not mutate
   the receiver," per `components/model/interface.go`). This CAN mutate what the model sees,
   because it owns the message slice it builds before delegating to the wrapped model — there is
   no shared-pointer hazard, since the decorator constructs its own copy.

So: **INBOUND** goes through the model-wrapper (`mwModel.injectSentinel` appends
`"\n[ARENA_MW]"` to the system message's content — or, if the request carries no system message,
inserts a new leading one containing just the sentinel — into a **freshly cloned** message slice,
never the caller's own); **OUTBOUND** goes through Eino's native `callbacks.Handler`, counting
`ChatModel`-component `OnStart`/`OnEnd` firings. Because this mode calls `Generate` directly
(no `compose.Graph` in between), the handler is attached via `callbacks.InitCallbacks` — the
mechanism `callbacks.EnsureRunInfo`'s own doc comment documents for exactly this situation:
*"Use this when running a component standalone outside a Graph — the Graph normally manages
RunInfo injection automatically, but standalone callers must set it up themselves."*

The `middleware.mechanism` field states both halves honestly rather than picking one label
(`"eino callbacks"` alone would misrepresent how inbound interception actually works;
`"model-wrapper"` alone would miss that outbound observation is genuinely native Eino callbacks,
not a bespoke counter).

`invocations` counts real `ChatModel`-component `OnStart` firings observed on the underlying
model this run (1 for a single qa turn — this mode makes exactly one `Generate` call, same as `qa`
mode). `inbound_fired`/`outbound_fired` are computed, not assumed: `inbound_fired` is set the
instant `injectSentinel` actually appends the sentinel to (or inserts) a system message;
`outbound_fired` is set only if the outbound handler's `OnEnd` actually observed a
`*model.CallbackOutput` with a non-nil `Message` for the `ChatModel` component.

## `stub` toggle (`ARENA_STUB=1`)

Setting `ARENA_STUB=1` swaps the chat model **every mode** drives — real agnes via
`eino-ext/components/model/openai` — for a local, network-free fake
(`stubChatModel`, `stub.go`) implementing Eino's own `model.ToolCallingChatModel` interface
(`Generate` + `Stream` + `WithTools`). This is a **single choke point**: `newChatModel()`
(`clients.go`) is the one function every mode calls to get a chat model, and it now branches on
`env.stub` — no mode-specific code changes were needed for `qa`, `rag`, `tools`, `memory`,
`structured`, `stream`, `memory_scoped`, `isolation_leak`, `crash_recovery`, `observability`, or
`middleware`; all ten mode files (plus `mode_middleware.go`) are unchanged by this feature besides
the return type of `newChatModel()` moving from the concrete `*einochatopenai.ChatModel` to the
`model.ToolCallingChatModel` **interface** it already satisfied.

`run.sh`/`run-docker.sh` skip the `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` requirement
entirely when `ARENA_STUB=1` — no `.env`, no real credentials, no network egress needed at all.
`loadEnv()` (`clients.go`) defaults `model` to `"arena-stub-echo"` when unset under stub, so the
wire contract's `model` field is never silently blank.

**What the fake does.** `stubChatModel.Generate`/`.Stream` deterministically **echo** the last
user message, prefixed `"[ARENA_STUB echo] "` so a stub reply is never confusable with a real
agnes answer, with a synthesized `ResponseMeta.Usage` (a word-count approximation — loudly fake,
never a fabricated *real* provider number, and never silently `-1` either). `Generate` also
manually replays the same `callbacks.EnsureRunInfo`/`OnStart`/`OnEnd` sequence the real vendored
client calls internally (`eino-ext/libs/acl/openai`'s `Client.Generate`), so a standalone
(non-graph) caller attached via `callbacks.InitCallbacks` — `middleware` mode above — observes
identical `outbound_fired`/`invocations` behavior whether the real model or this fake is behind
`newChatModel()`. Graph-driven modes (`tools`, `observability`) do not need that: Eino's
`compose.Graph` wraps every node's invocation with callbacks **generically at the graph layer**
(`compose/utils.go`'s `invokeWithCallbacks`/`initGraphCallbacks` — verified by reading that source
directly), independent of the node's own implementation, so those already fire correctly for
**any** `model.BaseChatModel` dropped into a node, stub included.

**In-memory stores.** `memory`, `memory_scoped`/`isolation_leak`, and `crash_recovery` persist to
`EINO_ARENA_MEMORY_DB` (default `.data/memory.db`, an on-disk SQLite file). Under `ARENA_STUB=1`
with no explicit override, `loadEnv()` defaults that path to `:memory:` instead — SQLite's
in-process, in-memory database — so no on-disk file is created at all. `openMemoryStore`/
`openScopedMemoryStore` pin the connection pool to exactly one connection (`SetMaxOpenConns(1)`)
for `:memory:` specifically: `database/sql` pools multiple physical connections by default, and
each connection to `:memory:` is its own SEPARATE, non-shared SQLite instance — without pinning,
a second pooled connection would see an empty database mid-run.

**Tool-calling scope note.** `stubChatModel` does not reason about when to call a tool; when tools
are bound and no tool result is present yet, it emits a CANNED `ToolCall` for `calculate` (or the
first bound tool) with a fixed, genuinely-evaluable expression — the same approach the Mastra
adapter's stub takes for the same tool shape. The tool itself always actually executes (real
evaluator, real result); only the DECISION to call it is canned. This lets `tools` mode's own
correctness assertion (`len(out.ToolCalls) == 0` → error, `mode_tools.go`) and `observability`
mode's scenario (which depends on the same ReAct tool-call loop) both genuinely pass under
`ARENA_STUB=1` with zero network calls. Verified below.

**Honest scope note — `rag` mode's embedder/Qdrant are NOT stubbed.** `ARENA_STUB` only replaces
the chat-model call and the conversational stores. `rag` mode's real e5 embedding call
(`EMBED_URL`) and real Qdrant search (`QDRANT_URL`) are untouched by this toggle and still require
those services reachable — deliberately out of scope for this change (see the acceptance criteria
this feature shipped against), not an oversight.

## Real infrastructure this adapter reaches

| Service | Reached via | Env var (default) |
|---|---|---|
| agnes-2.0-flash chat | `eino-ext/components/model/openai` | `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` (required, no defaults — see `run.sh`) |
| e5 embeddings server | `eino-ext/components/embedding/openai` | `EMBED_URL` (`http://127.0.0.1:8899`), `EMBED_MODEL` (`multilingual-e5-small-query`) |
| Qdrant (`arena_rag`) | `github.com/qdrant/go-client` (gRPC) | `QDRANT_URL` (`http://localhost:6333`, REST-style — hostname is parsed out), `QDRANT_GRPC_PORT` (derived: REST port + 1 = `6334` unless overridden), `QDRANT_COLLECTION` (`arena_rag`) |
| on-disk SQLite | `modernc.org/sqlite` | `EINO_ARENA_MEMORY_DB` (`.data/memory.db`) — shared by `memory`, `memory_scoped`/`isolation_leak` (separate table), and `crash_recovery` (same table as `memory`) |

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

### `observability` — real in-memory OTel spans from a live turn, via a packaged OSS integration

```bash
$ echo '{"mode":"observability"}' | bash run.sh
{"framework":"eino","mode":"observability","otel_supported":true,"span_count":4,"span_names":["eino.ChatModel","eino.calculate","eino.ReActAgent","eino.ChatModel"],"span_timings_ms":{"eino.ChatModel":6949.314375,"eino.ChatModel#2":6763.638541,"eino.ReActAgent":6949.506416,"eino.calculate":0.082709},"logs_structured":false,"log_sample":"{\"time\":\"2026-07-18T00:09:47Z\",\"level\":\"INFO\",\"msg\":\"eino_observability_turn\",\"framework\":\"eino\",\"raw_callback_events\":10,\"span_count\":4,\"total_ms\":13713}","notes":"Eino ships NO built-in OTel exporter usable offline. Three community OTel options were evaluated ... This mode uses (b) github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino: Eino's NATIVE callbacks.Handler seam ... drives that packaged handler, wired to a real go.opentelemetry.io/otel/sdk/trace TracerProvider backed by tracetest.InMemoryExporter ... zero network I/O, zero external collector, and none of this mode's own span-construction code. Observed 10 raw callback firings ... converted ... into 4 real OTel spans: [eino.ChatModel eino.calculate eino.ReActAgent eino.ChatModel] — it only instruments ChatModel/Tool/Embedding callback payloads, so (honest scope note) it does NOT emit a span for the ... Tools-node graph wrapper ..."}
```
(full `notes` text elided here for length — see [`observability` mode](#observability-mode) above for the complete write-up and the community-options evaluation table.) 4 real spans, real durations (the two `ChatModel` calls are ~6.9s/6.8s of genuine agnes network latency, not synthetic; `eino.calculate` is ~0.08ms, real in-process evaluator execution), exit code 0, exactly one JSON object on stdout — verified with `wc -l` on a captured stdout-only run. A parallel check with `source ../../../.env` confirmed `$OPENAI_API_KEY` appears in neither stdout nor stderr of this run.

### `middleware` — real inbound+outbound interception, one qa turn

```bash
$ echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' | bash run.sh
{"text":"OK","middleware":{"mechanism":"eino callbacks (outbound, native OnStart/OnEnd — observe-only by Eino's own contract) + model-wrapper (inbound, since callbacks cannot mutate shared Input)","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true}}
```
`inbound_fired: true` — `mwModel.injectSentinel` appended `"\n[ARENA_MW]"` to the system message
before the real agnes call. `outbound_fired: true` — the native `callbacks.Handler` attached via
`callbacks.InitCallbacks` actually observed the `ChatModel` component's `OnEnd`. `invocations: 1`
— exactly one real `Generate` call, matching the single qa turn. Exit code 0, exactly one JSON
object on stdout, verified with `wc -l` on a captured stdout-only run (also confirmed under
`ARENA_STUB=1` — see below).

### `stub` toggle — fully offline, zero `.env`/`OPENAI_*` needed

```bash
$ echo '{"mode":"qa","system":"You are concise.","messages":[{"role":"user","content":"What is the capital of France?"}]}' \
    | ARENA_STUB=1 bash run.sh
{"framework":"eino","mode":"qa","model":"arena-stub-echo","reply":"[ARENA_STUB echo] What is the capital of France?","input_tokens":9,"output_tokens":8,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":null,"resumed":null,"precrash_fact_recalled":null,"state_lost":null}
```
Run with the process environment **fully cleared** (`env -i`, no `.env` sourced, no
`OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` set at all) — `model` defaults to
`"arena-stub-echo"`, the reply is the echo (never a real agnes answer), `latency_ms: 0` (no
network round-trip happened), exit code 0.

```bash
$ echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' \
    | ARENA_STUB=1 bash run.sh
{"text":"[ARENA_STUB echo] Say OK.","middleware":{"mechanism":"eino callbacks (outbound, native OnStart/OnEnd — observe-only by Eino's own contract) + model-wrapper (inbound, since callbacks cannot mutate shared Input)","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true}}
```
`inbound_fired`/`outbound_fired` both still `true` under `ARENA_STUB=1` — the fake replays the
same `callbacks.EnsureRunInfo`/`OnStart`/`OnEnd` sequence the real client calls internally (see
[`stub` toggle](#stub-toggle-arena_stub1) above), so `middleware` mode's own offline behavior is
identical whether the real model or the fake is behind it.

```bash
$ echo '{"mode":"tools","messages":[{"role":"user","content":"What is (48 + 12) * 3 / 2? Use the calculator tool."}]}' \
    | ARENA_STUB=1 bash run.sh; echo "exit=$?"
{"framework":"eino","mode":"tools","model":"agnes-2.0-flash","reply":"[ARENA_STUB echo] tool result: {\"result\":4}","input_tokens":8,"output_tokens":8,"latency_ms":0,"tool_calls":[{"name":"calculate","arguments":{"expression":"2 + 2"},"result":"{\"result\":4}"}],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":null,"resumed":null,"precrash_fact_recalled":null,"state_lost":null}
exit=0
```
Zero network calls happened (`latency_ms: 0`, no agnes call), yet `tool_calls` is genuinely
non-empty: the stub's CANNED tool-call decision (`"expression":"2 + 2"`, not the model's own
arithmetic — see the "Tool-calling scope note" above) drove Eino's REAL ReAct loop into actually
executing `calculate` (`"result":4`, computed by the real evaluator) and then answering from that
real result — `tools` mode's own assertion (`len(out.ToolCalls) == 0` → error) genuinely passes.

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

### `crash_recovery` — real `kill -9` mid-turn-2, then a fresh process resumes

```bash
$ bash arenas/live/eino/test-crash.sh
=== crash_recovery scenario: session_id=crash-test-1784330070-96170 db=/tmp/.../crash-memory.db ===
--- building eino-crash-arena binary ---
PASS: binary builds (exit=0)
--- process A: starting (will be SIGKILLed mid-turn-2) ---
PASS: process A reached turn 2 (in-flight) before being killed
PASS: process A did not exit cleanly (was actually killed)
PASS: turn 2 never reached disk (kill landed before its commit)
--- process A stderr (for evidence) ---
eino-crash-arena: turn 1 starting for session "crash-test-1784330070-96170"
eino-crash-arena: turn 1 persisted for session "crash-test-1784330070-96170"
eino-crash-arena: turn 2 starting for session "crash-test-1784330070-96170"
--- process B: fresh process, resuming session ---
PASS: process B (resumer) ran successfully (exit=0)
--- process B stdout ---
{"framework":"eino","mode":"crash_recovery","model":"agnes-2.0-flash","reply":"BLUE-FALCON-77","input_tokens":324,"output_tokens":8,"latency_ms":10542,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":null,"resumed":true,"precrash_fact_recalled":true,"state_lost":false}
PASS: resumed=true (got=true)
PASS: precrash_fact_recalled=true (got=true)
PASS: state_lost=false (got=false)

=== test-crash.sh summary: 8 passed, 0 failed ===
```
Process A's stderr shows `"turn 2 starting"` with no matching `"turn 2 persisted"` line — turn 2
was genuinely in-flight (its real agnes HTTP call already dispatched) when `SIGKILL` landed, and
it never reached disk. Process B — a brand-new PID, brand-new Go runtime, started only after
process A was confirmed dead by `wait` — loaded turn 1 from the same on-disk file and answered
`BLUE-FALCON-77` verbatim, the exact fact stated in turn 1 and never restated in process B's own
input. `resumed=true` / `state_lost=false` / `precrash_fact_recalled=true` together are the
scenario's PASS condition; see [HONEST durability note](#crash_recovery-mode) above for exactly
what this test does and does not prove.

### Isolation — multi-tenant scoped memory (real agnes calls)

Write a fact under `tenant-a` (pure store op, no agnes call):
```bash
$ echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"prod-p","fact":"The secret launch code for project Nimbus is ALPHA-7291."}' | bash run.sh
{"framework":"eino","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"stored fact for tenant=tenant-a user=user-x product=prod-p","input_tokens":-1,"output_tokens":-1,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":null}
```

Recall under the **same** scope (real agnes call) — `same_scope_hit: true`:
```bash
$ echo '{"mode":"memory_scoped","tenant_id":"tenant-a","user_id":"user-x","product_id":"prod-p","query":"What is the secret launch code for project Nimbus?"}' | bash run.sh
{"framework":"eino","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"The secret launch code for project Nimbus is ALPHA-7291.","input_tokens":343,"output_tokens":16,"latency_ms":6890,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":{"same_scope_hit":true,"cross_tenant_miss":false,"recalled_text":"The secret launch code for project Nimbus is ALPHA-7291."},"leak_detected":null}
```

Recall the **same question** as `tenant-b` (same `user_id`/`product_id`, real agnes call) — `cross_tenant_miss: true`, no fact leaked into the reply:
```bash
$ echo '{"mode":"memory_scoped","tenant_id":"tenant-b","user_id":"user-x","product_id":"prod-p","query":"What is the secret launch code for project Nimbus?"}' | bash run.sh
{"framework":"eino","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"I do not have access to that information. The stored facts for this scope contain no data regarding Project Nimbus or any associated launch codes.","input_tokens":331,"output_tokens":28,"latency_ms":9222,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":{"same_scope_hit":false,"cross_tenant_miss":true,"recalled_text":""},"leak_detected":null}
```

`isolation_leak`: seed a secret under `tenant-a` (pure store op), then probe **as `tenant-b`** (real agnes call) — `leak_detected: false`:
```bash
$ echo '{"mode":"isolation_leak","tenant_id":"tenant-a","user_id":"user-x","product_id":"prod-p","fact":"TOP SECRET: tenant-a wire transfer PIN is 8842."}' | bash run.sh
{"framework":"eino","mode":"isolation_leak","model":"agnes-2.0-flash","reply":"seeded secret under tenant=tenant-a scope","input_tokens":-1,"output_tokens":-1,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":null}

$ echo '{"mode":"isolation_leak","tenant_id":"tenant-b","user_id":"user-x","product_id":"prod-p","query":"What is the wire transfer PIN?"}' | bash run.sh
{"framework":"eino","mode":"isolation_leak","model":"agnes-2.0-flash","reply":"I am not certain. The stored facts for this scope are \"(none)\".","input_tokens":328,"output_tokens":16,"latency_ms":6855,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":"","scoped_recall":null,"leak_detected":false}
```

Confirmed on disk: `sqlite3 .data/memory.db "SELECT tenant_id, user_id, product_id, fact FROM scoped_facts"` shows both facts stored only under `tenant-a` — no row with `tenant_id='tenant-b'` was ever written, which is exactly why the cross-tenant/probe queries above came back empty rather than needing the model to withhold anything.

### `test-isolation.sh`

```
=== memory_scoped: compound-scope isolation ===
PASS: memory_scoped write succeeds (tenant A) (error="")
PASS: same-scope hit (scoped_recall.same_scope_hit) (got=true)
PASS: same-scope reply contains the recalled fact
PASS: cross-tenant miss (scoped_recall.cross_tenant_miss) (got=true)
PASS: cross-tenant reply does NOT contain the secret

=== isolation_leak: seed under tenant A, probe as tenant B ===
PASS: isolation_leak seed succeeds (tenant A) (error="")
PASS: zero leak (leak_detected=false) probing as tenant B (got=false)
PASS: tenant B probe reply does NOT contain the secret

=== direct store check: scoped_facts on disk holds only tenant A rows for this run ===
PASS: on-disk store has zero rows for tenant B from this run (rows=0)

=== test-isolation.sh summary: 9 passed, 0 failed ===
```
Each run uses a fresh `test-isolation-tenant-{a,b}-<pid>-<epoch>` namespace so repeated runs never
collide with each other or with the manual smoke-test data above. Real agnes calls per run: 3
(same-scope recall, cross-tenant recall, isolation_leak probe) — the write/seed calls are pure
store operations.

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
| github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino **(NEW, observability mode)** — the packaged, community OSS `callbacks.Handler`→OTel bridge this mode uses; see [`observability` mode](#observability-mode) | v0.10.0 | Apache-2.0 |
| github.com/braintrustdata/braintrust-sdk-go **(NEW, observability mode, indirect — pulled in by the module above; not directly imported, no API key used)** | v0.10.0 | Apache-2.0 |
| go.opentelemetry.io/otel **(NEW, observability mode)** | v1.44.0 | Apache-2.0 |
| go.opentelemetry.io/otel/sdk **(NEW, observability mode)** | v1.44.0 | Apache-2.0 |
| go.opentelemetry.io/otel/trace **(NEW, observability mode, indirect)** | v1.44.0 | Apache-2.0 |
| go.opentelemetry.io/otel/metric **(NEW, observability mode, indirect)** | v1.44.0 | Apache-2.0 |
| go.opentelemetry.io/auto/sdk **(NEW, observability mode, indirect)** | v1.2.1 | Apache-2.0 |
| github.com/go-logr/logr **(NEW, observability mode, indirect)** | v1.4.3 | Apache-2.0 |
| github.com/go-logr/stdr **(NEW, observability mode, indirect)** | v1.2.2 | Apache-2.0 |
| github.com/cespare/xxhash/v2 **(NEW, observability mode, indirect)** | v2.3.0 | MIT |
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
- `mode_crash.go` — `crash_recovery` mode (producer/resumer over the same on-disk SQLite store
  `mode_memory.go` defines; see [`crash_recovery` mode](#crash_recovery-mode)).
- `mode_structured.go` — `structured` mode (Eino native JSON-Schema response format).
- `mode_stream.go` — `stream` mode (Eino native `Stream` + time-to-first-chunk).
- `mode_scoped_memory.go` — `memory_scoped` and `isolation_leak` modes (hand-rolled compound
  tenant/user/product scope key over the on-disk SQLite store; see
  [Multi-tenant scoped memory](#multi-tenant-scoped-memory-memory_scoped-isolation_leak-modes)).
- `observability.go` — the `observability` mode's OTel/callbacks plumbing: `obsRecorder` builds a
  real `go.opentelemetry.io/otel/sdk/trace` `TracerProvider` exporting to an in-memory
  `tracetest.InMemoryExporter` (no external collector) and hands it to the packaged
  `github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino` handler, which does
  the actual Eino-`callbacks.Handler`-to-OTel-span conversion; a small local `counterHandler()`
  is registered alongside it purely for the raw-callback-firing count; see
  [`observability` mode](#observability-mode).
- `mode_observability.go` — `observability` mode's scenario (a real multi-step ReAct turn) and its
  own JSON wire contract (`observabilityOutput`, distinct from `adapterOutput`); owns its full
  stdout write and process exit code.
- `mode_middleware.go` — `middleware` mode: `mwModel` (inbound interception, a
  `model.BaseChatModel` decorator) + a native `callbacks.Handler` attached via
  `callbacks.InitCallbacks` (outbound observation), and its own JSON wire contract
  (`middlewareOutput`, distinct from `adapterOutput`); owns its full stdout write and process exit
  code. See [`middleware` mode](#middleware-mode).
- `stub.go` — `ARENA_STUB=1`'s fake chat model (`stubChatModel`), implementing Eino's native
  `model.ToolCallingChatModel` interface with a network-free echo; the single-choke-point swap
  target for `clients.go`'s `newChatModel()`. See [`stub` toggle](#stub-toggle-arena_stub1).
- `run.sh` — sources `.env`, validates required env vars (skipped when `ARENA_STUB=1`), runs the
  adapter natively.
- `Dockerfile` — multi-stage build, no baked credentials, distroless nonroot runtime.
- `run-docker.sh` — builds (if needed) and runs the containerized adapter; passes through
  `ARENA_STUB`.
- `test-env.sh` — automated credential/env PASS/FAIL assertions (native + Docker).
- `test-isolation.sh` — automated multi-tenant isolation PASS/FAIL assertions (same-scope hit,
  cross-tenant miss, zero leak) using real agnes recall/probe calls.
- `test-crash.sh` — automated `crash_recovery` PASS/FAIL assertions: builds a standalone binary,
  runs process A, `SIGKILL`s it mid-turn-2 (synchronized via a stderr marker, not a timing
  guess), then runs a fresh process B and asserts `resumed`/`precrash_fact_recalled`/`state_lost`.
  Uses an isolated temp `EINO_ARENA_MEMORY_DB` per run — never touches `.data/memory.db`.
- `go.mod` / `go.sum` — module definition.
- `.go-version` — pins the local toolchain (via `goenv`) to an installed Go version compatible
  with the `go 1.25.0` directive (bumped from `1.23` by `go mod tidy` once
  `modernc.org/sqlite`'s dependency graph required it).
- `.gitignore` — excludes the runtime `.data/` SQLite store and stray local `go build` output.
