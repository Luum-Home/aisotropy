"""``stream`` mode — real token streaming through ``graph.stream(...)``.

Uses LangGraph's ``stream_mode="messages"``, which yields
``(message_chunk, metadata)`` pairs as the underlying ``ChatOpenAI`` call
streams real tokens from the provider (not a post-hoc chunking of a
completed response). ``ttft_ms`` is the wall-clock time from just before
``graph.stream(...)`` is called to the first chunk actually yielded by the
generator — a genuine time-to-first-token measurement of the real network
call, not simulated.

``stream_usage=True`` on ``ChatOpenAI`` asks the provider for usage on the
final streamed chunk (OpenAI-compatible ``stream_options.include_usage``);
if the provider does not include it, usage falls back to ``-1``/``-1``
rather than being fabricated.
"""
from __future__ import annotations

import time
import traceback
from typing import Any

from . import common

MODE = "stream"


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langgraph.graph import END, START, MessagesState, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")
    if not messages:
        return common.envelope(mode=MODE, model=model_name, error="no messages in request")

    try:
        llm = common.build_chat_model(model_name, api_key, base_url, stream_usage=True)

        def call_model(state: MessagesState) -> dict[str, Any]:
            return {"messages": [llm.invoke(state["messages"])]}

        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_model)
        graph_builder.add_edge(START, "agent")
        graph_builder.add_edge("agent", END)
        graph = graph_builder.compile()

        start = time.monotonic()
        ttft_ms = -1
        reply_parts: list[str] = []
        input_tokens, output_tokens = -1, -1

        for message_chunk, _metadata in graph.stream({"messages": messages}, stream_mode="messages"):
            if ttft_ms == -1:
                ttft_ms = int((time.monotonic() - start) * 1000)
            content = message_chunk.content
            if isinstance(content, str) and content:
                reply_parts.append(content)
            usage = getattr(message_chunk, "usage_metadata", None)
            if usage:
                inp = usage.get("input_tokens")
                out = usage.get("output_tokens")
                if isinstance(inp, int) and isinstance(out, int):
                    input_tokens, output_tokens = inp, out

        latency_ms = int((time.monotonic() - start) * 1000)
        reply = "".join(reply_parts)

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=reply,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            latency_ms=latency_ms,
            ttft_ms=ttft_ms,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
