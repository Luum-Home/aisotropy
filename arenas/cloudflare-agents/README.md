# Cloudflare Agents Arena

> **Framework:** [`agents`](https://www.npmjs.com/package/agents) `0.17.4` (MIT) — build AI
> Agents on Cloudflare Workers + Durable Objects.
> **Integration:** **REAL** — installed the real SDK, built an Orbit chatbot with its current
> API, and ran the isolation smoke inside genuine Durable Objects in `workerd` (via
> `@cloudflare/vitest-pool-workers` and `wrangler dev --local`). No API key, offline.
> **Headline finding:** each Agent is **one Durable Object with its OWN embedded SQLite**, so
> the per-object storage boundary is a **hard, by-construction platform guarantee** (no
> cross-object read exists) — *stronger* than the shared string-keyed stores in the other
> seven arenas. **But** the `(tenant,user,product)→object` binding is a developer-assembled
> **name string** (mis-assemble it → leak, proven), and the whole guarantee is a **rented
> Cloudflare-platform** boundary, not self-hostable OSS.

## Setup

```bash
# Requires Node 20+ and pnpm.
pnpm install            # agents@0.17.4 + wrangler + @cloudflare/vitest-pool-workers + vitest

# For a live LLM the Orbit chat uses a deterministic stub by default (no key needed).
# A real model would slot into OrbitAgent.chat() via a Workers AI / OpenAI / ai-sdk call.
```

## Smoke test (offline, real workerd, no key)

```bash
pnpm smoke              # = vitest run  -> 7 passed
```

Runs the adversarial isolation matrix (`test/isolation.test.ts`) plus a DO-SQLite FTS5
capability probe (`test/probe.test.ts`) **inside real Durable Objects** simulated by
`workerd`/`miniflare` — the same runtime `wrangler dev` uses. Actual output:

```
FTS5_PROBE: {"ok":true,"hit":"production uses aws lambda"}
 ✓ test/probe.test.ts > capability probes (DO SQLite) > probe: FTS5 available in Durable Object SQLite?
 ✓ test/isolation.test.ts > PART A: correct name reads its own object (positive control)
 ✓ test/isolation.test.ts > PART A: a DIFFERENT tenant is a DIFFERENT object — physically separate SQLite (NO leak)
 ✓ test/isolation.test.ts > PART A: a DIFFERENT product (correct name) is a DIFFERENT object (NO leak)
 ✓ test/isolation.test.ts > PART A: cross-object memory hydration stays private
 ✓ test/isolation.test.ts > PART B: DROPPING product_id from the name collapses two products onto ONE object (LEAK)
 ✓ test/isolation.test.ts > PART B: WRONG product substitution addresses the wrong (but still isolated) object
 Test Files  2 passed (2)
      Tests  7 passed (7)
```

## Live serve (real HTTP, real Durable Objects, local)

```bash
pnpm dev                # wrangler dev  ->  http://localhost:8787  (this repo used --port 4117)

# Orbit contract: POST /v1/tenant/{tenant}/user/{user}/product/{product}/chat
curl -X POST http://localhost:4117/v1/tenant/acme/user/alice/product/productX/chat \
  -H 'Content-Type: application/json' -d '{"prompt":"first productX message"}'
```

Executed end-to-end against `wrangler dev --local` (real `workerd`, real DOs), actual output:

```
GET  /healthz                                        -> {"status":"ok"}
POST /v1/tenant/acme/user/alice/product/productX/chat (turn 1)
     -> {"reply":"[orbit:acme|alice|productX] I have 0 prior turns ...","hydrated":0,"identity":"acme|alice|productX"}
POST /v1/tenant/acme/user/alice/product/productX/chat (turn 2)
     -> {"reply":"[orbit:acme|alice|productX] I have 2 prior turns ...","hydrated":2,"identity":"acme|alice|productX"}
POST /v1/tenant/acme/user/alice/product/productY/chat (turn 1, DIFFERENT object)
     -> {"reply":"[orbit:acme|alice|productY] I have 0 prior turns ...","hydrated":0,"identity":"acme|alice|productY"}
POST /v1/tenant/globex/user/alice/product/productX/chat (DIFFERENT tenant object)
     -> {"reply":"[orbit:globex|alice|productX] I have 0 prior turns ...","hydrated":0,"identity":"globex|alice|productX"}
```

`hydrated:2` for productX turn 2 proves per-object working memory persists in that DO's
SQLite; the sibling productY and cross-tenant globex objects each start at `hydrated:0` —
each has its own empty SQLite.

## Architecture (what maps to what)

| Orbit concept | Cloudflare Agents mechanism |
|---|---|
| Chat session state / working memory | `this.sql` — embedded SQLite **inside** the Agent's Durable Object (µs, in-process, zero network) |
| Durable JSON state, auto-synced to clients | `this.state` / `this.setState()` (Agents SDK state channel) |
| One (tenant,user,product) slice | One Durable Object, addressed by `getAgentByName(env.OrbitAgent, "tenant|user|product")` |
| Request routing | Worker `fetch` parses the Orbit URL, assembles the name, `getAgentByName` → RPC on the stub |
| Scheduled/async work | `this.schedule()` (cron / delay / date alarms) — built-in |
| Tools / MCP | `agents/mcp` — MCP client **and** server first-class |

Config: `wrangler.jsonc` binds `OrbitAgent` as a **SQLite-backed** Durable Object
(`migrations: [{ new_sqlite_classes: ["OrbitAgent"] }]`). Code: `src/index.ts`.

## Isolation model

**By-construction *object* boundary · convention *identity* binding · rented platform.**

```
Two names  ->  two Durable Objects  ->  two physically separate SQLite databases
             There is NO API to SELECT across Durable Objects.  (unrepresentable)

           getAgentByName(ns, `${tenant}|${user}|${product}`)
                              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
             THE isolation surface: a developer-assembled STRING.
             Right string  -> hard, platform-enforced isolation.
             Wrong string  -> a DIFFERENT-but-still-isolated object -> cross-read.
```

- **Strength (unique in the field):** the shared store is *gone*. Every scope gets its own
  SQLite; a cross-object read cannot be written. This beats every other arena's shared
  string-keyed store, where the leaky and correct reads sit in the *same* store one line apart.
- **Weakness (shared with the field):** the name is a bare string the framework binds to
  nothing. Drop `product_id` → two products share one object → leak (proven, PART B). Same
  failure *class* as Eino's dropped checkpoint segment or LangGraph's short prefix.
- **Sovereignty caveat:** Durable Objects run **only on Cloudflare**. `workerd` (Apache-2.0)
  + `miniflare` simulate them locally (how this arena ran), but production durability +
  global uniqueness + per-object isolation are **rented platform** properties, not a runtime
  you self-host.

Full per-feature breakdown and the exact leak reproductions in [`FEATURES.md`](./FEATURES.md).

## What actually executed vs. source-verified (honesty ledger)

| Piece | Status |
|---|---|
| `agents@0.17.4` real dependency (MIT), pinned in `package.json` | **Real** — `pnpm add`ed, imported, bundled |
| `OrbitAgent extends Agent<Env, State>`, `this.sql`, `setState`, `getAgentByName`, `routeAgentRequest` | **Real API** — signatures verified against the installed `.d.ts`; used as-is |
| Isolation smoke (6 cases) + FTS5 probe | **RAN** in real `workerd` via `@cloudflare/vitest-pool-workers` (7/7) |
| Orbit HTTP route (chat, memory hydration, per-object isolation) | **RAN** against `wrangler dev --local` (real DOs) |
| License scan | **RAN** — `pnpm licenses` over the real installed tree; 0 hard blockers |
| Streaming, MCP, `schedule()`, experimental memory/session, observability event bus | **source-verified, not executed** — classified from the installed SDK's types/exports, labelled `S` in FEATURES.md |
| A real LLM turn | **not run** — Orbit uses a deterministic stub (no API key); a real model slots into `OrbitAgent.chat()` |

Nothing here is faked: the Durable Objects, their embedded SQLite, the RPC stubs, and the
leak are all the genuine framework running locally in `workerd`.

## Adversarial isolation result

**LEAK CONFIRMED on the identity binding; NO LEAK on the object boundary.** The per-object
SQLite boundary held every time the name was correct (cross-tenant and cross-product reads
returned `null`/`hydrated:0`). The leak reproduces exactly when the developer **mis-assembles
the name** — dropping or substituting the `product_id` segment collapses/​misroutes objects.
The boundary is the strongest in the field; the binding to it is still developer convention;
and the guarantee is rented. See `test/isolation.test.ts`.

## How to run

| Task | Command |
|---|---|
| Install | `pnpm install` |
| Smoke (offline, workerd) | `pnpm smoke` |
| Live serve (local DOs) | `pnpm dev` |
| License scan | `./scripts/license_scan.sh` |

## Sources

- [`agents` on npm](https://www.npmjs.com/package/agents) · [Cloudflare Agents docs](https://developers.cloudflare.com/agents/) · [Agent class internals](https://developers.cloudflare.com/agents/concepts/agent-class/) · [Agents API (`getAgentByName`, `routeAgentRequest`)](https://developers.cloudflare.com/agents/api-reference/agents-api/) · [Store & sync state (`this.sql`)](https://developers.cloudflare.com/agents/api-reference/store-and-sync-state/)
