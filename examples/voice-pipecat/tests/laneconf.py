"""Voice testing lane configuration (ADR-0069, ADR-0077).

Central place for the lane gate and the env-derived settings every test module
shares. The lane is OPT-IN: without ``VOICE_LANE=real`` every model-dependent
test SKIPs (never fails, never silently passes) — the stub/real precedent of
the arena (`.claude/rules/arena-full-matrix.md`).

The audio backend itself is NOT configured here: it lives one level up in
``audio_backend.py`` so `bot.py` and this lane build the same chain from the
same switch (``VOICE_AUDIO_BACKEND``, default ``voicebox``). This module only
adds the lane-specific knobs.

Environment:
    VOICE_LANE           "real" turns the lane on; anything else = skip
    VOICE_AUDIO_BACKEND  voicebox (default) | kokoro-fastapi — see audio_backend.py
    VOICE_STT_MODEL      faster-whisper model for the KOKORO-FASTAPI backend only
                         (default base.en; auto-downloads ~74MB. On memory-constrained
                         machines use tiny.en (~40MB) — expect slightly higher WER;
                         the round-trip threshold was calibrated on base.en). The
                         voicebox backend's STT model is fixed server-side by
                         VOICEBOX_STT_MODEL in docker-compose.voicebox.yml.
    VOICE_STT_THREADS    CPU threads for faster-whisper (default 0 = library default;
                         set 2 on loaded machines). kokoro-fastapi backend only.
"""

from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest

TESTS_DIR = Path(__file__).resolve().parent
FIXTURES_DIR = TESTS_DIR / "fixtures"
EXAMPLE_DIR = TESTS_DIR.parent

# The example's own modules (audio_backend, voicebox_tts) are the SAME ones
# bot.py imports; pytest only puts `tests/` on sys.path, so add the parent.
if str(EXAMPLE_DIR) not in sys.path:
    sys.path.insert(0, str(EXAMPLE_DIR))

import audio_backend  # noqa: E402  — must follow the sys.path insert above

WHISPER_MODEL = os.environ.get("VOICE_STT_MODEL", "base.en")
WHISPER_THREADS = int(os.environ.get("VOICE_STT_THREADS", "0"))

# Kept as a module attribute because test modules read it; it now resolves per
# backend (a Kokoro id for voicebox, an OpenAI id for Kokoro-FastAPI).
BACKEND = audio_backend.resolve_backend()
AUDIO_BASE_URL = audio_backend.tts_base_url(BACKEND)
TTS_VOICE = audio_backend.tts_voice(BACKEND)


def lane_on() -> bool:
    return os.environ.get("VOICE_LANE") == "real"


# Module-level gate: `pytestmark = laneconf.require_voice_lane` in every
# model-dependent test module. Missing deps/services are then handled by the
# conftest fixtures (importorskip / explicit skips with bring-up instructions).
require_voice_lane = pytest.mark.skipif(
    not lane_on(),
    reason="voice lane off — set VOICE_LANE=real to run TTS/STT tests "
    "(ADR-0069; needs the audio backend container up)",
)
