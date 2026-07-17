"""Hand-rolled compound-scope key/value store for the two "scoped memory"
arena modes (``memory_scoped``, ``isolation_leak``).

LangGraph ships a checkpointer (``langgraph.checkpoint.*``, used by
``modes.memory``) keyed by a single caller-supplied ``thread_id`` string.
There is no multi-tenant concept anywhere in LangGraph's memory primitives:
no tenant/user/product dimension, no built-in isolation, no key-prefix
validation, nothing that stops one thread's state from being read under
another thread's id if the caller passes the wrong string. This module is
what a LangGraph app developer would have to write themselves to get scoped,
tenant-isolated memory — a small on-disk SQLite table keyed by a
hand-assembled compound scope string.

The scope key is built by joining ``tenant_id``/``user_id``/``product_id``
(and optionally ``session_id``) with the ASCII Unit Separator (``0x1F``)
rather than a printable delimiter like ``:`` or ``/``. A printable delimiter
is exactly the kind of small keying mistake this arena exists to surface:
with ``":"`` as the join character, ``tenant_id="A"`` + ``user_id="X:Y"``
collides with ``tenant_id="A:X"`` + ``user_id="Y"`` into the identical scope
string ``"A:X:Y"`` — a cross-tenant collision caused entirely by adapter
code, not by LangGraph. ``0x1F`` is not a realistic character inside a real
tenant/user/product id, so this specific collision class is closed, but the
broader point stands: EVERY read and write MUST pass through
``scope_key()``, and there is nothing in this module or in LangGraph that
enforces that a caller always supplies the right ``tenant_id``. That
discipline is 100% on the app — see the "Multi-tenant honesty" section of
README.md.
"""
from __future__ import annotations

import os
import sqlite3
import time
from typing import Optional

_SEP = "\x1f"  # ASCII Unit Separator — never realistic inside a real id.


def scope_key(tenant_id: str, user_id: str, product_id: str, session_id: Optional[str] = None) -> str:
    """Hand-rolled compound scope key. LangGraph has no equivalent primitive."""
    parts = [str(tenant_id), str(user_id), str(product_id)]
    if session_id:
        parts.append(str(session_id))
    return _SEP.join(parts)


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


class ScopedStore:
    """A tiny hand-rolled tenant-scoped fact store over on-disk SQLite.

    Every read/write goes through ``scope_key()`` — there is no implicit
    global namespace to fall back into. Isolation is a property of how the
    CALLER constructs the scope (see ``modes.memory_scoped`` and
    ``modes.isolation_leak``), not something this class or LangGraph
    guarantees automatically.
    """

    def __init__(self, db_path: str) -> None:
        os.makedirs(os.path.dirname(db_path) or ".", exist_ok=True)
        self._db_path = db_path
        with self._connect() as conn:
            conn.execute(
                """
                CREATE TABLE IF NOT EXISTS scoped_facts (
                    scope_key TEXT NOT NULL,
                    fact_key TEXT NOT NULL,
                    fact_value TEXT NOT NULL,
                    tenant_id TEXT NOT NULL,
                    created_at TEXT NOT NULL,
                    PRIMARY KEY (scope_key, fact_key)
                )
                """
            )

    def _connect(self) -> sqlite3.Connection:
        return sqlite3.connect(self._db_path)

    def write(
        self,
        tenant_id: str,
        user_id: str,
        product_id: str,
        fact_key: str,
        fact_value: str,
        session_id: Optional[str] = None,
    ) -> None:
        key = scope_key(tenant_id, user_id, product_id, session_id)
        with self._connect() as conn:
            conn.execute(
                """
                INSERT INTO scoped_facts (scope_key, fact_key, fact_value, tenant_id, created_at)
                VALUES (?, ?, ?, ?, ?)
                ON CONFLICT(scope_key, fact_key) DO UPDATE SET
                    fact_value = excluded.fact_value,
                    created_at = excluded.created_at
                """,
                (key, fact_key, fact_value, str(tenant_id), _now_iso()),
            )

    def recall(
        self,
        tenant_id: str,
        user_id: str,
        product_id: str,
        fact_key: str,
        session_id: Optional[str] = None,
    ) -> Optional[str]:
        key = scope_key(tenant_id, user_id, product_id, session_id)
        with self._connect() as conn:
            row = conn.execute(
                "SELECT fact_value FROM scoped_facts WHERE scope_key = ? AND fact_key = ?",
                (key, fact_key),
            ).fetchone()
        return row[0] if row else None
