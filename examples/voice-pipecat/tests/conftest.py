"""Shared fixtures for the voice testing lane (ADR-0069).

TTS backend: Kokoro-FastAPI over HTTP (`KOKORO_BASE_URL`, same container as
bot.py step 4 of the README). Chosen over the in-process `kokoro-onnx` pip
package for two reasons recorded in ADR-0069: kokoro-onnx transitively
requires `phonemizer-fork` (GPL-3.0) + a bundled espeak-ng (GPL-3.0) — the
container keeps those behind the separate-service license boundary the repo
already accepted for the PoC — and its espeakng-loader wheel was observed
broken on macOS arm64 (espeak data path baked to a CI runner path).

STT: faster-whisper (MIT), in-process; the model auto-downloads on first use.

Every fixture SKIPs (never fails) when its dependency is missing, with an
actionable reason. The lane itself is gated by ``laneconf.require_voice_lane``.
"""

from __future__ import annotations

import os
import subprocess
import wave

import pytest

import laneconf

KOKORO_BASE_URL = os.environ.get("KOKORO_BASE_URL", "http://localhost:8880/v1")

# scripts/resource-guard.sh at the repo root (commit 9e1a5d9). Absent on older
# checkouts -> no-op: a missing guard must not block the lane, but a BLOCKED
# verdict must (skip with reason, never an OOM-killed worker).
RESOURCE_GUARD = laneconf.TESTS_DIR.parents[2] / "scripts" / "resource-guard.sh"


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
def kokoro_api(resource_guard) -> str:
    """Base URL of a reachable Kokoro-FastAPI instance (skips when down)."""
    httpx = pytest.importorskip(
        "httpx", reason="httpx not installed (pip install -r requirements.txt)"
    )
    try:
        resp = httpx.get(f"{KOKORO_BASE_URL}/models", timeout=5.0)
        resp.raise_for_status()
    except Exception as e:  # noqa: BLE001 — any transport failure means "not up"
        pytest.skip(
            f"Kokoro-FastAPI not reachable at {KOKORO_BASE_URL} ({e}); start it with: "
            "docker run -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest"
        )
    return KOKORO_BASE_URL


@pytest.fixture(scope="session")
def synth(kokoro_api, tmp_path_factory):
    """text -> path of a mono 16-bit WAV synthesized by Kokoro."""
    import httpx

    outdir = tmp_path_factory.mktemp("tts")

    def _synth(text: str, name: str) -> str:
        resp = httpx.post(
            f"{kokoro_api}/audio/speech",
            json={
                "model": "kokoro",
                "voice": laneconf.KOKORO_VOICE,
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
    """A loaded faster-whisper model (auto-downloads on first use).

    On a resource-guard DEGRADED verdict the model is forced down to tiny.en
    with 2 CPU threads regardless of VOICE_STT_MODEL/VOICE_STT_THREADS —
    degrade before spending, applied to the dev machine.
    """
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
def transcribe(whisper):
    """WAV path -> transcript string."""

    def _transcribe(path: str) -> str:
        segments, _info = whisper.transcribe(str(path), language="en", beam_size=5)
        return " ".join(seg.text.strip() for seg in segments).strip()

    return _transcribe


def read_wav_pcm(path: str) -> tuple[bytes, int, int]:
    """Read a WAV file -> (pcm bytes, sample_rate, num_channels). stdlib only."""
    with wave.open(str(path), "rb") as w:
        if w.getsampwidth() != 2:
            raise ValueError(f"{path}: expected 16-bit PCM, got {w.getsampwidth() * 8}-bit")
        return w.readframes(w.getnframes()), w.getframerate(), w.getnchannels()
