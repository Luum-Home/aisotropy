---
type: research
title: "i18n / multilingual readiness — guardrails, memory tiers, RAG, language-awareness (Phase 0)"
description: Read-only (code-truth) audit of aisotropy's multilingual posture. Answers whether the guardrails are multilingual-ready, whether the runtime does any i18n / language detection, whether the primitives/RAG/DB tiers handle multilingual content, and whether the guardrails are hardcoded English or library/model-backed. Every aisotropy claim is grounded in a file/ADR; every non-code claim cites a source URL.
tags: [research, i18n, multilingual, guardrails, embeddings, fts5, pii, toxicity, prompt-injection, phase-0]
timestamp: 2026-07-17T00:00:00Z
status: read-only research — no code changed, no commit
---

# i18n / multilingual readiness — Phase 0 research

**Scope:** read-only. No code was edited; no commit was made. Every aisotropy claim is grounded in a
file path + line; every library/model claim carries a source URL. Honest residuals are called out
where regex/heuristics genuinely cannot be replaced by more regex (toxicity and injection in
arbitrary languages are model problems, not pattern problems).

**One-line verdict (details in §2):** aisotropy is an **English-first runtime with language-agnostic
plumbing**. The storage/transport layers (BadgerDB working memory, Qdrant vectors, the fence, ctx
propagation) are Unicode-clean and carry any language losslessly, but **every semantic/heuristic layer
that has to *understand* text — the injection regexes, the toxicity keyword list, half the PII tier,
the default embedder, and the facts full-text tokenizer — is hardcoded to English or an English-only
model.** The runtime never detects, stores, or routes on the user's language: there is no i18n seam at
all.

---

## 1. Honest current-state matrix

Status legend: **EN-hardcoded** (English literals/regex/keywords baked in) · **EN-model** (relies on an
English-only model) · **lang-agnostic** (structural / Unicode-clean, works in any language) ·
**multilingual** (genuinely handles many languages) · **absent** (capability not present).

