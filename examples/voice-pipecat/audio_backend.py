"""Which audio backend the PoC's STT/TTS legs talk to (ADR-0077).

One switch, `VOICE_AUDIO_BACKEND`, selects both audio legs at once:

    voicebox        (DEFAULT) one container serving STT + TTS over the OpenAI
                    audio API — `POST /v1/audio/transcriptions` and
                    `POST /v1/audio/speech`. Bring-up:
                        docker compose -f docker-compose.voicebox.yml up -d
    kokoro-fastapi  the previous stack, kept reachable in ONE variable: a
                    Kokoro-FastAPI container for TTS plus an in-process
                    faster-whisper model for STT.

Reverting is deliberately a variable, not a code edit: voicebox is a v0.2.x,
single-author dependency with no API-stability promise, and it is adopted under
a license-policy §Exceptions boundary (ADR-0077). A stack that can only be
un-adopted by editing code is a stack you are stuck with.

The pipecat service constructors live here so `bot.py` and the ADR-0069 test
lane build the SAME chain — a lane that exercises a different chain than the bot
proves nothing about the bot. pipecat is imported lazily inside the factories so
importing this module costs nothing in the skip-lane (pytest-only) environment.
"""

from __future__ import annotations

import os

VOICEBOX = "voicebox"
KOKORO_FASTAPI = "kokoro-fastapi"
BACKENDS = (VOICEBOX, KOKORO_FASTAPI)

# --- voicebox ---------------------------------------------------------------
VOICEBOX_BASE_URL = os.environ.get("VOICEBOX_BASE_URL", "http://localhost:8790/v1")
# A KOKORO voice id, not an OpenAI one: voicebox forwards `voice` straight to the
# engine and does no OpenAI-name mapping (see voicebox_tts.py's module docstring).
VOICEBOX_VOICE = os.environ.get("VOICEBOX_VOICE", "af_heart")
# voicebox accepts and ignores the STT `model` form field (app.py:150); the model
# is fixed by the server's VOICEBOX_STT_MODEL. Sent for wire compatibility only.
VOICEBOX_STT_MODEL_ID = os.environ.get("VOICEBOX_STT_MODEL_ID", "stt")
VOICEBOX_API_KEY = os.environ.get("VOICEBOX_API_KEY", "")

# --- kokoro-fastapi (previous stack) ----------------------------------------
KOKORO_BASE_URL = os.environ.get("KOKORO_BASE_URL", "http://localhost:8880/v1")
# "alloy" (an OpenAI voice id, mapped server-side by Kokoro-FastAPI) — pipecat's
# OpenAITTSService rejects Kokoro ids like "af_heart" via its VALID_VOICES lookup,
# degrading into silent no-audio (ADR-0069 §3).
KOKORO_VOICE = os.environ.get("KOKORO_VOICE", "alloy")
WHISPER_MODEL = os.environ.get("WHISPER_MODEL", "distil-medium.en")


def resolve_backend() -> str:
    """Return the selected backend, failing loudly on a typo."""
    backend = os.environ.get("VOICE_AUDIO_BACKEND", VOICEBOX).strip().lower()
    if backend not in BACKENDS:
        raise ValueError(
            f"VOICE_AUDIO_BACKEND={backend!r} is not one of {BACKENDS}. "
            "A misspelled backend must fail at boot, not silently fall back."
        )
    return backend


def tts_base_url(backend: str) -> str:
    """Base URL of whichever service serves `POST /audio/speech`."""
    return VOICEBOX_BASE_URL if backend == VOICEBOX else KOKORO_BASE_URL


def tts_voice(backend: str) -> str:
    """Voice id valid for that backend's TTS engine."""
    return VOICEBOX_VOICE if backend == VOICEBOX else KOKORO_VOICE


