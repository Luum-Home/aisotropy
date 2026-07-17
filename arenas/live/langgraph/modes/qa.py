"""``qa`` mode — plain single-turn chat through a minimal LangGraph graph.

This is the original single-shot adapter behavior, unchanged: one
``StateGraph(MessagesState)`` with one ``agent`` node between START and END.
If ``tools`` are present in the request, they are bound and a tool call is
captured single-shot (never executed) per the base arena contract in
SPEC.md. For a mode that actually executes tools multi-step, see
``modes.tools``.
"""
from __future__ import annotations

import time
import traceback
from typing import Any

from . import common

MODE = "qa"


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langchain_openai import ChatOpenAI
        from langgraph.graph import END, START, MessagesState, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")

    try:
        llm = ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key)

        tools = payload.get("tools") or []
        if tools:
            openai_tools = [common.to_openai_tool(t) for t in tools]
            bound_llm = llm.bind_tools(openai_tools)
        else:
            bound_llm = llm

        def call_model(state: MessagesState) -> dict[str, Any]:
            response = bound_llm.invoke(state["messages"])
            return {"messages": [response]}

        # Minimal single-node LangGraph graph: the model call genuinely
        # goes through graph.invoke(), not a bare LangChain call.
        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_model)
        graph_builder.add_edge(START, "agent")
        graph_builder.add_edge("agent", END)
        graph = graph_builder.compile()

        start = time.monotonic()
        result = graph.invoke({"messages": messages})
        latency_ms = int((time.monotonic() - start) * 1000)

        ai_message = result["messages"][-1]
        content = ai_message.content
        reply = content if isinstance(content, str) else str(content)
        input_tokens, output_tokens = common.extract_usage(ai_message)
        tool_calls = common.extract_tool_calls(ai_message)

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=reply,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            latency_ms=latency_ms,
            tool_calls=tool_calls,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
