---
type: Arena
title: "eve (Vercel) arena — filesystem-first durable agents"
description: Real offline integration of Vercel eve 0.24.6 building the Orbit durable support chatbot, with a run-verified DIY-memory partitioning demo, a steelman of eve's genuine strengths, and a license scan.
tags: [arenas, eve, vercel, typescript, durable-agents, isolation]
timestamp: 2026-07-16T00:00:00Z
---

# eve Arena

> **Framework:** [`eve@0.24.6`](https://github.com/vercel/eve) — Apache-2.0. Filesystem-first
> framework for **durable** backend AI agents. Author files under `agent/`; eve compiles them into
> a Nitro server whose turns run as durable workflows.
> **Integration:** **REAL (offline).** Compiled + booted here with **no API key, no network at
> runtime**; only the AI-SDK model is a deterministic mock. Runtime, tools, streaming, health and
> the license scan were **executed**. Output pasted below.
> **Angle:** convention-over-config — tools/skills/channels/schedules are *files*, discovered by the
> compiler. eve is **session-scoped by design**: multi-tenant memory is **out of its scope**, the
> same posture as Pydantic AI / Eino — not a defect. This arena shows the honest DIY path.
> **Port:** 4117.

## What eve actually is

You describe an agent as a directory:

```
agent/
├── agent.ts            # model + runtime config (defineAgent)
├── instructions.md     # always-on system prompt
├── tools/*.ts          # one typed tool per file (defineTool + zod)
├── skills/*.md         # procedures loaded on demand (load_skill tool)
├── channels/eve.ts     # the HTTP front door + fail-closed auth chain
└── schedules/*.md      # cron jobs
```

Each **turn** runs as a durable workflow (Vercel Workflow on Vercel; the open Workflow-SDK
**local world**, persisting to `.eve/.workflow-data`, otherwise). Sessions survive process
restarts and redeploys; work parks durably for HITL approval, OAuth, or subagents. The HTTP
contract is stable: `POST /eve/v1/session`, `GET /eve/v1/session/:id/stream` (NDJSON),
`POST /eve/v1/session/:id/cancel`, `GET /eve/v1/health`, `GET /eve/v1/info`.

This arena's agent is **Orbit**, a support chatbot with one memory tool (`orbit_memory`) used to
show the honest **DIY** path for the (tenant, user, product) axis — the axis eve does not have.

## Requirements & setup

eve **hard-requires Node ≥ 24** (the CLI refuses to run on older Node). This box ships Node 22, so
Node 24 was fetched to run the arena:

```bash
# This env: Node 22 is default; eve needs 24.
node node_modules/eve/bin/eve.js --version
# -> eve requires Node.js >=24. You are running v22.23.1. ...

# Fetch a Node 24 runtime (done once for this arena):
curl -sSL -o node24.tar.gz https://nodejs.org/dist/v24.10.0/node-v24.10.0-darwin-arm64.tar.gz
tar xzf node24.tar.gz
export PATH="$PWD/node-v24.10.0-darwin-arm64/bin:$PATH"   # now `node -v` => v24.10.0

npm install                     # eve@0.24.6, ai, zod (33 deps)
node node_modules/eve/bin/eve.js build     # Nitro/rolldown compile
```

## Run it (offline, no key)

```bash
# 1) Build + boot the real server
node node_modules/eve/bin/eve.js build
PORT=4117 HOST=127.0.0.1 node .output/server/index.mjs &   # or: eve start

# 2) Runtime + DIY-memory partitioning smoke (uses eve/client)
node scripts/smoke.mjs
```

> **Verified here (2026-07-17):** `npm install` (bumped the installed tree to **eve@0.24.6**) →
> `eve build` (rolldown/Nitro, "Server built in ~410ms", 5.74 MB) → `node scripts/smoke.mjs`
> (**6/6 PASS, exit 0**), all on the fetched Node 24.10.0. Pastes below are from that run.

### Docker smoke (additive — the native path above is unchanged)

The same 6/6 smoke also runs against a **containerized** eve server, so it needs no host Node 24:

```bash
bash docker/run.sh
```

It (1) `docker build`s `docker/Dockerfile` — a Node 24 builder that does the real
`npm install` + `eve build`, and a Node 24 runtime stage that boots
`.output/server/index.mjs` with the offline mock model on **:4117**; (2) runs the container
and waits for `GET /eve/v1/health` on the host-published port; (3) drives the arena's own
`scripts/smoke.mjs` (the typed `eve/client`) **inside** the container against that server —
asserting the isolation/product story (correct composite key `${t}:${u}:${p}` partitions
products; the naive key that drops the product segment collides, and that collision is caught);
(4) tears down the container and image. It **skips cleanly (exit 0) when Docker is absent** —
never fakes a pass.

> **Verified here (2026-07-17):** `bash docker/run.sh` → build (real `npm install` + `eve build`,
> 5.74 MB server) → container boot on :4117 → `docker exec … node scripts/smoke.mjs`
> (**6/6 PASS**) → clean teardown. Image on-disk **~458 MB** (`node:24-bookworm-slim` base +
> the `eve`/`ai`/`zod` tree). The smoke is driven on Node 24 inside the container because eve's
> CLI hard-requires Node ≥ 24; the compiled server itself is self-contained. Files:
> `docker/Dockerfile`, `docker/run.sh`.

### Pasted output — `eve build` (real Nitro compile)

```
[nitro] ✔ Generated public .eve/builds/.../output/public
[nitro] ℹ Building server (builder: rolldown, preset: node-server, ...)
[nitro] ✔ Server built in 410ms
  ├─ .../server/_libs/eve.mjs (4.74 MB) (1.13 MB gzip)
  ├─ .../server/_libs/ai.mjs (272 kB) (49.1 kB gzip)
  └─ .../server/index.mjs (229 kB) (73.3 kB gzip)
Σ Total size: 5.74 MB (1.36 MB gzip)
[BUILD] built output at .../arenas/eve/.output
```

### Pasted output — server boot + `/eve/v1/info` (real agent snapshot)

```
[world-local] Re-enqueued 1 active run(s) on startup
➜ Listening on: http://127.0.0.1:4117/

$ curl -s http://127.0.0.1:4117/eve/v1/health
{"ok":true,"status":"ready","workflowId":"workflow//eve//workflowEntry"}

$ curl -s http://127.0.0.1:4117/eve/v1/info   # (abridged)
{"agent":{"name":"eve-arena","model":{"id":"mock-provider/orbit-mock/deterministic",
  "contextWindowTokens":128000,"routing":{"kind":"external","provider":"mock-provider"}}},
 "channels":{"authored":[{"method":"POST","urlPath":"/eve/v1/session"},
  {"method":"GET","urlPath":"/eve/v1/session/:sessionId/stream"}, ...]}}
```

### Pasted output — one real turn's event stream (mock model → durable session)

```
session.started → turn.started → message.received{"hello Orbit"} → step.started
→ message.appended{"delta":"Orbit here. You said: hello Orbit"}
→ message.completed{"finishReason":"other"} → step.completed → turn.completed → session.waiting
(9 events)
```

### Pasted output — `scripts/smoke.mjs` (real run, 2026-07-17, eve@0.24.6)

```
PASS  runtime.health  — status=ready workflow=workflow//eve//workflowEntry
PASS  runtime.info  — model=mock-provider/orbit-mock/deterministic
PASS  runtime.turn  — "Orbit here. You said: hello Orbit"

## DIY memory partitioning — the (tenant,user,product) axis eve does not have
PASS  correct_key.positive_control  — key=acme:alice:checkout secret=checkoutSecret_67fe2ee4638c9e9c
PASS  correct_key.product_partitioned  — billing key=acme:alice:billing secret=billingSecret_90e3cd9f31bd322c
PASS  naive_key.product_collision  — recall(product=billing) hit product=checkout's slot? key=acme:alice
      ^ Expected: the naive key `${t}:${u}` drops product, so two products share
        one slot. Same-key/wrong-key dev bug, true of ANY KV incl. BadgerDB — the
        correct composite key above partitions fine. eve has no tenancy axis by design.

checks=6  naive_key_collisions=1  (expected: 1 — the documented DIY footgun)
```

The three memory checks are all **expected outcomes**: the correct composite key partitions
products (positive control + cross-product both PASS), and the naive key collides them. The
collision does **not** exit non-zero — it is the documented DIY footgun, not a framework failure.

## Isolation model — session-scoped by construction; multi-tenant memory is DIY / out-of-scope

eve is a **session-scoped durable runtime**. It has real isolation boundaries — they are simply
*not* a multi-tenant memory scope, because eve has no such axis by design:

- **Session ↔ session: by construction.** `sessionId` is runtime-owned; a session's durable state
  (`defineState`) is session-scoped; only one continuation token owns a session; subagents get their
  own session and "nothing crosses implicitly." Genuinely hard to cross — a real strength.
- **Channel auth: fail-closed, by construction.** Routes reject unauthenticated traffic (401
  default), verify signatures in constant time, and warn against trusting body-supplied identity —
  a stronger front door than most arenas.
- **Tenant / user / product memory: DIY / OUT-OF-SCOPE (by design, not a defect).** There is **no**
  `tenant_id`/`user_id`/`product_id` anywhere in the runtime — the same posture as **Pydantic AI**
  (stateless lib) and **Eino** (orchestration lib): no tenancy tier by design. Cross-session,
  per-customer, per-product memory is a **developer-built store keyed by a string the developer
  computes** (`agent/tools/orbit_memory.ts`). There is nothing for eve to validate — it has no
  tenancy primitive to validate against.

The naive-key **collision** shown above is the classic **same-key / wrong-key developer bug** that
is **true of *any* key-value store — including aisotropy's own BadgerDB** if a dev drops a scope
segment. It is a tautology of DIY keys, not an eve failing; the correct composite key
(`${t}:${u}:${p}`) partitions products fine, and a competent dev writes that (or, better, derives
the key from the channel-verified identity rather than from tool args). Where aisotropy differs on
*its* narrow axis: `product_id` rides `context.Context` inside an unforgeable value-object `Scope`,
so the naive read is **unrepresentable** rather than merely discouraged — construction vs. developer
discipline. That is one axis of difference, not a verdict that eve is insecure.

## What eve does genuinely well (steelman)

A fair comparison has two sides. aisotropy is a multi-tenant *runtime* with a narrow thesis
(isolation-by-default); eve is a **durable backend-agent framework with best-in-class authoring
DX**, and on *its* axes it is excellent — axes where aisotropy does not compete. The first four
below were **run-verified in this arena** (exit 0); the Vercel-platform surfaces are **source-verified
from shipped docs, NOT run** (labelled).

| Strength | Status | Evidence |
|---|---|---|
| **Filesystem-first authoring** | ▶ run-verified | An agent *is* a directory: `agent.ts`, `instructions.md`, `tools/*.ts` (one typed `defineTool`+zod per file, name = filename), `channels/`, `skills/`, `schedules/`. No central registry, no wiring graph — the compiler discovers files by convention. Least-ceremony of the arenas; `GET /eve/v1/info` reflects the whole resolved surface (model, channels) live. |
| **Nitro / rolldown build** | ▶ run-verified | `eve build` → rolldown builder, node-server preset, **"Server built in ~410ms"**, 5.74 MB (1.36 MB gzip) self-contained server at `.output/server/index.mjs`. Fast, reproducible, offline. |
| **Real NDJSON turn/event stream + durable session** | ▶ run-verified | Each turn is a durable Workflow-SDK run (local world persists to `.eve/.workflow-data`, survives restarts). One turn streamed the real event sequence `session.started → turn.started → message.received → step.started → message.appended → message.completed → step.completed → turn.completed → session.waiting` (9 events); `POST /session/:id/cancel` stops the turn and propagates to subagents. |
| **Typed `eve/client`** | ▶ run-verified | The smoke drives the server entirely through the shipped typed client (`new Client({host})`, `client.health()`, `client.info()`, `client.session().send()` returning an async-iterable event stream) — no hand-rolled HTTP. |
| **Vercel ecosystem gravity** | source-verified, **NOT run** | From `node_modules/eve/docs/**`: **AI Gateway** (provider-agnostic `model:"anthropic/claude-…"` string routing + fallbacks; here swapped for an offline mock via the same `string \| LanguageModel` seam), **Vercel Sandbox** (microVM isolation for untrusted model-run code + credential brokering), the **production workflow world** (managed durability), **Connect** OAuth brokering, and the **Observability** dashboard. On Vercel these are batteries-included; self-hosting them (`@workflow/world-postgres` + docker/microsandbox) is the off-road path. |

Where aisotropy differs: tiered memory (BadgerDB/rqlite-FTS/Qdrant), tenant/user/product scope bound
by `context.Context`, and pre-spend per-tenant governance — none of which eve ships. Where **eve** is
strong: durable execution as a free primitive, the filesystem-first DX, the fast Nitro/rolldown build
chain, and the Vercel-native platform. The two occupy different lanes; this arena scores eve on
aisotropy's lane while stating plainly the lanes eve owns.

## Files

| File | What |
|---|---|
| `agent/agent.ts` | `defineAgent` — mock model + context-window escape hatch |
| `agent/instructions.md` | Orbit system prompt |
| `agent/tools/orbit_memory.ts` | DIY per-(tenant,user,product) memory tool; correct-composite vs naive key builder |
| `agent/channels/eve.ts` | HTTP channel + `[localDev(), none()]` auth (open for the offline demo) |
| `agent/lib/model.ts` | offline deterministic `MockLanguageModelV4` (the only faked layer) |
| `scripts/smoke.mjs` | runtime + DIY-memory partitioning smoke via `eve/client` |
| `FEATURES.md` | 41-feature classification (ran + source-verified) |
| `DX-NOTES.md` | developer-experience assessment |

## Honesty caveats

- **REAL, offline** integration: eve's compiler, Nitro server, durable session (local Workflow-SDK
  world on disk), tool loop, channel auth, NDJSON streaming, and `/health` all ran. The **only**
  fake is the AI-SDK model.
- **Not exercised** (classified from shipped docs, `node_modules/eve/docs/**`): Vercel **Sandbox**
  (microVM code execution), **AI Gateway** (mock model used instead), the Vercel **production
  workflow world**, **Connect** OAuth, and the **Observability** dashboard.
- **Node 24** had to be fetched (the CLI hard-refuses on this box's Node 22).
- Nothing here targets a real org/person; the mock secrets are random tokens.
