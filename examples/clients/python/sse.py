"""SSE streaming transport for aisotropyd's POST .../chat/stream route.

openapi-python-client generates typed request/response models and JSON-route calls,
but it does not consume `text/event-stream`. This module fills that gap with a small
httpx-based reader (httpx is already a dependency of the generated client) that parses
the runtime's SSE frames and decodes each `data:` payload into the SAME generated
component models (StreamTokenEvent / StreamDoneEvent / StreamErrorEvent). Per the
transport bake-off, SSE-over-HTTP is the default streaming wire format.

The runtime frames each event as:
    event: token\\n data: {"text":"..."}\\n\\n
    event: done\\n  data: {"tenant_id":"..."}\\n\\n
    event: error\\n data: {"message":"..."}\\n\\n
"""

from __future__ import annotations

import json
from collections.abc import Iterator
from dataclasses import dataclass

import httpx

from aisotropy_client.models.chat_request import ChatRequest
from aisotropy_client.models.stream_done_event import StreamDoneEvent
from aisotropy_client.models.stream_error_event import StreamErrorEvent
from aisotropy_client.models.stream_token_event import StreamTokenEvent

StreamEvent = StreamTokenEvent | StreamDoneEvent | StreamErrorEvent


@dataclass
class _RawEvent:
    name: str
    data: str


def _parse_sse(lines: Iterator[str]) -> Iterator[_RawEvent]:
    """Group raw text lines into SSE events (blank line = event boundary)."""
    name = "message"
    data_lines: list[str] = []
    for line in lines:
        if line == "":
            if data_lines:
                yield _RawEvent(name, "\n".join(data_lines))
            name, data_lines = "message", []
            continue
        if line.startswith(":"):  # comment / heartbeat
            continue
        if line.startswith("event:"):
            name = line[len("event:") :].strip()
        elif line.startswith("data:"):
            data_lines.append(line[len("data:") :].strip())
    if data_lines:  # flush trailing event with no terminating blank line
        yield _RawEvent(name, "\n".join(data_lines))


def stream_chat(
    base_url: str,
    tenant_id: str,
    user_id: str,
    body: ChatRequest,
    *,
    token: str | None = None,
    timeout: float = 60.0,
) -> Iterator[StreamEvent]:
    """Yield typed stream events. Closing the generator aborts the upstream LLM call.

    Args:
        base_url:  e.g. "http://127.0.0.1:8080" (no trailing slash).
        tenant_id, user_id: path identity (isolation boundary).
        body: a generated ChatRequest.
        token: bearer token; required unless the server runs NoAuth.
    """
    url = f"{base_url}/v1/tenant/{tenant_id}/user/{user_id}/chat/stream"
    headers = {
        "content-type": "application/json",
        "accept": "text/event-stream",
    }
    if token:
        headers["authorization"] = f"Bearer {token}"

    payload = json.dumps(body.to_dict())

    with httpx.Client(timeout=timeout) as client:
        with client.stream("POST", url, content=payload, headers=headers) as res:
            if res.status_code != httpx.codes.OK:
                res.read()
                raise RuntimeError(
                    f"stream failed: HTTP {res.status_code} — {res.text}"
                )
            for ev in _parse_sse(res.iter_lines()):
                doc = json.loads(ev.data)
                if ev.name == "token":
                    yield StreamTokenEvent.from_dict(doc)
                elif ev.name == "done":
                    yield StreamDoneEvent.from_dict(doc)
                    return
                elif ev.name == "error":
                    yield StreamErrorEvent.from_dict(doc)
                    return
