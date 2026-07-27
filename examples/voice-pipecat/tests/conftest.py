"""Shared fixtures for the voice testing lane (ADR-0069, ADR-0077).

Audio backend: whatever ``VOICE_AUDIO_BACKEND`` selects (default ``voicebox``).

  voicebox        BOTH legs over HTTP against one container
                  (`docker-compose.voicebox.yml`): `POST /v1/audio/speech` for
                  TTS and `POST /v1/audio/transcriptions` for STT. The container
                  is the license boundary: voicebox is MIT but its TTS engines
                  pull GPL-3.0 code (`phonemizer-fork` via `kokoro-onnx`,
                  `piper-tts`), adopted under license-policy §Exceptions as a
                  separate, unmodified, HTTP-only service — ADR-0077. Nothing
                  GPL is pip-installed into this venv.
  kokoro-fastapi  the previous stack: Kokoro-FastAPI container for TTS +
                  in-process faster-whisper (MIT) for STT.

Every fixture SKIPs (never fails) when its dependency is missing, with an
actionable reason. The lane itself is gated by ``laneconf.require_voice_lane``.
"""

from __future__ import annotations

import subprocess
import sys
import wave
from pathlib import Path

import pytest

# The example's own modules (audio_backend, voicebox_tts) live one level up.
# pytest puts only `tests/` on sys.path, and conftest is imported BEFORE any
# test module, so the bootstrap has to happen here — not only in laneconf.
_EXAMPLE_DIR = str(Path(__file__).resolve().parent.parent)
if _EXAMPLE_DIR not in sys.path:
    sys.path.insert(0, _EXAMPLE_DIR)

import audio_backend  # noqa: E402  — must follow the sys.path bootstrap above
import laneconf  # noqa: E402

# scripts/resource-guard.sh at the repo root (commit 9e1a5d9). Absent on older
# checkouts -> no-op: a missing guard must not block the lane, but a BLOCKED
# verdict must (skip with reason, never an OOM-killed worker).
RESOURCE_GUARD = laneconf.TESTS_DIR.parents[2] / "scripts" / "resource-guard.sh"

_BRING_UP = {
    audio_backend.VOICEBOX: (
        "docker compose -f examples/voice-pipecat/docker-compose.voicebox.yml up -d --build"
    ),
    audio_backend.KOKORO_FASTAPI: (
        "docker run -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest"
    ),
}


@pytest.fixture(scope="session")
def resource_guard() -> bool:
    """Pre-flight machine check. Returns True when DEGRADED (small model, capped
    threads); skips the session's heavy tests when the machine is saturated."""
    if not RESOURCE_GUARD.is_file():
        return False
    rc = subprocess.run(
        ["bash", str(RESOURCE_GUARD)], capture_output=True, text=True
    ).returncode
    if rc == 3:
        pytest.skip("machine saturated (resource-guard BLOCKED) — retry later")
    return rc == 1


@pytest.fixture(scope="session")
def audio_api(resource_guard) -> str:
    """Base URL of the reachable audio backend (skips when it is down)."""
    httpx = pytest.importorskip(
        "httpx", reason="httpx not installed (pip install -r requirements.txt)"
    )
    url = laneconf.AUDIO_BASE_URL
    try:
        resp = httpx.get(f"{url}/models", timeout=5.0)
        resp.raise_for_status()
    except Exception as e:  # noqa: BLE001 — any transport failure means "not up"
        pytest.skip(
            f"audio backend {laneconf.BACKEND!r} not reachable at {url} ({e}); "
            f"start it with: {_BRING_UP[laneconf.BACKEND]}"
        )
    return url


# Back-compat alias: `kokoro_api` was the fixture name before the backend became
# switchable. Kept so an out-of-tree test (or a stale checkout) does not break.
@pytest.fixture(scope="session")
def kokoro_api(audio_api) -> str:
    return audio_api


@pytest.fixture(scope="session")
def synth(audio_api, tmp_path_factory):
    """text -> path of a mono 16-bit WAV synthesized by the backend's TTS."""
    import httpx

    outdir = tmp_path_factory.mktemp("tts")

    def _synth(text: str, name: str) -> str:
        resp = httpx.post(
            f"{audio_api}/audio/speech",
            json={
                # voicebox reads and ignores `model` (app.py:150); Kokoro-FastAPI
                # wants "kokoro". Both accept response_format=wav and return a
                # complete, correctly-sized RIFF header.
                "model": "kokoro" if laneconf.BACKEND == audio_backend.KOKORO_FASTAPI else "tts",
                "voice": laneconf.TTS_VOICE,
                "input": text,
                "response_format": "wav",
            },
            timeout=120.0,
        )
        resp.raise_for_status()
        path = outdir / f"{name}.wav"
        path.write_bytes(resp.content)
        return str(path)

    return _synth


@pytest.fixture(scope="session")
def whisper(resource_guard):
    """A loaded in-process faster-whisper model (kokoro-fastapi backend only).

    On a resource-guard DEGRADED verdict the model is forced down to tiny.en
    with 2 CPU threads regardless of VOICE_STT_MODEL/VOICE_STT_THREADS —
    degrade before spending, applied to the dev machine.
    """
    if laneconf.BACKEND != audio_backend.KOKORO_FASTAPI:
        pytest.skip(
            f"in-process faster-whisper is only used by the {audio_backend.KOKORO_FASTAPI} "
            f"backend; VOICE_AUDIO_BACKEND={laneconf.BACKEND} transcribes over HTTP"
        )
    fw = pytest.importorskip(
        "faster_whisper",
        reason="faster-whisper not installed (pip install -r requirements.txt)",
    )
    model = "tiny.en" if resource_guard else laneconf.WHISPER_MODEL
    threads = 2 if resource_guard else laneconf.WHISPER_THREADS
    try:
        return fw.WhisperModel(model, device="cpu", compute_type="int8", cpu_threads=threads)
    except Exception as e:  # noqa: BLE001 — first run needs network for the download
        pytest.skip(f"faster-whisper model {model!r} unavailable: {e}")


@pytest.fixture(scope="session")
def transcribe(request, audio_api):
    """WAV path -> transcript string, via the selected backend's STT."""
    if laneconf.BACKEND == audio_backend.VOICEBOX:
        import httpx

        def _transcribe_http(path: str) -> str:
            with open(path, "rb") as fh:
                resp = httpx.post(
                    f"{audio_api}/audio/transcriptions",
                    files={"file": (str(path).rsplit("/", 1)[-1], fh, "audio/wav")},
                    data={"model": audio_backend.VOICEBOX_STT_MODEL_ID, "language": "en"},
                    timeout=120.0,
                )
            resp.raise_for_status()
            return resp.json()["text"].strip()

        return _transcribe_http

    whisper_model = request.getfixturevalue("whisper")

    def _transcribe_local(path: str) -> str:
        segments, _info = whisper_model.transcribe(str(path), language="en", beam_size=5)
        return " ".join(seg.text.strip() for seg in segments).strip()

    return _transcribe_local


def read_wav_pcm(path: str) -> tuple[bytes, int, int]:
    """Read a WAV file -> (pcm bytes, sample_rate, num_channels). stdlib only."""
    with wave.open(str(path), "rb") as w:
        if w.getsampwidth() != 2:
            raise ValueError(f"{path}: expected 16-bit PCM, got {w.getsampwidth() * 8}-bit")
        return w.readframes(w.getnframes()), w.getframerate(), w.getnchannels()
