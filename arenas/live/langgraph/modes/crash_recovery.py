"""``crash_recovery`` mode — does LangGraph's checkpointer survive a real
SIGKILL mid-turn, not just a clean process exit?

``memory`` mode (``modes/memory.py``) already proves cross-process recall
across two *cleanly exiting* ``run.sh`` invocations. This mode proves a
harder, distinct claim: recovery after a **hard crash** — ``kill -9`` sent to
the process while it is blocked mid-flight on the real agnes HTTP call for a
SECOND turn, i.e. strictly after the FIRST turn's checkpoint was already
durably committed to disk, but before the second turn's checkpoint (or even
its response) exists anywhere.

Two ops, dispatched by the request's ``"op"`` field:

``op == "converse"`` (process A — the one that gets killed)
    One long-lived process drives a short multi-turn conversation on a
    single ``thread_id``, given as a list of ``"turns"`` (plain strings, one
    per turn). Each turn opens its OWN ``SqliteSaver.from_conn_string(...)``
    context manager, so the checkpoint write for turn *N* is durably
    committed (context manager closed, file flushed) BEFORE turn *N+1* even
    starts. ``crash-test.sh`` starts this op in the background, waits for
    the stderr ``PROGRESS: ... turn 1/N checkpointed`` line (proof turn 1's
    checkpoint already landed on disk), then ``kill -9``s the process while
    it is blocked on turn 2's real agnes call — before turn 2's checkpoint
    exists and before this function ever reaches its own ``print()`` of a
    result envelope. A killed process here emits ZERO stdout by design; that
    silence (the caller's stdout capture file stays empty) is itself part of
    the crash proof asserted by ``test-crash.sh``.

``op == "resume"`` (process B — the fresh, un-killed one; also the default,
so a plain ``{"mode":"crash_recovery","thread_id":...,"messages":[...]}``
request behaves like ``memory`` mode)
    A brand-new process opens the SAME on-disk checkpoint file for the SAME
    ``thread_id`` and answers one new turn. It has no in-memory knowledge of
    process A's conversation whatsoever — any recall of the turn-1 fact
    comes exclusively from what ``SqliteSaver`` durably persisted before the
    kill.

Contract addition for this mode only: the request JSON MUST include a
non-empty string ``"thread_id"``. ``op="converse"`` also requires a
non-empty list of strings ``"turns"``; ``op="resume"`` (default) requires
``"messages"`` exactly like ``memory`` mode.
"""
from __future__ import annotations

import os
import time
import traceback
from typing import Any

from . import common

MODE = "crash_recovery"


def _build_graph(model_name: str, api_key: str, base_url: str):
    from langgraph.graph import END, START, MessagesState, StateGraph

    llm = common.build_chat_model(model_name, api_key, base_url)

    def call_model(state: MessagesState) -> dict[str, Any]:
        return {"messages": [llm.invoke(state["messages"])]}

    graph_builder = StateGraph(MessagesState)
    graph_builder.add_node("agent", call_model)
    graph_builder.add_edge(START, "agent")
    graph_builder.add_edge("agent", END)
    return graph_builder


def _reply_of(ai_message: Any) -> str:
    content = ai_message.content
    return content if isinstance(content, str) else str(content)


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    thread_id = payload.get("thread_id")
    if not thread_id or not isinstance(thread_id, str):
        return common.envelope(
            mode=MODE, model=model_name, error="crash_recovery mode requires a non-empty string 'thread_id' field"
        )

    op = payload.get("op") or "resume"
    if op not in ("converse", "resume"):
        return common.envelope(
            mode=MODE, model=model_name, error=f"crash_recovery mode: unknown op {op!r}; supported: converse, resume"
        )

    db_path = payload.get("checkpoint_db_path") or common.DEFAULT_CRASH_CHECKPOINT_DB
    # ARENA_STUB=1 uses an in-memory checkpointer and never touches disk —
    # see common.checkpointer_context()'s docstring. Note: the whole point
    # of this mode (surviving a real kill -9) does NOT hold under the stub,
    # since in-memory state cannot survive a killed process; the stub path
    # exists purely so this mode's code runs end-to-end offline (e.g. for
    # import/smoke checks), not to simulate crash durability.
    if not common.is_stub():
        os.makedirs(os.path.dirname(db_path), exist_ok=True)

    try:
        graph_builder = _build_graph(model_name, api_key, base_url)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"failed to build graph: {exc}")

    if op == "converse":
        return _run_converse(graph_builder, db_path, thread_id, payload, model_name)
    return _run_resume(graph_builder, db_path, thread_id, payload, model_name)


def _run_converse(graph_builder, db_path: str, thread_id: str, payload: dict[str, Any], model_name: str) -> dict[str, Any]:
    from langchain_core.messages import HumanMessage

    turns = payload.get("turns")
    if not turns or not isinstance(turns, list) or not all(isinstance(t, str) and t for t in turns):
        return common.envelope(
            mode=MODE,
            model=model_name,
            error="crash_recovery mode op='converse' requires a non-empty list of non-empty strings 'turns'",
        )

    try:
        start = time.monotonic()
        last_reply = ""
        total_in = 0
        total_out = 0
        saw_usage = False
        for i, turn_text in enumerate(turns, start=1):
            # Each turn gets its OWN SqliteSaver context manager: the
            # checkpoint for turn i is durably flushed and the connection
            # closed BEFORE turn i+1's real agnes call even begins. This is
            # what makes "killed after turn 1's checkpoint, mid turn 2's
            # network call" a real, verifiable boundary rather than a
            # best-effort guess about timing.
            with common.checkpointer_context(db_path) as saver:
                graph = graph_builder.compile(checkpointer=saver)
                result = graph.invoke(
                    {"messages": [HumanMessage(content=turn_text)]},
                    config={"configurable": {"thread_id": thread_id}},
                )
            ai_message = result["messages"][-1]
            last_reply = _reply_of(ai_message)
            inp, out = common.extract_usage(ai_message)
            if inp >= 0 and out >= 0:
                total_in += inp
                total_out += out
                saw_usage = True
            # This PROGRESS line is the synchronization signal test-crash.sh
            # polls for on stderr: it proves turn i's checkpoint is already
            # durably on disk (the `with` block above has already exited)
            # before this process is allowed to proceed to turn i+1.
            common.log(f"PROGRESS: crash_recovery converse turn {i}/{len(turns)} checkpointed reply={last_reply[:80]!r}")
        latency_ms = int((time.monotonic() - start) * 1000)
        input_tokens, output_tokens = (total_in, total_out) if saw_usage else (-1, -1)
        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=last_reply,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            latency_ms=latency_ms,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")


def _run_resume(graph_builder, db_path: str, thread_id: str, payload: dict[str, Any], model_name: str) -> dict[str, Any]:
    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")
    if not messages:
        return common.envelope(mode=MODE, model=model_name, error="no messages in request")

    try:
        start = time.monotonic()
        # Fresh process, fresh SqliteSaver, SAME on-disk file + thread_id.
        # This process has no idea what happened before it — any recall it
        # produces comes exclusively from what was durably on disk when this
        # `with` block opened the file.
        with common.checkpointer_context(db_path) as saver:
            graph = graph_builder.compile(checkpointer=saver)
            result = graph.invoke(
                {"messages": messages},
                config={"configurable": {"thread_id": thread_id}},
            )
        latency_ms = int((time.monotonic() - start) * 1000)

        ai_message = result["messages"][-1]
        reply = _reply_of(ai_message)
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
