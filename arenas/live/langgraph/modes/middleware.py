"""``middleware`` mode — real LangGraph/LangChain-native "model middleware"
options, exercising TWO independently observable transforms around one real
(or ``ARENA_STUB=1`` offline) chat-model turn: an INBOUND system-prompt
mutation and an OUTBOUND invocation counter.

This mode does **not** return the shared multi-mode envelope
(``modes.common.envelope``) — like ``observability`` mode, it is a
diagnostic/report mode, not a chat turn:

.. code-block:: json

    {
      "framework": "langgraph",
      "mode": "middleware",
      "text": "<answer>",
      "middleware": {
        "mechanism": "pre_model_hook",
        "inbound_fired": true,
        "outbound_fired": true,
        "invocations": 1,
        "supported": true
      },
      "error": ""
    }

Contract: request shape is identical to ``qa`` mode's base contract
(``"system"``, ``"messages"``) — no new required fields.

Three LangChain/LangGraph-native "model middleware" mechanisms exist; this
mode's module docstring documents all three so the choice made here is
verifiable, not asserted:

(a) A ``RunnableLambda`` wrapper (LCEL composition) around the chat model —
    CAN mutate the outbound prompt, since a ``Runnable`` transforms its own
    input before delegating to the wrapped model.
(b) ``langchain_core.callbacks.BaseCallbackHandler`` — OBSERVE-ONLY.
    ``on_chat_model_start(serialized, messages, **kwargs)`` receives the
    messages the model is about to see, but LangChain never reads a
    callback method's return value to rewrite the call — verified directly
    against the pinned ``langchain-core==1.4.9`` source
    (``BaseCallbackManager``/``CallbackManagerForLLMRun`` treat every
    ``on_*`` hook's return value as discarded telemetry). Honest
    conclusion: callbacks CANNOT do inbound mutation. They are used below,
    correctly, for OUTBOUND observation only.
(c) ``langgraph.prebuilt.create_react_agent``'s ``pre_model_hook``
    parameter — a real graph node inserted before the ``agent`` node,
    contractually returning ``{"llm_input_messages": [...]}`` (confirmed
    directly against this arena's pinned ``langgraph==1.2.9`` /
    ``langgraph-prebuilt==1.1.0`` installed source,
    ``langgraph/prebuilt/chat_agent_executor.py`` lines ~396-424), which
    supplies the model's ACTUAL input for that turn WITHOUT overwriting the
    graph's persisted ``"messages"`` state — a genuine, framework-native
    inbound mutation seam, not a hand-rolled workaround.

This mode uses **(c) for INBOUND** (``mechanism: "pre_model_hook"``) — of
the three, it is the only one that genuinely rewrites what the model sees,
and it is a first-class LangGraph-native primitive. ``create_react_agent``
itself is deprecated since LangGraph v1.0 in favor of
``langchain.agents.create_agent`` (a runtime ``LangGraphDeprecatedSinceV10``
warning, suppressed from stdout/stderr noise below but never hidden from
this docstring) — but that successor lives in the separate top-level
``langchain`` package, which is confirmed NOT installed in this arena's
pinned ``.venv`` (only ``langchain-core``/``langchain-openai``/``langgraph``
are pinned in ``requirements.txt``; ``python -c "import langchain"`` fails).
``create_react_agent``'s ``pre_model_hook`` is therefore the mechanism
actually available and used here — fully functional against the pinned
version, deprecation notwithstanding.

For **OUTBOUND**, a ``BaseCallbackHandler.on_chat_model_start`` override
counts real chat-model invocations — a correct, fully-supported use of
LangChain's observe-only callback system (see (b) above), attached via
``config={"callbacks": [...]}`` on ``.invoke(...)`` with zero change to the
graph/agent under test.

``ARENA_STUB=1``: routes through ``common.build_chat_model`` like every
other mode — a real ``FakeListChatModel`` stands in for ``ChatOpenAI``, so
this mode's mechanism wiring (both transforms) is exercised with zero
network calls; only the model's own text differs (canned vs real).
"""
from __future__ import annotations

import time
import traceback
import warnings
from typing import Any

from . import common

MODE = "middleware"

# Inbound sentinel appended to the system prompt (module docstring, mechanism (c)).
_SENTINEL = "\n[ARENA_MW]"


