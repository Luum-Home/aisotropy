---
type: ADR
title: "ADR-0052 — Opt-in multilingual guardrail model: an mDeBERTa NLI worker behind the R4 InputScanner seam"
description: R4 (ADR-0050 line R4, normalize.go) added Unicode canonicalization — NFKC + case-fold + homoglyph/leet folding — that hardens the built-in English injection/toxicity regexes against obfuscation. But canonicalization is not translation - a CLEAN non-English attack such as "ignora las instrucciones anteriores" (Spanish) or "ignore as instruções anteriores" (Portuguese) folds to itself and scores 0 against English literal patterns; only a MODEL closes that gap. Decision - implement the opt-in model backend behind the SAME InputScanner interface R4 defined, following the ADR-0005 subprocess pattern (crates/embed / AISOTROPY_EMBED_BIN) so NO new Go dependency is added - a Go ModelScanner (internal/guardrails/scanner.go) shells out to a new candle Rust worker (crates/guardrail-model) that runs a pinned MIT multilingual zero-shot-NLI model (default MoritzLaurer/mDeBERTa-v3-base-mnli-xnli, DeBERTa-V2 arch, ~100 languages), scoring whether the user message (premise, any language) entails an English injection hypothesis. Env-gated OFF BY DEFAULT (AISOTROPY_GUARDRAIL_MODEL_BIN unset ⇒ regex-only path, byte-for-byte). Detection is additive and fails OPEN. License-gated MIT/Apache only (Llama-Prompt-Guard-2 = BSL-family BLOCKER). Ships as a proven Go seam + a functional-but-minimal worker whose end-to-end multilingual accuracy is explicitly PoC-not-yet-benchmarked.
tags: [decisions, adr, i18n, multilingual, guardrails, injection, prompt-injection, nli, mdeberta, candle, subprocess, security, seam]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0052 — Opt-in multilingual guardrail model: an mDeBERTa NLI worker behind the R4 InputScanner seam

## Status

