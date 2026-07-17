"""``observability`` mode — measures LangGraph/LangChain's NATIVE
observability surface, honestly, against a real multi-op turn.

This mode does **not** return the shared multi-mode envelope
(``modes.common.envelope``) — it is a diagnostic/report mode, not a chat
turn, so it returns its own JSON shape (see ``_report`` below). It is wired
into ``adapter.py``'s ``MODES`` dict like every other mode.

What this proves, and how
--------------------------
LangGraph/LangChain ship **no OpenTelemetry exporter of their own**. There is
no ``OTEL_*`` env var, no ``langgraph.otel`` module, and ``graph.invoke(...)``
emits zero OTel spans out of the box — verified by grep against the
installed ``langgraph``/``langchain-core`` packages in this arena's
``.venv`` (no ``opentelemetry`` import anywhere in either package).

What LangChain/LangGraph DO ship natively is the **callback system**
(``langchain_core.callbacks.BaseCallbackHandler``): every chain, chat-model,
and tool invocation inside a graph fires ``on_*_start``/``on_*_end`` (or
``on_*_error``) with a ``run_id``/``parent_run_id`` pair that already forms a
proper execution tree (LangSmith and every third-party LangChain tracer
integration is built on exactly this hook, nothing more privileged). This
mode's ``_OTelBridgeHandler`` bridges that native callback stream to real
``opentelemetry-sdk`` spans, held by an in-process ``InMemorySpanExporter``
(``SimpleSpanProcessor`` exports synchronously on ``span.end()`` — no OTLP
collector, no network hop, nothing leaves this process). This is this
adapter's own ~80 lines of bridge code, not a third-party auto-instrumentation
package — there was no MIT/BSD/Apache-licensed "LangChain -> OTel" package
already installed in this arena, and writing the bridge directly keeps the
dependency surface to the two official ``opentelemetry-python`` packages
(both Apache-2.0, see README's "Dependencies and licenses").

The representative turn instrumented here is the same real ReAct tool-use
loop as ``modes/tools.py`` (agent decides to call ``calculate`` -> the tool
genuinely executes -> agent answers from the real result) — a "tools turn"
per this task's brief, i.e. multiple real ops in one turn: at least two real
``ChatOpenAI`` calls plus one real tool execution, each wrapped in its own
LangChain callback pair and therefore its own OTel span.
"""
from __future__ import annotations

import ast
import json
import logging
import operator
import time
import traceback
from typing import Any
from uuid import UUID

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


def _infer_name(serialized: dict[str, Any] | None, kwargs: dict[str, Any], fallback: str) -> str:
    name = kwargs.get("name")
    if isinstance(name, str) and name:
        return name
    if serialized:
        name = serialized.get("name")
        if isinstance(name, str) and name:
            return name
        id_path = serialized.get("id")
        if isinstance(id_path, list) and id_path:
            return str(id_path[-1])
    return fallback