| Component | Status | Evidence (file · what) |
|---|---|---|
| **Prompt-injection detector** | **EN-hardcoded** | `internal/guardrails/injection.go:31-77` — 9 signals; 7 are English keyword regexes (`ignore\|forget\|disregard\|override … previous\|prior\|above … instruction`, `reveal … system prompt`, `you are now`, `DAN\|developer mode`, `bypass … safety`). Only `delimiter_break` (`</system>`, `[INST]`) and `base64_payload` are structural/lang-agnostic. No Unicode normalization → homoglyph/full-width/leetspeak evasion is unaddressed. |
| **Toxicity check** | **EN-hardcoded** | `internal/guardrails/output.go:42-57` — `builtinToxKeywords`: 14 English lowercase substrings (`kill you`, `hate`, `idiot`, `stupid`, `moron`, `worthless`, `shut up`, …). Code comment self-describes it as "intentionally small … not a comprehensive classifier". `WithToxicKeywords` (line 94) lets an integrator add terms, but there is no non-English default. |
| **PII redaction — secrets tier** | **lang-agnostic** | `internal/guardrails/redact.go:101-160` — email, credit-card (Luhn-gated), IPv4 (octet-validated), Bearer, PEM private key, provider key prefixes (`sk-`, `ghp_`, `AKIA`, `xox`), `AISOTROPY_*`. These are *structural* patterns; they fire regardless of surrounding language. |
| **PII redaction — identifiers tier** | **partly EN-hardcoded** | `redact.go:134-146` — phone (intl `+`/separated) and street-number shape are lang-agnostic-ish, but **postal code is keyword-anchored to English `zip`/`postal`** (line 140) and **street address requires English street-type suffixes** (`street\|avenue\|boulevard\|lane\|…`, line 146). Non-English address/postal forms are silently missed. Names / national-IDs (SSN/DNI/passport) / health IDs are explicitly out of scope (comment `redact.go:26-31`). |
| **System-prompt leak check** | **lang-agnostic (mechanically), semantically blind** | `output.go:241-284` `segmentEchoed` — normalized substring match (strips case/separators). Works on any script mechanically, but a **translated or paraphrased** echo of the system prompt is NOT caught (comment lines 236-240; pinned by `TestOutputLeakSemanticParaphraseResidual`, `docs/security/ai-layer-guardrails.md §5`). |
| **Untrusted-context fence** | **lang-agnostic** | `internal/api/chat_stream.go:52-86` — sentinel-tag wrapping + `sanitizeUntrusted`. Structural; independent of content language. But the **re-scan** of the fenced block (`chat_stream.go:356`) reuses the same **English** injection `Detector`, so a stored injection written in Spanish/Japanese is fenced but not *flagged*. (`docs/security/ai-layer-guardrails.md §1-3`.) |
| **Scan-on-ingest** | **EN-hardcoded (inherits detector)** | `docs/security/ai-layer-guardrails.md §2` + `internal/api/knowledge.go` `scanDocumentsForInjection` — runs the same English regex `Detector` over ingested docs; default-ON blocking. A non-English poisoned doc passes the gate. |
| **LLM-as-judge output check** | **multilingual (if wired) — but OFF by default** | `output.go:25-30` `OutputCheck` seam; `AISOTROPY_GUARDRAILS_LLM_JUDGE` = **OFF (opt-in)** (`docs/security/ai-layer-guardrails.md §4`). This is the **only** language-aware guardrail layer, and it is not shipped on. |
| **Embedder / semantic RAG (sovereign default)** | **EN-model** | ADR-0023 + `crates/embed/Cargo.toml:5` — pinned default **`BAAI/bge-small-en-v1.5`** (English-only). `internal/semantic/embed.go` `HashEmbedder` is lexical-overlap only (no cross-lingual match). Cross-lingual retrieval does **not** work on the shipped default. |
| **Embedder (BYO API path)** | **multilingual (external)** | `internal/semantic/api_embedder.go:19` default `text-embedding-3-small` (OpenAI, multilingual) — but this is the off-machine BYO path (ADR-0018), gated on `AISOTROPY_EMBED_URL`, and contradicts the sovereignty default. |
| **Facts tier full-text search (FTS5)** | **EN-hardcoded tokenizer** | `internal/facts/schema.go:23-28` — `tokenize='porter unicode61'`. **`porter`** is an English stemmer; **`unicode61`** folds diacritics but does **not** segment CJK/Thai/Japanese. Go-side `ftsQuery` (`sqlite.go:315-327`) splits on `unicode.IsLetter` — for a space-less CJK sentence the entire run collapses to **one token**, so full-text facts search is near-useless for CJK. |
| **Working memory (BadgerDB)** | **lang-agnostic** | `internal/memory/key.go:24` reserves only `:`, NUL, `\n`; values are opaque UTF-8 bytes. `internal/memory/summarizer.go:94` truncates on rune boundaries (`utf8.RuneStart`) — no mojibake on non-ASCII. Stores any language losslessly. |
| **Personas / system prompts** | **EN-hardcoded (overridable)** | `internal/api/persona.go:67-72` `systemPromptFor` — literal English ("You are the Aisotropy agent. Respond briefly and directly."). Consumers can override via `PersonaProvider` (any-language literal, ADR-0002 anti-magic), but there is no auto-detection or per-language selection. |
| **Primitives (friction/rewrite)** | **mechanism lang-agnostic, metadata EN-hardcoded** | `internal/primitives/primitives.go` — friction *scoring* is language-neutral (counts of failed/retried invocations, `primitives.go:56-63`), but `HeuristicRewriter` appends **English literal** guidance: `"[revised r%d: be precise about required inputs and the expected output shape]"` (`primitives.go:302-303`) and an English corrective example (`primitives.go:310-311`). |
| **Language detection / routing / storage** | **absent** | Repo-wide grep for `lang`, `locale`, `i18n`, `Accept-Language`, `language` returns only doc/comment hits (`internal/api/mcp_tools.go:19`, `internal/taskagent/runtask.go:27` — both about *programming* languages). **No** detection, **no** per-user language column, **no** language-based routing anywhere. |

