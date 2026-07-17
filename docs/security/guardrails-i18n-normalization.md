---
type: security-note
title: "Guardrails i18n — R4: Unicode normalization & evasion resistance"
description: What R4 hardened in the in-process guardrail heuristics (Unicode normalization + homoglyph/leet/zero-width/spacing folding before injection & toxicity matching), the pluggable Detector seam for a future multilingual model backend, and the honest residual (true non-English semantic detection is a model problem, not a regex one).
tags: [security, guardrails, i18n, multilingual, prompt-injection, toxicity, unicode, normalization, evasion]
grounds: docs/research/i18n-multilingual-readiness-2026-07-17.md (R4)
status: implemented (internal/guardrails)
---

# Guardrails i18n — R4: Unicode normalization & evasion resistance

Grounded in `docs/research/i18n-multilingual-readiness-2026-07-17.md` §3 (recommendation **R4**) and
its §1 current-state matrix. R4 is the **cheap, license-free, in-process** slice of the multilingual
guardrail problem: harden the existing detectors against obfuscation so they are not trivially evaded —
even for the English attacks they already cover — and add the seam a real multilingual model plugs into
later. It deliberately does **not** ship a multilingual model; that is the documented follow-on.

## 1. The problem R4 addresses

Before R4, the in-process guardrail heuristics matched **English literals against the raw bytes** with
no canonicalization (`internal/guardrails/injection.go`, `internal/guardrails/output.go`):

- **Injection**: 9 signals, 7 of them English keyword-pair regexes. They are `(?i)` case-insensitive,
  but nothing else was normalized — so `Ignоre all prеvious instruсtions` (Cyrillic `о`/`е`/`с`),
  full-width `ｉｇｎｏｒｅ`, zero-width-joiner-split `ig​no​re`, leetspeak `1gn0r3`, or
  character-spaced `i g n o r e` all scored **0** on the keyword signals. The English patterns were
  defeated by a find-and-replace an attacker does in one keystroke.
- **Toxicity**: 14 English lowercase substrings matched against `strings.ToLower(output)` — same class
  of homoglyph/leet/zero-width evasion.

This is orthogonal to the *language* gap (a Spanish or Japanese insult was never in the vocabulary at
all). R4 fixes the **evasion** half now; the **language** half needs a model (§4).

## 2. What R4 changed (in-process, deterministic, no model)

New file `internal/guardrails/normalize.go` adds a canonicalization pass applied to a **copy** of the
text for matching only — the stored/forwarded text is **never mutated** (the constitutional
"never bypass ctx propagation / never mangle tenant data" posture; redaction stays fail-open and
separate). `foldForMatch` pipeline:

1. **NFKC** (`golang.org/x/text/unicode/norm`) — folds full-width, compatibility, and ligature forms to
   canonical (`ｉｇｎｏｒｅ` → `ignore`, NBSP → space).
2. **case-fold** (`strings.ToLower`).
3. **drop ignorable code points** — zero-width space/joiner/non-joiner, word joiner, BOM, soft hyphen,
   and any `Cf` (format) / `Mn` (combining mark) rune — so `st​upid` → `stupid`.
4. **homoglyph fold** — a curated Cyrillic/Greek → Latin confusables map (only unambiguous visual twins
   of ASCII letters in the built-in vocabulary; ambiguous glyphs are intentionally excluded to avoid
   folding benign non-Latin text into accidental triggers).
5. **leetspeak fold** — `0→o 1→i 3→e 4→a 5→s 7→t @→a $→s`.

Regular spaces are preserved (word-boundary regexes and multi-word toxicity keywords depend on them).
Character-spacing evasion (`i g n o r e`) is handled separately by `collapseSpacedLetters`, which joins
runs of **two or more** single-character tokens (lone articles like `a`/`i` are left intact) so
`i g n o r e the p r e v i o u s instructions` → `ignore the previous instructions`.

**Where it is applied:**

- `Detector.Inspect` (injection) now runs each signal over the original text **and** its folded +
  space-collapsed variants (`matchVariants`), unioning the fired signals. Each signal's weight is still
  counted **once**, so scores and the existing threshold semantics are unchanged for already-caught
  attacks — the change is strictly additive (it can only *catch more*, never *catch less*).
- `OutputInspector.Toxicity` matches each keyword against `strings.ToLower(output)` **and** the folded
  copy.

