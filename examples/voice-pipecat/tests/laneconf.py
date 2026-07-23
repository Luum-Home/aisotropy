"""Voice testing lane configuration (ADR-0069).

Central place for the lane gate and the env-derived settings every test module
shares. The lane is OPT-IN: without ``VOICE_LANE=real`` every model-dependent
test SKIPs (never fails, never silently passes) — the stub/real precedent of
the arena (`.claude/rules/arena-full-matrix.md`).

Environment:
    VOICE_LANE        "real" turns the lane on; anything else = skip
    KOKORO_BASE_URL   Kokoro-FastAPI base URL (default http://localhost:8880/v1,
                      same container as bot.py — see conftest.py for why HTTP)
    KOKORO_VOICE      voice id                (default af_heart — same as bot.py)
    VOICE_STT_MODEL   faster-whisper model    (default base.en; auto-downloads ~74MB.
                      On memory-constrained machines use tiny.en (~40MB) — expect
                      slightly higher WER; the thresholds were calibrated on base.en)
    VOICE_STT_THREADS CPU threads for faster-whisper (default 0 = library default;
                      set 2 on loaded machines)
"""

from __future__ import annotations

import os
from pathlib import Path

import pytest

TESTS_DIR = Path(__file__).resolve().parent
FIXTURES_DIR = TESTS_DIR / "fixtures"

KOKORO_VOICE = os.environ.get("KOKORO_VOICE", "af_heart")
WHISPER_MODEL = os.environ.get("VOICE_STT_MODEL", "base.en")
WHISPER_THREADS = int(os.environ.get("VOICE_STT_THREADS", "0"))


def lane_on() -> bool:
    return os.environ.get("VOICE_LANE") == "real"


# Module-level gate: `pytestmark = laneconf.require_voice_lane` in every
# model-dependent test module. Missing deps/services are then handled by the
# conftest fixtures (importorskip / explicit skips with bring-up instructions).
require_voice_lane = pytest.mark.skipif(
    not lane_on(),
    reason="voice lane off — set VOICE_LANE=real to run TTS/STT tests "
    "(ADR-0069; needs the Kokoro container + a whisper model download)",
)
