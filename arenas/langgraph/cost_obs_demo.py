#!/usr/bin/env python3
"""
Cost + pre-spend gate + observability demo for LangGraph (Python).

Runs 100% OFFLINE with a deterministic FAKE chat model. Token counts are
SYNTHETIC (input = len(text)//4, output = fixed) and clearly labelled — the
point is to test what the FRAMEWORK gives you, not to measure real tokens.

Verified against LangGraph 1.2.9 / langchain-core 1.4.9 / langsmith 0.10.5 /
opentelemetry-sdk 1.43.0 (see arena .venv). Run:

    .venv/bin/python cost_obs_demo.py

It proves, BY EXECUTING, three things and prints a
[BUILT-IN] / [DIY] / [PAID] / [NOT-POSSIBLE] verdict for each:

  1. PER-TENANT COST TRACKING  — is usage returned per call? is per-tenant
                                 aggregation given, or DIY?
  2. PRE-SPEND BUDGET GATE     — can you reject a tenant BEFORE the model runs?
  3. OBSERVABILITY             — native OTel spans; is tenant_id on them?

Honesty rule: where LangGraph does NOT provide something, this demo implements
the DIY version and LABELS it DIY. It never fakes a built-in that isn't there.
"""

# ─────────────────────────────────────────────────────────────────────────────
# OTel + LangSmith-OTel-bridge env MUST be set BEFORE importing langchain, and
# the global TracerProvider MUST be installed first, so LangChain detects it and
# emits into OUR in-memory exporter instead of standing up its own. We point the
# bridge at an unreachable endpoint + OTEL_ONLY so NOTHING leaves the machine and
# no real LangSmith API key is needed (dummy key below). This is the offline,
# no-cloud capture path.
# ─────────────────────────────────────────────────────────────────────────────
import os

os.environ.setdefault("LANGSMITH_OTEL_ENABLED", "true")   # turn on the OTel bridge
os.environ.setdefault("LANGSMITH_TRACING", "true")        # enable tracing callbacks
os.environ.setdefault("LANGSMITH_OTEL_ONLY", "true")      # do NOT ship to LangSmith cloud
os.environ.setdefault("LANGSMITH_API_KEY", "lsv2_dummy_offline_key")  # never used (OTEL_ONLY)
os.environ.setdefault("LANGSMITH_ENDPOINT", "http://localhost:1")     # unreachable on purpose

from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

_SPAN_EXPORTER = InMemorySpanExporter()
_provider = TracerProvider()
_provider.add_span_processor(SimpleSpanProcessor(_SPAN_EXPORTER))
trace.set_tracer_provider(_provider)  # global provider — installed before LangChain import

import time
import warnings

warnings.filterwarnings("ignore")  # silence LangGraph v1 deprecation noise for a clean demo

from langchain_core.language_models.chat_models import BaseChatModel
from langchain_core.messages import AIMessage, HumanMessage
from langchain_core.outputs import ChatGeneration, ChatResult
from langgraph.graph import END, START, MessagesState, StateGraph

SYNTH_PRICE_PER_1K = 0.50  # SYNTHETIC $/1k tokens — arbitrary, for the cost math only


# ─────────────────────────────────────────────────────────────────────────────
# Fake, deterministic, offline chat model.
#  - carries usage_metadata on every AIMessage (this is the BUILT-IN cost signal)
#  - increments a shared call counter so we can PROVE the model did / did not run
# ─────────────────────────────────────────────────────────────────────────────
MODEL_CALLS = {"n": 0}


