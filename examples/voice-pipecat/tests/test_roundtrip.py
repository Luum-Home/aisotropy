"""TTS -> STT round-trip gated on WER (ADR-0069) — the audio golden test.

Synthesizes known sentences with Kokoro, transcribes them with faster-whisper,
and asserts the word error rate stays under an empirically grounded threshold.
This is the audio equivalent of the wire golden test in
`internal/api/openai_compat_test.go`: the same in-repo stack that will speak to
users proves, end to end, that what it says is intelligible enough to be
machine-recovered.

What this does NOT measure (honest boundary, ADR-0069): perceptual quality.
WER says "the words survive the trip", not "it sounds good" — MOS/human
territory.
"""

from __future__ import annotations

import pytest

import laneconf
from wer import wer

pytestmark = laneconf.require_voice_lane

# Empirical threshold — the `passQAThreshold` pattern from arena scoring
# (arenas/live/runner/scoring/scoring.go): a documented constant grounded in
# real measured fixtures, not a guess. Measured 2026-07-23 with Kokoro
# (af_heart, via Kokoro-FastAPI cpu:latest) -> faster-whisper base.en int8:
#   "Hello there."                              WER 0.000
#   "The quick brown fox ... lazy dog."         WER 0.000
#   "In 1969, Neil Armstrong ... Buzz Aldrin."  WER 0.000
#   "Doctor Rivera scheduled 42 ... deadline."  WER 0.083  ("Doctor" -> "Dr.")
# Worst observed error was FORMATTING (abbreviation/digit choices), not
# intelligibility. 0.20 = ~2x headroom over the worst measured case; a real
# regression (dropped clause, garbled number, noise) lands far above it.
WER_THRESHOLD = 0.20

# Varied length; numbers and proper nouns included deliberately — they are the
# first casualties of a bad TTS/STT pairing.
SENTENCES = [
    "Hello there.",
    "The quick brown fox jumps over the lazy dog.",
    "In 1969, Neil Armstrong walked on the Moon with Buzz Aldrin.",
    "Doctor Rivera scheduled 42 interviews across Barcelona and Montevideo before the deadline.",
]


@pytest.mark.parametrize("text", SENTENCES, ids=lambda s: s.split()[0].lower() + f"-{len(s.split())}w")
def test_roundtrip_wer(text, synth, transcribe):
    wav = synth(text, name=f"roundtrip_{abs(hash(text)) % 10**8}")
    hyp = transcribe(wav)
    score = wer(text, hyp)
    assert score <= WER_THRESHOLD, (
        f"round-trip WER {score:.3f} > {WER_THRESHOLD}\n  ref: {text!r}\n  hyp: {hyp!r}"
    )
