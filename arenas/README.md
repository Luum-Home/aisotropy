---
type: Survey
title: "Adversarial isolation matrix — 12 arenas (Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta, Dify, Cloudflare Agents, Spring AI, Flue, eve) + adjacent sandcastle"
description: Executable smoke tests that prove tenant-isolation-by-construction is aisotropy's surviving differentiator. Each arena writes secret-A into a shared session, then reads from every cross-tenant identity — a marker appearing in the wrong tenant's response is a proven leak. 12 arenas across 5 languages, plus an honest adjacent note on sandcastle.
tags: [arenas, isolation, adversarial, pydantic-ai, mastra, langgraph, eino, adk-go, genkit-go, letta, dify, cloudflare-agents, spring-ai, flue, eve, competitive]
timestamp: 2026-07-15T00:00:00Z
---

# Arenas — Adversarial isolation comparison

> **Goal:** Prove that writing the *right path* and the *leaky path* costs the same in every
> framework — and that every framework except aisotropy leaks when you take the leaky path.
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
| 1 | **Multi-tenant isolation** | CONVENTION — `deps.tenant_id` | CONVENTION — `resourceId`/`threadId` | CONVENTION — prefix-match `Store.search` | **NOT-POSSIBLE** — bare `CheckPointStore` string key | CONVENTION — composite `(app,user,session)` key, **no tenant binding** | CONVENTION — **no primitive**, dev-built store | CONVENTION — optional dev-passed filters (per-user key = PAID) | **BY CONSTRUCTION** — ctx + per-statement/key scoping |
| 1b | **Multi-product isolation** (4th scope axis, run-verified ▶) | CONVENTION — `deps.product_id`, bound to nothing | CONVENTION — product rides unkeyed metadata over 2-key memory | CONVENTION — unbound namespace element; shallow prefix scans past it | CONVENTION — folded into the bare `CheckPointStore` string; drop segment → leak | CONVENTION — no `ProductID`; folded into `userID`, trusted verbatim | CONVENTION — **no product primitive**; dev-built key | CONVENTION — no `product_id` param; only a dev-invented `product:<id>` tag | **BY CONSTRUCTION** — `product_id` rides ctx in `Scope` like tenant/user |
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
- **Multi-product isolation** ✓ (row 1b) — the cross-product read is *executed* and leaks in every
  arena (`product_id` is a developer-passed key the framework binds to nothing); in aisotropy it
  rides `context.Context` in the `Scope` value object like tenant/user, so it is unrepresentable-to-get-wrong.
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
| Spring AI | **REAL** | `spring-ai-client-chat/model/commons` **1.1.8**; `EchoChatModel` implements the real `ChatModel`, so `ChatClient` + `MessageChatMemoryAdvisor` + `MessageWindowChatMemory` + `InMemoryChatMemoryRepository` + `BeanOutputConverter` all run through the genuine pipeline. `mvn compile` BUILD SUCCESS; `SmokeMain`/`ProductIsolationMain`/`FeatureDemoMain`/`license_scan.sh` all exit 0; signatures verified with `javap` | Only the model is a keyless echo; a real provider turn, streaming Flux cancellation, Micrometer→OTel bridge, `VectorStore` are source-verified (not executed), labelled as such |
| Flue | **REAL** | `@flue/runtime@1.0.0-beta.9` (Apache-2.0, Astro team, on the Pi harness `@earendil-works/pi-ai@0.80.7`); real `defineAgent`/`defineTool`+valibot/`AgentRouteHandler` guard/`sqlite()` durable adapter/Hono app. `flue build --target node` compiled; two smokes ran fully offline via a fake `openai-completions` model + the built server as real subprocesses over HTTP (durable crash-safe recall + cross-product leak), both exit 0 | Only the model is a ~40-line keyless fake; findings tagged `[RAN]`/`[SRC from shipped .d.ts+bundle]`/`[DOC from flue docs read]`, never general knowledge |
| eve | **REAL** | `eve@0.24.5` (Apache-2.0, Vercel) on Node 24; `eve build` → real Nitro/rolldown compile (5.74MB server); booted offline, real `/eve/v1/health`+`/info`, a real 9-event NDJSON turn via mock model; `scripts/smoke.mjs` via `eve/client` 6/6 PASS incl. proven cross-product leak; `license-checker` ran; clean from-scratch reproducibility run | Only the model is a mock; Vercel Sandbox, AI Gateway, production workflow world, Connect OAuth, Observability dashboard are source-verified from shipped docs (not run) |