def make_stt(backend: str, *, whisper_model: str | None = None):
    """Build the pipecat STT service for `backend`.

    voicebox: pipecat's stock `OpenAISTTService` — re-verified on 1.6.0 to
    accept `base_url` (`pipecat/services/openai/stt.py:76`, reaching
    `AsyncOpenAI(api_key, base_url)` at `whisper/base_stt.py:228`) and to POST a
    complete WAV as multipart to `{base_url}/audio/transcriptions`
    (`openai/stt.py:178`, `self._client.audio.transcriptions.create`). That is
    exactly voicebox's route contract.

    kokoro-fastapi: the previous in-process faster-whisper service.

    The model id goes through `settings=` rather than the `model=` constructor
    argument: 1.x deprecated the latter on both services (removal in 2.0.0), and
    `Settings` is a delta — unset fields keep the service's own defaults
    (`services/settings.py:226-231`).
    """
    if backend == VOICEBOX:
        from pipecat.services.openai.stt import OpenAISTTService

        return OpenAISTTService(
            api_key=VOICEBOX_API_KEY or "not-needed",
            base_url=VOICEBOX_BASE_URL,
            settings=OpenAISTTService.Settings(model=VOICEBOX_STT_MODEL_ID),
        )

    from pipecat.services.whisper.stt import WhisperSTTService

    return WhisperSTTService(
        settings=WhisperSTTService.Settings(model=whisper_model or WHISPER_MODEL)
    )


def make_context_aggregators(context, *, vad_analyzer=None):
    """Build the pipecat 1.x user/assistant context aggregator pair.

    Lives here for the same reason `make_stt`/`make_tts` do: `bot.py` and the
    ADR-0069 lane must build the SAME chain. In pipecat 0.0.x this was one
    provider-owned call — `llm.create_context_aggregator(OpenAILLMContext())` —
    so there was nothing to share. 1.x replaced it with the universal
    `LLMContextAggregatorPair`, which now also owns the VAD analyzer and the
    turn-taking policy, so the construction is big enough to drift.

    Args:
        context: an `LLMContext` (pipecat 1.x's provider-neutral replacement
            for `OpenAILLMContext`).
        vad_analyzer: the VAD analyzer, or `None` when VAD frames are injected
            externally (the file-based lane feeds `VADUser*SpeakingFrame`s
            itself instead of running Silero over the fixture audio).

    Turn policy: `SpeechTimeoutUserTurnStopStrategy` reproduces 0.0.x's
    VAD-silence end-of-turn (it fires on `VADUserStoppedSpeakingFrame`,
    user_stop/speech_timeout_user_turn_stop_strategy.py:163). pipecat 1.x's
    DEFAULT is `TurnAnalyzerUserTurnStopStrategy(LocalSmartTurnAnalyzerV3)`
    (user_turn_strategies.py:43-51) — a semantic end-of-turn ONNX model that
    ships inside the wheel. That is a genuine upgrade, but adopting it is a
    behavior change, not a migration, so it is deliberately NOT taken here.
    """
    from pipecat.processors.aggregators.llm_response_universal import (
        LLMContextAggregatorPair,
        LLMUserAggregatorParams,
    )
    from pipecat.turns.user_stop.speech_timeout_user_turn_stop_strategy import (
        SpeechTimeoutUserTurnStopStrategy,
    )
    from pipecat.turns.user_turn_strategies import UserTurnStrategies

    return LLMContextAggregatorPair(
        context,
        user_params=LLMUserAggregatorParams(
            vad_analyzer=vad_analyzer,
            user_turn_strategies=UserTurnStrategies(
                stop=[SpeechTimeoutUserTurnStopStrategy()]
            ),
        ),
    )


def make_tts(backend: str):
    """Build the pipecat TTS service for `backend`."""
    if backend == VOICEBOX:
        from voicebox_tts import VoiceboxTTSService

        return VoiceboxTTSService(
            base_url=VOICEBOX_BASE_URL,
            voice=VOICEBOX_VOICE,
            api_key=VOICEBOX_API_KEY or None,
        )

    from pipecat.services.openai.tts import OpenAITTSService

    return OpenAITTSService(
        api_key="not-needed",
        base_url=KOKORO_BASE_URL,
        model="kokoro",
        voice=KOKORO_VOICE,
    )
