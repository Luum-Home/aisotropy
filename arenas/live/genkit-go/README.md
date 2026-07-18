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
| `memory` | Multi-turn state persisted to real rqlite; a **separate process invocation** recalls a fact from an earlier one. Keys **only** by `session_id` — no tenant/user/product dimension, so it says nothing about multi-tenant isolation. |
| `memory_scoped` | Hand-rolled **compound-scope** memory over the same rqlite store: writes/recalls a fact keyed by `(tenant_id, user_id, product_id[, session_id])`. Proves a same-scope recall hits and a different-tenant recall misses. |
| `isolation_leak` | Adversarial proof for the same compound-scope store: tenant A writes a secret, the store is queried **as tenant B**, and a real agnes turn is grounded only in what that query returned. |
| `crash_recovery` | HARD-crash durability, distinct from `memory`'s clean-exit persistence: designed to be driven by `test-crash.sh`, which SIGKILLs a live invocation mid-flight (after a prior turn checkpointed, before the killed turn ever could). A **fresh** process invocation then proves it resumes from exactly what was checkpointed — no more, no less. |
| `structured` | Native structured output (`ai.WithOutputType`) — a Go struct, schema-validated by Genkit, not ad-hoc JSON parsing. |
| `stream` | Native streaming generation (`ai.WithStreaming`); records `ttft_ms` (time to first chunk). |
| `observability` | Genkit's OWN native OpenTelemetry tracing + structured (`log/slog`) logging, captured in-process across one representative multi-op turn. Uses a **different stdout contract** — see "`observability` — Genkit's native OTel tracing + structured logs" below. |
| `middleware` | Genkit's NATIVE `ai.ModelMiddleware` (`func(ai.ModelFunc) ai.ModelFunc`), attached via `ai.WithMiddleware`: one real, observable inbound (pre-model) transform and outbound (post-model) observation around one qa-shaped turn. Uses a **different stdout contract** — see "`middleware` — native `ai.ModelMiddleware`" below. |

## Contract

`mode=observability` and `mode=middleware` are the two exceptions to everything in
this section — neither uses the shared envelope described here at all. See their own
per-mode design sections below for their stdout contracts.

**stdin** — one JSON object:

```json
{
  "mode": "qa | rag | tools | memory | memory_scoped | isolation_leak | crash_recovery | structured | stream | observability | middleware",
  "system": "string",
  "messages": [{"role": "user|assistant", "content": "string"}],
  "tools": [{"name": "string", "description": "string", "parameters": { /* JSON Schema */ }}],
  "session_id": "string — required for mode=memory and mode=crash_recovery, optional scope component for memory_scoped/isolation_leak",
  "top_k": 4,
  "tenant_id": "string — required for memory_scoped/isolation_leak",
  "user_id": "string — required for memory_scoped/isolation_leak",
  "product_id": "string — required for memory_scoped/isolation_leak",
  "fact": "string — the fact/secret to write; required for memory_scoped/isolation_leak. For crash_recovery, the keyword this turn's reply is expected to recall from an earlier checkpointed turn (optional — omit for a plain checkpoint-writing call)",
  "query": "string — the recall question; optional, has a default per mode",
  "cross_tenant_id": "string — the 'different tenant' to probe; optional, synthesized as '<tenant_id>-cross-tenant-probe' if absent"
}
```

- `mode` defaults to `"qa"` if absent.
- `tools` is used by `qa` (single-shot capture) and `tools` (real execution).
- `session_id` keys a conversation's persisted row set in rqlite; `memory` and `crash_recovery` modes require it.
- `top_k` (optional, default 4) bounds Qdrant retrieval count for `rag`.
- `tenant_id` / `user_id` / `product_id` / `fact` are required by `memory_scoped` and `isolation_leak`;
  `session_id` is an optional additional scope component (defaults to `""`); `query` and
  `cross_tenant_id` are optional with the defaults noted above.
