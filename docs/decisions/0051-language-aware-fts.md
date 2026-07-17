---
type: ADR
title: "ADR-0051 — Language-aware facts FTS5: drop porter, accent-fold, and bigram space-less scripts (no ICU under pure-Go SQLite)"
description: The facts-tier full-text index used `tokenize='porter unicode61'` — the porter English stemmer mis-stems every non-English Latin token and unicode61 cannot word-segment space-less scripts (CJK/Japanese/Thai). The proper fix is the ICU tokenizer, but modernc.org/sqlite is pure-Go and does NOT link ICU (verified: `tokenize='icu'` fails with "no such tokenizer: icu"). This ADR drops porter for `unicode61 remove_diacritics 2` (accent-folded Latin/Cyrillic/Greek) and adds a Go-side character-bigram shim so space-less scripts become searchable, fed through a dedicated fts_content column so the pure-Go and rqlite backends stay byte-identical. Honest residual: no true morphological segmentation without ICU.
tags: [decisions, adr, i18n, multilingual, fts5, sqlite, tokenizer, facts, cjk, honesty]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0051 — Language-aware facts FTS5: drop porter, accent-fold, and bigram space-less scripts

## Status

✅ **Accepted (2026-07-17).** i18n multilingual-readiness fix (R3) from the Phase-0 research
`docs/research/i18n-multilingual-readiness-2026-07-17.md` §3. Sibling to the language-detection seam
(ADR-0050) and the multilingual embedder (ADR-0049); this ADR fixes the **facts full-text tokenizer**
independently — it does not require language detection to be wired to deliver its recall win.

## Context

The facts tier (`internal/facts`) stores durable statements about a user in embedded SQLite + FTS5,
running on the pure-Go, CGO-free **`modernc.org/sqlite`** driver (BSD-3), chosen so the binary builds
and runs with no C toolchain. The same schema (`schema.go` `schemaStatements`) is executed verbatim by
both the embedded SQLite store (`sqlite.go`) and the networked rqlite store (`rqlite.go`), so they can
never drift.

The FTS5 index was created with **`tokenize='porter unicode61'`**. Two defects for non-English text:

1. **`porter` is an English stemmer.** It mis-stems every non-English Latin token (ES/PT/DE/FR), so
   inflected-form recall for those languages is *worse* than no stemming at all — the stemmer actively
   distorts tokens it was never designed for.
2. **`unicode61` cannot word-segment space-less scripts.** A run of Han / Kana / Hangul / Thai
   characters with no interior spaces collapses to a **single token**. The Go-side query builder
   (`ftsQuery`) splits on `unicode.IsLetter`, so a whole CJK sentence becomes one term — full-text
   recall for those languages is effectively zero.

The correct fix for (2) is the **ICU tokenizer** (`tokenize='icu'`), which does real Unicode word
segmentation. But ICU ships as a C library; **`modernc.org/sqlite` is pure-Go and does not link it.**

### Verification (code-truth, not doc-truth)

Per `.claude/rules/prune-evidence-gate.md`, the ICU-unavailability claim was verified empirically
against the actual driver, not asserted from memory. A throwaway probe issued `CREATE VIRTUAL TABLE`
with each tokenizer through `modernc.org/sqlite`:

| Statement | Result |
|---|---|
| `... USING fts5(x, tokenize='icu')` | ❌ `SQL logic error: no such tokenizer: icu (1)` |
| `... USING fts5(x, tokenize='icu th_TH')` | ❌ `SQL logic error: no such tokenizer: icu (1)` |
| `... USING fts5(x, tokenize='unicode61 remove_diacritics 2')` | ✅ ok (`err=<nil>`) |
| `... USING fts5(x, tokenize='trigram')` | ✅ ok (`err=<nil>`) |

ICU is **confirmed unavailable** without abandoning the pure-Go driver (a large architectural cost we
are not paying here). `unicode61 remove_diacritics 2` and the built-in `trigram` tokenizer are both
available; we use the former plus an application-level bigram shim (rationale below).

## Decision

### 1. Drop `porter`; use `tokenize='unicode61 remove_diacritics 2'`

- Removing porter stops the mis-stemming of every non-English Latin token.
- `remove_diacritics 2` accent-folds Latin/Cyrillic/Greek on **both** the index side and the query
  side (FTS applies the table tokenizer to MATCH terms too), so an accented stored term is found by an
  unaccented query and vice versa (`café`↔`cafe`, `canción`↔`cancion`, `réunion`↔`reunion`) with no
  Go-side normalization.
- **Accepted trade-off:** English porter stemming is gone, so `refactor` no longer stem-matches
  `refactoring`. This is deliberate and pinned by `TestSearchNoPorterStemming`. The Go-side re-rank
  (bm25 × importance × recency, unchanged) softens the recall loss, and the honest default for a
  multilingual runtime is "don't English-stem everything." A language-*conditional* choice (porter only
  when detected language == English, via the ADR-0050 detection seam) is the principled future version;
  this ADR ships the language-*neutral* baseline that is correct for every language today.

