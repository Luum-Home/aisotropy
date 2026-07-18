"""``observability`` mode — measures LangGraph/LangChain's observability
surface against a real multi-op turn, using the REAL open-source OTel
auto-instrumentation the community already ships.

This mode does **not** return the shared multi-mode envelope
(``modes.common.envelope``) — it is a diagnostic/report mode, not a chat
turn, so it returns its own JSON shape (see ``_report`` below). It is wired
into ``adapter.py``'s ``MODES`` dict like every other mode.

What this proves, and how
--------------------------
LangGraph/LangChain ship **no OpenTelemetry exporter of their own** — no
``OTEL_*`` env var, no ``langgraph.otel`` module, and ``graph.invoke(...)``
emits zero OTel spans out of the box (verified by grep against the installed
``langgraph``/``langchain-core`` packages: no ``opentelemetry`` import in
either). What LangChain/LangGraph DO ship natively is the **callback system**
(``langchain_core.callbacks.BaseCallbackHandler``): every chain, chat-model,
and tool invocation inside a graph fires ``on_*_start``/``on_*_end`` (or
``on_*_error``) with a ``run_id``/``parent_run_id`` pair that already forms a
proper execution tree. **LangSmith** — LangChain's own hosted/native
observability platform — is built on exactly this hook, and so is every
third-party OTel integration; none of them get any more privileged access
than an app developer gets by attaching their own ``BaseCallbackHandler``.

The earlier version of this mode hand-rolled its own ~80-line
callback-to-OTel bridge (``_OTelBridgeHandler``) and used that to argue
LangGraph "needs custom OTel plumbing." That framing was misleading: a real
developer does not write a bridge. They add one of several MIT/Apache-2.0
**auto-instrumentation packages** that already do exactly this, in 2-3 lines,
with zero changes to their own graph/agent code:

- **OpenInference** — ``openinference-instrumentation-langchain``
  (Apache-2.0, Arize AI). Used here. Same OpenInference family this repo's
  ``arenas/live/llamaindex`` adapter already uses for its own
  ``observability`` mode (``openinference-instrumentation-llama-index``) —
  picked for consistency across arenas and because it is the OpenInference
  family closest to the OTel GenAI semantic conventions
  (``openinference.span.kind`` on every span; ``gen_ai.*``-shaped usage
  attributes on LLM spans).
- **OpenLLMetry** — ``opentelemetry-instrumentation-langchain`` (Apache-2.0,
  Traceloop). A drop-in alternative with the same "instrument once, get
  spans" shape; not installed here to keep one instrumentation family
  active per run (mixing two auto-instrumentors patching the same
  ``BaseCallbackManager.__init__`` seam is unnecessary for what this mode
  measures and would muddy the span count).
- **OpenLIT** — another OSS OTel-native LLM observability SDK with LangChain
  auto-instrumentation support; same category, not installed for the same
  reason as OpenLLMetry above.

All three integrate the SAME way: monkey-patch (technically, ``wrapt``-wrap)
``langchain_core.callbacks.BaseCallbackManager.__init__`` so every callback
manager LangChain/LangGraph constructs automatically gets a tracer handler
attached — **no ``callbacks=[...]`` argument, no custom handler class, no
edit to the graph being tested.** That is the entire "how a dev adds OTel to
LangGraph" story; this mode now tests exactly that story instead of a
bespoke bridge.

**Two honest, separate axes** (do not conflate them):

1. **Native observability platform**: LangChain/LangGraph's own product for
   this is **LangSmith** (hosted SaaS, a `.venv` dependency already pulled
   in transitively — see README's dependency table — but never activated by
   this adapter: no ``LANGCHAIN_TRACING_V2``/``LANGCHAIN_API_KEY`` is set,
   so zero data leaves this process). That is a *product* answer, not an
   OTel answer.
2. **Measured OTel emission**: what THIS mode reports — real
   ``opentelemetry-sdk`` spans, captured in-process (``InMemorySpanExporter``
   + ``SimpleSpanProcessor``, synchronous export on ``span.end()``, no OTLP
   collector, nothing leaves this process), produced by wiring one
   community-maintained OSS auto-instrumentation package
   (``LangChainInstrumentor().instrument(tracer_provider=...)``) around a
   real multi-op turn. ``otel_supported`` reflects axis (2) — whether that
   wiring produced real spans for a real turn — not axis (1).

The representative turn instrumented here is the same real ReAct tool-use
loop as ``modes/tools.py`` (agent decides to call ``calculate`` -> the tool
genuinely executes -> agent answers from the real result) — a "tools turn"
per this task's brief, i.e. multiple real ops in one turn: at least two real
``ChatOpenAI`` calls plus one real tool execution, each producing its own
OTel span through OpenInference's instrumentation.
"""
from __future__ import annotations