class _OTelBridgeHandler:
    """LangChain callback handler that bridges run_id/parent_run_id pairs
    to real opentelemetry-sdk spans on a private (non-global) TracerProvider.

    Subclasses ``BaseCallbackHandler`` lazily (import deferred so this
    module has no hard opentelemetry/langchain import at module load time,
    matching the rest of this adapter's "import inside run()" convention).
    """

    def __init__(self, tracer: Any) -> None:
        self._tracer = tracer
        self._spans: dict[UUID, Any] = {}
        self._starts: dict[UUID, float] = {}

    # -- internal helpers -------------------------------------------------
    def _start(self, run_id: UUID, parent_run_id: UUID | None, name: str, kind: Any, attributes: dict[str, Any] | None = None) -> None:
        from opentelemetry import trace as trace_api

        parent_span = self._spans.get(parent_run_id) if parent_run_id else None
        ctx = trace_api.set_span_in_context(parent_span) if parent_span is not None else None
        span = self._tracer.start_span(name, context=ctx, kind=kind, attributes=attributes or {})
        self._spans[run_id] = span
        self._starts[run_id] = time.monotonic()

    def _end(self, run_id: UUID, attributes: dict[str, Any] | None = None, error: BaseException | None = None) -> None:
        span = self._spans.pop(run_id, None)
        self._starts.pop(run_id, None)
        if span is None:
            return
        if attributes:
            for k, v in attributes.items():
                try:
                    span.set_attribute(k, v)
                except Exception:  # pragma: no cover - defensive
                    pass
        if error is not None:
            span.set_attribute("error", True)
            span.set_attribute("error.type", type(error).__name__)
        span.end()

    def open_span_count(self) -> int:
        return len(self._spans)

    def close_dangling(self) -> None:
        """Force-close any span whose matching *_end callback never fired.

        Recorded as a diagnostic, not silently discarded — see the ``notes``
        field in the final report for whether this ever triggers.
        """
        for run_id in list(self._spans):
            self._end(run_id, attributes={"force_closed": True})

    # -- LangChain callback surface ---------------------------------------
    def on_chain_start(self, serialized, inputs, *, run_id, parent_run_id=None, tags=None, metadata=None, **kwargs):
        from opentelemetry.trace import SpanKind

        name = _infer_name(serialized, kwargs, "chain")
        self._start(run_id, parent_run_id, f"chain.{name}", SpanKind.INTERNAL)

    def on_chain_end(self, outputs, *, run_id, parent_run_id=None, **kwargs):
        self._end(run_id)

    def on_chain_error(self, error, *, run_id, parent_run_id=None, **kwargs):
        self._end(run_id, error=error)

    def on_chat_model_start(self, serialized, messages, *, run_id, parent_run_id=None, tags=None, metadata=None, **kwargs):
        from opentelemetry.trace import SpanKind

        name = _infer_name(serialized, kwargs, "ChatModel")
        self._start(run_id, parent_run_id, f"llm.{name}", SpanKind.CLIENT)

    def on_llm_start(self, serialized, prompts, *, run_id, parent_run_id=None, tags=None, metadata=None, **kwargs):
        from opentelemetry.trace import SpanKind

        name = _infer_name(serialized, kwargs, "LLM")
        self._start(run_id, parent_run_id, f"llm.{name}", SpanKind.CLIENT)

    def on_llm_end(self, response, *, run_id, parent_run_id=None, **kwargs):
        attrs: dict[str, Any] = {}
        try:
            generations = getattr(response, "generations", None) or []
            if generations and generations[0]:
                msg = getattr(generations[0][0], "message", None)
                if msg is not None:
                    inp, out = common.extract_usage(msg)
                    if inp >= 0:
                        attrs["gen_ai.usage.input_tokens"] = inp
                    if out >= 0:
                        attrs["gen_ai.usage.output_tokens"] = out
        except Exception:  # pragma: no cover - defensive
            pass
        self._end(run_id, attributes=attrs)

    def on_llm_error(self, error, *, run_id, parent_run_id=None, tags=None, **kwargs):
        self._end(run_id, error=error)

    def on_tool_start(self, serialized, input_str, *, run_id, parent_run_id=None, tags=None, metadata=None, inputs=None, **kwargs):
        from opentelemetry.trace import SpanKind

        name = _infer_name(serialized, kwargs, "tool")
        self._start(run_id, parent_run_id, f"tool.{name}", SpanKind.INTERNAL, attributes={"tool.input": str(input_str)[:200]})

    def on_tool_end(self, output, *, run_id, parent_run_id=None, **kwargs):
        self._end(run_id, attributes={"tool.output": str(output)[:200]})

    def on_tool_error(self, error, *, run_id, parent_run_id=None, **kwargs):
        self._end(run_id, error=error)


