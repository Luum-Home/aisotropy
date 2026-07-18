# aisotropy — live-arena adapter (EXHAUSTIVE, multi-mode)

The **reference** adapter for the live arena, upgraded from the original single-shot
`qa` adapter into an exhaustive **multi-mode** adapter that competes with the six
framework adapters on equal footing — using aisotropy's **real** backend tiers (not
the in-process stub defaults) and its **native** capabilities, including the one
capability no other framework here has: **executing a tool inside a WebAssembly
sandbox**.

Every mode drives a distinct, real capability of aisotropy's public façade
(`github.com/Luum-Home/aisotropy/aisotropy`) against the real `agnes-2.0-flash`
model held constant for the whole arena.

## Contract

Reads **one** JSON object on stdin (a `"mode"` field selects behavior), writes
**exactly one** JSON object on stdout (all logs to stderr):

```json
{"framework":"aisotropy","mode":"<mode>","model":"agnes-2.0-flash","reply":"…",
 "input_tokens":0,"output_tokens":0,"latency_ms":0,"tool_calls":[…],
 "retrieved":[…],"structured":<obj|null>,"ttft_ms":0,"error":""}
```

Provider-reported token usage only (or `-1` when unavailable). Errors are data, not
crashes. The agnes API key is read from `OPENAI_API_KEY` and **never** printed,
logged, or baked into the image.

## Modes

| mode | aisotropy capability exercised | seam |
|---|---|---|
| `qa` | Single-shot chat, full in-process pipeline (identity→ctx isolation, quota gate, guardrails, gateway model selection, memory hydration, outbox persist, fail-closed reflection, cost accounting). | `App.Turn` |
| `rag` | **Real** native RAG over aisotropy's OWN tiered memory: ingest the 22-doc Nimbus-Rail corpus into its semantic tier, then answer via aisotropy's real semantic hydration. | HTTP `POST /v1/…/knowledge` → `POST /v1/…/ask` |
| `tools` | **Real** tool EXECUTION: the model chooses a scope-gated `calculate` tool, aisotropy executes it and folds the result in. | HTTP `POST /v1/…/chat/agent` (`internal/api/chat_agent.go`) |
| `memory` | **Real** tiered memory ACROSS TWO PROCESS INVOCATIONS: persist a fact, recall it in a fresh process on the same tenant/user/session. | `App.Turn` + on-disk BadgerDB (`AISOTROPY_DATA_DIR`) |
| `structured` | Schema-shaped structured output over the real agent route, fail-closed (a bad `confidence` enum is rejected, never coerced). | HTTP `POST /v1/…/chat/agent` + advertised tool schema |
| `stream` | **Real** SSE streaming with time-to-first-token measured. | HTTP `POST /v1/…/chat/stream` |
| `wasm_tool` | **aisotropy's DIFFERENTIATOR**: a tool body executed inside a **wazero** WebAssembly sandbox (memory-isolated, no host FS/net/clock), its result folded into a real agnes turn. | in-process wazero (`wasmhost.go`, reproducing `internal/wasmtool`'s ABI) |
| `memory_scoped` | **Real** per-(tenant, user, product, session) scoped memory. A fact written under one scope is recalled there (`same_scope_hit`) but returns **nothing** to a different tenant (`cross_tenant_miss`) — enforced by aisotropy's **by-construction** isolation, not an adapter key. | `App.Turn` + working-memory key prefix |
| `isolation_leak` | **Real** constitutional isolation gate: a secret seeded by tenant A is **never** surfaced to a probe issued as tenant B (`leak_detected=false`) — the key prefix + ctx-scoped facts/semantic filters bound hydration to the probing identity. | `App.Turn` (probe as a different tenant) |
| `crash_recovery` | **Real** durability: a multi-turn conversation persisted to on-disk BadgerDB survives a hard `kill -9` mid-turn; a **fresh process** re-opens the store and recalls the pre-crash fact (`resumed`/`precrash_fact_recalled=true`, `state_lost=false`). | `App.Turn` + on-disk BadgerDB (`AISOTROPY_DATA_DIR`) across a SIGKILL |
| `observability` | **Real** measurement of aisotropy's NATIVE OpenTelemetry span emission (`internal/llm/otel.go`, ADR-0010) for one representative turn — no bridge, no auto-instrumentation, captured in-process (no external collector). Distinct stdout contract, see below. | `App.Turn` + an in-process OTLP/HTTP receiver this adapter stands up itself |