---

## 2. The direct answers to (a)–(d)

### (a) Are the guardrails multilingual-ready? — **No, not the shipped ones.**
The three built-in guardrail heuristics are English-hardcoded:
- **Injection** = 7 English keyword-pair regexes + 2 structural signals (`injection.go`). An identical
  attack in Spanish ("ignora las instrucciones anteriores"), Japanese, or Arabic scores **0** on the
  keyword signals; only `delimiter_break`/`base64` could fire. No Unicode normalization means even the
  *English* patterns fall to homoglyph/full-width/leetspeak substitution.
- **Toxicity** = 14 English substrings, self-described as non-comprehensive (`output.go`). Zero coverage
  of insults/threats in any other language.
- **PII** = secrets tier is language-agnostic (structural), but **postal + street-address** are
  English-keyword/English-suffix-anchored (`redact.go`).

The **only** language-aware layer is the opt-in **LLM-as-judge** `OutputCheck` seam, which is **OFF by
default** (`AISOTROPY_GUARDRAILS_LLM_JUDGE`). So out of the box: guardrails are **English-only**. The
recent AI-safety hardening (untrusted-context fence, scan-on-ingest, default-ON injection blocking —
`docs/security/ai-layer-guardrails.md`) improved the *architecture* (defense-in-depth, blast-radius
containment) but did **not** change the language story: the fence is structural (good, lang-agnostic),
yet every *detector* it feeds is still the English regex `Detector`.

### (b) Does aisotropy do i18n / does it know the user's language? — **No. There is no i18n seam.**
Nothing detects, stores, or routes on the user's language. `tenant_id`/`user_id` propagate on
`context.Context`; there is no `language`/`locale` companion. Personas are English literals
(`persona.go`). The runtime is language-*neutral* (it will faithfully pass Spanish in and out via the
LLM, which is itself multilingual) but language-*unaware* (it can't adapt guardrails, tokenization,
embeddings, or personas to the detected language because it never determines it).

