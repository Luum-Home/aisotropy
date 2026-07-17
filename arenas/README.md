---
type: Survey
title: "Adversarial isolation matrix — 12 arenas (Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta, Dify, Cloudflare Agents, Spring AI, Flue, eve) + adjacent sandcastle"
description: A fair, two-sided comparison. aisotropy's narrower thesis — tenant isolation BY DEFAULT via an unforgeable 4-axis Scope wired from context.Context — is ONE axis where it differs. The cross-scope read each arena's smoke exercises is a same-key/wrong-key developer bug (true of any KV store, including aisotropy's own BadgerDB), reproduced against each competitor's weakest config; a competent dev avoids it with the framework's proper primitive, shown here at equal weight. Every arena also carries a run-verified "what this framework is genuinely great at" section. 12 arenas across 5 languages, plus an honest adjacent note on sandcastle.
tags: [arenas, isolation, adversarial, steelman, pydantic-ai, mastra, langgraph, eino, adk-go, genkit-go, letta, dify, cloudflare-agents, spring-ai, flue, eve, competitive]
timestamp: 2026-07-15T00:00:00Z
---

# Arenas — Adversarial isolation comparison

> **Goal:** A fair, two-sided comparison. aisotropy's real differentiator is *narrow*: tenant
> isolation is enforced **by default** via an unforgeable 4-axis `Scope`
> (`tenant/user/session/product`) wired from `context.Context`, so no request-path code can issue an
> unscoped read. The cross-scope "leak" each arena's smoke exercises is a **same-key / wrong-key
> developer bug** — the property of *any* KV store with no auth layer, including aisotropy's own
> BadgerDB — reproduced against each competitor's *weakest* config; a competent developer avoids it
> with the framework's proper primitive, and the correct-config path is shown here at equal weight.
> What actually differs is **convention vs. construction**: whether isolation is on-by-default and
> unforgeable, not whether a framework *can* be made secure. Each arena also documents, run-verified,
> **what that framework is genuinely great at** — the axes where aisotropy does not compete.
> **Coverage:** **12 frameworks across 5 languages** (Python, TypeScript, Go, Java/JVM, and
> TypeScript-on-Workers) vs. the aisotropy runtime — the original 7 (Pydantic AI, Mastra, LangGraph,
> Eino, ADK Go, Genkit Go, Letta) plus 5 new entrants (Dify, Cloudflare Agents, Spring AI, Flue, eve).
> An adjacent, out-of-category tool — **sandcastle** — is noted honestly at the end (it orchestrates
> *coding* agents in sandboxes, not multi-tenant conversational runtimes). Of the 5 new entrants, 4
> ran as REAL offline integrations; **Dify is SOURCE-VERIFIED, NOT EXECUTED** (a multi-container
> server platform with no in-process library). Full 43-row capability matrix for all 12 in
> [`ARENA-SPEC-v2.md`](ARENA-SPEC-v2.md).

> **Full 41-feature comparison:** the 11-point isolation matrix below is the adversarial core;
> for the complete, doc-grounded and run-verified capability comparison across **all 41 aisotropy
> features** (memory tiers, contract, tools, isolation, governance, observability, consolidation,
> sovereignty) — with 25 rows executed offline against each pinned upstream framework and a crisp
> FINAL VERDICT — see [**`ARENA-SPEC-v2.md`**](ARENA-SPEC-v2.md).

> **Companion analyses.** Cost governance & observability across the arenas:
> [`COST-OBS.md`](COST-OBS.md). Framework-overhead & cost-governance-effectiveness benchmarks (model
> held constant as the keyless stub, so measured time is the *framework's* overhead — plus a
> token-counting proof that the pre-spend quota gate and response cache actually cut spend, and the
> reproducible cross-framework method): [`BENCHMARKS.md`](BENCHMARKS.md), runnable via
> [`scripts/run-overhead-bench.sh`](scripts/run-overhead-bench.sh).

## Feature-coverage matrix

Rows are the 11-point Orbit checklist. Each cell is a **coverage class** + terse note.

**Coverage classes:** `BUILT-IN` (ships, works out of the box) · `CONVENTION` (framework gives a
primitive but scoping/correctness is the developer's responsibility) · `PARTIAL` (present but
incomplete or backend-tethered) · `DIY` (you build it yourself / BYO) · `PAID` (only via a
paid/proprietary product) · `NOT-POSSIBLE` (framework cannot provide it by construction).

| # | Feature | Pydantic AI (Py) | Mastra (TS) | LangGraph (Py) | Eino (Go) | ADK Go (Go) | Genkit Go (Go) | Letta (Py) | **aisotropy (Go+Rust)** |
|---|---|---|---|---|---|---|---|---|---|
| 1 | **Multi-tenant isolation** | DIY / out-of-scope — stateless lib, no memory tier by design (not a defect) | CONVENTION — `resourceId`/`threadId` + 2 more levers (isolable by a competent dev) | CONVENTION — deep namespaces + `@auth.on.store()` identity binding (naive shallow prefix is a dev bug) | **NOT-POSSIBLE** — no tenancy tier by design (orchestration lib, like Pydantic AI); footgun ≠ bug | CONVENTION — composite `(app,user,session)`; `app_name` native non-droppable slot + auth callbacks | CONVENTION — experimental `ai/exp` `SessionStore` (session-keyed, tenant-unaware) | CONVENTION org-wide / **BUILT-IN by `agent_id`** (one-agent-per-principal isolates by construction) | **BY CONSTRUCTION** — ctx + per-statement/key scoping (isolation *by default*) |
| 1b | **Multi-product isolation** (4th scope axis, run-verified ▶) | DIY / out-of-scope — `product_id` is one more dev-computed key, no memory tier | CONVENTION — competent dev isolates via composite `resourceId` or `semanticRecall.filter`; naive tag path is a no-op | CONVENTION — deep namespace + `@auth.on.store()` prevents the cross-read; shallow prefix is a dev bug | CONVENTION — correct key isolates; a dropped segment collapses two products (same as any string-keyed KV) | **PARTIAL** — `app_name = product` (one runner/product) makes it native + non-droppable; one residual axis rides `userID` | CONVENTION — experimental `SessionStore` is session-keyed; product folded in by dev | CONVENTION org-wide / **BUILT-IN by `agent_id`** — competent dev avoids 3 of 4 old "leaks" | **BY CONSTRUCTION** — `product_id` rides ctx in `Scope` like tenant/user |
| 2 | Structured output + retry | BUILT-IN — fail-closed `raise ModelRetry` | BUILT-IN — AI-SDK v5 | BUILT-IN — `OutputFixingParser` | PARTIAL — parser only, no repair loop | BUILT-IN — `OutputSchema` node validation | BUILT-IN — `GenerateData[T]`, no repair loop | BUILT-IN — `response_format` json_schema, no repair loop | BUILT-IN — Gollem reflection (fail-closed) |
| 3 | Tools | BUILT-IN — function tools | BUILT-IN — native tool calling | BUILT-IN — graph nodes | BUILT-IN — `InferTool` + ReAct | BUILT-IN — `tool.Tool` (+MCP/A2A native) | BUILT-IN — `DefineTool` | BUILT-IN — LLM tool calls | BUILT-IN — `WithTypedTool[T]` (scope-gated) |
| 4 | Streaming (SSE) | BUILT-IN — `AsyncAgent.stream` | BUILT-IN — `ai_sdk` | BUILT-IN — callback handler | BUILT-IN — `StreamReader` | BUILT-IN — `StreamingModeSSE` | BUILT-IN — `WithStreaming` | BUILT-IN — `stream=True` | BUILT-IN — Huma SSE + `ctx` cancel |
| 5 | Working memory | DIY — app dict / Redis | BUILT-IN — resource-scoped | BUILT-IN — checkpointers + Store | CONVENTION — checkpoint keyed by bare string | BUILT-IN — Session backends (in-mem/sqlite/vertex) | DIY — build store; `genkitx-temporal` 3rd-party | BUILT-IN — Core Memory `Block`s | BUILT-IN — BadgerDB (µs) |
| 6 | Semantic memory (vectors) | DIY — BYO Chroma/Pinecone | BUILT-IN — vector recall | BUILT-IN — Store + semantic cache | DIY — #995 proposed | PARTIAL — Vertex MemoryBank (GCP-managed) | DIY — BYO retriever plugin | BUILT-IN — Archival `Passage`s | BUILT-IN — in-process cosine (active); Qdrant code-only |
| 7 | Facts memory (journal) | DIY | DIY | DIY | DIY | DIY | DIY | PARTIAL — Recall Memory | BUILT-IN — SQLite+FTS5 |
| 8 | Cost governance | pre-spend gate BUILT-IN (`UsageLimits`, per-**run**); per-tenant DIY | DIY — usage BUILT-IN, tenant ledger + gate DIY | DIY — `usage_metadata` BUILT-IN, per-tenant + gate DIY | DIY — `ResponseMeta.Usage` BUILT-IN, no tenant, no gate | DIY — `UsageMetadata` BUILT-IN, no tenant, no gate | DIY — `resp.Usage` BUILT-IN, no tenant, no gate | DIY — `usage` visibility BUILT-IN, meter + gate DIY | **BUILT-IN — per-TENANT pre-spend gate (BadgerDB token-bucket→`429`, by construction) + `costmeter` per-tenant meter** |
| 9 | Observability | BUILT-IN — OTel `gen_ai` spans offline; `tenant_id` DIY; Logfire PAID | BUILT-IN — `@mastra/observability` offline; `tenant_id` DIY; UI PAID | BUILT-IN spans offline (own exporter); `tenant_id` DIY; **dashboard PAID** (LangSmith) | BUILT-IN span/timing (callbacks); no native `gen_ai` emitter; `tenant_id` DIY; eino-ext PAID | BUILT-IN — OTel `gen_ai` semconv (13 spans, no config); `tenant.id` DIY | BUILT-IN OTel transport, `genkit:*` schema (no `gen_ai.*`); `tenant_id` DIY | BUILT-IN but SERVER-side (native trace = ClickHouse, NOT-POSSIBLE offline); client span DIY; dashboards PAID | **BUILT-IN — OTel `gen_ai` semconv with `tenant_id`/`user_id` NATIVE on the serving span** |
| 10 | Sovereignty / licensing | MIT | Apache 2.0 | OSS MIT + platform SaaS-gated | Apache 2.0 (ByteDance; APMPlus caveat) | Apache 2.0 (Google; Vertex gradient) | Apache 2.0 (Google) | Apache 2.0 (self-host server) | 100% MIT/Apache, fully self-hostable |
| 10b | **Dependency license posture** (real scan, run-verified ▶) | PERMISSIVE 51/53; +2 weak-copyleft MPL leaf (certifi, tqdm) = CAUTION; 0 strong-copyleft, 0 restricted | PERMISSIVE 276/277; 0 copyleft, 0 restricted | PERMISSIVE 51/53; +2 MPL (certifi, orjson-dual) = CAUTION; 0 strong-copyleft, 0 restricted | PERMISSIVE 24/24 linked; 0 copyleft, 0 restricted | PERMISSIVE 35/35 modules; 0 copyleft, 0 restricted | PERMISSIVE 24/24; 0 copyleft, 0 restricted | PERMISSIVE 21/22 (client tree); +1 MPL (certifi) = CAUTION; 0 strong-copyleft, 0 restricted | **100% MIT/Apache** — constitutional gate; no copyleft/BUSL without ADR |
| 11 | Language | Python | TypeScript | Python | Go | Go | Go | Python | Go + Rust |

**One correction vs. the earlier draft matrix:** ADK Go row 1 previously read "None (untyped
`map[string]any`)". That was factually wrong about the real framework. ADK Go's `session.Service`
*does* key by a composite `(appName, userID, sessionID)` — real construction. The accurate gap is
that it binds **no tenant** and does not bind identity to an authenticated caller, so it is
`CONVENTION`, not `NOT-POSSIBLE`. Corrected above and in the isolation-model table.

