#!/usr/bin/env python3
"""Exhaustive multi-mode LIVE arena adapter for Pydantic AI.

Reads one JSON object from stdin, including a `"mode"` key (default `"qa"`
when absent, for backward compatibility with the base live-arena runner's
uniform single-shot contract -- see `arenas/live/SPEC.md`). Writes exactly
one JSON object to stdout; all logs go to stderr as `PROGRESS: ...` lines.

Modes (each exercises a different Pydantic AI NATIVE feature against REAL
infrastructure -- see README.md for the full contract and evidence):

  qa         Single-shot chat. If `tools` are supplied, the model's native
             tool_calls are CAPTURED, not executed (the base-arena contract's
             single-shot tool-calling measurement). Default when `mode` is
             absent -- exact behavior of the original single-mode adapter.
  rag        Real retrieval (qdrant-client + e5 query embeddings against the
             REAL `arena_rag` Qdrant collection) augments the prompt; the
             model must answer only from the retrieved context.
  tools      A Pydantic AI `Agent` with a real `@agent.tool_plain` calculator
             that is ACTUALLY EXECUTED in the agent's multi-step loop (model
             calls the tool -> tool runs for real -> result feeds a second
             model call for the final answer).
  memory     `message_history` persisted to REAL rqlite across separate
             process invocations, keyed by `session_id`.
  structured `Agent(..., output_type=PromptedOutput(Answer))` producing a
             validated Pydantic object.
  stream     `agent.run_stream(...)` real token streaming; records `ttft_ms`.

Stdout contract (superset of the base arena's `adapterOutput`; the base
runner ignores fields it doesn't know, so this is additive/compatible):

  {"framework": "pydantic-ai", "mode": <mode>, "model": <model>,
   "reply": str, "input_tokens": int, "output_tokens": int,
   "latency_ms": int, "tool_calls": [...], "retrieved": [...],
   "structured": <obj|null>, "ttft_ms": int, "error": str}
"""

from __future__ import annotations

import asyncio
import json
import sys
import time

from arena_common import ConfigError, build_model, current_model_name, embed_query_vector, log, qdrant_search
from arena_memory_store import load_history_json, save_history_json
from arena_tools import safe_eval_expr


def build_error_result(mode: str, model_name: str, error: str, latency_ms: int) -> dict:
    return {
        "framework": "pydantic-ai",
        "mode": mode,
        "model": model_name,
        "reply": "",
        "input_tokens": -1,
        "output_tokens": -1,
        "latency_ms": latency_ms,
        "tool_calls": [],
        "retrieved": [],
        "structured": None,
        "ttft_ms": -1,
        "error": error,
    }


def _base_result(mode: str, model_name: str) -> dict:
    """A result dict pre-filled with contract defaults; callers overwrite what they used."""
    result = build_error_result(mode, model_name, "", 0)
    return result


def _usage_tokens(usage) -> tuple[int, int]:
    if usage is None:
        return -1, -1
    input_tokens = getattr(usage, "input_tokens", None)
    output_tokens = getattr(usage, "output_tokens", None)
    if input_tokens is None or output_tokens is None:
        return -1, -1
    if input_tokens == 0 and output_tokens == 0:
        return -1, -1
    return input_tokens, output_tokens


def _last_user_content(payload: dict) -> str:
    messages = payload.get("messages") or []
    if not messages:
        raise ValueError("payload.messages is empty")
    return messages[-1].get("content", "")


