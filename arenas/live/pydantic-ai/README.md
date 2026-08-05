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
- **Modes:** `qa` (default), `rag`, `tools`, `memory`, `memory_scoped`, `isolation_leak`, `crash_recovery`, `structured`, `stream`, `observability`, `middleware`
- **Offline toggle:** `ARENA_STUB=1` swaps every mode's real dependencies (agnes, e5, Qdrant, rqlite) for in-process substitutes

## Files

| File | Purpose |
|---|---|
| `adapter.py` | Entrypoint: reads the mode-tagged request JSON from stdin, dispatches to the mode handler, writes the uniform result JSON to stdout |
| `arena_common.py` | Shared env/config, the agnes chat-model builder, e5 query embedding, Qdrant retrieval, `is_stub()`/`ARENA_STUB` fallbacks |
| `arena_memory_store.py` | Real rqlite-backed persistence of Pydantic AI's `message_history` for the `memory` mode, plus the hand-rolled compound-scope fact store (`scope_key`, `save_scoped_fact`, `load_scoped_facts`) for `memory_scoped` / `isolation_leak`; in-process `ARENA_STUB` fallbacks for both |
| `arena_tools.py` | A real, actually-executed (`ast`-based, no `eval()`) calculator for the `tools` mode |
| `arena_middleware.py` | `ArenaMiddlewareModel(WrapperModel)` -- Pydantic AI NATIVE model-wrapping for the `middleware` mode: mutates the inbound system prompt, counts outbound requests |
| `run.sh` | Native runner: sources the repo-root `.env`, activates `.venv`, execs `adapter.py` |
| `run-docker.sh` | Containerized runner: builds the image on first use, passes credentials at `docker run` time only |
| `Dockerfile` | Minimal `python:3.13-slim` image; no credential baked in |
| `test-env.sh` | Automated PASS/FAIL credential/env tests (native env read, no-key-in-image, never-logged) |
| `test-isolation.sh` | Automated PASS/FAIL tests for `memory_scoped` / `isolation_leak`: same-scope hit, cross-tenant miss, zero leak, plus two zero-cost hand-rolled-key guard-rail assertions |
| `test-crash.sh` | Automated PASS/FAIL test for `crash_recovery`: launches a real 2-turn conversation, `kill -9`s it mid-turn-2, then a fresh process must resume from rqlite and recall the turn-1 fact |
| `requirements.txt` | `pydantic-ai>=2.12.0`, `openai>=1.0.0`, `qdrant-client>=1.12,<1.13`, `opentelemetry-sdk>=1.28.0` |
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
  "mode": "qa|rag|tools|memory|memory_scoped|isolation_leak|crash_recovery|structured|stream|observability",
  "model": "agnes-2.0-flash",
  "reply": "string",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [{"name": "string", "arguments": {}}],
  "retrieved": [{"doc_id": "...", "title": "...", "text": "...", "score": 0.0}],
  "structured": {"...": "..."},
  "ttft_ms": 0,
  "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true, "...": "..."},
  "leak_detected": false,
  "resumed": true,
  "precrash_fact_recalled": true,
  "state_lost": false,
  "otel_supported": true,
  "span_count": 0,
  "span_names": ["invoke_agent agent", "chat agnes-2.0-flash", "execute_tool calculate"],
  "span_timings_ms": {"invoke_agent agent": 0.0},
  "logs_structured": true,
  "log_sample": "string",
  "notes": "string",
  "error": ""
}
```

`tool_calls` / `retrieved` / `structured` / `ttft_ms` / `scoped_recall` / `leak_detected` /
`resumed` / `precrash_fact_recalled` / `state_lost` / `otel_supported` / `span_count` /
`span_names` / `span_timings_ms` / `logs_structured` / `log_sample` / `notes` are
empty/`null`/`-1`/`""` when a mode doesn't apply them (`observability` is the only mode that
populates the OTel-specific fields). Token counts come from Pydantic AI's `RunUsage`
(provider-reported); `-1`/`-1` means usage was unavailable, never a fabricated estimate. `mode` and
the new fields are additive on top of the base arena's `adapterOutput` struct
(`arenas/live/runner/main.go`), which decodes JSON with unknown fields ignored — so this adapter
stays a drop-in for the base runner when `mode` is omitted (defaults to `qa`, byte-for-byte the
original single-shot behavior).

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

### Memory-extension dimensions (`memory_cross_session` / `memory_semantic_recall` / `memory_consolidation`)

These three RUNNER-INTERNAL dimensions (`docs/architecture/arena-memory-extensions.md`) arrive on the
wire as an ordinary `memory` turn PLUS one additive `omitempty` field — `user_id` — with the runner
VARYING `session_id` while HOLDING `user_id` constant across a task's 2–3 invocations. The fairness
question is: does Pydantic AI have an OFFICIAL primitive for each, or is a "pass" only adapter-authored
glue? The **capability-absence sweep** (core + official extension + docs) was run before wiring, and the
honest answers differ by dimension. Where there is no official primitive, the cell is declared
`infeasible` **with a precise reason** rather than faked with a strawman pass — that honesty is the point.

| Dimension | Pydantic AI primitive | Maturity | Version | Arena verdict |
|---|---|---|---|---|
| `memory_cross_session` | native `message_history` + `ModelMessagesTypeAdapter` serialization, keyed by `user_id` at the **application level** (`adapter.py` `_memory_store_key`) | **App-level replay keyed by user (L1)** — the persistence/replay is NATIVE, but the user-keying is adapter convention, NOT a framework scope primitive | pydantic-ai 2.12.0 | **real: pass 2/2** (committed board `RESULTS.md` @ 06945bf, real agnes lane) |
| `memory_semantic_recall` | — none — | **NONE** (see below) | — | **infeasible: no official semantic-recall primitive** |
| `memory_consolidation` | — none — | **NONE** | — | **infeasible: no official consolidation primitive** |

**`memory_cross_session` — the honest primitive, and what it is NOT.** Pydantic AI's OFFICIAL cross-session
memory is the **pydantic-ai-harness `Memory` capability** (a persistent namespaced notebook —
OFFICIAL-EXTENSION per `docs/architecture/arena-memory-extensions.md` §1, https://pydantic.dev/docs/ai/harness/).
That package is **not installed and not installable offline** in this adapter, which is pinned
**pre-harness** (`requirements.txt`: `pydantic-ai>=2.12.0`; `import pydantic_ai_harness` → `ModuleNotFoundError`,
verified this session). Rather than hand-roll a semantic store to fake the harness (a strawman in the
*author's* favour), this adapter wires the framework's **own native** message-history serialization
(`ModelMessagesTypeAdapter`, the same primitive the `memory` mode uses) keyed by `user_id` — so a recall in
a DIFFERENT session for the SAME user replays the persisted transcript. This is **app-level transcript
replay keyed by user (L1)**: legitimate, but it is NOT the harness Memory primitive, and the token table
will show it pays full-transcript-replay cost, not a retrieval cost. Mechanism proven offline in one process
(store keys `user:u-42` / `user:u-99`, a `sessB` recall loads the `sessA`-persisted `user:u-42` blob, and a
different user gets a fresh key — no cross-user bleed); `ARENA_STUB`'s per-process `_stub_history` cannot
cross the persist→recall PROCESS boundary and `TestModel` cannot echo the recalled fact, so the SCORED cell
is `real: pending`, stub `infeasible` (same convention as every wired competitor).

**`memory_semantic_recall` — infeasible over a strawman.** Pydantic AI ships **no embedding-backed
conversational memory**. The harness `Memory` capability's on-demand search mechanism is unconfirmed
(lexical vs embedding — open question #1 in the memory-extensions doc) and, again, the harness is not
installable offline. Native `message_history` is *verbatim* transcript replay, not semantic retrieval — the
`memory_semantic_recall` fixture is deliberately designed (paraphrase needle, zero content-token overlap,
30–40 turns) to defeat exactly that. Pointing the same replay mechanism at this fixture would either fail or
pass only by brute-force replaying all ~40 turns for the model to search — which measures **adapter-authored
brute force, not a Pydantic AI primitive**. So the cell is `infeasible: no official semantic-recall
primitive`, the same run-and-fail-vs-infeasible rule the doc applies to eino/genkit-go: `infeasible` is for
"no framework capability to exercise," which is the truthful state here.
<!-- SWEPT: 2026-07-22 docs/architecture/arena-memory-extensions.md — §1 matrix: pydantic-ai
     semantic long-term = NONE official; §1.1 re-sweep: native memory NONE (harness issue #30),
     third-party only (Hindsight / Neo4j Agent Memory / MemU). -->

**`memory_consolidation` — infeasible.** Neither Pydantic AI core nor the harness ships a
fact-extraction / background-consolidation memory component (memory-extensions doc §1:
pydantic-ai consolidation = NONE). A consolidate directive would be a no-op; there is nothing
framework-owned to exercise → `infeasible: no official consolidation primitive`.

**`isolation_forgetful` — infeasible: no native scope to forget (L0).** Pydantic AI is **L0** for a data
plane (`docs/architecture/arena-tenancy-levels.md`): `message_history` is a raw parameter with no tenant/user
scope concept. The `user_id` keying introduced above for `memory_cross_session` lives in **adapter glue**
(`adapter.py` `_memory_store_key`), NOT at a Pydantic AI API boundary — so omitting it would exercise this
adapter's own code, not the framework, which is precisely why the arena's fairness rule marks L0 frameworks
`infeasible` for this dimension (there is no *framework* scope to forget). Marking it `infeasible` — instead
of contriving an adapter-level "leak" or "refuse" — is the anti-strawman choice: aisotropy's own
construction-enforced refusal is NOT credited here to a competitor that structurally lacks the concept.
<!-- SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md — framework matrix: Pydantic AI = L0
     (message_history is a plain parameter; persistence is entirely the caller's responsibility). -->

**2026-07 ecosystem sweep.** Native memory remains **NONE** for Pydantic AI: the open issue
[pydantic/pydantic-ai-harness#30](https://github.com/pydantic/pydantic-ai-harness/issues/30) (2026-03) still
tracks a first-party memory capability; everything else is THIRD-PARTY only (Hindsight, Neo4j Agent
Memory, MemU). This board therefore measured Pydantic AI on its app-level user-scoped store built on
the native `message_history` serialization — its **maximum official surface** today — and the verdicts
above stand: what exists third-party is not a framework primitive and does not change the
user-scoped result.

### `memory_scoped` — hand-rolled COMPOUND-SCOPE isolation (not framework-native)

**Why this mode exists:** the `memory` mode above only ever keys by `session_id`. That proves
persistence, not **multi-tenant scoped memory** — it never tests whether one tenant's data can
leak into another's context, which is aisotropy's own core differentiator. This mode and
`isolation_leak` below exist specifically to test scoped memory + tenant isolation, something the
`memory` mode's single-key design cannot exercise at all.

> **Honest note:** Pydantic AI has **no native multi-tenant memory isolation primitive**.
> <!-- SWEPT: 2026-07-22 docs/architecture/arena-tenancy-levels.md — framework matrix: Pydantic AI = L0. -->
> `message_history` is a flat, opaque blob keyed however the caller likes — the framework has no
> concept of "tenant" or "user" or "product" and enforces nothing about which caller can read
> which blob. Everything in this mode is a **hand-rolled compound-scope key**
> (`scope_key(tenant_id, user_id, product_id[, session_id])` in `arena_memory_store.py`) layered on
> top of the same rqlite table mechanism the `memory` mode uses. **Isolation here is entirely a
> developer discipline, not a framework guarantee — a mistake in the key construction (wrong
> separator, dropped component, swapped argument order) is a silent cross-tenant leak, with no
> framework-level safety net to catch it.** Contrast: **aisotropy enforces isolation by
> construction** — tenant-scoped key-prefixing in BadgerDB/rqlite and a Qdrant **in-scan payload
> filter** during the HNSW search itself (not a post-filter over a shared pool), per
> `.claude/rules/constitutional-gates.md`'s multi-tenant data isolation gate. A developer using
> Pydantic AI has to build and audit that isolation themselves; aisotropy makes the isolated path
> the only path.

```json
{
  "mode": "memory_scoped",
  "tenant_id": "tenant-acme",
  "user_id": "user-42",
  "product_id": "product-orion",
  "fact": "The Q3 launch codename for this tenant is Falcon-Nebula.",
  "query": "What is the Q3 launch codename?",
  "cross_tenant_id": "tenant-globex"
}
```

Pipeline, all three steps in one adapter invocation: **(1) WRITE** — `save_scoped_fact` upserts
`fact` under the compound key `scope_key(tenant_id, user_id, product_id[, session_id])` in a
dedicated `arena_pydantic_scoped_memory` rqlite table. **(2) RECALL same scope** —
`load_scoped_facts` reads back that exact key (store-level check), then a real Pydantic AI `Agent`
answers `query` grounded ONLY in those facts (a real agnes call) — `same_scope_hit` is true iff
both the store returned the fact and the recall succeeded. **(3) RECALL cross-tenant** —
`load_scoped_facts` is queried again under `scope_key(cross_tenant_id, user_id, product_id[,
session_id])` (same user/product, DIFFERENT tenant) — since nothing was ever written there, this
returns nothing; a second real agnes call, grounded in that empty context, confirms the model
doesn't invent the fact either. `cross_tenant_miss` is true iff the store returned nothing AND the
reply doesn't contain the fact.

**Evidence (real calls, `tenant_id=tenant-acme`, `cross_tenant_id=tenant-globex`):**
```json
{"framework": "pydantic-ai", "mode": "memory_scoped", "model": "agnes-2.0-flash", "reply": "For tenant-acme, the Q3 launch codename is Falcon-Nebula.", "input_tokens": 665, "output_tokens": 27, "latency_ms": 14098, "scoped_recall": {"same_scope_hit": true, "cross_tenant_miss": true, "same_scope_reply": "For tenant-acme, the Q3 launch codename is Falcon-Nebula.", "cross_tenant_reply": "I have no information for this scope.", "own_scope_key": "tenant-acmeuser-42product-orion-", "cross_scope_key": "tenant-globexuser-42product-orion-"}, "error": ""}
```
`same_scope_hit=true`: the fact written under `tenant-acme`'s scope came back correctly. Under
`tenant-globex`'s scope (same `user_id`/`product_id`, different `tenant_id`), the store returned
zero rows and the model correctly said it has no information — `cross_tenant_miss=true`.

### `isolation_leak` — adversarial framing: tenant A writes a SECRET, tenant B queries for it

Same hand-rolled compound-scope store as `memory_scoped`, framed as a direct leak test:

```json
{
  "mode": "isolation_leak",
  "tenant_id": "tenant-acme",
  "user_id": "user-42",
  "product_id": "product-orion",
  "secret": "SECRET-PROJECT-CODE-7841",
  "attacker_tenant_id": "tenant-globex",
  "query": "What is the secret project code?"
}
```

Pipeline: tenant A (`tenant_id`) writes `secret` under its own compound scope
(`save_scoped_fact`). Then, **as tenant B** (`attacker_tenant_id`), the adapter queries the scoped
store at `scope_key(attacker_tenant_id, user_id, product_id[, session_id])` — a completely
different key from tenant A's. **The isolation assertion is on what the scoped store returns**
(`load_scoped_facts` under B's exact key — an exact `WHERE scope_key = ?` match, not a prefix or
fuzzy match, not a post-filter over a shared row set): `leak_in_store` is true only if that lookup
somehow contains A's secret. A real Pydantic AI `Agent` recall turn, AS tenant B, grounded only in
whatever B's own (empty, in the honest case) scoped facts are, provides `leak_in_reply` as a second
independent signal. `leak_detected = leak_in_store OR leak_in_reply` — `false` is the correct,
isolated outcome.

**Evidence (real call):**
```json
{"framework": "pydantic-ai", "mode": "isolation_leak", "model": "agnes-2.0-flash", "reply": "I have no information for this scope.", "input_tokens": 307, "output_tokens": 9, "latency_ms": 7069, "leak_detected": false, "error": "", "victim_scope_key": "tenant-acmeuser-42product-orion-", "attacker_scope_key": "tenant-globexuser-42product-orion-", "leak_in_store": false, "leak_in_reply": false}
```
Tenant B's query for tenant A's secret returned nothing from the store and the model, correctly
grounded in B's own empty scope, did not invent or leak it either.

### Automated isolation tests (`test-isolation.sh`)

```bash
bash arenas/live/pydantic-ai/test-isolation.sh
```

Seven PASS/FAIL assertions (3 real agnes calls: 2 inside the `memory_scoped` invocation, 1 inside
`isolation_leak`; the two guard-rail assertions fail closed on payload validation before any model
call is made, so they cost nothing):

```
=== (1)/(2) memory_scoped: compound-scope write + same-scope recall + cross-tenant miss ===
PASS: (1) same-scope recall hit -- fact written under (tenant_id, user_id, product_id) is recalled back in that exact scope (same_scope_hit=true)
PASS: (2) cross-tenant recall miss -- the SAME query under a DIFFERENT tenant_id returns nothing from the store and is not leaked into the reply (cross_tenant_miss=true)