- `fact` is reused by `crash_recovery` (optional there) as the keyword to check for in that turn's
  reply — see "crash_recovery" below.

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
  "scoped_recall": null,
  "leak_detected": null,
  "resumed": null,
  "precrash_fact_recalled": null,
  "state_lost": null,
  "prior_turns_loaded": null,
  "checkpointed": null,
  "error": ""
}
```

- `input_tokens` / `output_tokens`: provider-reported usage, `-1` if unavailable.
- `retrieved`: populated by `rag` (top-K passages actually used to ground the answer).
- `structured`: populated by `structured` (the schema-validated object).
- `ttft_ms`: populated by `stream` (wall-clock to first streamed chunk); `0` otherwise.
- `scoped_recall`: populated by `memory_scoped` only — `{"same_scope_hit": bool, "cross_tenant_miss": bool,
  "recalled_text": "string"}`; `null` for every other mode (same null-when-unused convention as `structured`).
- `leak_detected`: populated by `isolation_leak` only — `false` means no leak observed (good); `null` for
  every other mode.
- `resumed` / `prior_turns_loaded` / `checkpointed`: populated by `crash_recovery` only — whether this
  invocation found and loaded any prior checkpointed turn, how many rows it loaded, and whether this
  invocation's own turn was durably checkpointed before returning; `null` for every other mode.
- `precrash_fact_recalled` / `state_lost`: populated by `crash_recovery` only, and only when the caller
  supplied `fact` — whether that keyword is present in this turn's reply, and its inverse; `null` for
  every other mode, and `null` for `crash_recovery` calls that omitted `fact` (e.g. a plain checkpoint
  step). See "`crash_recovery`" below.
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

### `crash_recovery` — HARD-crash durability, not just clean-exit persistence

**`memory` only ever proves recall across a *clean* process exit** — every `run.sh`
invocation always runs to completion (success or a returned Go `error`) before the next one
starts. That leaves a real question unanswered: does state survive if the process is
violently killed *mid-turn*, not given the chance to exit at all? `crash_recovery.go` +
`rqlite_client.go`'s new `arena_genkit_go_crash_recovery` table (a table kept **separate**
from `memory`'s, so the two modes' histories can never be cross-read) exist to answer that,
and `test-crash.sh` is the executable proof, not a doc claim:

1. **Turn 1** (process A, blocking): states a fact, runs `genkit.Generate` to completion,
   then `appendCrashTurn`s both the inbound message and the reply — this is the only point at
   which anything becomes durable. `checkpointed=true` confirms it.
2. **Turn 2** (process A, backgrounded, then SIGKILLed): `test-crash.sh` launches a second
   invocation on the same `session_id`, polls its stderr for the `PROGRESS: crash_recovery:
   entering genkit.Generate` marker (written **after** the read-only load of turn 1's
   checkpoint, **before** any write for turn 2 could happen), and sends `kill -9` the instant
   that marker appears. The killed process's stdout is empty — it never reached the final
   `json.Encoder` call, let alone the checkpoint — and `wait` reports a non-zero (signal-killed)
   status, both asserted by the script.
3. **Turn 3** (process B, a **completely fresh OS process**): asks for the turn-1 fact back.
   Its own input never mentions it. `runCrashRecovery` loads whatever rqlite actually has —
   which is turn 1's two checkpointed rows and *nothing* from turn 2, because turn 2 never
   reached its own `appendCrashTurn` calls — and answers grounded in that. `resumed=true` and
   `prior_turns_loaded=2` prove it picked up state from the dead process; `precrash_fact_recalled`
   (computed by checking whether the reply text contains the caller-supplied `fact` keyword,
   reusing the same `arenaInput.Fact` field `memory_scoped`/`isolation_leak` use) and its inverse
   `state_lost` are the pass/fail signal. `test-crash.sh` additionally greps the raw `reply` JSON
   field for the fact code itself, independent of the adapter's own self-reported booleans — the
   same "don't trust a claim without executable evidence" discipline
   `.claude/rules/prune-evidence-gate.md` applies to "dead code," applied here to "recovered."

**HONEST note — what this proves, and what it explicitly does NOT:**
- What it proves: the adapter's checkpoint boundary is real and atomic at the process level. A
  turn that never finishes its two `appendCrashTurn` calls contributes **zero** rows to rqlite —
  never a partial or corrupted one — and a **separate process with no shared memory** correctly
  resumes from exactly what was actually committed before it. This is genuine crash durability
  for what SQLite/rqlite itself already guarantees per-statement (each `INSERT` is atomic), not
  a durability property this adapter invents on top of it.
- **This is durable across a hard `SIGKILL`, not merely a clean shutdown** — the two are
  different claims, and this mode is built specifically to distinguish them (see
  "`memory`" above for the clean-exit-only case). `test-crash.sh`'s kill target is `kill -9`
  (unblockable, no signal handler can run), not `kill -TERM`.
- What it does NOT prove: multi-statement atomicity across the **two** `appendCrashTurn` calls
  per turn (inbound message row, then reply row) — a kill landing in the narrow window
  *between* those two calls (not inside `genkit.Generate`, which is what `test-crash.sh`
  targets and is ~1-2 orders of magnitude wider) would leave an inbound-only row with no
  matching reply, which `loadCrashTurns` would still load and feed to the model as a dangling
  user turn. This adapter does not wrap those two inserts in a single rqlite transaction — doing
  so was out of scope for this pass; the two-call gap is real and unverified here.
- It does not prove durability of rqlite's own on-disk storage across a **node**-level crash
  (rqlite process killed, disk corruption, etc.) — only that THIS adapter's process can be
  killed and a fresh one recovers correctly from whatever rqlite already had committed. rqlite's
  own Raft-log durability is out of scope; this mode treats rqlite as the trusted durable store,
  the same assumption `memory` mode already makes.
- It does not prove anything about `memory_scoped`/`isolation_leak`'s compound-scope table —
  `crash_recovery` uses its own table and its own (simpler, session_id-only) key.

### `memory_scoped` / `isolation_leak` — hand-rolled multi-tenant scope, and the honest note

**Genkit (Go) has no native multi-tenant memory-isolation primitive.** There is no scoped
store, no tenant-aware retriever contract, nothing in the framework that stops one tenant's
conversation state from being read back under a different tenant's request. The plain `memory`
mode above is a faithful demonstration of what Genkit-Go actually ships: a `session_id`-keyed
recall, full stop — it proves cross-process persistence, not isolation, because there is no
second dimension in its key at all.

`memory_scoped` and `isolation_leak` (`scoped_memory.go`) exist to test the dimension `memory`
doesn't: real scoped multi-tenant memory and cross-tenant isolation, which is aisotropy's
differentiator (`.claude/rules/constitutional-gates.md` → "Multi-tenant data isolation").
Doing so **required hand-rolling** a compound scope key over the same rqlite store `memory.go`
already uses — `rqlite_client.go`'s new `arena_genkit_go_scoped_memory` table adds
`tenant_id`, `user_id`, and `product_id` columns alongside `session_id`, and every read/write
goes through a parameterized SQL `WHERE tenant_id = ? AND user_id = ? AND product_id = ? AND
session_id = ?` — filtered by rqlite during the query itself, not fetched-then-filtered in Go
(the same "filter during the scan, not after" discipline this repo requires of Qdrant payload
filtering). **The developer wiring this adapter is fully responsible for supplying every scope
column on every read and write; nothing in Genkit-Go or rqlite enforces it.** Dropping
`tenant_id` from that WHERE clause — a one-line, easy-to-miss mistake — is a silent cross-tenant
data leak, not a framework error and not something either dependency would ever flag. Contrast:
aisotropy enforces tenant scoping **by construction**, injected once at the request boundary via
`context.Context` and required on every memory-tier read/write (see
`.claude/rules/architecture.md` → "Multi-tenancy" and `docs/architecture/multi-tenancy.md`) — a
missing scope column there is a compile-time/architecture-review concern, not a runtime data leak
waiting to happen the way it is here.

**`memory_scoped`** (`runMemoryScoped`) proves the hand-rolled scope works, in one process
invocation:
1. **WRITE** `fact` under scope `(tenant_id, user_id, product_id[, session_id])`.
2. **RECALL** under the **same** scope — expects the fact back (`same_scope_hit`).
3. **RECALL** under a **different** tenant (`cross_tenant_id`, same user/product/session) —
   expects nothing back (`cross_tenant_miss`).
4. One real agnes call answers `query`, grounded **only** in whatever the same-scope recall
   returned — proving the recall path feeds a real generation turn, not a SQL read nobody uses.

**`isolation_leak`** (`runIsolationLeak`) is the adversarial version: tenant A writes a secret
(`fact`); the store is then queried **as tenant B** (`cross_tenant_id`, same
user/product/session); a real agnes turn answers `query` grounded only in whatever that
tenant-B-scoped recall returned. `leak_detected` is `true` if **either** the scoped store itself
returned tenant A's secret under tenant B's scope, **or** the grounded reply nonetheless contains
the secret text. The store-level check is the primary, authoritative assertion — per
`.claude/rules/prune-evidence-gate.md`'s code-truth discipline, the isolation claim is verified
against what the store actually returns, not against what the model says or refuses to say; the
reply-level check is a belt-and-suspenders second signal, not a substitute for it. A correct scope
key structurally starves the model of the secret before generation even happens, so a clean store
recall should make a reply-level leak impossible by construction — see `test-isolation.sh`.

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

### `observability` — Genkit's native OTel tracing + structured logs

`observability.go` measures, rather than exercises, Genkit: it reports what Genkit's own
instrumentation emits, not a chat capability.

**Genkit-Go ships native OpenTelemetry tracing.** Every registered `Action.Run` (a model
call, a tool call) is wrapped in an OTel span by `core/action.go`'s
`Action.runWithTelemetry`, and `genkit.Generate` additionally wraps the whole turn in its
own outer `"generate"` span (`ai/generate.go`'s `runGenerate`). Both go through
`core/tracing.go`'s `tracing.RunInNewSpan`, which reads the process-global
`otel.GetTracerProvider()` and only falls back to a private, unexported default
`*sdktrace.TracerProvider` (**no span processor attached — spans are silently dropped**)
if the caller never sets one.

This mode installs its **own** `go.opentelemetry.io/otel/sdk` `TracerProvider`
(`sdktrace.WithSyncer` + a ~30-line hand-rolled in-memory `sdktrace.SpanExporter` —
**no OTel Collector, no Jaeger, no network hop**) via `otel.SetTracerProvider` **before**
`genkit.Init`/the first `genkit.Generate` call — ordering that matters, since Genkit
*adopts* whatever `TracerProvider` is already globally registered rather than
overriding it. Zero changes to the Genkit SDK, no fork.

It then drives **one representative multi-op turn** through the real `tools`-mode loop
(real calculator + real Qdrant/e5-backed `corpus_lookup`, real agnes calls — reusing
`runTools` from `tools.go`, defaulting to the same two tools and a two-part question if
the caller's stdin didn't supply its own) and reports every span Genkit closed for it.

**Finding (this pass, live):** a single two-tool turn produced **6 spans** — an outer
`"generate"` span, TWO nested `openai/agnes-2.0-flash` model-action spans, and one action
span each for `calculator` and `corpus_lookup`. The duplication is real, not a bug in this
adapter: Genkit's tool-execution loop **recurses** on any model turn that requests a tool
call, so iteration 2's `"generate"` + model spans open and close entirely *inside*
iteration 1's still-open `"generate"` span. Because `span_timings_ms` is a flat
`{name: ms}` map (per the required stdout contract) and the same span name can
legitimately repeat, repeats are disambiguated with a `#N` suffix (`N` = occurrence
order = span-end/export order) instead of one timing silently overwriting another.

