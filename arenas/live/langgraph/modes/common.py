"""Shared helpers for every mode of the LangGraph live-arena adapter.

Security: never print, log, or persist the API key. All diagnostics go to
stderr; stdout carries exactly one JSON object and nothing else (enforced by
``adapter.py``, not by this module).
"""
from __future__ import annotations

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
    error: str = "",
) -> dict[str, Any]:
    """Build the full multi-mode response envelope.

    ``retrieved``/``structured``/``ttft_ms`` default to the "not applicable
    to this mode" sentinel (``[]``/``None``/``-1``) rather than being
    omitted, so every mode emits the same key set.
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
        "error": error,
    }


def resolve_chat_env(mode: str) -> tuple[str, str | None, str | None, dict[str, Any] | None]:
    """Resolve OPENAI_* chat env vars.

    Returns ``(model_name, api_key, base_url, error_envelope)``. When a
    required var is missing, ``error_envelope`` is a ready-to-return
    envelope dict and the caller should return it immediately.
    """
    model_name = os.environ.get("OPENAI_MODEL", DEFAULT_MODEL)
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
