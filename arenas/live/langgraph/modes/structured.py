"""``structured`` mode — ``ChatOpenAI.with_structured_output`` in a graph node.

Validates the model's output against a JSON Schema via LangChain's native
structured-output support (``method="json_schema"``, which uses the
provider's native structured-output/JSON-mode machinery rather than a
prompt-only convention). ``include_raw=True`` is used so the real provider
token usage on the underlying ``AIMessage`` is still available — the plain
``with_structured_output(...).invoke(...)`` shortcut discards the raw
message and would force fabricating token counts, which the base contract
forbids.

The request may supply its own ``"schema"`` (a JSON Schema object); if
omitted, a small default schema is used so the mode is runnable without
extra configuration.
"""
from __future__ import annotations

import json
import time
import traceback
from typing import Any, TypedDict

from . import common

MODE = "structured"

DEFAULT_SCHEMA: dict[str, Any] = {
    "title": "Answer",
    "description": "A direct answer to the user's question with a self-reported confidence.",
    "type": "object",
    "properties": {
        "answer": {"type": "string", "description": "The direct answer, as short as possible."},
        "confidence": {"type": "number", "description": "Self-reported confidence from 0.0 to 1.0."},
    },
    "required": ["answer", "confidence"],
}


class StructuredState(TypedDict):
    parsed: dict[str, Any] | None
    reply: str
    input_tokens: int
    output_tokens: int
    parsing_error: str


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)
    if err:
        return err

    try:
        from langgraph.graph import END, START, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        messages = common.build_messages(payload)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"invalid request payload: {exc}")
    if not messages:
        return common.envelope(mode=MODE, model=model_name, error="no messages in request")

    schema = payload.get("schema") or DEFAULT_SCHEMA

    try:
        llm = common.build_chat_model(
            model_name,
            api_key,
            base_url,
            # Canned JSON reply matching DEFAULT_SCHEMA, used only when
            # ARENA_STUB=1 AND the caller did not supply a custom "schema"
            # (see the stub branch in call_model below).
            responses=['{"answer": "OK (ARENA_STUB offline stub reply)", "confidence": 1.0}'],
        )

        if common.is_stub():
            # FakeListChatModel (the stub chat model) inherits
            # BaseChatModel's default with_structured_output(), which
            # raises NotImplementedError (verified against this arena's
            # pinned langchain-core==1.4.9 — it only builds on top of
            # bind_tools()/native structured-output support neither of
            # which the stub implements). Offline path: invoke the stub
            # plainly and best-effort JSON-parse its canned reply instead
            # of exercising the real method="json_schema" native path.
            # This is an honest, documented reduction in fidelity — it does
            # NOT claim real provider-side schema validation occurred.
            def call_model(_state: StructuredState) -> dict[str, Any]:
                raw = llm.invoke(messages)
                content = raw.content if isinstance(raw.content, str) else str(raw.content)
                try:
                    parsed: Any = json.loads(content)
                except Exception:
                    parsed = {k: None for k in schema.get("required", [])}
                inp, outp = common.extract_usage(raw)
                return {
                    "parsed": parsed,
                    "reply": content,
                    "input_tokens": inp,
                    "output_tokens": outp,
                    "parsing_error": "",
                }
        else:
            # PROVIDER-ADAPTIVE, deliberately not pinned to one vendor.
            #
            # LangChain's method="json_schema" path sends
            # `response_format.json_schema.strict: false` by default — the value
            # OpenAI documents as ITS default. poolside's Laguna endpoint
            # REJECTS exactly that value with HTTP 400 while accepting both
            # `true` and `null`. Measured 2026-07-27 by capturing the real
            # outbound body with a forwarding proxy, then bisecting the field:
            #
            #     strict: false -> 400     strict: true -> 200     strict: null -> 200
            #
            # Nothing else in the payload mattered: every other shape (with and
            # without `description`, nested schema descriptions,
            # additionalProperties, streaming, and the /responses endpoint) is
            # accepted. So this is one boolean, validated backwards by one
            # provider — and without compensation the arena scores LangChain 0
            # on `structured` for something LangChain did not do wrong.
            #
            # WHY A FALLBACK RATHER THAN strict=True. This repo targets MORE THAN
            # ONE OpenAI-compatible provider (poolside and Agnes AI today, any
            # other tomorrow — see docs/architecture/model-provider.md). Hardcoding
            # the flag would trade a poolside failure for a possible failure
            # somewhere else, and would silently encode one vendor's quirk as if
            # it were the protocol. So the framework's OWN default is attempted
            # first, and only a provider that rejects it gets the second attempt.
            # On a conformant provider this branch never runs and the behaviour is
            # byte-identical to before.
            def _structured(strict: bool | None):
                kwargs: dict[str, Any] = {"method": "json_schema", "include_raw": True}
                if strict is not None:
                    kwargs["strict"] = strict
                return llm.with_structured_output(schema, **kwargs)

            def call_model(_state: StructuredState) -> dict[str, Any]:
                try:
                    out = _structured(None).invoke(messages)
                except Exception as exc:  # noqa: BLE001 - provider shapes vary
                    text = str(exc)
                    rejected_by_provider = "response_format" in text and (
                        "400" in text or "invalid request" in text.lower()
                    )
                    if not rejected_by_provider:
                        raise
                    common.log(
                        "structured: provider rejected LangChain's default "
                        "response_format (strict=false); retrying once with strict=True"
                    )
                    out = _structured(True).invoke(messages)
                raw = out["raw"]
                parsed = out["parsed"]
                parsing_error = out["parsing_error"]
                inp, outp = common.extract_usage(raw)
                content = raw.content if isinstance(raw.content, str) else str(raw.content)
                return {
                    "parsed": parsed,
                    "reply": content,
                    "input_tokens": inp,
                    "output_tokens": outp,
                    "parsing_error": str(parsing_error) if parsing_error else "",
                }

        graph_builder = StateGraph(StructuredState)
        graph_builder.add_node("structured", call_model)
        graph_builder.add_edge(START, "structured")
        graph_builder.add_edge("structured", END)
        graph = graph_builder.compile()

        start = time.monotonic()
        result = graph.invoke(
            {"parsed": None, "reply": "", "input_tokens": -1, "output_tokens": -1, "parsing_error": ""}
        )
        latency_ms = int((time.monotonic() - start) * 1000)

        if result["parsing_error"]:
            return common.envelope(
                mode=MODE,
                model=model_name,
                latency_ms=latency_ms,
                input_tokens=result["input_tokens"],
                output_tokens=result["output_tokens"],
                error=f"structured output failed schema validation: {result['parsing_error']}",
            )

        parsed = result["parsed"]
        structured_obj = parsed.model_dump() if hasattr(parsed, "model_dump") else parsed

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=result["reply"],
            input_tokens=result["input_tokens"],
            output_tokens=result["output_tokens"],
            latency_ms=latency_ms,
            structured=structured_obj,
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