## DX verdicts

### The GO-NATIVE cluster (Eino, ADK Go, Genkit Go) — the decisive comparison

These three share aisotropy's host language, so their **isolation story is the most decisive
comparison** — nothing about Python's GIL or TypeScript's runtime can be blamed for the gap; it is
purely a design choice. In all three, tenant isolation is a **string key the developer must own and
never get wrong**, and the leaky path is literally one string-concatenation (or one dropped token)
away from the correct one:

- **Eino** — `NOT-POSSIBLE` by the framework. `compose.CheckPointStore` is `Get/Set(id string)`
  keyed by a single developer-supplied string via `WithCheckPointID` — no tenant/user dimension
  anywhere, and **no default implementation**. The proof is driven by Eino's real interrupt/resume
  machinery: seed tenant A under `"shared-session"`, resume the bare ID as tenant B, and Eino
  restores tenant A's secret. `store.Keys()` returns `[shared-session]` — a bare `[]string` of
  developer-chosen IDs. Excellent Go-native velocity (type-safe `InferTool` + ReAct, clean model
  seam) but heavy transitive tree (sonic asm loader, gonja) and a checkpoint/resume API that needs
  the interrupt ID from the first run, not just the checkpoint ID.
- **ADK Go** — `CONVENTION` with real construction but **no tenant binding**. `session.Service`
  keys by composite `(app,user,session)`; fold tenant into the key and `globex|bob`/`acme|bob`
  isolate correctly — drop the tenant component (bare `"alice"`) and `globex`'s alice reads
  `acme`'s memo (`TestTenantDropLeaks`). `runner.Run` trusts the caller strings verbatim: no
  identity binding. **The one place ADK ties aisotropy** is observability — real OTel `gen_ai.*`
  semconv — which differentiates it from Eino (proprietary APMPlus) and Genkit (proprietary
  schema). Heavy dep graph (aiplatform + genai + gRPC + sqlite); rich memory is Vertex-tethered.
- **Genkit Go** — `CONVENTION`, and arguably the **weakest of the three**: stable Genkit ships
  **no** `Session`/`Memory`/`Thread` type at all (`go doc` confirms). You build the store yourself
  and hand history to `ai.WithMessages`; nothing binds the key to the request identity, so a
  mis-passed key re-leaks even after you invent namespacing. Generation ergonomics are genuinely
  strong and shipped (`GenerateData[T]`, `DefineTool`, dotprompt), but observability is a real
  liability for gen_ai-standardizing teams: `genkit:*` attributes with **zero** `gen_ai.*` keys.

**Go-native takeaway:** same language as aisotropy, opposite philosophy. All three treat tenancy as
app-layer scaffolding the developer bolts on — exactly the scaffolding that silently breaks.
aisotropy makes the unscoped read *unrepresentable*.

### The Python / TypeScript cluster

- **Pydantic AI (Py)** — `CONVENTION` via `RunContext[deps].tenant_id`; strongest fail-closed
  structured-output story (`raise ModelRetry`). Wrong `tenant_id` → silent cross-read.
- **Mastra (TS)** — `CONVENTION` via `resourceId`/`threadId`; strong built-in resource-scoped +
  vector memory. Wrong key, or the `threadId`-only leak (mastra#11856), cross-reads.
- **LangGraph (Py)** — `CONVENTION` via prefix-match `Store.search()`; a short prefix scans across
  tenants. Its observability is `PAID` (LangSmith SaaS) — the only arena where the standard
  telemetry path is a hosted product.
- **Letta (Py)** — the **closest conceptual neighbor**: its Core/Recall/Archival memory hierarchy
  is nearly isomorphic to aisotropy's BadgerDB/FTS5/Qdrant tiers, and it is the **only** arena that
  ships productized sleep-time consolidation (`enable_sleeptime=True`) — a direct analog of
  aisotropy's DataFusion sleep cycle. But isolation is `CONVENTION` (optional dev-passed
  `identity_id`/`project_id`/`tags`; per-user *key* isolation is `PAID` Letta Cloud only), memory
  is LLM-self-managed (probabilistic, hard to audit) vs. aisotropy's deterministic runtime
  hydration, and it is a server, not an embeddable in-process runtime.

