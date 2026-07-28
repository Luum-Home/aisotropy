#!/usr/bin/env python3
"""Voice sandwich over aisotropy (ADR-0066 §4): STT -> LLM -> TTS.

    mic -> Silero VAD -> voicebox STT -> aisotropy OpenAI-compat shim
        -> voicebox TTS -> speaker

The LLM leg is the whole point: Pipecat's stock ``OpenAILLMService`` pointed at
aisotropy's ``POST /v1/chat/completions`` shim with the tenant JWT passed AS
the api_key. Zero client modification — the shim speaks the exact wire Pipecat
already consumes (source-verified in ADR-0066: always ``stream:true`` +
``stream_options.include_usage``; usage optional; chunks pydantic-validated by
the openai SDK). Every turn runs aisotropy's FULL pipeline server-side:
tenant-isolated memory hydration, quota-before-spend, guardrails, cost
accounting — and interrupting the bot (barge-in) cancels the request, which
aisotropy's StreamCore turns into an immediate upstream LLM stop.

Both AUDIO legs come from one service, selected by ``VOICE_AUDIO_BACKEND``
(default ``voicebox``; ``kokoro-fastapi`` restores the previous stack — see
audio_backend.py and ADR-0077). The aisotropy core still contains zero audio
code either way.

Environment:
    AISOTROPY_OPENAI_BASE_URL  shim base URL         (default http://localhost:8080/v1)
    AISOTROPY_TENANT_JWT       tenant JWT = api_key  (required; mint with mint_jwt.py)
    VOICE_AUDIO_BACKEND        voicebox | kokoro-fastapi   (default voicebox)
    VOICEBOX_BASE_URL          voicebox URL          (default http://localhost:8790/v1)
    VOICEBOX_VOICE             Kokoro voice id       (default af_heart)
    KOKORO_BASE_URL            Kokoro-FastAPI URL    (default http://localhost:8880/v1)
    KOKORO_VOICE               OpenAI voice id       (default alloy; see ADR-0069 §3)
    WHISPER_MODEL              faster-whisper model  (default distil-medium.en;
                               kokoro-fastapi backend only)

Run:
    python3 bot.py

Requires local audio (portaudio) plus a running audio backend — see README.md
for the full bring-up.
"""

from __future__ import annotations

import asyncio
import os
import sys

from pipecat.audio.vad.silero import SileroVADAnalyzer
from pipecat.pipeline.pipeline import Pipeline
from pipecat.pipeline.worker import PipelineParams, PipelineWorker
from pipecat.processors.aggregators.llm_context import LLMContext
from pipecat.services.openai.llm import OpenAILLMService
from pipecat.transports.local.audio import (
    LocalAudioTransport,
    LocalAudioTransportParams,
)
from pipecat.workers.runner import WorkerRunner

import audio_backend

SHIM_BASE_URL = os.environ.get("AISOTROPY_OPENAI_BASE_URL", "http://localhost:8080/v1")
TENANT_JWT = os.environ.get("AISOTROPY_TENANT_JWT", "")


async def main() -> None:
    if not TENANT_JWT:
        print(
            "error: AISOTROPY_TENANT_JWT is required — mint one with:\n"
            "  python3 mint_jwt.py --tenant acme --user alice",
            file=sys.stderr,
        )
        raise SystemExit(2)

    # Mic + speaker. In pipecat 1.x the VAD analyzer is NO LONGER a transport
    # parameter — it moved onto the user context aggregator below
    # (`LLMUserAggregatorParams.vad_analyzer`, llm_response_universal.py:173),
    # which is also where turn-taking and interruptions are now decided.
    transport = LocalAudioTransport(
        LocalAudioTransportParams(
            audio_in_enabled=True,
            audio_out_enabled=True,
        )
    )

    # STT + TTS: both legs from the selected backend (audio_backend.py). Default
    # is the voicebox container — one process for both, faster-distil-whisper
    # for STT and Kokoro for TTS; `VOICE_AUDIO_BACKEND=kokoro-fastapi` restores
    # the in-process faster-whisper + Kokoro-FastAPI pair.
    backend = audio_backend.resolve_backend()
    stt = audio_backend.make_stt(backend)

    # LLM: aisotropy's OpenAI-compat shim. The tenant JWT IS the api_key.
    # NOTE: aisotropy owns the system prompt server-side (persona + tenant
    # memory hydration); incoming system messages are ignored by the shim (v1
    # floor), so no system message is configured here.
    # `settings=` rather than the deprecated `model=` constructor argument (1.x
    # marks the latter for removal in 2.0.0).
    llm = OpenAILLMService(
        api_key=TENANT_JWT,
        base_url=SHIM_BASE_URL,
        settings=OpenAILLMService.Settings(model="aisotropy"),
    )

    tts = audio_backend.make_tts(backend)

    # Conversation context: Pipecat aggregates STT transcriptions into user
    # turns and assistant replies back into context. aisotropy re-hydrates the
    # real cross-session memory server-side from its own tiers; this local
    # context just carries the current session's transcript shape the OpenAI
    # wire expects.
    #
    # 1.x replaces the provider-specific `OpenAILLMContext` +
    # `llm.create_context_aggregator(...)` with the universal `LLMContext` and
    # `LLMContextAggregatorPair` — the migration path 0.0.108 itself names in
    # its deprecation warning. Silero VAD and the turn policy are constructor
    # arguments of the pair now; barge-in is ON by default
    # (`BaseUserTurnStartStrategy.enable_interruptions=True`,
    # user_start/base_user_turn_start_strategy.py:56), which is what the old
    # `PipelineParams(allow_interruptions=True)` used to say.
    #
    # VAD frames still reach the STT service even though the analyzer now sits
    # DOWNSTREAM of it: the aggregator emits them with `broadcast_frame`, which
    # pushes upstream and downstream (frame_processor.py:761-777).
    context = LLMContext()
    context_aggregator = audio_backend.make_context_aggregators(
        context, vad_analyzer=SileroVADAnalyzer()
    )

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

    worker = PipelineWorker(pipeline, params=PipelineParams())

    runner = WorkerRunner()
    print(
        f"voice bot up: shim={SHIM_BASE_URL} audio_backend={backend} "
        f"audio={audio_backend.tts_base_url(backend)} voice={audio_backend.tts_voice(backend)}"
    )
    print("speak into the mic; interrupt the bot mid-reply to exercise barge-in. Ctrl-C to quit.")
    await runner.run(worker)


if __name__ == "__main__":
    asyncio.run(main())
