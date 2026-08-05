# Cloudflare Agents Arena

> **Framework:** [`agents`](https://www.npmjs.com/package/agents) `0.17.4` (MIT) — build AI
> Agents on Cloudflare Workers + Durable Objects.
> **Integration:** **REAL** — installed the real SDK, built an Orbit chatbot with its current
> API, and ran the isolation smoke inside genuine Durable Objects in `workerd` (via
> `@cloudflare/vitest-pool-workers` and `wrangler dev --local`). No API key, offline.
> **Headline finding:** each Agent is **one Durable Object with its OWN embedded SQLite**, so
> the per-object storage boundary is a **hard, by-construction platform guarantee** (no
> cross-object read exists) — genuinely *stronger* than the shared string-keyed stores in the
> other seven arenas. **Two honest caveats:** (1) the `(tenant,user,product)→object` binding
> is a developer-assembled **name string** — get the name wrong (drop the product axis) and two
> products address the same object; that is a same-key developer bug, not a framework defect,
> and the SDK gives a first-class seam (`onBeforeConnect`) to bind the name to a verified
> identity, which this arena now demonstrates at equal weight; (2) the whole guarantee is a
> **rented Cloudflare-platform** boundary, not self-hostable OSS.

## Setup

```bash
# Requires Node 20+ and pnpm.
pnpm install            # agents@0.17.4 + wrangler + @cloudflare/vitest-pool-workers + vitest

# For a live LLM the Orbit chat uses a deterministic stub by default (no key needed).
# A real model would slot into OrbitAgent.chat() via a Workers AI / OpenAI / ai-sdk call.
```

## Smoke test (offline, real workerd, no key)

```bash
pnpm smoke              # = vitest run  -> 10 passed
```

Runs the adversarial isolation matrix (`test/isolation.test.ts`) plus a DO-SQLite FTS5
capability probe (`test/probe.test.ts`) **inside real Durable Objects** simulated by
`workerd`/`miniflare` — the same runtime `wrangler dev` uses. PART A proves the object
boundary holds; PART B shows the naive-convention failure (a same-key developer bug); PART C
shows the strong identity-bound path that closes it. Actual output:

```
FTS5_PROBE: {"ok":true,"hit":"production uses aws lambda"}
 ✓ test/probe.test.ts > probe: FTS5 available in Durable Object SQLite?
 ✓ test/isolation.test.ts > PART A: correct name reads its own object (positive control)
 ✓ test/isolation.test.ts > PART A: a DIFFERENT tenant is a DIFFERENT object — physically separate SQLite (NO leak)
 ✓ test/isolation.test.ts > PART A: a DIFFERENT product (correct name) is a DIFFERENT object (NO leak)
 ✓ test/isolation.test.ts > PART A: cross-object memory hydration stays private
 ✓ test/isolation.test.ts > PART B: dropping the product axis makes two products share ONE object (same-key collision, a developer bug — not a framework defect)
 ✓ test/isolation.test.ts > PART B: asking for the wrong object returns the wrong object (addressing, not a leak — a tautology of any name->object map)
 ✓ test/isolation.test.ts > PART C: the DO name is derived from VERIFIED claims, not caller-supplied path text
 ✓ test/isolation.test.ts > PART C: must-agree bind REJECTS a caller asking for a product its token does not authorize (403, before any object is addressed)
 ✓ test/isolation.test.ts > PART C: a self-consistent, verified request resolves to exactly its own object and no other
 Test Files  2 passed (2)
      Tests  10 passed (10)
```

## Live serve (real HTTP, real Durable Objects, local)

```bash
pnpm dev                # wrangler dev  ->  http://localhost:8787  (this repo used --port 4117)

# Naive path (no credential): the URL scope is trusted as-is.
# Orbit contract: POST /v1/tenant/{tenant}/user/{user}/product/{product}/chat
curl -X POST http://localhost:4117/v1/tenant/acme/user/alice/product/productX/chat \
  -H 'Content-Type: application/json' -d '{"prompt":"first productX message"}'

# Strong path (credential present): the DO name is bound to VERIFIED claims via a
# must-agree check. A token that authorizes productX cannot reach productY (403).
curl -X POST http://localhost:4117/v1/tenant/acme/user/alice/product/productY/chat \
  -H 'Authorization: Bearer tok:acme|alice|productX' \
  -H 'Content-Type: application/json' -d '{"prompt":"hi"}'      # -> HTTP 403
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

# Strong path (must-agree bind), verified this pass:
POST .../product/productY/chat  Authorization: Bearer tok:acme|alice|productX  -> HTTP 403
POST .../product/productX/chat  Authorization: Bearer tok:acme|alice|productX  -> HTTP 200, identity "acme|alice|productX"
```

`hydrated:2` for productX turn 2 proves per-object working memory persists in that DO's
SQLite; the sibling productY and cross-tenant globex objects each start at `hydrated:0` —
each has its own empty SQLite.

## Containerized smoke (Docker — optional, run-verified)

```bash
./docker/run.sh        # builds docker/Dockerfile, boots wrangler dev inside a
                       # node container, drives the per-DO isolation smoke over
                       # HTTP against it, asserts, cleans up. SKIPs if docker absent.
```

**Feasibility verdict: YES — it containerizes, and the full DO smoke RAN in a container.**
This was not a given. Cloudflare Agents are **Durable Objects on `workerd`**, not a generic
Linux container process, and `wrangler` embeds a platform-specific `workerd` binary — so the
image cannot reuse the host `node_modules` (that carries `@cloudflare/workerd-darwin-*`); it
runs a **fresh Linux install** to pull `@cloudflare/workerd-linux-*`, on a **glibc** base
(Debian, not Alpine — `workerd` does not link musl cleanly). With that, `wrangler dev` boots
real `workerd` + real Durable Objects (binding shows `Mode: local`) inside the container, and
`docker/run.sh` drives the same isolation matrix the native `pnpm dev` path proves — over HTTP
against the containerized instance. Actual output:

```
==> Waiting for the DO-backed endpoint to come up ...
    ready after 4s
  PASS: productX turn 1 -> hydrated:0
  PASS: productX turn 2 -> hydrated:2 (per-object memory persisted)
  PASS: productY (different object) -> hydrated:0 (isolated)
  PASS: globex/productX (different tenant) -> hydrated:0 (isolated)
  PASS: productY response has its own identity, no productX bleed-through
  PASS: must-agree bind rejects wrong-product token -> HTTP 403
  PASS: self-consistent verified request -> HTTP 200
CONTAINERIZED DO SMOKE: PASS — real workerd + Durable Objects + per-object
SQLite isolation verified over HTTP against the container.
```

**Honest scope of what the container proves (and does NOT).** The container reproduces the
**local-fidelity** runtime — `workerd`/`miniflare` with real DOs and real embedded SQLite,
exactly what the native `pnpm smoke`/`pnpm dev` use — just as a reproducible image instead of
on the host. It does **not**, and *cannot*, reproduce Cloudflare's production edge: global
Durable Object uniqueness and rented durability are **platform** properties that live only on
Cloudflare, not in any self-hosted container (see the **Sovereignty caveat** below). So the
Docker path adds reproducibility and CI portability for the isolation smoke; it does **not**
turn the rented-platform guarantee into a self-hostable one. That boundary is the arena's
central honest caveat, and containerizing the local runtime does not erase it.

## Architecture (what maps to what)

| Orbit concept | Cloudflare Agents mechanism |
|---|---|
| Chat session state / working memory | `this.sql` — embedded SQLite **inside** the Agent's Durable Object (µs, in-process, zero network) |
| Durable JSON state, auto-synced to clients | `this.state` / `this.setState()` (Agents SDK state channel) |
| One (tenant,user,product) slice | One Durable Object, addressed by `getAgentByName(env.OrbitAgent, "tenant|user|product")` |
| Request routing | Worker `fetch` parses the Orbit URL → naive path assembles the name from the URL; strong path (credential present) verifies a token and mints the name from the **verified claims** (must-agree, 403 on mismatch), the `onBeforeConnect`-style check → `getAgentByName` → RPC on the stub |
| Scheduled/async work | `this.schedule()` (cron / delay / date alarms) — built-in |
| Tools / MCP | `agents/mcp` — MCP client **and** server first-class |

Config: `wrangler.jsonc` binds `OrbitAgent` as a **SQLite-backed** Durable Object
(`migrations: [{ new_sqlite_classes: ["OrbitAgent"] }]`). Code: `src/index.ts`.

## Isolation model

**By-construction *object* boundary · convention *identity* binding · rented platform.**

```
Two names  ->  two Durable Objects  ->  two physically separate SQLite databases
             There is NO API to SELECT across Durable Objects.  (unrepresentable)

  NAIVE path (PART B):  getAgentByName(ns, `${tenant}|${user}|${product}`)  <- from the URL
                                          ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
             A developer-assembled STRING the framework binds to nothing.
             Right string -> hard, platform-enforced isolation.
             Wrong string (drop the product axis) -> a DIFFERENT object,
                          two products collide -> same-key developer bug.

  STRONG path (PART C):  verify(token) -> claims;  must-agree(URL scope == claims);
                         getAgentByName(ns, nameFromClaims(claims))
             The name is minted from a VERIFIED identity, so the wrong name
             cannot be built. The SDK gives the seam (`onBeforeConnect`); the
             binding to identity is your middleware — same shape as aisotropy's
             ADR-0024 must-agree JWT bind.
```

- **Strength (unique in the field):** the shared store is *gone*. Every scope gets its own
  SQLite; a cross-object read cannot be written. This beats every other arena's shared
  string-keyed store, where the leaky and correct reads sit in the *same* store one line apart.
  Credit where due: **this is genuinely the strongest storage boundary in the comparison.**
- **The naive-convention trap (PART B):** the name is a bare string the framework binds to
  nothing. Drop `product_id` → two products address one object. This is a *same-key developer
  bug*, tautologically true of any name→object map (aisotropy's own `Scope` included) — **not a
  framework defect.** Same failure *class* as Eino's dropped checkpoint segment or LangGraph's
  short prefix. The second PART B case ("wrong substitution") is weaker still: asking for the
  wrong object and getting it is *addressing*, not a leak — no cross-object read occurs.
- **The strong path closes it (PART C):** the SDK's routing entry points run `onBeforeConnect`
  /`onBeforeRequest` before the DO wakes (source-verified: `getAgentByName` deliberately does
  **not** run them — "the caller is assumed to have performed whatever access checks are
  needed"). The competent-dev pattern verifies a credential there and derives the DO name from
  the **verified claims**, rejecting a URL scope that disagrees (403). Cloudflare still offers
  no value-object to make a dropped axis a *compile* error — that is aisotropy's edge — but the
  binding seam exists and is first-class.
- **Sovereignty caveat (real, load-bearing):** Durable Objects run **only on Cloudflare**.
  `workerd` (Apache-2.0) + `miniflare` simulate them locally (how this arena ran), but
  production durability + global uniqueness + per-object isolation are **rented platform**
  properties, not a runtime you self-host.

Full per-feature breakdown, the naive-path collision, and the strong identity-bound path in
[`FEATURES.md`](./FEATURES.md).

## What actually executed vs. source-verified (honesty ledger)

| Piece | Status |
|---|---|
| `agents@0.17.4` real dependency (MIT), pinned in `package.json` | **Real** — `pnpm add`ed, imported, bundled |
| `OrbitAgent extends Agent<Env, State>`, `this.sql`, `setState`, `getAgentByName` | **Real API** — signatures verified against the installed `.d.ts`; used as-is |
| `onBeforeConnect` seam claim (strong path) | **Source-verified** — the installed `.d.ts` states `getAgentByName` does **not** run `onBeforeConnect`; the hook lives on the routing path (`partyserver` `PartyServerOptions.onBeforeConnect`). The PART C must-agree binding is modelled on that seam and **RAN** |
| Isolation smoke: PART A object boundary (4) + PART B naive-convention bug (2) + PART C strong identity-bound path (3) + FTS5 probe | **RAN** in real `workerd` via `@cloudflare/vitest-pool-workers` (10/10) |
| Orbit HTTP route (chat, memory hydration, per-object isolation) | **RAN** against `wrangler dev --local` (real DOs) |
| License scan | **RAN** — `pnpm licenses` over the real installed tree; 0 hard blockers |
| Streaming, MCP, `schedule()`, experimental memory/session, observability event bus | **source-verified, not executed** — classified from the installed SDK's types/exports, labelled `S` in FEATURES.md |
| A real LLM turn | **not run** — Orbit uses a deterministic stub (no API key); a real model slots into `OrbitAgent.chat()` |

Nothing here is faked: the Durable Objects, their embedded SQLite, the RPC stubs, the naive-path
collision, and the identity-bound strong path are all the genuine framework running locally in
`workerd`.

## Adversarial isolation result

**The object boundary holds by construction; the name→object binding is developer convention
with a first-class identity seam.** Every correct-name read stayed private (cross-tenant and
cross-product reads returned `null`/`hydrated:0`) — a physically separate SQLite per object,
the strongest storage boundary in the field. The only way two scopes collide is a
**mis-assembled name** (drop the `product_id` axis): a *same-key developer bug*, not a
framework defect, and the same failure class as any name→object map. PART C demonstrates the
strong path that closes it — derive the DO name from a **verified** identity via the SDK's
`onBeforeConnect` seam, rejecting a caller who asks for a scope its token does not authorize
(403, before any object is addressed). The boundary is the strongest in the field; the binding
is developer middleware the framework supports; and the guarantee is rented. See
`test/isolation.test.ts` (PART A/B/C).

## What Cloudflare Agents does genuinely well (run-verified steelman)

Not faint praise — these are real, and several beat aisotropy on their own axis:

- **Physical per-object storage isolation — simply stronger than aisotropy here.** There is no
  shared store to guard: two names are two Durable Objects with two separate SQLite databases,
  and a cross-object `SELECT` is *unrepresentable*. aisotropy still keeps tenants in shared
  tiers behind a value-object `Scope`; Cloudflare deletes the shared store entirely. **[RAN]**
- **A real in-process SQLite per object, with FTS5.** `this.sql` is a full SQLite embedded in
  the DO (µs, zero network); the FTS5 probe returned a live match — so a keyword facts journal
  lives *inside the per-entity object*. LangGraph runs in-process SQLite too (`SqliteSaver`,
  `SqliteStore`), but its locality is per-OS-process, not per-conversation, and its server tier
  requires external Postgres+Redis — so it has no addressable object to put the journal *in*.
  **[RAN — the FTS5 probe; the LangGraph comparison is source-verified, not run]**
  <!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-langgraph-state-locality-2026-08-04.md -->
  <!-- Was: "something LangGraph cannot do at all. [RAN]". Both halves were wrong.
       LangGraph ships SqliteSaver/SqliteStore/InMemorySaver natively (release 2026-07-30),
       and this repo's own arenas/live/langgraph/modes/common.py opens an in-process
       SqliteSaver — our adapter ran what this line called impossible. And the [RAN]
       marker covered a comparison that was never executed: what ran was the FTS5 probe
       on this side. The marker now says which half ran, because a "measured" stamp over
       an unmeasured claim corrupts the convention the arena uses to separate the two. -->
- **Best offline-fidelity test story in the field.** `@cloudflare/vitest-pool-workers` runs
  tests **inside real `workerd`** with real DOs and real SQLite — what runs in the test is the
  production runtime, no mocks, no cloud, no key. **[RAN, 10/10]**
- **MCP client *and* server, first-class** (`agents/mcp`, `McpAgent`, OAuth provider),
  `this.schedule()` cron/delay alarms, and WebSocket-hibernation streaming are all built in —
  the three things most arenas make you assemble. **[source-verified]**
- **Transactional single-DO storage** makes an atomic turn+intent write *possible* inside one
  object, where 5 arenas classify the outbox pattern NOT-POSSIBLE. **[source-verified]**

Where aisotropy differs (not "better everywhere" — different axis): it is a **self-hostable
binary you own**, not a rented platform; it ships **four memory tiers of different nature** +
enforced **pre-spend/cost/guardrail governance**; and it makes the identity→scope binding
**unrepresentable-to-get-wrong** at the type level (value-object `Scope` with unexported
fields, `product_id` carried on `context.Context`) rather than a bare string bound by
convention. On raw storage isolation, Cloudflare wins outright.

## How to run

| Task | Command |
|---|---|
| Install | `pnpm install` |
| Smoke (offline, workerd) | `pnpm smoke` |
| Live serve (local DOs) | `pnpm dev` |
| Containerized DO smoke (Docker, optional) | `./docker/run.sh` |
| License scan | `./scripts/license_scan.sh` |

## Sources

- [`agents` on npm](https://www.npmjs.com/package/agents) · [Cloudflare Agents docs](https://developers.cloudflare.com/agents/) · [Agent class internals](https://developers.cloudflare.com/agents/concepts/agent-class/) · [Agents API (`getAgentByName`, `routeAgentRequest`)](https://developers.cloudflare.com/agents/api-reference/agents-api/) · [Store & sync state (`this.sql`)](https://developers.cloudflare.com/agents/api-reference/store-and-sync-state/)