**Logging:** Genkit uses Go's standard `log/slog` throughout — `core/logger.go` wraps a
`*slog.Logger`, and `core/tracing.go`'s `RunInNewSpan` itself logs `"span start"`/
`"span end"`, `core/action.go`'s `Action.Run` logs on every call — so every entry carries
typed key=value attributes (structured by construction), not `fmt.Printf` text. Genkit's
own default is a plain `slog.TextHandler` to stderr at `Info` level
(`core/logger.SetLevel`), which does **not** surface these entries — they're emitted at
`Debug`. This mode raises the level and swaps in a `slog.JSONHandler` (writing to both
stderr, for live `PROGRESS:` visibility, and an in-memory buffer) to observe and capture
them, exactly as a real integrator wiring log aggregation would.

**stdout contract** (mode-specific — NOT the shared envelope above):

```json
{
  "framework": "genkit-go",
  "mode": "observability",
  "otel_supported": true,
  "span_count": 6,
  "span_names": ["calculator", "corpus_lookup", "generate", "generate", "openai/agnes-2.0-flash", "openai/agnes-2.0-flash"],
  "span_timings_ms": {"calculator": 0.11, "corpus_lookup": 362.49, "generate": 6908.86, "generate#2": 17626.78, "openai/agnes-2.0-flash": 10354.33, "openai/agnes-2.0-flash#2": 6908.47},
  "logs_structured": true,
  "log_sample": "{\"time\":\"...\",\"level\":\"DEBUG\",\"msg\":\"span start\",\"name\":\"generate\"}",
  "notes": "..."
}
```

