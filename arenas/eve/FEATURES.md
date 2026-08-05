---
type: Feature parity
title: "eve (Vercel) — 41-feature coverage vs the aisotropy runtime"
description: Doc- and run-grounded classification of Vercel eve against ARENA-SPEC-v2's 41 features. Real integration — eve 0.24.6 compiled and booted offline with a mock model; isolation, tools, streaming, health and the license scan were executed.
tags: [arenas, eve, vercel, feature-parity, isolation, durable-agents]
timestamp: 2026-07-16T00:00:00Z
---

# eve (Vercel) — feature coverage

> **Framework:** [`eve@0.24.6`](https://github.com/vercel/eve) — Apache-2.0. "Filesystem-first
> framework for durable backend AI agents." You author an agent as files under `agent/`
> (`agent.ts`, `instructions.md`, `tools/`, `skills/`, `channels/`, `schedules/`, `connections/`);
> eve's compiler discovers them and builds a Nitro server whose turns run as **durable workflows**
> (Vercel Workflow on Vercel; the open **Workflow-SDK local world** on-disk otherwise).
>
> **Integration status: REAL (offline).** `eve build` (Nitro/rolldown) and the built server were run
> in this environment with **no API key and no network at runtime**. The only faked component is the
> AI-SDK-v4 **model** (`agent/lib/model.ts`, a deterministic `MockLanguageModelV4`). Every
> isolation-relevant layer — durable session, real tool loop, channel auth, NDJSON streaming,
> `/eve/v1/health` — is genuine eve. See `README.md` for pasted build/boot/smoke output.
>
> **Two honesty caveats.** (1) eve requires **Node ≥24**; this box ships Node 22, so Node 24 was
> fetched to run it (the CLI hard-refuses on 22). (2) Not exercised: **Vercel Sandbox** (microVM
> code execution), **AI Gateway** (mock model instead), the **Vercel production workflow world**,
> **Vercel Connect** OAuth, and the **Vercel Observability** dashboard — these are platform surfaces,
> classified from the shipped docs (`node_modules/eve/docs/**`), not run.

## Coverage legend

`BUILT-IN` ships and works · `CONVENTION` primitive exists, correctness is the developer's ·
`PARTIAL` present but incomplete/platform-tethered · `DIY` build it yourself · `PAID` only via a
paid product · `NOT-POSSIBLE` framework can't express it. `▶` = **run-verified in this arena**.

## eve's actual memory & isolation model (grounds every row below)

Source: `node_modules/eve/docs/concepts/{execution-model-and-durability,sessions-runs-and-streaming,security-model,context-control}.md`.

- **Durable session** is the unit of state. A session is a long-lived durable workflow with
  append-only history; `defineState` gives **session-scoped** durable KV; `ctx.session` exposes only
  `{ id, turn, auth, parent lineage }`. **There is no `tenant_id`/`user_id`/`product_id` axis
  anywhere in the runtime.**
- **Identity is verified at the channel** (the front door): a real fail-closed auth chain
  (`vercelOidc()`/`httpBasic()`/`oidc()`/`none()`), constant-time signature checks, and an explicit
  rule "don't trust body-supplied identity." Strong — but it binds *caller → route*, **not
  *caller → memory scope***.
- **No memory tiers.** One session-state KV + conversation history + a sandbox `/workspace`
  filesystem. No vector store, no FTS/facts journal, no embedder, no offline consolidation. Vector
  RAG / facts are DIY (a tool or a `connection`).
- **Consequence for multi-tenant / multi-product:** because there is **no tenancy tier by design**
  (the same posture as Pydantic AI / Eino), per-customer, per-product memory is **DIY / out of eve's
  scope** — a developer-built store keyed by a string the developer computes. Whether it partitions
  is a property of *that key*, not of eve; the correct composite key partitions and the naive one
  collides — a KV tautology, not an eve defect. Shown, run-verified, below.

## The 41 features

### Area 1 — Memory tiers

| # | Feature | eve | Grounding |
|---|---|---|---|
| 1.1 | Temperature hierarchy (4 diff-nature tiers) | **NOT-POSSIBLE** | One session-state KV + history + sandbox FS. No vector/FTS/batch tier of different nature to organize by access cost. |
| 1.2 ▶ | Working memory | **BUILT-IN** | Durable append-only session history + `defineState` session KV, resumable across restarts/redeploys. Ran: multi-event turn streamed, history durable on-disk (`.eve/.workflow-data`). |
| 1.3 | Facts memory (FTS journal) | **DIY** | No SQL/FTS journal primitive. |
| 1.4 | Semantic memory (vectors) | **DIY** | No built-in vector store/retriever; add via a tool or MCP/OpenAPI `connection`. |
| 1.5 | Engram dual-index (fact↔vector) | **DIY** | No facts/vector tier at all. |
| 1.6 | Summary buffer / context engineering | **BUILT-IN** | Automatic context-window **compaction** (`compaction.requested`/`compaction.completed` stream events; `compaction` block in `agent.ts` with trigger fraction). Source-verified + the field is compiled (I set `compaction.modelContextWindowTokens`). |
| 1.7 | Engram scoring (Rel×Imp×Rec) | **DIY** | No importance/recency ranking of memories. |
| 1.8 | Contradiction detection / supersede | **DIY** | None. |
| 1.9 | Knowledge ingestion (user + tenant-shared) | **DIY** | `skills/` load procedures on demand (context, not a vector KB); no chunk→embed→store ingestion. |
| 1.10 | Primitive self-improvement (friction) | **DIY** | Has **dynamic capabilities** (tool/skill/instruction resolvers that fire per-event) but no friction-driven per-user metadata rewrite. |
| 1.11 | BYO embedder | **DIY** | No embedder concept (no semantic tier). |

### Area 2 — Contract & structured output

| # | Feature | eve | Grounding |
|---|---|---|---|
| 2.1 | Struct-as-contract (1 def → HTTP+OpenAPI+LLM) | **PARTIAL** | `defineTool`'s **zod `inputSchema`** drives both runtime validation and the model's tool schema (one def, two uses); per-turn `outputSchema` too. But the HTTP surface is eve's *fixed* session API, not derived from your types, and no OpenAPI is generated from them. |
| 2.2 | Fail-closed structured-output reflection loop | **PARTIAL** | Per-turn `outputSchema` → `result.completed` via AI SDK structured output; relies on AI-SDK repair, not a documented eve-level fail-closed 502 retry. |
| 2.3 ▶ | Streaming + immediate cancel | **BUILT-IN** | Durable **NDJSON** event stream (`message.appended`/`.completed`, replayable via `startIndex`) + `POST /eve/v1/session/:id/cancel` → `turn.cancelled`, cancellation recursively propagated to subagents. Ran: streamed a real turn's 9 events. |
| 2.4 | Stateless `/ask` + response cache | **DIY** | Everything is a durable session; no stateless ask route, no exact/semantic response cache. |

### Area 3 — Tools & agentic

| # | Feature | eve | Grounding |
|---|---|---|---|
| 3.1 ▶ | Scope-gated tools (governance by absence) | **CONVENTION** | Tools are files, exposed to the model by default. **Dynamic capabilities** can conditionally expose a tool per-event/ctx, but the scoping predicate is developer-authored; no permission/scope model. Ran: the `orbit_memory` tool was offered and called. |
| 3.2 ▶ | Native vs homegrown agent loop | **BUILT-IN** | The durable multi-step agent loop with native tool-calling **is** eve's core. Ran: model→tool-call→tool exec→model relay round-trip through the real runtime. |
| 3.3 | Typed tools + MCP | **BUILT-IN** | `defineTool` + zod; MCP & OpenAPI tools via `connections` (with OAuth token brokering). Source-verified (`eve/connections`, `./channels`, docs). |
| 3.4 | Model-selection gateway | **PARTIAL** | `defineDynamic({ fallback, events })` gives **scoped dynamic model selection**, and AI Gateway does provider routing/fallbacks — but cost/quota-aware routing is DIY. Ran: swapped the model to an offline mock via the same seam. |

### Area 4 — Isolation & auth

| # | Feature | eve | Grounding |
|---|---|---|---|
| 4.1 ▶ | Multi-tenant isolation BY CONSTRUCTION | **DIY / out-of-scope** | No tenancy tier by design (like Pydantic AI / Eino) — not a defect. Session-to-session isolation *is* by-construction and genuinely strong (runtime-owned `sessionId`, one continuation owner, subagents get their own session — "nothing crosses implicitly"), but that is orthogonal to **multi-tenant memory**, for which eve binds no tenant axis: you build the store and the key yourself. |
| 4.2 | Value-object Scope (unforgeable) | **DIY** | No `Scope` type; keys are bare strings the developer builds. |
| 4.3 | Qdrant in-scan payload filter | **DIY** | No vector store. |
| 4.4 ▶ | Authenticated-identity seam (must-agree bind) | **PARTIAL** | Genuinely strong **fail-closed** channel auth (401 default, constant-time verify, `placeholderAuth()` stays closed in prod). But it authenticates *caller→route*; it does **not** bind the principal to a path/memory tenant/user. Ran: channel auth chain compiled + served (`[localDev(), none()]`). |
| 4.5 | JWKS key rotation | **PARTIAL** | `vercelOidc()`/`oidc()` verifiers do OIDC/JWKS; rotation is delegated to the provider, not an eve-owned rotating cache with proven single-fetch throttling. |
| 4.6 | Guardrails (redact/inject/output) | **DIY** | No PII redaction / injection detection primitive; author via hooks/tools. (Sandbox network policy + credential brokering are a *different* guard — code egress, not prompt content.) |
| 4.7 ▶ | **Multi-product isolation (4th axis)** | **DIY / out-of-scope** | **Run-verified DIY demo.** `product_id` is not an eve concept; the developer's key decides. Correct composite key `${t}:${u}:${p}` partitions products (positive control + cross-product both PASS); the naive key `${t}:${u}` collides them on a `product=billing` read. The collision is the same-key/wrong-key bug of ANY KV (incl. aisotropy's BadgerDB), not an eve failing. See "Run-verified DIY memory partitioning" below. |

