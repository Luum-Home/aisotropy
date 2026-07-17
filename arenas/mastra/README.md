# Mastra Arena

> **Framework:** Mastra (Apache 2.0)
> **DX rating:** Most batteries-included TS framework — everything is built-in, but the build is largest (~1240 lines). Best isolation of the three competitors, but still convention-based.
> **Build pain:** Writing the AI-SDK v5 model stub by hand was the most painful part.

## Setup

```bash
# Requires Node 20+
npm install

# For live LLM mode
export OPENAI_API_KEY=sk-...
# or
export OPENROUTER_API_KEY=sk-or-...
```

## Smoke test (offline, stub LLM)

```bash
npm run smoke
```

Runs the adversarial isolation matrix using Mastra's in-memory storage. No LLM API key needed.

## Live serve

```bash
# Starts the Mastra server at :4111
npm run serve

# Test it:
curl -X POST http://localhost:4111/v1/tenant/acme/user/alice/chat \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"hello","session_id":"s1"}'
```

## Docker smoke (containerized, offline)

Proves the framework's minimal app works **containerized** — a real Mastra `Agent` on
an offline AI-SDK v5 mock model (no API key) plus real `@mastra/memory` (`LibSQLStore`),
served over HTTP — and drives the isolation assertion against that running container:

```bash
bash docker/run.sh
```

One command does the whole closed loop: `docker build` → `docker run` (port 4111) →
wait for `/healthz` → drive the smoke over HTTP → assert → remove the container.

What it asserts against the containerized server (`docker/server.mjs`):

- `mock_model.chat` — `POST /chat` returns a deterministic reply with **no key** (the
  minimal app boots and serves a model response).
- `positive_control` — secret-A stored as **tenant A** (`POST /remember`) is returned
  by tenant A's own recall (`GET /recall?tenant=tenantA&thread=shared`).
- `isolation.cross_tenant_read` — a **cross-tenant** recall
  (`?tenant=tenantB&thread=shared`) does **NOT** see secret-A: Mastra's
  thread↔resource ownership guard refuses the read
  (`Thread ... is for resource tenantA but resource tenantB was queried`), surfaced
  as `{"isolated":true}`. This is the HTTP-served analogue of `npm run smoke` /
  `npm run product-isolation`, run against the real framework inside the container.

If Docker is not installed (or the daemon is down), `run.sh` **skips cleanly**
(exit 0, no fake pass) and points you at the native `npm run smoke`.

**Caveats (honest):** the image is `node:22-slim` + the pinned Mastra deps (`docker/package.json`,
installed with `--omit=dev`) ≈ **~740 MB**; a cold `docker build` is ~60–75 s (base pull +
`npm install` of ~267 packages), ~35 s warm (cached base layer). `docker/` is self-contained
and declares its own pinned deps — it does not reuse the arena root's ad-hoc, undeclared
`node_modules`. The container keeps memory on-disk via `LibSQLStore` (`AISOTROPY_DATA_DIR=/app/data`).

## What Mastra does genuinely well (run-verified)

Mastra is a **TypeScript-native agent framework**, and on the axes it is built for it is excellent —
aisotropy, a Go runtime, is not a TS framework at all, so this is not even a contest here. Run
`npm run strengths` (`scripts/strengths_demo.ts`, offline, exit 0) to execute the following against the
pinned install (`@mastra/core@1.51.0`):

- **Durable, typed workflows** — `createWorkflow`/`createStep` (`@mastra/core/workflows`) with real
  control flow: `.then` (sequential), `.branch` (conditional), and **suspend/resume** for
  human-in-the-loop. The demo routes a small charge through the auto-approve branch, suspends a large
  charge at a `humanReview` step (snapshot persisted via `LibSQLStore`), then `resume()`s it with typed
  reviewer input to completion. Steps carry Zod `input`/`output`/`resume`/`suspend` schemas end to end,
  so a malformed input is rejected at the boundary — verified live.
- **First-class evals / scorers** — `createScorer` (`@mastra/core/evals`) exposes a composable
  `preprocess → generateScore → generateReason` pipeline. The demo builds a code-based
  keyword-coverage scorer (no LLM) that scores a complete answer `1.00` and an off-topic one `0.00`,
  each with a human-readable reason.
- **Batteries-included memory DX** — working-memory + semantic recall + memory processors, all
  built-in and run-verified in `scripts/feature_parity_demo.ts` (`npm run parity`).

These are TS-native ergonomics aisotropy does not try to offer. **Right tool, right job:** reach for
Mastra when you want a polished TypeScript workflow/eval/memory framework; the arena's one narrow
counter-point is isolation *by default* (below), not a claim that Mastra is weak.

## Isolation model

**A competent developer CAN isolate every axis. The distinction is convention vs construction.**

Mastra ships **four documented scoping levers**, and a developer who uses them isolates tenants,
users, and products correctly:

1. **`resourceId`** — owner partition; Mastra binds each `threadId` to its owning `resourceId` and
   throws `validateThreadIsOwnedByResource` on a cross-resource read (run-verified in
   `scripts/product_isolation_demo.ts`).
