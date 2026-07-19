"""Shared helpers for every mode of the LangGraph live-arena adapter.

Security: never print, log, or persist the API key. All diagnostics go to
stderr; stdout carries exactly one JSON object and nothing else (enforced by
``adapter.py``, not by this module).
"""
from __future__ import annotations

import contextlib
import os
import sys
from typing import Any

FRAMEWORK = "langgraph"
DEFAULT_MODEL = "agnes-2.0-flash"

# arenas/live/langgraph/ (parent of this modes/ package directory).
ADAPTER_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Real infra defaults, overridable so Docker can point at
# host.docker.internal instead of the host-local loopback addresses.
DEFAULT_EMBED_URL = os.environ.get("EMBED_URL", "http://127.0.0.1:8899")
DEFAULT_QDRANT_URL = os.environ.get("QDRANT_URL", "http://localhost:6333")
DEFAULT_QDRANT_COLLECTION = os.environ.get("QDRANT_COLLECTION", "arena_rag")
DEFAULT_EMBED_MODEL_QUERY = os.environ.get(
    "EMBED_MODEL_QUERY", "multilingual-e5-small-query"
)
# The embed server (arenas/live/_shared/embed-server) does not check
# authentication, but langchain_openai.OpenAIEmbeddings requires a non-empty
# key string to construct its client. This is never a real credential.
DEFAULT_EMBED_API_KEY = os.environ.get("EMBED_API_KEY", "not-needed")
DEFAULT_CHECKPOINT_DB = os.environ.get(
    "CHECKPOINT_DB_PATH", os.path.join(ADAPTER_DIR, ".data", "checkpoints.sqlite")
)
# crash_recovery mode: a SEPARATE on-disk checkpoint file from `memory`
# mode's above, so a hard-crash test never clobbers (or is clobbered by)
# memory mode's own checkpoint history.
DEFAULT_CRASH_CHECKPOINT_DB = os.environ.get(
    "CRASH_CHECKPOINT_DB_PATH", os.path.join(ADAPTER_DIR, ".data", "checkpoints_crash.sqlite")
)
# Hand-rolled compound-scope fact store (memory_scoped / isolation_leak
# modes) — see modes/scoped_store.py. A separate on-disk file from the
# LangGraph checkpointer above: this store has nothing to do with
# LangGraph's own persistence, it is adapter-owned.
DEFAULT_SCOPED_STORE_DB = os.environ.get(
    "SCOPED_STORE_DB_PATH", os.path.join(ADAPTER_DIR, ".data", "scoped_store.sqlite")
)

# Canned offline reply used by the stub chat model (ARENA_STUB=1, see
# is_stub()/build_chat_model() below) when a mode does not supply its own
# mode-specific responses list.
_DEFAULT_STUB_RESPONSES = ["OK (ARENA_STUB offline stub reply)"]


def is_stub() -> bool:
    """True when ``ARENA_STUB=1`` (or any other truthy value) is set.

    When true, every mode MUST avoid all real network calls: no real
    ``ChatOpenAI``/agnes call, no real Qdrant/embed-server HTTP call, and no
    on-disk LangGraph checkpointer file — see ``build_chat_model()`` and
    ``checkpointer_context()`` below, which are the two central seams every
    mode routes through to honor this.
    """
    return os.environ.get("ARENA_STUB", "").strip().lower() in ("1", "true", "yes")


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def envelope(
    *,
    mode: str,
    model: str,
    reply: str = "",
    input_tokens: int = -1,
    output_tokens: int = -1,
    latency_ms: int = 0,
    tool_calls: list[dict[str, Any]] | None = None,
    retrieved: list[dict[str, Any]] | None = None,
    structured: Any = None,
    ttft_ms: int = -1,
    scoped_recall: dict[str, Any] | None = None,
    leak_detected: bool | None = None,
    error: str = "",
) -> dict[str, Any]:
    """Build the full multi-mode response envelope.

    ``retrieved``/``structured``/``ttft_ms`` default to the "not applicable
    to this mode" sentinel (``[]``/``None``/``-1``) rather than being
    omitted, so every mode emits the same key set. ``scoped_recall`` and
    ``leak_detected`` are additive fields for the ``memory_scoped`` and
    ``isolation_leak`` modes respectively; they default to ``None`` for
    every other mode (same "not applicable" convention).
    """
    return {
        "framework": FRAMEWORK,
        "mode": mode,
        "model": model,
        "reply": reply,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "latency_ms": latency_ms,
        "tool_calls": tool_calls if tool_calls is not None else [],
        "retrieved": retrieved if retrieved is not None else [],
        "structured": structured,
        "ttft_ms": ttft_ms,
        "scoped_recall": scoped_recall,
        "leak_detected": leak_detected,
        "error": error,
    }