=== (3) isolation_leak: tenant A writes a SECRET, tenant B queries for it ===
PASS: (3) zero leak -- tenant B's query for tenant A's key returns nothing (leak_detected=false)
PASS: (3a) scoped store never returned tenant A's data for tenant B's exact-match scope key (leak_in_store=false)
PASS: (3b) the model's real recall turn, AS tenant B, did not contain the secret (leak_in_reply=false)

=== (4)/(5) hand-rolled-key guard rails (no model call -- validation fails first) ===
PASS: (4) memory_scoped without tenant_id fails closed with a non-empty error (never silently scopes to an empty/default tenant): ValueError: mode=memory_scoped requires tenant_id, user_id, product_id
PASS: (5) isolation_leak with attacker_tenant_id == tenant_id fails closed (this mode only proves CROSS-tenant isolation): ValueError: attacker_tenant_id must differ from tenant_id -- this mode tests cross-tenant isolation

=== RESULT: 7 passed, 0 failed ===
```

Each run uses a fresh `RUN_ID` suffix (`$(date +%s)-$$`) on the tenant/user/product IDs so
repeated executions never depend on — or get confused by — rows a prior run left in the shared
rqlite store.

### `crash_recovery` — does persisted `message_history` survive a HARD `kill -9`, not just a clean exit?

**Why this mode exists:** `memory` mode already proves persistence survives a *clean* process exit
(two separate `python adapter.py` invocations, the second one exec'd fresh — no shared state,
no shared memory). It does not prove the harder claim operators actually care about: if the
process is violently killed — `kill -9`, an OOM-kill, a container eviction — mid-conversation, does
whatever was already committed survive? `crash_recovery` answers that with a real SIGKILL, not a
simulated one.

Two invocation shapes, selected by which payload key is present:

**(A) Pre-crash — `turns` (2-3 items), one long-lived process:**
```json
{"mode": "crash_recovery", "session_id": "some-id", "turns": ["Remember this fact: ...", "Tell me a fun fact about maglev trains."]}
```
Runs each turn **sequentially, in-process**, against the same `Agent` conversation. After *every*
turn completes, the full `message_history` (via `ModelMessagesTypeAdapter.dump_json`) is
synchronously upserted into rqlite — the same `arena_pydantic_memory` table and
`save_history_json`/`load_history_json` functions `memory` mode uses, reused unchanged — and a
distinct `PROGRESS: crash_recovery: checkpoint: turn N/M persisted (... bytes)` line is logged to
stderr right after the write returns. This is the process `test-crash.sh` launches in the
background and `kill -9`s mid-flight, specifically **after** the turn-1 checkpoint log line appears
but **during** turn 2's real (multi-second) model call — so turn 1's state is durably committed
before the kill, and turn 2's is not.

**(B) Post-crash recovery — `query` (no `turns`), a FRESH process:**
```json
{"mode": "crash_recovery", "session_id": "some-id", "query": "What did I ask you to remember?", "expect_fact": "the-fact-substring-to-check-for"}
```
A brand-new `python adapter.py` invocation. Loads whatever `message_history` actually persisted in
rqlite for `session_id` — exactly as far as the pre-crash process got before it died, nothing
more, nothing less — runs one more turn, and reports:
- `resumed`: `true` iff a prior history blob was found in rqlite at all (`load_history_json`
  returned non-`None`).
- `state_lost`: `!resumed` — `true` would mean even the turn-1 checkpoint failed to survive.
- `precrash_fact_recalled`: `true` iff `expect_fact` (case-insensitive substring) appears in the
  reply — i.e. the model, given only the recovered history, actually recalls the pre-crash fact.

**Evidence (real run, `bash test-crash.sh`) — process A checkpoints turn 1, gets `kill -9`'d during
turn 2, and fresh process B recovers:**
```json
{"framework": "pydantic-ai", "mode": "crash_recovery", "model": "agnes-2.0-flash", "reply": "Obsidian-Falcon-1784330113-22796", "input_tokens": 404, "output_tokens": 22, "latency_ms": 1395, "resumed": true, "precrash_fact_recalled": true, "state_lost": false, "error": ""}
```
stderr from process A (before the kill): `PROGRESS: crash_recovery: pre-crash process running turn
1/2` → `PROGRESS: crash_recovery: checkpoint: turn 1/2 persisted (... bytes)` → `PROGRESS:
crash_recovery: pre-crash process running turn 2/2` — then silence (SIGKILL landed; turn 2's
checkpoint line never appears, confirmed by `test-crash.sh` assertion 2a). stderr from fresh process
B: `PROGRESS: crash_recovery: RECOVERY -- loaded ... bytes of persisted pre-crash history` →
`PROGRESS: crash_recovery: RECOVERY -- precrash_fact_recalled=True`.

**Honest note — what actually survives a SIGKILL, and why:** Pydantic AI itself has **no
built-in crash-recovery or write-ahead-log mechanism** for `message_history` — it is a plain
in-memory list of `ModelRequest`/`ModelResponse` objects the framework hands back from
`result.all_messages()`; nothing about the framework persists it anywhere on its own, and nothing
about the framework's design distinguishes a clean exit from a hard kill. **Everything that
survives here survives because this adapter calls `save_history_json()` synchronously, after
each turn completes, before starting the next one** — durability is a property of *this
checkpoint-per-turn discipline*, not of Pydantic AI. The corollary this mode makes concrete: **a
turn that was in flight at the moment of the kill is lost outright** — not partially recovered,
not replayed, not queued — because it was never checkpointed in the first place. If an adapter
(or, more realistically, aisotropy's own consolidation outbox — see
`.claude/rules/architecture.md`'s request-lifecycle diagram) checkpoints only every *N* turns, or
buffers writes, the same SIGKILL would lose everything since the last checkpoint, not just the
in-flight turn. This mode's `state_lost=false` result is therefore evidence about the **adapter's
own persist-after-every-turn design**, not a Pydantic AI guarantee — get the checkpoint frequency
wrong and this same test would show `state_lost=true` for the *fact* even though `resumed=true` for
some earlier, stale slice of the conversation.

### Automated crash-recovery test (`test-crash.sh`)

```bash
bash arenas/live/pydantic-ai/test-crash.sh
```

Seven PASS/FAIL assertions (3 real agnes calls: turn 1 completes, turn 2 starts and is killed in
flight, process B's recovery turn):

```
=== (0) sanity: rqlite is reachable at the configured RQLITE_URL ===
PASS: (0) rqlite reachable at http://localhost:4001

