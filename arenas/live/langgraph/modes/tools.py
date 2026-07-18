"""``tools`` mode — a real LangGraph ReAct loop that EXECUTES a real tool.

Unlike ``qa`` mode's single-shot tool-call *capture* (SPEC.md rule 2: the
base contract's tools are bound but never executed), this mode builds the
classic multi-step agent<->tools LangGraph graph
(``StateGraph`` + ``ToolNode`` + ``tools_condition``, the same primitives
``langgraph.prebuilt.create_react_agent`` builds internally) so the model's
tool call is genuinely run and the result is fed back for a second real
model call before the final answer.

The tool offered is ``calculate`` — arithmetic evaluated through a
restricted AST walk (``ast.parse(..., mode="eval")`` over a small allowlist
of node types), never Python's ``eval``/``exec``, so untrusted expressions
from the model cannot execute arbitrary code.
"""
from __future__ import annotations

import ast
import operator
import time
import traceback
from typing import Any

from . import common

MODE = "tools"

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


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langgraph.graph import END, START, MessagesState, StateGraph
        from langgraph.prebuilt import ToolNode, tools_condition
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")
    if not messages:
        return common.envelope(mode=MODE, model=model_name, error="no messages in request")

    try:
        calculate = _make_calculate_tool()
        llm = common.build_chat_model(model_name, api_key, base_url)
        bound_llm = llm.bind_tools([calculate])

        def call_agent(state: MessagesState) -> dict[str, Any]:
            return {"messages": [bound_llm.invoke(state["messages"])]}

        # Classic ReAct graph: agent decides -> (tools_condition) -> ToolNode
        # actually runs the tool -> loop back to agent with the real
        # ToolMessage result -> repeat until the model stops calling tools.
        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_agent)
        graph_builder.add_node("tools", ToolNode([calculate]))
        graph_builder.add_edge(START, "agent")
        graph_builder.add_conditional_edges("agent", tools_condition, {"tools": "tools", END: END})
        graph_builder.add_edge("tools", "agent")
        graph = graph_builder.compile()

        start = time.monotonic()
        result = graph.invoke({"messages": messages}, config={"recursion_limit": 12})
        latency_ms = int((time.monotonic() - start) * 1000)

        out_messages = result["messages"]

        # Pair each executed tool call with its real ToolMessage result.
        tool_results_by_id: dict[str, str] = {}
        for m in out_messages:
            if type(m).__name__ == "ToolMessage":
                call_id = getattr(m, "tool_call_id", None)
                content = m.content
                tool_results_by_id[call_id] = content if isinstance(content, str) else str(content)

        tool_calls: list[dict[str, Any]] = []
        ai_messages = []
        for m in out_messages:
            if type(m).__name__ == "AIMessage":
                ai_messages.append(m)
                for call in getattr(m, "tool_calls", None) or []:
                    call_id = call.get("id") if isinstance(call, dict) else getattr(call, "id", None)
                    name = call.get("name") if isinstance(call, dict) else getattr(call, "name", "")
                    args = call.get("args") if isinstance(call, dict) else getattr(call, "args", {})
                    tool_calls.append(
                        {
                            "name": name,
                            "arguments": args if isinstance(args, dict) else {},
                            "result": tool_results_by_id.get(call_id, ""),
                        }
                    )

        final_ai = ai_messages[-1] if ai_messages else None
        content = final_ai.content if final_ai is not None else ""
        reply = content if isinstance(content, str) else str(content)
        input_tokens, output_tokens = common.sum_usage(ai_messages)

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
