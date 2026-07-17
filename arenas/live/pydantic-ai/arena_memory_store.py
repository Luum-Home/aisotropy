"""Real, persisted `message_history` storage for the `memory` mode, plus a
hand-rolled COMPOUND-SCOPE fact store for the `memory_scoped` / `isolation_leak`
modes.

Backed by the networked **rqlite** service (SQLite+FTS5 over HTTP) already
running for aisotropy's own facts tier (see `.claude/rules/services-config.md`).
This is deliberately NOT a process-local dict: state must survive a fresh
process invocation, because the `memory` mode's acceptance criterion is
recalling a fact across two *separate* adapter runs (two separate `python
adapter.py` processes, not two calls within one process).

The persisted payload is the exact bytes produced by Pydantic AI's own
`ModelMessagesTypeAdapter.dump_json(...)` -- so replaying it back through
`ModelMessagesTypeAdapter.validate_json(...)` reconstructs the framework's
native `ModelRequest`/`ModelResponse` history, not a hand-rolled re-encoding.

--------------------------------------------------------------------------
IMPORTANT (read before touching `scope_key` / the scoped-fact functions):

Pydantic AI has **no native multi-tenant memory isolation primitive** --
`message_history` above is keyed by a single opaque `session_id` and nothing
in the framework understands "tenant" or "user" or enforces that one
session's data cannot bleed into another's query. The `scope_key()` /
`save_scoped_fact()` / `load_scoped_facts()` functions below are a
**hand-rolled** compound-key convention layered on top of the same rqlite
table mechanism -- isolation here is a DEVELOPER DISCIPLINE, not a framework
guarantee. Get the key construction wrong (e.g. naive string concatenation
without a separator, so `tenant="A1", user="X"` collides with
`tenant="A", user="1X"`) and you have a cross-tenant data leak with no
framework-level safety net to catch it. See README.md's "Honest note" for
the contrast with aisotropy's own construction-enforced isolation
(key-prefix + Qdrant in-scan payload filter, not a hand-rolled convention).
--------------------------------------------------------------------------
"""

from __future__ import annotations

import datetime
import json
import urllib.request

from arena_common import log, rqlite_base_url

_TABLE = "arena_pydantic_memory"
_SCOPED_TABLE = "arena_pydantic_scoped_memory"


def _request(method: str, path: str, body=None, timeout: float = 15.0) -> dict:
    url = f"{rqlite_base_url()}{path}"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if data is not None:
        req.add_header("content-type", "application/json")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read().decode()
        return json.loads(raw) if raw else {}


def _ensure_table() -> None:
    _request(
        "POST",
        "/db/execute",
        [
            [
                f"CREATE TABLE IF NOT EXISTS {_TABLE} "
                "(session_id TEXT PRIMARY KEY, history_json TEXT NOT NULL, updated_at TEXT NOT NULL)"
            ]
        ],
    )


def load_history_json(session_id: str) -> bytes | None:
    """Return the raw persisted message-history JSON blob for session_id, or None if unseen."""
    _ensure_table()
    result = _request(
        "POST",
        "/db/query",
        [[f"SELECT history_json FROM {_TABLE} WHERE session_id = ?", session_id]],
    )
    rows = (result.get("results") or [{}])[0]
    values = rows.get("values") or []
    if not values:
        log(f"memory: no prior history in rqlite for session_id={session_id!r} (fresh session)")
        return None
    blob = values[0][0]
    log(f"memory: loaded prior history from rqlite for session_id={session_id!r} ({len(blob)} bytes)")
    return blob.encode()


def save_history_json(session_id: str, history_json: bytes) -> None:
    """Upsert the persisted history blob for session_id into rqlite (survives process exit)."""
    _ensure_table()
    now = datetime.datetime.now(datetime.timezone.utc).isoformat()
    _request(
        "POST",
        "/db/execute",
        [
            [
                f"INSERT INTO {_TABLE} (session_id, history_json, updated_at) VALUES (?, ?, ?) "
                "ON CONFLICT(session_id) DO UPDATE SET "
                "history_json = excluded.history_json, updated_at = excluded.updated_at",
                session_id,
                history_json.decode(),
                now,
            ]
        ],
    )
    log(f"memory: persisted history to rqlite for session_id={session_id!r} ({len(history_json)} bytes)")


