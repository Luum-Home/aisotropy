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
   `guide/building-agents`). Orbit folds all four aisotropy axes into that one string, and a `route`
   guard binds the caller's identity to the id. `scripts/isolation.mjs` runs both the **naive** path (a
   hand-named id slips to the wrong stream — a wrong-key dev bug) and the **strong** path (the server
   derives the id from verified identity, per Flue's own docs) at equal weight.

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

### Docker lane (containerized build + boot, same isolation proof)

An **additive** Docker smoke lives in `docker/` — the native `pnpm` smokes above are unchanged. It
proves the exact same runtime inside a stock `node:22` container:

```bash
bash docker/run.sh        # skips cleanly (exit 0) if Docker is unavailable — never fakes a pass
```

`docker/run.sh` builds the image (`docker/Dockerfile`: `pnpm install` + `flue build --target node`),
boots the container, waits for `GET /health`, then drives the **isolation** smoke
(`docker/smoke-http.mjs`) against the containerized server over its published HTTP port — write a
secret as one instance, assert cross-tenant / cross-user / **cross-product** isolation plus the
route-guard 401s — and tears the container and image down afterward. The container is self-contained
and offline: it runs *both* the deterministic fake model (`scripts/fake-model.mjs`, on `:4117`) and
the Flue server (on `:8080`), so no API key and no runtime network are needed.

Why a plain Node image suffices: Flue's file-backed durable store uses Node's **built-in**
`node:sqlite` (`DatabaseSync`) — there is no `better-sqlite3`, no native addon, no compiler in the
image. Image size ≈ **316 MB** (`node:22-bookworm-slim` + the installed dep tree). Note: pnpm 11
flags the unused optional build scripts (`sharp`/`workerd`/…) as `ERR_PNPM_IGNORED_BUILDS`; the
Dockerfile tolerates that non-fatal notice and invokes the Flue CLI binary directly (not `pnpm run`,
which would re-raise it). The host still needs `node` + `curl` to run the driver/readiness poll.

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

### Smoke 2 — isolation: by-construction stream vs. convention binding (exit 0)

```
[A] positive control + by-construction isolation (distinct id ⇒ distinct stream)
  PASS  same instance id recalls its own secret
  PASS  WRONG TENANT cannot read it (separate canonical stream)
  PASS  WRONG USER cannot read it
  PASS  WRONG PRODUCT cannot read it (when product is folded into the id correctly)
[B] the authorization boundary is developer-written (route guard)
  PASS  mismatched bearer token ⇒ 401 (our guard; Flue authenticates nothing itself)
  PASS  missing bearer token ⇒ 401
[C] NAIVE path — the client names the opaque id by hand
  PASS  a hand-named wrong id reaches the analytics stream (dev bug, not framework leak)
[D] STRONG path — the SERVER derives the id from verified identity (Flue's own doc pattern)
  PASS  server-derived billing id sees the billing stream, NOT the analytics secret
== isolation smoke PASSED (all assertions held) ==
```

**Reading it — both paths run at equal weight.** Distinct instance ids get distinct, isolated
canonical streams — that part is *by construction*, and Flue never crosses them. What is *convention*
is the **binding** between a caller's verified identity and the id they reach: tenant/user/product are
substrings the developer composes into that one id, and authorization is a `route` guard the developer
writes.

- **[C] Naive path** — the client names the opaque id directly and a dropped/typo'd segment (route a
  *billing* conversation to the *analytics* id) reaches the wrong stream. That is a **wrong-key
  developer bug**, the same class as querying *any* id→instance map — including aisotropy's own
  BadgerDB — with the wrong key. Flue faithfully returns the stream for the id it was handed; it is not
  the framework returning the wrong thing.