def resolve_chat_env(mode: str) -> tuple[str, str | None, str | None, dict[str, Any] | None]:
    """Resolve OPENAI_* chat env vars.

    Returns ``(model_name, api_key, base_url, error_envelope)``. When a
    required var is missing, ``error_envelope`` is a ready-to-return
    envelope dict and the caller should return it immediately.

    Under ``ARENA_STUB=1`` (see ``is_stub()``), the real ``OPENAI_API_KEY``/
    ``OPENAI_BASE_URL`` are never required — every mode must be runnable with
    zero real credentials and zero network access. ``build_chat_model()``
    below is what actually swaps in the offline stand-in; this function just
    stops gating on credentials that are about to go unused.
    """
    model_name = os.environ.get("OPENAI_MODEL", DEFAULT_MODEL)
    if is_stub():
        # Report the OFFLINE stub model (FakeListChatModel), never the real
        # OPENAI_MODEL — a stub run must be self-evidently offline in its output
        # (the runner's lane-leak guard reads the model field). run.sh sets
        # OPENAI_MODEL even for a stub run, so this must override it, not echo it.
        return "FakeListChatModel (ARENA_STUB)", "stub-key-not-a-real-credential", "http://arena-stub.invalid", None
    api_key = os.environ.get("OPENAI_API_KEY")
    base_url = os.environ.get("OPENAI_BASE_URL")
    if not api_key:
        return model_name, None, None, envelope(
            mode=mode, model=model_name, error="OPENAI_API_KEY is not set"
        )
    if not base_url:
        return model_name, None, None, envelope(
            mode=mode, model=model_name, error="OPENAI_BASE_URL is not set"
        )
    return model_name, api_key, base_url, None