**Rows 1b and 10b are run-verified (▶).** Each arena's product-isolation demo and license scan were
*executed offline* (fake model / real installed dependency tree), all exit 0; per-arena evidence and
pasted tool output live in each arena's `FEATURES.md`. Row 1b closes the multi-product gap: the
matrix originally tested only tenant/user, and aisotropy scopes by four axes
`(tenant_id, user_id, session_id, product_id)`. Row 10b scans the real dep tree with
`pip-licenses`/`license-checker`/`go-licenses`.

## The 5 new entrants (v2 expansion)

Five frameworks are folded into the comparison beyond the original seven. Four ran as **REAL**
offline integrations against the pinned upstream framework; **Dify** is **SOURCE-VERIFIED, NOT
EXECUTED** (it is a Flask+Celery+Postgres+Redis multi-container platform with no in-process library —
cloned at HEAD `1aea3460`, read line-by-line, its real schema transcribed into its own ORM for a
labelled behavioral smoke). The isolation-axis summary (full 43-row matrix in
[`ARENA-SPEC-v2.md`](ARENA-SPEC-v2.md)):

| # | Feature | Dify (Py, src) | Cloudflare Agents (TS/Workers, ▶) | Spring AI (JVM, ▶) | Flue (TS, ▶) | eve (TS, ▶) | **aisotropy** |
|---|---|---|---|---|---|---|---|
| 1 | **Multi-tenant isolation** | CONVENTION — shared-schema `tenant_id` discriminator, hand-written `.where()`, **no RLS**; `Conversation` has no `tenant_id` | **BUILT-IN storage boundary by construction** (one DO = one separate SQLite; cross-object read unrepresentable) — *but the name-mapping is CONVENTION and the runtime is RENTED* | CONVENTION — opaque `conversationId` bound to nothing; `findConversationIds()` returns every tenant's ids | CONVENTION — one opaque instance-`id`; per-instance stream isolated by-construction, tenant/user/product folded in; Flue authenticates nothing | CONVENTION — **no tenant/user/product concept**; session-scoped; per-(t,u,p) memory is a dev-built key | **BY CONSTRUCTION** — ctx + per-statement/key scoping |
| 1b | **Multi-product isolation** (4th axis, ▶) | CONVENTION — **no `product_id` column exists**; "product" = `app_id` convention or unbound tag (leak run-reproduced, src ORM) | CONVENTION — drop the product name segment → two products share ONE SQLite (leak proven in workerd) | CONVENTION — cross-product leaks incl. a **delimiter-injection collision** on the flat key | CONVENTION — cross-product leak one token apart (routed billing convo to analytics id) | CONVENTION — leaky key `${t}:${u}` returns wrong product's secret (`proven_cross_product_leaks=1`) | **BY CONSTRUCTION** — `product_id` rides ctx in `Scope` |
| 5 | Working memory | BUILT-IN (src) — SQL conversation/message history (server SQL, not µs) | BUILT-IN — in-process per-DO SQLite (hydrated:2 ▶) | BUILT-IN — `MessageWindowChatMemory` (▶) | PARTIAL — durable canonical conversation stream (crash-safe ▶) | BUILT-IN — session-scoped KV + history (▶) | BUILT-IN — BadgerDB (µs) |
| 6 | Semantic memory (vectors) | BUILT-IN (src) — per-dataset vector store | DIY — Vectorize/AutoRAG rented, not in-tree | PARTIAL — `VectorStore` (separate module, src) | DIY — no vector/RAG tier | DIY — no vector tier | BUILT-IN — in-process cosine; Qdrant code-only |
| 7 | Facts memory (journal) | DIY (src) — no FTS facts journal | DIY — FTS5 present in DO SQLite, journal is BYO | DIY — no FTS/facts tier | DIY — durable db is not a facts journal | DIY — no FTS tier | BUILT-IN — SQLite+FTS5 |
| — | Memory-tier hierarchy (row 1.1) | PARTIAL (src) — 2 surfaces, no FTS/batch | PARTIAL — one per-DO SQLite, not 4 tiers | PARTIAL — window memory + `VectorStore`, no FTS/batch | **NOT-POSSIBLE** — durable transcript only | **NOT-POSSIBLE** — one session KV, no vector/FTS/batch | BUILT-IN — 4 stores of different nature |
| 7.1 | Transactional outbox (atomic msg+intent) | **NOT-POSSIBLE** (src) — Celery decoupled from DB writes | PARTIAL — single DO's transactional SQLite (in-object) | PARTIAL — buildable in-process (`@Transactional` + shared JDBC) | PARTIAL — `dispatch()`→canonical stream (closest to an outbox) | DIY — durable session, single store | BUILT-IN — atomic, idempotent |
| 8 | Cost governance (per-tenant pre-spend) | DIY (src) — per-message USD field, no pre-spend gate | DIY — no pre-spend gate/meter/guardrails | DIY — no gate/breaker/per-tenant meter | DIY — per-call cost estimate (PARTIAL), no gate | DIY — usage post-hoc, no pre-spend | BUILT-IN — per-tenant token-bucket→429 |
| 10 | Sovereignty / licensing | **HARD BLOCKER** — modified Apache-2.0 §1a **forbids multi-tenant operation** without a paid commercial license (Orbit = the prohibited use) | PARTIAL — SDK MIT, but **RUNTIME RENTED** (DOs run only on Cloudflare; not self-hostable OSS) | Apache-2.0, 100% permissive, fully self-hostable, no SaaS gate | Apache-2.0 core, self-hostable multi-tenant (not the strict MIT/Apache line) | Apache-2.0 (eve + `ai`), permissive (some rented runtime surfaces) | 100% MIT/Apache, self-hostable |
| 10b | Dependency license posture (real scan, ▶) | PERMISSIVE (src-classified) — 39/41 direct deps permissive; 2 LGPL-3.0 leaves = CAUTION; 0 GPL/AGPL, 0 BUSL/SSPL | PERMISSIVE ▶ — prod 184 pkgs; +2 MPL-2.0 (lightningcss) = CAUTION; 0 hard blockers | PERMISSIVE ▶ — 41 deps, **0 MPL, a pure permissive line** | PERMISSIVE ▶ — 319/323; 4 weak-copyleft native leaves = CAUTION; 0 hard blockers | PERMISSIVE ▶ — 25 MIT + 7 Apache + 1 dual; 0 hard blockers, 0 MPL | **100% MIT/Apache** — constitutional gate |
| 11 | Language | Python (Flask+Celery) | TypeScript (Workers + Durable Objects) | Java / JVM | TypeScript (durable-agent harness) | TypeScript (filesystem-first) | Go + Rust |

