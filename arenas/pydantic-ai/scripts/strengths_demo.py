#!/usr/bin/env python3
"""
Steelman demo for Pydantic AI v2 — what the framework does GENUINELY well.

This is the two-sided counterpart to the isolation-model smoke: the honest
finding is that Pydantic AI ships no memory/multi-tenancy layer (DIY), but where
it IS excellent — type-safety and agent-loop DX — it is best-in-class. This file
DEMONSTRATES three real strengths with runnable v2 code, fully offline against the
pinned pydantic-ai>=2.0.0 (installed: 2.10.0). No API key, no network.

Every strength below EXECUTES the framework (Agent + FunctionModel/TestModel) and
asserts on the real result — nothing is claimed unverified.

  STRENGTH 1 — Structured output is a TYPED CONTRACT, fail-closed + self-correcting.
      `output_type=<PydanticModel>` makes the model's answer a validated Python
      object, not a string you parse. A union output type routes to the right
      typed branch. And validation failure is fail-closed: a bad field triggers
      the framework's reflection loop (the exact validation error is fed back as
      a retry prompt) until it validates or `retries` is exhausted (then it raises
      `UnexpectedModelBehavior` — never silent bad text). v2 symbols:
      `Agent(output_type=...)`, `pydantic.Field` validation, `Agent(retries=...)`.

  STRENGTH 2 — First-class TYPED dependency injection.
      `deps_type=Deps` + `RunContext[Deps]` threads a typed dependency object into
      every tool with full static typing and IDE autocomplete. This is the design
      the arena already praises — here it is executed. v2 symbols: `deps_type=`,
      `RunContext[Deps]`, `@agent.tool`.

  STRENGTH 3 — MODEL-AGNOSTIC by construction.
      The SAME agent + tools run against different model backends with zero code
      change — swapped here via `agent.override(model=...)`. In production this is
      how you move a workload between providers (OpenAI ↔ Anthropic ↔ Google) or
      drop to a cheap model under pressure. v2 symbols: `agent.override(model=...)`,
      `TestModel`, `FunctionModel`.

Honest overlap with aisotropy: aisotropy also does fail-closed structured output
(the Gollem reflection loop) and typed tools — but aisotropy is a Go RUNTIME that
wires memory tiers, multi-tenancy, cost, and observability by default, whereas
Pydantic AI is a Python type-safe agent LIBRARY. The strengths shown here are
exactly the layer where a library shines: developer ergonomics and type-safety
around the agent loop. They are genuine, and they are Pydantic AI's to keep.
"""

import sys
from dataclasses import dataclass
from typing import Union

from pydantic import BaseModel, Field

try:
    from pydantic_ai import Agent, RunContext, UnexpectedModelBehavior
    from pydantic_ai.models.function import AgentInfo, FunctionModel
    from pydantic_ai.models.test import TestModel
    from pydantic_ai.messages import ModelResponse, TextPart, ToolCallPart
except ImportError:
    print("ERROR: pydantic-ai not installed. Run: pip install pydantic-ai", file=sys.stderr)
    sys.exit(1)


# ── STRENGTH 1: structured output as a typed contract (+ fail-closed retry) ──

class Weather(BaseModel):
    city: str
    temp_c: float


class Refund(BaseModel):
    order_id: str


class Escalate(BaseModel):
    reason: str


class Sku(BaseModel):
    # A real constraint: the model output MUST match this pattern to validate.
    sku: str = Field(pattern=r"^[A-Z]{3}-\d{4}$")


def strength_structured_output() -> tuple[bool, list[str]]:
    out = []

    # (a) Typed object out, not a string. FunctionModel calls the output tool the
    #     framework auto-generated ("final_result") with structured args.
    def emit_weather(messages, info: AgentInfo) -> ModelResponse:
        name = info.output_tools[0].name  # framework's synthesized output tool
        return ModelResponse(parts=[ToolCallPart(tool_name=name, args={"city": "Paris", "temp_c": 21.5})])

    wa = Agent(model=FunctionModel(emit_weather), output_type=Weather)
    w = wa.run_sync("weather in paris").output
    typed_ok = isinstance(w, Weather) and w.city == "Paris" and w.temp_c == 21.5
    out.append(f"  [{'PASS' if typed_ok else 'FAIL'}] output_type=Weather -> validated object {w!r} (not a string to parse)")

    # (b) Union output routes to the correct typed branch (discriminated by the
    #     output tool the model picks). This is typed routing for free.
    def route_escalate(messages, info: AgentInfo) -> ModelResponse:
        # output_tools carries one tool per union member; pick the Escalate one.
        esc = next(t for t in info.output_tools if "escalate" in t.name.lower())
        return ModelResponse(parts=[ToolCallPart(tool_name=esc.name, args={"reason": "vip customer"})])

    ra = Agent(model=FunctionModel(route_escalate), output_type=Union[Refund, Escalate])
    routed = ra.run_sync("handle this ticket").output
    union_ok = isinstance(routed, Escalate) and routed.reason == "vip customer"
    out.append(f"  [{'PASS' if union_ok else 'FAIL'}] output_type=Union[Refund, Escalate] -> routed to {type(routed).__name__} (typed branch)")

    # (c) Fail-closed + self-correcting: first output violates the pattern; the
    #     framework feeds the validation error back and retries; 2nd is valid.
    calls = {"n": 0}

    def emit_sku(messages, info: AgentInfo) -> ModelResponse:
        calls["n"] += 1
        name = info.output_tools[0].name
        if calls["n"] == 1:
            return ModelResponse(parts=[ToolCallPart(tool_name=name, args={"sku": "not-a-sku"})])  # invalid
        return ModelResponse(parts=[ToolCallPart(tool_name=name, args={"sku": "ABC-1234"})])       # valid

    sa = Agent(model=FunctionModel(emit_sku), output_type=Sku, retries=3)
    sku = sa.run_sync("give me a sku").output
    retry_ok = isinstance(sku, Sku) and sku.sku == "ABC-1234" and calls["n"] == 2
    out.append(f"  [{'PASS' if retry_ok else 'FAIL'}] reflection loop: invalid output retried with the error -> valid {sku.sku!r} on generation #{calls['n']}")

    # (d) Fail-closed proof: if it NEVER validates, the framework raises rather
    #     than returning silent bad text.
    def always_bad(messages, info: AgentInfo) -> ModelResponse:
        name = info.output_tools[0].name
        return ModelResponse(parts=[ToolCallPart(tool_name=name, args={"sku": "still-bad"})])

    fa = Agent(model=FunctionModel(always_bad), output_type=Sku, retries=1)
    fail_closed = False
    try:
        fa.run_sync("give me a sku")
    except UnexpectedModelBehavior:
        fail_closed = True
    out.append(f"  [{'PASS' if fail_closed else 'FAIL'}] fail-closed: exhausted retries raised UnexpectedModelBehavior (no silent bad output)")

    return (typed_ok and union_ok and retry_ok and fail_closed), out


