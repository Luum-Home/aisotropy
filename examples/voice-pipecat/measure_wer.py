#!/usr/bin/env python3
"""Metric #3: per-case WER against the selected audio backend (evidencia ejecutable).

The ADR-0069 lane ASSERTS `wer <= threshold`; a green run therefore proves the
bound, not the number. This script prints the number — the same sentences, the
same pinned fixtures, and the same in-repo `tests/wer.py` the lane uses — so a
threshold can be re-derived from measurement instead of inherited on faith.
It is the harness the 2026-07-27 voicebox spike wrote in a scratchpad, folded
into the example as that report recommended (§8).

Run it whenever the audio backend, its model revisions, or its voice changes:

    docker compose -f docker-compose.voicebox.yml up -d      # backend up
    .venv/bin/python measure_wer.py                          # default: voicebox
    VOICE_AUDIO_BACKEND=kokoro-fastapi .venv/bin/python measure_wer.py

Exit codes (repo guard convention): 0 = every case within threshold,
1 = at least one case over, 2 = could not evaluate (backend down / bad config).
"""

from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path[:0] = [str(HERE / "tests"), str(HERE)]

import audio_backend  # noqa: E402
from wer import wer  # noqa: E402  — tests/wer.py, the lane's own scorer

# The exact cases the lane asserts on, imported so the two can never drift.
from test_fixtures import FIXTURE_WER_THRESHOLD, FIXTURES  # noqa: E402
from test_roundtrip import SENTENCES, WER_THRESHOLD  # noqa: E402


def main() -> int:
    try:
        import httpx
    except ImportError:
        print("httpx not installed (pip install -r requirements.txt)", file=sys.stderr)
        return 2

    backend = audio_backend.resolve_backend()
    base = audio_backend.tts_base_url(backend)
    voice = audio_backend.tts_voice(backend)
    if backend != audio_backend.VOICEBOX:
        print(
            f"backend {backend!r} transcribes in-process, not over HTTP — this script "
            "only measures backends whose STT is an endpoint. Run the lane instead:\n"
            "  VOICE_LANE=real pytest tests/ -q",
            file=sys.stderr,
        )
        return 2

    tts_model = "kokoro" if backend == audio_backend.KOKORO_FASTAPI else "tts"
    print(f"backend={backend} base={base} voice={voice}")

    def synth(text: str) -> bytes:
        r = httpx.post(
            f"{base}/audio/speech",
            json={
                "model": tts_model,
                "voice": voice,
                "input": text,
                "response_format": "wav",
            },
            timeout=120.0,
        )
        r.raise_for_status()
        return r.content

    def transcribe(wav: bytes, name: str = "audio.wav") -> str:
        r = httpx.post(
            f"{base}/audio/transcriptions",
            files={"file": (name, wav, "audio/wav")},
            data={"model": audio_backend.VOICEBOX_STT_MODEL_ID, "language": "en"},
            timeout=120.0,
        )
        r.raise_for_status()
        return r.json()["text"].strip()

    try:
        breached = False

        print(f"\n-- round-trip TTS->STT (threshold {WER_THRESHOLD}) --")
        worst = 0.0
        for text in SENTENCES:
            hyp = transcribe(synth(text))
            score = wer(text, hyp)
            worst = max(worst, score)
            breached |= score > WER_THRESHOLD
            print(f"  WER {score:.3f}  ref={text!r}")
            print(f"             hyp={hyp!r}")
        print(f"  worst {worst:.3f} / threshold {WER_THRESHOLD}")

        print(f"\n-- pinned fixtures, STT only (threshold {FIXTURE_WER_THRESHOLD}) --")
        fixture_worst = 0.0
        for name, ref in sorted(FIXTURES.items()):
            path = HERE / "tests" / "fixtures" / name
            if not path.is_file():
                print(f"  {name:14s} MISSING — run the lane once to generate it")
                return 2
            hyp = transcribe(path.read_bytes(), name)
            score = wer(ref, hyp)
            fixture_worst = max(fixture_worst, score)
            breached |= score > FIXTURE_WER_THRESHOLD
            print(f"  {name:14s} WER {score:.3f}  hyp={hyp!r}")
        print(f"  worst {fixture_worst:.3f} / threshold {FIXTURE_WER_THRESHOLD}")
    except Exception as e:  # noqa: BLE001 — any transport failure means "cannot evaluate"
        print(f"\nbackend {base} unreachable or failing: {e}", file=sys.stderr)
        return 2

    return 1 if breached else 0


if __name__ == "__main__":
    raise SystemExit(main())