### (c) Do primitives / RAG / the DBs handle multilingual content? — **Mixed, per tier:**
- **Working memory (BadgerDB):** **Yes** — opaque UTF-8, rune-safe truncation. Lossless for any script.
- **Semantic RAG (Qdrant + embedder):** **No on the sovereign default.** Vectors are stored fine, but
  the shipped `bge-small-en-v1.5` is English-only, so cross-lingual and non-English monolingual
  retrieval quality collapses. (The BYO API path with a multilingual model works, but it's off-machine.)
- **Facts FTS5:** **Degraded.** `porter unicode61` stems as English and cannot segment CJK/Thai/Japanese;
  full-text recall for those languages is near-zero. Latin-script non-English (ES/PT/DE/FR) *works
  partially* — unicode61 matches the words, but the porter (English) stemmer mis-stems them, so
  inflected-form recall is weaker than it should be.
- **Primitives:** the friction/rewrite *mechanism* is language-agnostic; only the auto-appended rewrite
  *metadata* is English (cosmetic — it's guidance text, not a matcher).

### (d) Do the guardrails use hardcoded data or a library/model? — **Hardcoded data.**
All three built-in detectors are **hand-maintained regex/keyword tables compiled into the binary** — no
library, no model, no lookup table sourced externally. This is a deliberate design choice (deterministic,
zero-dependency, µs-cheap, no network — consistent with the sovereignty thesis and the 100% MIT/Apache
stack). The trade-off is exactly the language problem: hardcoded English data doesn't generalize. The
codebase already exposes the *right* extension seams (`WithToxicKeywords`, `OutputCheck`, custom redaction
patterns, `PersonaProvider`) — they're just English/empty by default.

---

## 3. Recommendations (opinionated, license-gated)

License gate reference: `.claude/rules/constitutional-gates.md` + `.claude/rules/cos/license-policy.md` —
stack must stay MIT/Apache/BSD; AGPL/SSPL/BSL and **non-OSI "source-available"/custom community licenses
are blockers**. Each recommendation below states the license of every proposed dependency/model.

### R1 — Add a language-detection seam (detect once, carry on ctx, store per-user). **Highest leverage.**
This is the missing keystone: almost every other fix wants to know the language first.
- **Detect** at the Huma handler boundary (same place `tenant_id` is injected), carry a `language` value
  on `context.Context` alongside tenant/user, and **persist it per-user** (a facts-tier column or a
  working-memory key — both already Unicode-clean).
- **Library:** **`lingua-go`** — **Apache-2.0** ✅, 75 languages, n-gram (1–5) + rule engine, strong on
  *short* text (the chat case) [1][2]. Caveat: all-models-loaded ≈ **1.8 GB RAM**; **low-accuracy mode ≈
  110 MB**, and you can restrict to a language subset — do that, this is not a "load all 75" situation
  [7]. Alternative: **`whatlanggo`** — **MIT** ✅, trigram, much lighter, but weaker on short snippets
  [3][4]. Recommendation: `lingua-go` in low-accuracy / restricted-language mode for the chat path.
- Once language is on ctx, guardrails/tokenizer/embedder/persona selection can all become
  language-conditional instead of English-fixed. **The memory tiers do need it** — the FTS tokenizer and
  the persona choice both branch on language; working memory and Qdrant do not (they're already opaque).

### R2 — Ship a multilingual embedder as the sovereign default (or make it the documented recommended pin).
`bge-small-en-v1.5` is the wrong default for any non-English deployment.
- **`BAAI/bge-m3`** — **MIT** ✅, **100+ languages**, dense+sparse+ColBERT, 8192-token context [5][6]. It
  is heavier than bge-small (larger model, ~560M params) — a real latency/footprint cost given the
  subprocess/daemon path (ADR-0023). Lighter option: **`intfloat/multilingual-e5-small`** — **MIT** ✅,
  ~118M params, ~100 languages, a near drop-in for the current small English model. Recommendation: keep
  the English model as a *fast-path option*, add `multilingual-e5-small` as the recommended multilingual
  pin, and document `bge-m3` for deployments that want best-in-class cross-lingual + hybrid retrieval.
  No interface change — it slots under the same `semantic.Embedder` seam ADR-0023 already defined.

### R3 — Fix the facts FTS5 tokenizer for non-English (two honest, separate problems).
- **Latin-script non-English (ES/PT/DE/FR):** `porter` mis-stems. Cheapest fix: drop `porter`, keep
  `unicode61 remove_diacritics 2` — you lose English stemming but stop mis-stemming everything else, and
  the Go-side re-rank (`sqlite.go` bm25×importance×recency) softens the recall loss. A language-aware
  choice (porter only when detected language == English, via R1) is the principled version.
- **CJK/Thai/Japanese:** genuinely unsupported. `unicode61` treats a space-less run as **one token** → 0
  hits [8][9]. The proper fix is the **ICU tokenizer**, but **`modernc.org/sqlite` is pure-Go and does
  NOT link ICU** — so ICU is not available without abandoning the pure-Go driver (a large architectural
  cost). The pragmatic pure-Go path is **bigram/trigram preprocessing** of CJK before indexing (a
  documented 7-line-class fix [10]) applied only when R1 detects a CJK language. Recommendation:
  language-aware — Latin path stays FTS5+unicode61, CJK path gets a bigram tokenizing shim in Go. Be
  honest in the docs that networked **rqlite** (the scale backend) shares this limitation.

### R4 — Move toxicity + non-English injection to the model/judge layer, not more regex. **(Honest: this is a model problem.)**
You cannot regex your way to multilingual toxicity or multilingual injection detection — the space of
insults/jailbreaks across languages, plus homoglyph/leetspeak/translation evasion, is unbounded. The
right layers already exist as seams; wire real detectors behind them:
- **Toxicity:** **`unitary/multilingual-toxic-xlm-roberta`** (Detoxify multilingual) — **Apache-2.0** ✅,
  XLM-RoBERTa, multilingual [11][12]. Run it behind the existing `OutputCheck` seam as an env-gated
  subprocess/daemon (the exact ADR-0005 pattern `crates/embed` and `bitrate-scorer` already use), so the
  Go core stays sovereign and it's dormant unless enabled.
- **Injection:** **`meta-llama/Llama-Prompt-Guard-2-86M`** (mDeBERTa-based, multilingual [13]) is the
  obvious candidate BUT it ships under the **Llama Community License — NOT OSI MIT/Apache → a license
  BLOCKER** under the constitutional gate; do **not** vendor it without an explicit ADR + exception. The
  underlying **`microsoft/mdeberta-v3-base` is MIT** ✅ — a clean-license path is to fine-tune mDeBERTa on
  open multilingual injection data yourself, or ship the English regex + LLM-judge combo and document the
  gap. Also add **Unicode NFKC normalization + confusable/homoglyph folding** in front of the *existing*
  injection regexes (cheap, in-process, catches full-width/leetspeak evasion of the English patterns) —
  that is a real, license-free win independent of any model.
- **PII (names / national-IDs):** **Microsoft Presidio** — **MIT** ✅, multilingual via spaCy/HF NER +
  regex + checksum + context [14][15]. But it is **Python-only**; there is **no Go-native equivalent**.
  So it can only be an **opt-in sidecar/service** (env-gated), not an in-process default — acceptable
  under the license gate (MIT) but a deployment-weight decision, not the sovereign default.

### R5 — Personas: make persona selection language-aware once R1 exists.
`PersonaProvider` already lets a consumer register per-language literals; with a detected `language` on
ctx you can key persona selection on it. Low priority (cosmetic vs. the safety/retrieval gaps above).

**Priority order:** R1 (detection seam) → R2 (multilingual embedder) → R3 (FTS tokenizer) → R4
(toxicity/injection to the model layer + Unicode normalization) → R5 (personas). R1 unlocks the
language-conditional behavior the rest depend on; R4's Unicode-normalization sub-item is independently
cheap and worth doing regardless.

---

## 4. Proposed Phase-2 test plan (the falsifiable baseline)

Build a **multilingual adversarial corpus** and assert the *current* miss rate first, so every later fix
has a measurable before/after. Languages: at minimum **ES, PT, DE, FR** (Latin), **JA, ZH** (CJK),
**AR** (RTL/non-Latin), **RU** (Cyrillic).

1. **Injection corpus** (`internal/guardrails`): translate the existing English jailbreak payloads
   (ignore-previous-instructions, reveal-system-prompt, DAN, bypass-safety) into all 8 languages, plus
   homoglyph/full-width/leetspeak variants of the *English* payloads. Assert `Detector.Inspect(...)`
   `.Suspicious == false` today for the non-English + evaded set → this is the documented baseline miss.
   A future model/normalization fix flips these to `true`.
2. **Toxicity corpus** (`internal/guardrails/output.go`): insults/threats in all 8 languages. Assert
   `Toxicity(...)` scores ~0 today. Baseline for R4.
3. **PII corpus** (`internal/guardrails/redact.go`): non-English postal codes (e.g. UK "SW1A 1AA", JP
   "〒100-0001"), non-English street addresses ("Calle Mayor 12", "Hauptstraße 5"), and non-Latin names.
   Assert the identifier tier misses postal/address in non-English forms today.
4. **Cross-lingual RAG** (`internal/semantic`): ingest a fact in Spanish, query the same meaning in
   English; assert the sovereign `bge-small-en-v1.5`/`HashEmbedder` default fails to retrieve it, and
   that `multilingual-e5-small`/`bge-m3` succeeds (parametrize the embedder). Baseline for R2.
5. **FTS5 CJK** (`internal/facts`): save a Japanese/Chinese fact, search a substring term; assert
   `porter unicode61` returns 0 rows today. Baseline for R3.
6. **Working-memory round-trip** (`internal/memory`): save+load a value in each script; assert byte-exact
   round-trip (this one should **pass** today — it's the control proving storage is already lang-agnostic).

Test 6 passing while 1–5 fail is the crisp statement of the finding: **storage is multilingual; cognition
is not.**

---

## Sources

Code (aisotropy, this repo): `internal/guardrails/{injection,output,redact,guardrails}.go`,
`internal/api/{chat_stream,persona,knowledge,core,ask}.go`, `internal/semantic/{embed,api_embedder,embedder_env}.go`,
`internal/facts/{schema,sqlite}.go`, `internal/memory/{key,summarizer}.go`,
`internal/primitives/primitives.go`, `crates/embed/Cargo.toml`,
`docs/decisions/0023-sovereign-local-embedder-semantic-tier.md`, `docs/security/ai-layer-guardrails.md`.

Internet (each a load-bearing non-code claim):
- [1] lingua-go (Apache-2.0, 75 langs, n-gram+rules, short-text) — https://github.com/pemistahl/lingua-go
- [2] lingua-go pkg docs — https://pkg.go.dev/github.com/pemistahl/lingua-go
- [3] whatlanggo (MIT, trigram) — https://github.com/abadojack/whatlanggo
- [4] whatlanggo LICENSE (MIT) — https://github.com/abadojack/whatlanggo/blob/master/LICENSE
- [5] bge-m3 model card (100+ languages, MIT) — https://build.nvidia.com/baai/bge-m3/modelcard
- [6] BGE-M3 docs (multi-lingual/granularity/functionality) — https://bge-model.com/bge/bge_m3.html
- [7] lingua-go memory (≈1.8 GB all-loaded / ≈110 MB low-accuracy, lazy-load, subset) — https://github.com/pemistahl/lingua-go
- [8] SQLite FTS5 unicode61 + CJK (one-token problem) — https://sqlite-users.sqlite.narkive.com/N5MOmskp/sqlite-why-sqlite-fts5-unicode61-tokenizer-does-not-support-cjk-chinese-japanese-krean
- [9] FTS5 unicode61/ascii tokenizer behavior — https://audrey.feldroy.com/articles/2025-01-13-SQLite-FTS5-Tokenizers-unicode61-and-ascii
- [10] Pure-code bigram fix for CJK in FTS5 — https://dev.to/foxck016077/sqlite-fts5-wont-tokenize-chinese-heres-the-7-line-bigram-fix-that-did-4fcc
- [11] Detoxify multilingual (unitary/multilingual-toxic-xlm-roberta, Apache-2.0) — https://huggingface.co/unitary/multilingual-toxic-xlm-roberta
- [12] Detoxify repo — https://github.com/unitaryai/detoxify
- [13] Llama Prompt Guard 2 86M (mDeBERTa, multilingual; Llama Community License — NOT MIT/Apache) — https://huggingface.co/meta-llama/Llama-Prompt-Guard-2-86M
- [14] Microsoft Presidio (MIT, multilingual NER-based PII, Python-only) — https://github.com/microsoft/presidio
- [15] Presidio PII guide (spaCy/HF NER + regex + context, multi-language) — https://explainx.ai/blog/microsoft-presidio-pii-detection-anonymization-guide-2026

---

## TRUST_REPORT

```
TRUST_REPORT: SCORE=88 STATUS=MEDIUM EVIDENCE=6 UNCERTAINTIES=4
---
Score: 88/100

WHAT I VERIFIED (code, by direct read):
  - injection.go:31-77 — 9 signals, 7 English keyword regexes, 2 structural (base64, delimiter). No Unicode normalization.
  - output.go:42-57 — 14 hardcoded English toxicity substrings; comment self-labels "not comprehensive".
  - redact.go:101-146 — secrets tier structural (lang-agnostic); postal keyword-anchored to EN "zip/postal"; street address requires EN street-type suffixes.
  - facts/schema.go:23-28 — FTS5 tokenize='porter unicode61' (English stemmer; no CJK segmentation). Confirmed ftsQuery uses unicode.IsLetter (sqlite.go:315).
  - crates/embed/Cargo.toml:5 + ADR-0023 — sovereign default embedder = BAAI/bge-small-en-v1.5 (English-only). API path default text-embedding-3-small.
  - grep across internal/ cmd/ aisotropy/ for lang|locale|i18n|Accept-Language — only programming-language doc hits; NO user-language detection/store/route.
  - memory/key.go:24 + summarizer.go:94 — BadgerDB keys reserve only :/NUL/\n, rune-safe truncation → lang-agnostic storage confirmed.
  - persona.go:67-72 — hardcoded English persona; overridable via PersonaProvider.
  - Library/model licenses each fetched: lingua-go Apache-2.0, whatlanggo MIT, bge-m3 MIT, multilingual-e5 MIT, Detoxify-multilingual Apache-2.0, Presidio MIT, Llama-Prompt-Guard-2 = Llama Community License (BLOCKER, flagged).

UNSURE ABOUT:
  - I did not run the multilingual corpus (Phase 2) — the "current guardrails miss non-English" claim is inferred from reading the regexes/keywords, not from an executed failing test. It is high-confidence but not executed. The §4 test plan is exactly to make it falsifiable.
  - modernc.org/sqlite ICU: I asserted pure-Go modernc does not link ICU based on general SQLite/ICU knowledge + search results, not a compile test against the exact pinned modernc version. The unicode61 CJK one-token limitation is well-sourced; the "no ICU in modernc" specific claim should be confirmed by trying to CREATE a fts5 table with tokenize='icu' against the vendored driver before relying on it.
  - I read the two primary guardrail-adjacent files in internal/api (core.go, chat_stream.go) via grep excerpts, not full end-to-end; the exact default value of AISOTROPY_GUARDRAILS_BLOCK_INJECTION=ON is taken from docs/security/ai-layer-guardrails.md §4 (doc), cross-checked against the wiring greps, not from reading aisotropy/app.go construction line-by-line.
  - bge-m3 vs multilingual-e5-small latency/footprint on aisotropy's subprocess/daemon path is a design concern I flagged but did not benchmark (ADR-0023 itself notes "no latency numbers survived verification").

HUMAN SHOULD CHECK:
  - Confirm modernc.org/sqlite rejects tokenize='icu' before committing to the bigram-shim path in R3 (one CREATE VIRTUAL TABLE test).
  - Confirm the Llama license blocker (R4) against your legal posture before anyone reaches for Prompt-Guard-2; the mDeBERTa (MIT) fine-tune path is the clean alternative.
  - Decide the sovereign-default trade in R2: keep bge-small-en fast-path + add multilingual-e5-small, vs. switch default to bge-m3 (heavier).
```

**Honest one-line verdict on aisotropy's multilingual readiness:** *Storage is multilingual; cognition
is English-only* — BadgerDB/Qdrant/the fence/ctx carry any language losslessly, but every layer that has
to **understand** text (injection + toxicity + half of PII = hardcoded English regex/keywords; the
default embedder + the facts FTS tokenizer = English-only models) is English-bound, and the runtime has
**no language-detection seam at all**, so it can't even adapt — making aisotropy **not multilingual-ready
today**, though the extension seams to fix it (OutputCheck, WithToxicKeywords, PersonaProvider, the
env-gated embedder) already exist and are the clean path forward.
