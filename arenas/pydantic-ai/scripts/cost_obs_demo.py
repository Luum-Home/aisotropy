#!/usr/bin/env python3
"""
Cost + pre-spend gate + observability demo for Pydantic AI v2 (RUNS OFFLINE).

Run:
    cd arenas/pydantic-ai
    ./.venv/bin/python scripts/cost_obs_demo.py

Everything runs against a deterministic offline fake model — no network, no API
key. Token numbers come from Pydantic AI's own usage accounting (TestModel's
`_estimate_usage`, message-length based); the per-token *price* is a synthetic
constant we define here (clearly labeled). The point is not exact dollars — it is
whether the FRAMEWORK hands you the primitives, or you build them yourself.

What this PROVES by executing (verdict printed per concern):

  1. PER-TENANT COST TRACKING
     - Pydantic AI RETURNS usage per call via `result.usage` (RunUsage:
       input_tokens/output_tokens/total_tokens). That visibility is [BUILT-IN].
     - Aggregating those numbers into a per-tenant ledger is [DIY] — the
       framework has no tenant concept.

  2. PRE-SPEND BUDGET GATE
     - Pydantic AI ships a REAL pre-spend gate: `UsageLimits` with
       `count_tokens_before_request=True` calls the model's cheap
       `count_tokens()` and then `check_before_request()` raises
       `UsageLimitExceeded` BEFORE `model.request()` (the generation / spend).
       We prove the generation call-counter stays at 0 on the rejected turn.
       This per-RUN gate is [BUILT-IN].
     - The gate is per-RUN, not per-tenant. Turning a per-tenant budget into a
       `UsageLimits` for each turn (subtracting what the tenant already spent)
       is [DIY] — we implement and label it.

  3. OBSERVABILITY
     - `logfire.instrument_pydantic_ai()` with `send_to_logfire=False` emits
       standard OTel gen_ai spans (`invoke_agent`, `chat`) carrying model name,
       token usage, messages, conversation id. Captured here with an in-memory
       OTel exporter. Native OTel export is [BUILT-IN] (OSS). Shipping those
       spans to the Logfire SaaS dashboard is the [PAID] option.
     - `tenant_id` is NOT on the framework's spans. It appears only on the
       wrapping span WE open. Putting tenant_id on traces is [DIY].

Exit 0 = all three demonstrations ran and their assertions held.
"""

import asyncio
import sys

try:
    import logfire
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import (
        InMemorySpanExporter,
    )
    from pydantic_ai import Agent
    from pydantic_ai.exceptions import UsageLimitExceeded
    from pydantic_ai.messages import ModelMessage
    from pydantic_ai.models.test import TestModel
    from pydantic_ai.usage import RequestUsage, UsageLimits
except ImportError as exc:  # pragma: no cover - defensive
    print(f"ERROR: missing dependency ({exc}). "
          "Run: ./.venv/bin/pip install -r requirements.txt logfire", file=sys.stderr)
    sys.exit(1)


# ── Synthetic pricing (CLEARLY FAKE — offline demo) ─────────────────────────
# A made-up per-token price so we can print a "cost". Not a real rate card.
PRICE_PER_INPUT_TOKEN = 0.0000030   # $3 / 1M input tokens (synthetic)
PRICE_PER_OUTPUT_TOKEN = 0.0000150  # $15 / 1M output tokens (synthetic)


def cost_usd(input_tokens: int, output_tokens: int) -> float:
    return input_tokens * PRICE_PER_INPUT_TOKEN + output_tokens * PRICE_PER_OUTPUT_TOKEN


# ── Offline fake model with a generation call-counter ───────────────────────
# We subclass TestModel (a real, offline Pydantic AI model) so we inherit its
# valid ModelResponse + framework-computed usage. We add:
#   - request_calls : incremented on each GENERATION call (the actual spend)
#   - count_calls   : incremented on each cheap pre-count call (no spend)
# TestModel does NOT implement count_tokens(); we add it so the framework's
# `count_tokens_before_request` pre-spend path is exercised offline.

def _messages_text(messages: list[ModelMessage]) -> str:
    parts: list[str] = []
    for m in messages:
        for p in getattr(m, "parts", []):
            content = getattr(p, "content", None)
            if isinstance(content, str):
                parts.append(content)
    return " ".join(parts)


