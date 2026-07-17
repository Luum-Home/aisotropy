"""Real, persisted `message_history` storage for the `memory` mode.

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
"""

from __future__ import annotations

import datetime
import json
import urllib.request

from arena_common import log, rqlite_base_url

_TABLE = "arena_pydantic_memory"


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
