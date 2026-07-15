#!/usr/bin/env python3
"""
Cost + pre-spend gate + observability demo for Letta (MemGPT) — REAL SDK types.

Run:
    .venv/bin/python scripts/cost_obs_demo.py
    # or, from repo root:
    arenas/letta/.venv/bin/python arenas/letta/scripts/cost_obs_demo.py

What this PROVES, by executing, and the honest verdict for each of the three
axes the arena cares about:

  1. PER-TENANT COST TRACKING  — does the framework RETURN usage per call?
  2. PRE-SPEND BUDGET GATE      — does it reject a turn BEFORE the model runs?
  3. OBSERVABILITY              — native tracing/OTel, and does tenant_id ride
                                  on the spans?

HONESTY CONTRACT (read this):
  * Letta agents + their memory + their token accounting all live on a SERVER
    (`letta server` / Docker / Letta Cloud). This environment has NO server and
    we do NOT fake one. Where a real server would return a number, we build the
    REAL Letta SDK type (`letta_client...letta_response.Usage`, `ProviderTrace`)
    and fill it with a **synthetic** token count (`len(text)//4`) that is clearly
    labelled. The point is whether the FRAMEWORK gives you the lever, not the
    exact token arithmetic.
  * Anything Letta genuinely does NOT provide (a pre-spend gate; per-tenant
    aggregation; client-side span emission) is implemented here as an explicit
    DIY block and labelled DIY — we never dress DIY up as a built-in.

Verdict tags printed per axis: [BUILT-IN] / [DIY] / [PAID] / [NOT-POSSIBLE].
"""

from __future__ import annotations

import os
import sys

# Real Letta SDK types — fail loudly if the dependency is missing.
try:
    import letta_client  # noqa: F401
    from letta_client.types.agents.letta_response import Usage as LettaUsage
    from letta_client.types import ProviderTrace
except ImportError:
    print("ERROR: letta-client not installed. Run: pip install -r requirements.txt",
          file=sys.stderr)
    sys.exit(1)

# Real OpenTelemetry SDK — the same standard Letta's server exports with.
# Used offline to capture spans in an in-memory exporter (Apache-2.0).
try:
    from opentelemetry import trace as otel_trace
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import (
        InMemorySpanExporter,
    )
    _OTEL = True
except ImportError:
    _OTEL = False

# Make `orbit` importable when run as `python scripts/cost_obs_demo.py`.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from orbit.config import load_config  # noqa: E402
from orbit.memory import TenantScope  # noqa: E402