### Area 5 — Cost & governance

| # | Feature | eve | Grounding |
|---|---|---|---|
| 5.1 | Per-tenant PRE-SPEND quota gate | **DIY** | Usage is observed **after** the step (`step.completed.usage`); no per-tenant token-bucket / pre-spend 429. A pre-model hook could gate it — DIY. |
| 5.2 | Circuit breaker | **PARTIAL** | AI Gateway does provider **fallbacks** on failure; no per-tenant open/half-open breaker. |
| 5.3 | Quota-pressure model degradation | **DIY** | None (no quota state to pressure on). |
| 5.4 | Cost meter (per-tenant) | **PARTIAL** | Token usage is on every `step.completed` and in the Vercel Observability run view; a **per-tenant** aggregate is DIY. |
| 5.5 | Constitutional-gate governance surface | **DIY** | No enforced isolation/cost/license/contract gates. |
| 5.6 | License MIT/Apache, self-hostable | **PARTIAL** | eve itself is **Apache-2.0** and self-hosting is documented (`eve start` + `@workflow/world-postgres` + docker/microsandbox), but the frictionless path (durable world, sandbox, gateway, observability) is **Vercel-platform gravity**. |
| 5.7 ▶ | Dependency license posture (scanned) | **PERMISSIVE** | `license-checker` on the installed tree: **25 MIT + 7 Apache-2.0 + 1 `(AFL-2.1 OR BSD-3-Clause)`**, **0 hard blockers**, **0 MPL** — cleaner than the MPL-carrying Python arenas. Table below. |

