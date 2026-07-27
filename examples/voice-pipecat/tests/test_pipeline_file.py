"""File-based Pipecat pipeline test: WAV in -> captured TTS frames out (ADR-0069).

Runs the SAME processor chain as bot.py — literally the same constructors, from
`audio_backend.make_stt()` / `make_tts()` (STT -> user context aggregator ->
aisotropy shim LLM -> TTS) — with **no audio device**: instead of
`LocalAudioTransport`, the utterance is injected as `InputAudioRawFrame`s
bracketed by the VAD frames a real transport's VAD would emit, and the TTS
output is captured from the pipeline as `TTSAudioRawFrame`s.

The seam is first-party: `pipecat.tests.utils.run_test` (BSD-2, shipped inside
the pipecat package) wraps a processor in a source/sink pair of queue
processors — exactly the harness pipecat uses for its own unit tests. Written
against the pinned `pipecat-ai>=0.0.60,<0.1` line (requirements.txt); NOT
compatible with pipecat 1.x (which removed `openai_llm_context`).

Requirements beyond the round-trip tests: a running aisotropyd with the JWT
authenticator (README step 1) and `AISOTROPY_TENANT_JWT` exported (step 2).
Each missing piece SKIPs with the bring-up command.
"""

from __future__ import annotations

import asyncio
import importlib.metadata
import os

import pytest

import audio_backend
import laneconf
from conftest import read_wav_pcm

pytestmark = laneconf.require_voice_lane

SHIM_BASE_URL = os.environ.get("AISOTROPY_OPENAI_BASE_URL", "http://localhost:8080/v1")
TENANT_JWT = os.environ.get("AISOTROPY_TENANT_JWT", "")

WHISPER_SAMPLE_RATE = 16000  # faster-whisper consumes raw PCM at 16kHz
CHUNK_MS = 20


def _require_shim():
    if not TENANT_JWT:
        pytest.skip(
            "AISOTROPY_TENANT_JWT not set — mint one: "
            "export AISOTROPY_TENANT_JWT=$(python3 mint_jwt.py --tenant acme --user alice)"
        )
    import httpx

    try:
        # Any HTTP response (even 404) proves the server is up; only a
        # transport-level failure means it is not.
        httpx.get(f"{SHIM_BASE_URL}/models", timeout=5.0)
    except Exception as e:  # noqa: BLE001
        pytest.skip(
            f"aisotropyd not reachable at {SHIM_BASE_URL} ({e}); start it: "
            "AISOTROPY_JWT_SECRET=... go run ./cmd/aisotropyd"
        )


def _resample_to_16k(pcm: bytes, rate: int, channels: int) -> bytes:
    """Linear-interpolation resample of 16-bit PCM to 16kHz mono."""
    np = pytest.importorskip("numpy")

    samples = np.frombuffer(pcm, dtype=np.int16)
    if channels > 1:
        samples = samples.reshape(-1, channels).mean(axis=1).astype(np.int16)
    if rate == WHISPER_SAMPLE_RATE:
        return samples.tobytes()
    n_out = int(len(samples) * WHISPER_SAMPLE_RATE / rate)
    x_out = np.linspace(0, len(samples) - 1, n_out)
    resampled = np.interp(x_out, np.arange(len(samples)), samples.astype(np.float32))
    return resampled.astype(np.int16).tobytes()


def test_pipeline_wav_in_tts_frames_out(audio_api, synth):
    """The full voice sandwich, headless: fixture WAV -> STT -> LLM -> TTS frames."""
    _require_shim()

    pytest.importorskip(
        "pipecat", reason="pipecat-ai not installed (pip install -r requirements.txt)"
    )
    version = importlib.metadata.version("pipecat-ai")
    if not version.startswith("0."):
        pytest.skip(
            f"pipecat-ai {version} installed; this test targets the pinned "
            "pipecat-ai>=0.0.60,<0.1 line (see requirements.txt)"
        )

    asyncio.run(_run_pipeline(synth))


async def _run_pipeline(synth):

    try:
        from pipecat.frames.frames import (
            InputAudioRawFrame,
            TTSAudioRawFrame,
            UserStartedSpeakingFrame,
            UserStoppedSpeakingFrame,
            VADUserStartedSpeakingFrame,
            VADUserStoppedSpeakingFrame,
        )
        from pipecat.pipeline.pipeline import Pipeline
        from pipecat.pipeline.task import PipelineParams
        from pipecat.processors.aggregators.openai_llm_context import OpenAILLMContext
        from pipecat.services.openai.llm import OpenAILLMService
        from pipecat.tests.utils import SleepFrame, run_test
    except ImportError as e:  # partial install (e.g. missing transitive dep)
        pytest.skip(f"pipecat import incomplete: {e} — pip install -r requirements-test.txt")

    # The input utterance comes from the same TTS the fixtures use, so the
    # test needs no audio hardware AND no checked-in large binary.
    wav_path = synth("What is the capital of France?", name="pipeline_input")
    pcm, rate, channels = read_wav_pcm(wav_path)
    pcm16k = _resample_to_16k(pcm, rate, channels)

    # The SAME constructors bot.py uses — a lane that builds a different chain
    # than the bot proves nothing about the bot.
    stt = audio_backend.make_stt(laneconf.BACKEND, whisper_model=laneconf.WHISPER_MODEL)
    llm = OpenAILLMService(api_key=TENANT_JWT, base_url=SHIM_BASE_URL, model="aisotropy")
    tts = audio_backend.make_tts(laneconf.BACKEND)
    context_aggregator = llm.create_context_aggregator(OpenAILLMContext())

    pipeline = Pipeline([stt, context_aggregator.user(), llm, tts])

    # The utterance, as a transport-with-VAD would deliver it: user starts
    # speaking, audio chunks arrive, user stops -> STT segments and
    # transcribes -> aggregator emits the LLM context -> shim streams the
    # reply -> TTS synthesizes it.
    chunk_bytes = WHISPER_SAMPLE_RATE * 2 * CHUNK_MS // 1000
    audio_frames = [
        InputAudioRawFrame(
            audio=pcm16k[i : i + chunk_bytes],
            sample_rate=WHISPER_SAMPLE_RATE,
            num_channels=1,
        )
        for i in range(0, len(pcm16k), chunk_bytes)
    ]
    frames_to_send = [
        UserStartedSpeakingFrame(),
        VADUserStartedSpeakingFrame(),
        *audio_frames,
        VADUserStoppedSpeakingFrame(),
        UserStoppedSpeakingFrame(),
        # Give STT (whisper on CPU) + LLM (shim) + TTS (Kokoro) time to flush
        # before run_test sends EndFrame.
        SleepFrame(sleep=15.0),
    ]

    down_frames, _up_frames = await run_test(
        pipeline,
        frames_to_send=frames_to_send,
        pipeline_params=PipelineParams(
            audio_in_sample_rate=WHISPER_SAMPLE_RATE,
            audio_out_sample_rate=24000,
            allow_interruptions=True,
        ),
    )

    tts_audio = b"".join(
        f.audio for f in down_frames if isinstance(f, TTSAudioRawFrame)
    )
    assert tts_audio, (
        "no TTSAudioRawFrame captured — the WAV-in did not produce TTS-out; "
        f"downstream frames: {[type(f).__name__ for f in down_frames]}"
    )
    # At least ~0.2s of 24kHz 16-bit audio: a real spoken reply, not a click.
    assert len(tts_audio) > 0.2 * 24000 * 2, (
        f"TTS audio suspiciously short: {len(tts_audio)} bytes"
    )
