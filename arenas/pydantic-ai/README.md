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

The smoke test runs a fully offline isolation-model exercise (no LLM API key needed). It shows,
deterministically, that Pydantic AI ships no memory or multi-tenancy layer: scoping happens in a
BYO dict keyed by a developer-computed key. The exact-key store partitions correctly when the key
is right, and a wrong developer-computed key reads the wrong slice — because there is no framework
isolation layer to prevent it (the framework has no tenant/product concept). It additionally
constructs a Pydantic AI `Agent` backed by `TestModel` to confirm the installed DI +
structured-output wiring boots. Exits 0 when the positive control and the correct-scoping control
pass. This is a DIY/out-of-scope classification for the framework, not a leak in it.

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

**No isolation layer exists — it is 100% BYO / developer-managed (DIY).**

Pydantic AI is a **stateless agent library**. By design it ships **no memory store and no
multi-tenancy layer**: "Agents are stateless and designed to be global"; "no built-in persistent
memory, your agent starts from scratch every run"
([dependencies](https://ai.pydantic.dev/dependencies/),
[message-history](https://ai.pydantic.dev/message-history/)). `deps_type` / `RunContext` gives
excellent, first-class dependency injection — but `deps` is a plain object the **developer**
constructs, bound to nothing: no principal, no auth, no tenant concept. Any scoped memory is your
own dict / Redis / DB, keyed by a key **you** compute:

```python
# You own the store AND the key:
@agent.tool
async def get_messages(ctx: RunContext[Deps], session_id: str) -> list[Message]:
    tenant_id = ctx.deps.tenant_id  # a field you put in deps; the framework binds it to nothing
    return db.query(Message).where(Message.tenant_id == tenant_id)
```

There is no framework boundary to fail here. If a developer computes a scoping key wrong in their
own store, the framework has no concept of tenant/user/product to object — a plain dict returns
whatever key it is handed, exactly like any KV store (including aisotropy's BadgerDB). The
difference is **who owns key computation**: here it is entirely the developer.

**aisotropy's differentiator:** it computes and binds all four axes
(`tenant_id`/`user_id`/`session_id`/`product_id`) at the request boundary and propagates them via
`context.Context`, so the **default** read path is already scoped — no unscoped read can be issued.

## Features covered

| Feature | Status | Notes |
|---|---|---|
| Structured output | Built-in | `pydantic.TypeAdapter`, fail-closed via `raise ModelRetry` |
| Tool calling | Built-in | `@agent.tool` decorator |
| Streaming | Built-in | `agent.run_stream(...)` (v2 async context manager) |
| Working memory | DIY | App-level dict / Redis — no built-in tiered memory |
| Semantic memory | DIY | BYO Chroma/Pinecone/Weaviate |
| Facts memory | DIY | Not provided |
| Cost governance | Partial | `UsageLimits` per-run, not per-tenant |
| Observability | DIY | BYO OTel — no native integration |
| Multi-tenant isolation | DIY (no layer) | No memory/tenancy layer shipped; `RunContext[deps].tenant_id` is a developer-computed key bound to nothing |

## What Pydantic AI does genuinely well

The isolation classification above is the honest "DIY" side of the ledger. Here is the other side:
where Pydantic AI is genuinely excellent — **type-safety and agent-loop DX** — it is best-in-class.
The three strengths below are **RUN-VERIFIED** against the pinned `pydantic-ai>=2.0.0` (installed
2.10.0), fully offline. Run them: `./.venv/bin/python scripts/strengths_demo.py` (exit 0 = all held).

**1. Structured output is a typed contract — fail-closed and self-correcting.**
`output_type=<PydanticModel>` makes the model's answer a *validated Python object*, not a string you
parse. A `Union[...]` output type routes to the correct typed branch for free. And it fails closed:
an output that violates a field constraint triggers the reflection loop (the exact validation error
is fed back as a retry prompt) until it validates or `retries` is exhausted — at which point the
framework raises `UnexpectedModelBehavior` rather than returning silent bad text.

```python
from pydantic import BaseModel, Field
from pydantic_ai import Agent

class Sku(BaseModel):
    sku: str = Field(pattern=r"^[A-Z]{3}-\d{4}$")   # a real constraint

agent = Agent("openai:gpt-4o", output_type=Sku, retries=3)
result = agent.run_sync("make me a SKU")
result.output           # -> Sku(sku='ABC-1234'), typed & validated — or it raised, never garbage
```
Verified: typed object out (`Weather`), union routing (`Union[Refund, Escalate]` → `Escalate`),
invalid→retry→valid on generation #2, and exhausted-retries → `UnexpectedModelBehavior`.

**2. First-class typed dependency injection.** `deps_type=Deps` + `RunContext[Deps]` threads a typed
dependency object into every tool with full static typing — the design this arena already praises,
here executed. Verified: a tool read `ctx.deps.tenant_id` / `ctx.deps.api_budget` off a typed
`Deps`.

**3. Model-agnostic by construction.** The *same* agent + tools run against different model backends
with zero code change (swapped via `agent.override(model=...)`). In production that is how you move
a workload between providers or drop to a cheaper model under pressure. Verified: one agent ran
unchanged against `TestModel` and `FunctionModel`.

**Honest overlap and difference with aisotropy.** aisotropy *also* does fail-closed structured
output (the Gollem reflection loop) and typed tools — that part of the ledger overlaps. The
difference is category: Pydantic AI is a Python **type-safe agent library** whose edge is developer
ergonomics around the agent loop; aisotropy is a Go **runtime** that additionally ships the memory
tiers, multi-tenancy, cost governance, and observability by default. The strengths shown here are
exactly the layer where a library shines, they are real, and they are Pydantic AI's to keep — a
different tool for a different job. Both statements are true at once.

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

## Isolation classification

**No isolation layer exists (DIY) — not a leak.** The smoke test
(`.venv/bin/python scripts/smoke.py`, exits 0) demonstrates the framework's classification: Pydantic
AI ships no memory or multi-tenancy layer, so scoping lives entirely in a BYO store keyed by a
developer-computed key. The exercise runs against a 15-line plain-Python dict — the same DIY store
the `get_context` tool reads via `get_recent_messages` — which is **both the boundary and the only
thing that can compute the key wrong**. The Pydantic AI framework is not in that path at all.

The run (1) seeds tenant A's slice, (2) verifies the positive control (tenant A's correct key →
marker present), (3) shows the exact-key store partitions when the key is right (tenant B with its
own key → no marker), then (4) shows that a wrong developer-computed key reads the wrong slice —
because the framework has **no tenant/product concept to reject it**. That is plain-dict behavior,
true of any KV store including aisotropy's BadgerDB; it is not the failure of a Pydantic AI
boundary, because Pydantic AI ships none. A separate agent construction + `run_sync` confirms the
installed `Agent(model=, output_type=)` + `RunContext` DI wiring boots — an independent "the
library works" check, unrelated to the store-scoping question.

**Honest headline:** this is an out-of-scope / DIY classification (isolation is 100%
developer-managed), **not** a defect in Pydantic AI. The classification the feature matrix already
states — memory = DIY/NOT-POSSIBLE, isolation = no layer — is correct. aisotropy's differentiator
is that it binds all four axes at the request boundary and propagates them via `context.Context`,
so the **default** path is scoped.
