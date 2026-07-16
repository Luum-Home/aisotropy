---
type: Developer notes
title: "eve (Vercel) — DX notes"
description: Honest developer-experience assessment of Vercel eve from the perspective of building a multi-tenant production support agent.
tags: [arenas, eve, vercel, dx, developer-experience]
timestamp: 2026-07-16T00:00:00Z
---

# eve — DX Notes

Assessed by actually building and running the Orbit agent offline (`eve build` + booted server +
`eve/client` smoke). Perspective: shipping a **multi-tenant** support product where a cross-tenant
or cross-product leak is an incident.

## The good

### Filesystem-first is a genuinely nice authoring model
An agent is a directory you can read top-to-bottom: `agent.ts` (config), `instructions.md` (prompt),
`tools/*.ts` (one tool per file, name = filename), `skills/*.md`, `channels/`, `schedules/`. No
central registration, no wiring graph — the compiler discovers files by convention. Adding a tool is
"drop a file that `export default defineTool(...)`". For a support bot with a handful of actions this
is the least-ceremony framework in the seven. `GET /eve/v1/info` reflecting the whole resolved
surface (model, tools, channels, schedules) is a great operational touch.

### Durable execution is the real headline, and it's free
Every turn is a durable workflow: crash the process mid-turn and it resumes from the last completed
step, not the top. Locally this runs on the open Workflow-SDK **local world** persisting to
`.eve/.workflow-data` — no external DB to stand up for `eve start`. This is a capability none of the
other six arenas ship, and it's the thing aisotropy approximates only with its transactional-outbox
+ cold-boot recovery. If your agent does long, multi-step, human-in-the-loop work, this is worth a
lot.

### Streaming + cancel + subagents are first-class
NDJSON event stream with replay (`startIndex`), a real `POST /cancel` that stops the turn (spend
cutoff, propagated recursively to subagents), parked work for approvals/OAuth. I streamed a real
turn's 9 events and it behaved exactly as documented.

### Fail-closed channel auth is better than the field
Routes are 401 by default; `placeholderAuth()` keeps a half-built app closed in production;
signatures verified in constant time; explicit "don't trust body-supplied identity." This is a
stronger security *front door* than the Python/TS competitors, which mostly hand you a raw store and
trust the caller string.

### Clean license posture
`eve` and the AI SDK are Apache-2.0; the whole installed tree is 25 MIT / 7 Apache / 1 dual-BSD, zero
copyleft, zero MPL. Cleaner than Pydantic AI / LangGraph / Letta (which ship MPL `certifi`). Passes
aisotropy's hard gate outright.

### Provider seam is a true seam
`model` accepts an AI-Gateway id string **or** any AI-SDK `LanguageModel` — I proved it by handing it
an offline `MockLanguageModelV4` and running the entire runtime with no key and no network.

## The painful

### The runtime is Vercel-platform gravity
"Durable backend agents that run anywhere" is true *with asterisks*. The frictionless path — durable
production world, sandboxed compute, model routing, observability dashboard, OAuth connections — is
Vercel Workflow / Vercel Sandbox / AI Gateway / Vercel Connect / Vercel Observability. Self-hosting
is documented (`@workflow/world-postgres` + docker/microsandbox) but is the off-road path. For a
sovereignty-first multi-tenant runtime this is the opposite of aisotropy's single-binary posture.

### No memory tiers, and memory has no tenant/user/product axis
This is the big one for multi-tenant products. eve gives you a **session-scoped** durable KV
(`defineState`) and append-only history — and that's the memory story. No vector RAG, no facts/FTS
journal, no embedder, no consolidation. Worse for isolation: **there is no tenant/user/product
concept anywhere in the runtime**. `ctx.session` exposes `{id, turn, auth, parent}` — no tenant. So
per-customer, per-product memory is a store *you* build, keyed by a string *you* concatenate, and the
leaky key is one dropped segment from the correct one (I ran the leak). aisotropy's entire value
proposition — memory scoped by (tenant,user,product) by construction — is precisely the thing eve
leaves to developer discipline.

### No cost governance
Usage arrives *after* the step. There's no per-tenant token bucket, no pre-spend 429, no
quota-pressure degrade. In a multi-tenant product a runaway tenant is a P1 you have to build guards
for yourself (a pre-model hook), and a zero-cost test can't exercise them.

### Node 24 hard gate + heavy first build
The CLI flatly refuses Node < 24. The `eve` package is a 4.7 MB compiled bundle; the build pulls
rolldown/nitro and produces a ~5.7 MB server. Fine on Vercel; a friction point in a constrained CI.

### `eve start`'s own health probe was flaky here
`eve start` shut down reporting "did not become healthy within 60s" even though the built server's
`/eve/v1/health` returned `ready` in ~1s when I ran `node .output/server/index.mjs` directly. A
startup-timing quirk in this offline box, not a runtime fault — but worth knowing if you script it.

## The verdict

eve is the **best durable-execution and authoring DX** of the seven arenas, and a legitimately strong
*session-and-channel* security model. If you're building a **single-tenant or few-tenant durable
backend agent** — long tasks, HITL, tool-heavy, on Vercel — it is a delight and hard to beat.

**Best for:** durable, tool-heavy, human-in-the-loop backend agents on Vercel, where "the agent runs
for days and survives redeploys" is the requirement.

**Worst for:** a sovereign, self-hosted, **multi-tenant** product where memory must be tiered and
scoped by (tenant, user, product) and spend gated per tenant. eve has none of those as primitives;
you'd rebuild the entire aisotropy differentiator by hand, and the isolation you'd build would be
CONVENTION (a developer key) rather than construction.

**vs. aisotropy:** eve wins on durable execution, authoring ergonomics, and channel/session security;
aisotropy wins on tiered memory, tenant/user/product isolation-by-construction, pre-spend governance,
and single-binary sovereignty. The trade is a *convention-first durable agent framework with Vercel
gravity* vs a *constitutionally-gated multi-tenant runtime you host yourself*.