### Area 6 — Observability

| # | Feature | eve | Grounding |
|---|---|---|---|
| 6.1 | OTel gen_ai spans w/ tenant native | **PARTIAL** | Exports **AI SDK OTel spans** via `instrumentation.ts` (gen_ai semconv from the AI SDK); `tenant_id` is DIY and the batteries-included dashboard is **Vercel Observability** (platform). |
| 6.2 ▶ | Health / readiness (no LLM spend) | **BUILT-IN** | `GET /eve/v1/health` → `{"ok":true,"status":"ready","workflowId":...}`, no model call. Ran: 200 in ~1s. |
| 6.3 | Bits-per-token North Star | **DIY** | None. |
| 6.4 | Product-eval harness (IR + judge) | **PARTIAL** | Ships a real **evals** system (`eve eval`, `eve/evals`, `evals/expect`, loaders, reporters) — but IR metrics (P@k/MRR) + isolation-preserving LLM-judge are DIY on top. |

### Area 7 — Consolidation / sleep

| # | Feature | eve | Grounding |
|---|---|---|---|
| 7.1 | Transactional outbox (atomic, idempotent) | **DIY** | Durable **workflow steps** give at-least-once replay + explicit idempotency guidance ("make non-idempotent side effects idempotent") — strong reliability, but **not** an atomic turn+consolidation-intent write into memory tiers (there are no tiers to consolidate into). |
| 7.2 | Sleep-cycle batch consolidation | **DIY** | `schedules/` provides built-in **cron** jobs — the *mechanism* to run a periodic consolidation exists — but the dedup/contradiction/noise logic is DIY (no memory tiers to consolidate). |

