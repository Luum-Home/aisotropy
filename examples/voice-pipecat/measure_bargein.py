#!/usr/bin/env python3
"""Barge-in-to-silence — executable metric #2 of ADR-0066 §4.

Barge-in in the voice sandwich is: the user starts speaking, the orchestrator
cancels the in-flight LLM request, and the reply stream STOPS. aisotropy makes
the server side of that a constitutional guarantee (StreamCore's
ctx-cancellation stops the upstream LLM call immediately — the same gate the
SSE endpoints honor). This script measures the client-observable half
end-to-end through the shim:

  1. Start a streaming chat completion (the same wire Pipecat uses).
  2. On the FIRST content token, cancel the request (what Pipecat's
     interruption logic does when VAD detects user speech).
  3. Measure cancel -> stream fully torn down (no further tokens can arrive =
     the bot is silent).

What this does NOT measure: mic-to-VAD detection latency (hardware/VAD tuning,
independent of aisotropy) and the audio output drain of already-synthesized
TTS (Pipecat flushes it on interruption). The server-side guarantee that the
cancelled request stops the UPSTREAM model spend is asserted by the Go suite
(StreamCore's ctx.Done() path), not observable from the client.

Usage:
    export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)
    python3 measure_bargein.py [--runs 5]

Exit codes: 0 = measured, 2 = configuration/connectivity error.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import statistics
import sys
import time

from openai import AsyncOpenAI

SHIM_BASE_URL = os.environ.get("AISOTROPY_OPENAI_BASE_URL", "http://localhost:8080/v1")
TENANT_JWT = os.environ.get("AISOTROPY_TENANT_JWT", "")

# A prompt long enough that the reply would keep streaming well past the
# barge-in point if cancellation did not work.
PROMPT = "Tell me a long, detailed story about a lighthouse keeper. Take your time."


async def run_once(client: AsyncOpenAI) -> float:
    """Returns seconds from cancel-issued to stream-torn-down."""
    stream = await client.chat.completions.create(
        model="aisotropy",
        messages=[{"role": "user", "content": PROMPT}],
        stream=True,
        stream_options={"include_usage": True},
    )
    got_token = False
    async for chunk in stream:
        if chunk.choices and (chunk.choices[0].delta.content or ""):
            got_token = True
            break  # the bot is mid-reply: this is the barge-in moment
    if not got_token:
        raise RuntimeError("stream ended before any content token — nothing to barge into")

    cancel_at = time.perf_counter()
    # Pipecat's interruption cancels the request; AsyncOpenAI's close() aborts
    # the HTTP response, which server-side cancels the request ctx -> StreamCore
    # stops the upstream LLM immediately.
    await stream.close()
    return time.perf_counter() - cancel_at


async def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--runs", type=int, default=5, help="measurement runs (median reported; default 5)")
    args = ap.parse_args()

    if not TENANT_JWT:
        print("error: AISOTROPY_TENANT_JWT is required (mint_jwt.py)", file=sys.stderr)
        return 2

    client = AsyncOpenAI(api_key=TENANT_JWT, base_url=SHIM_BASE_URL)
    times: list[float] = []
    try:
        for i in range(args.runs):
            t = await run_once(client)
            times.append(t)
            print(f"run {i + 1}: barge_in_to_silence={t * 1000:.1f}ms")
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    print("---")
    print(f"median barge-in-to-silence = {statistics.median(times) * 1000:.1f} ms   (shim {SHIM_BASE_URL})")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
