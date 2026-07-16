---
type: Feature matrix
title: "Flue — 41-feature classification vs aisotropy"
description: Coverage-class map of Flue 1.0.0-beta.9 against the aisotropy runtime catalog, grounded in a real offline-run integration plus shipped-source and flue-docs verification.
tags: [arenas, flue, features, comparison, durable-execution]
timestamp: 2026-07-16T00:00:00Z
---

# Flue — Features vs aisotropy

**Coverage classes:** `BUILT-IN` (ships, works out of the box) · `CONVENTION` (framework gives a
primitive but scoping/correctness is the developer's responsibility) · `PARTIAL` (present but
incomplete or backend-tethered) · `DIY` (you build it) · `PAID` (proprietary product) ·
`NOT-POSSIBLE` (framework cannot provide it by construction).

**Grounding:** `[RAN]` = observed by executing the real framework offline (`pnpm run smoke` /
`isolation`, exit 0). `[SRC]` = verified from shipped `@flue/runtime` `.d.ts` / bundle. `[DOC]` =
verified from `flue docs read <page>` (the docs shipped in the installed CLI). No class below is
asserted from general knowledge.

Flue is a lean **durable agent harness**, not a multi-tenant runtime — so like Eino it lands few
`BUILT-IN`s and many `DIY`s, but it carries **one distinctive axis the field mostly lacks: durable,
crash-safe conversation memory + a durable submission queue.**

## Headline findings

1. **Durable execution → crash-safe CONVERSATION memory: real, BUILT-IN, run-verified.** `[RAN]` A
   second OS process recalled a secret the first (killed) process wrote, replayed from the durable
   canonical stream on a file-backed SQLite adapter. This is Flue's genuine edge.
2. **Memory TIERS: DIY.** `[DOC]` Flue's database stores the conversation stream, accepted
   submissions, attachments, and workflow runs — explicitly **not** "application-owned business data
   unless your own tools store it." There is no facts/FTS tier, no vector tier, no sleep/batch tier.
3. **Multi-tenancy: CONVENTION, not construction.** `[RAN][DOC]` The only external scoping axis is the
   opaque agent-instance `id`; tenant/user/product are substrings the developer folds in, and auth is
   a `route` guard the developer writes. Cross-product leak reproduced through the real runtime.

## Feature-by-feature (43 rows: 41 catalog + multi-product + license posture)

### Area 1 — Memory tiers

| # | Feature | Class | Evidence |
|---|---|---|---|
| 1.1 | Temperature hierarchy (4 tiers of different nature) | **NOT-POSSIBLE** | `[DOC]` One store only — the canonical conversation stream. No FTS/vector/batch tiers exist to organize by temperature. |
| 1.2 | Working memory (recent history, in-process) | **BUILT-IN** | `[RAN]` The per-instance canonical stream is auto-rebuilt into model context each turn (`seen_msgs` grew 2→4 across turns); durable, not µs-KV — it's the *only* tier. |
| 1.3 | Facts / episodic KEYWORD (FTS5) journal | **DIY** | `[RAN]` Orbit's `src/shared/tickets.ts` is a hand-rolled tuple-keyed map; Flue ships no facts tier. |
| 1.4 | Semantic / VECTOR retrieval (RAG) | **DIY** | `[SRC]` No vector store or retriever type in `@flue/runtime`. BYO in your data layer. |
| 1.5 | Engram dual-index (fact ↔ vector) | **DIY** | `[SRC]` No fact or vector primitive to dual-index. |
| 1.6 | Summary buffer / context engineering | **BUILT-IN** | `[SRC]` `CompactionConfig` (`reserveTokens`, `keepRecentTokens`, summarizer `model`) + `session.compact()`; automatic threshold compaction folds old messages into a summary. |
| 1.7 | Engram scoring (Rel×Imp×Rec, top-K) | **DIY** | `[SRC]` No relevance/importance scoring; the stream is replayed, not ranked. |
| 1.8 | Contradiction detection / supersede | **DIY** | `[SRC]` No supersede primitive. |
| 1.9 | Knowledge ingestion (user + tenant-shared) | **DIY** | `[SRC]` No ingestion/chunking surface. |
| 1.10 | Primitive self-improvement (friction) | **DIY** | `[SRC]` No analog. |
| 1.11 | BYO embedder | **DIY** | `[SRC]` No embedder seam (no vector tier); bring your own in a DIY retriever. |

### Area 2 — Contract & structured output