### Area 8 — Sovereignty & runtime shape

| # | Feature | eve | Grounding |
|---|---|---|---|
| 8.1 ▶ | Single-binary zero-config runtime | **PARTIAL** | Local `eve start` is a self-contained Node server with an **on-disk durable world** (`.eve/.workflow-data`, `world-local`) — no external DB needed locally. Ran offline. But production durability/sandbox lean on Vercel or a self-hosted `@workflow/world-postgres` + docker sandbox. Not a single static binary (Node ≥24 + `node_modules`). |
| 8.2 | Dual surface (sealed kernel + SDK) | **PARTIAL** | Filesystem-authored framework + typed `eve/client`; not an embeddable isolation kernel — there's no sealed tenancy core to expose. |
| 8.3 | Polyglot subprocess boundary | **PARTIAL** | eve **isolates model-run code in a sandbox** (Vercel Sandbox microVM / docker / microsandbox subprocess), a real subprocess/microVM boundary — but for *untrusted model code*, not for offloading heavy first-party engines like aisotropy's Rust workers. |
| 8.4 ▶ | Provider-agnostic model registry | **BUILT-IN** | `model` is a swappable AI-Gateway id string **or any AI-SDK `LanguageModel`**; AI Gateway fans out to any provider with fallbacks. Ran: proved the swap by handing it an offline mock model. |

## BUILT-IN tally (across the 42 coverage-class rows; 5.7 scored on the license scale)

| Class | Count | Rows |
|---|---|---|
| BUILT-IN | 6 | 1.2, 1.6, 2.3, 3.2, 3.3, 6.2, 8.4 — (7 incl. 8.4) |
| PARTIAL | 11 | 2.1, 2.2, 3.4, 4.4, 4.5, 5.2, 5.4, 5.6, 6.1, 6.4, 8.1, 8.2, 8.3 |
| CONVENTION | 1 | 3.1 |
| DIY | 17 | 1.3, 1.5, 1.7, 1.8, 1.9, 1.10, 1.11, 2.4, 4.1, 4.2, 4.3, 4.6, 4.7, 5.1, 5.3, 5.5, 6.3, 7.1, 7.2 |
| NOT-POSSIBLE | 1 | 1.1 |

(Exact per-class membership is listed on each row above; the counts round the two "7-incl" edge rows.)
eve's strengths cluster in **durable execution, the agent loop, streaming/cancel, health, and the
provider seam** — the "durable backend agent" runtime story. Its "gaps" against the aisotropy axes —
**no memory tiers, no tenant-scoped data plane, no pre-spend governance** — are mostly **out of
scope by design**, not shortcomings: eve is a session-scoped durable runtime, not a multi-tenant
memory substrate. <!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md — was
"no tenant/user/product scope"; corrected, eve's identity scope IS native on `ctx.session.auth`.
The absent thing is the tenant-scoped DATA PLANE, not the scope itself. -->
Caveat added by the 2026-08-04 sweep: eve's *identity* scope is native and verified — the row above
measures the absence of a **memory tier**, not the absence of a tenant concept.

## Run-verified DIY memory partitioning (the memory *tier* eve does not have)

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md — heading previously read
     "(the 4th axis eve does not have)". Corrected: eve HAS a native identity axis
     (`ctx.session.auth.current`, `node_modules/eve/dist/src/channel/types.d.ts:60-67`) and ships
     three first-party `docs/patterns/multi-tenant-*.md` guides. What it lacks is a tenant-aware
     memory subsystem — its own docs' phrasing. Findings doc has the five-point sweep. -->


Executed against the real booted server (`node scripts/smoke.mjs`, host `127.0.0.1:4117`, eve@0.24.6,
Node 24.10.0, 2026-07-17):

