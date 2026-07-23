"""WAV fixtures with known transcripts -> STT asserts (ADR-0069).

Fixtures are small mono 16-bit WAVs living in `tests/fixtures/` next to a
transcript declared here. Decision (recorded in ADR-0069): fixtures under
~1MB total are COMMITTED for determinism — a pinned WAV keeps the STT half of
the round-trip stable even if the TTS voice/model changes underneath. When a
fixture is missing (first run, or after a deliberate deletion to re-pin), it
is generated via the same Kokoro backend the round-trip test uses.

Unlike test_roundtrip.py (which exercises today's TTS), this file freezes the
INPUT side: a known WAV must keep transcribing to its known text.
"""

from __future__ import annotations

import pytest

import laneconf
from wer import wer

pytestmark = laneconf.require_voice_lane

# The fixture WER threshold is tighter than the round-trip one: the WAV is
# pinned, so the only moving part is the STT model itself. Empirically all
# three fixtures below measured WER 0.000 on 2026-07-23 (Kokoro af_heart ->
# faster-whisper base.en int8); 0.15 absorbs STT formatting drift (digit vs
# word, abbreviations) while still failing on any real mistranscription.
FIXTURE_WER_THRESHOLD = 0.15

FIXTURES: dict[str, str] = {
    "greeting.wav": "Hello there.",
    "pangram.wav": "The quick brown fox jumps over the lazy dog.",
    "numbers.wav": "The meeting starts at 9 on March 3.",
}


def _ensure_fixture(name: str, transcript: str, request) -> str:
    path = laneconf.FIXTURES_DIR / name
    if not path.is_file():
        # Generate on first run through the session TTS backend (skips with
        # bring-up instructions if Kokoro is down).
        synth = request.getfixturevalue("synth")
        laneconf.FIXTURES_DIR.mkdir(parents=True, exist_ok=True)
        generated = synth(transcript, name=f"fixture_{name.removesuffix('.wav')}")
        path.write_bytes(open(generated, "rb").read())
    return str(path)


@pytest.mark.parametrize("name,transcript", sorted(FIXTURES.items()))
def test_fixture_transcript(name, transcript, transcribe, request):
    path = _ensure_fixture(name, transcript, request)
    hyp = transcribe(path)
    score = wer(transcript, hyp)
    assert score <= FIXTURE_WER_THRESHOLD, (
        f"fixture {name}: WER {score:.3f} > {FIXTURE_WER_THRESHOLD}\n"
        f"  ref: {transcript!r}\n  hyp: {hyp!r}"
    )