## Isolation model summary

| Framework | Integration | Isolation model | Failure mode |
|---|---|---|---|
| **aisotropy** | — | **By construction** — ctx propagation + per-statement/key scoping | Unrepresentable — `Scope{}` has unexported fields |
| Pydantic AI | REAL | Convention — `RunContext[deps].tenant_id` | Wrong `tenant_id` → silent cross-read |
| Mastra | REAL | Convention — `resourceId`/`threadId` | Wrong key or mastra#11856 (threadId-only) |
| LangGraph | REAL | Convention — prefix-match `Store.search()` | Short prefix scans across tenants |
| Eino | REAL | **None** — bare `CheckPointStore` string key | Wrong checkpoint-ID → silent cross-read |
| ADK Go | REAL | Convention — composite `(app,user,session)` key, **no tenant binding** | Drop the tenant fold → silent cross-read (one line shorter) |
| Genkit Go | REAL | Convention — developer-built store, **no primitive** | Wrong/mis-passed session key → silent cross-read |
| Letta | PARTIAL | Convention — dev-passed `identity_id`/`project_id`/`tags` | Omit filter → org-wide scan; per-user key is PAID only |
| Dify | SOURCE-VERIFIED | Convention — shared-schema `tenant_id` `.where()`, **no RLS**; `Conversation` scoped only via `app_id` | Skip the app-ownership gate → `.where(app_id)` cross-reads; no `product_id` exists |
| Cloudflare Agents | REAL | **By construction (storage)** — one DO = one separate SQLite; cross-object read unrepresentable — but name-mapping is convention, runtime RENTED | Mis-assemble the `(t,u,p)` object name → two products share one SQLite (proven in `workerd`) |
| Spring AI | REAL | Convention — opaque `conversationId` bound to nothing | Wrong/guessed id → cross-read; `findConversationIds()` scans all tenants; delimiter-injection collision |
| Flue | REAL | Convention — one opaque instance-`id`; per-instance stream isolated, dev-written route guard | Route to wrong instance id (one token) → cross-product read; Flue authenticates nothing |
| eve | REAL | Convention — no tenant/user/product concept; session-scoped; dev-built `${t}:${u}:${p}` key | Drop `:${product}` → wrong product's secret returned (proven) |

## Bottom line

**Isolation is `CONVENTION` / developer-passed in eleven of the twelve frameworks — the Go-native
cluster included — and `BY CONSTRUCTION` only in aisotropy.** This is now proven across **12
frameworks in 5 languages** (Python, TypeScript, Go, Java/JVM, TypeScript-on-Workers). Language is
not the variable: Eino, ADK Go, and Genkit Go are the same Go as aisotropy's core, and all three
still leave tenancy as a string the developer must never fumble. In every arena the leaky read and
the correct read are one line — one string, one keyword, one dropped token — apart, and the type
system does nothing to stop the leak. In aisotropy the leaky path does not exist: `tenant_id` travels
in `context.Context` and every memory read is scoped per-statement/per-key, so no code path can issue
an unscoped read.

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
| `spring-ai/` | JDK 21+ / Maven 3.9+ | `mvn compile` (real `spring-ai` 1.1.8, 41-dep tree) | `SmokeMain` + `ProductIsolationMain` + `FeatureDemoMain` (via `mvn exec:java`) | n/a — JVM library, no HTTP server | n/a (JVM lib) |
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
3. **Negative probes**: Tenants B, users B, mixed identities read the same session — marker must NOT appear.
4. **Result**: If the marker appears in a cross-tenant read, the framework leaks.

The leak is demonstrated in every framework — the difference is **how hard it is to write the
correct path vs the leaky path**. In aisotropy, the leaky path is **unrepresentable** (no code
path issues an unscoped read). In the others, both paths are one line apart.

## References

- Competitive landscape: [docs/research/competitive-landscape.md](../research/competitive-landscape.md)
- Build vs adopt decision: [docs/decisions/0020-build-vs-adopt-lang-suite-mastra.md](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
- Reality ledger: [docs/architecture/reality-ledger.md](../architecture/reality-ledger.md)
- aisotropy isolation tests: [`aisotropy/isolation_e2e_test.go`](../aisotropy/isolation_e2e_test.go)
- Per-arena deliverables: each arena's `README.md`, `FEATURES.md`, `DX-NOTES.md`
