---
type: Arena
title: "eve (Vercel) arena — filesystem-first durable agents"
description: Real offline integration of Vercel eve 0.24.5 building the Orbit multi-tenant support chatbot, with a run-verified multi-product isolation smoke and license scan.
tags: [arenas, eve, vercel, typescript, durable-agents, isolation]
timestamp: 2026-07-16T00:00:00Z
---

# eve Arena

> **Framework:** [`eve@0.24.5`](https://github.com/vercel/eve) — Apache-2.0. Filesystem-first
> framework for **durable** backend AI agents. Author files under `agent/`; eve compiles them into
> a Nitro server whose turns run as durable workflows.
> **Integration:** **REAL (offline).** Compiled + booted here with **no API key, no network at
> runtime**; only the AI-SDK model is a deterministic mock. Isolation, tools, streaming, health and
> the license scan were **executed**. Output pasted below.
> **Angle:** convention-over-config — tools/skills/channels/schedules are *files*, discovered by the
> compiler. Memory/multi-tenancy are **not** framework concepts (session-scoped state + DIY keys).
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
probe the (tenant, user, product) isolation axis.

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

npm install                     # eve, ai, zod (33 deps)
node node_modules/eve/bin/eve.js build     # Nitro/rolldown compile
```

## Run it (offline, no key)

```bash
# 1) Build + boot the real server
node node_modules/eve/bin/eve.js build
PORT=4117 HOST=127.0.0.1 node .output/server/index.mjs &   # or: eve start

# 2) Adversarial isolation + runtime smoke (uses eve/client)
node scripts/smoke.mjs
```

### Pasted output — `eve build` (real Nitro compile)

```
[nitro] ✔ Generated public .eve/builds/.../output/public
[nitro] ℹ Building server (builder: rolldown, preset: node-server, ...)
[nitro] ✔ Server built in 357ms
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

### Pasted output — `scripts/smoke.mjs`

```
PASS  runtime.health  — status=ready workflow=workflow//eve//workflowEntry
PASS  runtime.info    — model=mock-provider/orbit-mock/deterministic
PASS  runtime.turn    — "Orbit here. You said: hello Orbit"

## Isolation — 4th scope axis (tenant, user, PRODUCT)
PASS  strict.positive_control  — key=acme:alice:checkout secret=checkoutSecret_cb0cdd64e0a1ef2d
PASS  strict.product_isolated  — billing key=acme:alice:billing secret=billingSecret_acd668bf6488f733
PASS  leaky.cross_product_read — recall(product=billing) returned product=checkout's secret; key=acme:alice
      ^ LEAK: product_id is unbound. Dropping `:${product}` from the developer key crosses products.

checks=6  proven_cross_product_leaks=1
```

## Isolation model — CONVENTION at the memory axis, by-construction at the session axis

eve has **two** isolation boundaries, and they are not the same as multi-tenant memory scoping:

- **Session ↔ session: by construction.** `sessionId` is runtime-owned; a session's durable state
  (`defineState`) is session-scoped; only one continuation token owns a session; subagents get their
  own session and "nothing crosses implicitly." This is genuinely hard to leak across.
- **Channel auth: fail-closed, by construction.** Routes reject unauthenticated traffic (401
  default), verify signatures in constant time, and warn against trusting body-supplied identity —
  a stronger front door than most arenas.
- **Tenant / user / product memory: CONVENTION.** There is **no** `tenant_id`/`user_id`/`product_id`
  anywhere in the runtime. Cross-session, per-customer, per-product memory is a **developer-built
  store keyed by a hand-concatenated string** (`agent/tools/orbit_memory.ts`). The strict key
  isolates products; the one-edit **leaky** key leaks them (proven above). The framework validates
  neither.

So: eve's leaky path is *one dropped `:${product}`* away from the correct one, and the type system
does nothing — exactly the CONVENTION failure mode the other arenas share. aisotropy makes
`product_id` ride `context.Context` in an unforgeable `Scope`, so the leaky read does not compile.

## Files

| File | What |
|---|---|
| `agent/agent.ts` | `defineAgent` — mock model + context-window escape hatch |
| `agent/instructions.md` | Orbit system prompt |
| `agent/tools/orbit_memory.ts` | per-(tenant,user,product) memory tool; strict vs leaky key builder |
| `agent/channels/eve.ts` | HTTP channel + `[localDev(), none()]` auth (open for the offline demo) |
| `agent/lib/model.ts` | offline deterministic `MockLanguageModelV4` (the only faked layer) |
| `scripts/smoke.mjs` | isolation + runtime smoke via `eve/client` |
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