| # | Feature | Class | Evidence |
|---|---|---|---|
| 2.1 | Struct-as-contract (1 def → HTTP + OpenAPI + LLM) | **DIY** | `[SRC]` valibot schemas drive tool I/O + `prompt({result})`, but HTTP is Hono/manual and there's no OpenAPI autogen — three separate declarations, not one. |
| 2.2 | Fail-closed structured output (repair loop → 502) | **PARTIAL** | `[SRC]` `prompt<S>(text, {result: S})` returns validated typed output; tool-input validation failure returns a model-visible error the model can retry. A *bounded fail-closed 502 loop* is not a framework guarantee — not verified. |
| 2.3 | SSE streaming + immediate ctx-cancel | **BUILT-IN** | `[SRC][RAN]` Streaming over Durable Streams (`GET /agents/<name>/<id>`); `CallHandle.abort()` + `options.signal` cancel in-flight calls; `client.agents.abort()` HTTP abort. Fake model SSE path exercised. |
| 2.4 | Stateless /ask + response cache | **DIY** | `[DOC]` Everything is the durable stateful conversation; no stateless-ask or response-cache primitive. |

### Area 3 — Tools & agentic

| # | Feature | Class | Evidence |
|---|---|---|---|
| 3.1 | Scope-gated tools (governance by absence) | **DIY** | `[DOC][RAN]` Flue injects every tool you pass; the docs teach *closing tools over the id-derived scope* (Orbit does this), but hiding a tool when a scope is absent is your predicate. |
| 3.2 | Multi-step agent loop | **BUILT-IN** | `[SRC]` Pi-harness agent loop (`session.prompt`/`skill`/`task`, tool calling, subagents) is first-class. |
| 3.3 | Typed tools + MCP | **BUILT-IN** | `[SRC]` `defineTool` with valibot input/output; `connectMcpServer(...)` returns ordinary tools (`mcp__<name>__<tool>`). |
| 3.4 | Model-selection gateway (rules/cost/capability) | **DIY** | `[DOC]` Per-agent/per-prompt model override exists, but no cost/image/capability routing policy chain. |

### Area 4 — Isolation & auth (the crux)

