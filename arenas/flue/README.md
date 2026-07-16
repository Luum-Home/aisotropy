---
type: Arena
title: "Flue — Orbit on a durable-execution agent framework (TypeScript)"
description: Real, offline-run integration of the Orbit multi-tenant support chatbot on Flue 1.0.0-beta.9 (the Astro team's durable-agent framework, built on the Pi harness). Tests whether durable execution buys crash-safe memory and whether multi-tenancy is by-construction or by-convention.
tags: [arenas, flue, durable-execution, typescript, isolation, competitive, pi-harness]
timestamp: 2026-07-16T00:00:00Z
---

# Flue arena — Orbit on a durable-execution agent framework

> **Framework:** [Flue](https://flueframework.com) `@flue/runtime@1.0.0-beta.9` (+ `@flue/cli`,
> `@flue/sdk`), Apache-2.0, from the Astro team (Fred K. Schott). Built on the **Pi** harness
> (`@earendil-works/pi-ai@0.80.7`), a Vite/rolldown build, and **Durable Streams**.
> **Language:** TypeScript / Node 22. **Integration:** **REAL** — installed, built, and **run
> offline** (fake OpenAI-compatible model, no API key). Both smokes exit 0.

## What Flue is, and why it's in the arena

Flue is a *durable-execution* agent framework. Its thesis: every agent instance owns **one
append-only canonical conversation stream**; direct HTTP prompts and async `dispatch(...)` inputs
enter one ordered durable queue → the stream; and when a process dies, a replacement replays that
stream and **continues from the last durable step** (at-least-once, conservative recovery, app-owned
idempotency keys). That durability story overlaps aisotropy's transactional-outbox / crash-durability
thesis, which is exactly why it's worth testing here.

**The two questions this arena answers, by running code:**

1. **Does durable execution give you crash-safe memory?** — **Yes, for *conversation* memory.**
   `scripts/smoke.mjs` seeds a secret in one process, **kills it**, boots a fresh process against the
   same file-backed SQLite adapter, and the new process **recalls the secret from the durable stream**.
   That is real crash-safe conversation memory. But it is *only* the conversation transcript: Flue
   explicitly does **not** store "application-owned business data unless your own tools store it"
   (docs: `guide/database`). Memory **tiers** (facts / semantic / vector) are DIY in your own data layer.
2. **Is it multi-tenant by construction or by convention?** — **By convention.** Flue's *only*
   external scoping axis is the agent-instance **`id`** (one path segment: `POST /agents/orbit/<id>`).
   The conversation stream is isolated per-id *by construction*, but **tenant / user / product are
   not framework concepts** — "It's up to the developer to decide what `id` means" (docs:
   `guide/building-agents`). Orbit folds all four aisotropy axes into that one opaque string, and the
   `route` guard is the entire authorization boundary. `scripts/isolation.mjs` proves both halves.

## The Orbit build (real Flue API)

A standard Flue Node project (`flue init --target node` scaffold), all files authored against the
**real current API** verified from shipped `.d.ts` + `flue docs`:

| File | Role |
|---|---|
| `src/agents/orbit.ts` | `defineAgent(({id}) => ...)` — Orbit support agent; `defineTool` + valibot `lookup_ticket` tool closed over the id-derived scope; a `route: AgentRouteHandler` guard that binds a bearer token to the id's identity. |
| `src/app.ts` | Hono entrypoint. Registers the **offline fake provider** via `registerProvider('orbit-fake', { api: 'openai-completions', baseUrl })` — Flue's documented custom-provider path (they show Ollama the same way). Mounts `flue()`. |
| `src/db.ts` | `export default sqlite(process.env.ORBIT_DB_PATH \|\| ':memory:')` — the durable persistence adapter that makes conversation memory survive a restart. |
| `src/shared/scope.ts` | Folds `(tenant,user,product)` into / out of the single instance id. **This encode/parse is the whole isolation surface — pure convention.** |
| `src/shared/tickets.ts` | A DIY facts/records tier (in-memory map keyed by the full tuple) — the data layer Flue does not store for you. |
| `scripts/fake-model.mjs` | An `openai-completions`-wire fake model (SSE + non-stream). Deterministic; echoes any `ORBIT-SECRET-*` it sees so leaks are observable. No API key. |

## How to run (offline, no API key)

```bash
pnpm install
pnpm run smoke        # flue build + durable-execution / crash-safe-memory proof
pnpm run isolation    # flue build + the isolation crux (by-construction vs convention)
pnpm run licenses     # pnpm-native license scan of the real installed tree
```

Both smokes boot a fake `openai-completions` model + a built Flue Node server (`dist/server.mjs`) as
real subprocesses and drive them over HTTP. `flue build --target node` compiles the app to
`dist/server.mjs` via Flue's Vite/rolldown pipeline.

### Smoke 1 — durable execution → crash-safe conversation memory (exit 0)

```
[1] process P1: seed secret into durable stream
  PASS  turn 1 accepted (200)  — status=200
  PASS  model saw the secret this turn
      reply: ORBIT_REPLY :: seen_msgs=2 :: secrets=[ORBIT-SECRET-A] :: last=""
[2] stop P1 at a turn boundary + checkpoint durable db, then exit
[3] process P2: fresh server on the SAME file-backed db
      reply: ORBIT_REPLY :: seen_msgs=4 :: secrets=[ORBIT-SECRET-A] :: last=""
  PASS  turn 2 accepted (200)  — status=200
  PASS  DURABLE MEMORY: P2 recalled the secret written by the dead P1
  PASS  context was rebuilt from the canonical stream (history > 1 turn)  — seen_msgs=4
== smoke PASSED ==
```

A *second OS process* recalled a secret the *first, now-dead* process wrote. That is durable,
crash-safe conversation memory — Flue's headline, executed. (Note: a hard `SIGKILL` mid-write can
lose the un-checkpointed last turn — expected SQLite behavior; the docs promise a graceful
turn-boundary stop, which is what the smoke exercises.)