### `middleware` — native `ai.ModelMiddleware`

`middleware.go` exercises Genkit-Go's native model-middleware mechanism, confirmed by
reading the pinned `v1.10.0` module source directly (per `.claude/rules/prune-evidence-gate.md`'s
code-truth discipline — this is what the code actually is, not a doc claim):

```go
// ai/generate.go
type ModelFunc = core.StreamingFunc[*ModelRequest, *ModelResponse, *ModelResponseChunk]
type ModelMiddleware = core.Middleware[*ModelRequest, *ModelResponse, *ModelResponseChunk]

// core/middleware.go
type Middleware[In, Out, Stream any] = func(StreamingFunc[In, Out, Stream]) StreamingFunc[In, Out, Stream]
```

i.e. `ai.ModelMiddleware` is **exactly** `func(ai.ModelFunc) ai.ModelFunc` — a function that
takes the next step in the chain and returns a new one, the same shape as Go's own
`net/http` middleware convention. It attaches to one `genkit.Generate` call via
`ai.WithMiddleware(mw...)` (`ai/option.go`), which `v1.10.0` still ships and applies —
marked `// Deprecated: Use WithUse instead, which supports Generate, Model, and Tool
hooks`, but not removed. `ai.WithUse(ai.Hooks{WrapModel: ...})` is the newer, non-deprecated
way to reach the same wrapping point; this mode deliberately uses the literal
`func(ModelFunc) ModelFunc` shape via `WithMiddleware` because that shape **is** the
artifact under test (the task explicitly asked for `ai.ModelMiddleware` as
`func(ModelFunc) ModelFunc`, applied via `WithMiddleware`).

`newArenaMiddleware` (`middleware.go`) builds one middleware with two independently
observable transforms, both proven by a shared, mutex-guarded `middlewareState`:

- **INBOUND** — before calling `next(ctx, req, cb)`, it appends the sentinel
  `"\n[ARENA_MW]"` to the text of the request's `ai.RoleSystem` message (or, if the
  request has no system message at all, injects a new leading one carrying only the
  sentinel). This mutates `req` **before** anything downstream — including the model
  itself — ever sees it.
- **OUTBOUND** — after `next` returns, it sets `outboundFired = true`, proving this
  middleware's own code ran **again**, on the response path, after the full chain
  underneath (every other middleware plus the model call) completed — not merely once
  "around" the call.

`runMiddleware` drives one qa-shaped turn (`buildBaseOpts` + `ai.WithMiddleware(mw)`)
and reports `inbound_fired`/`outbound_fired`/`invocations` read back from that shared
state — the executable proof, not a doc claim, that the mechanism is real and wired
correctly in this Genkit-Go version.

**stdout contract** (mode-specific — NOT the shared envelope above):

```json
{"text": "string — the model's final reply", "middleware": {"mechanism": "ai.ModelMiddleware", "inbound_fired": true, "outbound_fired": true, "invocations": 1, "supported": true}, "error": ""}
```

**Verified smoke output (this pass, live, real agnes):**

```jsonc
{"text":"OK","middleware":{"mechanism":"ai.ModelMiddleware","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true}}
```

## ARENA_STUB — fully offline toggle (every mode)

`ARENA_STUB=1` swaps every piece of real infrastructure this adapter talks to for a
deterministic, in-process fake, so **every** mode above (including `middleware`) runs
with zero network calls — no `OPENAI_API_KEY`/`OPENAI_BASE_URL`, no `EMBED_URL`, no
`QDRANT_URL`, no `RQLITE_URL` required or read. Implemented additively in `stub.go` (the
toggle + model/embedding/search fakes) plus small `if stubMode`/`if c.stub` branches at
the top of `embed_client.go`'s `embed`, `qdrant_client.go`'s `searchQdrant`, and every
business method on `rqlite_client.go`'s `rqliteClient` — every non-stub code path is
byte-for-byte unchanged.

