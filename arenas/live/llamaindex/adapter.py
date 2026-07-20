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
      "mode": "qa"|"rag"|"tools"|"memory"|"structured"|"stream"|"memory_scoped"|"isolation_leak"
              |"crash_recovery"|"observability"|"middleware",    # optional, defaults "qa"
      "system": str,
      "messages": [{"role": "user"|"assistant", "content": str}, ...],
      "tools": [{"name": str, "description": str, "parameters": <JSON Schema>}, ...],  # qa only
      "session_id": str,        # memory mode: same id across two invocations to test recall.
                                 # memory_scoped/isolation_leak: OPTIONAL extra scope axis, added
                                 # on top of the required tenant_id/user_id/product_id below.
                                 # crash_recovery: REQUIRED, shared by process_a and process_b.
      "tenant_id": str,         # memory_scoped, isolation_leak
      "user_id": str,           # memory_scoped, isolation_leak
      "product_id": str,        # memory_scoped, isolation_leak
      "fact": str,               # memory_scoped: the fact to write and recall
      "cross_tenant_id": str,   # memory_scoped: a DIFFERENT tenant_id to prove isolation against
      "secret": str,            # isolation_leak: the secret tenant_id writes (falls back to "fact")
      "leak_probe_tenant_id": str,  # isolation_leak: the DIFFERENT tenant_id probing for the leak
      "query": str,              # memory_scoped, isolation_leak, crash_recovery(process_b):
                                 # recall question (has a default)
      "role": str,               # crash_recovery: "process_a" | "process_b", REQUIRED
      "crash_after_turn": int,   # crash_recovery process_a: last turn that MUST be durably
                                 # committed before the crash window (default 1)
      "inject_delay_ms": int,    # crash_recovery process_a: deterministic pre-flight sleep
                                 # (test hook) inserted right before the turn AFTER
                                 # crash_after_turn, so a test harness gets a reliable SIGKILL
                                 # window (default 0 — no delay)
      "expected_fact_substring": str,  # crash_recovery process_b: substring proving pre-crash
                                 # recall (checked against BOTH the recovered on-disk history
                                 # and the live LLM reply)
      "turn2_marker_substring": str    # crash_recovery process_b: substring that would ONLY be
                                 # present if the crashed turn was (wrongly) partially committed
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
      "retrieved": [{"doc_id": str, "title": str, "text": str, "score": float}, ...],
      "structured": object | null,
      "ttft_ms": int,
      "error": str,
      "scoped_recall": {"same_scope_hit": bool, "cross_tenant_miss": bool} | absent,  # memory_scoped only
      "leak_detected": bool | absent,                                                  # isolation_leak only
      "role": str,                        # crash_recovery only: "process_a" | "process_b"
      "resumed": bool,                    # crash_recovery only
      "precrash_fact_recalled": bool,     # crash_recovery only
      "state_lost": bool,                 # crash_recovery only
      "turn2_committed": bool | absent    # crash_recovery process_b only
    }

