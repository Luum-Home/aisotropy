"""Pipecat TTS service for voicebox's `POST /v1/audio/speech` (ADR-0077).

WHY THIS EXISTS INSTEAD OF pipecat's stock `OpenAITTSService`
-------------------------------------------------------------
voicebox speaks the OpenAI audio wire, so the obvious move is the same
`base_url` + `api_key` trick the LLM leg uses. It does not work for TTS, for one
concrete reason verified in the installed source (pipecat-ai 0.0.108):

    .venv/.../pipecat/services/openai/tts.py:242
        "voice": VALID_VOICES[self._settings.voice],

`VALID_VOICES` (tts.py:46-60) is a closed dict of the **13 OpenAI voice ids**
(`alloy`, `ash`, ... `verse`). A voice outside it raises `KeyError` inside
`run_tts`. Kokoro-FastAPI got away with this because it MAPS OpenAI voice names
to Kokoro voices server-side — which is why `bot.py` used to pass `alloy`.
voicebox does **no such mapping**: it forwards `voice` straight to the engine —
Kokoro (`external/voicebox/src/voicebox/tts.py:52`, `kokoro.create(..., voice=v)`)
or Piper (`tts_piper.py:78`) — and both know only their own ids (`af_heart`,
`en_US-amy-medium`). So `OpenAITTSService` can only send ids voicebox cannot
resolve, and voicebox can only accept ids `OpenAITTSService` refuses to send.
There is no seam to override: the lookup is inline in `run_tts`, not a method.

The alternatives were (a) mutating pipecat's `VALID_VOICES` dict at import time
— a library mutation that breaks silently the day the constant moves — or
(b) patching voicebox to add an alias table, which would VOID the "unmodified
service" condition the ADR-0077 license exception rests on. Both are worse than
~40 lines of explicit HTTP here. The STT leg needs none of this: pipecat's
`OpenAISTTService` accepts `base_url` and passes `model` through untouched.

CONTRACT USED (voicebox 0.2.12, `external/voicebox/src/voicebox/app.py:143-197`)
    POST {base_url}/audio/speech
    {"model", "input", "voice", "response_format": "pcm", "speed"}
    -> streamed raw s16le PCM, mono, with the format declared out-of-band in
       X-Audio-Sample-Rate / X-Audio-Channels / X-Audio-Sample-Format headers
       (the streaming path deliberately carries no WAV header — a placeholder
       size decodes to distorted audio in strict decoders, app.py:169-172).
    4xx on empty/oversized input, unsupported format, or out-of-range speed.

Licensed under this repo's terms; voicebox is a separate service (see ADR-0077).
"""

from __future__ import annotations

from typing import AsyncGenerator, Optional

import httpx
from loguru import logger

from pipecat.frames.frames import CancelFrame, EndFrame, ErrorFrame, Frame, TTSAudioRawFrame
from pipecat.services.tts_service import TTSService

# voicebox's Kokoro engine emits 24 kHz mono s16le (tts.py:20 `sample_rate = 24000`).
# The Piper engine's rate is voice-dependent, so the service reads the actual rate
# from the response header and warns on a mismatch rather than silently resampling.
VOICEBOX_SAMPLE_RATE = 24000


class VoiceboxTTSService(TTSService):
    """Stream TTS audio from a voicebox server over its OpenAI-shaped HTTP API."""

    def __init__(
        self,
        *,
        base_url: str,
        voice: str,
        model: str = "tts",
        api_key: Optional[str] = None,
        speed: Optional[float] = None,
        sample_rate: int = VOICEBOX_SAMPLE_RATE,
        timeout_s: float = 120.0,
        **kwargs,
    ):
        """Initialize the service.

        Args:
            base_url: voicebox base URL including the version prefix, e.g.
                ``http://localhost:8790/v1``.
            voice: an id the CONFIGURED ENGINE knows — a Kokoro id such as
                ``af_heart`` (``VOICEBOX_TTS_ENGINE=kokoro``) or a Piper id such
                as ``en_US-amy-medium``. Not an OpenAI voice name.
            model: sent for wire compatibility; voicebox reads and ignores it
                (``app.py:150`` binds it to ``_model``).
            api_key: sent as a bearer token when voicebox runs with
                ``VOICEBOX_API_KEY``; ``None`` for the keyless local default.
            speed: 0.25-4.0; anything else is a 400 from voicebox
                (``app.py:14-30``). ``None`` leaves voicebox's default of 1.0.
            sample_rate: expected output rate; the real rate is taken from the
                response header when present.
            timeout_s: per-request timeout.
        """
        super().__init__(
            sample_rate=sample_rate,
            push_start_frame=True,
            push_stop_frames=True,
            **kwargs,
        )
        self._base_url = base_url.rstrip("/")
        self._voice = voice
        self._model = model
        self._speed = speed
        headers = {"Authorization": f"Bearer {api_key}"} if api_key else {}
        self._client = httpx.AsyncClient(timeout=timeout_s, headers=headers)

    def can_generate_metrics(self) -> bool:
        """This service reports TTFB/usage metrics."""
        return True

    async def stop(self, frame: EndFrame):
        """Close the HTTP client on pipeline end."""
        await super().stop(frame)
        await self._client.aclose()

    async def cancel(self, frame: CancelFrame):
        """Close the HTTP client on cancellation (barge-in tears the request down)."""
        await super().cancel(frame)
        await self._client.aclose()

    async def run_tts(self, text: str, context_id: str) -> AsyncGenerator[Frame, None]:
        """Synthesize ``text`` and yield audio frames as PCM arrives.

        The base class emits TTSStartedFrame/TTSStoppedFrame around this
        generator (``push_start_frame``/``push_stop_frames``), so only audio
        frames are yielded here — the same division of labour as
        ``OpenAITTSService.run_tts``.
        """
        logger.debug(f"{self}: Generating TTS [{text}]")
        payload = {
            "model": self._model,
            "input": text,
            "voice": self._voice,
            "response_format": "pcm",
        }
        if self._speed is not None:
            payload["speed"] = self._speed

        try:
            async with self._client.stream(
                "POST", f"{self._base_url}/audio/speech", json=payload
            ) as response:
                if response.status_code != 200:
                    # voicebox answers 4xx with a JSON {"detail": ...}; read it
                    # so the ErrorFrame names the real reason (bad voice,
                    # oversized input, unsupported format) instead of a code.
                    body = (await response.aread()).decode("utf-8", "replace")[:500]
                    logger.error(
                        f"{self} error getting audio (status: {response.status_code}, "
                        f"error: {body})"
                    )
                    yield ErrorFrame(
                        error=f"voicebox TTS error (status: {response.status_code}, error: {body})"
                    )
                    return

                rate = int(response.headers.get("x-audio-sample-rate", self.sample_rate))
                if rate != self.sample_rate:
                    # Piper voices are not all 24 kHz. Frames carry the true rate
                    # so the transport resamples instead of playing it chipmunked.
                    logger.warning(
                        f"{self}: voicebox reports {rate}Hz, service configured for "
                        f"{self.sample_rate}Hz — emitting frames at {rate}Hz"
                    )

                await self.start_tts_usage_metrics(text)

                async for chunk in response.aiter_bytes(self.chunk_size):
                    if chunk:
                        await self.stop_ttfb_metrics()
                        yield TTSAudioRawFrame(chunk, rate, 1, context_id=context_id)
        except httpx.HTTPError as e:
            yield ErrorFrame(error=f"voicebox TTS transport error: {e}")