### Smoke 2 — isolation: by-construction stream vs. convention scoping (exit 0)

```
[A] positive control + by-construction isolation (distinct id ⇒ distinct stream)
  PASS  same instance id recalls its own secret
  PASS  WRONG TENANT cannot read it (separate canonical stream)
  PASS  WRONG USER cannot read it
  PASS  WRONG PRODUCT cannot read it (when product is folded into the id correctly)
[B] the authorization boundary is 100% developer-written (route guard)
  PASS  mismatched bearer token ⇒ 401 (our guard; Flue authenticates nothing itself)
  PASS  missing bearer token ⇒ 401
[C] THE LEAK — the id is an opaque string Flue binds to NOTHING
  PASS  CROSS-PRODUCT LEAK reproduced: billing request reading analytics secret
== isolation smoke PASSED (all assertions held) ==
```

**Reading it:** distinct instance ids get distinct, isolated canonical streams — that part is
*by construction*. But every axis (tenant, user, product) is a substring the developer folds into
that one id, and authorization is a route guard the developer writes. Route a *billing* conversation
to the *analytics* instance id — a one-token slip the framework has no product concept to catch — and
product B reads product A's secret. In aisotropy, `product_id` is a field of the unforgeable `Scope`
value object carried in `context.Context`; the wrong-product read is *unrepresentable*, not one typo
away.

## Isolation model (vs. the arena)

| | Flue | aisotropy |
|---|---|---|
| External scoping axes | **1** — the opaque instance `id` string | **4** — `(tenant,user,session,product)` in a value-object `Scope` |
| Conversation isolation | **By construction** per instance id (separate canonical stream) | By construction per full Scope |
| tenant/user/product isolation | **Convention** — folded into the id by the dev | **By construction** — Scope fields on `context.Context` |
| Auth / identity binding | **Developer-written** `route` guard; Flue verifies nothing itself | Fail-closed auth seam, must-agree bind to path identity |
| Failure mode | Drop/typo an id segment, or a wrong route guard → cross-read | Unrepresentable — `Scope{}` has unexported fields |

**Verdict:** same class as the Go-native arenas (Eino / ADK / Genkit) — **isolation is
`CONVENTION`** — with the honest nuance that Flue's per-instance canonical stream *does* isolate the
conversation by construction and gives you a `route` seam to hang auth on. It just has no
tenant/user/product concept of its own.

## Where Flue genuinely leads the field

- **Durable, crash-safe conversation memory** is real and BUILT-IN (proven above). Of the 8 arenas,
  only Letta ships productized durable/sleep-time memory; Flue's durable-stream recovery is a
  distinct, stronger *runtime* durability story for the conversation itself.
- **Durable submission queue** (`dispatch` → ordered canonical stream, at-least-once, conservative
  recovery, app idempotency keys) is the closest any arena comes to aisotropy's transactional
  outbox — though it durably links *submission → conversation*, not *message → memory-tier
  consolidation-intent* in one atomic write (that stays DIY on top).

## Where it's DIY / convention (the multi-tenant-runtime gaps)

Memory **tiers** (facts FTS, semantic/vector, sleep consolidation), **per-tenant** cost meter +
**pre-spend** quota gate, circuit breaker, quota-pressure degradation, health/readiness, and — the
crux — **tenant/user/product isolation and identity binding** are all DIY or convention. Flue is a
superb *durable agent harness*; it is not a multi-tenant *runtime* with isolation and governance
guaranteed by the type system.

## License posture (real scan — `pnpm licenses list`, 323 packages)

**0 strong-copyleft, 0 restricted/source-available. Clears aisotropy's hard-blocker gate.** Four
weak-copyleft **build/CLI-time native leaves** (CAUTION, not BLOCKER, used unmodified as
dynamically-linked libs/tools): `lightningcss` + `lightningcss-darwin-arm64` (MPL-2.0, via Vite),
`@img/sharp-libvips-darwin-arm64` (LGPL-3.0-or-later, via wrangler image), `node-liblzma` (LGPL-3.0).
Core `@flue/*` are Apache-2.0. Full table in `FEATURES.md`.

## Deliverables

- `README.md` (this file), `FEATURES.md` (41-feature classification + license scan), `DX-NOTES.md`.
- `src/` (real Orbit app), `scripts/{smoke,isolation,fake-model,lib}.mjs` (run-verified, offline).

See [`../ARENA-SPEC-v2.md`](../ARENA-SPEC-v2.md) for the full aisotropy feature catalog and
[`../README.md`](../README.md) for the 7-arena isolation matrix this extends to an 8th framework.