# --------------------------------------------------------------------------
# Hand-rolled COMPOUND-SCOPE fact store -- `memory_scoped` / `isolation_leak`
# --------------------------------------------------------------------------
#
# Pydantic AI ships nothing for this: no tenant/user/product concept exists
# in the framework's memory surface. Everything below is this adapter's own
# convention, built on the same rqlite table mechanism as the `memory` mode
# above, and it is only as safe as this code makes it. There is no framework
# guardrail that would catch a broken key -- get the separator or the
# component order wrong and two different tenants silently share a row.

_SCOPE_SEP = "\x1f"  # ASCII unit separator: chosen specifically because it is
# not a character any caller-supplied tenant_id/user_id/product_id/session_id
# is expected to contain, so concatenation cannot ambiguously collide the way
# a printable delimiter like ":" or "/" could (e.g. tenant="A:1", user="X" vs.
# tenant="A", user="1:X"). This is exactly the class of dev mistake the
# README's honest note warns about: pick a delimiter a real ID can contain,
# and two distinct scopes hash to the same key -- a cross-tenant leak.


def scope_key(tenant_id: str, user_id: str, product_id: str, session_id: str | None = None) -> str:
    """Build the hand-rolled compound scope key: (tenant_id, user_id, product_id[, session_id]).

    This is the ENTIRE isolation mechanism for the `memory_scoped` /
    `isolation_leak` modes -- there is no framework-enforced tenant boundary
    underneath it. Every caller MUST supply tenant_id; omitting it (or
    passing the wrong one) is indistinguishable, to this store, from an
    intentional cross-tenant query.
    """
    if not tenant_id:
        raise ValueError("scope_key requires a non-empty tenant_id (isolation has no meaning without it)")
    parts = [str(tenant_id), str(user_id or "-"), str(product_id or "-"), str(session_id) if session_id else "-"]
    return _SCOPE_SEP.join(parts)


def _ensure_scoped_table() -> None:
    _request(
        "POST",
        "/db/execute",
        [
            [
                f"CREATE TABLE IF NOT EXISTS {_SCOPED_TABLE} "
                "(scope_key TEXT PRIMARY KEY, fact_text TEXT NOT NULL, updated_at TEXT NOT NULL)"
            ]
        ],
    )


def save_scoped_fact(scope: str, fact_text: str) -> None:
    """Upsert (append) a fact under the given compound scope key.

    Multiple writes to the same scope accumulate (newline-joined), same as a
    real fact journal would. Logs only the byte length, never the fact
    content -- the fact may itself be test secret material.
    """
    _ensure_scoped_table()
    now = datetime.datetime.now(datetime.timezone.utc).isoformat()
    _request(
        "POST",
        "/db/execute",
        [
            [
                f"INSERT INTO {_SCOPED_TABLE} (scope_key, fact_text, updated_at) VALUES (?, ?, ?) "
                "ON CONFLICT(scope_key) DO UPDATE SET "
                "fact_text = fact_text || char(10) || excluded.fact_text, updated_at = excluded.updated_at",
                scope,
                fact_text,
                now,
            ]
        ],
    )
    log(f"memory_scoped: wrote fact to rqlite scope_key_len={len(scope)} ({len(fact_text)} bytes)")


def load_scoped_facts(scope: str) -> str | None:
    """Return the accumulated fact text for exactly this compound scope key, or None if unseen.

    Deliberately an EXACT match on scope_key (`WHERE scope_key = ?`), not a
    prefix or fuzzy match -- a different tenant_id/user_id/product_id/
    session_id produces a completely different key, so this query returns no
    rows for a scope this adapter never wrote to. This exact-match property
    is what `isolation_leak` mode's `leak_detected` check relies on.
    """
    _ensure_scoped_table()
    result = _request(
        "POST",
        "/db/query",
        [[f"SELECT fact_text FROM {_SCOPED_TABLE} WHERE scope_key = ?", scope]],
    )
    rows = (result.get("results") or [{}])[0]
    values = rows.get("values") or []
    if not values:
        log(f"memory_scoped: no facts in rqlite for scope_key_len={len(scope)} (unseen scope)")
        return None
    text = values[0][0]
    log(f"memory_scoped: loaded facts from rqlite for scope_key_len={len(scope)} ({len(text)} bytes)")
    return text