# --------------------------------------------------------------------------
# mode: qa -- single-shot, tool_calls CAPTURED not executed (base contract)
# --------------------------------------------------------------------------
async def mode_qa(payload: dict) -> dict:
    from pydantic_ai import Agent, Tool
    from pydantic_ai.messages import ModelRequest, ModelResponse, TextPart, ToolCallPart, UserPromptPart
    from pydantic_ai.usage import UsageLimits

    model, model_name = build_model()

    system = payload.get("system") or ""
    messages = payload.get("messages") or []
    tool_specs = payload.get("tools") or []
    if not messages:
        raise ValueError("payload.messages is empty")

    history_specs = messages[:-1]
    last = messages[-1]
    user_prompt = last.get("content", "")

    history: list = []
    for m in history_specs:
        role = m.get("role")
        content = m.get("content", "")
        if role == "assistant":
            history.append(ModelResponse(parts=[TextPart(content=content)]))
        else:
            history.append(ModelRequest(parts=[UserPromptPart(content=content)]))

    def make_stub(tool_name: str):
        def _stub(**kwargs):  # pragma: no cover - never expected to execute
            return json.dumps({"status": "not_executed_single_shot", "tool": tool_name})

        return _stub

    tools = []
    for spec in tool_specs:
        name = spec["name"]
        description = spec.get("description", "")
        schema = spec.get("parameters") or {"type": "object", "properties": {}}
        tools.append(Tool.from_schema(make_stub(name), name=name, description=description, json_schema=schema))

    log(f"qa: building agent model={model_name} tools={len(tools)}")
    agent = Agent(model, tools=tools, system_prompt=system)

    start = time.monotonic()
    resp = None
    async with agent.iter(
        user_prompt, message_history=history or None, usage_limits=UsageLimits(request_limit=1)
    ) as agent_run:
        async for node in agent_run:
            if Agent.is_call_tools_node(node):
                resp = node.model_response
                break
    usage = agent_run.usage
    latency_ms = int((time.monotonic() - start) * 1000)

    if resp is None:
        raise RuntimeError("model produced no response node")

    reply = "".join(p.content for p in resp.parts if isinstance(p, TextPart))
    tool_calls = [
        {"name": p.tool_name, "arguments": p.args_as_dict()} for p in resp.parts if isinstance(p, ToolCallPart)
    ]
    input_tokens, output_tokens = _usage_tokens(usage)

    result = _base_result("qa", model_name)
    result.update(
        reply=reply,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        tool_calls=tool_calls,
        error="",
    )
    return result


# --------------------------------------------------------------------------
# mode: rag -- real qdrant-client + e5 retrieval, answer only from context
# --------------------------------------------------------------------------
async def mode_rag(payload: dict) -> dict:
    from pydantic_ai import Agent

    model, model_name = build_model()
    query = payload.get("query") or _last_user_content(payload)
    top_k = int(payload.get("top_k") or 3)

    log(f"rag: embedding query (len={len(query)}) via e5 server")
    vector = embed_query_vector(query)
    log(f"rag: searching qdrant collection=arena_rag top_k={top_k}")
    retrieved = qdrant_search(vector, top_k=top_k)
    log(f"rag: retrieved {len(retrieved)} passages, top1={retrieved[0]['doc_id'] if retrieved else None!r}")

    context_block = "\n\n".join(f"[{r['doc_id']}] {r['title']}\n{r['text']}" for r in retrieved)
    base_system = payload.get("system") or "You are a precise, grounded assistant."
    system = (
        f"{base_system}\n\n"
        "Answer ONLY using the CONTEXT below. If the context does not contain the answer, "
        "say plainly that you don't know / it isn't in the source -- never invent an answer.\n\n"
        f"CONTEXT:\n{context_block}"
    )

    agent = Agent(model, system_prompt=system)
    start = time.monotonic()
    result = await agent.run(query)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    out = _base_result("rag", model_name)
    out.update(
        reply=result.output,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        retrieved=retrieved,
        error="",
    )
    return out


# --------------------------------------------------------------------------
# mode: tools -- real @agent.tool_plain calculator, ACTUALLY EXECUTED
# --------------------------------------------------------------------------
async def mode_tools(payload: dict) -> dict:
    from pydantic_ai import Agent

    model, model_name = build_model()
    prompt = _last_user_content(payload)
    system = payload.get("system") or (
        "You are a precise assistant with a `calculate` tool. For ANY arithmetic in the "
        "user's question, call `calculate` with the exact expression instead of computing it "
        "yourself, then use the tool's real result in your final answer."
    )

    agent = Agent(model, system_prompt=system)
    executed: list[dict] = []

    @agent.tool_plain
    def calculate(expression: str) -> str:
        """Evaluate a real arithmetic expression (+ - * / // % **, parentheses) and return it."""
        value = safe_eval_expr(expression)
        executed.append({"name": "calculate", "arguments": {"expression": expression}, "result": value})
        log(f"tools: EXECUTED calculate({expression!r}) = {value}")
        return str(value)

    log(f"tools: building agent model={model_name} with real calculate tool")
    start = time.monotonic()
    result = await agent.run(prompt)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    out = _base_result("tools", model_name)
    out.update(
        reply=result.output,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        tool_calls=executed,
        error="",
    )
    return out


# --------------------------------------------------------------------------
# mode: memory -- message_history persisted to REAL rqlite across processes
# --------------------------------------------------------------------------
async def mode_memory(payload: dict) -> dict:
    from pydantic_ai import Agent
    from pydantic_ai.messages import ModelMessagesTypeAdapter

    session_id = payload.get("session_id")
    if not session_id:
        raise ValueError("mode=memory requires payload.session_id")
    prompt = _last_user_content(payload)
    system = payload.get("system") or "You are a helpful assistant with persistent memory across turns."

    model, model_name = build_model()

    prior_json = load_history_json(session_id)
    prior_history = ModelMessagesTypeAdapter.validate_json(prior_json) if prior_json else None

    agent = Agent(model, system_prompt=system)
    start = time.monotonic()
    result = await agent.run(prompt, message_history=prior_history)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    updated_json = ModelMessagesTypeAdapter.dump_json(result.all_messages())
    save_history_json(session_id, updated_json)

    out = _base_result("memory", model_name)
    out.update(
        reply=result.output,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        error="",
    )
    return out