**Two things the new entrants sharpen.** (1) **Cloudflare Agents** is the only framework of all
twelve that *deletes* the shared store — a cross-object read is unrepresentable, the strongest
storage boundary in the field — yet its `(tenant,user,product)→object` name is developer-CONVENTION
(mis-assembled key = leak, proven in `workerd`) and the whole boundary is a **rented, not-self-hostable**
Cloudflare-platform property. (2) **Dify** is the one arena whose **own license** blocks the Orbit
use case: its modified Apache-2.0 forbids operating a multi-tenant environment without a paid
commercial license — independent of its permissive dependency tree. Neither closes the lane
aisotropy holds: isolation by construction in self-hostable, permissively-licensed OSS.

### Governance: executable vs architectural

The constitutional-gate surface (see [`ARENA-SPEC-v2.md`](ARENA-SPEC-v2.md) §5.5) splits into gates
you can *run* and gates you can only *classify*. **Three are now executable and run-verified** across
all seven arenas + aisotropy, offline, every demo exit 0:

- **Structured-output fail-closed** ✓ (already, row 2) — the reflection loop returns 502 rather than
  garbage; a true fail-closed retry only in Pydantic AI and Mastra, PARTIAL/DIY elsewhere.
- **Multi-product isolation** ✓ (row 1b) — the naive-config cross-product read is *executed* in every
  arena and surfaces the marker (a developer-passed key the framework binds to nothing — a dev bug,
  not a defect; the correct-config path isolates and is shown at equal weight); in aisotropy
  `product_id` rides `context.Context` in the `Scope` value object like tenant/user, so it is
  unrepresentable-to-get-wrong.