# ─────────────────────────────────────────────────────────────────────────────
# The fake model. A stand-in for the Letta SERVER's LLM step. It has a CALL
# COUNTER so we can prove, in axis #2, that a budget-rejected turn never reaches
# it. Token counts are SYNTHETIC (len//4) and labelled as such — no server here.
# ─────────────────────────────────────────────────────────────────────────────
class FakeLettaModel:
    """Deterministic offline substitute for a Letta server agent step.

    `run()` returns the REAL Letta `Usage` type the server puts on
    `LettaResponse.usage` — populated with synthetic token counts. The call
    counter is the honesty instrument for the pre-spend gate proof.
    """

    def __init__(self) -> None:
        self.calls = 0

    @staticmethod
    def _synthetic_tokens(text: str) -> int:
        # Clearly-synthetic estimator. Real accounting is server-side.
        return max(1, len(text) // 4)

    def run(self, prompt: str, reply: str) -> LettaUsage:
        self.calls += 1
        prompt_tokens = self._synthetic_tokens(prompt)
        completion_tokens = self._synthetic_tokens(reply)
        # This is the GENUINE Letta SDK type returned on every messages.create.
        return LettaUsage(
            message_type="usage_statistics",
            prompt_tokens=prompt_tokens,
            completion_tokens=completion_tokens,
            total_tokens=prompt_tokens + completion_tokens,
            step_count=1,
            context_tokens=prompt_tokens,
        )


# Synthetic price book (USD per 1K tokens). DIY — Letta's Usage type carries
# tokens but NO dollar field; cost is always the developer's own multiplication.
PRICE_PER_1K = {"prompt": 0.0005, "completion": 0.0015}


def usd(u: LettaUsage) -> float:
    p = (u.prompt_tokens or 0) / 1000 * PRICE_PER_1K["prompt"]
    c = (u.completion_tokens or 0) / 1000 * PRICE_PER_1K["completion"]
    return round(p + c, 6)


def hr(title: str) -> None:
    print()
    print("=" * 72)
    print(title)
    print("=" * 72)


# ─────────────────────────────────────────────────────────────────────────────
# AXIS 1 — PER-TENANT COST TRACKING
# ─────────────────────────────────────────────────────────────────────────────
def axis1_cost_tracking(model: FakeLettaModel) -> dict[str, dict]:
    hr("AXIS 1 — PER-TENANT COST TRACKING (2 turns tenantA, 2 turns tenantB)")

    scope_a = TenantScope(tenant_id="acme", user_id="alice")
    scope_b = TenantScope(tenant_id="globex", user_id="bob")

    turns = [
        (scope_a, "Where's my last invoice?", "Your last invoice is #A-1001, paid."),
        (scope_a, "What plan am I on?", "You are on the Team plan, 5 seats."),
        (scope_b, "Reset my API key please.", "Rotated. New key emailed to bob."),
        (scope_b, "Any open tickets?", "One open ticket: latency on EU region."),
    ]

    # DIY per-tenant aggregation ledger. Letta returns usage PER CALL (built-in),
    # but gives you NO per-tenant meter — this dict is the developer's own.
    ledger: dict[str, dict] = {}

    for scope, question, reply in turns:
        usage = model.run(question, reply)  # server returns real Usage type
        key = scope.tenant_id
        row = ledger.setdefault(key, {"turns": 0, "prompt": 0, "completion": 0,
                                      "total": 0, "usd": 0.0})
        row["turns"] += 1
        row["prompt"] += usage.prompt_tokens or 0
        row["completion"] += usage.completion_tokens or 0
        row["total"] += usage.total_tokens or 0
        row["usd"] = round(row["usd"] + usd(usage), 6)
        print(f"  turn[{key}/{scope.user_id}] usage="
              f"(prompt={usage.prompt_tokens}, completion={usage.completion_tokens}, "
              f"total={usage.total_tokens}, step_count={usage.step_count})  "
              f"cost=${usd(usage)}  [Usage = real letta SDK type]")

    print()
    print("  Per-tenant totals (DIY aggregation over the built-in per-call Usage):")
    for tenant, row in ledger.items():
        print(f"    {tenant:7s}  turns={row['turns']}  tokens={row['total']:5d}  "
              f"(prompt={row['prompt']}, completion={row['completion']})  "
              f"cost=${row['usd']}")

    print()
    print("  [BUILT-IN] Per-call usage VISIBILITY: every agents.messages.create")
    print("             returns LettaResponse.usage (the real `Usage` type above:")
    print("             prompt/completion/total tokens, step_count, cache tokens).")
    print("             Also retrievable per run via client.runs.usage.retrieve(run_id).")
    print("  [DIY]      Per-TENANT aggregation + USD cost: no per-tenant meter and")
    print("             no dollar field on Usage — the ledger + price book above are")
    print("             the developer's own. (Server-side, tenant would be an")
    print("             `agent_tags`/identity filter you also pass yourself.)")
    return ledger


# ─────────────────────────────────────────────────────────────────────────────
# AXIS 2 — PRE-SPEND BUDGET GATE
# ─────────────────────────────────────────────────────────────────────────────
def axis2_pre_spend_gate(model: FakeLettaModel, ledger: dict[str, dict]) -> None:
    hr("AXIS 2 — PRE-SPEND BUDGET GATE (reject BEFORE the model runs)")

    # Per-tenant token budgets (realistic: different tenants, different plans).
    # acme is on a tiny budget it has already blown; globex has headroom — so we
    # exercise BOTH the rejected and the allowed pre-spend path deterministically.
    BUDGETS = {"acme": 20, "globex": 100}
    print(f"  Per-tenant budgets (tokens): {BUDGETS}")
    print(f"  Spent so far (from axis 1): "
          f"acme={ledger['acme']['total']}, globex={ledger['globex']['total']}")
    print()

    def diy_pre_spend_check(tenant: str) -> tuple[bool, str]:
        """DIY gate. Letta has NO equivalent of Pydantic AI's
        UsageLimits.check_before_request — its Usage type only exists AFTER the
        call, and `max_steps` caps the agent LOOP server-side, it is not a
        token/cost budget. So this pre-call check is entirely the developer's."""
        spent = ledger.get(tenant, {}).get("total", 0)
        budget = BUDGETS[tenant]
        if spent >= budget:
            return False, f"tenant '{tenant}' over budget ({spent} >= {budget})"
        return True, f"tenant '{tenant}' under budget ({spent} < {budget})"

    calls_before = model.calls

    # Rejected turn: acme is over budget -> must NOT call the model.
    print("  Turn R1  tenant=acme  (expected: REJECTED before model)")
    allowed, why = diy_pre_spend_check("acme")
    if allowed:
        model.run("another acme question", "another acme answer")
        print("    ALLOWED -> model ran")
    else:
        print(f"    REJECTED (pre-spend): {why}")
        print("    -> model.run() SKIPPED; no tokens spent")

    calls_after_reject = model.calls

    # Allowed turn: globex is under budget -> model runs.
    print("  Turn R2  tenant=globex (expected: ALLOWED, model runs)")
    allowed, why = diy_pre_spend_check("globex")
    if allowed:
        usage = model.run("globex follow-up", "globex follow-up answer")
        print(f"    ALLOWED (pre-spend): {why}")
        print(f"    -> model ran; usage.total_tokens={usage.total_tokens}")
    else:
        print(f"    REJECTED: {why}")

    calls_after_allow = model.calls

    # PROOF via the call counter.
    print()
    print(f"  Call-counter proof: before={calls_before}  "
          f"after_rejected_turn={calls_after_reject}  "
          f"after_allowed_turn={calls_after_allow}")
    rejected_did_not_call = (calls_after_reject == calls_before)
    allowed_did_call = (calls_after_allow == calls_after_reject + 1)
    print(f"    rejected turn did NOT reach the model: {rejected_did_not_call}")
    print(f"    allowed  turn DID  reach the model:    {allowed_did_call}")
    assert rejected_did_not_call, "BUG: rejected turn still called the model"
    assert allowed_did_call, "BUG: allowed turn did not call the model"

    print()
    print("  [DIY]      Pre-spend gate is DEVELOPER-BUILT. Letta ships NO")
    print("             check-before-request: `Usage` is a post-call object, and")
    print("             `max_steps` is a server-side loop cap, not a token/$ budget.")
    print("             Contrast Pydantic AI's UsageLimits.check_before_request,")
    print("             and aisotropy's token-bucket quota gate that rejects (429)")
    print("             BEFORE the LLM call by construction.")


# ─────────────────────────────────────────────────────────────────────────────
# AXIS 3 — OBSERVABILITY
# ─────────────────────────────────────────────────────────────────────────────
def axis3_observability(model: FakeLettaModel) -> None:
    hr("AXIS 3 — OBSERVABILITY (native tracing/OTel; is tenant_id on the span?)")

    scope = TenantScope(tenant_id="acme", user_id="alice")

    # -- 3a. Letta's NATIVE trace object (real SDK type), as the SERVER emits it.
    # We construct the genuine `ProviderTrace` the Letta server records per LLM
    # call. It proves what Letta natively captures AND that tenant identity rides
    # on `agent_tags` (+ user_id/org_id/billing_context). This object is REAL;
    # only the field values are synthetic (no server to emit it here).
    native = ProviderTrace(
        request_json={"model": "openai/gpt-4o-mini", "messages": ["<redacted>"]},
        response_json={"choices": ["<redacted>"]},
        agent_id="agent-acme-alice",
        agent_tags=scope.tags,          # <-- tenant identity lives HERE (dev-passed)
        user_id=scope.user_id,
        org_id=scope.tenant_id,
        call_type="agent_step",
        latency_ms=812,
        run_id="run-synthetic-0001",
        source="memgpt-server",
    )
    print("  3a. Letta NATIVE trace (real ProviderTrace SDK type; server-emitted):")
    print(f"      call_type = {native.call_type}")
    print(f"      agent_tags = {native.agent_tags}   <-- tenant_id/user_id ride here")
    print(f"      user_id={native.user_id}  org_id={native.org_id}  "
          f"latency_ms={native.latency_ms}")
    print(f"      billing_context = {native.billing_context}  "
          "(populated on Cloud/quota plans)")
    print("      Retrieval API: client.runs.trace.retrieve(run_id) -> OTEL spans,")
    print("      but its docstring states: 'Requires ClickHouse to be configured")
    print("      for trace storage.' i.e. emission happens SERVER-side.")

    # -- 3b. What we CAN do offline: real OpenTelemetry in-memory exporter (the
    # same OTel standard Letta's server uses), capturing a client-side span with
    # tenant_id as an attribute. This is DIY client instrumentation around the
    # call — NOT Letta's built-in server tracing.
    print()
    if not _OTEL:
        print("  3b. [NOT-POSSIBLE offline] opentelemetry-sdk not installed; cannot")
        print("      capture spans here. Install: pip install opentelemetry-sdk")
    else:
        exporter = InMemorySpanExporter()
        provider = TracerProvider()
        provider.add_span_processor(SimpleSpanProcessor(exporter))
        tracer = provider.get_tracer("orbit.letta.demo")

        with tracer.start_as_current_span("letta.agent.step") as span:
            span.set_attribute("tenant_id", scope.tenant_id)      # DIY attribute
            span.set_attribute("user_id", scope.user_id)
            span.set_attribute("letta.call_type", "agent_step")
            usage = model.run("traced question", "traced answer")
            span.set_attribute("gen_ai.usage.input_tokens", usage.prompt_tokens or 0)
            span.set_attribute("gen_ai.usage.output_tokens", usage.completion_tokens or 0)
            span.set_attribute("gen_ai.usage.total_tokens", usage.total_tokens or 0)

        provider.force_flush()
        spans = exporter.get_finished_spans()
        print(f"  3b. DIY client-side OTel: captured {len(spans)} span(s) in an")
        print("      InMemorySpanExporter (real opentelemetry-sdk):")
        for s in spans:
            print(f"      span name = {s.name!r}")
            attrs = dict(s.attributes or {})
            for k in sorted(attrs):
                print(f"        {k} = {attrs[k]}")
            print(f"        tenant_id present on span: {'tenant_id' in attrs}")

    print()
    print("  [BUILT-IN] Native tracing EXISTS: the Letta server bundles OTel")
    print("             exporters and records a ProviderTrace per LLM call carrying")
    print("             agent_tags/user_id/org_id/billing_context; retrieved via")
    print("             client.runs.trace.retrieve(run_id).")
    print("  [NOT-POSSIBLE offline] That emission is SERVER-side and needs")
    print("             ClickHouse configured — the offline client cannot emit it.")
    print("  [DIY]      To trace tenant_id from the client TODAY (as in 3b) you add")
    print("             OTel spans yourself; tenant_id is a span attribute you set,")
    print("             not a framework-provided dimension.")
    print("  [PAID]     Turn-key hosted dashboards/per-tenant cost views are the")
    print("             Letta Cloud product surface, not the OSS client.")


def main() -> int:
    print("#" * 72)
    print("# Letta (MemGPT) — cost / pre-spend gate / observability demo")
    print("# Offline. Real letta-client 1.12.1 types. Synthetic token counts.")
    print("#" * 72)

    cfg = load_config()
    if cfg.is_live:
        print(f"\n[note] LETTA_BASE_URL is set ({cfg.base_url}); this demo still uses")
        print("       the offline path deterministically (no agent run issued here).")
    else:
        print("\n[note] No LETTA_BASE_URL — offline run. Token counts are synthetic")
        print("       (len//4); the SDK *types* returned are the genuine article.")

    model = FakeLettaModel()
    ledger = axis1_cost_tracking(model)
    axis2_pre_spend_gate(model, ledger)
    axis3_observability(model)

    hr("SUMMARY — Letta cost & observability reality (run-verified)")
    print("  1. Per-tenant cost:  per-call usage [BUILT-IN] · aggregation+$ [DIY]")
    print("  2. Pre-spend gate:   [DIY] — no check-before-request in the framework")
    print("  3. Observability:    native OTel [BUILT-IN, SERVER-side] ·")
    print("                       offline client emission [NOT-POSSIBLE] ·")
    print("                       tenant_id on spans [DIY] · hosted views [PAID]")
    print()
    print("Result: demo ran end-to-end (exit 0).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