class CountingFakeModel(BaseChatModel):
    """Offline stub. usage_metadata tokens are SYNTHETIC (len(text)//4)."""

    def _generate(self, messages, stop=None, run_manager=None, **kwargs):
        MODEL_CALLS["n"] += 1
        text = messages[-1].content if messages else ""
        in_tok = max(1, len(str(text)) // 4)   # SYNTHETIC input tokens
        out_tok = 12                            # SYNTHETIC output tokens
        msg = AIMessage(
            content=f"[fake-reply] processed: {str(text)[:40]}",
            usage_metadata={
                "input_tokens": in_tok,
                "output_tokens": out_tok,
                "total_tokens": in_tok + out_tok,
            },
        )
        return ChatResult(generations=[ChatGeneration(message=msg)])

    @property
    def _llm_type(self):
        return "counting-fake"


MODEL = CountingFakeModel()


def build_graph():
    """Minimal LangGraph: START -> chat(model) -> END."""
    def chat_node(state: MessagesState):
        return {"messages": [MODEL.invoke(state["messages"])]}

    g = StateGraph(MessagesState)
    g.add_node("chat", chat_node)
    g.add_edge(START, "chat")
    g.add_edge("chat", END)
    return g.compile()


GRAPH = build_graph()


def rule(title):
    print("\n" + "=" * 74)
    print(title)
    print("=" * 74)


# ─────────────────────────────────────────────────────────────────────────────
# PART 1 — PER-TENANT COST TRACKING
# ─────────────────────────────────────────────────────────────────────────────
def part1_per_tenant_cost():
    rule("PART 1 — PER-TENANT COST TRACKING")
    print(
        "Framework signal: every AIMessage carries `.usage_metadata` "
        "(input/output/total tokens).\n"
        "That per-call visibility is BUILT-IN. Attribution to a TENANT and "
        "summing across a\ntenant's turns is NOT — we do that by hand (DIY).\n"
    )

    turns = {
        "tenantA": ["What is our refund policy?", "And for international orders?"],
        "tenantB": ["Summarize the Q3 report", "Now translate it to Spanish"],
    }

    # DIY per-tenant ledger. The framework does not key usage by tenant for you.
    ledger = {}  # tenant -> {"input":int,"output":int,"total":int,"turns":int,"cost":float}

    for tenant, prompts in turns.items():
        tot = {"input": 0, "output": 0, "total": 0}
        for i, prompt in enumerate(prompts, 1):
            out = GRAPH.invoke(
                {"messages": [HumanMessage(content=prompt)]},
                config={"metadata": {"tenant_id": tenant}, "tags": [f"tenant:{tenant}"]},
            )
            ai = [m for m in out["messages"] if isinstance(m, AIMessage)][-1]
            um = ai.usage_metadata  # <-- BUILT-IN per-call usage, straight off the AIMessage
            print(
                f"  {tenant} turn {i}: usage_metadata={um}  "
                f"(BUILT-IN, read straight off the AIMessage)"
            )
            # DIY: attribute this call's usage to the tenant and accumulate.
            tot["input"] += um["input_tokens"]
            tot["output"] += um["output_tokens"]
            tot["total"] += um["total_tokens"]

        cost = round(tot["total"] / 1000 * SYNTH_PRICE_PER_1K, 6)
        ledger[tenant] = {**tot, "turns": len(prompts), "cost": cost}

    print("\n  PER-TENANT TOTALS (SYNTHETIC tokens; cost @ "
          f"${SYNTH_PRICE_PER_1K}/1k):")
    for tenant, v in ledger.items():
        print(
            f"    {tenant}: turns={v['turns']}  input={v['input']}  "
            f"output={v['output']}  total={v['total']}  cost=${v['cost']}"
        )

    print(
        "\n  VERDICT:\n"
        "    [BUILT-IN] Per-call usage visibility — AIMessage.usage_metadata on every reply\n"
        "               (the totals above are summed from it).\n"
        "    [BUILT-IN] Within-scope aggregation helpers exist — UsageMetadataCallbackHandler\n"
        "               and get_usage_metadata_callback() — but they key usage by MODEL NAME,\n"
        "               with NO notion of tenant.\n"
        "    [DIY]      Per-TENANT attribution & totals — the framework has no tenant concept;\n"
        "               you keep the per-tenant ledger yourself (done above). aisotropy ships a\n"
        "               per-tenant cost meter built in."
    )
    return ledger


# ─────────────────────────────────────────────────────────────────────────────
# PART 2 — PRE-SPEND BUDGET GATE
# ─────────────────────────────────────────────────────────────────────────────
def part2_pre_spend_gate(ledger):
    rule("PART 2 — PRE-SPEND BUDGET GATE (reject BEFORE the model runs)")
    print(
        "Question: does LangGraph provide a pre-spend check (like Pydantic AI's\n"
        "UsageLimits.check_before_request) that blocks a call BEFORE tokens are spent?\n"
        "Answer (verified): NO. What LangChain 1.x actually ships:\n"
        "  - langchain.agents.middleware.ModelCallLimitMiddleware -> limits the NUMBER of\n"
        "    model CALLS per run/thread and terminates the agent; it is per-agent and\n"
        "    reactive (counts calls as they happen), NOT a per-tenant TOKEN budget.\n"
        "  - UsageMetadataCallbackHandler -> aggregates usage AFTER calls (reactive).\n"
        "  - There is NO `UsageLimits` / no pre-spend token gate in langchain/langchain_core.\n"
        "So the pre-spend, per-tenant TOKEN gate is DIY. We implement + label it DIY below.\n"
    )

    # DIY per-tenant token budget. tenantA already spent its turns in Part 1.
    BUDGET = {"tenantA": 25, "tenantB": 10_000}  # tiny budget for tenantA on purpose
    spent = {t: ledger[t]["total"] for t in BUDGET}
    print(f"  DIY budgets (SYNTHETIC tokens): {BUDGET}")
    print(f"  Already spent (from Part 1):    {spent}\n")

    def estimate_tokens(prompt: str) -> int:
        # same SYNTHETIC model as the fake model, computed WITHOUT calling it
        return max(1, len(prompt) // 4) + 12

    def guarded_invoke(tenant: str, prompt: str):
        """DIY pre-spend gate: check budget, then decide to call the model or not."""
        est = estimate_tokens(prompt)
        projected = spent[tenant] + est
        if projected > BUDGET[tenant]:
            # REJECT *before* GRAPH.invoke — the model is never reached.
            return {
                "allowed": False,
                "reason": f"pre-spend gate: {spent[tenant]}+{est}={projected} > "
                          f"budget {BUDGET[tenant]}",
            }
        out = GRAPH.invoke(
            {"messages": [HumanMessage(content=prompt)]},
            config={"metadata": {"tenant_id": tenant}, "tags": [f"tenant:{tenant}"]},
        )
        ai = [m for m in out["messages"] if isinstance(m, AIMessage)][-1]
        spent[tenant] += ai.usage_metadata["total_tokens"]
        return {"allowed": True, "usage": ai.usage_metadata}

    # ---- ALLOWED turn: tenantB is well under budget -> model SHOULD run ----
    before = MODEL_CALLS["n"]
    r_ok = guarded_invoke("tenantB", "Give me a one-line status.")
    after_ok = MODEL_CALLS["n"]
    print(f"  ALLOWED  tenantB: {r_ok}")
    print(f"    model call counter: {before} -> {after_ok}  "
          f"(delta={after_ok - before}, model DID run)\n")

    # ---- REJECTED turn: tenantA is over its tiny budget -> model MUST NOT run ----
    before_rej = MODEL_CALLS["n"]
    r_no = guarded_invoke("tenantA", "Another expensive question please.")
    after_rej = MODEL_CALLS["n"]
    print(f"  REJECTED tenantA: {r_no}")
    print(f"    model call counter: {before_rej} -> {after_rej}  "
          f"(delta={after_rej - before_rej}, model did NOT run)")

    gate_proven = (after_ok - before == 1) and (after_rej - before_rej == 0) and (not r_no["allowed"])
    print(
        "\n  PROOF: model ran on the allowed turn (delta=1) and was NOT reached on the\n"
        f"         rejected turn (delta=0). Pre-spend gate demonstrated: {gate_proven}\n"
    )
    print(
        "  VERDICT:\n"
        "    [DIY]  Pre-spend per-tenant TOKEN gate — NOT provided by LangGraph/LangChain.\n"
        "           No UsageLimits / no check_before_request equivalent. Implemented above\n"
        "           and proven to block before GRAPH.invoke. Closest built-in is\n"
        "           ModelCallLimitMiddleware (call-COUNT, per-agent, reactive) — not a fit.\n"
        "           aisotropy ships a pre-spend quota gate (token-bucket) as a runtime default."
    )
    return gate_proven


# ─────────────────────────────────────────────────────────────────────────────
# PART 3 — OBSERVABILITY
# ─────────────────────────────────────────────────────────────────────────────
def part3_observability():
    rule("PART 3 — OBSERVABILITY (native OTel spans, captured in-memory, offline)")
    print(
        "We installed a GLOBAL OpenTelemetry TracerProvider + InMemorySpanExporter at the\n"
        "top of this file and set LANGSMITH_OTEL_ENABLED=true / LANGSMITH_OTEL_ONLY=true.\n"
        "LangChain detects the global provider and emits spans into OUR exporter. Nothing\n"
        "leaves the machine; no real LangSmith key is used.\n"
    )

    # LangSmith's OTel bridge exports on a BACKGROUND thread. Drain everything from
    # parts 1 & 2 first (flush + settle), THEN clear, so we capture only this turn.
    time.sleep(0.6)
    _provider.force_flush()
    _SPAN_EXPORTER.clear()

    GRAPH.invoke(
        {"messages": [HumanMessage(content="trace this turn")]},
        config={
            "metadata": {"tenant_id": "tenantA"},   # <-- DIY: we inject tenant_id
            "tags": ["tenant:tenantA"],
            "run_name": "chat-turn",
        },
    )

    # let the background tracer finish, then force the processor to export.
    time.sleep(0.6)
    _provider.force_flush()
    spans = _SPAN_EXPORTER.get_finished_spans()
    print(f"  SPANS CAPTURED by the in-memory OTel exporter: {len(spans)}")
    tenant_on_span = False
    usage_on_span = False
    for s in spans:
        attrs = dict(s.attributes or {})
        gen_ai = {k: v for k, v in attrs.items() if k.startswith("gen_ai.")}
        tkeys = {k: v for k, v in attrs.items() if "tenant" in k.lower()}
        if tkeys:
            tenant_on_span = True
        if "gen_ai.usage.total_tokens" in attrs:
            usage_on_span = True
        print(f"\n  SPAN: {s.name}")
        print(f"    gen_ai.system            = {attrs.get('gen_ai.system')}")
        print(f"    gen_ai.operation.name    = {attrs.get('gen_ai.operation.name')}")
        if "gen_ai.usage.total_tokens" in attrs:
            print(
                f"    gen_ai.usage.*           = in={attrs.get('gen_ai.usage.input_tokens')} "
                f"out={attrs.get('gen_ai.usage.output_tokens')} "
                f"total={attrs.get('gen_ai.usage.total_tokens')}  (token usage ON the span)"
            )
        if tkeys:
            print(f"    tenant attribute(s)      = {tkeys}")
        if "langsmith.span.tags" in attrs:
            print(f"    langsmith.span.tags      = {attrs['langsmith.span.tags']}")

    print(
        f"\n  tenant_id present on spans: {tenant_on_span}   "
        f"token usage present on spans: {usage_on_span}\n"
    )
    print(
        "  VERDICT:\n"
        "    [BUILT-IN] Native OTel spans — LangGraph/LangChain emit gen_ai-semconv spans\n"
        "               (gen_ai.system, gen_ai.operation.name, gen_ai.usage.*) into ANY\n"
        "               OTel exporter you install (captured here in-memory, fully offline).\n"
        "               NB: the emission bridge ships inside the `langsmith` SDK package\n"
        "               (LANGSMITH_OTEL_ENABLED) — free to run, points at your own collector.\n"
        "    [PAID]     LangSmith cloud is the native MANAGED dashboard/backend for these\n"
        "               traces (SaaS, key-gated) — the sales-gated path.\n"
        "    [DIY]      tenant_id ON the spans — not automatic; you inject it into run\n"
        "               metadata/tags (done above -> `langsmith.metadata.tenant_id`).\n"
        "               aisotropy emits OTel gen_ai spans with tenant scope by default,\n"
        "               no SaaS and no per-call wiring."
    )
    return tenant_on_span, usage_on_span, len(spans)


def main():
    print("#" * 74)
    print("# LangGraph — cost + pre-spend gate + observability demo (OFFLINE, fake model)")
    print("# token counts are SYNTHETIC (len(text)//4). testing the FRAMEWORK, not counts.")
    print("#" * 74)

    ledger = part1_per_tenant_cost()
    gate_ok = part2_pre_spend_gate(ledger)
    tenant_on_span, usage_on_span, nspans = part3_observability()

    rule("SUMMARY OF VERDICTS")
    print("  1. PER-TENANT COST   : [BUILT-IN] per-call usage_metadata + within-scope")
    print("                          aggregation | [DIY] per-tenant attribution/totals")
    print("  2. PRE-SPEND GATE    : [DIY] no UsageLimits/pre-spend token gate in the")
    print("                          framework; implemented & proven (model NOT called on reject)")
    print("  3. OBSERVABILITY     : [BUILT-IN] native gen_ai OTel spans -> your own exporter")
    print("                          (bridge lives in langsmith SDK) | [PAID] LangSmith cloud")
    print("                          | [DIY] tenant_id on spans (inject into run metadata)")

    ok = gate_ok and tenant_on_span and usage_on_span and nspans >= 1
    print(f"\n  ALL THREE DEMONSTRATED BY EXECUTION: {ok}")
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