class CountingFakeModel(TestModel):
    """Deterministic offline model that counts generation vs pre-count calls."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.request_calls = 0   # GENERATION calls = real spend
        self.count_calls = 0     # cheap pre-spend token counts = no spend

    async def request(self, messages, model_settings, model_request_parameters):
        # This is the SPEND. If the pre-spend gate works, it must NOT run on a
        # rejected turn.
        self.request_calls += 1
        return await super().request(messages, model_settings, model_request_parameters)

    async def count_tokens(self, messages, model_settings, model_request_parameters):
        # Cheap, no generation. Synthetic token estimate: len(text)//4.
        self.count_calls += 1
        text = _messages_text(messages)
        synthetic = max(1, len(text) // 4)
        return RequestUsage(input_tokens=synthetic)


# ── Per-tenant ledger (DIY — framework has no tenant concept) ───────────────

class TenantLedger:
    """DIY per-tenant token/cost aggregation. Pydantic AI gives per-call usage;
    the summing-by-tenant is entirely our code."""

    def __init__(self):
        self._input: dict[str, int] = {}
        self._output: dict[str, int] = {}

    def record(self, tenant_id: str, input_tokens: int, output_tokens: int):
        self._input[tenant_id] = self._input.get(tenant_id, 0) + input_tokens
        self._output[tenant_id] = self._output.get(tenant_id, 0) + output_tokens

    def totals(self, tenant_id: str) -> tuple[int, int]:
        return self._input.get(tenant_id, 0), self._output.get(tenant_id, 0)

    def total_tokens(self, tenant_id: str) -> int:
        i, o = self.totals(tenant_id)
        return i + o


def hr(title: str):
    print()
    print("=" * 74)
    print(title)
    print("=" * 74)


# ── DEMO 1: per-tenant cost tracking ────────────────────────────────────────

async def demo_cost_tracking() -> bool:
    hr("DEMO 1 — PER-TENANT COST TRACKING")
    print("Running 2 turns for tenantA and 2 for tenantB against the offline model.")
    print("Reading `result.usage` after each call (framework-returned), summing per tenant (DIY).")
    print()

    ledger = TenantLedger()
    model = CountingFakeModel()
    agent = Agent(model=model)

    turns = [
        ("tenantA", "summarize the incident report for me please"),
        ("tenantA", "now list the action items"),
        ("tenantB", "hi"),
        ("tenantB", "what is the weather like today in a few words"),
    ]

    for tenant, prompt in turns:
        result = await agent.run(prompt)
        usage = result.usage  # <-- FRAMEWORK-RETURNED per-call usage (property in v2.x)
        ledger.record(tenant, usage.input_tokens, usage.output_tokens)
        print(f"  [{tenant}] prompt={prompt[:34]!r:38} "
              f"in={usage.input_tokens:<3} out={usage.output_tokens:<3} "
              f"(result.usage -> {type(usage).__name__})")

    print()
    for tenant in ("tenantA", "tenantB"):
        i, o = ledger.totals(tenant)
        print(f"  TOTAL {tenant}: input={i} output={o} "
              f"total={i + o} synthetic_cost=${cost_usd(i, o):.6f}")

    ok = ledger.total_tokens("tenantA") > 0 and ledger.total_tokens("tenantB") > 0
    print()
    print("  VERDICT per-call usage visibility : [BUILT-IN]  (result.usage returns RunUsage)")
    print("  VERDICT per-tenant aggregation    : [DIY]       (framework has no tenant concept;")
    print("                                                   we summed into TenantLedger)")
    return ok


# ── DEMO 2: pre-spend budget gate ───────────────────────────────────────────

async def demo_prespend_gate() -> bool:
    hr("DEMO 2 — PRE-SPEND BUDGET GATE")
    print("Framework primitive: UsageLimits(count_tokens_before_request=True).")
    print("It calls model.count_tokens() (cheap) then check_before_request() which raises")
    print("UsageLimitExceeded BEFORE model.request() (generation). We prove generation is skipped.")
    print()

    # DIY per-tenant budget: tenantB has already spent almost all of a tiny budget.
    PER_TENANT_TOKEN_BUDGET = 200
    already_spent = {"tenantA": 0, "tenantB": 198}  # tenantB is nearly exhausted

    # ---- Allowed turn (tenantA has budget) ----
    model_a = CountingFakeModel()
    agent_a = Agent(model=model_a)
    remaining_a = PER_TENANT_TOKEN_BUDGET - already_spent["tenantA"]  # DIY: derive per-run limit
    limits_a = UsageLimits(input_tokens_limit=remaining_a, count_tokens_before_request=True)
    print(f"  ALLOWED turn (tenantA): remaining budget={remaining_a} tokens")
    allowed_ok = False
    try:
        result_a = await agent_a.run("please give me a short status update", usage_limits=limits_a)
        allowed_ok = True
        print(f"    -> ran. generation calls={model_a.request_calls} "
              f"pre-count calls={model_a.count_calls} usage={result_a.usage}")
    except UsageLimitExceeded as exc:
        print(f"    -> UNEXPECTEDLY rejected: {exc}")

    print()

    # ---- Rejected turn (tenantB over budget) ----
    model_b = CountingFakeModel()
    agent_b = Agent(model=model_b)
    remaining_b = PER_TENANT_TOKEN_BUDGET - already_spent["tenantB"]  # = 2, tiny
    limits_b = UsageLimits(input_tokens_limit=remaining_b, count_tokens_before_request=True)
    print(f"  REJECTED turn (tenantB): remaining budget={remaining_b} tokens (over budget)")
    rejected_ok = False
    try:
        await agent_b.run("this prompt is longer than the two remaining tokens allow", usage_limits=limits_b)
        print("    -> ERROR: expected rejection but the model ran")
    except UsageLimitExceeded as exc:
        rejected_ok = True
        print(f"    -> REJECTED before spending: {exc}")
        print(f"    -> generation calls (MUST be 0)={model_b.request_calls}  "
              f"pre-count calls={model_b.count_calls}")

    gate_proven = allowed_ok and rejected_ok and model_b.request_calls == 0
    print()
    print("  VERDICT pre-spend gate            : [BUILT-IN]  (UsageLimits.check_before_request +")
    print("                                                   count_tokens_before_request raise")
    print("                                                   UsageLimitExceeded pre-generation)")
    print("  VERDICT per-tenant budget         : [DIY]       (gate is per-RUN; we tracked each")
    print("                                                   tenant's spend and derived the limit)")
    return gate_proven


# ── DEMO 3: observability ───────────────────────────────────────────────────

async def demo_observability() -> bool:
    hr("DEMO 3 — OBSERVABILITY (native OTel via logfire, send_to_logfire=False)")
    print("Configuring logfire offline with an in-memory OTel span exporter, then running one turn.")
    print()

    exporter = InMemorySpanExporter()
    logfire.configure(
        send_to_logfire=False,  # OFFLINE — no data leaves the process
        additional_span_processors=[SimpleSpanProcessor(exporter)],
    )
    logfire.instrument_pydantic_ai()  # framework-native OTel gen_ai instrumentation

    agent = Agent(model=TestModel())

    # We open a wrapping span and put tenant_id on IT (the framework won't).
    with logfire.span("tenant.turn", tenant_id="tenantA"):
        await agent.run("hello")

    spans = exporter.get_finished_spans()
    framework_span_seen = False
    tenant_on_framework_span = False
    tenant_on_wrapper_span = False

    print("  Captured spans:")
    for s in spans:
        attrs = dict(s.attributes or {})
        tenant = attrs.get("tenant_id")
        interesting = {k: attrs[k] for k in (
            "gen_ai.operation.name", "gen_ai.request.model",
            "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens",
            "gen_ai.aggregated_usage.input_tokens", "tenant_id",
        ) if k in attrs}
        print(f"    - {s.name:<18} {interesting}")
        if "gen_ai.operation.name" in attrs:
            framework_span_seen = True
            if tenant is not None:
                tenant_on_framework_span = True
        if s.name == "tenant.turn" and tenant == "tenantA":
            tenant_on_wrapper_span = True

    ok = framework_span_seen and tenant_on_wrapper_span and not tenant_on_framework_span
    print()
    print(f"  framework gen_ai spans emitted        : {framework_span_seen}")
    print(f"  tenant_id on framework spans          : {tenant_on_framework_span}  (framework does not add it)")
    print(f"  tenant_id on OUR wrapping span        : {tenant_on_wrapper_span}  (we set it)")
    print()
    print("  VERDICT native OTel tracing           : [BUILT-IN]  (logfire.instrument_pydantic_ai emits")
    print("                                                       gen_ai spans w/ model+usage+messages)")
    print("  VERDICT hosted trace dashboard        : [PAID]      (Logfire SaaS; also LangSmith etc.)")
    print("  VERDICT tenant_id on spans            : [DIY]       (only on the span we open)")
    return ok


# ── Main ────────────────────────────────────────────────────────────────────

async def main() -> int:
    print("Pydantic AI v2 — cost / pre-spend gate / observability demo (OFFLINE)")
    print(f"pydantic-ai + logfire, TestModel-based fake, synthetic pricing "
          f"(${PRICE_PER_INPUT_TOKEN * 1e6:.0f}/1M in, ${PRICE_PER_OUTPUT_TOKEN * 1e6:.0f}/1M out)")

    r1 = await demo_cost_tracking()
    r2 = await demo_prespend_gate()
    r3 = await demo_observability()

    hr("SUMMARY")
    print(f"  1. per-tenant cost tracking : {'OK' if r1 else 'FAIL'}  "
          "-> usage [BUILT-IN], per-tenant sum [DIY]")
    print(f"  2. pre-spend budget gate    : {'OK' if r2 else 'FAIL'}  "
          "-> gate [BUILT-IN] (fires pre-generation), per-tenant budget [DIY]")
    print(f"  3. observability            : {'OK' if r3 else 'FAIL'}  "
          "-> OTel spans [BUILT-IN], hosted [PAID], tenant_id on spans [DIY]")
    print()

    all_ok = r1 and r2 and r3
    print("RESULT:", "PASS" if all_ok else "FAIL")
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