✅ **Accepted — 2026-07-17.** Implements recommendation **R4** ("toxicity/injection at the model
layer") of the Phase-0 i18n readiness research
(`docs/research/i18n-multilingual-readiness-2026-07-17.md`), the follow-on that R4's own
canonicalization layer (`internal/guardrails/normalize.go`, and its doc
`docs/security/guardrails-i18n-normalization.md`) explicitly deferred to a model. This ADR delivers
the **opt-in model backend + the Go↔worker subprocess contract**; the worker's real-model
multilingual accuracy benchmark is a separate, model-present follow-on (see *PoC vs Production*).

## Context

R4 shipped `normalize.go`: before the built-in injection/toxicity heuristics run, each input is
canonicalized — NFKC (folds full-width / compatibility / ligature forms), case-fold,
zero-width/format/combining-mark stripping, and a curated Cyrillic/Greek homoglyph + leetspeak fold
— so obfuscated variants of the **English** attack vocabulary collapse toward the detectable form.
That is a real hardening: `"Ignоre"` (Cyrillic о), `"ｉｇｎｏｒｅ"`, `"i g n o r e"`, and `"1gn0r3"` all
now fire the `ignore_previous_instructions` pattern.

But R4's own doc is emphatic about what it does **not** do:

> HONEST SCOPE: this is canonicalization, not comprehension. … it does NOT add semantic coverage of
> a genuinely non-English insult or jailbreak. True multilingual toxicity/injection detection is a
> model problem … You cannot regex your way to arbitrary-language safety.

The gap is concrete and testable: a **clean** Spanish injection —
`"ignora las instrucciones anteriores"` — contains no homoglyphs, no leetspeak, no spacing tricks.
Folding is a no-op on it; it simply is not English, so the English literal regexes score it **0**.
Same for Portuguese `"ignore as instruções anteriores"`. The runtime is, per the i18n research, an
"English-first runtime with language-agnostic plumbing": storage and transport carry any language
losslessly, but every layer that must *understand* text was hardcoded to English.

R4 anticipated exactly this and left a seam: the `InputScanner` interface in
`internal/guardrails/injection.go` (`Inspect(string) Verdict`) and `Detector.WithScanner(...)`,
documented as the plug-point for "an opt-in multilingual injection classifier — the research names
the MIT-licensed mDeBERTa family; NOT Llama Prompt Guard 2, whose Llama Community License is a
constitutional license BLOCKER." This ADR fills that seam.

## Decision

Add the multilingual model backend **behind the existing seam**, wired the ADR-0005 way — a
**separate worker invoked as a subprocess**, not a new in-process Go dependency, not CGO/FFI, not
WASM — mirroring `crates/embed` (`AISOTROPY_EMBED_BIN`) and `crates/sleep-cycle`.

### Components

1. **Go `ModelScanner`** (`internal/guardrails/scanner.go`) implements `InputScanner`. It execs a
   local worker over an NDJSON/stdio contract (temp `--input`/`--output` files, model dir in the
   env), keeping tenant text inside the process tree (zero network). It is attached to the built-in
   heuristics via `Detector.WithScanner`, so a model verdict only ever **raises** suspicion
   (`mergeVerdict`: max score, union of signals, OR of Suspicious). It is a value type, safe for
   concurrent use.

2. **Rust worker `crates/guardrail-model`** loads a pinned multilingual **zero-shot-NLI** model via
   candle and reuses candle-transformers' **maintained** `DebertaV2SeqClassificationModel` (per the
   ADR-0002 anti-magic rule — not hand-rolled). The user message is the NLI **premise** (any of the
   model's ~100 languages); a fixed English **hypothesis** asserts injection intent. NLI entailment
   is cross-lingual, so a Spanish/Portuguese premise against the English hypothesis yields a real
   entailment probability — that probability is the injection score:

   ```
   injection_score(text) = softmax( classifier(premise=text, hypothesis) )[entailment]
   ```

3. **ADR-0005 subprocess contract** (this file + the crate/scanner docs):

   ```
   argv:   --input <requests.ndjson> --output <results.ndjson>
   env:    AISOTROPY_GUARDRAIL_MODEL_DIR  (config.json + model.safetensors + tokenizer.json)
   input:  {"id":"...","text":"...","hypothesis":"..."}   (hypothesis optional; Go sends its default)
   output: {"id":"...","score":F,"label":"injection|benign","suspicious":B,"model_id":"<model>@<rev>"}
   stdout: {"scored":N,"model_id":"...","threshold":T,"backend":"NLI_REAL"}   (final summary line)
   exit:   0 = ok; non-zero = failure (reason on stderr — a Rust panic surfaces as a non-zero exit).
   ```

### Default model + license gate (constitutional)

Default: **`MoritzLaurer/mDeBERTa-v3-base-mnli-xnli`** — **MIT**, DeBERTa-V2 architecture (candle
`models::debertav2`), trained on MNLI + XNLI, ~100 languages. Sanctioned alternates, all MIT/Apache:
`MoritzLaurer/multilingual-MiniLMv2-L6-mnli-xnli` (MIT, smaller/faster),
`joeddav/xlm-roberta-large-xnli` (MIT, candle `models::xlm_roberta` —
`XLMRobertaForSequenceClassification` is also present should a swap be wanted).

**BLOCKED**: `meta-llama/Llama-Prompt-Guard-2-*`. The Llama Community License is a source-available,
field-of-use-restricted (BSL-family) license — a **constitutional BLOCKER** for a multi-tenant SaaS
runtime per `.claude/rules/cos/license-policy.md` and the repo's "100% MIT/Apache" stack gate. The
gate is documented in the crate header, this ADR, and `constitutional-gates.md` intent; there is no
model-download code to enforce it in-process, so it is a review/provisioning gate: do not point
`AISOTROPY_GUARDRAIL_MODEL_DIR` at a copyleft/source-available model.

### Env surface — OFF BY DEFAULT

| Variable | Role |
|---|---|
| `AISOTROPY_GUARDRAIL_MODEL_BIN` | Path to the built worker. **The only switch that opts the feature in.** Unset ⇒ dormant. |
| `AISOTROPY_GUARDRAIL_MODEL_DIR` | Pinned model dir (config.json + model.safetensors + tokenizer.json). Required when the bin is set. |
| `AISOTROPY_GUARDRAIL_THRESHOLD` | Scanner Suspicious cutoff, 0..1 (default 0.5). |
| `AISOTROPY_GUARDRAIL_HYPOTHESIS` | Override the NLI hypothesis. |
| `AISOTROPY_GUARDRAIL_MODEL_ID` / `_REV` | Echoed `model_id` label (worker-side). |

With `AISOTROPY_GUARDRAIL_MODEL_BIN` unset, `ModelScannerFromEnv()` returns `(nil, false)` and
`DetectorFromEnv()` returns exactly `NewDetector()` — no backend, so `Inspect` is **byte-for-byte
identical** to the pre-existing regex-only path (proven by
`TestDetectorFromEnvUnsetIsIdenticalToNewDetector`).

### Adoption (composition root — not changed by this ADR)

The wiring point is a one-liner the composition root (`aisotropy/app.go`) can adopt when ready,
left untouched here so the feature ships dark:

```go
guardrails.New(guardrails.Config{Detector: guardrails.DetectorFromEnv()})
```

## Consequences

### Positive
- The exact R4 gap closes when enabled: a clean Spanish/Portuguese injection that the regex path
  scores 0 is flagged via the model backend (`TestScannerClosesNonEnglishInjectionGap`).
- No new Go dependency, no CGO — the Go binary stays static/Distroless (ADR-0005 rationale).
- Additive + fail-open: a worker fault (missing binary, non-zero exit, timeout, malformed row)
  degrades to the regex-only verdict with a warning; it never hardens into a request failure and
  never removes the built-in heuristics.
- Tenant text stays in the process tree (zero network), consistent with the sovereignty thesis.

### Negative / costs
- Latency + footprint: a per-call subprocess pays process spawn + model load each time. A warm
  `--serve` daemon form (as `crates/embed` added) is the obvious optimization and is a documented
  follow-on, not built here.
- Model provisioning: weights download once into `AISOTROPY_GUARDRAIL_MODEL_DIR` (a one-time
  `huggingface-cli download`); the crate itself makes zero network calls. DeBERTa-v3 repos vary in
  whether they ship a fast `tokenizer.json` — the sanctioned default does; a repo that ships only a
  SentencePiece `spm.model` needs a one-time conversion.
- The license gate is a provisioning/review gate, not an in-process enforcement.

### PoC vs Production (honest scope)
- **Real and CI-verified**: the Go seam end-to-end (subprocess round-trip, threshold, fail-open,
  empty-input short-circuit, env gating, byte-for-byte-identical default) via a POSIX-sh fake
  worker — exactly the semantic tier's `writeFakeLocalEmbedBin` pattern; and the worker's pure
  scoring glue (softmax, entailment-index resolution, threshold verdict) via model-free Rust unit
  tests. `go build ./... && go vet ./internal/guardrails/... && go test ./internal/guardrails/...`
  and `cargo build && cargo test` all pass; gofmt + cargo fmt clean.
- **PoC — not yet benchmarked**: the real-model forward pass. It reuses the maintained candle
  DeBERTa-V2 seq-classification head, but is exercised only with a real model dir present (not in
  CI, which has no weights), so multilingual detection **accuracy** (precision/recall on a
  cross-lingual injection corpus) is unmeasured. Detection is real, not faked — but its quality is
  the explicit next step, alongside the warm-daemon latency form and the `app.go` adoption.

## Alternatives considered
- **XLM-RoBERTa NLI** (`joeddav/xlm-roberta-large-xnli`, MIT) via candle `xlm_roberta`: also viable
  and reliably ships `tokenizer.json`, but "large" (~560M) is heavier than the base mDeBERTa; kept
  as a sanctioned drop-in alternate rather than the default.
- **A Python `transformers` worker**: the industry-standard zero-shot path, but it adds a Python
  runtime + torch to provisioning and diverges from the repo's all-Rust worker precedent
  (`embed`, `bitrate-scorer`, `sleep-cycle`); rejected for consistency and footprint.
- **In-process Go inference**: would add a heavy ML dependency to the core binary, violating the
  ADR-0005 boundary rationale; rejected.
- **Llama Prompt Guard 2**: strong purpose-built injection model, but its license is a
  constitutional BLOCKER (above); rejected on license grounds.
