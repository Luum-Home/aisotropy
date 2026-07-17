"""``isolation_leak`` mode — exercises the REAL agnes path to prove (or
expose) a cross-tenant memory leak.

CONTEXT: ``modes.memory_scoped`` tests the hand-rolled compound-scope store
in isolation, with no LLM call. This mode goes one step further: tenant A
writes a SECRET fact into ``modes.scoped_store.ScopedStore`` under its own
scope, then a REAL agnes turn answers a question AS tenant B (a different
``tenant_id``, same ``user_id``/``product_id``). The isolation assertion is
made on what the SCOPED STORE returns for tenant B's cross-tenant recall —
that recall result is the ONLY thing about the secret's key that is ever
allowed to reach the system prompt built for tenant B's real model call.

This deliberately does NOT test "is the model honest" — a model asked "what
is X" with zero context about X will usually say it does not know, but that
proves nothing about isolation. What this mode proves is that the retrieval
layer NEVER hands tenant A's secret to tenant B's prompt in the first place:
``store.recall(cross_tenant_id, ...)`` runs BEFORE the system prompt is
built, and the system prompt is constructed EXCLUSIVELY from that recall's
return value. If the hand-rolled scope key were built incorrectly (e.g. the
adapter forgot to include ``tenant_id`` when writing or reading), the
cross-tenant recall itself would return the secret, it would appear
verbatim inside the system prompt handed to the real model, and
``leak_detected`` would flip to true — a leak at the KEYING layer, which
LangGraph does nothing to prevent (see ``modes/scoped_store.py``).

Contract additions for this mode:
    Required: "tenant_id" (the WRITER tenant), "user_id", "product_id",
    "secret_key", "secret_value" (all non-empty strings).
    Optional:
        "cross_tenant_id" - the READER tenant probing for the secret
                             (default "<tenant_id>-cross-tenant").
        "question"        - the user question asked as the cross tenant
                             (default asks for secret_key's value directly).
        "scoped_store_db_path" - override the on-disk SQLite path.

Real LLM calls: exactly 1 (the cross-tenant turn).
"""
from __future__ import annotations

import time
import traceback
from typing import Any

from . import common
from .scoped_store import ScopedStore

MODE = "isolation_leak"


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name, api_key, base_url, err = common.resolve_chat_env(MODE)

    tenant_id = payload.get("tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    secret_key = payload.get("secret_key")
    secret_value = payload.get("secret_value")
    missing = [
        name
        for name, value in (
            ("tenant_id", tenant_id),
            ("user_id", user_id),
            ("product_id", product_id),
            ("secret_key", secret_key),
            ("secret_value", secret_value),
        )
        if not value or not isinstance(value, str)
    ]
    if missing:
        return common.envelope(
            mode=MODE,
            model=model_name,
            error=f"isolation_leak mode requires non-empty string fields: {missing}",
        )
    if err:
        return err

    cross_tenant_id = payload.get("cross_tenant_id") or f"{tenant_id}-cross-tenant"
    question = payload.get("question") or (
        f"What is the {secret_key.replace('_', ' ')}? Answer strictly from the context "
        "given to you; if the context does not contain it, say you don't know."
    )

    try:
        from langchain_core.messages import HumanMessage, SystemMessage
        from langchain_openai import ChatOpenAI
        from langgraph.graph import END, START, MessagesState, StateGraph
    except Exception as exc:  # pragma: no cover - import failure path
        return common.envelope(mode=MODE, model=model_name, error=f"import failure: {exc}")

    try:
        store = ScopedStore(payload.get("scoped_store_db_path") or common.DEFAULT_SCOPED_STORE_DB)

        # 1. Tenant A writes the secret under its OWN scope.
        store.write(tenant_id, user_id, product_id, secret_key, secret_value)

        # 2. Store-level isolation check: recall AS tenant B, same
        #    user_id/product_id/secret_key. This runs BEFORE any LLM call —
        #    a leak here means the wrong data is about to enter tenant B's
        #    prompt, which is the actual thing under test.
        cross_tenant_recall = store.recall(cross_tenant_id, user_id, product_id, secret_key)
        store_level_leak = cross_tenant_recall is not None and secret_value in cross_tenant_recall

        # 3. Build tenant B's system prompt EXCLUSIVELY from what the
        #    cross-tenant recall returned (nothing, if isolation held).
        if cross_tenant_recall:
            context_block = f"Known fact for this tenant/user/product scope: {secret_key} = {cross_tenant_recall}"
        else:
            context_block = "No facts are recorded for this tenant/user/product scope."
        system_prompt = (
            f"You are a support assistant answering on behalf of tenant {cross_tenant_id!r}. "
            "Answer strictly from the context below; never invent or infer information outside "
            f"it.\n\nContext:\n{context_block}"
        )

        llm = ChatOpenAI(model=model_name, base_url=base_url, api_key=api_key)

        def call_model(state: MessagesState) -> dict[str, Any]:
            return {"messages": [llm.invoke(state["messages"])]}

        graph_builder = StateGraph(MessagesState)
        graph_builder.add_node("agent", call_model)
        graph_builder.add_edge(START, "agent")
        graph_builder.add_edge("agent", END)
        graph = graph_builder.compile()

        messages = [SystemMessage(content=system_prompt), HumanMessage(content=question)]

        start = time.monotonic()
        result = graph.invoke({"messages": messages})
        latency_ms = int((time.monotonic() - start) * 1000)

        ai_message = result["messages"][-1]
        content = ai_message.content
        reply = content if isinstance(content, str) else str(content)
        input_tokens, output_tokens = common.extract_usage(ai_message)

        model_leaked_secret = secret_value.lower() in reply.lower()
        leak_detected = store_level_leak or model_leaked_secret

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=reply,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            latency_ms=latency_ms,
            leak_detected=leak_detected,
            structured={
                "writer_tenant_id": tenant_id,
                "reader_tenant_id": cross_tenant_id,
                "secret_key": secret_key,
                "store_level_cross_tenant_recall": cross_tenant_recall,
                "store_level_leak": store_level_leak,
                "model_leaked_secret_in_reply": model_leaked_secret,
            },
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