# --------------------------------------------------------------------------
# mode: structured -- Agent(output_type=PromptedOutput(Answer))
# --------------------------------------------------------------------------
async def mode_structured(payload: dict) -> dict:
    from pydantic import BaseModel, Field
    from pydantic_ai import Agent
    from pydantic_ai.output import PromptedOutput

    class Answer(BaseModel):
        answer: str = Field(description="direct, short answer to the question")
        key_points: list[str] = Field(description="1-3 short supporting facts")
        confidence: float = Field(ge=0, le=1, description="model's self-rated confidence, 0..1")

    prompt = _last_user_content(payload)
    system = payload.get("system") or "Answer concisely and factually."

    model, model_name = build_model()
    # NOTE (verified gotcha, see README): the default native-tool-call output
    # strategy (bare `output_type=Answer`) fails against agnes-2.0-flash with
    # `UnexpectedModelBehavior: Exceeded maximum output retries` -- the model
    # never emits a valid `final_result` tool call. `PromptedOutput` (ask for
    # JSON in the prompt instead of forcing a tool call) works reliably and is
    # used here.
    agent = Agent(model, output_type=PromptedOutput(Answer), system_prompt=system)

    log(f"structured: building agent model={model_name} output_type=PromptedOutput(Answer)")
    start = time.monotonic()
    result = await agent.run(prompt)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    structured_obj = result.output.model_dump()

    out = _base_result("structured", model_name)
    out.update(
        reply=structured_obj.get("answer", ""),
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        structured=structured_obj,
        error="",
    )
    return out


# --------------------------------------------------------------------------
# mode: stream -- agent.run_stream(...), records ttft_ms
# --------------------------------------------------------------------------
async def mode_stream(payload: dict) -> dict:
    from pydantic_ai import Agent

    prompt = _last_user_content(payload)
    system = payload.get("system") or "You are concise."

    model, model_name = build_model()
    agent = Agent(model, system_prompt=system)

    log(f"stream: opening run_stream model={model_name}")
    start = time.monotonic()
    ttft_ms = -1
    chunks: list[str] = []
    async with agent.run_stream(prompt) as stream_result:
        async for chunk in stream_result.stream_text(delta=True):
            if ttft_ms == -1:
                ttft_ms = int((time.monotonic() - start) * 1000)
            chunks.append(chunk)
        usage = stream_result.usage
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(usage)

    out = _base_result("stream", model_name)
    out.update(
        reply="".join(chunks),
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        ttft_ms=ttft_ms,
        error="",
    )
    return out


MODE_HANDLERS = {
    "qa": mode_qa,
    "rag": mode_rag,
    "tools": mode_tools,
    "memory": mode_memory,
    "structured": mode_structured,
    "stream": mode_stream,
}


async def run_adapter(payload: dict) -> dict:
    mode = payload.get("mode") or "qa"
    model_name_probe = current_model_name()
    start = time.monotonic()

    handler = MODE_HANDLERS.get(mode)
    if handler is None:
        return build_error_result(mode, model_name_probe, f"unknown mode: {mode!r} (valid: {sorted(MODE_HANDLERS)})", 0)

    try:
        log(f"mode={mode}: starting")
        result = await handler(payload)
        log(f"mode={mode}: done latency_ms={result.get('latency_ms')}")
        return result
    except ConfigError as exc:
        log(f"mode={mode}: config error: {exc}")
        return build_error_result(mode, model_name_probe, str(exc), int((time.monotonic() - start) * 1000))
    except Exception as exc:  # noqa: BLE001 - report any failure via JSON, not a crash
        log(f"mode={mode}: error: {type(exc).__name__}: {exc}")
        return build_error_result(
            mode, model_name_probe, f"{type(exc).__name__}: {exc}", int((time.monotonic() - start) * 1000)
        )


def main() -> int:
    log("reading stdin payload")
    try:
        raw = sys.stdin.read()
        payload = json.loads(raw)
    except Exception as exc:  # noqa: BLE001
        result = build_error_result("unknown", current_model_name(), f"invalid stdin JSON: {exc}", 0)
        print(json.dumps(result))
        return 0

    result = asyncio.run(run_adapter(payload))
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