- **Dependency license posture** ✓ (row 10b) — real license scans of the installed tree; all seven
  clear the hard-blocker gate (0 GPL/AGPL/BUSL/SSPL/ELv2/Commons-Clause/FSL), aisotropy holds 100%
  MIT/Apache by construction.

The remaining constitutional gates stay **architectural — classified, not run** — they are
structural properties no smoke can exercise: **single-binary sovereignty** (the complete zero-config
runtime), **test-before-merge** (a CI/process gate, not a runtime behavior), and the
**backward-compatible-contract** gate (the Go struct *is* the OpenAPI contract, so a JSON-tag/field
change is a breaking-change gate). Honest classifications, not executed demos.

## Real integrations vs. honest simulations

All claims are grounded in code that was fetched, built, and run. Of the twelve arenas, ten are
**REAL integrations** against the pinned upstream framework; two are labelled honestly — **Letta** is
a **partial** integration (real SDK types, but the isolation proof runs against a clearly-labelled
behavioral simulation of the server data plane because Letta OSS needs a live DB + provider key this
offline env lacks), and **Dify** is **SOURCE-VERIFIED, NOT EXECUTED** (a Flask+Celery+Postgres+Redis
multi-container platform with no in-process library — the real backend was cloned at HEAD `1aea3460`
and read line-by-line, and its real schema transcribed into its own ORM for a labelled behavioral
smoke).

| Arena | Integration | What is real | Honesty caveat |
|---|---|---|---|
| Pydantic AI | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| Mastra | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| LangGraph | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| Eino | **REAL** | `cloudwego/eino@v0.9.12` pinned; real `Chain`/`react.NewAgent`/`CheckPointStore`; `StubChatModel` implements the real `model.ToolCallingChatModel`. `go build`/`vet`/`test` (6/6) + smoke all exit 0; leak proven through the real runtime | Only the ChatModel is a keyless stub; the runtime, graph, and checkpoint store are the genuine framework |
| ADK Go | **REAL** | `google.golang.org/adk/v2@v2.0.0`; real `llmagent.New` + `runner.Run` + `session.InMemoryService()`; `FakeModel` implements the real `model.LLM`. `gofmt`/`vet`/`build`/`test` clean; HTTP server booted on :4114, composite key isolated over real HTTP | Only the model is a keyless fake; session/state is genuine construction |
| Genkit Go | **REAL** | `firebase/genkit/go@v1.10.0`; real `genkit.Init`/`DefineFlow`/`Generate`/`GenerateData[T]`/`DefineTool` + dotprompt; fake model via `DefineModel` (no built-in stub exists). `build`/`vet`/`test`/smoke run; leak proven through the real `Generate` pipeline; live server emits real SSE | Only the model is a keyless fake; observability schema confirmed `genkit:*` (no `gen_ai.*`) against v1.10.0 source |
| Letta | **PARTIAL** | `letta-client==1.12.1` real SDK; real `BlockResponse`/`AgentState`/`Passage` types; payloads validated against real `AgentCreateParams`; 7/7 pytest + smoke exit 0 | Isolation smoke is a **labelled** simulation of the server's block/passage plane (real SDK types, real filter contract); no live `agents.create` turn ran — Letta OSS needs a running server + DB + LLM key. Live path only activates with `LETTA_BASE_URL` and pings honestly |
| Dify | **SOURCE-VERIFIED** | Real backend cloned at HEAD `1aea3460`; `api/models` + `api/services/conversation_service.py` + RAG datasource + `LICENSE` + `pyproject.toml` read line-by-line; `python -m compileall` OK, `pytest` 9 passed, real `pip-licenses` on the harness venv (7/7 permissive) | **NOT EXECUTED** against a live Dify — it is a multi-container server with no in-process library. The isolation/product smoke is a **labelled** behavioral simulation: Dify's real schema (`App.tenant_id`, `Conversation.app_id`-only, `Dataset/DocumentSegment.tenant_id`) transcribed into its own ORM (SQLAlchemy 2.0), each query citing `api/<path>:<line>`. Live path activates on `DIFY_API_URL`/`DIFY_API_KEY` |
| Cloudflare Agents | **REAL** | `agents@0.17.4` (MIT); `OrbitAgent extends Agent<Env,State>`, `this.sql` DO SQLite, `getAgentByName`+`routeAgentRequest` — signatures verified against installed `.d.ts`. Ran in real `workerd` via `@cloudflare/vitest-pool-workers` (7 passed) + live `wrangler dev --local` over HTTP; per-object isolation + FTS5 probe confirmed | Model turns/streaming/MCP source-verified from `.d.ts`; the runtime is a **rented** Cloudflare-platform boundary (`workerd`/miniflare simulate DOs locally, production isolation is not self-hostable OSS) |
| Spring AI | **REAL** | `spring-ai-client-chat/model/commons` **2.0.0**; `EchoChatModel` implements the real `ChatModel`, so `ChatClient` + `MessageChatMemoryAdvisor` + `MessageWindowChatMemory` + `InMemoryChatMemoryRepository` + `BeanOutputConverter` all run through the genuine pipeline. `mvn compile` BUILD SUCCESS; `SmokeMain`/`ProductIsolationMain`/`FeatureDemoMain`/`license_scan.sh` all exit 0; signatures verified with `javap` | Only the model is a keyless echo; a real provider turn, streaming Flux cancellation, Micrometer→OTel bridge, `VectorStore` are source-verified (not executed), labelled as such |
| Flue | **REAL** | `@flue/runtime@1.0.0-beta.9` (Apache-2.0, Astro team, on the Pi harness `@earendil-works/pi-ai@0.80.7`); real `defineAgent`/`defineTool`+valibot/`AgentRouteHandler` guard/`sqlite()` durable adapter/Hono app. `flue build --target node` compiled; two smokes ran fully offline via a fake `openai-completions` model + the built server as real subprocesses over HTTP (durable crash-safe recall + cross-product leak), both exit 0 | Only the model is a ~40-line keyless fake; findings tagged `[RAN]`/`[SRC from shipped .d.ts+bundle]`/`[DOC from flue docs read]`, never general knowledge |
| eve | **REAL** | `eve@0.24.5` (Apache-2.0, Vercel) on Node 24; `eve build` → real Nitro/rolldown compile (5.74MB server); booted offline, real `/eve/v1/health`+`/info`, a real 9-event NDJSON turn via mock model; `scripts/smoke.mjs` via `eve/client` 6/6 PASS incl. proven cross-product leak; `license-checker` ran; clean from-scratch reproducibility run | Only the model is a mock; Vercel Sandbox, AI Gateway, production workflow world, Connect OAuth, Observability dashboard are source-verified from shipped docs (not run) |

