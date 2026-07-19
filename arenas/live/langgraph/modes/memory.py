"""``memory`` mode — LangGraph checkpointer persistence across processes.

Each adapter invocation is one process, one stdin/stdout turn (SPEC.md). To
prove real cross-process memory, this mode uses ``langgraph.checkpoint
.sqlite.SqliteSaver`` against an on-disk SQLite file
(``CHECKPOINT_DB_PATH``, default ``<adapter_dir>/.data/checkpoints.sqlite``)
keyed by a caller-supplied ``thread_id``. Two separate ``run.sh`` invocations
that reuse the same ``thread_id`` share the same checkpointed message
history even though they are different OS processes — the second process
never sees the first turn's text on its own stdin, it recovers it from the
checkpoint.

Contract for this mode: the request JSON MUST carry a conversation
identifier — the arena sends it as ``"session_id"`` (SPEC.md, runner
``ChatInput.SessionID`` -> ``json:"session_id"``), which this mode uses
directly as the LangGraph checkpointer ``thread_id``. An explicit
``"thread_id"`` is still accepted and takes precedence as an override for
direct callers. ``"messages"`` should contain only the NEW turn(s) since the
last checkpoint for that thread — history is recovered from the checkpoint,
not replayed by the caller (that is the entire point of the mode).
"""
from __future__ import annotations

import os
import time
import traceback
from typing import Any

from . import common

MODE = "memory"


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    # The live-arena contract (SPEC.md, runner types.go ChatInput.SessionID
    # `json:"session_id"`) carries the conversation identifier as
    # ``session_id`` — that IS this mode's LangGraph checkpointer thread key.
    # An explicit ``thread_id`` remains accepted as an override for direct
    # callers, but the arena's ``session_id`` must work on its own.
    thread_id = payload.get("thread_id") or payload.get("session_id")
    if not thread_id or not isinstance(thread_id, str):
        return common.envelope(
            mode=MODE,
            model=model_name,
            error="memory mode requires a non-empty string 'session_id' (or 'thread_id') field",
        )

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

    db_path = payload.get("checkpoint_db_path") or common.DEFAULT_CHECKPOINT_DB
    # ARENA_STUB=1 uses an in-memory checkpointer (common.checkpointer_context)
    # and never touches disk — see that function's docstring.
    if not common.is_stub():
        os.makedirs(os.path.dirname(db_path), exist_ok=True)

    try:
        llm = common.build_chat_model(model_name, api_key, base_url)

        def call_model(state: MessagesState) -> dict[str, Any]:
            return {"messages": [llm.invoke(state["messages"])]}

        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_model)
        graph_builder.add_edge(START, "agent")
        graph_builder.add_edge("agent", END)

        start = time.monotonic()
        # checkpointer_context is a context manager; a single invocation
        # opens the checkpointer (real on-disk file, or ARENA_STUB=1's
        # in-memory saver), runs the one graph.invoke() for this turn (which
        # both reads the prior checkpoint for thread_id, if any, and
        # durably writes the new one), then closes cleanly before the
        # process exits.
        with common.checkpointer_context(db_path) as saver:
            graph = graph_builder.compile(checkpointer=saver)
            result = graph.invoke(
                {"messages": messages},
                config={"configurable": {"thread_id": thread_id}},
            )
        latency_ms = int((time.monotonic() - start) * 1000)

        ai_message = result["messages"][-1]
        content = ai_message.content
        reply = content if isinstance(content, str) else str(content)
        input_tokens, output_tokens = common.extract_usage(ai_message)

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=reply,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            latency_ms=latency_ms,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