### 2. Character-bigram shim for space-less scripts (CJK / Japanese / Thai)

Since ICU is unavailable, re-express each run of space-less characters as **overlapping character
bigrams** — a well-known pure-Go FTS5 workaround — so `unicode61` emits one token per bigram:

- **Index time** (`ftsIndexText`): every non-space-less character passes through verbatim (so Latin
  tokenizes exactly as before); each maximal run of Han/Hiragana/Katakana/Hangul/Thai is rewritten as
  space-separated overlapping bigrams (`数据库` → `数据 据库`). A lone ideograph stays itself.
- **Query time** (`ftsQuery`): the identical transform emits one quoted MATCH term per bigram
  (`数据库` → `"数据" OR "据库"`), so query and index agree. Non-CJK terms are unchanged (whole quoted
  word, single-char Latin noise dropped as before).

Script detection uses Go's `unicode.RangeTable`s (`unicode.Han`, `Hiragana`, `Katakana`, `Hangul`,
`Thai`) — no hardcoded ranges, no new dependency.

### 3. Feed the index from a dedicated `fts_content` column (backend-parity mechanism)

The bigram transform must run at **index** time, but the index is populated by pure-SQL triggers that
cannot call a Go function — and a custom SQL tokenizer registered on `modernc.org/sqlite` would not
exist on the networked **rqlite** backend, breaking schema parity. Instead:

- The `facts` table gains a `fts_content TEXT NOT NULL DEFAULT ''` column (added via a
  **duplicate-column-tolerant `ALTER`** so a pre-existing on-disk DB migrates on next start; `migrate`
  swallows `"duplicate column name"`).
- The external-content FTS5 table indexes `fts_content` (not the display `content` column). The three
  sync triggers reference `new.fts_content` / `old.fts_content`.
- Go computes `fts_content = ftsIndexText(content)` and binds it as a parameter in `upsertSQL` (kept in
  the `ON CONFLICT DO UPDATE SET` so the AFTER-UPDATE trigger always re-indexes from the fresh value).

Because the transform happens in Go **before** the SQL executes, both backends issue byte-identical
statements and stay in parity — no custom SQL function, no CGO, no per-connection registration.

## Consequences

**Positive**
- Non-English Latin recall improves (no mis-stemming; accent-folding on both sides).
- CJK/Japanese/Thai facts become searchable at all (previously ~0 recall) via the bigram path.
- No new dependency, no CGO, no license change (stays 100% pure-Go / BSD-3 driver). Backend parity
  preserved; the networked rqlite store gets the same behavior for free.

**Negative / honest residuals**
- **No true morphological segmentation.** Bigrams give substring recall, not real word boundaries or
  stemming, for space-less scripts — that needs ICU or a language-specific segmenter, neither available
  in-process under the pure-Go driver. Bigram matching is also coarser (a bigram can appear across a
  conceptual word boundary), tolerated because the Go-side re-rank orders results and precision is a
  secondary concern to *any* recall.
- **English stemming lost** (see trade-off above).
- **rqlite `ALTER`-tolerance is not locally verifiable.** The `duplicate column name` swallow in the
  rqlite migrate path mirrors the SQLite one, but rqlite is not provisioned in local dev/tests, so that
  specific branch is asserted by construction (SQLite's verbatim error text) rather than exercised.
- The `fts_content` column duplicates the preprocessed text on disk (external-content FTS already avoids
  duplicating the *raw* text; this adds the *preprocessed* copy in the base row). Acceptable for the
  facts tier's size.

## Verification

`go build ./... && go vet ./internal/facts/... && go test ./internal/facts/...` all exit 0.
New tests: `TestSearchAccentFoldingLatin` (diacritic-insensitive Latin match both directions),
`TestSearchCJKBigramPath` (Japanese + Chinese substrings found via bigrams, cross-script isolation),
`TestFtsIndexTextBigrams` / `TestFtsQueryBigrams` (pure-unit transform pinning), and
`TestSearchNoPorterStemming` (documents the deliberate loss of English stemming). Existing English FTS
tests still pass.

## Alternatives considered

- **Built-in `trigram` tokenizer** (available in modernc, per the probe): rejected — it changes MATCH
  semantics to substring-LIKE for *all* text and needs ≥3-char sequences, regressing normal English
  word search. The task and this ADR want a targeted CJK fix, not a global semantic change.
- **Custom Go SQL tokenizer via `sqlite.RegisterScalarFunction`**: rejected — would not exist on the
  networked rqlite backend, breaking the shared-schema parity invariant.
- **Abandon `modernc.org/sqlite` for a CGO SQLite with ICU**: rejected — it discards the zero-C-toolchain,
  single-binary property that is a core project constraint (see ADR-0045 for the same driver constraint
  in the encryption context).