## DX verdicts

### The GO-NATIVE cluster (Eino, ADK Go, Genkit Go)

These three share aisotropy's host language, so the isolation axis is a clean design comparison —
nothing about Python's GIL or TypeScript's runtime is in play. The difference is **convention vs.
construction**: all three leave tenancy as a key the developer composes, where aisotropy composes it
from `context.Context` by default. That is one axis; each is genuinely strong on its own turf, and
the naive cross-scope read each smoke exercises is a same-key/wrong-key developer bug reproduced
against the framework's weakest config — a competent developer avoids it with the framework's proper
primitive.

- **Eino** — `NOT-POSSIBLE` **because Eino is a graph-orchestration library with no memory/tenancy
  tier by design** (the same category as Pydantic AI or LangGraph), not because it "leaks."
  `compose.CheckPointStore` is `Get/Set(id string)`, a human-in-the-loop interrupt/resume KV keyed
  by one developer string via `WithCheckPointID` — there is no tenant dimension for it to enforce.
  The smoke seeds tenant A under `"shared-session"` and resumes the bare ID as tenant B; the store
  returns A's value **exactly as any string-keyed KV does** (aisotropy's own BadgerDB included). A
  dropped segment collapsing two keys is a footgun, not a framework bug. **Genuinely great at:**
  type-safe, composable, **streaming graph orchestration in Go** — `NewGraph[I,O]` compile-checks
  every edge, one compiled `Runnable` serves `Invoke/Stream/Collect/Transform` for free, and
  `StreamReader.Copy`/`MergeStreamReaders` make streaming a native concern. aisotropy is a runtime,
  not a general orchestration library, so on this axis Eino is the reference.
- **ADK Go** — `CONVENTION` with real construction across **three native slots**. `session.Service`
  keys by composite `(appName, userID, sessionID)`; `appName` is bound at runner construction and is
  **never a per-request argument**, so a competent developer makes any one axis native and
  non-droppable by running one runner per value of it (e.g. `appName=product`). Three slots can't
  cover four axes, so exactly one axis rides `userID` by convention and is the drop-vulnerable
  residual — the honest cost of composing a 4th axis by hand. Note the symmetry: aisotropy **itself**
  trusts the path `tenant_id` in its default NoAuth mode (ADR-0024), and ADK offers `BeforeAgent`/
  `BeforeModel` auth **callbacks** to bind identity (arena feature 16 is `PARTIAL`, not
  `NOT-POSSIBLE`). **The one place ADK ties aisotropy** is observability — real OTel `gen_ai.*`
  semconv, self-hostable — which beats Eino (proprietary APMPlus) and Genkit (proprietary schema).
  **Genuinely great at:** multi-agent orchestration — `transfer_to_agent`, sub-agents, A2A, a full
  callback pipeline — which aisotropy does not attempt at all.
- **Genkit Go** — `CONVENTION`. **Correction to earlier drafts: Genkit-Go v1.10.0 does ship a memory
  primitive** — the experimental `ai/exp` Agents API with a pluggable `SessionStore` and a shipped
  in-memory `localstore.NewInMemorySessionStore` (July-2026 preview, gated behind
  `requireExperimental`). It is keyed by a developer-chosen session id with no tenant/user/product
  binding, so it gives you **persistence, not isolation** — the leak stands against the real
  primitive, but "no Session/Memory/Thread type at all" was false. **Genuinely great at:** flows +
  dotprompt + a model-agnostic provider abstraction — `DefineFlow`/`DefineStreamingFlow` turn a
  function into an observable, deployable unit with auto-emitted spans; `.prompt` files hold the
  template + typed input schema hot-editable outside Go source; one `Generate` call dispatches to any
  provider by name. Observability is the real trade-off for gen_ai-standardizing teams: `genkit:*`
  attributes with **zero** `gen_ai.*` keys.

