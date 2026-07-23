"""Word Error Rate — hand-rolled word-level Levenshtein (ADR-0069).

Deliberately NOT a dependency: jiwer (Apache-2.0) would pass the license gate,
but word-level WER is ~30 lines of stdlib, and an auditable in-tree primitive
beats a transitive dependency for something this small (the same call the Go
side makes with `arenas/live/runner/scoring` — pure functions, stdlib only).

WER = (substitutions + insertions + deletions) / len(reference words), computed
over NORMALIZED word lists: lowercase, hyphens to spaces, punctuation stripped
(intra-word apostrophes kept: "don't" stays one word). Can exceed 1.0 when the
hypothesis is much longer than the reference.
"""

from __future__ import annotations

import re

_PUNCT = re.compile(r"[^\w\s']", re.UNICODE)
_WS = re.compile(r"\s+")


def normalize(text: str) -> list[str]:
    """Lowercased, punctuation-stripped word list."""
    text = text.lower().replace("-", " ")
    text = _PUNCT.sub("", text)
    text = text.strip()
    return _WS.split(text) if text else []


def wer(reference: str, hypothesis: str) -> float:
    """Word error rate of hypothesis against reference (0.0 = perfect)."""
    ref = normalize(reference)
    hyp = normalize(hypothesis)
    if not ref:
        return 0.0 if not hyp else float(len(hyp))
    # Word-level Levenshtein, two-row DP; sub/ins/del all cost 1.
    prev = list(range(len(hyp) + 1))
    for i, r in enumerate(ref, 1):
        cur = [i]
        for j, h in enumerate(hyp, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (r != h)))
        prev = cur
    return prev[-1] / len(ref)
