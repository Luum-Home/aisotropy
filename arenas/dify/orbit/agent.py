"""
Orbit chatbot skeleton for Dify.

SWEPT: 2026-08-04 docs/architecture/arena-sweep-dify-2026-08-04.md
  Five-point sweep at langgenius/dify HEAD 4213e6b1: sdks/ ships nodejs + php REST
  clients and points at java/go/ruby REST clients; plugins run out-of-process via
  dify-plugin-daemon (subprocess/TCP/Lambda); PyPI dify-client 0.1.10 is REST and last
  released 2023-11-15; langgenius/dify-sdk-go is archived. HEAD does now carry an
  in-repo dify-agent (Python, v1.16.1) + dify-agent-runtime (Go) — but dify-agent is
  NOT on PyPI (404) and is a FastAPI/gRPC/e2b service with its own Dockerfile, not an
  embeddable Dify. The claim below stands.

HONESTY BANNER: no chat turn actually runs here. Dify has no in-process library;
the ONLY way to run an Orbit agent is to POST to a live Dify server's REST API
(`POST {base_url}/chat-messages`) with a per-app API key. That server needs
Postgres + Redis + the model runtime + a provider key — none of which exist in
this offline env. So `send_message` builds the REAL Dify request payload and,
in live mode, would send it; offline it returns a deterministic canned answer
and is clearly labelled as such.

Real Dify API surface (verified against docs.dify.ai + api/controllers):
  POST /v1/chat-messages
    body: {inputs, query, response_mode: "streaming"|"blocking",
           user, conversation_id}
    auth: Authorization: Bearer app-<key>   (per-APP key = per-app scope)
Note: the API key is scoped to ONE app. Multi-tenant fan-out (many workspaces
behind one Orbit) is exactly the LICENSE §1a "multi-tenant environment" that
requires a commercial license.
"""

from __future__ import annotations

from typing import Any

from orbit.config import Config


def build_chat_payload(query: str, user: str, conversation_id: str | None = None) -> dict[str, Any]:
    """The genuine Dify /v1/chat-messages request body. Every key is real."""
    payload: dict[str, Any] = {
        "inputs": {},
        "query": query,
        "response_mode": "streaming",
        "user": user,  # Dify's end-user identifier (NOT a tenant — see README)
    }
    if conversation_id:
        payload["conversation_id"] = conversation_id
    return payload


def send_message(cfg: Config, query: str, user: str, conversation_id: str | None = None) -> str:
    """
    Live: POST to a real Dify server. Offline: deterministic stand-in, labelled.
    We never fabricate a server round-trip.
    """
    payload = build_chat_payload(query, user, conversation_id)
    if not cfg.is_live:
        return f"[offline-stub] (would POST {cfg.base_url or '<no DIFY_API_URL>'}/chat-messages) " \
               f"answer to: {payload['query']!r}"
    # Live path — real REST call. Kept import-local so the offline smoke needs no httpx.
    import json
    import urllib.request

    req = urllib.request.Request(
        url=f"{cfg.base_url.rstrip('/')}/chat-messages",
        data=json.dumps(payload).encode(),
        headers={
            "Authorization": f"Bearer {cfg.api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:  # noqa: S310 (user-provided URL)
        return resp.read().decode()
