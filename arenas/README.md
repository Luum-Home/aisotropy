---
type: Survey
title: "Adversarial isolation matrix — Pydantic AI, Mastra, LangGraph, Eino, ADK Go, Genkit Go, Letta"
description: Executable smoke tests that prove tenant-isolation-by-construction is aisotropy's surviving differentiator. Each arena writes secret-A into a shared session, then reads from every cross-tenant identity — a marker appearing in the wrong tenant's response is a proven leak.
tags: [arenas, isolation, adversarial, pydantic-ai, mastra, langgraph, eino, adk-go, genkit-go, letta, competitive]
timestamp: 2026-07-15T00:00:00Z
---

# Arenas — Adversarial isolation comparison

> **Goal:** Prove that writing the *right path* and the *leaky path* costs the same in every
> framework — and that every framework except aisotropy leaks when you take the leaky path.
> **Coverage:** 7 frameworks across 3 languages (Python, TypeScript, Go) vs. the aisotropy runtime.

## Feature-coverage matrix

Rows are the 11-point Orbit checklist. Each cell is a **coverage class** + terse note.

**Coverage classes:** `BUILT-IN` (ships, works out of the box) · `CONVENTION` (framework gives a
primitive but scoping/correctness is the developer's responsibility) · `PARTIAL` (present but
incomplete or backend-tethered) · `DIY` (you build it yourself / BYO) · `PAID` (only via a
paid/proprietary product) · `NOT-POSSIBLE` (framework cannot provide it by construction).

| # | Feature | Pydantic AI (Py) | Mastra (TS) | LangGraph (Py) | Eino (Go) | ADK Go (Go) | Genkit Go (Go) | Letta (Py) | **aisotropy (Go+Rust)** |
|---|---|---|---|---|---|---|---|---|---|
| 1 | **Multi-tenant isolation** | CONVENTION — `deps.tenant_id` | CONVENTION — `resourceId`/`threadId` | CONVENTION — prefix-match `Store.search` | **NOT-POSSIBLE** — bare `CheckPointStore` string key | CONVENTION — composite `(app,user,session)` key, **no tenant binding** | CONVENTION — **no primitive**, dev-built store | CONVENTION — optional dev-passed filters (per-user key = PAID) | **BY CONSTRUCTION** — ctx + per-statement/key scoping |
| 2 | Structured output + retry | BUILT-IN — fail-closed `raise ModelRetry` | BUILT-IN — AI-SDK v5 | BUILT-IN — `OutputFixingParser` | PARTIAL — parser only, no repair loop | BUILT-IN — `OutputSchema` node validation | BUILT-IN — `GenerateData[T]`, no repair loop | BUILT-IN — `response_format` json_schema, no repair loop | BUILT-IN — Gollem reflection (fail-closed) |
| 3 | Tools | BUILT-IN — function tools | BUILT-IN — native tool calling | BUILT-IN — graph nodes | BUILT-IN — `InferTool` + ReAct | BUILT-IN — `tool.Tool` (+MCP/A2A native) | BUILT-IN — `DefineTool` | BUILT-IN — LLM tool calls | BUILT-IN — `WithTypedTool[T]` (scope-gated) |
| 4 | Streaming (SSE) | BUILT-IN — `AsyncAgent.stream` | BUILT-IN — `ai_sdk` | BUILT-IN — callback handler | BUILT-IN — `StreamReader` | BUILT-IN — `StreamingModeSSE` | BUILT-IN — `WithStreaming` | BUILT-IN — `stream=True` | BUILT-IN — Huma SSE + `ctx` cancel |
| 5 | Working memory | DIY — app dict / Redis | BUILT-IN — resource-scoped | BUILT-IN — checkpointers + Store | CONVENTION — checkpoint keyed by bare string | BUILT-IN — Session backends (in-mem/sqlite/vertex) | DIY — build store; `genkitx-temporal` 3rd-party | BUILT-IN — Core Memory `Block`s | BUILT-IN — BadgerDB (µs) |
| 6 | Semantic memory (vectors) | DIY — BYO Chroma/Pinecone | BUILT-IN — vector recall | BUILT-IN — Store + semantic cache | DIY — #995 proposed | PARTIAL — Vertex MemoryBank (GCP-managed) | DIY — BYO retriever plugin | BUILT-IN — Archival `Passage`s | BUILT-IN — in-process cosine (active); Qdrant code-only |
| 7 | Facts memory (journal) | DIY | DIY | DIY | DIY | DIY | DIY | PARTIAL — Recall Memory | BUILT-IN — SQLite+FTS5 |
| 8 | Cost governance | pre-spend gate BUILT-IN (`UsageLimits`, per-**run**); per-tenant DIY | DIY — usage BUILT-IN, tenant ledger + gate DIY | DIY — `usage_metadata` BUILT-IN, per-tenant + gate DIY | DIY — `ResponseMeta.Usage` BUILT-IN, no tenant, no gate | DIY — `UsageMetadata` BUILT-IN, no tenant, no gate | DIY — `resp.Usage` BUILT-IN, no tenant, no gate | DIY — `usage` visibility BUILT-IN, meter + gate DIY | **BUILT-IN — per-TENANT pre-spend gate (BadgerDB token-bucket→`429`, by construction) + `costmeter` per-tenant meter** |
| 9 | Observability | BUILT-IN — OTel `gen_ai` spans offline; `tenant_id` DIY; Logfire PAID | BUILT-IN — `@mastra/observability` offline; `tenant_id` DIY; UI PAID | BUILT-IN spans offline (own exporter); `tenant_id` DIY; **dashboard PAID** (LangSmith) | BUILT-IN span/timing (callbacks); no native `gen_ai` emitter; `tenant_id` DIY; eino-ext PAID | BUILT-IN — OTel `gen_ai` semconv (13 spans, no config); `tenant.id` DIY | BUILT-IN OTel transport, `genkit:*` schema (no `gen_ai.*`); `tenant_id` DIY | BUILT-IN but SERVER-side (native trace = ClickHouse, NOT-POSSIBLE offline); client span DIY; dashboards PAID | **BUILT-IN — OTel `gen_ai` semconv with `tenant_id`/`user_id` NATIVE on the serving span** |
| 10 | Sovereignty / licensing | MIT | Apache 2.0 | OSS MIT + platform SaaS-gated | Apache 2.0 (ByteDance; APMPlus caveat) | Apache 2.0 (Google; Vertex gradient) | Apache 2.0 (Google) | Apache 2.0 (self-host server) | 100% MIT/Apache, fully self-hostable |
| 11 | Language | Python | TypeScript | Python | Go | Go | Go | Python | Go + Rust |