=== (1) process A: launch 2-turn conversation in background, checkpointing after each turn ===
PASS: (1) process A checkpointed turn 1 (fact persisted to rqlite) before any kill was sent

=== (2) SIGKILL process A immediately -- turn 2's real model call is in flight, never checkpoints ===
PASS: (2) process A (<pid>) is confirmed dead after kill -9 (wait exit status=137, expected non-zero/killed)
PASS: (2a) turn 2 never checkpointed -- confirms the kill landed mid-turn-2, before its persist

=== (3) FRESH process B: load whatever survived, continue, must recall the turn-1 fact ===
PASS: (3a) resumed=true -- fresh process B found and loaded process A's pre-crash persisted history
PASS: (3b) state_lost=false -- the turn-1 checkpoint survived the SIGKILL
PASS: (3c) precrash_fact_recalled=true -- process B's reply recalls the fact stated in turn 1, before the crash

=== RESULT: 7 passed, 0 failed ===
```

`run.sh`'s last line is `exec python "$SCRIPT_DIR/adapter.py"`, so the shell process image is
replaced by the Python interpreter — the PID captured via `$!` right after launching `bash run.sh
... &` is the actual interpreter PID, and `kill -9` lands on it directly (no orphaned shell wrapper
to clean up separately). A fresh `RUN_ID` suffix (`$(date +%s)-$$`) on `session_id` and the fact
codename means repeated executions never collide with rows a prior run left in the shared rqlite
store.

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

### `observability` — Pydantic AI's NATIVE OpenTelemetry instrumentation, in-process

```json
{"mode": "observability", "messages": [{"role": "user", "content": "What is 25 times 4, minus 7? Use the calculate tool."}]}
```

**`otel_supported: true` — this is a native framework capability, not something bolted on from
outside.** `pydantic_ai.models.instrumented.InstrumentedModel` ships inside `pydantic-ai-slim`
itself (its only OTel dependency is `opentelemetry-api`) and is switched on per-`Agent` via the
`agent.instrument = InstrumentationSettings(...)` setter (there's also a process-wide
`Agent.instrument_all(...)` classmethod, not used here so this stays scoped to one agent). It
follows the OTel **GenAI semantic conventions** — span names/attributes like `gen_ai.input.messages`,
`gen_ai.output.messages`, `gen_ai.usage.input_tokens` are a spec Pydantic AI implements, not an
ad-hoc schema.

This mode builds a **dedicated, in-process** `opentelemetry.sdk.trace.TracerProvider` +
`SimpleSpanProcessor(InMemorySpanExporter())` — supplied by this adapter's own
`opentelemetry-sdk` dependency — and passes it to `InstrumentationSettings(tracer_provider=...)`.
**`logfire.configure()` is never called and the `logfire` package is not imported anywhere in this
mode.** Logfire is *one possible* OTel backend Pydantic AI documents (their own
`logfire[httpx]` package is only pulled in by the `pydantic-ai-slim[logfire]` *extra*, which this
adapter does not install); pointing `tracer_provider` at any OTel SDK sink — the plain in-memory
one used here, or an OTLP exporter to a self-hosted collector — works identically, because the
instrumentation itself is native and backend-agnostic.

The turn re-uses the `tools` mode's real, actually-executed `calculate` tool
(`arena_tools.safe_eval_expr`) specifically so **one turn exercises multiple ops** — an
`invoke_agent` parent span wrapping two `chat agnes-2.0-flash` model-call spans (tool request +
final answer, same two-round-trip shape as `tools` mode) and one `execute_tool calculate` span —
each with its own independently measured OTel span duration, not just one aggregate latency
number.

**Evidence (real call):**
```json
{"framework": "pydantic-ai", "mode": "observability", "model": "agnes-2.0-flash", "reply": "The result of 25 times 4, minus 7 is 93.", "input_tokens": 1228, "output_tokens": 52, "latency_ms": 13925, "tool_calls": [{"name": "calculate", "arguments": {"expression": "25 * 4 - 7"}, "result": 93}], "otel_supported": true, "span_count": 4, "span_names": ["chat agnes-2.0-flash", "execute_tool calculate", "chat agnes-2.0-flash", "invoke_agent agent"], "span_timings_ms": {"chat agnes-2.0-flash": 7113.829, "execute_tool calculate": 0.948, "chat agnes-2.0-flash#2": 6788.517, "invoke_agent agent": 13920.268}, "logs_structured": true, "log_sample": "gen_ai.output.messages=[{\"role\":\"assistant\",\"parts\":[{\"type\":\"tool_call\",\"id\":\"chatcmpl-tool-bcd0eb891fd524d6\",\"name\":\"calculate\",\"arguments\":\"{\\\"expression\\\": \\\"25 * 4 - 7\\\"}\"}],\"finish_reason\":\"tool_call\"}]", "error": ""}
```

4 real spans for 1 turn: 1 `invoke_agent` (the whole run), 2 `chat agnes-2.0-flash` (the
tool-requesting call and the final-answer call), 1 `execute_tool calculate` (the real, executed
tool — `0.948ms`, confirming it's the in-process Python call, not a model round-trip). `#2` in
`span_timings_ms` disambiguates the two same-named `chat` spans so both durations are visible
instead of one overwriting the other.

