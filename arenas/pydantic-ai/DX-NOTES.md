---
type: Developer notes
title: "Pydantic AI — DX notes"
description: Honest developer-experience assessment of Pydantic AI v2 from the perspective of building a multi-tenant production agent.
tags: [arenas, pydantic-ai, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# Pydantic AI — DX Notes

## The good

### `TestModel` — offline killer
The single best testing primitive of the three frameworks. Spin up a fake LLM, assert on calls,
zero API key needed:

```python
from pydantic_ai import Agent
from pydantic_ai.models.test import TestModel

agent = Agent('openai:gpt-4o', model=TestModel())
result = agent.run_sync('hello')
assert result.output == 'success (no tool calls)'  # v2 API: result.output (not result.data)
```

This makes prototyping and unit testing frictionless. aisotropy's equivalent is the credential-less
stub LLM (`llm.StubClient`) — both work, but `TestModel` is more expressive (you can control
per-call behavior).

### `deps_type` / `RunContext` — DI of first class
Dependency injection is woven into the agent model, not bolted on:

```python
class Deps(pydantic.BaseModel):
    tenant_id: str
    db: Database

agent = Agent('openai:gpt-4o', deps_type=Deps)

@agent.tool
async def get_messages(ctx: RunContext[Deps], session_id: str) -> list[Message]:
    return await ctx.deps.db.query(...)
```

This is clean, typed, and discoverable. aisotropy achieves the same through the builder pattern
(`WithTypedTool[T]`, `WithToolExecutor`) — equally typed but more verbose.

### Structured output — smoothest of the three
Define a Pydantic model, get JSON schema, validation, and retry — all automatic:

```python
from pydantic import BaseModel

class WeatherResponse(BaseModel):
    city: str
    temp: float

agent = Agent('openai:gpt-4o', output_type=WeatherResponse)  # v2 API: output_type (not response_type)
```

The fail-closed retry (`raise ModelRetry`) is elegant. aisotropy's Gollem reflection loop
achieves the same but through a more opaque mechanism (homegrown fallback vs. Pydantic's
first-party integration).

### Small codebase — 838 lines
You can read the entire framework in an afternoon. This is both a strength (understandable) and
a weakness (less feature coverage). aisotropy's sealed engine is orders of magnitude larger
because it ships everything built-in.

## The painful

### Multi-tenant docs are thin
Exactly where you'd need guidance — tenant scoping, isolation patterns, cross-tenant attack
mitigation — the docs are quiet. You're on your own. The `orbit-support-agent` reference demo
confirms: "no database/session handling, memory persistence, or multi-tenancy mechanisms."

aisotropy leads here: isolation is **documented as a constitutional gate**, tested via black-box
E2E (`isolation_e2e_test.go`), and enforced by value objects (`tenancy.Scope`) with unexported
fields.

### Memory = DIY across the board
Working memory, semantic search, facts journal, vector storage — none of this is built in.
You wire Redis, Chroma, SQLite, whatever. aisotropy ships all four tiers (working + facts +
semantic + sleep-cycle) with the first three running by default.

### `UsageLimits` is per-run, not per-tenant
You can cap a single agent run's token budget, but there's no tenant-level aggregation or
pre-spend blocking. aisotropy's cost meter tracks per-tenant spend and gates before the LLM
call — a meaningful difference for multi-tenant billing.

### No native observability
BYO OpenTelemetry. No built-in `/costs`, `/health`, or circuit-breaker metrics. aisotropy
ships OTel gen_ai semconv compliant tracing by default.

## The verdict

Pydantic AI v2 is the **most pleasant DX for the agent loop itself** — structured output, tool
calling, streaming, and testing are all top-notch. But it's fundamentally an orchestration
library, not a runtime. Every infrastructure concern is DIY.

**Best for:** Teams that want the best typed DX for building individual agents and are happy to
wire up memory, cost, observability, and multi-tenancy themselves.

**Worst for:** Teams building a long-lived multi-tenant SaaS where isolation, cost governance,
and observability are non-negotiable — you'll spend more time wiring infrastructure than building
agent logic.

**vs. aisotropy:** Pydantic AI wins on agent-loop DX; aisotropy wins on everything else. The
trade is: best-typed framework + DIY infrastructure vs. build-it-all-yourself Go runtime with
everything wired in.
