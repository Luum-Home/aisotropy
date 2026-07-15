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

## Isolation model

**Closest of the three to construction — but still convention.**

Mastra uses `resourceId`/`threadId` for scoping. These are server-authoritative keys with
ownership validation, making it closer to isolation-by-construction than Pydantic AI or
LangGraph. However, a key without a tenant prefix still filters across tenants.

The bug [mastra#11856](https://github.com/mastra-ai/mastra/issues/11856) proved this:
`listMessages` was threadId-only, missing the resourceId filter — a real cross-tenant leak.

```typescript
// The correct path:
const mastra = new Mastra({
  storage: new MemoryStorage(),
  // resourceId scopes to a tenant
});

const agent = new Agent({
  name: 'support',
  instructions: 'You are a support agent.',
  model: openai('gpt-4o'),
});

// THE LEAK: if resourceId is missing or wrong, threads cross tenants
const thread = await mastra.getThreads({ resourceId: 'tenantA' });
// If you pass 'tenantA' when the request came from 'tenantB' → leak
```

Both paths are structurally harder to get wrong than Pydantic AI (keys are more authoritative),
but a key without tenant scoping still leaks.

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
| Multi-tenant isolation | Convention (closest) | `resourceId`/`threadId` with ownership validation, but mastra#11856 proved leaks |

## DX notes

**What's great:**
- Most batteries-included TS framework — memory, tools, streaming, structured output all built-in.
- `resourceId`/`threadId` scoping is the closest any competitor comes to isolation-by-construction.
- Memory processors that trim retrieved messages before the context window — cognitive efficiency.
- Apache 2.0 license (relicensed from ELv2 after enterprise pushback — market proof that permissive matters).

**What's painful:**
- Largest build of the three (~1240 lines). Writing the AI-SDK v5 model stub by hand was the
  most frustrating part — the abstraction layer is thick.
- TypeScript ecosystem is young — tooling and docs are still maturing.
- `listMessages` threadId-only bug (mastra#11856) — even the closest framework had a real leak.
- No facts memory tier, no cost governance, no built-in observability — partially built-in,
  partially DIY.

## Adversarial isolation result

**LEAK CONFIRMED (structural).** While Mastra is the closest competitor to isolation-by-construction,
the smoke test demonstrates that when `resourceId` is omitted or mismatched, cross-tenant reads
still occur. The bug mastra#11856 (`listMessages` ignoring resourceId) is living proof.

See `scripts/smoke.ts` for the exact test — it uses Mastra's MemoryStorage with deliberately
mismatched `resourceId` values to prove the leak.