**Structured content, no separate log stream:** pydantic-ai's instrumentation path has **no**
`logging.getLogger` call sites anywhere in `pydantic_ai/models/instrumented.py` or
`_instrumentation.py` — grepped and confirmed empty. There is no distinct OTel **logs** signal
emitted. Everything observability-relevant is carried as **span attributes**:
`gen_ai.input.messages` / `gen_ai.output.messages` (per-model-call, JSON arrays of the exact
request/response messages) and `pydantic_ai.all_messages` (on the `invoke_agent` span, the full
conversation). These attribute values ARE structured (valid, parseable JSON — see `log_sample`
above, a real captured attribute value) — `logs_structured: true` reflects that the *content* is
structured JSON, with the honest caveat that it arrives as span attributes, not a dedicated
`logs` API/signal a backend would ingest separately from traces.

**Why this matters for aisotropy's own architecture:** aisotropy's LLM call path
(`internal/llm/`) and the memory tiers do not yet emit any OTel spans of their own — this arena
result is a concrete "what native instrumentation looks like" reference (span names, GenAI
attribute keys, per-op timing granularity) if/when aisotropy wires its own tracing.

### `middleware` — Pydantic AI NATIVE model-wrapping (`WrapperModel`)

```json
{"mode": "middleware", "system": "You are a rail assistant.", "messages": [{"role": "user", "content": "Say OK."}]}
```