**One correction vs. the earlier draft matrix:** ADK Go row 1 previously read "None (untyped
`map[string]any`)". That was factually wrong about the real framework. ADK Go's `session.Service`
*does* key by a composite `(appName, userID, sessionID)` — real construction. The accurate gap is
that it binds **no tenant** and does not bind identity to an authenticated caller, so it is
`CONVENTION`, not `NOT-POSSIBLE`. Corrected above and in the isolation-model table.

## Real integrations vs. honest simulations

All claims are grounded in code that was fetched, built, and run. Six of seven arenas are **REAL
integrations** against the pinned upstream framework; one (Letta) is a **partial** integration —
real SDK types, but the isolation proof runs against a clearly-labelled behavioral simulation of
the server data plane because Letta OSS is a server that needs a live DB + provider key this
offline env lacks.

| Arena | Integration | What is real | Honesty caveat |
|---|---|---|---|
| Pydantic AI | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| Mastra | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| LangGraph | **REAL** | Real framework + stub LLM; smoke + tests run offline | — |
| Eino | **REAL** | `cloudwego/eino@v0.9.12` pinned; real `Chain`/`react.NewAgent`/`CheckPointStore`; `StubChatModel` implements the real `model.ToolCallingChatModel`. `go build`/`vet`/`test` (6/6) + smoke all exit 0; leak proven through the real runtime | Only the ChatModel is a keyless stub; the runtime, graph, and checkpoint store are the genuine framework |
| ADK Go | **REAL** | `google.golang.org/adk/v2@v2.0.0`; real `llmagent.New` + `runner.Run` + `session.InMemoryService()`; `FakeModel` implements the real `model.LLM`. `gofmt`/`vet`/`build`/`test` clean; HTTP server booted on :4114, composite key isolated over real HTTP | Only the model is a keyless fake; session/state is genuine construction |
| Genkit Go | **REAL** | `firebase/genkit/go@v1.10.0`; real `genkit.Init`/`DefineFlow`/`Generate`/`GenerateData[T]`/`DefineTool` + dotprompt; fake model via `DefineModel` (no built-in stub exists). `build`/`vet`/`test`/smoke run; leak proven through the real `Generate` pipeline; live server emits real SSE | Only the model is a keyless fake; observability schema confirmed `genkit:*` (no `gen_ai.*`) against v1.10.0 source |
| Letta | **PARTIAL** | `letta-client==1.12.1` real SDK; real `BlockResponse`/`AgentState`/`Passage` types; payloads validated against real `AgentCreateParams`; 7/7 pytest + smoke exit 0 | Isolation smoke is a **labelled** simulation of the server's block/passage plane (real SDK types, real filter contract); no live `agents.create` turn ran — Letta OSS needs a running server + DB + LLM key. Live path only activates with `LETTA_BASE_URL` and pings honestly |

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

## Bottom line

**Isolation is `CONVENTION` / developer-passed in all seven frameworks — the Go-native cluster
included — and `BY CONSTRUCTION` only in aisotropy.** This is now proven across **7 frameworks in 3
languages** (Python, TypeScript, Go). Language is not the variable: Eino, ADK Go, and Genkit Go are
the same Go as aisotropy's core, and all three still leave tenancy as a string the developer must
never fumble. In every arena the leaky read and the correct read are one line — one string, one
keyword, one dropped token — apart, and the type system does nothing to stop the leak. In aisotropy
the leaky path does not exist: `tenant_id` travels in `context.Context` and every memory read is
scoped per-statement/per-key, so no code path can issue an unscoped read.

Two arenas cost aisotropy an honest concession: **ADK Go ties on OTel `gen_ai` observability**, and
**Letta validates the memory-OS thesis** (isomorphic tiers + productized sleep-time). Neither closes
the isolation lane — ADK leaves the whole tenant dimension to discipline, and Letta's per-user key
isolation is a paid-cloud feature. The open lane stands: enforced multi-tenant isolation is
something none of the seven builds, and aisotropy makes it unrepresentable-to-get-wrong.

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
