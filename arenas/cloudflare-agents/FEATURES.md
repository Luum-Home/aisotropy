---
type: Feature matrix
title: "Cloudflare Agents — features vs aisotropy"
description: Coverage map of the Cloudflare Agents SDK (agents@0.17.4, Durable Objects + embedded SQLite) against aisotropy's 43-feature catalog. Grounded in what actually ran in workerd plus source-verified SDK inspection.
tags: [arenas, cloudflare-agents, durable-objects, features, comparison]
timestamp: 2026-07-16T00:00:00Z
---

# Cloudflare Agents — Features

> **Framework:** `agents` 0.17.4 (MIT) on Cloudflare Workers + Durable Objects.
> **What ran:** the full comparison below is grounded in code that **executed in real
> `workerd`** (via `@cloudflare/vitest-pool-workers` and `wrangler dev`), plus **source
> inspection** of the installed `agents` dist (`.d.ts`) for capabilities not exercised here.
> Each row is tagged **[RAN]** or **[source-verified, not executed]**.

**Coverage classes** (same legend as the other arenas): `BUILT-IN` · `CONVENTION` (framework
gives a primitive, correctness is the developer's job) · `PARTIAL` · `DIY` · `PAID` ·
`NOT-POSSIBLE`.

## The one-paragraph headline

The Agents SDK is a lean **actor runtime**, not a batteries-included memory/governance
framework. Its distinctive asset is that **each Agent is one Durable Object with its own
embedded SQLite** — so the per-object storage boundary is a **hard platform guarantee**
(a DO physically cannot read another DO's SQLite; there is no cross-object `SELECT`). That
is a **stronger** storage boundary than any of the other seven arenas' shared string-keyed
stores. **Two honest caveats temper it:** (1) the mapping from `(tenant,user,product)` to
*which* object is a **developer-assembled name string** — build the wrong name (drop the
product axis) and two products address one object; that is a *same-key developer bug*, not a
framework defect, and the SDK ships a first-class `onBeforeConnect` seam to bind the name to a
verified identity (demonstrated below, PART C); and (2) the whole guarantee is a **rented
Cloudflare-platform boundary**, not self-hostable OSS. Almost everything else in the catalog —
facts scoring, guardrails, pre-spend quota, cost meter, gen_ai telemetry — is DIY on top of
the runtime.

## What actually executed (closed loop)

```
pnpm add agents            -> agents@0.17.4 (MIT)
pnpm add -D wrangler @cloudflare/vitest-pool-workers vitest
pnpm exec vitest run       -> 10 passed (real workerd / miniflare, offline, no API key)
wrangler dev --local + curl -> Orbit HTTP route serves; per-object memory hydration observed
./scripts/license_scan.sh  -> 0 hard-blocker licenses (PASS)
```

Isolation smoke (`test/isolation.test.ts`, 9/9: PART A object boundary ×4, PART B
naive-convention same-key bug ×2, PART C strong identity-bound path ×3) and the DO-SQLite FTS5
probe (`test/probe.test.ts`, 1/1) both ran inside genuine Durable Objects. See the README for
pasted output.

## Full 43-feature parity table

`R` = executed in workerd here · `S` = source-verified in the installed SDK, not executed.

| # | Feature | Class | Evidence |
|---|---|---|---|
| 1.1 | Temperature-tier hierarchy | **PARTIAL** | R+S — ONE in-process per-DO SQLite (KV+relational+FTS5, same store) is the only built-in tier. Vector = separate **rented Vectorize** product; **no** offline-batch tier. aisotropy = 4 stores of different nature |
| 1.2 | Working memory (recent history, in-process) | **BUILT-IN** | R — `this.sql` is embedded in the DO, zero network. Proved hydration: turn 2 saw `hydrated:2`; a sibling object saw `0` |
| 1.3 | Facts memory w/ keyword/FTS search | **DIY** | R — framework ships raw `this.sql` only; you build the journal. **FTS5 IS available** in DO SQLite (probe: `{"ok":true,"hit":"production uses aws lambda"}`), so it's DIY-on-a-real-primitive — *stronger* than LangGraph's NOT-POSSIBLE |
| 1.4 | Semantic / vector RAG | **DIY** | S — core SDK has no vector store. BYO Cloudflare **Vectorize** (separate rented product) or external. Isolation there is metadata-filter CONVENTION |
| 1.5 | Engram dual-index (fact↔vector) | **DIY** | S — no analog |
| 1.6 | Summary / rolling buffer | **PARTIAL** | S — `agents/experimental/memory/session` ships `AgentSessionProvider` + compaction helpers (experimental); rolling summary is opt-in/experimental, not default |
| 1.7 | Importance×recency×relevance scoring | **DIY** | S — hand-rolled over your own SQLite |
| 1.8 | Contradiction detection / supersede | **DIY** | R — `INSERT OR REPLACE` is key-upsert; semantic supersede is your code |
| 1.9 | Knowledge ingestion (chunk+embed) | **DIY** | S — AI Search / AutoRAG is a separate rented product; nothing at the SDK layer |
| 1.10 | Primitive self-improvement (friction) | **DIY** | S — no analog |
| 1.11 | Bring-your-own embedder | **DIY** | S — BYO (Workers AI binding or external); no embedder seam in the SDK |
| 2.1 | Struct-as-contract (1 struct → HTTP+OpenAPI+LLM) | **DIY** | S — TS types are compile-time only; no OpenAPI generation, no runtime validation contract |
| 2.2 | Fail-closed reflection loop (502) | **DIY** | S — SDK is model-agnostic; structured-output repair is whatever LLM lib you plug (ai-sdk). No built-in fail-closed loop |
| 2.3 | SSE / token streaming + immediate cancel | **BUILT-IN** | S — `StreamingResponse`, `AIChatAgent`, WebSocket-hibernation streaming are first-class; DO request abort cancels. (Streaming not exercised in this arena; HTTP req/resp was) |
| 2.4 | Stateless /ask + response cache | **DIY** | S — no built-in per-tenant response cache (Workers Cache API is a DIY substrate) |
| 3.1 | Scope-gated tools (governance by absence) | **DIY** | S — you filter the tool list yourself before the model call |
| 3.2 | Multi-step agent loop | **PARTIAL** | S — SDK provides the runtime + tool plumbing + `codemode`; the reason→act loop comes from the ai-sdk you integrate |
| 3.3 | Typed tools + MCP | **BUILT-IN** | S — MCP **client AND server** are first-class (`agents/mcp`, `McpAgent`, OAuth provider). Genuine strength |
| 3.4 | Model-selection gateway | **DIY** | S — Cloudflare **AI Gateway** (rented) does provider routing/fallback; nothing at the SDK layer |
| 4.1 | Isolation BY CONSTRUCTION | **CONVENTION** | R — see the crux section. Per-**object** storage boundary is by-construction & unforgeable (**BUILT-IN and stronger than all 7 arenas** — credited in prose); the row scores the *identity binding*, which is a developer-assembled **name string** (naive path) with a first-class `onBeforeConnect` seam (strong path, PART C); the whole boundary is **rented** platform |
| 4.2 | Value-object Scope (unforgeable) | **DIY** | R — the DO name is a bare `string`; nothing unforgeable at the *type* level. The strong path (PART C) mints the name from verified claims, but a dropped axis is a runtime bug, not a compile error — aisotropy's `Scope` value-object is the edge here |
| 4.3 | In-scan vector payload filter (not post-filter) | **DIY** | S — no vector tier in the SDK; Vectorize filtering is CONVENTION |
| 4.4 | Authenticated-identity seam (must-agree bind) | **CONVENTION** | R+S — the SDK ships the seam: routing entry points run `onBeforeConnect`/`onBeforeRequest` before the DO wakes (source-verified: `getAgentByName` deliberately does **not** — "the caller is assumed to have performed access checks"). PART C **RAN** a must-agree bind (name from verified claims, 403 on disagreement). Binding it to a real principal is your middleware — same shape as aisotropy's ADR-0024 |
| 4.5 | JWKS key rotation | **DIY** | S — no analog |
| 4.6 | Guardrails (redact/inject/output) | **DIY** | S — no analog |
| 4.7 | Multi-product isolation (4th scope axis) | **CONVENTION** | R — with the correct name, `productY` is a physically separate object (PART A, `hydrated:0` — the *storage* isolation is BUILT-IN). `product_id` is one more segment of the name: drop it → two products address ONE object (`collided === SECRET`), a same-key developer bug — not a framework defect. PART C's identity-bound naming makes the wrong name unbuildable |
| 5.1 | Per-tenant PRE-SPEND quota gate (429) | **DIY** | S — no token bucket; build it on a DO + the Rate Limiting binding |
| 5.2 | Circuit breaker | **DIY** | S — no analog |
| 5.3 | Quota-pressure model degradation | **DIY** | S — no analog |
| 5.4 | Cost meter (per-tenant) | **DIY** | S — AI Gateway logs usage (rented); per-tenant meter is your code |
| 5.5 | Constitutional-gate governance surface | **DIY** | S — none of these are enforced gates |
| 5.6 | License 100% MIT/Apache, self-hostable | **PARTIAL** | R+S — SDK is **MIT**, runtime `workerd` is **Apache-2.0** (open, runs locally via miniflare), **but production Durable Objects run ONLY on Cloudflare** — the durability + global-uniqueness + per-object isolation guarantee is a **rented platform**, not self-hostable OSS |
| 5.7 | Dependency license posture (scanned) † | **PERMISSIVE + weak-copyleft leaves** | R — `pnpm licenses`: prod tree 184 pkgs, **0 hard blockers**; 2× **MPL-2.0** (`lightningcss`) = CAUTION. Full tree 237 pkgs adds 1× **LGPL-3.0** (`@img/sharp-libvips`, dev tooling only). Passes the hard gate; not "100% MIT/Apache" |
| 6.1 | OTel gen_ai spans w/ tenant_id native | **DIY** | S — SDK "observability" is a **diagnostics-channel event bus** (`ObservabilityEvent`, lifecycle/MCP events), **not** gen_ai OTel spans. Platform AI Gateway captures LLM telemetry (rented); `tenant_id` DIY |
| 6.2 | Health / readiness probe (no LLM spend) | **DIY** | R — I hand-wrote `/healthz`; no built-in readiness that probes tiers |
| 6.3 | Bits-per-token North Star | **DIY** | S — no analog |
| 6.4 | Product evaluation harness (IR + judge) | **DIY** | S — no analog |
| 7.1 | Transactional outbox (atomic msg+event) | **PARTIAL** | S — a DO is a single-threaded actor with **transactional SQLite storage**, so writing the turn + a consolidation intent in ONE object *is* atomic — **better** than the 5 arenas where this is NOT-POSSIBLE. But draining into a *shared* tier crosses object boundaries (no cross-DO txn), so the full outbox pattern is DIY |
| 7.2 | Sleep-cycle consolidation (offline batch) | **DIY** | S — `schedule()` (cron/alarms) is **BUILT-IN** and gives you the periodic trigger for free; the dedup/reconcile logic is your code. No batch engine |
| 8.1 | Single-binary zero-config runtime | **PARTIAL** | R — local dev is a single `workerd`; **production is a rented serverless platform**, the opposite of aisotropy's self-hostable single binary |
| 8.2 | Dual surface (sealed kernel + SDK) | **PARTIAL** | S — composable SDK, but no sealed multi-tenant kernel; isolation is the developer's to assemble |
| 8.3 | Go+Rust subprocess boundary | **NOT-POSSIBLE** | S — Workers isolates cannot spawn OS subprocesses; heavy compute goes to a separate rented Worker/Container/Queue, not a stdio subprocess |
| 8.4 | Provider-agnostic LLM seam | **PARTIAL** | S — model-agnostic by BYO (ai-sdk / Workers AI / OpenAI); no built-in registry object; AI Gateway is the rented multi-provider path |

**Tally (42 coverage rows, 5.7 excluded as a license-scale row):**
BUILT-IN **3** · PARTIAL **8** · CONVENTION **3** · DIY **27** · PAID **0** · NOT-POSSIBLE **1**.
(4.4 authenticated-identity seam moved DIY→CONVENTION: the SDK ships the `onBeforeConnect` hook;
verifying + binding a principal to the name is the developer's job — the CONVENTION definition.)

The 3 genuine BUILT-INs — **in-process working memory (per-DO SQLite)**, **streaming**, and
**MCP client+server** — plus one under-appreciated PARTIAL (**transactional single-DO
storage**, which makes the outbox *possible* where 5 arenas can't) are the SDK's real
strengths. Everything memory-semantic and governance-shaped is DIY.

## The crux — isolation: by-construction *object* boundary, convention *identity* binding, rented platform

This is the finding the arena exists to nail. Three claims, all executed in `workerd`:

**1. The per-object storage boundary is BY CONSTRUCTION and unforgeable — stronger than every other arena.**
Two Agents with different names are two different Durable Objects, each with its **own
embedded SQLite**. There is **no API to `SELECT` across Durable Objects** — a cross-object
read is *unrepresentable*, not merely discouraged. Proven:

- `acme|alice|productX` writes a secret → `globex|alice|productX` reads `null` (physically
  separate SQLite).
- Same tenant+user, correct `productY` name → third distinct object, reads `null`.
- Memory hydration stays private: productY's first turn sees `hydrated:0` and none of
  productX's transcript.

Contrast: the other seven arenas keep all tenants in **one shared store** behind a
string/prefix key, so the leaky read and the correct read are one line apart *in the same
store*. Cloudflare removes the shared store entirely — the boundary is the object itself.

**2. The identity→object MAPPING is developer convention — but the SDK ships a seam to bind it.**
The object is addressed by a **name the developer assembles** (`getAgentByName(ns, name)`), and
the framework binds that name to nothing. The naive path (PART B) shows the failure mode:

- **Drop `product_id`** from the name → `acme|alice` → productX and productY address **one**
  object and share one SQLite → `collided === SECRET`. This is a **same-key developer bug**,
  tautologically true of any name→object map (aisotropy's own `Scope` included) — *not a
  framework defect*.
- **Wrong product substitution** → a handler *intending* productY builds the name with
  productX and reads productX. This is weaker still: asking for the wrong object and getting it
  is **addressing, not a leak** — no cross-object read occurs. It is a tautology of any keyed
  store.

So the failure *class* is identical to Eino's dropped checkpoint segment or LangGraph's short
prefix: a mis-assembled key. Two differences, both in Cloudflare's favour: (a) each key lands
in a **separately-isolated** store, not a shared one; and (b) the SDK provides a first-class
fix. **Source-verified in the installed `.d.ts`:** `getAgentByName` deliberately does **not**
run `onBeforeConnect` — "the caller is assumed to have performed whatever access checks are
needed" — because that check belongs on the *routing* path (`routeAgentRequest` /
`onBeforeConnect` / `onBeforeRequest`, from `partyserver`'s `PartyServerOptions`). The strong
path (PART C, RAN) uses it: verify a credential → derive the DO name from the **verified
claims** → must-agree-reject a URL scope that disagrees (403, before any object is addressed).
That makes the wrong name unbuildable. What Cloudflare still lacks vs aisotropy is a *type*-level
value-object: the name is a string, so a dropped axis is a runtime bug, not a compile error.

**3. The whole guarantee is RENTED, not self-hostable.**
Durable Objects run only on Cloudflare's platform. `workerd` (Apache-2.0) + `miniflare`
simulate them locally for dev — which is how this arena ran offline — but the production
durability, global single-instance guarantee, and per-object isolation are **Cloudflare
platform properties you rent**, not an OSS runtime you self-host. aisotropy's isolation is a
property of *its own code + type system*, hostable anywhere.

**Net:** Cloudflare has the **strongest storage boundary** in the field (BUILT-IN, unforgeable,
physically separate SQLite per object — it simply beats aisotropy's shared-tier-behind-a-`Scope`
here), the **same identity-binding class** as the field (name convention, but with a first-class
`onBeforeConnect` seam to bind it), delivered on a **rented** platform. aisotropy's distinct
edge is elsewhere: it makes the identity→scope binding *itself* unrepresentable-to-get-wrong at
the *type* level (value-object `Scope` with unexported fields, `product_id` carried on
`context.Context`), ships four memory tiers + enforced governance, and runs in a binary you own.

## License scan (executable governance) — RUN-VERIFIED

`./scripts/license_scan.sh` (offline; `pnpm licenses list --json` → `scripts/classify-licenses.mjs`):

```
== PROD-ONLY TREE (ships to the edge: agents + transitive) ==
TOTAL packages: 184
157 MIT · 16 ISC · 4 BSD-3-Clause · 2 Apache-2.0 · 2 MPL-2.0 · 1 CC-BY-4.0 · 1 BSD-2-Clause · 1 BlueOak-1.0.0
HARD BLOCKERS: 0
WEAK COPYLEFT (CAUTION): 2  ->  MPL-2.0 :: lightningcss, lightningcss-darwin-arm64
PASS: zero hard-blocker licenses.

== FULL TREE (prod + dev tooling: wrangler, vitest, workerd) ==
TOTAL packages: 237
199 MIT · 17 ISC · 7 Apache-2.0 · 4 BSD-3 · 3 (MIT OR Apache-2.0) · 2 MPL-2.0 · 1 LGPL-3.0-or-later · 1 CC0 · 1 CC-BY-4.0 · 1 BSD-2 · 1 BlueOak
HARD BLOCKERS: 0
WEAK COPYLEFT (CAUTION): 3  ->  LGPL-3.0-or-later :: @img/sharp-libvips-darwin-arm64 (dev tooling); MPL-2.0 :: lightningcss ×2
PASS: zero hard-blocker licenses.
```

**Verdict:** the `agents` SDK itself is **MIT**. The tree clears aisotropy's *hard* blocker
gate (0 GPL/AGPL/SSPL/BUSL/ELv2/Commons-Clause/FSL) but is **not** purely permissive:
the shipped tree carries **MPL-2.0** (`lightningcss`, weak-copyleft leaf, CAUTION under
aisotropy's `license-policy.md`), and the dev toolchain adds one **LGPL-3.0** leaf
(`@img/sharp-libvips`, build-time only, never deployed). Comparable posture to the MPL-leaf
arenas (Pydantic AI / LangGraph / Letta) — and to aisotropy's own tree, which likewise carries
one transitive MPL-2.0 leaf (`docs/architecture/license-audit.md`); both clear the same hard
gate. The bigger sovereignty caveat is not the licenses —
it's that the *runtime* is rented (row 5.6), which no license scan captures.

## Summary vs aisotropy

Cloudflare Agents wins the **storage-boundary** lane outright (physically separate SQLite per
object — no other arena removes the shared store) and ships genuine **MCP** + **streaming** +
**in-process memory**, on a very clean DX. It loses the lanes aisotropy is built for:
**identity→scope binding stays developer-convention** (a mis-assembled name is a same-key bug,
not a value-object the type system guards — though PART C shows the SDK's seam to bind it),
**governance is entirely DIY** (no pre-spend gate, cost meter, guardrails, gen_ai telemetry,
constitutional gates), the **memory hierarchy is one SQLite** (not four tiers of different
nature), and the whole thing is a **rented platform**, not a self-hostable sovereign runtime.
The trade: the best isolation *substrate* in the field, rented, with everything above the
substrate left to you — vs. aisotropy's four-tier memory, by-construction identity scoping,
and enforced governance in a binary you own.

† Row 5.7 is scored on a license-posture scale, not the coverage-class legend, so it is
excluded from the BUILT-IN tally.