| Real dependency | Fake used (`stub.go` unless noted) |
|---|---|
| agnes chat model (`oai.DefineModel`) | A locally **`genkit.DefineModel`'d echo model** (`stubModelFunc`), registered under the same `OPENAI_MODEL` name every mode already expects — see main.go's `run()`. Echoes `"[stub-echo] " + <last user message>` by default; when the request carries `ai.WithOutputType`'s JSON-Schema instructions it instead emits schema-conforming placeholder JSON (`stubJSONForSchema`); when the request offers tools and hasn't already received a tool response this turn, it emits one native `ToolRequest` for the first offered tool with synthesized arguments (`stubArgsForSchema`) — genuinely driving Genkit's real tool-execution loop offline. |
| e5 embeddings server (`embed_client.go`'s `embed`) | `stubEmbedding` — a deterministic 384-dim pseudo-vector seeded from an FNV-1a hash of the input text (same input → same vector, every run). **Not semantically meaningful** — unrelated inputs do not cluster; only used so downstream code has *a* vector to pass around. |
| Qdrant `arena_rag` collection (`qdrant_client.go`'s `searchQdrant`) | `stubQdrantSearch` — a small, canned, Nimbus-Rail-themed in-process "corpus" (3 docs, fixed content/scores). Returns the same docs in the same order every call — it does **not** rank by similarity to the query (`stubEmbedding`'s vectors are pseudo-random, so a real cosine ranking over them would be theater, not a fake search). |
| rqlite (`rqlite_client.go`'s `rqliteClient`) | Package-level in-memory maps (`stubTurns`, `stubCrashTurns`, `stubScoped`), guarded by one mutex, standing in for the `memory`/`crash_recovery`/`memory_scoped`/`isolation_leak` tables. |

**Honest limitation — cross-process persistence is NOT stubbed:** `memory` mode's whole
point is proving a **separate, later `run.sh` invocation** (a fresh OS process) recalls
what an earlier, already-exited process wrote to a real, shared rqlite node.
`ARENA_STUB`'s in-memory maps are package-level Go variables — they live only for the
lifetime of **one** process and reset on every new invocation, so two separate
`ARENA_STUB=1` invocations sharing a `session_id` will **not** recall each other's state
the way two real-rqlite invocations do. This is a structural consequence of "fully
offline" (no real, externally-durable store is reachable), not a bug — `crash_recovery`
inherits the same limitation for the same reason. Every other mode's within-one-process
behavior (including two-tool `tools`/`observability` loops, `structured`'s schema
validation, and `middleware`'s inbound/outbound proof) is unaffected.

**Verified smoke output (this pass, `ARENA_STUB=1`, zero network calls):**

```jsonc
// qa
{"framework":"genkit-go","mode":"qa","model":"agnes-2.0-flash","reply":"[stub-echo] Say OK.","input_tokens":2,"output_tokens":3,"latency_ms":0,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"error":""}

// middleware
{"text":"[stub-echo] Say OK.","middleware":{"mechanism":"ai.ModelMiddleware","inbound_fired":true,"outbound_fired":true,"invocations":1,"supported":true}}

// structured (schema-conforming placeholder JSON, validated by Genkit's own format handler)
{"framework":"genkit-go","mode":"structured","reply":"stub","structured":{"answer":"stub","confidence":0.5,"key_points":["stub"]},"error":""}

// tools (real tool-execution loop, offline: calculator actually evaluates "2 + 2")
{"framework":"genkit-go","mode":"tools","reply":"[stub-echo] What is 23 times 19?","tool_calls":[{"name":"calculator","arguments":{"expression":"2 + 2"}}],"error":""}

// observability (real spans captured from the stub model + real calculator tool)
{"framework":"genkit-go","mode":"observability","otel_supported":true,"span_count":5,"span_names":["agnes-2.0-flash","agnes-2.0-flash","calculator","generate","generate"],"error":""}
```

Usage:

```bash
echo '{"mode":"qa","messages":[{"role":"user","content":"Say OK."}]}' \
  | ARENA_STUB=1 bash arenas/live/genkit-go/run.sh
```

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
| `ARENA_STUB` | `1` runs every mode fully offline against fakes (see "ARENA_STUB" above); any other value/unset uses real infra | unset |

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

# crash_recovery (see test-crash.sh for the actual SIGKILL-driven sequence;
# this is just the plain contract shape — a real crash test needs to
# background + kill process A mid-turn, which a single shell pipeline can't do)
echo '{"mode":"crash_recovery","session_id":"demo-crash-1","messages":[{"role":"user","content":"Remember this secret launch code: DELTA-9."}]}' \
  | bash arenas/live/genkit-go/run.sh
echo '{"mode":"crash_recovery","session_id":"demo-crash-1","fact":"DELTA-9","messages":[{"role":"user","content":"What secret launch code did I give you?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# memory_scoped (same-scope hit + cross-tenant miss, one invocation)
echo '{"mode":"memory_scoped","tenant_id":"tenant-alpha","user_id":"user-x","product_id":"prod-p","session_id":"sess-1","fact":"The customer'"'"'s preferred shipping method is overnight express.","query":"What is the customer'"'"'s preferred shipping method?"}' \
  | bash arenas/live/genkit-go/run.sh

# isolation_leak (tenant A writes a secret; queried as tenant B must return nothing)
echo '{"mode":"isolation_leak","tenant_id":"tenant-secretco","user_id":"user-y","product_id":"prod-q","session_id":"sess-2","fact":"SECRET: the vault override code is 8842-ZULU.","query":"What secret did I tell you?"}' \
  | bash arenas/live/genkit-go/run.sh

# structured
echo '{"mode":"structured","messages":[{"role":"user","content":"What is the boiling point of water at sea level in Celsius?"}]}' \
  | bash arenas/live/genkit-go/run.sh

# stream
echo '{"mode":"stream","messages":[{"role":"user","content":"Write two sentences about maglev trains."}]}' \
  | bash arenas/live/genkit-go/run.sh

# observability (bare request — defaults to a 2-tool representative turn)
echo '{"mode":"observability"}' | bash arenas/live/genkit-go/run.sh

# middleware (native ai.ModelMiddleware, inbound + outbound proof)
echo '{"mode":"middleware","system":"You are a rail assistant.","messages":[{"role":"user","content":"Say OK."}]}' \
  | bash arenas/live/genkit-go/run.sh

# ARENA_STUB (any mode, fully offline — no OPENAI_*/EMBED_URL/QDRANT_URL/RQLITE_URL needed)
echo '{"mode":"qa","messages":[{"role":"user","content":"Say OK."}]}' \
  | ARENA_STUB=1 bash arenas/live/genkit-go/run.sh
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

### `test-isolation.sh`

```bash
bash arenas/live/genkit-go/test-isolation.sh
```

The executable proof for `memory_scoped` and `isolation_leak` — two real agnes calls against
the real rqlite node, PASS/FAILing:

- **memory_scoped** — the smoke call succeeds, `same_scope_hit=true`, `cross_tenant_miss=true`,
  `recalled_text` is non-empty, and neither stdout nor stderr contain the literal API key.
- **isolation_leak** — the smoke call succeeds, `leak_detected=false`, the model's reply does not
  contain the literal secret text, and neither stdout nor stderr contain the literal API key.

Each run uses a fresh `tenant_id`/secret suffixed with a run id (`$(date +%s)-$$`) so repeated
runs don't reuse another run's rows in the scoped table, which has no retention policy (see
"Uncertainties").

### `test-crash.sh`

```bash
bash arenas/live/genkit-go/test-crash.sh
```

The executable proof for `crash_recovery` — a real hard-crash-and-resume sequence against the
real rqlite node, PASS/FAILing 15 assertions across three turns:

- **turn 1** (blocking) — smoke call succeeds, `checkpointed=true`, `resumed=false` (fresh
  session).
- **turn 2** (backgrounded, then `kill -9`ed) — the script polls stderr for the
  `entering genkit.Generate` marker and confirms it's still alive at that instant before killing
  it; asserts a non-zero (signal-killed) `wait` status and an **empty stdout** (proof the process
  never reached its final JSON emit, i.e. it really died mid-flight and not after quietly
  succeeding); neither stdout nor stderr contain the literal API key.
- **turn 3** (a completely fresh process) — smoke call succeeds, `resumed=true`,
  `prior_turns_loaded=2` (exactly turn 1's two rows, proving turn 2 contributed none),
  `precrash_fact_recalled=true`, `state_lost=false`, and — independent of the adapter's own
  self-reported booleans — the script greps the raw `reply` text for the fact code itself;
  neither stdout nor stderr contain the literal API key.

Each run uses a fresh `session_id` and fact code suffixed with a run id (`$(date +%s)-$$`) so
repeated runs never collide with an earlier run's rows.

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

// memory_scoped — same_scope_hit=true, cross_tenant_miss=true
{"framework":"genkit-go","mode":"memory_scoped","model":"agnes-2.0-flash","reply":"The customer's preferred shipping method is overnight express.","input_tokens":314,"output_tokens":11,"latency_ms":7730,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"scoped_recall":{"same_scope_hit":true,"cross_tenant_miss":true,"recalled_text":"The customer's preferred shipping method is overnight express."},"leak_detected":null,"error":""}

// isolation_leak — leak_detected=false (tenant B's scope query returned nothing, so agnes had no
// secret in its context to leak)
{"framework":"genkit-go","mode":"isolation_leak","model":"agnes-2.0-flash","reply":"I do not have any record of secrets being shared with me in this conversation. As an AI, I do not retain memory of past interactions once the session ends, and I do not store personal secrets.","input_tokens":310,"output_tokens":42,"latency_ms":7188,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"scoped_recall":null,"leak_detected":false,"error":""}

// crash_recovery — turn 1 (process A, runs to completion, checkpoints)
{"framework":"genkit-go","mode":"crash_recovery","model":"agnes-2.0-flash","reply":"Understood, I will remember DELTA-1784330248.","input_tokens":352,"output_tokens":16,"latency_ms":6612,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"scoped_recall":null,"leak_detected":null,"resumed":false,"precrash_fact_recalled":null,"state_lost":null,"prior_turns_loaded":0,"checkpointed":true,"error":""}

// crash_recovery — turn 2 (process A, launched, SIGKILLed mid-genkit.Generate by test-crash.sh;
// stdout is EMPTY — no JSON was ever emitted, nothing was ever checkpointed for this turn)

// crash_recovery — turn 3 (process B, a completely FRESH process; its own input never mentions
// the fact code; resumed=true / prior_turns_loaded=2 prove it picked up ONLY turn 1's checkpoint)
{"framework":"genkit-go","mode":"crash_recovery","model":"agnes-2.0-flash","reply":"DELTA-1784330248-8220","input_tokens":360,"output_tokens":19,"latency_ms":6930,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"scoped_recall":null,"leak_detected":null,"resumed":true,"precrash_fact_recalled":true,"state_lost":false,"prior_turns_loaded":2,"checkpointed":true,"error":""}

// observability — bare {"mode":"observability"} request, own (non-shared-envelope) contract;
// 6 real spans captured from ONE representative tools-mode turn (2 real agnes calls, since the
// tool loop recurses: decide-to-call-tools turn, then finalize-after-tool-results turn)
{"framework":"genkit-go","mode":"observability","otel_supported":true,"span_count":6,"span_names":["calculator","corpus_lookup","generate","generate","openai/agnes-2.0-flash","openai/agnes-2.0-flash"],"span_timings_ms":{"calculator":0.113208,"corpus_lookup":362.489833,"generate":6908.858416,"generate#2":17626.776208,"openai/agnes-2.0-flash":10354.332083,"openai/agnes-2.0-flash#2":6908.474916},"logs_structured":true,"log_sample":"{\"time\":\"2026-07-17T20:18:30.213215-03:00\",\"level\":\"DEBUG\",\"msg\":\"span start\",\"name\":\"generate\"}","error":""}
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

```
$ bash arenas/live/genkit-go/test-isolation.sh
--- (1) memory_scoped: same-scope hit + cross-tenant miss ---
PASS: memory_scoped: smoke call succeeded (error field empty)
PASS: memory_scoped: same_scope_hit=true (fact recalled under the scope it was written to)
PASS: memory_scoped: cross_tenant_miss=true (a different tenant's recall returned nothing)
PASS: memory_scoped: recalled_text is non-empty ("The customer's preferred shipping method is overnight express.")
PASS: memory_scoped: stdout does not contain the literal API key
PASS: memory_scoped: stderr does not contain the literal API key
--- (2) isolation_leak: zero leak across tenants ---
PASS: isolation_leak: smoke call succeeded (error field empty)
PASS: isolation_leak: leak_detected=false (tenant B's scope did not surface tenant A's secret)
PASS: isolation_leak: the model's reply does not contain the literal secret text
PASS: isolation_leak: stdout does not contain the literal API key
PASS: isolation_leak: stderr does not contain the literal API key
---
RESULT: ALL PASS
```

All runs above are from a single, fully re-verified pass through the real agnes
endpoint, the real embed server, the real Qdrant collection, and a real rqlite node
(this session) — 12 total real agnes calls across every mode's smoke plus
`test-env.sh`'s two `qa` smokes, under the 15-call budget (the `tools` smoke made 2
internal agnes round-trips within Genkit's own tool loop — the decision turn and the
finalize-after-tool-results turn — counted in that total).

The `memory_scoped`/`isolation_leak` addition (a later pass, same real infra) made **2 real
agnes calls** for its own smoke output above plus **2 more** inside `test-isolation.sh` (one per
mode) — 4 total, well under an 8-call budget for that pass.

```
$ bash arenas/live/genkit-go/test-crash.sh
--- (1) turn 1: state the fact, run to completion, checkpoint ---
PASS: turn 1: smoke call succeeded (error field empty)
PASS: turn 1: checkpointed=true (turn 1 durably written before turn 2 begins)
PASS: turn 1: resumed=false (fresh session, nothing to resume yet)
--- (2) turn 2: launch, wait for mid-flight marker, SIGKILL ---
PASS: turn 2: caught mid-flight (uncommitted — inside genkit.Generate, before any checkpoint write)
PASS: turn 2: process did not exit cleanly after SIGKILL (wait status=137, expected non-zero)
PASS: turn 2: stdout is empty (the process never reached its final JSON emit — proves it died mid-flight, not after success)
PASS: turn 2: stdout/stderr does not contain the literal API key
--- (3) turn 3: fresh process, resume, recall the pre-crash fact ---
PASS: turn 3: smoke call succeeded (error field empty)
PASS: turn 3: resumed=true (loaded checkpointed state left behind by an earlier, now-dead process)
PASS: turn 3: prior_turns_loaded=2 (exactly turn 1's user+assistant rows — turn 2 contributed zero, as expected)
PASS: turn 3: precrash_fact_recalled=true
PASS: turn 3: state_lost=false
PASS: turn 3: reply text independently contains the fact code (DELTA-1784330248-8220) — cross-checked outside the adapter's own self-report
PASS: turn 3: stdout does not contain the literal API key
PASS: turn 3: stderr does not contain the literal API key
---
FINAL_JSON: {"framework":"genkit-go","mode":"crash_recovery","model":"agnes-2.0-flash","reply":"DELTA-1784330248-8220","input_tokens":360,"output_tokens":19,"latency_ms":6930,"tool_calls":[],"retrieved":[],"structured":null,"ttft_ms":0,"scoped_recall":null,"leak_detected":null,"resumed":true,"precrash_fact_recalled":true,"state_lost":false,"prior_turns_loaded":2,"checkpointed":true,"error":""}
---
RESULT: ALL PASS
```

The `crash_recovery` addition (a later pass, same real infra) made **2 real, completed agnes
calls** (turn 1, turn 3) plus **1 real agnes request sent-but-response-discarded** (turn 2 — the
request was in flight when SIGKILLed; the adapter process never received or processed the
response) — well under an 8-call budget for that pass. Run twice during this pass to confirm
determinism; both runs were `RESULT: ALL PASS`.

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
| `go.opentelemetry.io/otel` (direct as of `observability` mode; previously transitive-only via genkit) | Apache-2.0 |
| `go.opentelemetry.io/otel/sdk` (direct as of `observability` mode; previously transitive-only via genkit) | Apache-2.0 |
| `go.opentelemetry.io/auto/sdk`, `otel/metric`, `otel/trace` | Apache-2.0 |
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

`memory_scoped` and `isolation_leak` add **zero new dependencies** — they reuse the same
`rqliteClient` (`rqlite_client.go`) and the same `github.com/firebase/genkit/go` /
`github.com/openai/openai-go` call surfaces every other mode already uses. `go.mod`/`go.sum` are
unchanged by this addition.

`crash_recovery` likewise adds **zero new dependencies** — it reuses the same `rqliteClient`
(new methods on the existing type in `rqlite_client.go`, a new table, no new package) and the
same Genkit/agnes call surface. `go.mod`/`go.sum` are unchanged by this addition too.

## Files

- `main.go` — mode dispatch, shared arena I/O contract, agnes/Genkit client setup, `qa` mode.
- `rag.go` — `rag` mode: embed → Qdrant search → grounded `genkit.DefineFlow` answer.
- `tools.go` — `tools` mode: real multi-step tool execution (calculator, corpus_lookup, clock fallback) + the tiny arithmetic evaluator.
- `memory.go` — `memory` mode: rqlite-backed cross-process conversation persistence (session_id only).
- `crash_recovery.go` — `crash_recovery` mode: rqlite-backed CHECKPOINTED conversation persistence,
  designed to be SIGKILLed mid-turn by `test-crash.sh` and resumed by a fresh process.
- `scoped_memory.go` — `memory_scoped` and `isolation_leak` modes: hand-rolled compound-scope
  (tenant_id, user_id, product_id, session_id) memory + the cross-tenant leak proof.
- `structured.go` — `structured` mode: `ai.WithOutputType` schema-validated output.
- `stream.go` — `stream` mode: `ai.WithStreaming` + `ttft_ms`.
- `observability.go` — `observability` mode: installs an in-process `go.opentelemetry.io/otel/sdk`
  `TracerProvider` + in-memory span exporter (no collector) before `genkit.Init`, drives one
  representative `tools`-mode turn, and reports Genkit's own captured span names/counts/timings
  plus a structured-log sample (see its own README section above).
- `middleware.go` — `middleware` mode: a real `ai.ModelMiddleware` (`func(ai.ModelFunc) ai.ModelFunc`)
  attached via `ai.WithMiddleware`, proving one inbound (pre-model, sentinel-injecting) and one
  outbound (post-model) transform actually fired around one qa-shaped turn.
- `stub.go` — the `ARENA_STUB` toggle: `stubMode` (read from the env once), the locally
  `genkit.DefineModel`'d echo model (`stubModelFunc`) with schema-conforming structured-output and
  offline tool-call synthesis, `stubEmbedding`, and `stubQdrantSearch`'s canned corpus.
- `embed_client.go` — HTTP client for the real e5 embeddings server (stub-aware: see `stub.go`).
- `qdrant_client.go` — real Qdrant search via `github.com/qdrant/go-client` (gRPC; stub-aware: see `stub.go`).
- `rqlite_client.go` — HTTP client for rqlite's `/db/execute` / `/db/query` (stub-aware: see `stub.go`'s
  "ARENA_STUB in-memory backing" comment); also the compound-scope table
  (`arena_genkit_go_scoped_memory`) used by `scoped_memory.go` and the crash-recovery checkpoint
  table (`arena_genkit_go_crash_recovery`) used by `crash_recovery.go`.
- `run.sh` — native runner: sources repo-root `.env`, builds, forwards stdin → stdout.
- `Dockerfile` — multi-stage static build on `distroless/static-debian12:nonroot`.
- `run-docker.sh` — builds + runs the Docker image, repointing infra URLs at `host.docker.internal` and passing `OPENAI_*` as runtime-only env.
- `test-env.sh` — automated credential/env PASS/FAIL assertions (native + Docker).
- `test-isolation.sh` — automated PASS/FAIL assertions for `memory_scoped`/`isolation_leak`
  (same-scope hit, cross-tenant miss, zero leak).
- `test-crash.sh` — automated PASS/FAIL assertions for `crash_recovery`: checkpoints turn 1,
  SIGKILLs turn 2 mid-flight, then proves a fresh process resumes correctly from exactly what
  was checkpointed.
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
- **`memory_scoped`/`isolation_leak`'s `arena_genkit_go_scoped_memory` table** has no
  retention/cleanup policy either (same caveat as `memory` mode's table, one row up) — every
  `memory_scoped`/`isolation_leak` invocation against the same rqlite node accumulates rows
  forever. `test-isolation.sh` suffixes its `tenant_id`/secret with a run id specifically to
  avoid a later run's cross-tenant recall accidentally matching an earlier run's leftover row
  under the same synthesized `cross_tenant_id`.
- **`cross_tenant_id`'s default synthesis** (`"<tenant_id>-cross-tenant-probe"`) is this
  adapter's own convention for picking "a different tenant" when the caller doesn't supply one
  — not a claim about how a real caller should name tenants. A caller that already has two real
  tenant ids should pass `cross_tenant_id` explicitly rather than relying on the synthesized
  default.
- **`isolation_leak`'s reply-level leak check** (`strings.Contains(reply, fact)`) is an exact
  substring match against the literal secret text passed in `fact`. A model that paraphrases or
  partially reconstructs a leaked secret without repeating the exact string would not be caught
  by this check — only by the store-level check, which is why the store-level assertion is
  documented as primary/authoritative and the reply-level check as a secondary signal, not the
  other way around.
- **`crash_recovery`'s two-call checkpoint gap is unverified**: a turn's inbound message and its
  reply are two separate `appendCrashTurn` calls, not one rqlite transaction. `test-crash.sh`
  targets the (much wider) window inside `genkit.Generate`, which it can reliably hit by polling
  for a stderr marker; it does NOT attempt to land a kill in the narrow window between the two
  `appendCrashTurn` calls, so that specific partial-checkpoint case (a dangling inbound row with
  no matching reply row) is a real, named uncertainty, not something this pass demonstrated one
  way or the other. See the "crash_recovery" per-mode section's HONEST note for the full
  breakdown of what is and isn't proven.
- **`precrash_fact_recalled`'s substring check** (`strings.Contains(reply, fact)`, case-insensitive)
  has the same limitation as `isolation_leak`'s reply-level check above: a model that paraphrases
  the recalled fact instead of repeating it verbatim would score `precrash_fact_recalled=false`
  even though the underlying checkpoint was read correctly — a false negative on the model's
  phrasing, not a failure of persistence. `test-crash.sh`'s prompt (`"Reply with just the code,
  nothing else"`) is deliberately chosen to make this unlikely for the smoke test, but the check
  itself is textual, not semantic.
- **`crash_recovery`'s rqlite table** (`arena_genkit_go_crash_recovery`) has no retention/cleanup
  policy either (same caveat as `memory` and `memory_scoped`/`isolation_leak`'s tables, above) —
  `test-crash.sh` suffixes `session_id` and the fact code with a run id specifically to avoid
  collisions with an earlier run's leftover rows.
- **`test-crash.sh`'s SIGKILL timing relies on a stderr marker plus `kill -0`**, not a hard
  guarantee that the process is still inside `genkit.Generate` at the exact instant `kill -9` is
  sent (there is an unavoidable, small window between the `grep` observing the marker and the
  `kill -9` call landing). In every run during this verification the killed process's stdout was
  empty and turn 3 loaded exactly 2 prior rows, which is the actual evidence the crash landed
  where intended — but this is inference from outcome, not a proof of the exact instruction the
  process was executing when signaled.
