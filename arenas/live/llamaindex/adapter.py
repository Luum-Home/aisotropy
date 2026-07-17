#!/usr/bin/env python3
"""LlamaIndex live-arena adapter — EXHAUSTIVE multi-mode.

Competes using LlamaIndex's NATIVE features against REAL infrastructure: agnes-2.0-flash
(OpenAI-compatible chat), a real e5 embeddings server, a real Qdrant collection (`arena_rag`,
22 Nimbus-Rail docs), and a real on-disk SQLite store for cross-process memory. See README.md
for the full source-grounded wiring trail per mode.

Contract
--------
Reads ONE JSON object from stdin:
    {
      "mode": "qa"|"rag"|"tools"|"memory"|"structured"|"stream",   # optional, defaults "qa"
      "system": str,
      "messages": [{"role": "user"|"assistant", "content": str}, ...],
      "tools": [{"name": str, "description": str, "parameters": <JSON Schema>}, ...],  # qa only
      "session_id": str   # memory mode only — same id across two invocations to test recall
    }

Writes ONE JSON object to stdout — and ONLY that; all diagnostics go to stderr (PROGRESS
markers, tracebacks):
    {
      "framework": "llamaindex",
      "mode": str,
      "model": str,
      "reply": str,
      "input_tokens": int,
      "output_tokens": int,
      "latency_ms": int,
      "tool_calls": [{"name": str, "arguments": object, "result": str}, ...],
      "retrieved": [str, ...],
      "structured": object | null,
      "ttft_ms": int,
      "error": str
    }

Modes (see README.md for the exact LlamaIndex classes/source lines backing each one):
  qa         Single-shot chat turn. Native tool_calls are captured, NOT executed (unchanged
             from the original single-shot adapter — this is the runner's default mode).
  rag        Real retrieval: `QdrantVectorStore` + `VectorStoreIndex` + `RetrieverQueryEngine`
             against the real `arena_rag` Qdrant collection, embedding queries through the real
             e5 server. System instruction forces "answer only from context, else I don't know".
  tools      `FunctionAgent` multi-step loop that ACTUALLY EXECUTES a real `calculate` and/or
             `corpus_lookup` tool (not single-shot capture) and uses the result in the answer.
  memory     Persists chat turns to a REAL on-disk SQLite store (`llama_index.core.memory.Memory`,
             SQLAlchemy-backed) keyed by `session_id`, so a later, separate process invocation
             with the same `session_id` recalls facts from an earlier one.
  structured `llm.structured_predict()` (LlamaIndex's native structured-prediction /
             PydanticProgram path) producing a validated typed object.
  stream     Real token streaming via `llm.stream_chat()`; records time-to-first-token.

Token usage is captured uniformly across every mode via a `BaseCallbackHandler` attached to the
single shared `OpenAILike` instance: it reads the provider's raw `usage` object off every
`CBEventType.LLM` event the LLM emits (`ChatResponse.raw.usage` — the same field the original
single-shot adapter read directly). If a call reports no usage, tokens stay `-1` — never
fabricated. Modes that make more than one LLM call internally (e.g. `tools`) get the SUM of
usage across those calls, which is the mode's real cost.
"""
from __future__ import annotations

import ast
import asyncio
import json
import operator
import os
import sys
import time
import traceback
from pathlib import Path
from typing import Any, Callable


FRAMEWORK = "llamaindex"
SCRIPT_DIR = Path(__file__).resolve().parent

# Env-configurable host targets (ADR-none; local convention for this arena) — defaults match
# arenas/live/_shared, overridable so the Docker variant can point at host.docker.internal.
EMBED_URL = os.environ.get("EMBED_URL", "http://127.0.0.1:8899").rstrip("/")
QDRANT_URL = os.environ.get("QDRANT_URL", "http://localhost:6333").rstrip("/")
ARENA_COLLECTION = os.environ.get("ARENA_COLLECTION", "arena_rag")
EMBED_QUERY_MODEL = "multilingual-e5-small-query"  # "-query" suffix -> e5 query: prefix

MEMORY_DIR = Path(os.environ.get("LLAMAINDEX_MEMORY_DIR", str(SCRIPT_DIR / ".memory")))

VALID_MODES = {"qa", "rag", "tools", "memory", "structured", "stream"}


# --------------------------------------------------------------------------------------
# Envelope helpers
# --------------------------------------------------------------------------------------


def _progress(message: str) -> None:
    print(f"PROGRESS: {message}", file=sys.stderr, flush=True)