2. **`threadId`** — conversation partition.
3. **working-memory `scope`** — `'resource' | 'thread'`
   ([docs](https://mastra.ai/docs/memory/working-memory)).
4. **`semanticRecall.filter`** — MongoDB-style metadata filter over the vector store; the docs' own
   example is `filter: { projectId: { $eq: 'project-a' } }`, with operators `$and/$or/$eq/$ne/$gt/$in/…`
   ([docs](https://mastra.ai/docs/memory/semantic-recall)). Run-verified in the product demo: filtering
   recall to one resource returns only that resource's rows.

So the earlier "the memory layer never keys on it" claim was wrong. It DOES key — on `resourceId`
and `threadId`, on both the message-history and semantic-recall paths, plus the metadata filter.

**The honest, narrower thesis:** none of the four levers is a **server-authoritative, unforgeable,
auto-propagated axis**. Each is a convention the developer must remember to apply on every call, and
the **default path** — product/tenant placed in none of the levers, riding only as message metadata —
silently does not partition (a developer omission, not a framework defect). `mastra#11856`
(`listMessages` threadId-only, missing the resourceId filter) is the class of bug this leaves open:
one forgotten filter, one convention not applied.

```typescript
// The isolating path (composite key — folds product/tenant into the authoritative resourceId):
const memory = new Memory({
  storage: new LibSQLStore({ url: 'file:store.db' }),
  options: { semanticRecall: { scope: 'resource' }, workingMemory: { scope: 'resource' } },
});
// resourceId = 'tenant:user:product' → cross-product read throws (ownership-validated).
// Or scope semantic recall by metadata: filter: { projectId: { $eq: 'productX' } }.
```

Contrast: aisotropy's `Scope{}` value object (unexported fields, built from verified identity,
carried via `context.Context`) filters `tenant_id`/`user_id`/`session_id`/`product_id` server-side on
**every** read whether or not the developer thought about it — a wrong or forgotten axis is
unrepresentable by construction, not merely discouraged.

## Features covered

| Feature | Status | Notes |
|---|---|---|
| Structured output | Built-in | AI-SDK v5 native |
| Tool calling | Built-in | Native tool calling |
| Streaming | Built-in | AI-SDK streaming |
| Working memory | Built-in | Resource-scoped + thread-scoped |
| Semantic memory | Built-in | Vector recall (built-in) |
| Facts memory | DIY | Not provided |
| Cost governance | DIY | Not built-in |
| Observability | DIY | BYO |
| Multi-tenant isolation | Convention (four levers) | `resourceId`/`threadId` + working-memory scope + `semanticRecall.filter`, all ownership/metadata-validated. Isolable by a competent dev; convention, not auto-propagated construction |

## DX notes

**What's great:**
- Most batteries-included TS framework — memory, tools, streaming, structured output all built-in.
- Four real scoping levers (`resourceId`/`threadId` + working-memory scope + `semanticRecall.filter`),
  with thread↔resource ownership validation — the closest any competitor comes to authoritative keys.
- Memory processors that trim retrieved messages before the context window — cognitive efficiency.
- Apache 2.0 license (relicensed from ELv2 after enterprise pushback — market proof that permissive matters).

**What's painful:**
- Largest build of the three (~1240 lines). Writing the AI-SDK v5 model stub by hand was the
  most frustrating part — the abstraction layer is thick.
- TypeScript ecosystem is young — tooling and docs are still maturing.
- Isolation is convention: each lever is a per-call responsibility, and the default (axis in no lever)
  silently does not partition. `mastra#11856` (`listMessages` threadId-only) is the class of bug this
  leaves open — one forgotten filter.
- No facts memory tier, no cost governance, no built-in observability — partially built-in,
  partially DIY.

## Adversarial isolation result

**Isolable — convention, not construction.** A competent developer prevents cross-tenant and
cross-product reads with Mastra's four levers, and `scripts/product_isolation_demo.ts` run-verifies
BOTH competent paths against the **real** `@mastra/memory`: (c) folding product into the `resourceId`
makes a cross-product read throw (ownership-validated); (d) `semanticRecall.filter` scopes recall to a
single resource. What the demo also shows honestly: the **naive default path** — product placed in
none of the levers, riding only as metadata — does not partition, because that is a developer
omission, not a framework guarantee. The framework gives you the levers; it does not auto-propagate an
unforgeable identity axis the way aisotropy's `Scope{}` does.

- `scripts/product_isolation_demo.ts` (`npm run product-isolation`) — real `@mastra/memory`; the
  authoritative demo. Naive path vs the two competent paths, exit 0.
- `scripts/smoke.js` (`npm run smoke`) — a small **simulation** of the keying model (a hand-written
  store, not the real framework) illustrating that a developer-passed key is a convention: pass the
  wrong `resourceId` and you read the wrong data, because the key is an opaque string, not a
  server-derived unforgeable `Scope`.