Modes (see README.md for the exact LlamaIndex classes/source lines backing each one):
  qa             Single-shot chat turn. Native tool_calls are captured, NOT executed (unchanged
                 from the original single-shot adapter — this is the runner's default mode).
  rag            Real retrieval: `QdrantVectorStore` + `VectorStoreIndex` + `RetrieverQueryEngine`
                 against the real `arena_rag` Qdrant collection, embedding queries through the
                 real e5 server. System instruction forces "answer only from context, else I
                 don't know".
  tools          `FunctionAgent` multi-step loop that ACTUALLY EXECUTES a real `calculate` and/or
                 `corpus_lookup` tool (not single-shot capture) and uses the result in the answer.
  memory         Persists chat turns to a REAL on-disk SQLite store (`llama_index.core.memory.
                 Memory`, SQLAlchemy-backed) keyed by `session_id` ONLY, so a later, separate
                 process invocation with the same `session_id` recalls facts from an earlier one.
                 NOTE: this mode does NOT test multi-tenant isolation — a single `session_id`
                 axis has no concept of tenant/user/product at all. See `memory_scoped` below.
  structured     `LLMTextCompletionProgram` + `PydanticOutputParser` (LlamaIndex's native
                 PydanticProgram structured-prediction path) producing a validated typed object.
  stream         Real token streaming via `llm.stream_chat()`; records time-to-first-token.
  memory_scoped  Hand-rolled COMPOUND scope key `(tenant_id, user_id, product_id[, session_id])`
                 over the same on-disk `Memory` store `memory` mode uses. Proves same-scope
                 recall hits and cross-tenant recall misses in one invocation. LlamaIndex has NO
                 native multi-tenant memory isolation — this compound key is 100% hand-rolled
                 adapter code; see `_scope_key()`'s docstring.
  isolation_leak Tenant A writes a secret into its scope; a real agnes recall turn runs AS
                 tenant B (different compound scope) and the adapter asserts the secret does
                 NOT come back out of tenant B's scoped store.
  crash_recovery Reuses `memory` mode's EXACT on-disk store. `role=process_a` runs 2+ sequential
                 turns against a real session_id db, committing each turn (via `memory.aput()`,
                 which internally awaits `session.commit()`) before starting the next — a test
                 harness (`test-crash.sh`) SIGKILLs this process mid-turn-2. `role=process_b` is
                 a FRESH process that reopens the SAME db and proves whether the pre-crash fact
                 survives. See README "Honest note" for exactly what kill -9 durability this
                 does and does not prove.
  middleware     LlamaIndex NATIVE interception, two independent + HONEST mechanisms around ONE
                 qa turn. INBOUND (prompt mutation): a thin `_MiddlewareLLM` wrapper subclassing
                 `llama_index.core.llms.llm.LLM` appends the sentinel "\n[ARENA_MW]" to the
                 outbound system message before delegating to the real wired LLM — this is a
                 genuine LlamaIndex extension point (every real LLM integration, e.g.
                 `OpenAILike`, subclasses this same base), not a monkeypatch. OUTBOUND (call
                 counting): the NATIVE `llama_index.core.instrumentation` dispatcher (same seam
                 `observability` mode uses) is OBSERVE-ONLY — a `BaseEventHandler` on the root
                 dispatcher counts every `LLMChatStartEvent` the wrapped call fires; it observes
                 the mutation the wrapper performs, it does not (and cannot) perform it. Emits
                 its OWN JSON shape on stdout (see `run_middleware()` docstring), NOT the uniform
                 envelope above.
  observability  Measures LlamaIndex's OWN observability surface — NOT a chat-quality mode.
                 Wires (1) the NATIVE `llama_index.core.instrumentation` dispatcher (a
                 `SimpleSpanHandler` + a custom `BaseEventHandler` attached to the root
                 dispatcher) and (2) OpenInference's OTel integration
                 (`openinference-instrumentation-llama-index`) exporting to an in-process
                 `InMemorySpanExporter` (no external collector) around ONE real, multi-op RAG
                 turn (embed -> retrieve -> synthesize -> LLM chat), then reports span/event
                 counts, names, and per-span timings from BOTH telemetry paths. Emits its OWN
                 JSON shape on stdout (see `run_observability()` docstring), NOT the uniform
                 envelope above — the two are evaluating different things.

Offline stub toggle
--------------------
`ARENA_STUB=1` (any of `1`/`true`/`yes`, case-insensitive) swaps every real backend for an
in-process LlamaIndex-native mock so the ENTIRE adapter runs with zero network egress:
  - LLM: `llama_index.core.llms.MockLLM` (`llama_index.core.llms.mock.MockLLM`) instead of the
    agnes-backed `OpenAILike` — `OPENAI_API_KEY`/`OPENAI_BASE_URL`/`OPENAI_MODEL` are NOT
    required in this mode.
  - Embeddings: `llama_index.core.embeddings.MockEmbedding` (`llama_index.core.embeddings.
    mock_embed_model.MockEmbedding`) instead of the real e5 server (`rag`/`tools`/
    `observability` — the only modes that embed).
  - Vector store: an in-memory `VectorStoreIndex.from_documents()` over a tiny fixed stub
    corpus instead of the real Qdrant `arena_rag` collection — no `QdrantClient` connection is
    constructed at all (`rag`/`tools`/`observability`).
  - `memory`/`memory_scoped`/`isolation_leak`/`crash_recovery` already persist to a local
    on-disk SQLite file (never a network store), so they need no swap to be offline.
Both mock classes ship in `llama-index-core` (already a pinned dependency) — no new package is
added for this toggle. `MockLLM.chat()`/`.complete()` return deterministic non-network text
(echoes the prompt, or "text text ..." if `max_tokens` is set) — good enough to prove every mode
executes end-to-end offline, but NOT a stand-in for real model quality: `tools` mode's
`FunctionAgent` requires a `FunctionCallingLLM` and fails fast (cleanly, offline, no network
attempted) against a plain `MockLLM`; `structured` mode's `PydanticOutputParser` likewise fails
its JSON validation against MockLLM's non-JSON echoed text. Both failures are caught by `main()`
and reported as `error` in a valid JSON envelope — never a crash, never a network call.

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
import re
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

# Single arena tenant every corpus doc shares (matches the aisotropy adapter's
# arenaTenant + populate-qdrant.py's ARENA_TENANT). Applied as a native LlamaIndex
# MetadataFilter on the RAG retriever so this adapter does the SAME in-scan tenant
# filtering aisotropy pays (matched DURING the Qdrant HNSW scan, not a post-filter).
# LlamaIndex's QdrantVectorStore builds the qdrant FieldCondition with the raw
# filter key (key=subfilter.key, no `metadata.` prefix), matching the flat
# `tenant_id` payload key populate-qdrant.py seeds. Single-tenant corpus => the
# filter returns the same doc set an unfiltered top-k would; it only adds work.
ARENA_TENANT = os.environ.get("ARENA_TENANT", "arena")

MEMORY_DIR = Path(os.environ.get("LLAMAINDEX_MEMORY_DIR", str(SCRIPT_DIR / ".memory")))

# Offline stub toggle (see module docstring "Offline stub toggle") — swaps the agnes-backed LLM
# for llama_index.core.llms.MockLLM, the real e5/Qdrant RAG backends for llama_index.core.
# embeddings.MockEmbedding + an in-memory VectorStoreIndex, everywhere this adapter would
# otherwise make a network call. Never affects on-disk SQLite memory modes — those are already
# local-only.
ARENA_STUB = os.environ.get("ARENA_STUB", "").strip().lower() in ("1", "true", "yes")

# Sentinel appended to the outbound system prompt by `middleware` mode's INBOUND LLM wrapper —
# see run_middleware()'s docstring for the mechanism.
MIDDLEWARE_INBOUND_SENTINEL = "\n[ARENA_MW]"

VALID_MODES = {
    "qa",
    "rag",
    "tools",
    "memory",
    "structured",
    "stream",
    "memory_scoped",
    "isolation_leak",
    "observability",
    "crash_recovery",
    "middleware",
}


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
    if mode == "crash_recovery":
        # Always-present verdict fields for this mode, even on an early _fail() (e.g. missing
        # role/session_id) — the task contract requires these three keys at top level always.
        base.update(
            {
                "role": "",
                "resumed": False,
                "precrash_fact_recalled": False,
                "state_lost": False,
            }
        )
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
# ARENA_STUB helpers — offline replacements for the real e5-embed-server + Qdrant RAG backend.
# Used by run_rag(), corpus_lookup(), and run_observability() ONLY when ARENA_STUB is set; the
# real-backend code path in each of those is untouched so non-stub behavior is unchanged.
# --------------------------------------------------------------------------------------

_STUB_RAG_DOCS = [
    {
        "title": "Blue Guarantee Policy (ARENA_STUB)",
        "text": (
            "Nimbus Rail's Blue Guarantee gives passengers an automatic full refund when a "
            "trainset arrives more than 30 minutes late. The refund is sent to the original "
            "payment method within 5 business days."
        ),
    },
    {
        "title": "Nimbus Rail Network Overview (ARENA_STUB)",
        "text": (
            "Nimbus Rail operates high-speed trainsets across the northern corridor, with "
            "three daily departures connecting the capital to the coast."
        ),
    },
]


def _stub_embed_model() -> Any:
    """`llama_index.core.embeddings.MockEmbedding` — deterministic, in-process, no HTTP call to
    an embedding server. Ships in `llama-index-core` (already pinned); no new dependency."""
    from llama_index.core.embeddings import MockEmbedding

    return MockEmbedding(embed_dim=8)


def _stub_index(embed_model: Any) -> Any:
    """In-memory `VectorStoreIndex` seeded with a tiny fixed corpus — the ARENA_STUB replacement
    for the real `arena_rag` Qdrant collection. No `QdrantClient`/network connection is
    constructed anywhere in this path."""
    from llama_index.core import Document, VectorStoreIndex

    docs = [Document(text=d["text"], metadata={"title": d["title"]}) for d in _STUB_RAG_DOCS]
    return VectorStoreIndex.from_documents(docs, embed_model=embed_model)


# --------------------------------------------------------------------------------------
# ARENA_STUB function-calling LLM — offline replacement for agnes ONLY for the two modes whose
# real LlamaIndex machinery structurally requires a genuine FunctionCallingLLM: `tools`
# (FunctionAgent raises "LLM must be a FunctionCallingLLM" against a plain MockLLM) and
# `structured` (PydanticOutputParser fails to validate MockLLM's echoed-prompt text as JSON).
# Every other mode keeps using the plain llama_index.core.llms.MockLLM built in main() —
# unchanged.
#
# Built on llama_index.core.llms.mock.MockFunctionCallingLLM — LlamaIndex's OWN native mock
# base for exactly this contract (ships in llama-index-core, already pinned; no new
# dependency), not a hand-rolled fake. Only its `response_generator` callback below is adapter
# code, mirroring the same "canned decision, real execution" convention the eino and mastra
# arena adapters already use for their own stub tool-calling (arenas/live/eino/stub.go,
# arenas/live/mastra/lib/stub-model.mjs): the stub decides WHICH tool to call and WITH WHAT
# arguments deterministically — never "reasoning" — but the tool itself still genuinely
# executes: calculate("2 + 2") really evaluates to "4" via the real AST evaluator below.
# --------------------------------------------------------------------------------------

_STUB_SCHEMA_MARK = "Here's a JSON schema to follow:"
_STUB_SCHEMA_END_MARK = "\n\nOutput a valid JSON object"


def _stub_fabricate_from_schema(schema: Any) -> Any:
    """Deterministic placeholder value shaped to a JSON-Schema-7 fragment — never the result of
    any reasoning, the same convention as the Mastra adapter's stub-model.mjs `fabricate()`."""
    if not isinstance(schema, dict):
        return None
    if isinstance(schema.get("enum"), list) and schema["enum"]:
        return schema["enum"][0]
    schema_type = schema.get("type")
    if schema_type == "object" or "properties" in schema:
        return {
            key: _stub_fabricate_from_schema(sub)
            for key, sub in (schema.get("properties") or {}).items()
        }
    if schema_type == "array":
        return []
    if schema_type in ("number", "integer"):
        return 0
    if schema_type == "boolean":
        return True
    return "stub"


def _stub_extract_schema_json(text: str) -> dict[str, Any] | None:
    """Pull the JSON-Schema-7 object PydanticOutputParser.format() embeds in the prompt text
    (llama_index/core/output_parsers/pydantic.py::PYDANTIC_FORMAT_TMPL) so the stub can
    fabricate a matching object instead of echoing the prompt back as MockLLM does (which is
    never valid JSON, and is exactly why `structured` mode fails offline against plain
    MockLLM — see README.md "Offline stub toggle")."""
    if _STUB_SCHEMA_MARK not in text:
        return None
    after = text.split(_STUB_SCHEMA_MARK, 1)[1]
    schema_str = after.split(_STUB_SCHEMA_END_MARK, 1)[0].strip()
    # PydanticOutputParser.get_format_string(escape_json=True) doubles every brace
    # ("{"->"{{", "}"->"}}") so the schema survives PromptTemplate's own str.format() pass
    # untouched — verified empirically: PromptTemplate.format_messages() does NOT collapse the
    # doubled braces back down (the real agnes model still answers correctly despite reading
    # literally-doubled braces in its instructions; only this stub needs the schema parsed).
    # Undo that escaping before parsing.
    schema_str = schema_str.replace("{{", "{").replace("}}", "}")
    try:
        parsed = json.loads(schema_str)
    except (json.JSONDecodeError, ValueError):
        return None
    return parsed if isinstance(parsed, dict) else None


def _stub_tool_or_structured_response(messages: Any, **kwargs: Any) -> Any:
    """`response_generator` for the ARENA_STUB=1 `tools`/`structured` stub LLM (see module
    comment above). Branches, checked in order:

    1. Tools are bound AND no tool-result message is in the conversation yet -> emit a CANNED
       ToolCallBlock, preferring the "calculate" tool with a real, evaluable expression (the
       tool ACTUALLY runs on this; only the decision to call it is canned) — mirrors
       eino/mastra's identical convention for the identical tool shape.
    2. A tool result IS already in the conversation -> the tool-calling loop is done; reply with
       plain text summarizing it (no more tool calls, so FunctionAgent finalizes the turn) —
       the same "tool calls complete" shape LlamaIndex's OWN default MockFunctionCallingLLM
       generator uses (llama_index/core/llms/mock.py::_tool_calling_response_generator).
    3. Neither of the above, but the prompt carries a PydanticOutputParser JSON-schema
       instruction (`structured` mode) -> fabricate a JSON object shaped to that schema.
    4. Otherwise -> a fixed, unmistakably-synthetic plain-text reply.
    """
    from llama_index.core.base.llms.types import ToolCallBlock
    from llama_index.core.llms import ChatMessage, MessageRole

    tools = kwargs.get("tools") or []
    msg_list = list(messages) if messages else []
    has_tool_result = any(m.role == MessageRole.TOOL for m in msg_list)

    if tools and not has_tool_result:
        target = tools[0]
        for t in tools:
            if getattr(t.metadata, "name", "") == "calculate":
                target = t
                break
        tool_name = target.metadata.name or ""
        args: dict[str, Any] = {"expr": "2 + 2"} if tool_name == "calculate" else {}
        return ChatMessage(
            role=MessageRole.ASSISTANT,
            blocks=[
                ToolCallBlock(tool_call_id="arena-stub-call-1", tool_name=tool_name, tool_kwargs=args)
            ],
        )

    if has_tool_result:
        last_tool = next(m for m in reversed(msg_list) if m.role == MessageRole.TOOL)
        return ChatMessage(
            role=MessageRole.ASSISTANT,
            content=f"[ARENA_STUB] tool result: {last_tool.content or ''}",
        )

    last_text = msg_list[-1].content if msg_list else ""
    schema = _stub_extract_schema_json(last_text or "")
    if schema is not None:
        fabricated = _stub_fabricate_from_schema(schema)
        return ChatMessage(role=MessageRole.ASSISTANT, content=json.dumps(fabricated))

    return ChatMessage(role=MessageRole.ASSISTANT, content=f"[ARENA_STUB] {last_text}")


def _build_stub_function_calling_llm(callback_manager: Any) -> Any:
    """Constructs the ARENA_STUB=1 LLM used ONLY for `tools`/`structured` (see module comment
    above) — llama_index.core.llms.mock.MockFunctionCallingLLM, LlamaIndex's own native mock
    FunctionCallingLLM, driven by `_stub_tool_or_structured_response` above."""
    from llama_index.core.llms.mock import MockFunctionCallingLLM

    return MockFunctionCallingLLM(
        is_chat_model=True,
        callback_manager=callback_manager,
        response_generator=_stub_tool_or_structured_response,
    )


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
    if ARENA_STUB:
        # ARENA_STUB=1: MockEmbedding + in-memory stub corpus, no e5/Qdrant network call.
        embed_model = _stub_embed_model()
        index = _stub_index(embed_model)
        nodes = index.as_retriever(similarity_top_k=1).retrieve(query)
        if not nodes:
            return "No matching passage found in the Nimbus Rail knowledge base."
        node = nodes[0].node
        title = node.metadata.get("title", "unknown")
        return f"{title}: {node.get_content()}"

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

    # Native tenant payload filter for the REAL Qdrant path only. The in-memory stub
    # corpus (_stub_index) carries no tenant_id payload, so filtering there would
    # (correctly) return nothing — the stub proves wiring, not this filter.
    retriever_filters = None
    if ARENA_STUB:
        _progress("[rag] ARENA_STUB=1: MockEmbedding + in-memory stub corpus, no e5/Qdrant network calls")
        embed_model = _stub_embed_model()
        index = _stub_index(embed_model)
    else:
        _progress(f"[rag] embedding query via {EMBED_URL} (model={EMBED_QUERY_MODEL})")
        embed_model = OpenAILikeEmbedding(model_name=EMBED_QUERY_MODEL, api_base=EMBED_URL, api_key="none", timeout=30.0)

        _progress(f"[rag] connecting to Qdrant at {QDRANT_URL} collection={ARENA_COLLECTION!r}")
        client = QdrantClient(url=QDRANT_URL, check_compatibility=False)
        vector_store = QdrantVectorStore(client=client, collection_name=ARENA_COLLECTION)
        index = VectorStoreIndex.from_vector_store(vector_store=vector_store, embed_model=embed_model)

        # In-scan tenant filter — the SAME work aisotropy's semantic tier pays. The
        # QdrantVectorStore translates this MetadataFilter into a qdrant FieldCondition
        # matched DURING the HNSW scan (query_filter), keyed on the flat `tenant_id`
        # payload populate-qdrant.py seeds. Single-tenant corpus => same result set.
        from llama_index.core.vector_stores.types import (
            FilterOperator,
            MetadataFilter,
            MetadataFilters,
        )

        tenant = payload.get("tenant_id") or ARENA_TENANT
        retriever_filters = MetadataFilters(
            filters=[MetadataFilter(key="tenant_id", value=tenant, operator=FilterOperator.EQ)]
        )
        _progress(f"[rag] tenant payload filter: tenant_id={tenant!r} (in-scan, matches aisotropy)")

    retriever = index.as_retriever(similarity_top_k=3, filters=retriever_filters)

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

    # Emit the SAME object shape every other arena adapter emits and the runner
    # decodes into its `retrievedDoc` struct ({doc_id,title,score,text} — see
    # arenas/live/runner/types.go). hit@1 (ScoreRAG in
    # arenas/live/runner/scoring/scoring.go) compares the TOP entry's `doc_id`
    # against the gold `supporting_docs` (basenames like "08-blue-guarantee.md",
    # the SAME value populate-qdrant.py seeds as the flat `doc_id` payload key).
    # The prior code emitted a list of bare strings PREFERRING the human `title`
    # ("Blue Guarantee Policy"), which the runner could not read a doc_id from —
    # every grounded question scored miss@1. `doc_id` must come first.
    retrieved = [
        {
            "doc_id": sn.node.metadata.get("doc_id") or sn.node.node_id,
            "title": sn.node.metadata.get("title") or "",
            "text": sn.node.metadata.get("text") or sn.node.get_content() or "",
            "score": float(sn.score) if sn.score is not None else 0.0,
        }
        for sn in response.source_nodes
    ]
    _progress(f"[rag] retrieved {len(retrieved)} passage(s): {[r['doc_id'] for r in retrieved]}")

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

    Each invocation is one process: load history for `session_id` from disk, answer with that
    history plus ALL of this invocation's new turns in context, persist every new turn, exit.
    Two separate `run.sh` invocations with the same `session_id` therefore share real, on-disk
    state — not a process-local variable.

    An invocation may carry MORE than one new turn: the arena's persist invocation sends the
    whole conversation prefix (SPEC.md / runner `buildMemory`), e.g. for mem-02-pets
    `[user "I have a dog named Pepper.", assistant ..., user "I also have a cat named Miso.",
    assistant ...]`. Every one of those messages must be appended to the store, otherwise a
    fact stated in an EARLIER turn of the same invocation is silently lost and the later
    recall invocation cannot answer it. (This adapter previously persisted only the LAST user
    message of the payload, so "Pepper" never reached disk — verified in the on-disk store.)
    """
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.memory import Memory

    new_messages = [m for m in (payload.get("messages") or []) if (m.get("content") or "").strip()]
    if not any(m.get("role") == "user" for m in new_messages):
        raise RuntimeError("memory mode requires at least one user message")
    question = _last_user_message(payload)

    session_id = payload.get("session_id") or "arena-default"
    MEMORY_DIR.mkdir(parents=True, exist_ok=True)
    db_path = MEMORY_DIR / f"{session_id}.db"
    db_uri = f"sqlite+aiosqlite:///{db_path}"

    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use the conversation history to recall facts the user "
        "told you in earlier turns."
    )

    role_map = {"user": MessageRole.USER, "assistant": MessageRole.ASSISTANT}
    new_chat_messages = [
        ChatMessage(role=role_map.get(m.get("role", "user"), MessageRole.USER), content=m.get("content", ""))
        for m in new_messages
    ]

    async def _run() -> tuple[str, int]:
        memory = Memory.from_defaults(session_id=session_id, token_limit=8000, async_database_uri=db_uri)
        history = await memory.aget()
        messages = (
            [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
            + list(history)
            + new_chat_messages
        )
        response = await llm.achat(messages)
        reply_text = response.message.content or ""
        # Persist EVERY new turn this invocation carried (not just the last user one), then
        # the model's reply — otherwise an earlier fact in a multi-turn persist payload is
        # dropped and a later recall invocation can never see it.
        for msg in new_chat_messages:
            await memory.aput(msg)
        await memory.aput(ChatMessage(role=MessageRole.ASSISTANT, content=reply_text))
        return reply_text, len(history)

    _progress(f"[memory] session_id={session_id!r} db={db_path}")
    start = time.monotonic()
    reply_text, prior_turn_count = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)
    _progress(f"[memory] loaded {prior_turn_count} prior message(s) from disk before this turn")

    return {"reply": reply_text, "latency_ms": latency_ms}


def _scope_key(tenant_id: str, user_id: str, product_id: str, session_id: str | None = None) -> str:
    """Hand-rolled compound multi-tenant scope key.

    LlamaIndex's `Memory` (see `run_memory` above) has EXACTLY ONE native partition axis:
    `session_id`. There is no `tenant_id`/`user_id`/`product_id` concept anywhere in
    `llama_index.core.memory.Memory.from_defaults()`'s signature — multi-tenant isolation is
    NOT a framework feature here. It is entirely the responsibility of whatever string this
    adapter chooses to pass as `session_id`.

    `run_memory` above passes the raw, single-axis `session_id` straight through — that mode
    is explicitly NOT tenant-safe (see README "memory mode does not test isolation"): two
    different tenants sharing one `session_id` would land in the SAME on-disk SQLite file and
    read each other's history. This function is the fix for THIS pair of modes: it folds
    `tenant_id`, `user_id`, and `product_id` (plus an optional `session_id`) into one compound
    string and THAT is what gets passed as LlamaIndex's `session_id`. Get this compound wrong —
    omit `tenant_id`, typo it, or let two tenants collide on the same joined string — and the
    isolation silently disappears; nothing in LlamaIndex enforces it. Verified empirically: two
    `Memory` instances opened against the same on-disk file but different `session_id` strings
    return disjoint history (see README "Verified" note) — so a CORRECT compound key does
    isolate, but correctness is 100% on the adapter author, never on the framework.

    Contrast: aisotropy's own facts/semantic tiers enforce tenant scoping by construction —
    key-prefix on BadgerDB/rqlite reads and Qdrant in-HNSW-scan payload filtering, not a
    post-filter (`.claude/rules/constitutional-gates.md` "Multi-tenant data isolation"). There,
    forgetting the filter is not an available mistake because there is no code path without it.
    Here, forgetting IS available, and forgetting IS a leak.
    """
    parts = [str(tenant_id), str(user_id), str(product_id)]
    if session_id:
        parts.append(str(session_id))
    raw = "::".join(parts)
    # Sanitize for safe use as a filesystem path segment (db_path = MEMORY_DIR / f"{key}.db") —
    # untrusted tenant/user/product ids must not be able to escape MEMORY_DIR.
    return re.sub(r"[^A-Za-z0-9_.:-]", "_", raw)


async def _scope_write(scope_key: str, fact: str) -> Path:
    """Deterministic, no-LLM-call persist of `fact` into the compound-scope on-disk store.

    Mirrors `run_memory`'s on-disk wiring (same `Memory.from_defaults(...,
    async_database_uri=...)` call, same `MEMORY_DIR / f"{key}.db"` file-per-scope layout) but
    keyed by the hand-rolled compound `scope_key` instead of a raw `session_id`.
    """
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.memory import Memory

    MEMORY_DIR.mkdir(parents=True, exist_ok=True)
    db_path = MEMORY_DIR / f"{scope_key}.db"
    db_uri = f"sqlite+aiosqlite:///{db_path}"
    memory = Memory.from_defaults(session_id=scope_key, token_limit=8000, async_database_uri=db_uri)
    await memory.aput(ChatMessage(role=MessageRole.USER, content=fact))
    return db_path


async def _scope_read(scope_key: str) -> tuple[list[Any], Path]:
    """Load whatever chat history exists for `scope_key`.

    Returns an empty list when the scope's `.db` file was never written for this key — e.g. a
    different tenant's compound key never touches this file, and even when scopes share a
    physical file, `Memory` itself filters rows by `session_id` internally (verified directly:
    see README "Verified" note) — a second, independent layer under our own file-per-scope
    convention.
    """
    from llama_index.core.memory import Memory

    MEMORY_DIR.mkdir(parents=True, exist_ok=True)
    db_path = MEMORY_DIR / f"{scope_key}.db"
    db_uri = f"sqlite+aiosqlite:///{db_path}"
    memory = Memory.from_defaults(session_id=scope_key, token_limit=8000, async_database_uri=db_uri)
    history = await memory.aget()
    return history, db_path


def _history_contains(history: list[Any], needle: str) -> bool:
    return any(needle in (getattr(m, "content", None) or "") for m in history)


def run_memory_scoped(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Prove hand-rolled compound-scope multi-tenant memory isolation.

    Extends `run_memory`'s session_id-only on-disk store (see `_scope_key` docstring for why
    this is HAND-ROLLED, not framework-native) with a compound `(tenant_id, user_id,
    product_id[, session_id])` key. One invocation performs the full three-step proof:

      1. WRITE `fact` into scope (tenant_id, user_id, product_id[, session_id]) — deterministic
         disk write, no LLM call.
      2. RECALL under the SAME scope — deterministic store-level check that `fact` comes back
         (`same_scope_hit`), PLUS one real agnes recall turn grounded in that scope's history
         (`reply`), proving live recall through the LLM, not just raw disk I/O.
      3. RECALL under `cross_tenant_id` (same user_id/product_id/session_id, a DIFFERENT
         tenant_id) — deterministic store-level check that `fact` does NOT come back
         (`cross_tenant_miss`). No LLM call for this step: per design, the isolation verdict is
         read directly off the scoped store's returned history, not off an LLM's paraphrase of
         it — a paraphrase is a strictly weaker, fuzzier signal than a direct substring check
         against what was actually persisted and returned for that scope key.
    """
    tenant_id = payload.get("tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    session_id = payload.get("session_id") or None
    cross_tenant_id = payload.get("cross_tenant_id")
    fact = payload.get("fact")
    query = payload.get("query") or "What did I tell you earlier? Answer in one short sentence."

    missing = [
        name
        for name, val in (
            ("tenant_id", tenant_id),
            ("user_id", user_id),
            ("product_id", product_id),
            ("cross_tenant_id", cross_tenant_id),
            ("fact", fact),
        )
        if not val
    ]
    if missing:
        raise RuntimeError(f"memory_scoped mode requires: {', '.join(missing)}")

    same_key = _scope_key(tenant_id, user_id, product_id, session_id)
    cross_key = _scope_key(cross_tenant_id, user_id, product_id, session_id)
    if same_key == cross_key:
        raise RuntimeError(
            f"cross_tenant_id={cross_tenant_id!r} produced the SAME compound scope key as "
            f"tenant_id={tenant_id!r} — cannot test isolation against an identical scope"
        )

    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use the conversation history to recall facts the user "
        "told you in earlier turns. If you have no relevant history, say you don't know."
    )

    async def _run() -> tuple[str, list[Any], list[Any], Path, Path]:
        from llama_index.core.llms import ChatMessage, MessageRole

        await _scope_write(same_key, fact)
        same_history, same_db_path = await _scope_read(same_key)
        cross_history, cross_db_path = await _scope_read(cross_key)

        messages = (
            [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
            + list(same_history)
            + [ChatMessage(role=MessageRole.USER, content=query)]
        )
        response = await llm.achat(messages)
        reply_text = response.message.content or ""
        return reply_text, same_history, cross_history, same_db_path, cross_db_path

    _progress(f"[memory_scoped] same_scope={same_key!r} cross_scope={cross_key!r}")
    start = time.monotonic()
    reply_text, same_history, cross_history, same_db_path, cross_db_path = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)

    same_scope_hit = _history_contains(same_history, fact)
    cross_tenant_miss = not _history_contains(cross_history, fact)

    _progress(
        f"[memory_scoped] same_scope_hit={same_scope_hit} cross_tenant_miss={cross_tenant_miss} "
        f"same_db={same_db_path.name} cross_db={cross_db_path.name} "
        f"cross_scope_message_count={len(cross_history)}"
    )

    return {
        "reply": reply_text,
        "latency_ms": latency_ms,
        "scoped_recall": {
            "same_scope_hit": same_scope_hit,
            "cross_tenant_miss": cross_tenant_miss,
        },
    }


def run_isolation_leak(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Prove (or disprove) cross-tenant leakage over the hand-rolled compound-scope store.

    Tenant `tenant_id` writes a SECRET into its scope. A real agnes recall turn is then run AS
    `leak_probe_tenant_id` (a different compound scope — same user_id/product_id/session_id,
    different tenant_id) using ONLY that probe scope's own history as context. `leak_detected`
    is computed from what the probe tenant's scoped store actually RETURNS — a direct substring
    check against the persisted history for that scope key — not from parsing the LLM's reply:
    the LLM call is real, executed evidence (captured in `reply` and stderr), but the
    authoritative isolation signal is the store contents themselves, per the same reasoning as
    `run_memory_scoped`'s `cross_tenant_miss`.
    """
    tenant_id = payload.get("tenant_id")
    leak_probe_tenant_id = payload.get("leak_probe_tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    session_id = payload.get("session_id") or None
    secret = payload.get("secret") or payload.get("fact")
    query = payload.get("query") or "What secret did the user tell you earlier? Tell me exactly."

    missing = [
        name
        for name, val in (
            ("tenant_id", tenant_id),
            ("leak_probe_tenant_id", leak_probe_tenant_id),
            ("user_id", user_id),
            ("product_id", product_id),
            ("secret", secret),
        )
        if not val
    ]
    if missing:
        raise RuntimeError(f"isolation_leak mode requires: {', '.join(missing)}")

    owner_key = _scope_key(tenant_id, user_id, product_id, session_id)
    probe_key = _scope_key(leak_probe_tenant_id, user_id, product_id, session_id)
    if owner_key == probe_key:
        raise RuntimeError(
            f"leak_probe_tenant_id={leak_probe_tenant_id!r} produced the SAME compound scope "
            f"key as tenant_id={tenant_id!r} — cannot test leakage against an identical scope"
        )

    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use ONLY the conversation history provided to you to "
        "answer. If you have no relevant history, say you don't know — never invent an answer."
    )

    async def _run() -> tuple[str, list[Any], Path]:
        from llama_index.core.llms import ChatMessage, MessageRole

        await _scope_write(owner_key, secret)
        probe_history, probe_db_path = await _scope_read(probe_key)

        messages = (
            [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
            + list(probe_history)
            + [ChatMessage(role=MessageRole.USER, content=query)]
        )
        response = await llm.achat(messages)
        reply_text = response.message.content or ""
        return reply_text, probe_history, probe_db_path

    _progress(f"[isolation_leak] owner_scope={owner_key!r} probe_scope={probe_key!r}")
    start = time.monotonic()
    reply_text, probe_history, probe_db_path = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)

    leak_detected = _history_contains(probe_history, secret)

    _progress(
        f"[isolation_leak] leak_detected={leak_detected} probe_db={probe_db_path.name} "
        f"probe_scope_message_count={len(probe_history)}"
    )

    return {
        "reply": reply_text,
        "latency_ms": latency_ms,
        "leak_detected": leak_detected,
    }


# --------------------------------------------------------------------------------------
# crash_recovery — kill -9 durability of the SAME on-disk store `memory` mode uses
# --------------------------------------------------------------------------------------


def _crash_db_path(session_id: str) -> Path:
    """Same file-per-session_id convention as `run_memory` — this mode is NOT a different
    store, it is `memory` mode's exact store, driven by two OS processes instead of two shell
    invocations of the same script."""
    MEMORY_DIR.mkdir(parents=True, exist_ok=True)
    return MEMORY_DIR / f"{session_id}.db"


def run_crash_recovery(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Prove (or disprove) whether LlamaIndex's on-disk `Memory` store survives a HARD crash
    (`kill -9`) mid-conversation, as distinct from `memory` mode's clean-process-exit recall.

    Reuses `run_memory`'s exact wiring — same `Memory.from_defaults(session_id=..., async_
    database_uri="sqlite+aiosqlite:///<MEMORY_DIR>/<session_id>.db")` call, same on-disk file.
    The only thing new here is the two-role harness `test-crash.sh` drives as two REAL OS
    processes:

      role="process_a"  Runs N (>=2) sequential turns from `messages` against a real session_id
        db. Each turn's user+assistant pair is committed via `memory.aput()` — which internally
        `await session.commit()`s per call (`llama_index/core/storage/chat_store/sql.py::
        add_message`) — BEFORE the next turn starts. A `PROGRESS: CHECKPOINT_TURN_<n>_COMMITTED`
        line hits stderr the instant that commit returns, so `test-crash.sh` can synchronize a
        SIGKILL to land strictly after turn `crash_after_turn` (default 1) is durable and
        strictly before turn `crash_after_turn + 1` finishes. `inject_delay_ms` (test hook,
        default 0) sleeps that many ms at the very start of turn `crash_after_turn + 1`, BEFORE
        any further disk or network I/O — this converts an otherwise network-jitter-dependent
        race into a deterministic kill window without touching the durability mechanism under
        test (SQLite commit-before-continue). See README "Honest note" for exactly what this
        proves and does not prove.

      role="process_b"  A FRESH process (fresh Python interpreter, fresh `Memory` instance, no
        shared state with process_a beyond the session_id and the on-disk file) reopens the SAME
        db and reads history back with `memory.aget()`, then runs one real, live agnes
        continuation turn grounded in that recovered history.

    Verdict fields (top-level in the envelope; deterministic store-level checks, same discipline
    as `run_memory_scoped`/`run_isolation_leak` — never inferred from the LLM's reply alone):
      resumed                 process_b's history load succeeded (no exception) AND is non-empty
      precrash_fact_recalled  `expected_fact_substring` is present BOTH in the recovered on-disk
                               history (`fact_in_store`, deterministic) AND in the live agnes
                               continuation reply (`fact_in_reply`, functional proof)
      state_lost               True only if `expected_fact_substring` was requested but is
                               MISSING from the recovered history — a genuine durability failure;
                               always False when `fact_in_store` is True
      turn2_committed          whether `turn2_marker_substring` (the crashed turn's own text)
                               made it to disk anyway. Expected False. True would mean the kill
                               window did not actually land before that turn's commit — a
                               test-harness timing miss, not a framework finding.
    """
    role = payload.get("role")
    session_id = payload.get("session_id")
    if role not in ("process_a", "process_b"):
        raise RuntimeError("crash_recovery mode requires role: 'process_a' or 'process_b'")
    if not session_id:
        raise RuntimeError("crash_recovery mode requires session_id")

    if role == "process_a":
        return _crash_recovery_process_a(payload, llm, session_id)
    return _crash_recovery_process_b(payload, llm, session_id)


def _crash_recovery_process_a(payload: dict[str, Any], llm: Any, session_id: str) -> dict[str, Any]:
    """See `run_crash_recovery` docstring, role="process_a". Under a real `test-crash.sh` run
    this function's process is SIGKILLed mid-turn and NEVER reaches its own `return` — that is
    the entire point (a hard crash gives no chance to flush/log/emit anything further). The
    return value below only matters for a standalone, no-crash control invocation."""
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.memory import Memory

    turns = [m.get("content", "") for m in (payload.get("messages") or []) if m.get("role") == "user"]
    if len(turns) < 2:
        raise RuntimeError("crash_recovery process_a requires at least 2 user turns in messages")

    crash_after_turn = int(payload.get("crash_after_turn") or 1)
    inject_delay_ms = int(payload.get("inject_delay_ms") or 0)

    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use the conversation history to recall facts the user "
        "told you in earlier turns."
    )

    db_path = _crash_db_path(session_id)
    db_uri = f"sqlite+aiosqlite:///{db_path}"

    async def _run() -> int:
        memory = Memory.from_defaults(session_id=session_id, token_limit=8000, async_database_uri=db_uri)
        completed = 0
        for i, user_text in enumerate(turns, start=1):
            if i == crash_after_turn + 1 and inject_delay_ms > 0:
                _progress(
                    f"[crash_recovery] process_a: crash-injection delay {inject_delay_ms}ms "
                    f"before turn {i} (turn {crash_after_turn} already committed to disk)"
                )
                await asyncio.sleep(inject_delay_ms / 1000)

            history = await memory.aget()
            messages = (
                [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
                + list(history)
                + [ChatMessage(role=MessageRole.USER, content=user_text)]
            )
            _progress(f"[crash_recovery] process_a: turn {i} calling agnes (pid={os.getpid()})")
            response = await llm.achat(messages)
            reply_text = response.message.content or ""
            await memory.aput(ChatMessage(role=MessageRole.USER, content=user_text))
            await memory.aput(ChatMessage(role=MessageRole.ASSISTANT, content=reply_text))
            completed = i
            _progress(f"CHECKPOINT_TURN_{i}_COMMITTED session={session_id} pid={os.getpid()}")
        return completed

    _progress(
        f"[crash_recovery] process_a: session_id={session_id!r} db={db_path} turns={len(turns)} "
        f"crash_after_turn={crash_after_turn} inject_delay_ms={inject_delay_ms} pid={os.getpid()}"
    )
    start = time.monotonic()
    completed = asyncio.run(_run())
    latency_ms = int((time.monotonic() - start) * 1000)
    _progress(f"[crash_recovery] process_a: completed all {completed} turn(s) WITHOUT being killed")

    return {
        "reply": f"process_a completed {completed} turn(s) without being killed (no-crash control run)",
        "latency_ms": latency_ms,
        "role": "process_a",
        "resumed": False,
        "precrash_fact_recalled": False,
        "state_lost": False,
        "turns_completed": completed,
    }


def _crash_recovery_process_b(payload: dict[str, Any], llm: Any, session_id: str) -> dict[str, Any]:
    """See `run_crash_recovery` docstring, role="process_b". A fresh process, fresh `Memory`
    instance, reopening process_a's on-disk db after it was killed."""
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.memory import Memory

    query = payload.get("query") or "Do you remember what I told you earlier? Repeat it back to me exactly."
    expected_fact = payload.get("expected_fact_substring") or ""
    turn2_marker = payload.get("turn2_marker_substring") or ""
    system_prompt = payload.get("system") or (
        "You are a helpful assistant. Use the conversation history to recall facts the user "
        "told you in earlier turns."
    )

    db_path = _crash_db_path(session_id)
    db_uri = f"sqlite+aiosqlite:///{db_path}"

    async def _run() -> tuple[str, list[Any]]:
        memory = Memory.from_defaults(session_id=session_id, token_limit=8000, async_database_uri=db_uri)
        history = await memory.aget()
        messages = (
            [ChatMessage(role=MessageRole.SYSTEM, content=system_prompt)]
            + list(history)
            + [ChatMessage(role=MessageRole.USER, content=query)]
        )
        response = await llm.achat(messages)
        reply_text = response.message.content or ""
        # Persist this continuation turn too — process_b behaves like a real resumed session,
        # not a read-only probe.
        await memory.aput(ChatMessage(role=MessageRole.USER, content=query))
        await memory.aput(ChatMessage(role=MessageRole.ASSISTANT, content=reply_text))
        return reply_text, history

    _progress(
        f"[crash_recovery] process_b: session_id={session_id!r} db={db_path} "
        f"db_exists={db_path.exists()} pid={os.getpid()}"
    )
    start = time.monotonic()
    db_error = ""
    try:
        reply_text, history = asyncio.run(_run())
    except Exception as exc:  # a DB read/open failure IS the "state lost" signal, not a harness bug
        _progress(f"[crash_recovery] process_b: FAILED to load/read db: {exc}")
        reply_text, history, db_error = "", [], str(exc)
    latency_ms = int((time.monotonic() - start) * 1000)

    fact_in_store = bool(expected_fact) and _history_contains(history, expected_fact)
    fact_in_reply = bool(expected_fact) and (expected_fact.lower() in reply_text.lower())
    turn2_committed = bool(turn2_marker) and _history_contains(history, turn2_marker)

    resumed = db_error == "" and len(history) > 0
    precrash_fact_recalled = fact_in_store and fact_in_reply
    state_lost = bool(expected_fact) and not fact_in_store

    _progress(
        f"[crash_recovery] process_b: prior_messages={len(history)} fact_in_store={fact_in_store} "
        f"fact_in_reply={fact_in_reply} turn2_committed={turn2_committed} resumed={resumed} "
        f"precrash_fact_recalled={precrash_fact_recalled} state_lost={state_lost}"
    )

    return {
        "reply": reply_text,
        "latency_ms": latency_ms,
        "role": "process_b",
        "resumed": resumed,
        "precrash_fact_recalled": precrash_fact_recalled,
        "state_lost": state_lost,
        "turn2_committed": turn2_committed,
        "prior_message_count": len(history),
        "error": db_error,
    }


_SPAN_UUID_SUFFIX_RE = re.compile(
    r"-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", re.IGNORECASE
)


def _strip_span_uuid(span_id: str) -> str:
    """Native span ids are `{ClassName}.{method_name}-{uuid4}` (see `Dispatcher.span()` in
    `llama_index_instrumentation/dispatcher.py`: `id_ = f"{actual_class}.{method_name}-{uuid.
    uuid4()}"`). A uuid4 itself contains 4 internal dashes, so a naive `rpartition("-")` only
    strips the last of its 5 dash-groups — verified empirically the hard way (first pass at
    this function under-stripped, e.g. `RetrieverQueryEngine.query-95e65356-7d16-48fb-bae2`
    instead of `RetrieverQueryEngine.query`). This strips the FULL trailing uuid4 via regex so
    repeated calls to the same method collapse onto one readable span *name*, matching how the
    OTel/OpenInference side reports span `.name` (a stable string, no per-call uuid)."""
    return _SPAN_UUID_SUFFIX_RE.sub("", span_id)


def run_observability(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Measure LlamaIndex's NATIVE observability surface — spans, events, and OTel support.

    This mode is fundamentally different from every other mode above: it is not evaluating
    chat quality, retrieval correctness, or tool execution — it is evaluating what telemetry
    LlamaIndex itself emits while doing real work, and returns its OWN JSON shape (see the
    `return` at the bottom), not the uniform envelope `_envelope()` builds for every other mode
    (no `reply`/`tool_calls`/`retrieved`/`structured` fields make sense here).

    Two INDEPENDENT telemetry paths are wired around the SAME real turn so both observe
    identical work:

    1. **Native dispatcher** (`llama_index.core.instrumentation`, ships in `llama-index-core`,
       no extra dependency). Every LlamaIndex component method decorated with
       `dispatcher.span` (via `DispatcherSpanMixin`, e.g. `BaseRetriever.retrieve`,
       `BaseQueryEngine.query`, `BaseSynthesizer.synthesize`, `FunctionAgent` workflow steps)
       reports span-enter/span-exit calls up the dispatcher parent chain to the ROOT dispatcher
       (`get_dispatcher()` with no args — see `llama_index_instrumentation/__init__.py:
       get_dispatcher()`). A `SimpleSpanHandler` (ships in `llama-index-core`'s own
       `llama_index_instrumentation` package) is attached to the root dispatcher's
       `span_handlers` list and records every completed/dropped span with wall-clock
       `start_time`/`end_time`/`duration`. Separately, LlamaIndex's typed `BaseEvent` subclasses
       (`LLMChatStartEvent`, `RetrievalEndEvent`, `EmbeddingEndEvent`, etc.) are captured by a
       custom `BaseEventHandler` also attached to the root dispatcher's `event_handlers` list.
       This is the SAME mechanism LlamaIndex's own docs recommend for building an observability
       integration (`llama_index/core/instrumentation` is literally the seam OpenInference's own
       integration below is built on top of).

    2. **OTel via OpenInference** (`openinference-instrumentation-llama-index`, Apache-2.0,
       NOT bundled with `llama-index-core` — added as a new direct dependency for this mode
       only). `LlamaIndexInstrumentor().instrument(tracer_provider=...)` monkey-patches the
       SAME dispatcher-decorated methods (it is built on the native instrumentation dispatcher
       above, not a separate hook point) and emits real `opentelemetry.trace.Span` objects.
       Wired to a `TracerProvider` with a `SimpleSpanProcessor(InMemorySpanExporter())` —
       `InMemorySpanExporter` is OTel SDK's own in-process, no-network exporter
       (`opentelemetry.sdk.trace.export.in_memory_span_exporter`); nothing here ever attempts a
       network call to a collector.

    The representative turn is the SAME real RAG wiring `run_rag()` uses (real e5 embed server
    query embedding, real Qdrant `arena_rag` retrieval, real agnes chat completion) — a
    multi-op turn on purpose, so there is more than one span/event to observe.

    Returns (own JSON shape, NOT `_envelope()`'s):
        {
          "framework": "llamaindex", "mode": "observability",
          "otel_supported": bool,          # did LlamaIndexInstrumentor().instrument() succeed
                                            # AND produce >=1 OTel span for this turn?
          "span_count": int,               # native dispatcher spans captured
          "span_names": [str, ...],        # native span names, one per span, in start order
          "span_timings_ms": {name: ms},   # per-span-NAME wall-clock duration (last occurrence
                                            # wins if a name repeats — see docstring note below)
          "logs_structured": bool,         # did llama_index's stdlib logger emit structured
                                            # (JSON/key=value) records, or plain prose?
          "log_sample": str,               # one verbatim captured log line (or explanation if
                                            # none were emitted)
          "notes": str                     # honest summary: native vs OTel vs logging findings
        }
    """
    import logging

    from llama_index.core import PromptTemplate, VectorStoreIndex
    from llama_index.core.instrumentation import get_dispatcher
    from llama_index.core.instrumentation.event_handlers.base import BaseEventHandler
    from llama_index.core.instrumentation.span_handlers import SimpleSpanHandler
    from llama_index.core.query_engine import RetrieverQueryEngine
    from llama_index.embeddings.openai_like import OpenAILikeEmbedding
    from llama_index.vector_stores.qdrant import QdrantVectorStore
    from qdrant_client import QdrantClient

    # ---- (1) native instrumentation: span handler + event handler on the ROOT dispatcher ----
    span_handler = SimpleSpanHandler()
    captured_events: list[dict[str, Any]] = []

    class _ArenaEventCapture(BaseEventHandler):
        @classmethod
        def class_name(cls) -> str:
            return "ArenaEventCapture"

        def handle(self, event: Any, **kwargs: Any) -> Any:
            captured_events.append({"class": type(event).__name__, "id": event.id_})

    event_handler = _ArenaEventCapture()
    dispatcher = get_dispatcher()  # root dispatcher — every module dispatcher propagates here
    dispatcher.add_span_handler(span_handler)
    dispatcher.add_event_handler(event_handler)
    _progress(
        "[observability] attached SimpleSpanHandler + custom BaseEventHandler to the root "
        "instrumentation dispatcher (llama_index.core.instrumentation.get_dispatcher())"
    )

    # ---- (2) OTel via OpenInference, wired to an in-process InMemorySpanExporter only ----
    otel_supported = False
    memory_exporter = None
    instrumentor = None
    otel_error = ""
    try:
        from openinference.instrumentation.llama_index import LlamaIndexInstrumentor
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import SimpleSpanProcessor
        from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

        memory_exporter = InMemorySpanExporter()
        tracer_provider = TracerProvider()
        tracer_provider.add_span_processor(SimpleSpanProcessor(memory_exporter))
        instrumentor = LlamaIndexInstrumentor()
        instrumentor.instrument(tracer_provider=tracer_provider)
        otel_supported = True
        _progress(
            "[observability] OpenInference LlamaIndexInstrumentor wired to an in-process "
            "opentelemetry InMemorySpanExporter (SimpleSpanProcessor, NO external collector)"
        )
    except Exception as exc:
        otel_error = f"{type(exc).__name__}: {exc}"
        _progress(f"WARNING: [observability] OTel/OpenInference wiring failed: {otel_error}")

    # ---- structured-logging capture: attach a handler to llama_index's stdlib logger tree ----
    log_records: list[str] = []

    class _ListLogHandler(logging.Handler):
        def emit(self, record: logging.LogRecord) -> None:
            log_records.append(self.format(record))

    li_logger = logging.getLogger("llama_index")
    prior_level = li_logger.level
    list_handler = _ListLogHandler()
    list_handler.setFormatter(logging.Formatter("%(name)s %(levelname)s %(message)s"))
    li_logger.addHandler(list_handler)
    li_logger.setLevel(logging.DEBUG)

    # ---- run ONE representative, multi-op RAG turn against REAL infra ----
    question = _last_user_message(payload) or (
        "How late does a Nimbus Rail trainset have to be before the Blue Guarantee gives a "
        "passenger an automatic full refund, and where is the refund sent?"
    )

    if ARENA_STUB:
        _progress(f"[observability] ARENA_STUB=1: MockEmbedding + in-memory stub corpus, no e5/Qdrant network calls: {question!r}")
        embed_model = _stub_embed_model()
        index = _stub_index(embed_model)
    else:
        _progress(f"[observability] embed+retrieve+synthesize via real e5/Qdrant/agnes: {question!r}")
        embed_model = OpenAILikeEmbedding(model_name=EMBED_QUERY_MODEL, api_base=EMBED_URL, api_key="none", timeout=30.0)
        client = QdrantClient(url=QDRANT_URL, check_compatibility=False)
        vector_store = QdrantVectorStore(client=client, collection_name=ARENA_COLLECTION)
        index = VectorStoreIndex.from_vector_store(vector_store=vector_store, embed_model=embed_model)
    retriever = index.as_retriever(similarity_top_k=3)
    qa_template = PromptTemplate(
        "Answer ONLY from the retrieved context below. If the answer is not present, say "
        "exactly: I don't know.\n\nContext information is below.\n---------------------\n"
        "{context_str}\n---------------------\nQuestion: {query_str}\nAnswer: "
    )
    query_engine = RetrieverQueryEngine.from_args(retriever=retriever, llm=llm, text_qa_template=qa_template)

    try:
        response = query_engine.query(question)
        reply_text = str(response)
    finally:
        # ---- teardown: detach everything so this one-shot CLI process doesn't leak state ----
        li_logger.removeHandler(list_handler)
        li_logger.setLevel(prior_level)
        if instrumentor is not None:
            try:
                instrumentor.uninstrument()
            except Exception as exc:
                _progress(f"WARNING: [observability] instrumentor.uninstrument() failed non-fatally: {exc}")

    # ---- summarize native dispatcher spans ----
    all_spans = list(span_handler.completed_spans) + list(span_handler.dropped_spans)
    all_spans.sort(key=lambda s: s.start_time)
    span_names = [_strip_span_uuid(s.id_) for s in all_spans]
    span_timings_ms: dict[str, float] = {}
    for span, name in zip(all_spans, span_names):
        duration_s = getattr(span, "duration", None)
        span_timings_ms[name] = round(duration_s * 1000, 2) if duration_s else 0.0

    distinct_event_types = sorted({e["class"] for e in captured_events})
    _progress(
        f"[observability] native: {len(all_spans)} span(s) ({len(set(span_names))} distinct "
        f"name(s)), {len(captured_events)} event(s) ({len(distinct_event_types)} distinct "
        f"type(s)): {distinct_event_types}"
    )

    # ---- summarize OTel spans (if wiring succeeded) ----
    otel_span_count = 0
    otel_span_names: list[str] = []
    otel_span_kinds: list[str] = []
    if memory_exporter is not None:
        otel_spans = memory_exporter.get_finished_spans()
        otel_span_count = len(otel_spans)
        otel_span_names = [sp.name for sp in otel_spans]
        # openinference.span.kind is the OpenInference semantic-convention attribute (LLM,
        # RETRIEVER, EMBEDDING, CHAIN, ...) — checked directly off the real captured spans, not
        # assumed from docs.
        otel_span_kinds = sorted(
            {
                sp.attributes.get("openinference.span.kind")
                for sp in otel_spans
                if sp.attributes and sp.attributes.get("openinference.span.kind")
            }
        )
        otel_supported = otel_supported and otel_span_count > 0
        _progress(
            f"[observability] OTel/OpenInference: {otel_span_count} span(s), kinds="
            f"{otel_span_kinds}: {otel_span_names}"
        )

    # ---- summarize structured-logging: llama_index's stdlib logger emits plain prose, never
    # JSON/key=value structured records — verified empirically here, not asserted from docs.
    if log_records:
        log_sample = log_records[0]
        stripped = log_sample.split(" ", 2)[-1].strip() if " " in log_sample else log_sample
        logs_structured = stripped.startswith("{") or "=" in stripped.split(" ")[0]
    else:
        log_sample = "(no llama_index stdlib logger records emitted at DEBUG during this turn)"
        logs_structured = False

    notes = (
        f"Native instrumentation (llama_index.core.instrumentation, bundled in "
        f"llama-index-core, zero extra deps): {len(all_spans)} span(s) via SimpleSpanHandler + "
        f"{len(captured_events)} event(s) ({len(distinct_event_types)} distinct typed BaseEvent "
        f"subclasses: {distinct_event_types}) via a custom BaseEventHandler, both attached to "
        f"the ROOT dispatcher (llama_index.core.instrumentation.get_dispatcher()). OTel: "
        f"openinference-instrumentation-llama-index (Apache-2.0, NOT bundled with "
        f"llama-index-core — added as a new direct dependency) "
        f"{'wired successfully, ' + str(otel_span_count) + ' span(s) captured' if otel_supported else 'did NOT produce OTel spans (' + (otel_error or 'zero spans exported') + ')'} "
        f"via an in-process opentelemetry.sdk.trace.export.in_memory_span_exporter."
        f"InMemorySpanExporter + SimpleSpanProcessor (no external collector/endpoint "
        f"configured, verified: no network export code path was exercised). Observed directly "
        f"on the real captured spans (not assumed): OTel span NAMES are IDENTICAL strings to "
        f"the native dispatcher's span names (both 'ClassName.method_name', e.g. "
        f"'RetrieverQueryEngine.query') — OpenInference's LlamaIndexInstrumentor is itself built "
        f"on top of this SAME native dispatcher and does not rename spans; the semantic layer "
        f"OpenInference adds instead lives in span ATTRIBUTES, specifically "
        f"'openinference.span.kind' = {otel_span_kinds or '(none found on any span)'}. Logging: "
        f"llama_index's stdlib logger emitted {len(log_records)} record(s) at DEBUG for this "
        f"turn; sample is plain prose, not structured JSON/kv (logs_structured=False is the "
        f"honest reading of what was captured, not an assumption). reply={reply_text!r}"
    )

    return {
        "framework": FRAMEWORK,
        "mode": "observability",
        "otel_supported": otel_supported,
        "span_count": len(all_spans),
        "span_names": span_names,
        "span_timings_ms": span_timings_ms,
        "logs_structured": logs_structured,
        "log_sample": log_sample,
        "notes": notes,
    }


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


