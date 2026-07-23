#!/usr/bin/env python3
"""Voice sandwich over aisotropy (ADR-0066 §4): STT -> LLM -> TTS.

    mic -> Silero VAD -> faster-whisper STT -> aisotropy OpenAI-compat shim
        -> Kokoro-FastAPI TTS -> speaker

The LLM leg is the whole point: Pipecat's stock ``OpenAILLMService`` pointed at
aisotropy's ``POST /v1/chat/completions`` shim with the tenant JWT passed AS
the api_key. Zero client modification — the shim speaks the exact wire Pipecat
already consumes (source-verified in ADR-0066: always ``stream:true`` +
``stream_options.include_usage``; usage optional; chunks pydantic-validated by
the openai SDK). Every turn runs aisotropy's FULL pipeline server-side:
tenant-isolated memory hydration, quota-before-spend, guardrails, cost
accounting — and interrupting the bot (barge-in) cancels the request, which
aisotropy's StreamCore turns into an immediate upstream LLM stop.

Environment:
    AISOTROPY_OPENAI_BASE_URL  shim base URL         (default http://localhost:8080/v1)
    AISOTROPY_TENANT_JWT       tenant JWT = api_key  (required; mint with mint_jwt.py)
    KOKORO_BASE_URL            Kokoro-FastAPI URL    (default http://localhost:8880/v1)
    KOKORO_VOICE               Kokoro voice          (default alloy; see ADR-0069 §3)
    WHISPER_MODEL              faster-whisper model  (default distil-medium.en)

Run:
    python3 bot.py

Requires local audio (portaudio) plus the STT/TTS models — see README.md for
the full bring-up, including the Kokoro-FastAPI container.
"""

from __future__ import annotations

import asyncio
import os
import sys

from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.runner import PipelineRunner
from pipecat.pipeline.task import PipelineParams, PipelineTask
from pipecat.processors.aggregators.openai_llm_context import OpenAILLMContext
from pipecat.services.openai.llm import OpenAILLMService
from pipecat.services.openai.tts import OpenAITTSService
from pipecat.services.whisper.stt import WhisperSTTService
from pipecat.transports.local.audio import (
    LocalAudioTransport,
    LocalAudioTransportParams,
)

SHIM_BASE_URL = os.environ.get("AISOTROPY_OPENAI_BASE_URL", "http://localhost:8080/v1")
TENANT_JWT = os.environ.get("AISOTROPY_TENANT_JWT", "")
KOKORO_BASE_URL = os.environ.get("KOKORO_BASE_URL", "http://localhost:8880/v1")
# "alloy" (an OpenAI voice id, mapped server-side by Kokoro-FastAPI) — pipecat
# 0.0.x's OpenAITTSService rejects Kokoro ids like "af_heart" via its
# VALID_VOICES lookup, degrading into silent no-audio (ADR-0069 §3).
KOKORO_VOICE = os.environ.get("KOKORO_VOICE", "alloy")
WHISPER_MODEL = os.environ.get("WHISPER_MODEL", "distil-medium.en")


async def main() -> None:
    if not TENANT_JWT:
        print(
            "error: AISOTROPY_TENANT_JWT is required — mint one with:\n"
            "  python3 mint_jwt.py --tenant acme --user alice",
            file=sys.stderr,
        )
        raise SystemExit(2)

    # Mic + speaker with Silero VAD: VAD both segments the user's turns for STT
    # and triggers interruptions — Pipecat cancels the in-flight LLM/TTS work
    # when the user starts speaking, which closes the HTTP request to the shim,
    # which aisotropy's StreamCore turns into an immediate upstream LLM stop
    # (the constitutional ctx-cancellation gate). That chain IS barge-in.
    transport = LocalAudioTransport(
        LocalAudioTransportParams(
            audio_in_enabled=True,
            audio_out_enabled=True,
            vad_analyzer=SileroVADAnalyzer(),
        )
    )

    # STT: faster-whisper (MIT), fully local. First run downloads the model.
    stt = WhisperSTTService(model=WHISPER_MODEL)

    # LLM: aisotropy's OpenAI-compat shim. The tenant JWT IS the api_key.
    # NOTE: aisotropy owns the system prompt server-side (persona + tenant
    # memory hydration); incoming system messages are ignored by the shim (v1
    # floor), so no system message is configured here.
    llm = OpenAILLMService(api_key=TENANT_JWT, base_url=SHIM_BASE_URL, model="aisotropy")

    # TTS: Kokoro-FastAPI (Apache-2.0) exposes an OpenAI-compatible
    # /v1/audio/speech, so Pipecat's stock OpenAITTSService drives it too —
    # the same base_url+api_key trick, twice.
    tts = OpenAITTSService(
        api_key="not-needed",
        base_url=KOKORO_BASE_URL,
        model="kokoro",
        voice=KOKORO_VOICE,
    )

    # Conversation context: Pipecat aggregates STT transcriptions into user
    # turns and assistant replies back into context. aisotropy re-hydrates the
    # real cross-session memory server-side from its own tiers; this local
    # context just carries the current session's transcript shape the OpenAI
    # wire expects.
    context = OpenAILLMContext()
    context_aggregator = llm.create_context_aggregator(context)

    pipeline = Pipeline(
        [
            transport.input(),
            stt,
            context_aggregator.user(),
            llm,
            tts,
            transport.output(),
            context_aggregator.assistant(),
        ]
    )

    task = PipelineTask(
        pipeline,
        params=PipelineParams(
            allow_interruptions=True,  # barge-in ON: user speech cancels the bot
        ),
    )

    runner = PipelineRunner()
    print(f"voice bot up: shim={SHIM_BASE_URL} tts={KOKORO_BASE_URL} stt=whisper:{WHISPER_MODEL}")
    print("speak into the mic; interrupt the bot mid-reply to exercise barge-in. Ctrl-C to quit.")
    await runner.run(task)


if __name__ == "__main__":
    asyncio.run(main())
