---
type: DX notes
title: "Flue — developer-experience notes (aisotropy arena)"
description: Hands-on DX from building and running the Orbit chatbot on Flue 1.0.0-beta.9 offline — setup, the durable-execution mental model, the isolation gotcha, and the offline-testing story.
tags: [arenas, flue, dx, durable-execution]
timestamp: 2026-07-16T00:00:00Z
---

# Flue — DX notes

Built and **ran** (offline, no API key) the Orbit multi-tenant support chatbot on
`@flue/runtime@1.0.0-beta.9`. Verdict: an unusually clean *durable agent harness* with excellent
docs and a great offline-provider story — but a runtime whose only isolation axis is a single opaque
string, which a multi-tenant builder must not forget.

## Setup — smooth

- `pnpm add @flue/runtime @flue/cli @flue/sdk` then `flue init --target node` scaffolds a one-line
  `flue.config.ts`. Project layout is convention-over-config: `src/agents/*.ts` (one file = one
  addressable agent), `src/app.ts` (optional Hono entrypoint), `src/db.ts` (optional persistence).
- `flue build --target node` compiled cleanly to a single `dist/server.mjs` (Vite/rolldown) on the
  first try — agents auto-discovered, `.env` auto-loaded. `flue dev`/`flue run` also exist.
- **`flue docs` is a standout.** 97 documentation pages ship *inside the installed CLI*
  (`flue docs read guide/tools`, `flue docs search <q>`). Offline, authoritative, and the single best
  reason the arena integration is `[DOC]`-grounded rather than guesswork. More frameworks should do
  this.

## The API is small and tasteful

```ts
export default defineAgent(({ id }) => ({
  model: 'anthropic/claude-haiku-4-5',
  instructions: '...',
  tools: [defineTool({ name, description, input: v.object({...}), async run({ input }) {...} })],
}));
```

- `defineAgent(({id, env}) => config)` — the initializer receives the instance `id`, which is the
  hook for per-instance resource scoping.
- `defineTool` uses **valibot** schemas for typed input/output; `connectMcpServer` yields ordinary
  tools. `session.prompt(text, { result: schema })` gives validated structured output. `session.task`
  delegates to subagents. `session.compact()` + `CompactionConfig` handle context-window summarization.
- Providers: built-in Pi catalog (`anthropic/…`, `openai/…`, `openrouter/…`) keyed by env vars, plus
  `registerProvider(id, { api: 'openai-completions', baseUrl })` for anything OpenAI-compatible.

## Durable execution is the real selling point — and it works

The mental model that clicks: **each agent instance owns one append-only canonical conversation
stream.** Prompts and `dispatch()` inputs are ordered operations on that stream; recovery replays it.
On Node with a file-backed `sqlite('./data/flue.db')` adapter, this survives a full process restart.

Our smoke proved it end-to-end: seed a secret in process P1, stop P1, boot P2 on the same db file →
**P2 recalls the secret**. For anyone building long-running or webhook-driven agents, this is a
genuinely better durability story than the "history is a conduit you re-pass" model most arenas use.

**Two honest caveats I hit:**
- A hard `SIGKILL` mid-write can lose the un-checkpointed last turn (expected SQLite/WAL behavior).
  The docs are precise about this: graceful shutdown stops "at a turn boundary." The smoke uses
  `SIGTERM` + wait-for-exit and recalls reliably.
- Durability covers the *conversation + accepted submissions + attachments + workflow runs* — **not**
  your business data. "Application-owned business data unless your own tools store it" is DIY. So
  durable execution buys crash-safe *conversation* memory, not crash-safe *memory tiers*.

## The isolation gotcha (the crux for a multi-tenant build)

Flue gives you **one** external scoping axis: the agent-instance `id` in `POST /agents/orbit/<id>`.
The docs are refreshingly blunt: *"It's up to the developer to decide what `id` means"* and *"Your
application must verify that the caller can access the selected agent `id`."*

Concretely, to make Orbit multi-tenant the strong config is:
1. **Derive** the id from verified identity — `deriveInstanceId(principal, product)` — so the caller
   never names a raw id (the client-names-the-id path is the naive one).
2. Write the auth check in the agent's `route` handler: authenticate the caller, then verify they may
   access the selected id. This is Flue's own documented pattern (`guide/building-agents`).
3. Keep every tool closed over the *parsed* scope so the model can't choose the customer.

The isolation smoke runs two paths at equal weight. **Naive:** the client hand-names the id and a
dropped/typo'd segment (a *billing* conversation sent to the *analytics* id) reaches the wrong stream —
but that's a wrong-key developer bug, the same as querying any id→instance map with the wrong key, not
Flue returning the wrong thing. **Strong:** the server derives `p=billing`, so the request can only
touch the billing stream and the slip is unreachable. The honest read: the per-instance stream *is*
isolated by construction and the `route` seam is a clean, documented place to bind auth; what's on you
is composing the id from verified identity and writing that guard — there's no tenant/product concept
or value-object scope to catch a slip for you. Same `CONVENTION` class as the Go-native arenas.

## Offline testing story — better than most

`registerProvider(id, { api: 'openai-completions', baseUrl })` is the documented path for any
OpenAI-compatible endpoint (they show Ollama), so a ~40-line local fake model (`scripts/fake-model.mjs`)
made the whole arena run **with no API key**. Pi also ships an in-process `registerFauxProvider` /
`fauxProvider` (`@earendil-works/pi-ai/compat`) with `setResponses([...])` for pure unit tests, and
Flue integrates `vitest-evals` for repeatable LLM-as-judge eval suites. Good testing ergonomics.

## Rough edges

- **Beta churn:** `1.0.0-beta.9`; the runtime bundles a "temporary compatibility entrypoint" in Pi and
  notes an in-flight ModelManager migration. Expect API movement.
- **Cloudflare gravity:** the richest durability semantics (Durable Object per instance, Fiber wake
  recovery) are Cloudflare-specific. The Node target is fully usable but "one live owner per instance,
  route each instance to one process" is a real operational constraint for multi-replica deploys.
- **No OpenAPI / unified contract:** unlike aisotropy's struct-as-contract, HTTP validation, tool
  schema, and LLM output schema are three separate declarations.
- **Ports/lifecycle when scripting:** running real subprocess servers, I had to add port-freeing +
  fake-model readiness polling to `scripts/lib.mjs` (a stale server squatting a port returned 404s).
  Not Flue's fault, but worth noting for anyone orchestrating `flue run`/`dev` in tests.

## Bottom line

Flue is the best *durable agent harness* I've handled in this arena set: crash-safe conversation
memory is real and proven, the API is small and typed, and shipping the docs in the CLI is a gift.
But it's a harness, not a multi-tenant runtime — memory tiers, per-tenant governance, and
isolation-by-construction are all on you. Pick Flue when *durable single- or few-tenant agents* are
the goal; reach for aisotropy when a cross-tenant leak is an incident and memory must be tiered and
auditable by construction.