# --------------------------------------------------------------------------------------
# middleware — LlamaIndex NATIVE interception: an LLM wrapper (inbound mutation) + the
# observe-only instrumentation dispatcher (outbound call counting).
# --------------------------------------------------------------------------------------


def run_middleware(payload: dict[str, Any], llm: Any) -> dict[str, Any]:
    """Two independent, HONEST interception mechanisms around ONE real qa turn.

    LlamaIndex's `llama_index.core.instrumentation` dispatcher — the same seam `observability`
    mode above uses — is OBSERVE-ONLY: a `BaseEventHandler.handle()` receives a `BaseEvent`
    instance (e.g. `LLMChatStartEvent`) as a side-effecting listener; there is no supported way
    for a handler to rewrite the request that produced the event. There is NO native LlamaIndex
    mechanism to mutate a prompt from an event handler. So this mode uses TWO DIFFERENT
    mechanisms for its two DIFFERENT transforms, and says so honestly instead of overclaiming
    one mechanism does both:

      INBOUND (prompt mutation) -> LLM WRAPPER. `_MiddlewareLLM` below subclasses
        `llama_index.core.llms.llm.LLM` — the SAME abstract base every real LlamaIndex LLM
        integration subclasses (e.g. `llama_index.llms.openai_like.OpenAILike`, which `main()`
        wires as `llm` for every other mode in this file). It wraps the real `llm` passed in,
        and its `chat`/`achat`/`stream_chat`/`astream_chat` append the sentinel
        `MIDDLEWARE_INBOUND_SENTINEL` ("\\n[ARENA_MW]") to the outbound system `ChatMessage`
        (injecting one if none was provided) before delegating to the wrapped LLM. This is a
        genuine LlamaIndex extension point, not a monkeypatch — every other abstract method
        (`complete`/`acomplete`/`stream_complete`/`astream_complete`/`metadata`) delegates to
        the wrapped LLM unchanged.

      OUTBOUND (call counting) -> INSTRUMENTATION EVENT HANDLER. A `BaseEventHandler` is
        attached to the ROOT dispatcher (`get_dispatcher()`, same call `run_observability()`
        uses) and counts every `LLMChatStartEvent` it sees. `llm_chat_callback()` — the
        decorator every `LLM.chat()`/`.achat()` implementation carries, including the wrapped
        real LLM's — fires `dispatcher.event(LLMChatStartEvent(...))` on every call
        (`llama_index/core/llms/callbacks.py::llm_chat_callback`, verified directly against
        this adapter's pinned `llama-index-core` install, not assumed from docs). The handler
        OBSERVES the call the wrapper above triggers; per the dispatcher's own design it cannot
        alter it — that is the honest reading `mechanism` reports.

    Returns its OWN JSON shape (like `observability` mode) — `text` + `middleware`, not the
    uniform qa/rag/tools-shaped envelope `_envelope()` builds.
    """
    from pydantic import PrivateAttr

    from llama_index.core.instrumentation import get_dispatcher
    from llama_index.core.instrumentation.event_handlers.base import BaseEventHandler
    from llama_index.core.instrumentation.events.llm import LLMChatStartEvent
    from llama_index.core.llms import ChatMessage, MessageRole
    from llama_index.core.llms.llm import LLM as LlamaLLM

    system_prompt = payload.get("system") or ""
    messages_in = payload.get("messages") or []
    chat_messages = _build_chat_messages(system_prompt, messages_in)

    class _MiddlewareLLM(LlamaLLM):
        """INBOUND mechanism: a thin wrapper subclassing LlamaIndex's `LLM` base. See
        run_middleware()'s docstring for why the observe-only instrumentation dispatcher
        cannot do this job instead."""

        _inner: Any = PrivateAttr()
        _sentinel: str = PrivateAttr()
        _inbound_fired: bool = PrivateAttr(default=False)

        def __init__(self, inner: Any, sentinel: str) -> None:
            super().__init__()
            self._inner = inner
            self._sentinel = sentinel

        @property
        def inbound_fired(self) -> bool:
            return self._inbound_fired

        @classmethod
        def class_name(cls) -> str:
            return "ArenaMiddlewareLLM"

        @property
        def metadata(self) -> Any:
            return self._inner.metadata

        def _mutate(self, messages: Any) -> list[Any]:
            mutated = list(messages)
            for i, m in enumerate(mutated):
                if m.role == MessageRole.SYSTEM:
                    mutated[i] = ChatMessage(role=MessageRole.SYSTEM, content=(m.content or "") + self._sentinel)
                    self._inbound_fired = True
                    return mutated
            mutated.insert(0, ChatMessage(role=MessageRole.SYSTEM, content=self._sentinel))
            self._inbound_fired = True
            return mutated

        def chat(self, messages: Any, **kwargs: Any) -> Any:
            return self._inner.chat(self._mutate(messages), **kwargs)

        async def achat(self, messages: Any, **kwargs: Any) -> Any:
            return await self._inner.achat(self._mutate(messages), **kwargs)

        def stream_chat(self, messages: Any, **kwargs: Any) -> Any:
            return self._inner.stream_chat(self._mutate(messages), **kwargs)

        async def astream_chat(self, messages: Any, **kwargs: Any) -> Any:
            return await self._inner.astream_chat(self._mutate(messages), **kwargs)

        def complete(self, prompt: str, formatted: bool = False, **kwargs: Any) -> Any:
            return self._inner.complete(prompt, formatted=formatted, **kwargs)

        async def acomplete(self, prompt: str, formatted: bool = False, **kwargs: Any) -> Any:
            return await self._inner.acomplete(prompt, formatted=formatted, **kwargs)

        def stream_complete(self, prompt: str, formatted: bool = False, **kwargs: Any) -> Any:
            return self._inner.stream_complete(prompt, formatted=formatted, **kwargs)

        async def astream_complete(self, prompt: str, formatted: bool = False, **kwargs: Any) -> Any:
            return await self._inner.astream_complete(prompt, formatted=formatted, **kwargs)

    invocations = {"n": 0}

    class _MWOutboundCounter(BaseEventHandler):
        """OUTBOUND mechanism: observe-only instrumentation event handler, counts real LLM
        calls. Never mutates anything — see run_middleware()'s docstring."""

        @classmethod
        def class_name(cls) -> str:
            return "ArenaMiddlewareOutboundCounter"

        def handle(self, event: Any, **kwargs: Any) -> Any:
            if isinstance(event, LLMChatStartEvent):
                invocations["n"] += 1

    dispatcher = get_dispatcher()
    outbound_handler = _MWOutboundCounter()
    dispatcher.add_event_handler(outbound_handler)

    mw_llm = _MiddlewareLLM(llm, MIDDLEWARE_INBOUND_SENTINEL)

    _progress(
        "[middleware] inbound=llm-wrapper (_MiddlewareLLM subclasses llama_index.core.llms.llm.LLM) "
        "outbound=instrumentation-handlers (BaseEventHandler on the root dispatcher, observe-only)"
    )
    try:
        response = mw_llm.chat(chat_messages)
    finally:
        dispatcher.event_handlers.remove(outbound_handler)

    reply_text = response.message.content or ""
    outbound_fired = invocations["n"] > 0

    _progress(
        f"[middleware] inbound_fired={mw_llm.inbound_fired} outbound_fired={outbound_fired} "
        f"invocations={invocations['n']}"
    )

    return {
        "framework": FRAMEWORK,
        "mode": "middleware",
        "text": reply_text,
        "middleware": {
            "mechanism": "llm-wrapper (inbound) + instrumentation-handlers (outbound)",
            "inbound_fired": mw_llm.inbound_fired,
            "outbound_fired": outbound_fired,
            "invocations": invocations["n"],
            "supported": True,
        },
    }