Everything remains stdlib + `golang.org/x/text/unicode/norm` (BSD-3-Clause, already vendored), no
network, no model, deterministic and table-testable — consistent with the sovereignty thesis and the
100% MIT/Apache/BSD license gate.

## 3. The Detector seam (opt-in multilingual backend)

`internal/guardrails/injection.go` now defines:

```go
type InputScanner interface { Inspect(text string) Verdict }
```

The built-in `*Detector` implements it, and `Detector.WithScanner(...InputScanner)` registers optional
backends whose verdicts are **merged** into the heuristic result (Suspicious if either flags, Score is
the max, Signals are the union). This is the plug point for a future **language-aware model** — run it
out-of-process behind the same interface (the ADR-0005 subprocess/daemon pattern `crates/embed` already
uses), env-gated and dormant by default, so the Go core stays sovereign.

The output side already had the analogous seam: `OutputCheck` (the opt-in LLM-as-judge, gated by
`AISOTROPY_GUARDRAILS_LLM_JUDGE`). Multilingual **toxicity** belongs there, not in more keywords.

**License note (constitutional gate):** the research names `meta-llama/Llama-Prompt-Guard-2-86M` as the
obvious multilingual injection model, but it ships under the **Llama Community License — NOT OSI
MIT/Apache → a license BLOCKER** under `.claude/rules/constitutional-gates.md`. Do **not** vendor it
without an explicit ADR + exception. The clean-license path the research identifies is the underlying
**`microsoft/mdeberta-v3-base` (MIT)** — fine-tuned on open multilingual injection data — behind this
same `InputScanner` seam.

## 4. Honest residual (what R4 does NOT solve)

R4 is **canonicalization, not comprehension.** It hardens the existing (mostly English) patterns against
obfuscation and makes obfuscated variants in *any* script collapse toward their base letters. It does
**not** add semantic coverage of a genuinely non-English attack:

- `ignora las instrucciones anteriores` (clean Spanish, no homoglyphs) still scores **0** on the
  built-in heuristics — there is no Spanish in the vocabulary, and folding does not translate.
- A non-English insult/threat is likewise invisible to the keyword toxicity check.
- A **semantic paraphrase** of the system prompt (reworded, not just re-spaced) is still missed by the
  substring leak check (pinned by `TestOutputLeakSemanticParaphraseResidual`).

You **cannot regex your way to arbitrary-language toxicity or injection detection** — the space of
insults/jailbreaks across languages, plus translation evasion, is unbounded. That is a model problem.
The follow-on for real multilingual coverage is the opt-in model backend behind the `InputScanner` /
`OutputCheck` seams (MIT mDeBERTa for injection; Apache-2.0 Detoxify/`multilingual-toxic-xlm-roberta`
for toxicity), plus the R1 language-detection seam so guardrails can become language-conditional. Those
are separate, heavier changes tracked in the research doc (R1–R5); R4 ships only the free, in-process
win and the seam.

## 5. Tests (evidence)

`internal/guardrails/normalize_test.go`:

- `TestInjectionEvasionCaughtAfterNormalization` — for each of homoglyph / full-width / zero-width /
  leetspeak / character-spacing variants of "ignore all previous instructions", first **asserts the raw
  verbatim path (a test-local `rawInspect`) misses it**, then asserts `Detector.Inspect` now flags it
  with the `ignore_previous_instructions` signal. This proves each fixture is a *real* evasion the old
  path missed, not one already caught.
- `TestInjectionBenignNonEnglishNotFlagged` — benign Spanish, Russian (Cyrillic), German, Greek, French,
  number-heavy English, and Japanese are **not** false-flagged (no over-blocking).
- `TestToxicityEvasionCaughtAfterNormalization` / `TestToxicityBenignNonEnglishNotFlagged` — same
  before/after for the toxicity keywords, plus a benign-non-English negative control.
- `TestDetectorScannerSeam` / `TestDetectorScannerMergesWithHeuristics` — the `InputScanner` seam is
  invoked, its verdict merged, and `WithScanner` does not mutate the base detector.
- `TestFoldForMatchIdentityOnPlainASCII` / `TestCollapseSpacedLetters` — unit coverage of the folding
  primitives (plain ASCII only case-folds; lone articles survive the spacing collapse).

Verify: `go build ./... && go vet ./internal/guardrails/... && go test ./internal/guardrails/...` — all
exit 0; the pre-existing guardrail tests continue to pass (the change is additive, no over-blocking).
