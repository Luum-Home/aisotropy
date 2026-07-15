# Pydantic AI Arena

> **Framework:** Pydantic AI v2 (MIT)
> **DX rating:** Best-typed DX of the three — `deps_type`/`RunContext` is first-class DI, structured output fail-closed natively, `TestModel` gives a killer offline mode.
> **Build size:** 838 lines (smallest of the three)

## Setup

```bash
# Requires Python 3.13 (3.14 has no wheels yet)
python3.13 -m venv .venv
source .venv/bin/activate
pip install pydantic-ai httpx pydantic

# Optional: for live LLM mode
export OPENAI_API_KEY=sk-...
# or
export OPENROUTER_API_KEY=sk-or-...
```

## Smoke test (offline, stub LLM)

```bash
python scripts/smoke.py
```

The smoke test runs a fully offline adversarial isolation exercise (no LLM API key needed). It
asserts isolation deterministically at the memory-store layer, and additionally constructs a
Pydantic AI `Agent` backed by `TestModel` to confirm the installed DI + structured-output wiring
boots. Exits 0 when the positive control passes and the expected leak reproduces.

## Live serve

```bash
# Starts a FastAPI/uvicorn app at :4110 with the orbit pattern
uvicorn orbit.app:app --port 4110

# Test it:
curl -N -X POST http://localhost:4110/v1/tenant/acme/user/alice/chat/stream \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"hello","session_id":"s1"}'
```

## Isolation model

**Convention — developer-passed `tenant_id` via `RunContext` DI.**

Pydantic AI v2's `deps_type` / `RunContext` gives excellent dependency injection — you can pass
a `tenant_id` through the DI system and it's clean. But it's a **convention**, not a guarantee:

```python
# The correct path:
@agent.tool
async def get_messages(ctx: RunContext[deps], session_id: str) -> list[Message]:
    tenant_id = ctx.deps.tenant_id  # <-- developer must remember to pass this
    return db.query(Message).where(Message.tenant_id == tenant_id)

# The leaky path (same line, wrong value):
async def leaky(ctx: RunContext[deps]):
    ctx.deps.tenant_id = "tenantA"  # accidentally passed tenantB's deps
    # → silent cross-tenant read
```

Both paths are one line apart. The framework doesn't prevent the leak.

## Features covered

| Feature | Status | Notes |
|---|---|---|
| Structured output | Built-in | `pydantic.TypeAdapter`, fail-closed via `raise ModelRetry` |
| Tool calling | Built-in | `@agent.tool` decorator |
| Streaming | Built-in | `AsyncAgent.stream()` |
| Working memory | DIY | App-level dict / Redis — no built-in tiered memory |
| Semantic memory | DIY | BYO Chroma/Pinecone/Weaviate |
| Facts memory | DIY | Not provided |
| Cost governance | Partial | `UsageLimits` per-run, not per-tenant |
| Observability | DIY | BYO OTel — no native integration |
| Multi-tenant isolation | Convention | `RunContext[deps].tenant_id` — developer responsibility |

## DX notes

**What's great:**
- `TestModel` is a killer for offline testing — spin up a fake LLM, assert on calls, no API key.
- `deps_type`/`RunContext` gives first-class dependency injection that feels natural.
- Structured output with Pydantic models is the smoothest of the three — you define the schema
  once, the framework handles validation and retry.
- Smallest codebase to understand — 838 lines means you can read the whole framework in an afternoon.

**What's painful:**
- Multi-tenant docs are thin — exactly where you'd need guidance (tenant scoping, isolation),
  the docs are quiet. You're on your own.
- No built-in memory tiers — working memory, semantic search, facts journal: all DIY.
- `UsageLimits` is per-run, not per-tenant — you can cap a single agent run but not a tenant's
  aggregate spend.
- The `orbit-support-agent` demo (reference implementation by coleam00) confirms: "no database
  / session handling, memory persistence, or multi-tenancy mechanisms."

## Adversarial isolation result

**LEAK CONFIRMED.** The smoke test (`.venv/bin/python scripts/smoke.py`, exits 0) proves that a
wrong developer-passed `tenant_id` results in a silent cross-tenant read. The correct path and
the leaky path are one variable assignment apart.

The test is deterministic at the memory-store layer — the same DIY store the `get_context` tool
reads via `get_recent_messages`, not a stub-LLM echo. It (1) seeds tenant A's secret, (2) verifies
the positive control (tenant A reads its own key → marker present), (3) shows the exact-key store
is safe when the key is right (tenant B with its own key → no marker), then (4) demonstrates the
leak: a tenant B request served with tenant A's `deps.tenant_id` reads tenant A's marker. Pydantic
AI has no built-in tenant isolation — the lookup key is developer-passed convention, never checked
against the caller. (A live agent construction + `run_sync` also runs to confirm the installed
`Agent(model=, output_type=)` + `RunContext` DI wiring, but isolation is asserted at the store.)
