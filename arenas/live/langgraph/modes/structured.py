"""``structured`` mode — ``ChatOpenAI.with_structured_output`` in a graph node.

Validates the model's output against a JSON Schema via LangChain's native
structured-output support (``method="json_schema"``, which uses the
provider's native structured-output/JSON-mode machinery rather than a
prompt-only convention). ``include_raw=True`` is used so the real provider
token usage on the underlying ``AIMessage`` is still available — the plain
``with_structured_output(...).invoke(...)`` shortcut discards the raw
message and would force fabricating token counts, which the base contract
forbids.

The request may supply its own ``"schema"`` (a JSON Schema object); if
omitted, a small default schema is used so the mode is runnable without
extra configuration.
"""
from __future__ import annotations

import time
import traceback
from typing import Any, TypedDict

from . import common

MODE = "structured"

DEFAULT_SCHEMA: dict[str, Any] = {
    "title": "Answer",
    "description": "A direct answer to the user's question with a self-reported confidence.",
    "type": "object",
    "properties": {
        "answer": {"type": "string", "description": "The direct answer, as short as possible."},
        "confidence": {"type": "number", "description": "Self-reported confidence from 0.0 to 1.0."},
    },
    "required": ["answer", "confidence"],
}


class StructuredState(TypedDict):
    parsed: dict[str, Any] | None
    reply: str
    input_tokens: int
    output_tokens: int
    parsing_error: str


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langchain_openai import ChatOpenAI
        from langgraph.graph import END, START, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")
    if not messages:
        return common.envelope(mode=MODE, model=model_name, error="no messages in request")

    schema = payload.get("schema") or DEFAULT_SCHEMA

    try:
        llm = ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key)
        structured_llm = llm.with_structured_output(schema, method="json_schema", include_raw=True)

        def call_model(_state: StructuredState) -> dict[str, Any]:
            out = structured_llm.invoke(messages)
            raw = out["raw"]
            parsed = out["parsed"]
            parsing_error = out["parsing_error"]
            inp, outp = common.extract_usage(raw)
            content = raw.content if isinstance(raw.content, str) else str(raw.content)
            return {
                "parsed": parsed,
                "reply": content,
                "input_tokens": inp,
                "output_tokens": outp,
                "parsing_error": str(parsing_error) if parsing_error else "",
            }

        graph_builder = StateGraph(StructuredState)
        graph_builder.add_node("structured", call_model)
        graph_builder.add_edge(START, "structured")
        graph_builder.add_edge("structured", END)
        graph = graph_builder.compile()

        start = time.monotonic()
        result = graph.invoke(
            {"parsed": None, "reply": "", "input_tokens": -1, "output_tokens": -1, "parsing_error": ""}
        )
        latency_ms = int((time.monotonic() - start) * 1000)

        if result["parsing_error"]:
            return common.envelope(
                mode=MODE,
                model=model_name,
                latency_ms=latency_ms,
                input_tokens=result["input_tokens"],
                output_tokens=result["output_tokens"],
                error=f"structured output failed schema validation: {result['parsing_error']}",
            )

        parsed = result["parsed"]
        structured_obj = parsed.model_dump() if hasattr(parsed, "model_dump") else parsed

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=result["reply"],
            input_tokens=result["input_tokens"],
            output_tokens=result["output_tokens"],
            latency_ms=latency_ms,
            structured=structured_obj,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