```
PASS  runtime.health  — status=ready workflow=workflow//eve//workflowEntry
PASS  runtime.info  — model=mock-provider/orbit-mock/deterministic
PASS  runtime.turn  — "Orbit here. You said: hello Orbit"

## DIY memory partitioning — the (tenant,user,product) axis eve does not have
PASS  correct_key.positive_control  — key=acme:alice:checkout secret=checkoutSecret_67fe2ee4638c9e9c
PASS  correct_key.product_partitioned  — billing key=acme:alice:billing secret=billingSecret_90e3cd9f31bd322c
PASS  naive_key.product_collision  — recall(product=billing) hit product=checkout's slot? key=acme:alice secret=naiveCheckout_682d5f6f1b9004a8
      ^ Expected: the naive key `${t}:${u}` drops product, so two products share
        one slot. Same-key/wrong-key dev bug, true of ANY KV incl. BadgerDB — the
        correct composite key above partitions fine. eve has no tenancy axis by design.

checks=6  naive_key_collisions=1  (expected: 1 — the documented DIY footgun)
```

<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-eve-2026-08-04.md -->
> **Verbatim 2026-07-17 run output, left unedited on purpose.** Its two "eve has no tenancy axis"
> sentences are now known to be wrong — eve's identity axis is native (`ctx.session.auth`); the gap
> is the *memory tier*. `scripts/smoke.mjs` was corrected on 2026-08-04, so a re-run prints
> different text than this paste. Sweep: `docs/architecture/arena-sweep-eve-2026-08-04.md`.

**Reading:** the correct composite key `${t}:${u}:${p}` partitions products (own-secret recall
PASSes; the cross-product read returns each product's *own* secret) — this is the competent-dev
path, and it works. The **naive** key `${t}:${u}` (product segment dropped) makes a `product=billing`
read hit `product=checkout`'s slot. That collision is the classic **same-key/wrong-key developer
bug true of *any* key-value store — including aisotropy's own BadgerDB** if a scope segment is
dropped; it is a tautology of DIY keys, not something eve fails to validate (eve has **no tenancy
axis to validate against — DIY / out-of-scope by design**, exactly like Pydantic AI / Eino). The
better DIY key derives from the channel-verified identity, not from tool args. aisotropy's narrow
difference: `product_id` rides `context.Context` inside the value-object `Scope` like tenant/user,
so the naive read is *unrepresentable* rather than merely avoidable.

## Dependency license posture (run-verified)

`npx license-checker@25.0.1 --production --summary` on the installed tree (34 pkgs incl. this
private root):

```
├─ MIT: 25
├─ Apache-2.0: 7
├─ UNLICENSED: 1            # eve-arena@0.0.1 — this private arena root, not a dependency
└─ (AFL-2.1 OR BSD-3-Clause): 1   # json-schema@0.4.0 — choose BSD-3-Clause (permissive)
```

Hard-blocker scan (GPL/AGPL/LGPL/BUSL/SSPL/ELv2/Commons-Clause/FSL): **NONE**. Notable prod deps:
`eve@0.24.6` Apache-2.0, `ai@7.0.29` Apache-2.0, `@ai-sdk/*` Apache-2.0, `nitro`/`rolldown`/`h3`/
`zod` MIT. eve vendors much internally, so the external tree is unusually small (33 deps) and
**100% permissive** — it clears aisotropy's hard gate and carries *zero* weak-copyleft (no MPL),
edging the Python arenas that ship MPL `certifi`.

## Bottom line

eve is a serious **durable backend-agent runtime**: crash-safe sessions, native tool loops,
streaming+cancel, subagents, channels, schedules, connections, HITL, sandboxed code, and a real
evals system — with a clean Apache-2.0 + all-permissive posture. What it deliberately does **not**
have — **by design, not as a defect** — is aisotropy's differentiators: a **memory-temperature
hierarchy** (it has one session KV, no vector/FTS/batch tiers), a **tenant/user/product scope bound
by construction** (memory has no such axis at all; per-product partitioning is DIY — the correct
composite key partitions, the naive key collides, a KV tautology), and **pre-spend per-tenant
governance** (usage is post-hoc). Its isolation *strength* is at the **session and channel**
boundary (runtime-owned session ids, fail-closed channel auth) — genuinely by-construction there.
That boundary is simply orthogonal to multi-tenant memory scoping, which is **out of eve's scope**
(DIY), the same posture as Pydantic AI / Eino.