### How the native tool-execution path is wired (`tools`)

`App.Turn` is aisotropy's text-first chat surface and deliberately **cannot** surface
native `tool_calls` (that was the `0/3` artifact in the original single-shot adapter).
The real tool-execution seam is the **agent route** `POST /v1/…/chat/agent`
(`internal/api/chat_agent.go`, ADR-0012). So `tools` mode:

1. registers a real `calculate` tool (`aisotropy.WithTool` + `aisotropy.WithToolExecutor`,
   `RequiredScope:"calc"`) whose executor **actually evaluates** the arithmetic
   (`calc.go`, a shunting-yard evaluator — never a canned value);
2. boots `App.Run` on an ephemeral loopback port with `WithAuthenticator(NoAuth())`
   (allowed because the listener is loopback);
3. `POST`s `/chat/agent` with the arithmetic prompt and `scopes:["calc"]`;
4. aisotropy runs its agentic loop, the model selects `calculate`, the runtime
   executes it through the registered executor and returns the ordered tool
   decisions + results, which become the response `tool_calls` (with the executed
   `result`), folded into `reply`.

Because the injected LLM client is a plain streaming `LLMClient` (not an
`llm.ToolCaller` — that internal interface can't be implemented across the module
boundary), the route runs its **structured-output agent-loop branch** of
`chat_agent.go` — the same endpoint, same real executor, same double scope-gate.

### The WASM differentiator (`wasm_tool`)

`testdata/wasmguest/main.go` is a `wasip1` reactor guest implementing aisotropy's
WASM tool ABI (`aisotropy_alloc` / `aisotropy_run`, packed-i64 result). It is
compiled by `build.sh` (`GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`) into
`calc.wasm` and embedded via `go:embed`. `wasmhost.go` reproduces
`internal/wasmtool`'s runner (the adapter is a separate module and can't import
`internal/`): a fresh guest instance per call, hard memory bound
(`WithMemoryLimitPages`), CPU deadline (`WithCloseOnContextDone`), and WASI present
but **powerless** (no preopens/env/net). The guest also proves its isolation by
attempting a host file read and reporting `fs_read_denied=true` — the honest basis
for the broker invariant that **the sandbox cannot reach the agnes key** the host
holds.

### Isolation BY CONSTRUCTION, not a hand-rolled key (`memory_scoped`, `isolation_leak`)

The six framework adapters have **no native multi-tenant memory-isolation
primitive**, so each HAND-ROLLS a compound scope key (`tenant_id ⋄ user_id ⋄
product_id ⋄ session_id`) over a shared store and trusts every future dev to
compute it correctly — a field forgotten or an argument transposed is silent at
compile time and shows up only as one tenant answering another's question.

aisotropy needs **no adapter-side key or filter**. Its isolation is
constitutional (`.claude/rules/constitutional-gates.md`):

- **Working memory (BadgerDB)** keys every entry under
  `tenant:{t}:user:{u}:session:{s}:…` (`internal/memory/key.go`). The `:`
  separator is a **reserved character rejected inside any identity component**,
  so `tenant:A:…` can never be a prefix of `tenant:B:…` — one tenant's scan
  cannot reach another's buffer. `SubjectPrefix` is the enforcement point.
- **Facts (rqlite/SQLite FTS5)** and **semantic (Qdrant)** are scoped from the
  **same ctx identity**: Qdrant applies a tenant/user **payload filter DURING
  the HNSW scan** (an in-scan filter, not a post-filter — a post-filter would be
  a leak, not a fix), and facts are FTS5-scoped per tenant.

