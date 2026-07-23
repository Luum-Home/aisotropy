"""Unit tests for the hand-rolled WER (ADR-0069).

These are pure stdlib and ALWAYS run — they are the deterministic floor of the
voice lane, the piece that stays green in per-PR CI with no models installed.
"""

from __future__ import annotations

from wer import normalize, wer


def test_identical_is_zero():
    assert wer("the quick brown fox", "the quick brown fox") == 0.0


def test_case_and_punctuation_normalized():
    assert wer("Hello, there!", "hello there") == 0.0
    assert wer("Don't stop.", "don't stop") == 0.0


def test_hyphens_split_words():
    assert wer("twenty-three birds", "twenty three birds") == 0.0


def test_single_substitution():
    # 1 substitution over a 4-word reference
    assert wer("the quick brown fox", "the quick brown dog") == 0.25


def test_deletion_and_insertion():
    assert wer("a b c d", "a b c") == 0.25  # 1 deletion / 4
    assert wer("a b c", "a b c d") == 1 / 3  # 1 insertion / 3


def test_completely_wrong():
    assert wer("a b", "x y") == 1.0


def test_empty_reference():
    assert wer("", "") == 0.0
    assert wer("", "one two") == 2.0  # insertions counted, no zero-division


def test_can_exceed_one():
    assert wer("hi", "a b c d") > 1.0