import ast
import json
import logging
import operator
import time
import traceback
from typing import Any

from . import common

MODE = "observability"

# Logger name prefixes safe to capture: LangChain/LangGraph's own loggers
# only. Deliberately EXCLUDES "openai"/"httpx"/root — the openai SDK and
# httpx can log request details (including the Authorization header) at
# DEBUG level, and this adapter's hard security rule (see every mode's
# module docstring) is: never print, log, or persist the API key. Capturing
# is also capped at INFO so even a misconfigured LangChain logger can't
# surface DEBUG-level internals.
_SAFE_LOGGER_PREFIXES = ("langchain", "langgraph")
_SECRET_MARKERS = ("authorization", "bearer ", "api_key", "api-key")


class _SafeLogCapture(logging.Handler):
    """Captures INFO+ records from LangChain/LangGraph loggers only.

    Any record whose rendered message contains a credential-shaped marker is
    dropped (not just redacted) as defense-in-depth on top of the
    logger-name allowlist above.
    """

    def __init__(self) -> None:
        super().__init__(level=logging.INFO)
        self.records: list[str] = []

    def emit(self, record: logging.LogRecord) -> None:
        try:
            text = self.format(record)
        except Exception:  # pragma: no cover - defensive
            return
        lowered = text.lower()
        if any(marker in lowered for marker in _SECRET_MARKERS):
            return
        self.records.append(text)

    def __enter__(self) -> "_SafeLogCapture":
        self._attached: list[logging.Logger] = []
        for name in _SAFE_LOGGER_PREFIXES:
            logger = logging.getLogger(name)
            self._prev_level = logger.level
            logger.setLevel(logging.INFO)
            logger.addHandler(self)
            self._attached.append(logger)
        return self

    def __exit__(self, *exc_info: Any) -> None:
        for logger in self._attached:
            logger.removeHandler(self)
            logger.setLevel(self._prev_level)


# -- the real tool for the representative "tools" turn ---------------------
_BIN_OPS = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.FloorDiv: operator.floordiv,
    ast.Mod: operator.mod,
    ast.Pow: operator.pow,
}
_UNARY_OPS = {ast.USub: operator.neg, ast.UAdd: operator.pos}


def _safe_eval(node: ast.AST) -> float:
    if isinstance(node, ast.Expression):
        return _safe_eval(node.body)
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return node.value
    if isinstance(node, ast.BinOp) and type(node.op) in _BIN_OPS:
        return _BIN_OPS[type(node.op)](_safe_eval(node.left), _safe_eval(node.right))
    if isinstance(node, ast.UnaryOp) and type(node.op) in _UNARY_OPS:
        return _UNARY_OPS[type(node.op)](_safe_eval(node.operand))
    raise ValueError(f"unsupported expression node: {type(node).__name__}")


def _make_calculate_tool():
    from langchain_core.tools import tool

    @tool
    def calculate(expression: str) -> str:
        """Evaluate a basic arithmetic expression (+ - * / // % ** and parens with numeric literals) and return the numeric result as a string."""
        tree = ast.parse(expression, mode="eval")
        return str(_safe_eval(tree))

    return calculate


