#!/usr/bin/env python3
"""Time-to-first-audio-byte (TTFAB) — executable metric #1 of ADR-0066 §4.

Measures the two serial legs between "user's turn ended" and "first audio byte
ready to play", printing each and their sum (evidencia ejecutable: the latency
claim IS this command's output, reproducible by anyone):

  1. LLM leg  — time-to-first-token from aisotropy's OpenAI-compat shim,
     measured through the SAME client stack Pipecat uses (AsyncOpenAI with
     ``stream=True`` + ``stream_options.include_usage``, api_key = tenant JWT).
  2. TTS leg  — time-to-first-audio-byte from Kokoro-FastAPI's streaming
     ``/v1/audio/speech`` for the first sentence of the reply.

STT is deliberately out of scope here: it overlaps the user's speech (streamed
locally by faster-whisper) rather than adding serial latency after the turn
ends; the serial voice-sandwich floor is LLM-TTFT + TTS-TTFAB.

Usage:
    export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)
    python3 measure_ttfab.py                       # both legs
    python3 measure_ttfab.py --skip-tts            # LLM leg only (no Kokoro up)
    python3 measure_ttfab.py --runs 5              # median over 5 runs
    python3 measure_ttfab.py --assert-ttfab-ms 800 # threshold mode (ADR-0069):
                                                   # exit 1 if median TTFAB > 800ms

Environment: AISOTROPY_OPENAI_BASE_URL, AISOTROPY_TENANT_JWT, KOKORO_BASE_URL,
KOKORO_VOICE (same defaults as bot.py).

Exit codes: 0 = measured (and under threshold when asserting),
1 = --assert-ttfab-ms threshold breached, 2 = configuration/connectivity error.
"""

from __future__ import annotations

import argparse
import asyncio
import os
import statistics
import sys
import time

import httpx
from openai import AsyncOpenAI

SHIM_BASE_URL = os.environ.get("AISOTROPY_OPENAI_BASE_URL", "http://localhost:8080/v1")
TENANT_JWT = os.environ.get("AISOTROPY_TENANT_JWT", "")
KOKORO_BASE_URL = os.environ.get("KOKORO_BASE_URL", "http://localhost:8880/v1")
KOKORO_VOICE = os.environ.get("KOKORO_VOICE", "af_heart")

PROMPT = "In one short sentence, say hello."


async def llm_first_token_and_sentence(client: AsyncOpenAI) -> tuple[float, str]:
    """Streams one completion; returns (seconds to first content token, first sentence).

    Mirrors the Pipecat request contract verified in ADR-0066: stream:true +
    stream_options.include_usage, content read from choices[0].delta.content.
    """
    start = time.perf_counter()
    ttft = -1.0
    sentence = ""
    stream = await client.chat.completions.create(
        model="aisotropy",
        messages=[{"role": "user", "content": PROMPT}],
        stream=True,
        stream_options={"include_usage": True},
    )
    async for chunk in stream:
        if not chunk.choices:
            continue  # usage-only chunk
        delta = chunk.choices[0].delta.content or ""
        if delta and ttft < 0:
            ttft = time.perf_counter() - start
        sentence += delta
        # First sentence is enough for the TTS leg (the voice pipeline chunks
        # replies at sentence boundaries the same way).
        for stop in (". ", "! ", "? "):
            if stop in sentence:
                sentence = sentence.split(stop)[0] + stop.strip()
                await stream.close()
                return ttft, sentence
    return ttft, sentence or "Hello."


async def tts_first_audio_byte(text: str) -> float:
    """Streams Kokoro-FastAPI /v1/audio/speech; returns seconds to first audio byte."""
    start = time.perf_counter()
    async with httpx.AsyncClient(timeout=30.0) as client:
        async with client.stream(
            "POST",
            f"{KOKORO_BASE_URL}/audio/speech",
            json={
                "model": "kokoro",
                "voice": KOKORO_VOICE,
                "input": text,
                "response_format": "pcm",
                "stream": True,
            },
        ) as resp:
            resp.raise_for_status()
            async for _ in resp.aiter_bytes():
                return time.perf_counter() - start
    raise RuntimeError("TTS stream produced no audio bytes")


async def run_once(client: AsyncOpenAI, skip_tts: bool) -> tuple[float, float]:
    ttft, sentence = await llm_first_token_and_sentence(client)
    if ttft < 0:
        raise RuntimeError("LLM stream produced no content tokens")
    tts = 0.0
    if not skip_tts:
        tts = await tts_first_audio_byte(sentence)
    return ttft, tts


async def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--runs", type=int, default=3, help="measurement runs (median reported; default 3)")
    ap.add_argument("--skip-tts", action="store_true", help="measure the LLM leg only (Kokoro not running)")
    ap.add_argument(
        "--assert-ttfab-ms",
        type=float,
        default=None,
        metavar="N",
        help="threshold mode: exit 1 if the median TTFAB exceeds N milliseconds "
        "(with --skip-tts the threshold applies to the LLM leg alone)",
    )
    args = ap.parse_args()

    if not TENANT_JWT:
        print("error: AISOTROPY_TENANT_JWT is required (mint_jwt.py)", file=sys.stderr)
        return 2

    client = AsyncOpenAI(api_key=TENANT_JWT, base_url=SHIM_BASE_URL)
    llm_times: list[float] = []
    tts_times: list[float] = []
    try:
        for i in range(args.runs):
            ttft, tts = await run_once(client, args.skip_tts)
            llm_times.append(ttft)
            tts_times.append(tts)
            print(f"run {i + 1}: llm_ttft={ttft * 1000:.1f}ms tts_ttfab={tts * 1000:.1f}ms total={(ttft + tts) * 1000:.1f}ms")
    except Exception as e:  # connectivity/config failures are actionable, not tracebacks
        print(f"error: {e}", file=sys.stderr)
        return 2

    llm_med = statistics.median(llm_times)
    tts_med = statistics.median(tts_times)
    total_med_ms = (llm_med + tts_med) * 1000
    print("---")
    print(f"median llm_ttft   = {llm_med * 1000:.1f} ms   (shim {SHIM_BASE_URL})")
    if not args.skip_tts:
        print(f"median tts_ttfab  = {tts_med * 1000:.1f} ms   (kokoro {KOKORO_BASE_URL})")
    print(f"median TTFAB      = {total_med_ms:.1f} ms")

    if args.assert_ttfab_ms is not None:
        if total_med_ms > args.assert_ttfab_ms:
            print(
                f"FAIL: median TTFAB {total_med_ms:.1f} ms > threshold {args.assert_ttfab_ms:.1f} ms",
                file=sys.stderr,
            )
            return 1
        print(f"PASS: median TTFAB {total_med_ms:.1f} ms <= threshold {args.assert_ttfab_ms:.1f} ms")
    return 0


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