def _emit(payload: dict[str, Any]) -> None:
    """Write the uniform envelope to stdout — and ONLY the envelope."""
    sys.stdout.write(json.dumps(payload))
    sys.stdout.write("\n")
    sys.stdout.flush()


def _envelope(mode: str, model: str, **overrides: Any) -> dict[str, Any]:
    base: dict[str, Any] = {
        "framework": FRAMEWORK,
        "mode": mode,
        "model": model,
        "reply": "",
        "input_tokens": -1,
        "output_tokens": -1,
        "latency_ms": -1,
        "tool_calls": [],
        "retrieved": [],
        "structured": None,
        "ttft_ms": -1,
        "error": "",
    }
    base.update(overrides)
    return base


def _fail(mode: str, message: str, model: str = "") -> None:
    _progress(f"ERROR: {message}")
    _emit(_envelope(mode, model, error=message))
    sys.exit(1)


def _read_request() -> dict[str, Any]:
    raw = sys.stdin.read()
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError as exc:
        _fail("unknown", f"invalid JSON on stdin: {exc}")
        raise  # unreachable, keeps type-checkers happy
    if not isinstance(payload, dict):
        _fail("unknown", "stdin JSON must be an object")
        raise
    return payload


# --------------------------------------------------------------------------------------
# Shared chat-message / usage-capture plumbing (used by qa + stream)
# --------------------------------------------------------------------------------------


def _build_chat_messages(system_prompt: str, messages_in: list[dict[str, Any]]) -> list[Any]:
    from llama_index.core.llms import ChatMessage, MessageRole

    role_map = {"user": MessageRole.USER, "assistant": MessageRole.ASSISTANT}
    chat_messages: list[Any] = []
    if system_prompt:
        chat_messages.append(ChatMessage(role=MessageRole.SYSTEM, content=system_prompt))
    for m in messages_in:
        role = role_map.get(m.get("role", "user"), MessageRole.USER)
        chat_messages.append(ChatMessage(role=role, content=m.get("content", "")))
    return chat_messages


def _last_user_message(payload: dict[str, Any]) -> str:
    for m in reversed(payload.get("messages") or []):
        if m.get("role") == "user":
            return m.get("content", "")
    return ""


def _extract_tool_calls(response: Any) -> list[dict[str, Any]]:
    """Single-shot capture: read native tool_calls off a ChatResponse, do NOT execute."""
    tool_calls: list[dict[str, Any]] = []
    try:
        from llama_index.core.base.llms.types import ToolCallBlock

        for block in response.message.blocks:
            if isinstance(block, ToolCallBlock):
                args = block.tool_kwargs
                if isinstance(args, str):
                    try:
                        args = json.loads(args) if args else {}
                    except json.JSONDecodeError:
                        args = {"_raw": args}
                tool_calls.append({"name": block.tool_name, "arguments": args})
    except Exception as exc:
        _progress(f"WARNING: tool_call parsing failed non-fatally: {exc}")
    return tool_calls


