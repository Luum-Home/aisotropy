"""``memory_scoped`` mode — hand-rolled compound-scope memory, tested against
the STORE, not the model.

CONTEXT: ``modes.memory`` (the pre-existing mode) proves LangGraph's
``SqliteSaver`` checkpointer persists cross-process, but it keys purely by a
caller-supplied ``thread_id`` — no tenant, no user, no product dimension,
and nothing that isolates one thread from another. This mode proves the
thing aisotropy treats as a first-class constitutional gate (see
``.claude/rules/constitutional-gates.md`` -> "Multi-tenant data isolation"):
scoped memory keyed by ``(tenant_id, user_id, product_id[, session_id])``.

LangGraph DOES provide a scoping primitive for this — ``BaseStore``
(``InMemoryStore``/``PostgresStore``) namespace tuples,
``store.put((tenant_id, user_id, product_id), key, value)`` — but it does
NOT enforce that the caller supplies the right scope (namespaces are
organizational, not access control). ``modes.scoped_store.ScopedStore`` is a
small hand-rolled on-disk SQLite key/value store the adapter built to
exercise that same namespacing directly and offline (no Postgres) — see that
module's docstring for the compound-key construction and its honest caveats.
This mode does not call the model at all: it is a pure test of whether the
hand-rolled scope key isolates correctly, which is the property under test.

Scenario (default, no ``"op"`` field, one adapter invocation):
  1. WRITE a fact under scope (tenant_id, user_id, product_id[, session_id]).
  2. RECALL under the SAME scope -> must return the fact (same_scope_hit).
  3. RECALL under ``cross_tenant_id`` (different tenant, same user/product)
     -> must return nothing (cross_tenant_miss).

Contract additions for this mode:
    Required: "tenant_id", "user_id", "product_id" (all non-empty strings).
    Optional:
        "session_id"       - extends the scope key further.
        "fact"              - the value to write (default: a canned fact).
        "fact_key"          - logical key for the fact (default "note").
        "cross_tenant_id"   - the different tenant to probe from
                               (default "<tenant_id>-cross-tenant").
        "scoped_store_db_path" - override the on-disk SQLite path.
        "op"                - "write" | "recall". When present, this
                               invocation performs ONLY that single store
                               operation against the SAME on-disk file
                               (default ``SCOPED_STORE_DB_PATH``), so the
                               3-step scenario can also be reproduced across
                               TWO separate ``run.sh`` invocations — mirrors
                               ``modes.memory``'s cross-process pattern.
                               When absent, the full 3-step scenario above
                               runs inside this one invocation (the primary,
                               simplest way to exercise this mode).
"""
from __future__ import annotations

import os
import time
import traceback
from typing import Any

from . import common
from .scoped_store import ScopedStore

MODE = "memory_scoped"


def _scope_fields(payload: dict[str, Any]) -> tuple[str | None, str | None, str | None, list[str]]:
    tenant_id = payload.get("tenant_id")
    user_id = payload.get("user_id")
    product_id = payload.get("product_id")
    missing = [
        name
        for name, value in (("tenant_id", tenant_id), ("user_id", user_id), ("product_id", product_id))
        if not value or not isinstance(value, str)
    ]
    return tenant_id, user_id, product_id, missing


def run(payload: dict[str, Any]) -> dict[str, Any]:
    model_name = os.environ.get("OPENAI_MODEL", common.DEFAULT_MODEL)

    tenant_id, user_id, product_id, missing = _scope_fields(payload)
    if missing:
        return common.envelope(
            mode=MODE,
            model=model_name,
            error=f"memory_scoped mode requires non-empty string fields: {missing}",
        )

    session_id = payload.get("session_id")
    fact_key = payload.get("fact_key") or "note"
    op = payload.get("op")
    db_path = payload.get("scoped_store_db_path") or common.DEFAULT_SCOPED_STORE_DB

    try:
        store = ScopedStore(db_path)
    except Exception as exc:
        return common.envelope(mode=MODE, model=model_name, error=f"scoped store init failure: {exc}")

    try:
        if op == "write":
            fact = payload.get("fact")
            if not fact or not isinstance(fact, str):
                return common.envelope(
                    mode=MODE, model=model_name, error="op=write requires a non-empty string 'fact' field"
                )
            store.write(tenant_id, user_id, product_id, fact_key, fact, session_id=session_id)
            return common.envelope(mode=MODE, model=model_name, reply=f"wrote fact_key={fact_key!r} under scope")

        if op == "recall":
            value = store.recall(tenant_id, user_id, product_id, fact_key, session_id=session_id)
            return common.envelope(
                mode=MODE,
                model=model_name,
                reply=value or "",
                scoped_recall={"same_scope_hit": value is not None, "cross_tenant_miss": None},
            )

        if op is not None:
            return common.envelope(mode=MODE, model=model_name, error=f"unknown op {op!r}; supported: write, recall")

        # Default: full 3-step scenario in one invocation.
        fact = payload.get("fact") or "The account's plan renews on the 3rd of every month."
        cross_tenant_id = payload.get("cross_tenant_id") or f"{tenant_id}-cross-tenant"

        start = time.monotonic()
        store.write(tenant_id, user_id, product_id, fact_key, fact, session_id=session_id)

        same_scope_value = store.recall(tenant_id, user_id, product_id, fact_key, session_id=session_id)
        cross_tenant_value = store.recall(cross_tenant_id, user_id, product_id, fact_key, session_id=session_id)
        latency_ms = int((time.monotonic() - start) * 1000)

        same_scope_hit = same_scope_value == fact
        cross_tenant_miss = cross_tenant_value is None

        return common.envelope(
            mode=MODE,
            model=model_name,
            reply=same_scope_value or "",
            latency_ms=latency_ms,
            scoped_recall={"same_scope_hit": same_scope_hit, "cross_tenant_miss": cross_tenant_miss},
            structured={
                "recalled_same_scope": same_scope_value,
                "recalled_cross_tenant": cross_tenant_value,
                "scope": {
                    "tenant_id": tenant_id,
                    "user_id": user_id,
                    "product_id": product_id,
                    "session_id": session_id,
                },
                "cross_tenant_id": cross_tenant_id,
                "fact_key": fact_key,
            },
        )
    except Exception as exc:
        common.log("adapter error:\n" + traceback.format_exc())
        return common.envelope(mode=MODE, model=model_name, error=f"{type(exc).__name__}: {exc}")
