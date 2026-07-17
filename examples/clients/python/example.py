"""Runnable usage example for the generated aisotropyd Python client.

Run against a local NoAuth server:
    AISOTROPY_ALLOW_NOAUTH=1 AISOTROPY_ADDR=127.0.0.1:8080 go run ./cmd/aisotropyd
    python example.py

Env: AISOTROPY_BASE_URL (default http://127.0.0.1:8080), AISOTROPY_TOKEN (optional).
"""

from __future__ import annotations

import os
import sys

from aisotropy_client import AuthenticatedClient, Client
from aisotropy_client.api.default import chat as chat_api
from aisotropy_client.models.chat_output_body import ChatOutputBody
from aisotropy_client.models.chat_request import ChatRequest
from aisotropy_client.models.error_model import ErrorModel
from aisotropy_client.models.stream_done_event import StreamDoneEvent
from aisotropy_client.models.stream_error_event import StreamErrorEvent
from aisotropy_client.models.stream_token_event import StreamTokenEvent

from sse import stream_chat

BASE_URL = os.environ.get("AISOTROPY_BASE_URL", "http://127.0.0.1:8080")
TOKEN = os.environ.get("AISOTROPY_TOKEN")  # required unless server is NoAuth
TENANT = "acme"
USER = "alice"


def build_request(prompt: str) -> ChatRequest:
    # session_id, product_id, prompt are the three required fields.
    return ChatRequest(
        prompt=prompt,
        product_id="analytics-v1",
        session_id="session-42",
    )


def make_client() -> Client:
    if TOKEN:
        return AuthenticatedClient(base_url=BASE_URL, token=TOKEN)
    return Client(base_url=BASE_URL)


def main() -> int:
    client = make_client()

    # 1) Non-streaming, schema-enforced reply (generated call).
    result = chat_api.sync(
        tenant_id=TENANT,
        user_id=USER,
        client=client,
        body=build_request("Give me a one-line status."),
    )
    if isinstance(result, ErrorModel):
        print(f"[chat] error: {result.status} {result.title} — {result.detail}")
        return 1
    assert isinstance(result, ChatOutputBody)
    print(f"[chat] tenant={result.tenant_id} stub={result.stub}\n  reply: {result.reply}")

    # 2) SSE streaming — typed events from the same generated models.
    print("[stream] ", end="", flush=True)
    for ev in stream_chat(
        BASE_URL, TENANT, USER, build_request("Stream a short greeting."), token=TOKEN
    ):
        if isinstance(ev, StreamTokenEvent):
            print(ev.text, end="", flush=True)
        elif isinstance(ev, StreamDoneEvent):
            print(f"\n[stream] done — served by tenant={ev.tenant_id}")
        elif isinstance(ev, StreamErrorEvent):
            print(f"\n[stream] error: {ev.message}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