MODE_HANDLERS: dict[str, Callable[[dict[str, Any], Any], dict[str, Any]]] = {
    "qa": run_qa,
    "rag": run_rag,
    "tools": run_tools,
    "memory": run_memory,
    "structured": run_structured,
    "stream": run_stream,
    "memory_scoped": run_memory_scoped,
    "isolation_leak": run_isolation_leak,
    "observability": run_observability,
    "crash_recovery": run_crash_recovery,
    "middleware": run_middleware,
}

# Modes whose handler returns its OWN complete JSON shape (not envelope overrides to merge
# into `_envelope()`) — `main()` emits their return value verbatim instead of building the
# uniform envelope. See `run_observability()`'s / `run_middleware()`'s docstrings for why these
# modes differ.
SELF_CONTAINED_MODES = {"observability", "middleware"}


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

    if not ARENA_STUB:
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
    else:
        # ARENA_STUB=1 (see module docstring "Offline stub toggle"): OPENAI_*/EMBED_URL/
        # QDRANT_URL are irrelevant — every backend is swapped for an in-process LlamaIndex
        # mock, so no OPENAI_* env var is required. Force the reported model name to reflect
        # what actually served the request (a leftover OPENAI_MODEL in the environment must
        # never make the envelope claim a real model handled this call).
        model_name = "mock-llm"

    try:
        from llama_index.core import Settings

        if ARENA_STUB:
            from llama_index.core.llms import MockLLM
        else:
            from llama_index.llms.openai_like import OpenAILike
    except Exception as exc:  # import-time failure (bad install, etc.)
        _fail(mode, f"failed to import llama_index: {exc}", model_name)
        return

    usage = _UsageCapture()
    cb_manager = usage.build_callback_manager()
    # Global fallback for components that don't inherit callback_manager off `llm` (see
    # _UsageCapture.build_callback_manager docstring) — e.g. RetrieverQueryEngine.from_args.
    Settings.callback_manager = cb_manager

    if ARENA_STUB:
        if mode in ("tools", "structured"):
            # See "ARENA_STUB function-calling LLM" module comment above _stub_fabricate_from_schema:
            # a plain MockLLM cannot satisfy FunctionAgent's FunctionCallingLLM requirement
            # (`tools`) nor produce valid schema-shaped JSON (`structured`) — both need the
            # native MockFunctionCallingLLM-based stub instead.
            _progress(
                f"mode={mode} ARENA_STUB=1: wiring llama_index.core.llms.mock."
                f"MockFunctionCallingLLM (canned tool-call / schema-fabricated JSON) — no "
                f"OPENAI_*/EMBED_URL/QDRANT_URL network calls"
            )
            llm = _build_stub_function_calling_llm(cb_manager)
        else:
            _progress(
                f"mode={mode} ARENA_STUB=1: wiring llama_index.core.llms.MockLLM — no OPENAI_*/"
                f"EMBED_URL/QDRANT_URL network calls in any mode"
            )
            llm = MockLLM(is_chat_model=True, callback_manager=cb_manager)
    else:
        _progress(
            f"mode={mode} wiring OpenAILike model={model_name!r} base_url={base_url!r} "
            f"embed_url={EMBED_URL!r} qdrant_url={QDRANT_URL!r}"
        )
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

    if mode in SELF_CONTAINED_MODES:
        # This mode's handler already returned its complete, self-describing JSON shape (see
        # MODE_HANDLERS / SELF_CONTAINED_MODES comment above) — emit it verbatim, do not force
        # it through the qa/rag/tools-shaped `_envelope()`.
        if mode == "middleware":
            _progress(f"done: mode={mode} middleware={result.get('middleware')}")
        else:
            _progress(
                f"done: mode={mode} span_count={result.get('span_count')} "
                f"otel_supported={result.get('otel_supported')} logs_structured={result.get('logs_structured')}"
            )
        _emit(result)
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