class _UsageCapture:
    """CallbackHandler that sums provider-reported usage across every LLM call this run makes.

    Wired onto the single shared `OpenAILike` instance's `callback_manager`, so every mode
    (qa/rag/tools/memory/structured/stream) gets accurate, provider-reported token counts with
    no per-mode bookkeeping — including modes that make more than one internal LLM call (e.g.
    `tools`, where the agent loop calls the model once to select the tool and again to answer).
    Never fabricates: if a response carries no usage, nothing is added for that call.
    """

    def __init__(self) -> None:
        self.input_tokens = -1
        self.output_tokens = -1

    def _record(self, response: Any) -> None:
        raw = getattr(response, "raw", None)
        raw_usage = getattr(raw, "usage", None) if raw is not None else None
        prompt_tokens = None
        completion_tokens = None
        if raw_usage is not None:
            prompt_tokens = getattr(raw_usage, "prompt_tokens", None)
            completion_tokens = getattr(raw_usage, "completion_tokens", None)
        elif getattr(response, "additional_kwargs", None):
            prompt_tokens = response.additional_kwargs.get("prompt_tokens")
            completion_tokens = response.additional_kwargs.get("completion_tokens")
        if prompt_tokens is not None:
            self.input_tokens = (self.input_tokens if self.input_tokens > 0 else 0) + prompt_tokens
        if completion_tokens is not None:
            self.output_tokens = (self.output_tokens if self.output_tokens > 0 else 0) + completion_tokens

    def build_callback_manager(self) -> Any:
        """Build a CallbackManager wired to this capture instance.

        NOTE: several LlamaIndex composed objects (`RetrieverQueryEngine.from_args`,
        response synthesizers) do NOT inherit the callback_manager off the `llm` instance
        they are given — they fall back to the process-global `Settings.callback_manager`
        when no explicit `callback_manager=` kwarg is passed (see
        `llama_index/core/query_engine/retriever_query_engine.py::from_args`:
        `callback_manager = callback_manager or Settings.callback_manager`). main() sets
        `Settings.callback_manager` to this SAME manager so every mode's internal LLM
        call — whatever component makes it — reports usage into this one capture object.
        """
        from llama_index.core.callbacks import CallbackManager, CBEventType, EventPayload
        from llama_index.core.callbacks.base_handler import BaseCallbackHandler

        capture = self

        class _Handler(BaseCallbackHandler):
            def __init__(self) -> None:
                super().__init__(event_starts_to_ignore=[], event_ends_to_ignore=[])

            def on_event_start(self, event_type, payload=None, event_id="", parent_id="", **kwargs):  # noqa: D401
                return event_id

            def on_event_end(self, event_type, payload=None, event_id="", **kwargs):
                if event_type == CBEventType.LLM and payload:
                    response = payload.get(EventPayload.RESPONSE)
                    if response is not None:
                        capture._record(response)

            def start_trace(self, trace_id=None):
                pass

            def end_trace(self, trace_id=None, trace_map=None):
                pass

        return CallbackManager([_Handler()])


# --------------------------------------------------------------------------------------
# Real, executable tools for `tools` mode (per arenas/live/_shared/README.md convention: no
# stubbed tool that returns a canned value — these actually run).
# --------------------------------------------------------------------------------------

_SAFE_OPS: dict[type, Callable[..., float]] = {
    ast.Add: operator.add,
    ast.Sub: operator.sub,
    ast.Mult: operator.mul,
    ast.Div: operator.truediv,
    ast.Pow: operator.pow,
    ast.Mod: operator.mod,
    ast.FloorDiv: operator.floordiv,
    ast.USub: operator.neg,
}


def _safe_eval_expr(expr: str) -> float:
    """AST-walk a restricted arithmetic grammar — no eval(), no builtins, no names."""

    def _ev(node: ast.AST) -> float:
        if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
            return node.value
        if isinstance(node, ast.BinOp) and type(node.op) in _SAFE_OPS:
            return _SAFE_OPS[type(node.op)](_ev(node.left), _ev(node.right))
        if isinstance(node, ast.UnaryOp) and type(node.op) in _SAFE_OPS:
            return _SAFE_OPS[type(node.op)](_ev(node.operand))
        raise ValueError(f"unsupported expression: {ast.dump(node)}")

    tree = ast.parse(expr, mode="eval")
    return _ev(tree.body)


def calculate(expr: str) -> str:
    """Evaluate a basic arithmetic expression (+ - * / ** % // and parentheses) and return the numeric result as a string."""
    try:
        return str(_safe_eval_expr(expr))
    except Exception as exc:
        return f"error: could not evaluate expression {expr!r}: {exc}"


def corpus_lookup(query: str) -> str:
    """Search the Nimbus Rail knowledge base for the given query and return the best-matching passage's title and full text. Use this for any question about Nimbus Rail trains, policies, staff, or routes."""
    from qdrant_client import QdrantClient
    from llama_index.core.vector_stores.types import VectorStoreQuery
    from llama_index.embeddings.openai_like import OpenAILikeEmbedding
    from llama_index.vector_stores.qdrant import QdrantVectorStore

    emb = OpenAILikeEmbedding(model_name=EMBED_QUERY_MODEL, api_base=EMBED_URL, api_key="none", timeout=30.0)
    client = QdrantClient(url=QDRANT_URL, check_compatibility=False)
    vs = QdrantVectorStore(client=client, collection_name=ARENA_COLLECTION)
    vector = emb.get_query_embedding(query)
    result = vs.query(VectorStoreQuery(query_embedding=vector, similarity_top_k=1))
    if not result.nodes:
        return "No matching passage found in the Nimbus Rail knowledge base."
    node = result.nodes[0]
    title = node.metadata.get("title", "unknown")
    return f"{title}: {node.get_content()}"