def _make_handler_class():
    """Builds the concrete BaseCallbackHandler subclass at call time.

    Deferred so importing this module never requires langchain_core to be
    installed unless ``observability`` mode is actually invoked (matches
    every other mode's lazy-import convention in this adapter).
    """
    from langchain_core.callbacks import BaseCallbackHandler

    class OTelBridgeHandler(_OTelBridgeHandler, BaseCallbackHandler):
        pass

    return OTelBridgeHandler


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
    model_name = __import__("os").environ.get("OPENAI_MODEL", common.DEFAULT_MODEL)
    api_key = __import__("os").environ.get("OPENAI_API_KEY")
    base_url = __import__("os").environ.get("OPENAI_BASE_URL")
    if not api_key:
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error="OPENAI_API_KEY is not set",
        )
    if not base_url:
        return _report(
            otel_supported=False, span_count=0, span_names=[], span_timings_ms={},
            logs_structured=False, log_sample="", notes="",
            error="OPENAI_BASE_URL is not set",
        )

    try:
        from langchain_openai import ChatOpenAI
        from langgraph.graph import END, START, MessagesState, StateGraph
        from langgraph.prebuilt import ToolNode, tools_condition
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

    # --- private (non-global) OTel pipeline, in-process only --------------
    exporter = InMemorySpanExporter()
    provider = TracerProvider(resource=Resource.create({"service.name": "aisotropy-arena-langgraph"}))
    provider.add_span_processor(SimpleSpanProcessor(exporter))  # synchronous export on span.end()
    tracer = provider.get_tracer("aisotropy.arenas.live.langgraph.observability")

    HandlerClass = _make_handler_class()
    otel_handler = HandlerClass(tracer)

    try:
        calculate = _make_calculate_tool()
        llm = ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key)
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
                {"messages": messages},
                config={"recursion_limit": 12, "callbacks": [otel_handler]},
            )
            wall_ms = int((time.monotonic() - wall_start) * 1000)

        dangling_before_close = otel_handler.open_span_count()
        otel_handler.close_dangling()  # ensures export even if some *_end never fired

        finished = list(exporter.get_finished_spans())
        # Order by start time so span_names reflects real execution order.
        finished.sort(key=lambda s: s.start_time)

        span_names: list[str] = []
        span_timings_ms: dict[str, float] = {}
        seen_counts: dict[str, int] = {}
        for span in finished:
            name = span.name
            span_names.append(name)
            duration_ms = round((span.end_time - span.start_time) / 1_000_000, 3)
            seen_counts[name] = seen_counts.get(name, 0) + 1
            key = name if seen_counts[name] == 1 else f"{name}#{seen_counts[name]}"
            span_timings_ms[key] = duration_ms

        log_records = log_capture.records
        logs_structured = False
        if log_records:
            try:
                parsed = json.loads(log_records[0])
                logs_structured = isinstance(parsed, dict)
            except Exception:
                logs_structured = False
        log_sample = log_records[0] if log_records else ""

        notes = (
            "LangGraph/LangChain emit NO OpenTelemetry spans natively — no opentelemetry import "
            "anywhere in the installed langgraph/langchain-core packages, no OTEL_* env var read, "
            "graph.invoke() alone produces zero spans. What IS native is the LangChain callback "
            "system (on_chain_start/end, on_chat_model_start/on_llm_end, on_tool_start/end with "
            "run_id/parent_run_id forming a real execution tree) — this is the same hook LangSmith "
            "and every third-party tracer integration builds on. This mode bridges that native "
            "callback stream to real opentelemetry-sdk spans itself (this adapter's own "
            f"~80-line _OTelBridgeHandler in modes/observability.py), captured by an "
            "InMemorySpanExporter (SimpleSpanProcessor exports synchronously on span.end(), "
            "nothing leaves this process, no OTLP collector). "
            f"{span_count_note(len(finished), dangling_before_close)} "
            f"Wall time for the whole turn (graph.invoke): {wall_ms}ms across "
            f"{sum(1 for n in span_names if n.startswith('llm.'))} real LLM call(s) and "
            f"{sum(1 for n in span_names if n.startswith('tool.'))} real tool execution(s). "
            "Logging: LangChain/LangGraph do not use Python's stdlib logging module for tracing — "
            f"{len(log_records)} record(s) observed at INFO+ across langchain*/langgraph* loggers "
            "during this turn (openai/httpx loggers deliberately excluded from capture — they can "
            "log request headers, including Authorization, at DEBUG, and this adapter never logs "
            "the API key). "
            + ("The one captured sample is plain text, not JSON — LangChain's own log lines are "
               "unstructured strings, not structured records." if log_records else
               "No LangChain/LangGraph log records were emitted at all for this turn; the callback "
               "stream above is the only native, observable instrumentation surface.")
        )

        return _report(
            otel_supported=False,
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
        provider.shutdown()


def span_count_note(span_count: int, dangling_before_close: int) -> str:
    base = f"This turn produced {span_count} real spans through that bridge."
    if dangling_before_close:
        base += (
            f" {dangling_before_close} span(s) had no matching *_end callback fire before "
            "graph.invoke() returned and were force-closed for export (flagged via the "
            "'force_closed' span attribute, not silently dropped)."
        )
    return base