**Go-native takeaway:** same language as aisotropy, different *category* — three capable
orchestration/application libraries vs. one sovereign multi-tenant runtime. aisotropy's narrow edge
is that isolation is on-by-default and unforgeable; each of the three is the better tool on its own
axis (graph orchestration, multi-agent delegation, flow/deploy DX).

### The Python / TypeScript cluster

- **Pydantic AI (Py)** — `DIY / out-of-scope`, **not a leak**: it is a stateless agent library that
  ships no memory or multi-tenancy layer by design ("agents are stateless and designed to be
  global"). `deps` is pure dependency injection; a wrong developer-computed key reads the wrong slice
  because there is no framework boundary to reject it — plain-dict behavior. **Genuinely great at:**
  type-safe structured output as a *validated contract* (fail-closed `raise ModelRetry` self-repair,
  typed `Union` routing) and first-class typed dependency injection — best-typed agent-loop DX of the
  set.
- **Mastra (TS)** — `CONVENTION` via `resourceId`/`threadId` plus two more levers (working-memory
  `scope`, `semanticRecall.filter`); a competent developer isolates correctly (run-verified via a
  composite `resourceId` and a metadata filter), the naive default path silently does not partition
  (the class behind mastra#11856). **Genuinely great at:** TS-native, schema-typed **durable
  workflows** with suspend/resume human-in-the-loop, first-class composable **evals/scorers**, and
  batteries-included memory DX — none of which aisotropy offers.
- **LangGraph (Py)** — `CONVENTION`: on the raw `Store`, `search()` uses element-wise namespace
  prefix matching and a shallow prefix is an under-scoped **dev bug**; deep namespaces plus the
  LangGraph-Server `@auth.on.store()` handler rewrite the namespace to the authenticated caller and
  make the cross-read structurally impossible (run-verified). Its managed observability path
  (LangSmith) is a paid SaaS — a genuine sovereignty consideration. **Genuinely great at:** durable,
  stateful **graph execution** — checkpoint/resume, human-in-the-loop interrupts, time-travel over
  checkpoint history, cyclic-graph streaming — where aisotropy has no equivalent and makes no claim.
- **Letta (Py)** — the **closest conceptual neighbor**: its Core/Recall/Archival memory hierarchy
  is nearly isomorphic to aisotropy's BadgerDB/FTS5/Qdrant tiers, and it is the **only** arena that
  ships productized sleep-time consolidation (`enable_sleeptime=True`) — a direct analog of
  aisotropy's DataFusion sleep cycle. Isolation is `CONVENTION` org-wide but **`BUILT-IN` by
  `agent_id`**: the agent-scoped subresources (`agents.blocks.list(agent_id)`,
  `agents.passages.search(agent_id)`) isolate by construction, and Letta's documented multi-tenant
  pattern — **one agent per principal** — lets a competent developer avoid three of the four sharp
  edges an earlier draft dressed up as "leaks" (the residual: OSS Letta is single-org with no
  framework caller-binding, and the org-wide primitives carry no guard). Memory is LLM-self-managed
  (probabilistic) vs. aisotropy's deterministic hydration, and it is a server, not an in-process
  runtime. **Genuinely great at:** self-editing core memory + sleep-time consolidation +
  stateful-agent-as-a-service — Letta validates the memory-OS thesis and productizes it today.

## What each framework is genuinely great at (the steelman, run-verified)

Isolation-by-default is one axis. On every *other* axis a competitor leads, and each arena's
`strengths_demo` exercises it against the pinned framework offline. aisotropy does not compete on
these — they are real, and they are the framework's to keep.

| Framework | Genuinely great at (run-verified) |
|---|---|
| **LangGraph** | Durable, stateful **graphs** — checkpoint/resume, human-in-the-loop interrupts, time-travel over checkpoint history, cyclic-graph streaming. aisotropy has no durable graph engine. |
| **Mastra** | TS-native, schema-typed **durable workflows** (suspend/resume HITL), first-class composable **evals/scorers**, batteries-included **memory DX**. |
| **Letta** | Self-editing **core memory** (MemGPT) + **sleep-time consolidation** + stateful agent-as-a-service — validates and productizes the memory-OS thesis today. |
| **ADK Go** | Multi-agent **orchestration** — `transfer_to_agent`, sub-agents, A2A, full callback pipeline — plus self-hostable **gen_ai OTel** (ties aisotropy). |
| **Pydantic AI** | Best-typed agent-loop DX — **type-safe structured output** as a validated contract (fail-closed self-repair, typed `Union` routing) + first-class **typed DI**. |
| **Eino** | Type-safe, composable **streaming graph orchestration** in Go — generics compile-check every edge; one `Runnable` boxes `Invoke/Stream/Collect/Transform`. |
| **Genkit Go** | **Flows** + **dotprompt** + a model-agnostic **provider abstraction** — observable/deployable units, hot-editable typed prompts, one-call provider swap. |
| **sandcastle** *(adjacent)* | Running a **whole, unmodified coding CLI in a box** (Claude Code / Codex / Cursor) with isolated workspaces + merge-back — a niche aisotropy's `RunTask` does not attempt. |

aisotropy keeps two honest concessions here: **ADK Go ties on OTel `gen_ai`**, and **Letta validates
the memory-OS thesis** (isomorphic tiers + productized sleep-time). aisotropy's own credible edge is
the *combination*: isolation by default (unforgeable 4-axis `Scope` wired from ctx), governance
defaults (per-tenant pre-spend gate, breaker, cost meter), and 100% MIT/Apache self-hostable
sovereignty — a sovereign multi-tenant runtime, not the best tool on any single axis above.

## Isolation model summary

| Framework | Integration | Isolation model | Failure mode |
|---|---|---|---|
| **aisotropy** | — | **By construction** — ctx propagation + per-statement/key scoping | Unrepresentable — `Scope{}` has unexported fields |
| Pydantic AI | REAL | DIY / out-of-scope — stateless lib, no memory tier by design | Wrong dev-computed key → wrong slice (plain-dict; no framework layer to reject it) |
| Mastra | REAL | Convention — `resourceId`/`threadId` + 2 levers (isolable by a competent dev) | Naive default path (axis in no lever); composite key / metadata filter prevent it |
| LangGraph | REAL | Convention — deep namespaces + `@auth.on.store()` identity binding | Naive shallow prefix is a dev bug; identity-bound store makes cross-read impossible |
| Eino | REAL | None **by design** — orchestration lib, no tenancy tier | Dropped-segment key collapse (any string-keyed KV); footgun ≠ framework bug |
| ADK Go | REAL | Convention — composite `(app,user,session)`; `appName` native slot + auth callbacks | Drop the one hand-composed residual axis → cross-read (native slots don't drop) |
| Genkit Go | REAL | Convention — experimental `ai/exp` `SessionStore` (session-keyed, tenant-unaware) | Mis-passed session key → cross-read (persistence, not isolation) |
| Letta | PARTIAL | Convention org-wide / **BUILT-IN by `agent_id`** (one-agent-per-principal) | Org-wide under-scope → scan; agent-scoped reads isolate by construction |
| Dify | SOURCE-VERIFIED | Convention — shared-schema `tenant_id` `.where()`, **no RLS**; `Conversation` scoped only via `app_id` | Skip the app-ownership gate → `.where(app_id)` cross-reads; no `product_id` exists |
| Cloudflare Agents | REAL | **By construction (storage)** — one DO = one separate SQLite; cross-object read unrepresentable — but name-mapping is convention, runtime RENTED | Mis-assemble the `(t,u,p)` object name → two products share one SQLite (proven in `workerd`) |
| Spring AI | REAL | Convention — opaque `conversationId` bound to nothing | Wrong/guessed id → cross-read; `findConversationIds()` scans all tenants; delimiter-injection collision |
| Flue | REAL | Convention — one opaque instance-`id`; per-instance stream isolated, dev-written route guard | Route to wrong instance id (one token) → cross-product read; Flue authenticates nothing |
| eve | REAL | Convention — no tenant/user/product concept; session-scoped; dev-built `${t}:${u}:${p}` key | Drop `:${product}` → wrong product's secret returned (proven) |

## Bottom line

**Isolation is `CONVENTION` / developer-owned in the shared-store frameworks and `BY CONSTRUCTION`
only in aisotropy — that is the *one* narrow axis this matrix isolates.** Every arena can be made
secure: a competent developer applies the framework's proper primitive (Mastra's composite
`resourceId`, LangGraph's `@auth.on.store()`, ADK's per-runner `appName`, Letta's one-agent-per-
principal) and the cross-scope read never happens. The naive path each smoke exercises is a
same-key/wrong-key developer bug — the property of any KV store with no auth layer, aisotropy's own
BadgerDB included — reproduced against each framework's weakest config; the correct-config path is
shown here at equal weight. What differs is *default posture*: in aisotropy `tenant_id`/`product_id`
travel in `context.Context` as an unforgeable `Scope`, every memory read is scoped per-statement/
per-key, and the unscoped read is unrepresentable — isolation is on **by default**, not a discipline
the developer must remember. That is a real edge for a sovereign multi-tenant runtime; it is *not* a
claim that the other frameworks "leak," and it is one axis among many where each competitor leads.

Two of the original arenas cost aisotropy an honest concession: **ADK Go ties on OTel `gen_ai`
observability**, and **Letta validates the memory-OS thesis** (isomorphic tiers + productized
sleep-time). The 5 new entrants add three more concessions that still do not close the lane:

- **Cloudflare Agents** goes further than any of the original seven on isolation — it is the only
  framework that *deletes* the shared store (one Durable Object = one physically separate SQLite;
  cross-object read unrepresentable), the strongest **storage** boundary in the field. But the
  `(tenant,user,product)→object` name is developer-CONVENTION the framework binds to nothing
  (mis-assembled key = leak, proven in `workerd`), and the whole guarantee is a **RENTED,
  not-self-hostable** Cloudflare-platform property. By-construction storage isolation that is rented
  and identity-unbound is not the self-hostable tenant-data isolation this arena tests.
- **Dify** is the broadest *product* of the twelve, but isolation is shared-schema app-layer `WHERE`
  with no RLS, there is **no `product_id` column at all**, and — decisively — **its own modified
  Apache-2.0 license forbids operating a multi-tenant environment** (Orbit) without a paid commercial
  license. It is the one arena whose license, not just its architecture, rules out the use case.
- **Spring AI** is the JVM's richest self-hostable, fully-permissive agent SDK, but scoping is the
  same `conversationId` convention: a string bound to nothing, `findConversationIds()` returning
  every tenant's ids, a product-axis delimiter-injection collision.

The open lane stands: **enforced multi-tenant isolation in self-hostable, permissively-licensed OSS
is something none of the twelve builds — and aisotropy makes it unrepresentable-to-get-wrong.**

## Adjacent: sandcastle (out of category)

[`mattpocock/sandcastle`](https://github.com/mattpocock/sandcastle) comes up in the same breath as
these frameworks, so it is worth naming why it is **not** an arena here. Sandcastle is a TypeScript
library for orchestrating **coding agents** in isolated sandboxes — you hand it an agent that writes
and runs code, and it spins that agent up inside a fresh, disposable execution environment (Docker
locally, or a Vercel sandbox) so the agent's file writes and shell commands cannot touch the host.
Its whole reason to exist is **execution isolation of an untrusted coding agent**, not serving a
multi-tenant conversational product.

**Why it does not fit the arena.** Every framework above is measured on the Orbit yardstick: a
multi-tenant support chatbot where the question is whether tenant A's *memory* (conversation history,
facts, vectors) can leak into tenant B's *response*. Sandcastle has no conversational runtime, no
tenant/user/product identity model, no memory tiers, no per-tenant governance, and no isolation of
*data* between callers — it isolates a *process*, once, per coding task. There is no shared store to
leak through because there is no shared store at all; there is also no tenant abstraction to get
wrong. It answers a different question ("can this agent's `rm -rf` escape?") than the arena's
("can this tenant read that tenant's data?").

**The one axis where it genuinely compares to aisotropy — and its limit.** Both draw an isolation
boundary using an OS-level mechanism. Sandcastle's boundary is a **sandbox/process boundary**
(Docker container or Vercel sandbox) around an *untrusted coding agent's execution*. aisotropy's
comparable boundary is its **Go+Rust subprocess boundary** (ADR-0005): heavy Rust engines
(DataFusion sleep-cycle, bitrate scorer) run as separate subprocesses over an explicit I/O contract,
so a Rust panic is a Go-observable non-zero exit, never an in-process crash. Both are honest
execution-isolation boundaries — but **neither is the tenant-data isolation the arena tests.**
aisotropy's *tenant* isolation lives one layer up, in the `context.Context` + value-object `Scope`
type system, not in the subprocess boundary. So sandcastle and aisotropy share exactly one axis
(execution isolation via sandbox/process vs. subprocess), and it is precisely the axis that is
*orthogonal* to the multi-tenant-memory question every arena is scored on.

## How to run each arena

Each sub-directory is self-contained; per-arena `README.md` has full detail. Offline smoke tests
need **no API key** (every arena ships a stub/fake LLM). A leak-confirmed smoke exits non-zero by
design in the arenas that adopt the `leak = exit 1` contract.

| Arena | Lang / runtime | Setup | Smoke test | Live serve | Port |
|---|---|---|---|---|---|
| `pydantic-ai/` | Python 3.13 | `pip install pydantic-ai httpx` | `python scripts/smoke.py` | `uvicorn orbit.app:app` | 4110 |
| `mastra/` | Node 20+ | `npm install` | `npm run smoke` | `npm run serve` | 4111 |
| `langgraph/` | Python 3.13 | `pip install langgraph langchain-openai` | `python scripts/smoke.py` | `uvicorn orbit.server:app` | 4112 |
| `eino/` | Go 1.23+ | `go get github.com/cloudwego/eino` | `go run scripts/smoke.go` | `go run cmd/server/main.go` | 4113 |
| `adk-go/` | Go 1.25+ | `go get google.golang.org/adk/v2@v2.0.0` | `go run scripts/smoke.go` | `go run cmd/server/main.go` | 4114 |
| `letta/` | Python 3.13 | `pip install letta-client` (+ `letta` server for live) | `python scripts/smoke.py` | `letta server --port 4115` then `LETTA_BASE_URL=...` | 4115 |
| `genkit-go/` | Go 1.23+ | `go get github.com/firebase/genkit/go@v1.10.0` | `GENKIT_ENV=prod go run ./scripts/` | `GENKIT_ENV=prod go run ./cmd/server/` | 4116 |
| `dify/` (src) | Python 3.13 | `python -m venv .venv && pip install -r requirements.txt` | `python scripts/smoke.py` + `python scripts/product_isolation_demo.py` | n/a — needs full Dify stack via `DIFY_API_URL`/`DIFY_API_KEY` | n/a (server platform) |
| `cloudflare-agents/` | Node 20+ / pnpm | `pnpm install` (`agents@0.17.4` + wrangler + vitest) | `pnpm smoke` (= `vitest run` → 7 passed) | `pnpm dev` (real `workerd` Durable Objects) | 4117 / 8787 |
| `spring-ai/` | JDK 21+ / Maven 3.9+ | `mvn compile` (real `spring-ai` 2.0.0, 41-dep tree) | `SmokeMain` + `ProductIsolationMain` + `FeatureDemoMain` (via `mvn exec:java`) | n/a — JVM library, no HTTP server | n/a (JVM lib) |
| `flue/` | Node + pnpm | `pnpm install` + `flue build --target node` | `pnpm run smoke` + `pnpm run isolation` | built `dist/server.mjs` (file-backed SQLite) | local subprocess |
| `eve/` | Node 24+ | `npm install` + `eve build` | `node scripts/smoke.mjs` (6/6 PASS) | `node .output/server/index.mjs` | 4117 |

> **Port note:** `cloudflare-agents` and `eve` both document port 4117 for local serve — they were
> authored independently and are never run simultaneously. `spring-ai` and `dify` expose no local
> HTTP server in this arena (`spring-ai` is a JVM library exercised via `main` methods; `dify` is
> SOURCE-VERIFIED with the live path gated on a real Dify server via `DIFY_API_URL`/`DIFY_API_KEY`).

> **Genkit note:** set `GENKIT_ENV=prod` for CI/smoke — otherwise `genkit.Init` starts a dev
> Reflection server on :3100. **ADK Go note:** requires Go 1.25+ (v2 declares `go 1.25.0`).

## What each smoke test proves

Every arena runs the **same adversarial pattern**:

1. **Seed tenant A** with a unique marker token in a shared session ID.
2. **Positive control**: Tenant A reads its own session — marker must appear.
3. **Naive-convention path**: a cross-scope read using the framework's *weakest* config (a wrong or
   under-scoped developer-computed key) — the marker surfaces, a same-key/wrong-key developer bug.
4. **Correct-config path (shown at equal weight)**: the same read using the framework's proper
   primitive (composite key, identity-bound store, per-runner slot, agent-scoped subresource) — the
   marker does NOT surface.

The naive-path cross-read is reproducible in the shared-store frameworks, but it is a **developer
bug**, not a framework defect — the property of any KV store with no auth layer, aisotropy's own
BadgerDB included. The real difference is *default posture*: in aisotropy the unscoped read is
**unrepresentable** (no code path issues one), so the naive path does not exist; in the others the
naive path and the correct path both exist and the developer must choose the correct one.

## References

- Competitive landscape: [docs/research/competitive-landscape.md](../research/competitive-landscape.md)
- Build vs adopt decision: [docs/decisions/0020-build-vs-adopt-lang-suite-mastra.md](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
- Reality ledger: [docs/architecture/reality-ledger.md](../architecture/reality-ledger.md)
- aisotropy isolation tests: [`aisotropy/isolation_e2e_test.go`](../aisotropy/isolation_e2e_test.go)
- Per-arena deliverables: each arena's `README.md`, `FEATURES.md`, `DX-NOTES.md`
