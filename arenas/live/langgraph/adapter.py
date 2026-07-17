#!/usr/bin/env python3
"""LangGraph live-arena adapter — multi-mode.

Reads ONE JSON request object from stdin, drives it through a real
LangGraph graph appropriate to the requested ``"mode"`` against the real
agnes-2.0-flash model (and, for ``rag``, the real e5 embed server + Qdrant),
and writes ONE JSON envelope to stdout. This is part of a family of "live
arena" adapters that all speak the same stdin/stdout contract so different
agent frameworks can be compared against the SAME real model — see
``SPEC.md`` in ``arenas/live/`` for the base single-mode contract this
extends, and ``README.md`` in this directory for the full multi-mode
contract.

Modes (dispatched by the request's ``"mode"`` field, default ``"qa"``):
    qa             - plain single-turn chat (base contract, unchanged).
    rag            - retrieval-augmented generation over Qdrant `arena_rag`.
    tools          - a LangGraph ReAct loop that actually EXECUTES a tool.
    memory         - a LangGraph checkpointer persisted across processes,
                      keyed by a bare ``thread_id`` (no multi-tenant scope).
    crash_recovery - like `memory`, but proves survival of a real `kill -9`
                      mid-turn, not just a clean process exit. See
                      modes/crash_recovery.py and test-crash.sh.
    structured     - ChatOpenAI.with_structured_output(schema).
    stream         - real token streaming via graph.stream(...), reports
                      ttft_ms.
    memory_scoped  - hand-rolled compound-scope memory
                      (tenant_id, user_id, product_id[, session_id]) over a
                      small on-disk store; proves same-scope recall hits and
                      cross-tenant recall misses. No LLM call.
    isolation_leak - a REAL agnes turn answered as a different tenant than
                      the one that wrote a secret fact, to prove the secret
                      never reaches that tenant's prompt.
    observability  - bridges LangChain's native callback stream to real
                      in-process OpenTelemetry spans (InMemorySpanExporter,
                      no external collector) over a real ReAct tools turn,
                      and reports what LangGraph's native surface emits.
                      See modes/observability.py.

Security: never print, log, or persist the API key. All diagnostics go to
stderr; stdout carries exactly one JSON object and nothing else.
"""
from __future__ import annotations

import json
import os
import sys

from modes import (
    common,
    crash_recovery,
    isolation_leak,
    memory,
    memory_scoped,
    observability,
    qa,
    rag,
    stream,
    structured,
    tools,
)

MODES = {
    "qa": qa.run,
    "rag": rag.run,
    "tools": tools.run,
    "memory": memory.run,
    "crash_recovery": crash_recovery.run,
    "structured": structured.run,
    "stream": stream.run,
    "memory_scoped": memory_scoped.run,
    "isolation_leak": isolation_leak.run,
    "observability": observability.run,
}


def main() -> int:
    raw = sys.stdin.read()
    model_name = os.environ.get("OPENAI_MODEL", common.DEFAULT_MODEL)

    try:
        payload = json.loads(raw)
    except Exception as exc:
        common.log(f"failed to parse stdin as JSON: {exc}")
        print(json.dumps(common.envelope(mode="unknown", model=model_name, error=f"invalid JSON on stdin: {exc}")))
        return 0

    if not isinstance(payload, dict):
        print(json.dumps(common.envelope(mode="unknown", model=model_name, error="stdin JSON must be an object")))
        return 0

    mode = payload.get("mode") or "qa"
    handler = MODES.get(mode)
    if handler is None:
        print(
            json.dumps(
                common.envelope(
                    mode=mode,
                    model=model_name,
                    error=f"unknown mode {mode!r}; supported: {sorted(MODES)}",
                )
            )
        )
        return 0

    common.log(f"PROGRESS: langgraph adapter running mode={mode!r}")
    envelope = handler(payload)
    common.log(f"PROGRESS: langgraph adapter mode={mode!r} done error={envelope.get('error') or '(none)'!r}")
    print(json.dumps(envelope))
    return 0


if __name__ == "__main__":
    sys.exit(main())