def _report(
    *,
    text: str,
    mechanism: str,
    inbound_fired: bool,
    outbound_fired: bool,
    invocations: int,
    supported: bool,
    error: str = "",
) -> dict[str, Any]:
    return {
        "framework": common.FRAMEWORK,
        "mode": MODE,
        "text": text,
        "middleware": {
            "mechanism": mechanism,
            "inbound_fired": inbound_fired,
            "outbound_fired": outbound_fired,
            "invocations": invocations,
            "supported": supported,
        },
        "error": error,
    }


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return _report(
            text="",
            mechanism="pre_model_hook",
            inbound_fired=False,
            outbound_fired=False,
            invocations=0,
            supported=False,
            error=err.get("error", "missing chat env"),
        )

    try:
        from langchain_core.callbacks import BaseCallbackHandler
        from langchain_core.messages import SystemMessage
        from langgraph.prebuilt import create_react_agent
    except Exception as exc:  # pragma: no cover - import failure path
        return _report(
            text="",
            mechanism="pre_model_hook",
            inbound_fired=False,
            outbound_fired=False,
            invocations=0,
            supported=False,
            error=f"import failure: {exc}",
        )

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return _report(
            text="",
            mechanism="pre_model_hook",
            inbound_fired=False,
            outbound_fired=False,
            invocations=0,
            supported=False,
            error=f"invalid request payload: {exc}",
        )
    if not messages:
        return _report(
            text="",
            mechanism="pre_model_hook",
            inbound_fired=False,
            outbound_fired=False,
            invocations=0,
            supported=False,
            error="no messages in request",
        )

    hook_state = {"inbound_fired": False}

    def _pre_model_hook(state: dict[str, Any]) -> dict[str, Any]:
        """INBOUND transform: appends ``_SENTINEL`` to the system message
        (synthesizing one if the request had none), returned as
        ``llm_input_messages`` so the model's ACTUAL input for this turn is
        mutated WITHOUT overwriting the graph's persisted ``"messages"``
        state — see module docstring, mechanism (c).
        """
        current = state["messages"]
        rewritten: list[Any] = []
        seen_system = False
        for m in current:
            if isinstance(m, SystemMessage):
                rewritten.append(SystemMessage(content=str(m.content) + _SENTINEL))
                seen_system = True
            else:
                rewritten.append(m)
        if not seen_system:
            rewritten = [SystemMessage(content=_SENTINEL.strip())] + rewritten
        hook_state["inbound_fired"] = True
        return {"llm_input_messages": rewritten}

    class _CountingHandler(BaseCallbackHandler):
        """OUTBOUND transform: counts real chat-model invocations via
        ``on_chat_model_start`` — observe-only (module docstring, mechanism
        (b)); never mutates the prompt, only counts.
        """

        def __init__(self) -> None:
            self.invocations = 0

        def on_chat_model_start(self, serialized: Any, messages: Any, **kwargs: Any) -> None:  # noqa: ARG002
            self.invocations += 1

    counter = _CountingHandler()

    try:
        llm = common.build_chat_model(model_name, api_key, base_url)

        with warnings.catch_warnings():
            # create_react_agent is deprecated since LangGraph v1.0 in favor
            # of langchain.agents.create_agent — see module docstring for
            # why that successor is not available in this arena's pinned
            # dependency set. Suppressed here so it never lands on
            # stdout/stderr as noise; the deprecation is documented above,
            # not hidden, and pre_model_hook remains fully functional
            # against the pinned langgraph==1.2.9 / langgraph-prebuilt==1.1.0.
            warnings.simplefilter("ignore")
            agent = create_react_agent(model=llm, tools=[], pre_model_hook=_pre_model_hook)

            start = time.monotonic()
            result = agent.invoke(
                {"messages": messages},
                config={"callbacks": [counter]},
            )
            latency_ms = int((time.monotonic() - start) * 1000)

        ai_message = result["messages"][-1]
        content = ai_message.content
        text = content if isinstance(content, str) else str(content)

        outbound_fired = counter.invocations > 0
        common.log(
            f"PROGRESS: middleware turn done inbound_fired={hook_state['inbound_fired']} "
            f"outbound_fired={outbound_fired} invocations={counter.invocations} latency_ms={latency_ms}"
        )

        return _report(
            text=text,
            mechanism="pre_model_hook",
            inbound_fired=hook_state["inbound_fired"],
            outbound_fired=outbound_fired,
            invocations=counter.invocations,
            supported=True,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return _report(
            text="",
            mechanism="pre_model_hook",
            inbound_fired=hook_state["inbound_fired"],
            outbound_fired=counter.invocations > 0,
            invocations=counter.invocations,
            supported=False,
            error=f"{type(exc).__name__}: {exc}",
        )