Both modes drive the **real runtime** (`App.Turn`, full parity with
`POST /v1/tenant/{tenant_id}/user/{user_id}/chat`): the fact is persisted as a
real user turn under the caller's identity, and recall is a real turn whose
hydration is bounded to that identity by the seams above. **The adapter never
looks at a key or filters a row** — it asks aisotropy under a given identity and
reports whether the scoped fact came back (`expect_substring` present in
aisotropy's own reply). `same_scope_hit` / `cross_tenant_miss` / `leak_detected`
are that single observation — the constitutional gate seen end to end.

Verified run (`test-isolation.sh`, real agnes): same-scope recall returned the
marker; the cross-tenant recall returned `48291` (a guess — **no marker**,
because nothing was hydrated for tenant B); the tenant-B probe reported
`leak_detected=false`.

### Crash durability across a real SIGKILL (`crash_recovery`)

Distinct from `memory` mode (recall across a **clean** exit): `crash_recovery`
proves aisotropy's on-disk BadgerDB working memory survives a **hard `kill -9`
mid-conversation**. `App.Turn` persists each user turn to the on-disk store
(`internal/api/chat.go` → `chatPersistUserTurn` → `AppendWithOutbox`) **before**
the model call, so a committed turn is already durable when a later turn's
in-flight LLM call is killed. `test-crash.sh` runs two REAL, separate OS
processes:

1. **producer** (process A): a 2-turn conversation on one `(tenant,user,session)`;
   turn 1 states a secret. The script waits for the stderr line proving turn 1
   is committed AND turn 2's model call is in-flight, then `kill -9`s the
   **standalone binary** (not a `go run` parent), so the signal lands on the
   process holding the BadgerDB handle. No `defer`/`Close()` ever runs.
2. **resumer** (process B): a completely fresh process that re-opens the SAME
   `AISOTROPY_DATA_DIR` (BadgerDB replays its value log) and asks a question only
   answerable by recalling the turn-1 fact.

**What actually survives a hard kill (honest note):** turn 1's write already
reached the value log via `write()` before the kill; the OS page cache retains
those bytes across a **process** death (they are lost only on an OS crash /
power loss), and BadgerDB replays the value log on the fresh open — so the
resumer recovers the fact even though process A never fsync'd or closed cleanly.
Because each user turn commits atomically **before** its model call, the kill
lands between commits: there is no torn transaction to corrupt the store, and
only turn 2's **assistant** reply (whose LLM call was interrupted) is lost.
Through the public façade the authoritative proof that state survived is that
the runtime **recalled** it — a fresh process could only echo the marker if
aisotropy's on-disk store replayed the pre-crash turn into hydration, so
`resumed` is set to that same recall signal (and `state_lost` its negation).

Verified run (`test-crash.sh`, real agnes + real SIGKILL): resumer returned
`BLUE-FALCON-77` with `resumed=true`, `precrash_fact_recalled=true`,
`state_lost=false`.

### Measuring aisotropy's NATIVE OTel emission (`observability`)

aisotropy is the only adapter in this arena backed by a framework that ships
**native** OpenTelemetry tracing (`internal/llm/otel.go`, ADR-0010) — no bridge, no
third-party instrumentation package, no auto-instrumentation agent. `WithOTel` is a
hand-written decorator over the internal `llm.Client` interface that starts one real
`go.opentelemetry.io/otel/trace` span per `StreamChat`/`CallTools` call, carrying a
small, locally-owned `gen_ai.*` attribute vocabulary (`gen_ai.system`,
`gen_ai.request.model`, `gen_ai.operation.name`, `gen_ai.usage.input_tokens` /
`.output_tokens`, plus `aisotropy.ttft_ms` / `aisotropy.duration_ms`) deliberately
name-compatible with the OTel GenAI semantic-convention draft.

`observability` mode's stdout contract is **distinct** from every other mode (see
`mode_observability.go`'s `observabilityOutput`), matching the sibling
`eino`/`genkit-go` adapters' observability modes:

```json
{"framework":"aisotropy","mode":"observability","otel_supported":true,
 "span_count":1,"span_names":["chat"],"span_timings_ms":{"chat":7238.39},
 "logs_structured":true,"log_sample":"…","notes":"…"}
```

**How the spans are actually captured (investigated against the code, not
assumed).** aisotropy's `App` does **not** read the process-global
`otel.GetTracerProvider()` (unlike, e.g., Genkit-Go's `core/tracing.go`), and the
public façade (`aisotropy.Option`) exposes **no `WithTracerProvider` builder
option** either. Every `App.startRuntime` call builds its **own**
`*sdktrace.TracerProvider` from scratch (`internal/llm/otel.go`'s
`NewTracerProvider`), choosing between a no-op provider and a real
`go.opentelemetry.io/otel/sdk/trace` provider batched over OTLP/HTTP
(`otlptracehttp`), gated purely on the standard
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT` env vars —
there is no `sdktrace.WithSyncer`/in-memory-exporter lever reachable from this
adapter without editing `internal/llm/otel.go` itself, which is out of scope (this
adapter is additive-only, per `.claude/rules/prune-evidence-gate.md` and
`.claude/rules/architecture.md`).

So `otlpReceiver` (`mode_observability.go`) stands up a loopback-only, in-process
HTTP server (`net/http/httptest`, same OS process, **no external collector
binary/container, no network hop leaving the host**) that decodes the **real**
OTLP/HTTP wire protocol (protobuf, optionally gzip,
`go.opentelemetry.io/proto/otlp/collector/trace/v1`) exactly as
`otlptracehttp.Exporter` emits it — the wire contract is mirrored, for
compatibility, from `otlptracehttp`'s own test-only mock collector
(`otlptracehttp@v1.44.0/mock_collector_test.go`). `OTEL_EXPORTER_OTLP_ENDPOINT` is
pointed at that receiver before `newApp` builds the `App`; `app.Close()` (which
flushes the batched exporter via the same `shutdownTracing` func `startRuntime`
built the tracer with) is called explicitly, before reading the receiver's
snapshot, so span capture is deterministic rather than racing the exporter's
default 5s batch interval. No span is fabricated, filtered, or synthesized —
every span reported is exactly what the real exporter sent over the wire.

The mode also installs a `slog.JSONHandler` in front of the process-wide
`slog.Default()` before booting the App, so aisotropy's own `log/slog` calls
(`aisotropy/app.go`'s `slog.Info`/`slog.Error` sites) are captured as real,
typed-attribute JSON (`logs_structured`), the same technique a real integrator
wiring log aggregation would use.

**Complementary trail.** aisotropy also ships a hash-chained, tamper-**evident**
audit sink (`internal/audit/hashchain.go`, ADR-0056): each record commits
`SHA-256(prev_hash || canonical(event))`, so any edit/removal/reorder of a past
identity+request-metadata record breaks the chain (independently checkable via
`internal/audit/cmd/audit-verify`). That is a *different* concern from tracing
(integrity of the request log, not latency/attribute observability) — not wired
into this mode's run, but a second, independent observability signal aisotropy
ships natively.

Verified run (real agnes, real backend tiers): `otel_supported=true`,
`span_count=1`, `span_names=["chat"]`, `span_timings_ms={"chat":7238.39}`,
`logs_structured=true` with a real captured `log_sample` — a genuine
`slog`-emitted WARN about working-memory encryption-at-rest, not a canned string.

## Real backends (env-configurable)

`run.sh` wires the real tiers (defaults target the local live-arena infra;
`run-docker.sh` points them at `host.docker.internal`):

| Env | Tier | Default |
|---|---|---|
| `AISOTROPY_EMBED_URL` / `AISOTROPY_EMBED_MODEL` / `AISOTROPY_EMBED_DIM` | e5 semantic embedder (real embed server) | `http://127.0.0.1:8899` / `multilingual-e5-small-query` / `384` |
| `QDRANT_HOST` / `QDRANT_PORT` / `QDRANT_COLLECTION` | semantic tier (real Qdrant, gRPC) | `localhost` / `6334` / `aisotropy_arena` |
| `QDRANT_REST_URL` | Qdrant REST (used only to MIRROR aisotropy's retrieval for the `retrieved` field) | `http://localhost:6333` |
| `RQLITE_URL` | facts tier (real networked rqlite) | `http://localhost:4001` |
| `AISOTROPY_DATA_DIR` | on-disk BadgerDB working memory (stable so `memory` mode persists across processes) | `./.aisotropy-data` |

The arena uses its OWN 384-dim Qdrant collection (`aisotropy_arena`) so it never
clobbers a pre-existing `aisotropy_knowledge` collection of a different dimension.

## Usage

```bash
# native
echo '{"mode":"rag","messages":[{"role":"user","content":"…"}]}' | bash run.sh

# docker (build context is the REPO ROOT because of the replace directive)
echo '{"mode":"qa","messages":[{"role":"user","content":"…"}]}' | bash run-docker.sh

# credential / env tests (PASS/FAIL per assertion)
bash test-env.sh            # native + wasm-isolation + docker no-key checks
RUN_DOCKER=0 bash test-env.sh   # skip the slow docker checks

# multi-tenant isolation tests (memory_scoped + isolation_leak; 5 real agnes calls)
bash test-isolation.sh

# crash-durability test (crash_recovery; real kill -9 across two processes)
bash test-crash.sh

# observability (native OTel span capture; runnable bare, no stdin fields required)
echo '{"mode":"observability"}' | bash run.sh
```

## Known upstream issue worked around (rqlite migration idempotency)

`internal/facts/rqlite.go`'s `migrate()` is **not idempotent** across process
restarts: its schema runs a bare `ALTER TABLE facts ADD COLUMN fts_content …`, and on
a re-run rqlite returns a **transaction-level** error that `migrate()` returns
*before* it reaches the per-statement duplicate-column guard it (correctly) applies
on the embedded-SQLite path (`internal/facts/sqlite.go`). So every boot after the
first against an already-migrated rqlite fails.

`internal/` is out of scope for this adapter, so `rqlite_reset.go` resets **only
aisotropy's own** `facts`/`facts_fts` tables (never other adapters' memory tables)
before each boot, so the non-idempotent `ALTER` always runs clean. This keeps the
**real networked rqlite** tier (aisotropy still writes/reads it within the run) and
does not weaken `memory` mode (cross-process recall rides on the BadgerDB working
tier, never reset). The honest fix belongs upstream in `internal/facts/rqlite.go`.

A second gotcha, Docker-only: gorqlite does cluster discovery and adopts the address
the rqlite node advertises for itself (`localhost:4001`), which is unreachable from
inside the container. `run-docker.sh` therefore appends `?disableClusterDiscovery=true`
to the container's `RQLITE_URL`.

## Files

| Path | Role |
|---|---|
| `main.go` | Contract + mode dispatch. |
| `agnes_client.go` | Usage-capturing `LLMClient` over agnes (OpenAI Chat Completions SSE). |
| `app.go` | Shared `App` builder + usage read-back. |
| `mode_qa.go` / `mode_rag.go` / `mode_tools.go` / `mode_memory.go` / `mode_structured.go` / `mode_stream.go` / `mode_wasmtool.go` | The seven original modes. |
| `mode_scoped.go` | `memory_scoped` + `isolation_leak` — by-construction tenant isolation via `App.Turn` (working-memory key prefix + ctx-scoped tiers). |
| `mode_crash.go` | `crash_recovery` — on-disk BadgerDB durability across a real `kill -9` (producer / resumer). |
| `mode_observability.go` | `observability` — captures aisotropy's native OTel span emission via an in-process OTLP/HTTP receiver + structured-log capture (distinct stdout contract). |
| `rag_backends.go` | Corpus loader + e5 query embed + Qdrant REST mirror of aisotropy's retrieval. |
| `server_boot.go` | Boots `App.Run` on an ephemeral loopback port (tools/stream/structured). |
| `calc.go` | Host-side arithmetic evaluator (tools mode). |
| `wasmhost.go` | wazero runner reproducing `internal/wasmtool`'s ABI. |
| `testdata/wasmguest/main.go` | wasip1 WASM tool guest → `calc.wasm`. |
| `rqlite_reset.go` | Upstream-bug workaround (see above). |
| `build.sh` / `run.sh` / `run-docker.sh` / `test-env.sh` | Build, native runner, docker runner, credential tests. |
| `test-isolation.sh` / `test-crash.sh` | Multi-tenant isolation tests (PASS/FAIL) and the two-process real-SIGKILL crash-durability test. |
| `Dockerfile` | Multi-stage, distroless, static, no baked key. |

## Dependencies & licenses (all MIT / BSD / Apache-2.0)

| Dependency | License |
|---|---|
| `github.com/Luum-Home/aisotropy` (façade + its MIT/Apache stack) | MIT-family |
| `github.com/tetratelabs/wazero` | Apache-2.0 |
| `go.opentelemetry.io/proto/otlp` (already an aisotropy transitive dep; used directly by `mode_observability.go`'s in-process OTLP receiver) | Apache-2.0 |
| `google.golang.org/protobuf` (already an aisotropy transitive dep; used directly to decode/encode OTLP wire messages) | BSD-3 |
| Go standard library | BSD-3 |

No GPL/AGPL/SSPL/BSL — passes the repo's constitutional license gate.
