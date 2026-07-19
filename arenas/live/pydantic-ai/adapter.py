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
  observability  Measures Pydantic AI's NATIVE OpenTelemetry instrumentation
             (`Agent.instrument`, `pydantic_ai.models.instrumented`) with an
             IN-PROCESS OTel SDK `TracerProvider` + `InMemorySpanExporter`
             (no external collector, no `logfire.configure()`/logfire-cloud).
             Runs one real turn with an ACTUALLY-EXECUTED tool (same
             `calculate` tool as `tools` mode, multiple ops per turn) and
             reports every finished span's name and duration.
  middleware Pydantic AI's NATIVE model-wrapping seam:
             `pydantic_ai.models.wrapper.WrapperModel`, subclassed as
             `arena_middleware.ArenaMiddlewareModel`. INBOUND: appends a
             sentinel to the system prompt inside `request()`, before the
             wrapped model ever sees it. OUTBOUND: the wrapper counts every
             real request to the wrapped model. Emits a DIFFERENT, smaller
             stdout shape than every other mode -- `{"text", "middleware"}`
             only -- see `mode_middleware`'s docstring below.
  memory_scoped  Compound-scope `(tenant_id, user_id, product_id[, session_id])`
             fact store, HAND-ROLLED on top of `arena_memory_store` (Pydantic
             AI has no native multi-tenant memory concept -- see
             `arena_memory_store.py`'s module docstring and README.md).
             Writes a fact under one scope, recalls it back in-scope, and
             recalls again under a different tenant to prove the miss.
  isolation_leak  Same hand-rolled scoped store, adversarial framing: tenant
             A writes a secret, tenant B queries for it. Asserts the store
             never returns A's data for B's scope key.
  crash_recovery  Does `message_history` persistence survive a HARD `kill -9`
             mid-conversation, not just a clean process exit? Two invocation
             shapes selected by payload (see mode_crash_recovery docstring):
             `turns: [...]` runs a multi-turn conversation IN-PROCESS,
             checkpointing to rqlite after every turn (this is the long-lived
             process `test-crash.sh` SIGKILLs mid-flight); `query` (no
             `turns`) is the FRESH recovery process that loads whatever
             actually survived and reports `resumed` / `precrash_fact_recalled`
             / `state_lost`.

`ARENA_STUB=1` (any mode): swaps every mode's real dependencies for offline, in-process
substitutes -- `pydantic_ai.models.test.TestModel` in place of the real agnes
`OpenAIChatModel`, canned/local retrieval in place of the real e5+Qdrant calls, and an
in-process dict in place of rqlite -- see `arena_common.is_stub()` / `build_model()` and
`arena_memory_store.py`'s `_stub_*` fallbacks. No `OPENAI_*`/`RQLITE_URL`/`QDRANT_URL`/
`EMBED_URL` env is required and no network I/O happens when set.

Stdout contract (superset of the base arena's `adapterOutput`; the base
runner ignores fields it doesn't know, so this is additive/compatible):

  {"framework": "pydantic-ai", "mode": <mode>, "model": <model>,
   "reply": str, "input_tokens": int, "output_tokens": int,
   "latency_ms": int, "tool_calls": [...], "retrieved": [...],
   "structured": <obj|null>, "ttft_ms": int, "error": str,
   "scoped_recall": <obj|null>, "leak_detected": <bool|null>,
   "resumed": <bool|null>, "precrash_fact_recalled": <bool|null>,
   "state_lost": <bool|null>,
   "otel_supported": <bool|null>, "span_count": <int|null>,
   "span_names": [...], "span_timings_ms": {name: ms, ...},
   "logs_structured": <bool|null>, "log_sample": str, "notes": str}
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time

from arena_common import ConfigError, build_model, current_model_name, embed_query_vector, is_stub, log, qdrant_search
from arena_memory_store import load_history_json, load_scoped_facts, save_history_json, save_scoped_fact, scope_key
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
        "scoped_recall": None,
        "leak_detected": None,
        "resumed": None,
        "precrash_fact_recalled": None,
        "state_lost": None,
        "otel_supported": None,
        "span_count": None,
        "span_names": [],
        "span_timings_ms": {},
        "logs_structured": None,
        "log_sample": "",
        "notes": "",
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
    collection = os.environ.get("QDRANT_COLLECTION", "arena_rag")
    log(f"rag: searching qdrant collection={collection} top_k={top_k}")
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
        # Report `result` as a STRING: the runner's adapterOutput contract types
        # toolCall.Result as a Go `string` (arenas/live/runner/types.go). Emitting a
        # bare JSON number here makes json.Unmarshal fail the whole line, so the
        # runner records "no valid result JSON object on stdout" and the tools row
        # scores as `no tool ✗` even though the tool really executed. langgraph/eino/
        # aisotropy all stringify their tool result for the same reason.
        executed.append({"name": "calculate", "arguments": {"expression": expression}, "result": str(value)})
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
# Shared helper for memory_scoped / isolation_leak -- one real agnes recall
# turn, grounded strictly in whatever scoped facts (if any) are passed in.
# --------------------------------------------------------------------------
async def _scoped_recall_turn(model, facts_text: str | None, query: str):
    from pydantic_ai import Agent

    if facts_text:
        system = (
            "You are a scoped-memory assistant. Answer the user's question ONLY using the "
            "KNOWN FACTS below, which are scoped strictly to the current tenant/user/product. "
            "If the KNOWN FACTS do not contain the answer, say plainly that you have no "
            "information for this scope -- never invent an answer and never use information "
            "from outside the KNOWN FACTS block.\n\nKNOWN FACTS:\n" + facts_text
        )
    else:
        system = (
            "You are a scoped-memory assistant. No KNOWN FACTS are available for the current "
            "tenant/user/product scope. If asked something you have no facts for, say plainly "
            "that you have no information for this scope -- never invent an answer."
        )
    agent = Agent(model, system_prompt=system)
    result = await agent.run(query)
    input_tokens, output_tokens = _usage_tokens(result.usage)
    return result.output, input_tokens, output_tokens


def _sum_tokens(pairs: list[tuple[int, int]]) -> tuple[int, int]:
    """Sum (input,output) token pairs, treating any -1 (unavailable) as 0 -- unless ALL are -1."""
    if all(i < 0 and o < 0 for i, o in pairs):
        return -1, -1
    return sum(max(i, 0) for i, o in pairs), sum(max(o, 0) for i, o in pairs)


# --------------------------------------------------------------------------
# mode: memory_scoped -- hand-rolled COMPOUND-SCOPE fact store, isolation
#
# Pydantic AI has no native multi-tenant memory concept. This mode proves the
# hand-rolled scope_key() convention in arena_memory_store.py works both
# ways: (1) a fact written under (tenant_id, user_id, product_id[, session_id])
# is recalled back under the SAME scope, and (2) the SAME query under a
# DIFFERENT tenant_id returns nothing -- neither from the store nor leaked
# into the model's reply. See README.md's honest note: this isolation is a
# developer convention, not a framework guarantee -- get the key wrong and
# it silently leaks.
# --------------------------------------------------------------------------
async def mode_memory_scoped(payload: dict) -> dict:
    model, model_name = build_model()
    start = time.monotonic()

    tenant_id = payload.get("tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    session_id = payload.get("session_id")
    fact = payload.get("fact")
    query = payload.get("query") or _last_user_content(payload)
    cross_tenant_id = payload.get("cross_tenant_id") or f"{tenant_id}-cross-check"

    if not tenant_id or not user_id or not product_id:
        raise ValueError("mode=memory_scoped requires tenant_id, user_id, product_id")
    if not fact:
        raise ValueError("mode=memory_scoped requires payload.fact (the fact to write before recalling)")
    if tenant_id == cross_tenant_id:
        raise ValueError("cross_tenant_id must differ from tenant_id to prove cross-tenant isolation")

    own_scope = scope_key(tenant_id, user_id, product_id, session_id)
    cross_scope = scope_key(cross_tenant_id, user_id, product_id, session_id)

    # (1) WRITE fact under (tenant_id, user_id, product_id[, session_id])
    log(f"memory_scoped: WRITE fact under own scope (tenant={tenant_id!r} user={user_id!r} product={product_id!r})")
    save_scoped_fact(own_scope, fact)

    # (2) RECALL same scope -- store-level check + a real agnes recall turn
    own_facts = load_scoped_facts(own_scope)
    store_same_scope_hit = own_facts is not None and fact in own_facts
    log(f"memory_scoped: RECALL same scope -- store hit={store_same_scope_hit}")
    same_reply, in1, out1 = await _scoped_recall_turn(model, own_facts, query)

    # (3) RECALL under a DIFFERENT tenant -- must return NOTHING
    cross_facts = load_scoped_facts(cross_scope)
    store_cross_tenant_miss = cross_facts is None or fact not in cross_facts
    log(f"memory_scoped: RECALL cross-tenant (tenant={cross_tenant_id!r}) -- store miss={store_cross_tenant_miss}")
    cross_reply, in2, out2 = await _scoped_recall_turn(model, cross_facts, query)
    reply_leaked_fact = fact.strip().lower() in cross_reply.strip().lower()

    same_scope_hit = store_same_scope_hit
    cross_tenant_miss = store_cross_tenant_miss and not reply_leaked_fact
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _sum_tokens([(in1, out1), (in2, out2)])

    out = _base_result("memory_scoped", model_name)
    out.update(
        reply=same_reply,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        scoped_recall={
            "same_scope_hit": same_scope_hit,
            "cross_tenant_miss": cross_tenant_miss,
            "same_scope_reply": same_reply,
            "cross_tenant_reply": cross_reply,
            "own_scope_key": own_scope,
            "cross_scope_key": cross_scope,
        },
        error="",
    )
    return out


# --------------------------------------------------------------------------
# mode: isolation_leak -- adversarial framing over the same hand-rolled
# compound-scope store: tenant A writes a SECRET, tenant B queries for it.
# `leak_detected` is the OR of two independent checks: (a) the scoped store
# itself returning A's data for B's scope key (the authoritative,
# code-verifiable check -- an exact `WHERE scope_key = ?` match, see
# arena_memory_store.load_scoped_facts), and (b) the model's reply, as a
# real agnes recall turn, containing the secret anyway.
# --------------------------------------------------------------------------
async def mode_isolation_leak(payload: dict) -> dict:
    model, model_name = build_model()
    start = time.monotonic()

    tenant_id = payload.get("tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    session_id = payload.get("session_id")
    secret = payload.get("secret")
    attacker_tenant_id = payload.get("attacker_tenant_id")
    query = payload.get("query") or _last_user_content(payload)

    if not tenant_id or not user_id or not product_id:
        raise ValueError("mode=isolation_leak requires tenant_id, user_id, product_id")
    if not secret:
        raise ValueError("mode=isolation_leak requires payload.secret (the fact tenant A writes)")
    if not attacker_tenant_id:
        raise ValueError("mode=isolation_leak requires payload.attacker_tenant_id")
    if attacker_tenant_id == tenant_id:
        raise ValueError("attacker_tenant_id must differ from tenant_id -- this mode tests cross-tenant isolation")

    victim_scope = scope_key(tenant_id, user_id, product_id, session_id)
    attacker_scope = scope_key(attacker_tenant_id, user_id, product_id, session_id)

    log(f"isolation_leak: tenant A ({tenant_id!r}) writes a SECRET under its own scope")
    save_scoped_fact(victim_scope, secret)

    # Authoritative check: what does the scoped store return for tenant B's
    # scope key? This is the assertion that matters -- exact-match lookup,
    # not a post-filter over a shared pool.
    attacker_facts = load_scoped_facts(attacker_scope)
    leak_in_store = attacker_facts is not None and secret in attacker_facts
    log(f"isolation_leak: tenant B ({attacker_tenant_id!r}) store lookup -- leak_in_store={leak_in_store}")

    # Real agnes recall turn, AS tenant B, grounded only in tenant B's own
    # (empty, in the honest case) scoped facts.
    attacker_reply, in1, out1 = await _scoped_recall_turn(model, attacker_facts, query)
    leak_in_reply = secret.strip().lower() in attacker_reply.strip().lower()
    log(f"isolation_leak: tenant B agnes reply -- leak_in_reply={leak_in_reply}")

    leak_detected = leak_in_store or leak_in_reply
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = in1, out1

    out = _base_result("isolation_leak", model_name)
    out.update(
        reply=attacker_reply,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        leak_detected=leak_detected,
        error="",
    )
    out["victim_scope_key"] = victim_scope
    out["attacker_scope_key"] = attacker_scope
    out["leak_in_store"] = leak_in_store
    out["leak_in_reply"] = leak_in_reply
    return out


# --------------------------------------------------------------------------
# mode: crash_recovery -- does persisted `message_history` survive a HARD
# `kill -9` mid-conversation, or only a clean process exit?
#
# `mode_memory` already proves persistence survives a clean exit (two
# *separate* `python adapter.py` invocations). This mode proves the harder
# case: state is checkpointed to REAL rqlite SYNCHRONOUSLY after every turn,
# so anything that already completed a turn is durable even if the process
# is violently killed one instant later -- but Pydantic AI itself has NO
# built-in crash-recovery mechanism; the durability is entirely this
# adapter's own "persist after each turn, before starting the next" discipline
# (see README.md's honest note).
#
# Two invocation shapes, selected by which payload key is present:
#
#   PRE-CRASH (payload has "turns": [str, ...], 2-3 items): runs each turn
#   SEQUENTIALLY against the SAME agent conversation, IN-PROCESS, persisting
#   (checkpointing) the full message_history to rqlite after every turn
#   completes, logging a distinct, greppable "checkpoint: turn N/M persisted"
#   PROGRESS line to stderr right after each checkpoint lands. This is the
#   long-lived process `test-crash.sh` launches in the background and
#   SIGKILLs mid-flight -- deliberately NOT single-shot like `memory` mode,
#   because simulating "killed mid-turn-2" requires turn 2's real model call
#   to still be in flight in the SAME OS process whose turn-1 checkpoint
#   already landed in rqlite.
#
#   POST-CRASH RECOVERY (payload has "query" instead of "turns"): a FRESH
#   process invocation (run.sh / adapter.py started again from scratch).
#   Loads whatever message_history actually persisted in rqlite for
#   session_id -- exactly as far as the pre-crash process got before it
#   died -- runs one more turn, and reports whether the turn-1 fact survives
#   the round trip via `resumed` / `precrash_fact_recalled` / `state_lost`.
# --------------------------------------------------------------------------
async def mode_crash_recovery(payload: dict) -> dict:
    from pydantic_ai import Agent
    from pydantic_ai.messages import ModelMessagesTypeAdapter

    session_id = payload.get("session_id")
    if not session_id:
        raise ValueError("mode=crash_recovery requires payload.session_id")

    model, model_name = build_model()
    system = payload.get("system") or "You are a helpful assistant with persistent memory across turns."

    turns = payload.get("turns")
    if turns:
        if not isinstance(turns, list) or not all(isinstance(t, str) and t for t in turns):
            raise ValueError("mode=crash_recovery payload.turns must be a non-empty list of non-empty strings")
        total = len(turns)
        agent = Agent(model, system_prompt=system)
        history = None
        last_reply = ""
        start = time.monotonic()
        for i, turn_prompt in enumerate(turns, start=1):
            log(f"crash_recovery: pre-crash process running turn {i}/{total}")
            result = await agent.run(turn_prompt, message_history=history)
            history = result.all_messages()
            last_reply = result.output
            updated_json = ModelMessagesTypeAdapter.dump_json(history)
            save_history_json(session_id, updated_json)
            log(f"crash_recovery: checkpoint: turn {i}/{total} persisted ({len(updated_json)} bytes)")

        # Reached only if the process was NOT killed -- e.g. a control run.
        latency_ms = int((time.monotonic() - start) * 1000)
        out = _base_result("crash_recovery", model_name)
        out.update(reply=last_reply, latency_ms=latency_ms, error="")
        out["phase"] = "precrash_complete_no_crash"
        out["turns_completed"] = total
        return out

    # POST-CRASH RECOVERY: fresh process, load whatever survived, continue.
    query = payload.get("query") or _last_user_content(payload)
    expect_fact = payload.get("expect_fact") or ""

    prior_json = load_history_json(session_id)
    resumed = prior_json is not None
    state_lost = not resumed
    prior_history = ModelMessagesTypeAdapter.validate_json(prior_json) if prior_json else None

    if state_lost:
        log(f"crash_recovery: RECOVERY -- no persisted history found for session_id={session_id!r}; state_lost=true")
    else:
        log(f"crash_recovery: RECOVERY -- loaded {len(prior_json)} bytes of persisted pre-crash history")

    agent = Agent(model, system_prompt=system)
    start = time.monotonic()
    result = await agent.run(query, message_history=prior_history)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    updated_json = ModelMessagesTypeAdapter.dump_json(result.all_messages())
    save_history_json(session_id, updated_json)
    log(f"crash_recovery: RECOVERY -- persisted post-recovery history ({len(updated_json)} bytes)")

    precrash_fact_recalled = bool(expect_fact) and expect_fact.strip().lower() in result.output.strip().lower()
    log(f"crash_recovery: RECOVERY -- precrash_fact_recalled={precrash_fact_recalled}")

    out = _base_result("crash_recovery", model_name)
    out.update(
        reply=result.output,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        error="",
    )
    out["resumed"] = resumed
    out["precrash_fact_recalled"] = precrash_fact_recalled
    out["state_lost"] = state_lost
    return out


# --------------------------------------------------------------------------
# mode: observability -- Pydantic AI's NATIVE OpenTelemetry instrumentation,
# measured with an IN-PROCESS OTel SDK sink (no external collector, no
# logfire.configure()/logfire-cloud).
#
# Pydantic AI ships native OTel instrumentation: `pydantic_ai.models.instrumented
# .InstrumentedModel`, wired onto an `Agent` via the `agent.instrument` setter
# (an `InstrumentationSettings | bool | None`). This is NOT a third-party
# wrapper bolted on from outside -- it lives inside `pydantic-ai-slim` itself
# and only depends on `opentelemetry-api`; `opentelemetry-sdk` is what THIS
# adapter supplies (any OTel SDK works -- logfire is one *possible* backend,
# not a requirement). Setting `InstrumentationSettings(tracer_provider=...)`
# to a plain `opentelemetry.sdk.trace.TracerProvider` +
# `InMemorySpanExporter`, instead of calling `logfire.configure()` (which
# would point the global provider at logfire-cloud), keeps every span
# strictly in-process -- nothing leaves this Python process.
#
# The representative turn re-uses the `tools` mode's real, actually-executed
# `calculate` tool so a single turn exercises MULTIPLE ops (an agent-run
# span, one or more model-call spans, and a tool-execution span) rather than
# just one bare model call.
# --------------------------------------------------------------------------
async def mode_observability(payload: dict) -> dict:
    from pydantic_ai import Agent
    from pydantic_ai.agent import InstrumentationSettings

    try:
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import SimpleSpanProcessor
        from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
    except ImportError as exc:
        raise ConfigError(f"opentelemetry-sdk not installed: {exc}") from exc

    model, model_name = build_model()
    prompt = _last_user_content(payload)
    system = payload.get("system") or (
        "You are a precise assistant with a `calculate` tool. For ANY arithmetic in the "
        "user's question, call `calculate` with the exact expression instead of computing it "
        "yourself, then use the tool's real result in your final answer."
    )

    # In-process OTel SDK sink: a DEDICATED TracerProvider (not the process
    # global) feeding a SimpleSpanProcessor -> InMemorySpanExporter. No
    # collector, no network export, no logfire.configure().
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    instrumentation = InstrumentationSettings(tracer_provider=provider)

    agent = Agent(model, system_prompt=system)
    agent.instrument = instrumentation  # native per-agent instrumentation switch
    executed: list[dict] = []

    @agent.tool_plain
    def calculate(expression: str) -> str:
        """Evaluate a real arithmetic expression (+ - * / // % **, parentheses) and return it."""
        value = safe_eval_expr(expression)
        # `result` must be a string to satisfy the runner's toolCall.Result contract
        # (see the tools-mode note above / arenas/live/runner/types.go).
        executed.append({"name": "calculate", "arguments": {"expression": expression}, "result": str(value)})
        log(f"observability: EXECUTED calculate({expression!r}) = {value}")
        return str(value)

    log(f"observability: building agent model={model_name} instrument=InstrumentationSettings(in-memory OTel SDK)")
    start = time.monotonic()
    result = await agent.run(prompt)
    latency_ms = int((time.monotonic() - start) * 1000)
    input_tokens, output_tokens = _usage_tokens(result.usage)

    provider.force_flush()
    spans = exporter.get_finished_spans()
    span_count = len(spans)
    span_names: list[str] = []
    span_timings_ms: dict[str, float] = {}
    for s in spans:
        span_names.append(s.name)
        dur_ms = (s.end_time - s.start_time) / 1_000_000 if s.start_time and s.end_time else -1.0
        key = s.name
        n = 1
        while key in span_timings_ms:  # disambiguate repeated span names (e.g. 2 "chat" calls)
            n += 1
            key = f"{s.name}#{n}"
        span_timings_ms[key] = round(dur_ms, 3)
    log(f"observability: {span_count} spans emitted: {span_names}")

    # Pydantic AI has NO separate structured-logging channel in its
    # instrumentation path (no `logging.getLogger` calls anywhere in
    # `pydantic_ai/models/instrumented.py` or `_instrumentation.py`) -- every
    # observability signal is carried as OTel span ATTRIBUTES following the
    # OTel GenAI semantic conventions (`gen_ai.input.messages` /
    # `gen_ai.output.messages`, valid JSON strings; `pydantic_ai.all_messages`
    # on the agent-run span). That data IS structured (machine-parseable
    # JSON), just attached to spans rather than emitted as a distinct `logs`
    # signal -- `log_sample` below is one REAL captured attribute value, not
    # a fabricated example.
    log_sample = ""
    sample_span = ""
    for s in spans:
        attrs = dict(s.attributes or {})
        for attr_key in ("gen_ai.output.messages", "gen_ai.input.messages", "pydantic_ai.all_messages"):
            if attr_key in attrs:
                log_sample = f"{attr_key}={attrs[attr_key]}"
                sample_span = s.name
                break
        if log_sample:
            break
    if sample_span:
        log(f"observability: log_sample drawn from span {sample_span!r}")

    out = _base_result("observability", model_name)
    out.update(
        reply=result.output,
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        latency_ms=latency_ms,
        tool_calls=executed,
        error="",
    )
    out["otel_supported"] = True
    out["span_count"] = span_count
    out["span_names"] = span_names
    out["span_timings_ms"] = span_timings_ms
    out["logs_structured"] = True
    out["log_sample"] = log_sample[:4000]
    out["notes"] = (
        "Pydantic AI has NATIVE OpenTelemetry instrumentation "
        "(pydantic_ai.models.instrumented.InstrumentedModel, switched on per-Agent via the "
        "agent.instrument = InstrumentationSettings(...) setter) -- not a wrapper wired on from "
        "outside. This run pointed InstrumentationSettings.tracer_provider at a LOCAL, in-process "
        "opentelemetry.sdk.trace.TracerProvider + InMemorySpanExporter; logfire.configure() (which "
        "would export to logfire-cloud) was never called and logfire's package is not imported by "
        f"this mode. One turn with a real executed tool produced {span_count} spans: {span_names} "
        "-- an agent-run span (invoke_agent) wrapping one-or-more model-call spans (chat "
        "<model>) and a tool-execution span (execute_tool <name>) for a real, invoked tool, so a "
        "single turn already yields multiple distinct observable ops with independent timings. "
        "There is no separate structured-logging API in the instrumentation path (no "
        "logging.getLogger call sites) -- everything is carried as OTel span attributes under the "
        "GenAI semantic conventions (gen_ai.input.messages / gen_ai.output.messages / "
        "pydantic_ai.all_messages), which are valid JSON and therefore structured, just not a "
        "distinct 'logs' OTel signal."
    )
    return out


# --------------------------------------------------------------------------
# mode: middleware -- Pydantic AI NATIVE model-wrapping (WrapperModel):
# mutates the inbound system prompt, counts outbound requests. See
# arena_middleware.py's module docstring for the full mechanism writeup and
# why WrapperModel (not InstrumentedModel / history_processors) was used.
#
# NOTE: this mode's stdout shape is DELIBERATELY DIFFERENT from every other
# mode's uniform contract -- {"text": str, "middleware": {...}} only, per
# the task brief -- not the "framework"/"reply"/... superset the other nine
# modes share. The base arena runner ignores unknown fields either way, so
# this stays additive; it just doesn't reuse `_base_result()`.
# --------------------------------------------------------------------------
async def mode_middleware(payload: dict) -> dict:
    from pydantic_ai import Agent

    from arena_middleware import ArenaMiddlewareModel

    system = payload.get("system") or ""
    prompt = _last_user_content(payload)

    base_model, model_name = build_model()
    wrapped_model = ArenaMiddlewareModel(base_model)

    log(f"middleware: building agent model={model_name} wrapped in ArenaMiddlewareModel (WrapperModel)")
    agent = Agent(wrapped_model, system_prompt=system)

    start = time.monotonic()
    result = await agent.run(prompt)
    latency_ms = int((time.monotonic() - start) * 1000)
    log(
        f"middleware: turn done latency_ms={latency_ms} inbound_fired={wrapped_model.inbound_fired} "
        f"invocations={wrapped_model.request_count}"
    )

    return {
        "text": result.output,
        "middleware": {
            "mechanism": "WrapperModel",
            "inbound_fired": wrapped_model.inbound_fired,
            "outbound_fired": wrapped_model.request_count > 0,
            "invocations": wrapped_model.request_count,
            "supported": True,
        },
    }


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

    # Under ARENA_STUB=1, TestModel's default text output ("success (no tool
    # calls)") is not valid JSON, so PromptedOutput's schema validation fails
    # closed with UnexpectedModelBehavior every time -- not a meaningful stub
    # result. Canning a schema-valid Answer as TestModel's custom_output_text
    # keeps the stub offline/deterministic while producing a domain-correct
    # (if canned) structured result, the same "canned valid structured JSON"
    # pattern used for the `tools`/`observability` modes' canned tool args.
    canned_text = None
    if is_stub():
        canned_answer = Answer(
            answer="ARENA_STUB canned answer -- no real model call",
            key_points=["ARENA_STUB canned key point (ARENA_STUB=1, no network I/O)"],
            confidence=0.5,
        )
        canned_text = canned_answer.model_dump_json()

    model, model_name = build_model(custom_output_text=canned_text)
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
    "memory_scoped": mode_memory_scoped,
    "isolation_leak": mode_isolation_leak,
    "crash_recovery": mode_crash_recovery,
    "structured": mode_structured,
    "stream": mode_stream,
    "observability": mode_observability,
    "middleware": mode_middleware,
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