def build_chat_model(
    model_name: str,
    api_key: str | None,
    base_url: str | None,
    *,
    responses: list[str] | None = None,
    **chat_openai_kwargs: Any,
) -> Any:
    """Construct the chat model every mode calls the LLM through.

    Real path (default): ``langchain_openai.ChatOpenAI`` against the real
    agnes-compatible endpoint, unchanged from every mode's prior direct
    ``ChatOpenAI(...)`` construction.

    Stub path (``ARENA_STUB=1``, see ``is_stub()``): a REAL LangChain-native
    fake chat model —
    ``langchain_core.language_models.fake_chat_models.FakeListChatModel``
    (cycles through a fixed list of canned string responses; documented at
    https://python.langchain.com/api_reference/core/language_models/langchain_core.language_models.fake_chat_models.FakeListChatModel.html)
    — never a hand-rolled mock class. It is subclassed here to override
    ``bind_tools()`` (``FakeListChatModel`` inherits ``BaseChatModel``'s
    default ``bind_tools()``, which raises ``NotImplementedError`` — verified
    against this arena's pinned ``langchain-core==1.4.9``) and ``_generate()``.

    Tool-calling scope note (mirrors the Eino adapter's ``stub.go`` and the
    Mastra adapter's ``lib/stub-model.mjs`` — same approach, same reason):
    when tools are bound and the input messages do NOT yet contain a
    ``ToolMessage`` (i.e. no tool has been executed for this turn yet), the
    stub emits a CANNED ``tool_calls`` entry for the first bound tool with a
    fixed, genuinely-evaluable expression (``"(482 * 17) + 391"`` for a tool
    named ``calculate``, empty args otherwise) — this is what lets
    ``modes.tools``' and ``modes.observability``'s real LangGraph
    ``ToolNode``/``tools_condition`` ReAct loop actually execute the real
    tool offline, instead of the loop never entering the tools branch at
    all. Once a ``ToolMessage`` IS present (the real tool already ran and
    its result was fed back), the stub falls back to its plain canned text
    reply so the loop terminates via ``tools_condition`` -> END exactly like
    the real model would after answering from the tool result. The stub
    never simulates real tool-call REASONING (it does not read the
    expression/arguments the caller asked for) — only the DECISION to call
    the tool and the loop's terminating shape are canned; the tool itself
    (and its execution) is always genuine. This is documented, not a
    fabricated real answer.
    """
    if is_stub():
        from langchain_core.language_models.fake_chat_models import FakeListChatModel
        from pydantic import PrivateAttr

        class _StubChatModel(FakeListChatModel):
            """FakeListChatModel + tool-call-aware bind_tools()/_generate().

            See build_chat_model()'s docstring above for why this override
            exists and what it does (and does not) simulate.
            """

            _stub_tools: list[Any] = PrivateAttr(default_factory=list)

            def bind_tools(self, tools: Any, **kwargs: Any) -> "_StubChatModel":  # noqa: ARG002
                self._stub_tools = list(tools)
                return self

            @staticmethod
            def _tool_name(tool: Any) -> str:
                if isinstance(tool, dict):
                    fn = tool.get("function") or {}
                    return fn.get("name") or tool.get("name") or "calculate"
                return getattr(tool, "name", None) or "calculate"

            def _generate(  # type: ignore[override]
                self,
                messages: Any,
                stop: list[str] | None = None,
                run_manager: Any = None,
                **kwargs: Any,
            ) -> Any:
                if self._stub_tools:
                    from langchain_core.messages import ToolMessage

                    already_ran = any(isinstance(m, ToolMessage) for m in messages)
                    if not already_ran:
                        from langchain_core.messages import AIMessage
                        from langchain_core.outputs import ChatGeneration, ChatResult

                        name = self._tool_name(self._stub_tools[0])
                        args = {"expression": "(482 * 17) + 391"} if name == "calculate" else {}
                        ai_msg = AIMessage(
                            content="",
                            tool_calls=[
                                {"name": name, "args": args, "id": "arena-stub-call-1", "type": "tool_call"}
                            ],
                        )
                        return ChatResult(generations=[ChatGeneration(message=ai_msg)])
                return super()._generate(messages, stop=stop, run_manager=run_manager, **kwargs)

        return _StubChatModel(responses=list(responses or _DEFAULT_STUB_RESPONSES))

    from langchain_openai import ChatOpenAI

    return ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key, **chat_openai_kwargs)


@contextlib.contextmanager
def checkpointer_context(db_path: str):
    """Yield the LangGraph checkpointer every persistence mode compiles with.

    Real path (default): the real on-disk
    ``langgraph.checkpoint.sqlite.SqliteSaver`` against ``db_path``, same
    context-manager usage every persistence mode already had inline.

    Stub path (``ARENA_STUB=1``): ``langgraph.checkpoint.memory.InMemorySaver``
    — LangGraph's own real in-memory checkpointer implementation (not a
    hand-rolled dict), so no ``.data/*.sqlite`` file is created or touched.
    This intentionally does NOT preserve state across separate OS processes
    (that is the entire point of ``memory``/``crash_recovery`` modes in the
    real path) — under the stub it only proves the mode's own code path
    runs end-to-end offline, not cross-process persistence.
    """
    if is_stub():
        from langgraph.checkpoint.memory import InMemorySaver

        yield InMemorySaver()
        return

    from langgraph.checkpoint.sqlite import SqliteSaver

    with SqliteSaver.from_conn_string(db_path) as saver:
        yield saver