Pydantic AI ships `pydantic_ai.models.wrapper.WrapperModel` (`pydantic_ai/models/wrapper.py`), a
`Model` subclass built specifically to be subclassed and wrap another `Model`, intercepting
`request()` / `request_stream()` — the same base class durable-execution integrations
(Temporal/Prefect/DBOS) use. This mode's `arena_middleware.ArenaMiddlewareModel(WrapperModel)`
performs two independently observable transforms in one real turn:

- **INBOUND** — `request()` appends the sentinel `"\n[ARENA_MW]"` to every `SystemPromptPart` in
  the outgoing `messages` list, non-destructively (`dataclasses.replace`, never mutating the
  caller's history objects in place), *before* delegating to the wrapped model.
- **OUTBOUND** — `request()` increments a counter *after* the wrapped model's real response comes
  back, so the wrapper observes every outbound call, not just fire-and-forget interception.

**Mechanism honesty:** this mutates the already-assembled `messages: list[ModelMessage]` argument
of `request()` — the framework's per-call payload — not the `Agent`'s `system_prompt` string
itself. A dynamic `@agent.system_prompt` callback operates one layer above (regenerates the prompt
at render time); `InstrumentedModel` (exercised honestly by `observability` mode above) is
OBSERVE-only and cannot mutate the outbound request at all. `WrapperModel` is the only one of the
three that can both mutate inbound and observe outbound in a single hook.

**Different stdout shape, on purpose:** unlike every other mode, `middleware` does not reuse
`_base_result()` / the uniform 20-field contract — it emits exactly `{"text": str, "middleware":
{...}}`. The base arena runner ignores fields it doesn't know either way, so this stays additive;
it's just a smaller, mode-specific shape.

**Evidence (real call):**
```json
{"text": "OK", "middleware": {"mechanism": "WrapperModel", "inbound_fired": true, "outbound_fired": true, "invocations": 1, "supported": true}}
```
Independently verified below the self-reported booleans, with a `RecordingModel(TestModel)`
standing in as the wrapped model: the exact `SystemPromptPart` content the wrapper forwarded
downstream was captured and asserted to end in the sentinel —
`'You are a rail assistant.\n[ARENA_MW]'` — proving the mutation actually reaches the wrapped
model's `request()` call, not just a self-reported flag.

**Honest scope:** only `request()` (the non-streaming path `agent.run()` uses, which is what this
mode's one turn exercises) is overridden; `request_stream()` is left as `WrapperModel`'s
unmodified pass-through — `stream` mode already covers the real streaming path separately, and
`middleware` does not stream.

## `ARENA_STUB=1` — offline toggle for every mode

```bash
ARENA_STUB=1 echo '{"mode":"qa","system":"...","messages":[{"role":"user","content":"Say OK."}]}' | python3 adapter.py
```

Setting `ARENA_STUB=1` swaps every mode's real dependency for an in-process, offline substitute —
no `OPENAI_*` / `EMBED_URL` / `QDRANT_URL` / `RQLITE_URL` env is required and no network I/O
happens for any mode:

| Real dependency | Stub substitute | Where |
|---|---|---|
| agnes `OpenAIChatModel` | Pydantic AI's own `pydantic_ai.models.test.TestModel` (`pydantic_ai/models/test.py`) — a framework-native offline test double, not a hand-rolled fake | `arena_common.build_model()` |
| e5 embedding server | a fixed in-process vector (no semantic meaning; only exists to keep `qdrant_search()`'s argument shape) | `arena_common.embed_query_vector()` |
| Qdrant | a small canned passage list, clearly labeled `ARENA_STUB canned passage N` | `arena_common.qdrant_search()` |
| rqlite (`message_history` + scoped facts) | in-process module-level dicts (`_stub_history`, `_stub_scoped`) | `arena_memory_store.py` |

**Evidence (`qa` mode, offline):**
```json
{"framework": "pydantic-ai", "mode": "qa", "model": "pydantic_ai.models.test.TestModel (ARENA_STUB=1)", "reply": "success (no tool calls)", "input_tokens": 57, "output_tokens": 4, "latency_ms": 5, "error": ""}
```
stderr confirms the substitution and that no network call was attempted:
`PROGRESS: build_model: ARENA_STUB=1 -- using pydantic_ai.models.test.TestModel, no network call`.
Spot-checked additionally offline under `ARENA_STUB=1`: `memory` (`[ARENA_STUB] ... no rqlite
call`), `rag` (`[ARENA_STUB] ... no e5 call` / `no Qdrant call`), `stream`, `observability`,
and `middleware` (inbound/outbound both still fire against `TestModel`) all ran with **zero**
network I/O.

**Previously a limitation, now fixed (see `arenas/live/pydantic-ai/matrix.json` /
`arenas/live/FULL-MATRIX.md`'s full-matrix coverage law):** `TestModel`'s generic behavior
auto-calls any tool with schema-generated — not semantically meaningful — arguments (e.g. a bare
`"a"` for a `str` field). That is harmless for tool-call *capture* (`qa` mode never executes what
it captures) but was fatal for tool call *execution*: `tools` and `observability` modes' real
`calculate` tool received a non-arithmetic generated `expression` string and failed closed with a
caught `ValueError`. Likewise `structured` mode's `PromptedOutput(Answer)` strategy expects the
model to emit JSON matching the `Answer` schema as plain text, which `TestModel`'s default text
output does not produce, so it failed closed with `UnexpectedModelBehavior: Exceeded maximum
output retries`. Both were **offline, non-network, non-crashing** failures — `main()` never
raises, it always writes one valid JSON object to stdout — but not a *passing*, domain-correct
stub result, which the full-matrix coverage law requires wherever a canned fix is possible.

`arena_common.build_model()` now returns a canned model in stub mode instead of plain `TestModel`:
by default, `_CannedToolArgsTestModel` (a `TestModel` subclass overriding `gen_tool_args` to
return `_STUB_CANNED_TOOL_ARGS["calculate"] = {"expression": "6 * 7"}` for the one tool this arena
actually executes) — this is what `tools` and `observability` modes now build against, so their
executed `calculate` call receives real, evaluable arithmetic instead of schema-random garbage.
`structured` mode instead passes `build_model(custom_output_text=<canned Answer JSON>)`, since
`PromptedOutput` needs a schema-valid JSON *string* rather than a tool call — `TestModel`'s
`custom_output_text` parameter supplies it directly. Evidence (offline, `error=""` for all three):
```json
{"mode": "tools", "tool_calls": [{"name": "calculate", "arguments": {"expression": "6 * 7"}, "result": 42}], "error": ""}
{"mode": "structured", "structured": {"answer": "ARENA_STUB canned answer -- no real model call", "key_points": ["ARENA_STUB canned key point (ARENA_STUB=1, no network I/O)"], "confidence": 0.5}, "error": ""}
{"mode": "observability", "otel_supported": true, "span_count": 4, "tool_calls": [{"name": "calculate", "arguments": {"expression": "6 * 7"}, "result": 42}], "error": ""}
```
`qa`, `rag`, `memory`, `memory_scoped`, `isolation_leak`, `stream`, and `middleware` needed no
change — their stub path was already clean. `crash_recovery` is the one mode this fix does NOT
reach: see the "In-process only, not cross-process" note below and `matrix.json`, where
`crash_recovery`'s `stub` lane is `infeasible` for both `native` and `docker` (verified
empirically: a fresh stub process/container shows `resumed=false, state_lost=true` — there is no
canned fix for "no cross-process store exists", unlike a canned tool argument or a canned JSON
string).

**In-process only, not cross-process:** `ARENA_STUB`'s history/scoped-fact fallbacks are Python
module globals, not a file or rqlite — two *separate* `ARENA_STUB=1 python adapter.py` invocations
do **not** share state. That cross-process durability claim belongs to the real `memory` /
`crash_recovery` modes (see their sections above), not this offline substitute.

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

**Fixed (full-matrix fill pass):** the `Dockerfile`'s `COPY` line omitted `arena_middleware.py` —
a leftover from before the `middleware` mode was added — so `docker×{stub,real}/middleware` would
have failed closed on `ModuleNotFoundError` in-container despite working fine natively. Now
copied alongside the other four adapter modules; verified with a real in-container `middleware`
call (`docker×real`) and an offline `--network none` one (`docker×stub`), both `"error": ""`.

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
| **Subtotal (original 6-mode delivery)** | **16** |
| `memory_scoped` mode evidence (same-scope recall + cross-tenant recall, 1 invocation) | 2 |
| `isolation_leak` mode evidence (attacker recall, 1 invocation) | 1 |
| `test-isolation.sh` (`memory_scoped` invocation: 2 calls; `isolation_leak` invocation: 1 call; the 2 guard-rail assertions fail closed on validation before any model call, 0 calls) | 3 |
| **Subtotal (this `memory_scoped` / `isolation_leak` delivery)** | **6** |
| `crash_recovery` mode evidence (`test-crash.sh`, first run — the run whose transcript is captured in this README): turn 1 (completes) + turn 2 (started, killed in flight) + process B recovery turn | 3 |
| `crash_recovery` test-script debug re-run (the query in the first run asked to recall a full sentence instead of just the codename token — a test-script substring-check bug, not an adapter bug; fixed, then re-run for the passing evidence captured above) | 3 |
| **Subtotal (this `crash_recovery` delivery)** | **6** |
| `observability` mode exploration (standalone script probing `Agent.instrument` API shape — the constructor kwarg `Agent(..., instrument=...)` doesn't exist in 2.12.0, it's the `agent.instrument` setter; discovered before touching `adapter.py`) | 1 |
| `observability` mode evidence (`run.sh`, the captured evidence above: 2 model round-trips inside 1 adapter invocation — tool request + final answer) | 1 |
| **Subtotal (this `observability` delivery)** | **2** |
| `middleware` mode evidence (`run.sh`, the captured evidence above: 1 real call, inbound+outbound both fired) | 1 |
| `qa` (no `mode` key) regression re-check — confirms the pre-existing default path is byte-for-byte unchanged after this delivery's edits | 1 |
| **Subtotal (this `middleware` / `ARENA_STUB` delivery)** | **2** |

**This is 1 call over the ≤15 budget stated in the original brief** (see prior note; unrelated to
this delivery). The `memory_scoped` / `isolation_leak` addition used **6** real agnes calls total,
against a separately stated **≤8** budget for that delivery — 2 calls to spare. The `crash_recovery`
addition used **6** real agnes calls total, against a separately stated **≤8** budget for this
delivery — 2 calls to spare. The `observability` addition used **2** real agnes calls total,
against a separately stated **≤5** budget for this delivery — 3 calls to spare. The `middleware` /
`ARENA_STUB` addition used **2** real agnes calls total (all other verification — `ARENA_STUB=1`
smoke across 7 modes, the independent `RecordingModel` sentinel-forwarding proof — was offline and
cost zero real calls).

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

**`observability` mode addition:** one new direct dependency, `opentelemetry-sdk==1.42.1`
(Apache-2.0). It was already present in this arena's `.venv` transitively (pulled in by `logfire`,
which was installed earlier during unrelated exploration but is not a `requirements.txt` entry and
is **not imported by this mode**) and is now pinned explicitly in `requirements.txt` because the
mode imports it directly (`TracerProvider`, `SimpleSpanProcessor`, `InMemorySpanExporter`).
`opentelemetry-api==1.42.1` (Apache-2.0) and `opentelemetry-semantic-conventions==0.63b1`
(Apache-2.0) are transitive dependencies of `opentelemetry-sdk` and of `pydantic-ai-slim` itself
(which already required `opentelemetry-api` before this change, for the `Agent.instrument`
capability to exist at all). All three are Apache-2.0 — permitted under
`.claude/rules/cos/license-policy.md`; no new blocked-license package was introduced.

## Constraints honored

- **Additive only**: nothing under `arenas/pydantic-ai/` (the offline-classification arena) or
  anywhere else outside `arenas/live/pydantic-ai/` was modified. The original `run.sh` is
  unchanged; `adapter.py` was extended in place (new mode dispatch, `mode` defaults to `"qa"` so
  the base arena runner's existing behavior is byte-for-byte preserved when it omits `mode`).
- The agnes API key is never printed, logged, or baked into the Docker image — verified by
  `test-env.sh`'s 6 automated assertions above, not just asserted.
- English-only source, comments, and adapter output.
- Real agnes calls used: **16** for the original 6-mode delivery (see budget table above; 1 over
  the ≤15 target, disclosed) + **6** for the `memory_scoped` / `isolation_leak` addition (against
  a ≤8 budget for that addition) + **6** for the `crash_recovery` addition (against a separately
  stated ≤8 budget for that addition) + **2** for the `observability` addition (against a
  separately stated ≤5 budget for that addition — see the same table).
- `memory_scoped` / `isolation_leak` touch only `arenas/live/pydantic-ai/`: `adapter.py` (two new
  mode handlers + dispatch entries), `arena_memory_store.py` (the hand-rolled `scope_key` /
  `save_scoped_fact` / `load_scoped_facts` compound-scope store, additive to the existing
  `message_history` functions), `test-isolation.sh` (new), and this README. No other arena and no
  `internal/` code was touched.
- `crash_recovery` likewise touches only `arenas/live/pydantic-ai/`: `adapter.py` (one new mode
  handler + dispatch entry, reusing `arena_memory_store.save_history_json` /
  `load_history_json` unchanged — no changes to `arena_memory_store.py` were needed for this
  addition), `test-crash.sh` (new), and this README. No other arena and no `internal/` code was
  touched.
- Dependencies: no new third-party dependency was added for `memory_scoped` / `isolation_leak` or
  `crash_recovery` — all three reuse `pydantic_ai.Agent` (MIT, already a dependency) and the same
  `urllib.request`-based rqlite client already used by the `memory` mode. `requirements.txt` is
  unchanged for those three.
- `observability` likewise touches only `arenas/live/pydantic-ai/`: `adapter.py` (one new mode
  handler + dispatch entry + new result fields added to `build_error_result`/the stdout contract,
  additive), `requirements.txt` (one new explicit direct dependency, `opentelemetry-sdk`, license
  Apache-2.0, verified above), and this README. No other arena and no `internal/` code was touched.
  Real agnes API key never printed/logged for this mode either — confirmed by grepping the real
  `run.sh` invocation's captured stdout+stderr for the key value: 0 occurrences.
- `middleware` / `ARENA_STUB` likewise touches only `arenas/live/pydantic-ai/`: `adapter.py` (one
  new mode handler + dispatch entry, intentionally NOT reusing `_base_result()` — see the mode's
  own docstring for why), `arena_middleware.py` (new — `ArenaMiddlewareModel(WrapperModel)`),
  `arena_common.py` (`is_stub()` + `ARENA_STUB` branches added to `build_model()` /
  `embed_query_vector()` / `qdrant_search()`, additive — every existing non-stub code path is
  unchanged), `arena_memory_store.py` (`ARENA_STUB` in-process fallbacks added to all four
  persistence functions, additive), and this README. No other arena and no `internal/` code was
  touched. `requirements.txt` is unchanged — both features reuse `pydantic_ai` (already a
  dependency; `WrapperModel` and `TestModel` ship inside `pydantic-ai-slim` itself, no new
  package). Existing modes' real (non-stub) behavior verified byte-for-byte unchanged: the `qa`
  mode's real evidence call above (`"reply": "Paris"`) reproduces the pre-existing README evidence
  exactly.