- **[D] Strong path** — the config a competent dev ships, and the one **Flue's own docs teach**
  (`guide/building-agents`: *"Your application must verify that the caller can access the selected agent
  `id`"*): the server **derives** the id from an already-verified identity (incl. product) via
  `deriveInstanceId`, so the caller never names a raw id. A billing request derives `p=billing` and can
  only touch the billing stream — the cross-product read is structurally unreachable.

The honest, narrower point stands: per-instance stream isolation is **by construction**, but the
identity→instance binding is **convention** Flue leaves to you (it authenticates nothing by default).
aisotropy closes the same gap upstream: `product_id` is a field of the unforgeable `Scope` value object
carried in `context.Context`, so the naive [C] path is *unrepresentable*, not one convention away.

## Isolation model (vs. the arena)

| | Flue | aisotropy |
|---|---|---|
| External scoping axes | **1** — the opaque instance `id` string | **4** — `(tenant,user,session,product)` in a value-object `Scope` |
| Conversation isolation | **By construction** per instance id (separate canonical stream) | By construction per full Scope |
| tenant/user/product isolation | **Convention** — folded into the id by the dev | **By construction** — Scope fields on `context.Context` |
| Auth / identity binding | **Developer-written** `route` guard (Flue's documented seam); Flue verifies nothing itself | Fail-closed auth seam, must-agree bind to path identity |
| Failure mode (naive path) | Hand-name the id and drop/typo a segment → cross-read (wrong-key dev bug) | Unrepresentable — `Scope{}` has unexported fields |
| Strong path | Server `deriveInstanceId(principal, product)` + guard checks caller may access it → slip unreachable | Same discipline, enforced by the type system rather than convention |

**Verdict — `CONVENTION`, honestly narrowed.** Same class as the Go-native arenas (Eino / ADK /
Genkit): the per-instance canonical stream *does* isolate the conversation **by construction**, and
Flue ships a documented `route` seam plus the `deriveInstanceId` discipline to close the binding gap.
What stays convention is that gap itself — the (tenant,user,product)→id mapping and the auth check are
developer code, because Flue has no tenant/user/product concept of its own and authenticates nothing by
default. The cross-product read is a wrong-key developer bug (true of any id→instance map), not a Flue
defect; aisotropy's edge is that it makes the same bug *unrepresentable* via an unforgeable `Scope`.

## What Flue does genuinely well (steelman — RUN-VERIFIED)

A fair comparison has two sides. Flue is the Astro team's **durable-execution agent framework**, and
on its home axis — *crash-safe, durable conversation runtime for long-running agents* — it is the
strongest entrant in this arena set. aisotropy is a multi-tenant *runtime* with tiered memory and
isolation-by-construction; on durable single-/few-tenant conversation execution, **Flue is the
reference, and this is a real strength, not a consolation prize.** Each item below is executed by the
committed smokes (exit 0), not asserted from docs:

| Strength | Real 1.0.0-beta.9 surface | Run-verified evidence |
|---|---|---|
| **Durable, crash-safe conversation memory** | file-backed `sqlite(...)` adapter over the append-only canonical stream (`src/db.ts`, `@flue/runtime/node`) | `[RAN]` `pnpm run smoke`: process P1 seeds a secret then **dies**; a *separate* process P2 on the same db recalls it, context rebuilt from the stream (`seen_msgs` 2→4). Of the arenas, only Letta ships productized durable/sleep-time memory; Flue's stream-replay recovery is a distinct, stronger *runtime* durability story for the conversation itself. |
| **Durable submission queue (closest thing to a transactional outbox)** | `dispatch(...)` → one ordered durable queue → canonical stream; at-least-once, conservative crash recovery, app-owned idempotency keys | `[RAN][DOC]` The smokes drive the same ordered-durable-stream path. This is the closest any arena comes to aisotropy's transactional outbox — though it durably couples *submission → conversation*, not *message → memory-tier consolidation-intent* in one atomic write (that stays DIY on top, because there are no memory tiers to consolidate into). |
| **Valibot-typed agents & tools** | `defineTool({ input: v.object({...}), output })`, `session.prompt(text, { result })` validated structured output | `[RAN][SRC]` Orbit's `lookup_ticket` tool is valibot-typed input/output; the fake-model path exercises tool calls end to end. Type-safe I/O without a codegen step. |
| **Hono app + clean `route` seam** | `src/app.ts` is an ordinary Hono app; agents expose a `route: AgentRouteHandler` (= Hono `MiddlewareHandler`) | `[RAN][SRC]` `/health` is app-owned; the per-instance guard is a middleware Flue calls before every prompt — exactly where the docs say to authenticate. A tasteful, standard-web ingress model. |
| **Durable-agent runtime model + offline provider registry** | one addressable agent instance = one canonical stream; `registerProvider(id, { api: 'openai-completions', baseUrl })` | `[RAN][DOC]` The whole arena runs **with no API key** through the documented custom-provider path (Ollama uses the same one). The durable-instance mental model is coherent and well-documented (97 docs ship in the CLI). |

**Where aisotropy does NOT compete:** aisotropy has no durable-execution / stream-replay recovery for
the *live conversation* — its crash-durability thesis is the transactional **outbox → async
consolidation** into memory tiers, a different guarantee. For a long-running or webhook-driven agent
whose transcript must survive a process death, Flue's model is genuinely better, and this arena says so.

This does not soften the isolation finding — the two sit side by side. **Flue is great at durable
conversation execution; it simply has no tenant/user/product runtime and no memory tiers**, which is
the space aisotropy is built around.

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
- `docker/{Dockerfile,run.sh,smoke-http.mjs}` — additive containerized build+boot lane driving the
  isolation smoke against the container over HTTP (run-verified; skips cleanly without Docker).

See [`../ARENA-SPEC-v2.md`](../ARENA-SPEC-v2.md) for the full aisotropy feature catalog and
[`../README.md`](../README.md) for the 7-arena isolation matrix this extends to an 8th framework.