def to_openai_tool(tool: dict[str, Any]) -> dict[str, Any]:
    """Convert one arena-contract tool spec into an OpenAI-style tool dict.

    LangChain's ``bind_tools()`` accepts dicts already shaped like the
    OpenAI function-calling schema and passes them through
    ``convert_to_openai_tool`` largely unchanged when wrapped in
    ``{"type": "function", "function": {...}}``.
    """
    return {
        "type": "function",
        "function": {
            "name": tool["name"],
            "description": tool.get("description", ""),
            "parameters": tool.get("parameters") or {"type": "object", "properties": {}},
        },
    }


def build_messages(payload: dict[str, Any]):
    """Translate the arena stdin contract's system/messages into LC messages."""
    from langchain_core.messages import AIMessage, HumanMessage, SystemMessage

    messages = []
    system = payload.get("system")
    if system:
        messages.append(SystemMessage(content=system))
    for m in payload.get("messages", []):
        role = m.get("role")
        content = m.get("content", "")
        if role == "user":
            messages.append(HumanMessage(content=content))
        elif role == "assistant":
            messages.append(AIMessage(content=content))
        else:
            raise ValueError(f"unsupported message role: {role!r}")
    return messages


def last_user_content(payload: dict[str, Any]) -> str:
    """The final user message's content — "the one to answer" per SPEC.md."""
    for m in reversed(payload.get("messages", [])):
        if m.get("role") == "user":
            return m.get("content", "")
    return ""


def extract_usage(ai_message: Any) -> tuple[int, int]:
    """Pull real provider token usage off the AIMessage.

    Prefers the LangChain-standardized ``usage_metadata`` (input_tokens /
    output_tokens), falls back to the raw OpenAI-compatible
    ``response_metadata["token_usage"]`` (prompt_tokens / completion_tokens).
    Returns (-1, -1) if neither is present rather than fabricating a number.
    """
    usage = getattr(ai_message, "usage_metadata", None)
    if usage:
        inp = usage.get("input_tokens")
        out = usage.get("output_tokens")
        if isinstance(inp, int) and isinstance(out, int):
            return inp, out

    meta = getattr(ai_message, "response_metadata", None) or {}
    token_usage = meta.get("token_usage") or {}
    inp = token_usage.get("prompt_tokens")
    out = token_usage.get("completion_tokens")
    if isinstance(inp, int) and isinstance(out, int):
        return inp, out

    return -1, -1


def sum_usage(ai_messages: list[Any]) -> tuple[int, int]:
    """Sum provider usage across multiple AIMessages (multi-call modes).

    Multi-step modes (tools' agent<->tools loop) make more than one real
    model call; each carries its own provider usage. Sums whatever usage was
    actually reported and returns (-1, -1) only if none of the calls
    reported usage at all — never fabricates a partial total silently.
    """
    total_in = 0
    total_out = 0
    saw_any = False
    for m in ai_messages:
        inp, out = extract_usage(m)
        if inp >= 0 and out >= 0:
            total_in += inp
            total_out += out
            saw_any = True
    if not saw_any:
        return -1, -1
    return total_in, total_out


def extract_tool_calls(ai_message: Any) -> list[dict[str, Any]]:
    calls = getattr(ai_message, "tool_calls", None) or []
    result = []
    for call in calls:
        if isinstance(call, dict):
            name = call.get("name", "")
            args = call.get("args", {})
        else:  # pragma: no cover - defensive, LC normally yields dicts
            name = getattr(call, "name", "")
            args = getattr(call, "args", {})
        result.append({"name": name, "arguments": args if isinstance(args, dict) else {}})
    return result