# ── STRENGTH 2: first-class typed dependency injection ──────────────────────

@dataclass
class Deps:
    tenant_id: str
    api_budget: int


def strength_dependency_injection() -> tuple[bool, list[str]]:
    out = []
    captured = {}

    def noop(messages, info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart("done")])

    agent = Agent(model=FunctionModel(noop), deps_type=Deps)

    @agent.tool
    async def read_context(ctx: RunContext[Deps]) -> str:
        # ctx.deps is statically typed as Deps — IDE autocomplete, mypy-checked.
        captured["tenant"] = ctx.deps.tenant_id
        captured["budget"] = ctx.deps.api_budget
        return f"ctx for {ctx.deps.tenant_id}"

    # Force the tool to run by using TestModel (calls every tool) via override.
    with agent.override(model=TestModel()):
        agent.run_sync("hi", deps=Deps(tenant_id="acme", api_budget=1000))

    di_ok = captured.get("tenant") == "acme" and captured.get("budget") == 1000
    out.append(f"  [{'PASS' if di_ok else 'FAIL'}] deps_type=Deps + RunContext[Deps] -> tool read typed deps {captured!r}")
    return di_ok, out


# ── STRENGTH 3: model-agnostic by construction ──────────────────────────────

def strength_model_agnostic() -> tuple[bool, list[str]]:
    out = []

    # Define the agent + tool ONCE.
    agent = Agent(deps_type=int)

    @agent.tool
    async def double(ctx: RunContext[int]) -> int:
        return ctx.deps * 2

    # Same agent, same tool — two different model backends, zero code change.
    def fn_backend(messages, info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart("answer-from-function-model")])

    with agent.override(model=TestModel(custom_output_text="answer-from-test-model")):
        r_test = agent.run_sync("x", deps=3).output
    with agent.override(model=FunctionModel(fn_backend)):
        r_fn = agent.run_sync("x", deps=3).output

    swap_ok = r_test == "answer-from-test-model" and r_fn == "answer-from-function-model"
    out.append(f"  [{'PASS' if swap_ok else 'FAIL'}] agent.override(model=...) swapped backend with no code change: TestModel={r_test!r}, FunctionModel={r_fn!r}")
    return swap_ok, out


# ── Main ────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import pydantic_ai

    print("=" * 68)
    print(f"Pydantic AI — Steelman: genuine strengths (v2, pydantic-ai {pydantic_ai.__version__})")
    print("=" * 68)
    print()

    results = []

    print("STRENGTH 1 — Structured output as a typed contract (fail-closed + self-correcting):")
    ok1, lines = strength_structured_output()
    for ln in lines:
        print(ln)
    results.append(ok1)
    print()

    print("STRENGTH 2 — First-class typed dependency injection:")
    ok2, lines = strength_dependency_injection()
    for ln in lines:
        print(ln)
    results.append(ok2)
    print()

    print("STRENGTH 3 — Model-agnostic by construction:")
    ok3, lines = strength_model_agnostic()
    for ln in lines:
        print(ln)
    results.append(ok3)
    print()

    print("-" * 68)
    if all(results):
        print("Result: PASS — all three strengths executed against pydantic-ai "
              f"{pydantic_ai.__version__} and held.")
        print("  These are Pydantic AI's real edge: type-safety + agent-loop DX. aisotropy")
        print("  overlaps on fail-closed structured output and typed tools, but is a Go runtime")
        print("  that ships memory/tenancy/cost/observability by default — a different tool for")
        print("  a different job. Both statements are true at once.")
        sys.exit(0)
    else:
        print(f"Result: FAIL — {sum(results)}/3 strengths held (see above).")
        sys.exit(1)