# --------------------------------------------------------------------------------------
# Mode implementations — each returns a dict of envelope overrides. input_tokens/output_tokens
# are filled in by main() from the shared _UsageCapture unless a mode sets them itself.
# --------------------------------------------------------------------------------------


def run_qa(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Single-shot chat turn — unchanged behavior from the original adapter."""
    system_prompt = payload.get("system") or ""
    messages_in = payload.get("messages") or []
    tools_in = payload.get("tools") or []

    chat_messages = _build_chat_messages(system_prompt, messages_in)

    chat_kwargs: dict[str, Any] = {}
    if tools_in:
        openai_tools = [
            {
                "type": "function",
                "function": {
                    "name": t["name"],
                    "description": t.get("description", ""),
                    "parameters": t.get("parameters", {"type": "object", "properties": {}}),
                },
            }
            for t in tools_in
        ]
        chat_kwargs["tools"] = openai_tools
        chat_kwargs["tool_choice"] = "auto"

    _progress(f"[qa] sending chat turn: {len(chat_messages)} messages, {len(tools_in)} tool(s) offered")
    start = time.monotonic()
    response = llm.chat(chat_messages, **chat_kwargs)
    latency_ms = int((time.monotonic() - start) * 1000)

    reply_text = response.message.content or ""
    tool_calls = _extract_tool_calls(response)

    return {"reply": reply_text, "latency_ms": latency_ms, "tool_calls": tool_calls}


def run_rag(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Real retrieval: QdrantVectorStore + VectorStoreIndex + RetrieverQueryEngine.

    Answers ONLY from the retrieved context (system instruction below); trap questions whose
    answer is not in the corpus must yield "I don't know", not an invented fact.
    """
    from llama_index.core import PromptTemplate, VectorStoreIndex
    from llama_index.core.query_engine import RetrieverQueryEngine
    from llama_index.embeddings.openai_like import OpenAILikeEmbedding
    from llama_index.vector_stores.qdrant import QdrantVectorStore
    from qdrant_client import QdrantClient

    question = _last_user_message(payload)
    if not question:
        raise RuntimeError("rag mode requires at least one user message")

    _progress(f"[rag] embedding query via {EMBED_URL} (model={EMBED_QUERY_MODEL})")
    embed_model = OpenAILikeEmbedding(model_name=EMBED_QUERY_MODEL, api_base=EMBED_URL, api_key="none", timeout=30.0)

    _progress(f"[rag] connecting to Qdrant at {QDRANT_URL} collection={ARENA_COLLECTION!r}")
    client = QdrantClient(url=QDRANT_URL, check_compatibility=False)
    vector_store = QdrantVectorStore(client=client, collection_name=ARENA_COLLECTION)
    index = VectorStoreIndex.from_vector_store(vector_store=vector_store, embed_model=embed_model)
    retriever = index.as_retriever(similarity_top_k=3)

    system_prompt = payload.get("system") or ""
    instruction = (
        "Answer ONLY from the retrieved context below. If the answer is not present in the "
        "context, respond exactly with: I don't know. Do not use outside knowledge."
    )
    qa_template = PromptTemplate(
        (system_prompt + "\n" if system_prompt else "")
        + instruction
        + "\n\nContext information is below.\n---------------------\n{context_str}\n"
        "---------------------\nQuestion: {query_str}\nAnswer: "
    )

    query_engine = RetrieverQueryEngine.from_args(retriever=retriever, llm=llm, text_qa_template=qa_template)

    _progress(f"[rag] querying: {question!r}")
    start = time.monotonic()
    response = query_engine.query(question)
    latency_ms = int((time.monotonic() - start) * 1000)

    retrieved = [
        sn.node.metadata.get("title") or sn.node.metadata.get("doc_id") or sn.node.node_id
        for sn in response.source_nodes
    ]
    _progress(f"[rag] retrieved {len(retrieved)} passage(s): {retrieved}")

    return {"reply": str(response), "retrieved": retrieved, "latency_ms": latency_ms}


def run_tools(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """FunctionAgent multi-step loop — the tool is ACTUALLY EXECUTED, result used in the answer."""
    from llama_index.core.agent.workflow import FunctionAgent

    question = _last_user_message(payload)
    if not question:
        raise RuntimeError("tools mode requires at least one user message")

    system_prompt = payload.get("system") or (
        "You are a helpful assistant with two tools: `calculate` for arithmetic and "
        "`corpus_lookup` for questions about Nimbus Rail. Always call the relevant tool before "
        "answering, then answer using the tool's real result."
    )

    agent = FunctionAgent(tools=[calculate, corpus_lookup], llm=llm, system_prompt=system_prompt)

    async def _run() -> tuple[str, list[dict[str, Any]]]:
        handler = agent.run(user_msg=question)
        tool_calls: list[dict[str, Any]] = []
        async for ev in handler.stream_events():
            if type(ev).__name__ == "ToolCallResult":
                _progress(f"[tools] executed {ev.tool_name}({dict(ev.tool_kwargs or {})}) -> {ev.tool_output!s}")
                tool_calls.append(
                    {
                        "name": ev.tool_name,
                        "arguments": dict(ev.tool_kwargs or {}),
                        "result": str(ev.tool_output),
                    }
                )
        result = await handler
        return str(result), tool_calls

    start = time.monotonic()
    reply_text, tool_calls = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)

    if not tool_calls:
        _progress("WARNING: [tools] agent answered without executing any tool")

    return {"reply": reply_text, "tool_calls": tool_calls, "latency_ms": latency_ms}


def run_memory(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Persist chat turns to a REAL on-disk SQLite store (llama_index.core.memory.Memory).

    Each invocation is one process: load history for `session_id` from disk, answer the new
    user turn with that history in context, persist both turns, exit. Two separate `run.sh`
    invocations with the same `session_id` therefore share real, on-disk state — not a
    process-local variable.
    """
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.memory import Memory

    question = _last_user_message(payload)
    if not question:
        raise RuntimeError("memory mode requires at least one user message")

    session_id = payload.get("session_id") or "arena-default"
    MEMORY_DIR.mkdir(parents=True, exist_ok=True)
    db_path = MEMORY_DIR / f"{session_id}.db"
    db_uri = f"sqlite+aiosqlite:///{db_path}"

    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use the conversation history to recall facts the user "
        "told you in earlier turns."
    )

    async def _run() -> tuple[str, int]:
        memory = Memory.from_defaults(session_id=session_id, token_limit=8000, async_database_uri=db_uri)
        history = await memory.aget()
        messages = (
            [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
            + list(history)
            + [ChatMessage(role=MessageRole.USER, content=question)]
        )
        response = await llm.achat(messages)
        reply_text = response.message.content or ""
        await memory.aput(ChatMessage(role=MessageRole.USER, content=question))
        await memory.aput(ChatMessage(role=MessageRole.ASSISTANT, content=reply_text))
        return reply_text, len(history)

    _progress(f"[memory] session_id={session_id!r} db={db_path}")
    start = time.monotonic()
    reply_text, prior_turn_count = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)
    _progress(f"[memory] loaded {prior_turn_count} prior message(s) from disk before this turn")

    return {"reply": reply_text, "latency_ms": latency_ms}


def run_structured(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """LlamaIndex native PydanticProgram structured prediction -> validated typed object.

    Uses `LLMTextCompletionProgram` + `PydanticOutputParser` (`llama_index.core.program`),
    NOT `llm.structured_predict()`'s forced-native-tool-call path: agnes-2.0-flash's
    OpenAI-compatible endpoint was observed (see README.md "Uncertainties") to sometimes
    return zero tool_calls even with `tool_choice="required"`, which
    `FunctionCallingProgram`/`structured_predict()` treats as a hard error
    ("Expected at least one tool call, but got 0 tool calls."). `LLMTextCompletionProgram`
    instead asks for the schema as formatted JSON text via a normal `.chat()` call and
    parses+validates the reply with Pydantic — the same LlamaIndex "PydanticProgram" family,
    just the completion-based member instead of the function-calling one, and it does not
    depend on the provider's function-calling reliability.
    """
    from llama_index.core.output_parsers import PydanticOutputParser
    from llama_index.core.program import LLMTextCompletionProgram
    from pydantic import BaseModel, Field

    class ExtractedFact(BaseModel):
        subject: str = Field(description="Who or what the sentence is about")
        role_or_property: str = Field(description="Their role, title, or the key property/number stated")
        organization: str = Field(description="The organization or entity mentioned, empty string if none")

    text = _last_user_message(payload) or "Dr. Imara Sowande is the Head of Safety at Nimbus Rail."

    output_parser = PydanticOutputParser(output_cls=ExtractedFact)
    prompt_template_str = output_parser.format(
        "Extract the subject, their role or key property, and the organization from this "
        "sentence:\n{text}"
    )
    program = LLMTextCompletionProgram.from_defaults(
        output_parser=output_parser,
        prompt_template_str=prompt_template_str,
        llm=llm,
    )

    _progress(f"[structured] extracting from: {text!r}")
    start = time.monotonic()
    obj = program(text=text)
    latency_ms = int((time.monotonic() - start) * 1000)

    return {"reply": obj.model_dump_json(), "structured": obj.model_dump(), "latency_ms": latency_ms}


def run_stream(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Real token streaming via llm.stream_chat(); records wall-clock time-to-first-token."""
    system_prompt = payload.get("system") or ""
    messages_in = payload.get("messages") or []
    chat_messages = _build_chat_messages(system_prompt, messages_in)

    _progress(f"[stream] streaming chat turn: {len(chat_messages)} messages")
    start = time.monotonic()
    ttft_ms = -1
    last_chunk = None
    chunk_count = 0
    for chunk in llm.stream_chat(chat_messages):
        chunk_count += 1
        if ttft_ms == -1 and chunk.delta:
            ttft_ms = int((time.monotonic() - start) * 1000)
        last_chunk = chunk
    latency_ms = int((time.monotonic() - start) * 1000)
    _progress(f"[stream] received {chunk_count} chunk(s), ttft_ms={ttft_ms}")

    reply_text = last_chunk.message.content if last_chunk else ""
    return {"reply": reply_text, "ttft_ms": ttft_ms, "latency_ms": latency_ms}


MODE_HANDLERS: dict[str, Callable[[dict[str, Any], Any], dict[str, Any]]] = {
    "qa": run_qa,
    "rag": run_rag,
    "tools": run_tools,
    "memory": run_memory,
    "structured": run_structured,
    "stream": run_stream,
}


# --------------------------------------------------------------------------------------
# Entry point
# --------------------------------------------------------------------------------------


def main() -> None:
    payload = _read_request()
    mode = payload.get("mode") or "qa"

    if mode not in VALID_MODES:
        _fail(mode, f"unknown mode {mode!r}; valid modes: {sorted(VALID_MODES)}")
        return

    model_name = os.environ.get("OPENAI_MODEL", "")
    api_key = os.environ.get("OPENAI_API_KEY", "")
    base_url = os.environ.get("OPENAI_BASE_URL", "")

    missing = [
        name
        for name, val in (
            ("OPENAI_API_KEY", api_key),
            ("OPENAI_BASE_URL", base_url),
            ("OPENAI_MODEL", model_name),
        )
        if not val
    ]
    if missing:
        _fail(mode, f"missing required env var(s): {', '.join(missing)}", model_name)
        return

    _progress(
        f"mode={mode} wiring OpenAILike model={model_name!r} base_url={base_url!r} "
        f"embed_url={EMBED_URL!r} qdrant_url={QDRANT_URL!r}"
    )

    try:
        from llama_index.core import Settings
        from llama_index.llms.openai_like import OpenAILike
    except Exception as exc:  # import-time failure (bad install, etc.)
        _fail(mode, f"failed to import llama_index: {exc}", model_name)
        return

    usage = _UsageCapture()
    cb_manager = usage.build_callback_manager()
    # Global fallback for components that don't inherit callback_manager off `llm` (see
    # _UsageCapture.build_callback_manager docstring) — e.g. RetrieverQueryEngine.from_args.
    Settings.callback_manager = cb_manager
    llm = OpenAILike(
        model=model_name,
        api_base=base_url,
        api_key=api_key,
        is_chat_model=True,
        is_function_calling_model=True,
        timeout=60.0,
        callback_manager=cb_manager,
    )

    try:
        result = MODE_HANDLERS[mode](payload, llm)
    except Exception as exc:  # network/auth/provider/logic errors — never crash, emit as data
        _progress(traceback.format_exc())
        _fail(mode, f"{mode} mode failed: {exc}", model_name)
        return

    result.setdefault("input_tokens", usage.input_tokens)
    result.setdefault("output_tokens", usage.output_tokens)

    envelope = _envelope(mode, model_name, **result)

    _progress(
        f"done: mode={mode} latency_ms={envelope['latency_ms']} "
        f"input_tokens={envelope['input_tokens']} output_tokens={envelope['output_tokens']} "
        f"tool_calls={len(envelope['tool_calls'])} retrieved={len(envelope['retrieved'])} "
        f"ttft_ms={envelope['ttft_ms']}"
    )
    _emit(envelope)


if __name__ == "__main__":
    main()