| # | Feature | Class | Evidence |
|---|---|---|---|
| 4.1 | Multi-tenant isolation BY CONSTRUCTION | **CONVENTION** | `[RAN][DOC]` Conversation stream isolated per instance `id` by construction, but tenant/user/product are folded into that one opaque string by the dev. Distinct ids isolate; a dropped/typo'd segment leaks. |
| 4.2 | Value-object Scope (unforgeable) | **DIY** | `[SRC]` The `id` is a bare string; `AgentDispatchRequest = {id, input}`. No scope type, no unexported fields. |
| 4.3 | Qdrant in-scan payload filter | **DIY** | `[SRC]` No vector tier to filter. |
| 4.4 | Authenticated-identity seam (must-agree bind) | **CONVENTION** | `[RAN][DOC]` Flue provides the `route: AgentRouteHandler` seam and docs mandate "your application must verify that the caller can access the selected `id`"; the bind is developer code (Orbit's guard 401s on token≠id-identity). Flue authenticates nothing itself. |
| 4.5 | JWKS key rotation | **DIY** | `[SRC]` No JWT/JWKS primitive; verify in your route guard. |
| 4.6 | Guardrails (redact / inject / output) | **DIY** | `[SRC]` No guardrail surface. |
| 4.7 | Multi-product isolation (4th axis) — run-verified ▶ | **CONVENTION** | `[RAN]` `product_id` is not a Flue concept; folded into the `id`. Cross-product leak reproduced: a billing request routed to the analytics instance id read the analytics secret. |

### Area 5 — Cost & governance

| # | Feature | Class | Evidence |
|---|---|---|---|
| 5.1 | Per-tenant PRE-SPEND quota gate (429) | **DIY** | `[SRC]` No quota/token-bucket primitive; gate it yourself in the route/app. |
| 5.2 | Circuit breaker | **DIY** | `[SRC]` `DurabilityConfig` (maxAttempts/timeout) governs recovery, not a provider-failure breaker. |
| 5.3 | Quota-pressure model degradation | **DIY** | `[SRC]` No degrade seam. |
| 5.4 | Cost meter (per-tenant) | **PARTIAL** | `[RAN]` Per-call usage **including a cost estimate** is BUILT-IN — the response carried `usage.cost.total` (Pi catalog pricing). Per-tenant aggregation/`/costs` surface is DIY. Stronger than the token-only arenas. |
| 5.5 | Constitutional-gate governance surface | **DIY** | `[DOC]` Governance is app-layer discipline, not enforced gates. |
| 5.6 | License 100% permissive, self-hostable | **BUILT-IN** | `[RAN]` Apache-2.0 core; self-hostable on Node/Docker/Fly/etc. Caveat: the *managed durability* story leans on Cloudflare Durable Objects on that target. |
| 5.7 | Dependency license posture (scanned) ▶ | **PERMISSIVE + weak-copyleft build leaves** | `[RAN]` See scan below — 0 strong-copyleft, 0 restricted. |

### Area 6 — Observability

| # | Feature | Class | Evidence |
|---|---|---|---|
| 6.1 | OTel gen_ai spans w/ tenant_id native | **PARTIAL** | `[DOC]` `@flue/opentelemetry` + `guide/observability` export telemetry and run/agent activity; `tenant_id` on spans is DIY and gen_ai-semconv attribute parity is unverified here. |
| 6.2 | Health / readiness probes (no LLM spend) | **DIY** | `[RAN]` Orbit's `/health` is hand-added in `app.ts`; no built-in readiness that probes memory tiers. |
| 6.3 | Bits-per-token North Star | **DIY** | `[SRC]` No compression/memory-lift metric. |
| 6.4 | Product-evaluation harness (IR + judge) | **PARTIAL** | `[DOC]` `guide/evals` + `ecosystem/tooling/vitest-evals` give a real repeatable eval harness (LLM-as-judge via vitest-evals); IR metrics (P@k) + isolation-preserving eval are DIY. |

### Area 7 — Consolidation / durability

| # | Feature | Class | Evidence |
|---|---|---|---|
| 7.1 | Transactional outbox (atomic, idempotent) | **PARTIAL** | `[RAN][DOC]` `dispatch(...)` → one ordered durable submission queue → canonical stream, at-least-once, conservative crash recovery, app-owned idempotency keys — the closest any arena comes. But it durably couples *submission → conversation*, not *message + memory-tier consolidation-intent* in one txn (no memory tiers to consolidate into). |
| 7.2 | Sleep-cycle batch consolidation | **DIY** | `[SRC]` No consolidation/dedup/contradiction batch. |

### Area 8 — Sovereignty & runtime shape

| # | Feature | Class | Evidence |
|---|---|---|---|
| 8.1 | Single-binary zero-config runtime | **PARTIAL** | `[RAN]` `flue build --target node` → one `dist/server.mjs`; zero-config in-memory default runs with no db/key. But it's a Node bundle (not a static binary), and surviving host loss needs external Postgres + single-owner routing. |
| 8.2 | Dual surface (sealed kernel + SDK) | **PARTIAL** | `[SRC]` `@flue/runtime` (author) + `@flue/sdk` (consume deployed agents) is a real dual surface; there is no sealed isolation kernel. |
| 8.3 | Go+Rust subprocess boundary | **DIY** | `[DOC]` Single-language (TS/Node); heavy work runs in a sandbox (`local()`/E2B/etc.) as a subprocess/remote, but that's a workspace, not a polyglot compute boundary. |
| 8.4 | Provider-agnostic LLM registry | **BUILT-IN** | `[RAN][DOC]` Pi catalog (anthropic/openai/openrouter/cloudflare) + `registerProvider(...)` for any `openai-completions`/custom endpoint. Orbit's offline fake provider is exactly this path. |

### Tally (42 coverage-class rows)

| Class | Count | Rows |
|---|---|---|
| BUILT-IN | 6 | 1.2, 1.6, 2.3, 3.2, 3.3, 8.4 (+5.6 license) |
| PARTIAL | 7 | 2.2, 5.4, 6.1, 6.4, 7.1, 8.1, 8.2 |
| CONVENTION | 3 | 4.1, 4.4, 4.7 |
| DIY | 24 | 1.3–1.5, 1.7–1.11, 2.1, 2.4, 3.1, 3.4, 4.2, 4.3, 4.5, 4.6, 5.1–5.3, 5.5, 6.2, 6.3, 7.2, 8.3 |
| NOT-POSSIBLE | 1 | 1.1 |

Profile: **lean like Eino** (few BUILT-INs, DIY-heavy) but with a **PARTIAL cluster the leanest
arenas lack** — durable submission queue (7.1), per-call cost estimate (5.4), and a real eval harness
(6.4) — plus the genuinely BUILT-IN durable conversation memory (1.2/1.6) and provider registry (8.4).

## Multi-PRODUCT isolation — the 4th axis (run-verified ▶)

`scripts/isolation.mjs` (`pnpm run isolation`, exit 0) extends the tenant/user smoke to `product_id`
against the **real Flue runtime**. Flue exposes **one** scoping axis — the agent-instance `id`.
aisotropy keys memory on **four** native `context.Context` axes `(tenant,user,session,product)`.

Executed evidence:

- **(A) Positive + by-construction isolation** — distinct instance ids (`p=analytics` vs
  `p=billing`, and wrong tenant/user) get **distinct canonical streams**; none read another's secret. ✓
- **(B) Auth is developer-written** — a bearer token mismatched to the id's identity → **401**;
  missing token → **401**. The guard is Orbit's `route` export; Flue verifies nothing itself. ✓
- **(C) CROSS-PRODUCT LEAK (reproduced)** — a *billing* conversation routed to the *analytics*
  instance id read `ORBIT-SECRET-A`. Root cause: the `id` is an opaque string Flue binds to nothing;
  `product` is characters, not a scope. One typo (`analytics`↔`billing`) collapses two products. ✓

**Verdict:** `product_id` isolation is `CONVENTION` over one opaque id, not construction over four
axes. aisotropy filters `product_id` server-side on every read from an unforgeable `Scope` — the
wrong-product read is unrepresentable.

## Dependency LICENSE posture (run-verified ▶)

`pnpm licenses list` over the **real installed tree** (runtime + CLI + SDK + all transitive):

```
 220  MIT              13  ISC                  2  MPL-2.0          (weak-copyleft, build/CLI leaf)
  57  Apache-2.0        3  MIT OR Apache-2.0    1  LGPL-3.0-or-later (weak-copyleft, native leaf)
  18  BSD-3-Clause      2  BSD-2-Clause         1  LGPL-3.0          (weak-copyleft, native leaf)
   1  0BSD              1  BlueOak-1.0.0        1  Python-2.0 · 1 CC0-1.0 · 1 (MIT OR WTFPL) · 1 (BSD-2 OR MIT OR Apache-2.0)
                                               ── 323 packages total ──
```

| Bucket | Count | Notes |
|---|---|---|
| Permissive (MIT/BSD/Apache/ISC/0BSD/BlueOak/PSF/CC0 + permissive-OR) | **319** | — |
| Weak-copyleft (MPL/LGPL) | **4** | CAUTION not BLOCKER — all build/CLI-time **native leaves, used unmodified/dynamically-linked** |
| Strong-copyleft (GPL/AGPL) | **0** | — |
| Restricted / source-available (BUSL/SSPL/ELv2/Commons-Clause/FSL) | **0** | explicit grep = 0 hits |

The 4 weak-copyleft leaves: `lightningcss` + `lightningcss-darwin-arm64` (MPL-2.0, CSS transform via
Vite), `@img/sharp-libvips-darwin-arm64` (LGPL-3.0-or-later, libvips native), `node-liblzma`
(LGPL-3.0, xz native binding) — all pulled by the **build/CLI/Cloudflare toolchain**, none in the
Node agent runtime hot path, all used as unmodified dynamically-linked libraries/tools. Core `@flue/*`
are **Apache-2.0**.

**Verdict:** Flue's real tree **clears aisotropy's hard-blocker gate** (0 strong-copyleft, 0
restricted). It does not hold the *strict* "100% MIT/Apache" line by construction — BSD/ISC/PSF and 4
weak-copyleft build leaves appear — but nothing blocks self-hosted multi-tenant use. aisotropy alone
holds the strict MIT/Apache line by construction.

## Summary

Flue is a **durable agent harness**, and a good one: durable, crash-safe **conversation** memory and
a durable submission queue are real, BUILT-IN, and run-verified — the closest any arena comes to
aisotropy's crash-durability thesis for the conversation itself. But it is **not a multi-tenant
runtime**: there are no memory tiers (facts/vector/sleep), no per-tenant cost/quota governance, and
isolation is `CONVENTION` — one opaque instance `id` plus a developer-written `route` guard, with
tenant/user/product folded into a string the framework binds to nothing. **vs aisotropy:** Flue wins
on TS DX and durable-conversation ergonomics; aisotropy wins on tiered memory, isolation-by-
construction, per-tenant governance, and the atomic message+consolidation outbox.