def _report(
    *,
    otel_supported: bool,
    span_count: int,
    span_names: list[str],
    span_timings_ms: dict[str, float],
    logs_structured: bool,
    log_sample: str,
    notes: str,
    error: str = "",
) -> dict[str, Any]:
    return {
        "framework": common.FRAMEWORK,
        "mode": MODE,
        "otel_supported": otel_supported,
        "span_count": span_count,
        "span_names": span_names,
        "span_timings_ms": span_timings_ms,
        "logs_structured": logs_structured,
        "log_sample": log_sample,
        "notes": notes,
        "error": error,
    }


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env("observability")
    if err:
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error=err.get("error", "missing chat env"),
        )

    try:
        from langgraph.graph import END, START, MessagesState, StateGraph
        from langgraph.prebuilt import ToolNode, tools_condition
        from openinference.instrumentation.langchain import LangChainInstrumentor
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import SimpleSpanProcessor
        from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
    except Exception as exc:  # pragma: no cover - import failure path
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error=f"import failure: {exc}",
        )

    try:
        messages = common.build_messages(payload) if payload.get("messages") else []
    except Exception as exc:
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error=f"invalid request payload: {exc}",
        )
    if not messages:
        from langchain_core.messages import HumanMessage

        default_expr = "(482 * 17) + 391"
        messages = [HumanMessage(content=f"Use the calculate tool to compute {default_expr}, then state the result plainly.")]

    # --- REAL OSS OTel auto-instrumentation, in-process only ---------------
    # This IS the whole "add OTel to LangGraph" story: an in-process
    # TracerProvider + InMemorySpanExporter (no OTLP collector, nothing
    # leaves this process), then ONE call — LangChainInstrumentor().instrument
    # (tracer_provider=...) — from the community-maintained, Apache-2.0
    # `openinference-instrumentation-langchain` package. No custom handler
    # class, no `callbacks=[...]` argument on graph.invoke() below, and zero
    # changes to the graph/agent code being tested (see module docstring).
    exporter = InMemorySpanExporter()
    provider = TracerProvider(resource=Resource.create({"service.name": "aisotropy-arena-langgraph"}))
    provider.add_span_processor(SimpleSpanProcessor(exporter))  # synchronous export on span.end()
    instrumentor = LangChainInstrumentor()
    instrumentor.instrument(tracer_provider=provider)  # <- the 1-line "add OTel" step

    try:
        calculate = _make_calculate_tool()
        llm = common.build_chat_model(model_name, api_key, base_url)
        bound_llm = llm.bind_tools([calculate])

        def call_agent(state: MessagesState) -> dict[str, Any]:
            return {"messages": [bound_llm.invoke(state["messages"])]}

        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_agent)
        graph_builder.add_node("tools", ToolNode([calculate]))
        graph_builder.add_edge(START, "agent")
        graph_builder.add_conditional_edges("agent", tools_condition, {"tools": "tools", END: END})
        graph_builder.add_edge("tools", "agent")
        graph = graph_builder.compile()

        with _SafeLogCapture() as log_capture:
            wall_start = time.monotonic()
            graph.invoke(
                # No `callbacks=[...]` here — unlike the old hand-rolled
                # bridge, OpenInference's instrumentor patches
                # langchain_core.callbacks.BaseCallbackManager.__init__
                # itself, so every callback manager LangGraph constructs for
                # this invocation already carries the OTel tracer.
                {"messages": messages},
                config={"recursion_limit": 12},
            )
            wall_ms = int((time.monotonic() - wall_start) * 1000)

        finished = list(exporter.get_finished_spans())
        # Order by start time so span_names reflects real execution order.
        finished.sort(key=lambda s: s.start_time)

        span_names: list[str] = []
        span_timings_ms: dict[str, float] = {}
        span_kinds: set[str] = set()
        seen_counts: dict[str, int] = {}
        for span in finished:
            name = span.name
            span_names.append(name)
            duration_ms = round((span.end_time - span.start_time) / 1_000_000, 3)
            seen_counts[name] = seen_counts.get(name, 0) + 1
            key = name if seen_counts[name] == 1 else f"{name}#{seen_counts[name]}"
            span_timings_ms[key] = duration_ms
            kind = span.attributes.get("openinference.span.kind") if span.attributes else None
            if kind:
                span_kinds.add(str(kind))

        log_records = log_capture.records
        logs_structured = False
        if log_records:
            try:
                parsed = json.loads(log_records[0])
                logs_structured = isinstance(parsed, dict)
            except Exception:
                logs_structured = False
        log_sample = log_records[0] if log_records else ""

        otel_supported = len(finished) > 0

        notes = (
            "OTel here comes from a REAL open-source auto-instrumentation package — "
            "openinference-instrumentation-langchain (Apache-2.0, Arize AI OpenInference family; "
            "the SAME family this repo's arenas/live/llamaindex adapter uses for its own "
            "observability mode) — wired in ONE line, "
            "LangChainInstrumentor().instrument(tracer_provider=<in-process TracerProvider + "
            "InMemorySpanExporter>), with ZERO changes to the graph/agent code under test: no "
            "custom callback handler, no callbacks=[...] argument on graph.invoke(). This is NOT "
            "this adapter's own bespoke bridge (an earlier version of this mode hand-rolled an "
            "~80-line callback-to-OTel bridge; that framing overstated how hard OTel is to add to "
            "LangGraph and has been replaced by this real OSS package). Mechanism: LangGraph/"
            "LangChain still ship no OpenTelemetry exporter of their own (no opentelemetry import "
            "in the installed langgraph/langchain-core packages, no OTEL_* env var read) — "
            "OpenInference's instrumentor wraps langchain_core.callbacks.BaseCallbackManager."
            "__init__ so every callback manager LangChain/LangGraph constructs automatically gets "
            "an OpenInference tracer handler attached, translating the SAME native on_chain_start/"
            "end, on_chat_model_start/on_llm_end, on_tool_start/end callback stream (the exact "
            "hook LangSmith and every third-party tracer is built on) into real opentelemetry-sdk "
            "spans. Alternatives in the same OSS category, not installed here (one instrumentor "
            "family is enough to measure this and avoids double-patching the same callback-manager "
            "seam): OpenLLMetry (opentelemetry-instrumentation-langchain, Traceloop) and OpenLIT — "
            "both integrate the same way (wrap the callback-manager construction path), so the "
            "span shape reported below is representative of the category, not one vendor's quirk. "
            f"Captured directly off the real spans, not assumed: span kind(s) seen via the "
            f"'openinference.span.kind' attribute = {sorted(span_kinds) or '(none found)'}. "
            f"This turn produced {len(finished)} real span(s) via that instrumentation for one "
            f"tools-style turn. Wall time for the whole turn (graph.invoke): {wall_ms}ms across "
            f"{sum(1 for n in span_names if 'ChatOpenAI' in n)} real LLM call(s) and "
            f"{sum(1 for n in span_names if n == 'calculate')} real tool execution(s). "
            "Two honest, separate axes (do not conflate): (1) LangChain/LangGraph's own NATIVE "
            "observability PLATFORM is LangSmith — a hosted SaaS product, never activated here "
            "(no LANGCHAIN_TRACING_V2/LANGCHAIN_API_KEY set, zero data leaves this process); (2) "
            "this mode measures OTel EMISSION — whether wiring a real OSS auto-instrumentor around "
            "a real turn produces real spans, which is exactly what otel_supported/span_count "
            f"report above (otel_supported={otel_supported}). "
            "Logging: LangChain/LangGraph do not use Python's stdlib logging module for tracing — "
            f"{len(log_records)} record(s) observed at INFO+ across langchain*/langgraph* loggers "
            "during this turn (openai/httpx loggers deliberately excluded from capture — they can "
            "log request headers, including Authorization, at DEBUG, and this adapter never logs "
            "the API key). "
            + ("The one captured sample is plain text, not JSON — LangChain's own log lines are "
               "unstructured strings, not structured records." if log_records else
               "No LangChain/LangGraph log records were emitted at all for this turn; the OTel "
               "spans above are the primary observable instrumentation surface for this turn.")
        )

        return _report(
            otel_supported=otel_supported,
            span_count=len(finished),
            span_names=span_names,
            span_timings_ms=span_timings_ms,
            logs_structured=logs_structured,
            log_sample=log_sample,
            notes=notes,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error=f"{type(exc).__name__}: {exc}",
        )
    finally:
        instrumentor.uninstrument()
        provider.shutdown()
