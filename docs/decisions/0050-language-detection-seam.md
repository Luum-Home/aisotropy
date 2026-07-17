---
type: ADR
title: "ADR-0050 — Language-detection seam: detect once at the request boundary, carry the language on ctx"
description: The i18n readiness research (docs/research/i18n-multilingual-readiness-2026-07-17.md, R1) found aisotropy had NO language-detection seam — nothing detected, carried, or routed on the user's language, so every semantic layer (injection/toxicity heuristics, persona selection, the facts FTS tokenizer, the default embedder) was hardcoded to English and could not even adapt. Decision — add an internal/lang package that detects the human language of a chat prompt ONCE, at the same Huma-handler boundary that already injects tenant_id/user_id, and carries the result on context.Context via a single canonical contract (lang.WithLanguage / lang.FromContext + the Language type). Detection uses pemistahl/lingua-go (Apache-2.0, license-gate PASS) in low-accuracy, language-restricted mode to bound memory. The chat handlers are the ONLY writer; personas (R5) and, later, language-conditional guardrails/tokenizer/embedder selection (R3/R4) are readers. Language persistence per-user is deferred as a documented follow-on. No behavior change when detection is Undetermined (consumers keep their English/default path).
tags: [decisions, adr, i18n, multilingual, language-detection, context-propagation, guardrails, personas, seam]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0050 — Language-detection seam: detect once at the request boundary, carry the language on ctx

## Status

✅ **Accepted — 2026-07-17.** Implements recommendation **R1** ("Add a language-detection seam —
detect once, carry on ctx, store per-user — highest leverage") of the Phase-0 i18n readiness
research (`docs/research/i18n-multilingual-readiness-2026-07-17.md`). R1 is the keystone the rest
of that research's recommendations depend on: R3 (facts FTS tokenizer), R4 (toxicity/injection at
the model layer), and R5 (language-aware personas) all need to know the language first. This ADR
delivers only the detection + ctx-propagation seam; R2–R5 are separate changes that consume it.

## Context

The research established, by direct code read, that aisotropy is an **English-first runtime with
language-agnostic plumbing**: storage/transport (BadgerDB working memory, Qdrant vectors, the
untrusted-context fence, the `context.Context` identity propagation) carry any language losslessly,
but **every layer that has to *understand* text is hardcoded to English** — the prompt-injection
regexes (`internal/guardrails/injection.go`), the toxicity keyword list
(`internal/guardrails/output.go`), the default embedder (`bge-small-en-v1.5`, ADR-0023), the facts
FTS5 tokenizer (`porter unicode61`, `internal/facts/schema.go`), and the built-in persona literal
(`internal/api/persona.go`).

The root cause of the *inability to adapt* was a single missing capability: a repo-wide grep for
`lang`, `locale`, `i18n`, `Accept-Language` returned only programming-language doc hits. The runtime
**never determined the user's language**, so no downstream layer could branch on it even where the
seam to do so already existed (`WithToxicKeywords`, `OutputCheck`, `PersonaProvider`). Identity
(`tenant_id`/`user_id`) propagates natively on `context.Context` from the Huma handler downward
(`internal/api/chat.go` `withIdentity` / `chatIdentity`); there was simply no `language` companion
on that same context.

## Decision

Add an **`internal/lang`** package that owns two things:

1. **Detection** — `lang.Detect(text string) Language` classifies the human language of a chat
   prompt and returns its lowercase ISO 639-1 code (a BCP-47 primary subtag: `"en"`, `"es"`,
   `"ja"`, …), or `lang.Undetermined` when it cannot confidently classify (empty/short/ambiguous
   input).
2. **The canonical ctx contract** — `lang.WithLanguage(ctx, Language)` (writer) and
   `lang.FromContext(ctx) (Language, bool)` (reader), keyed by a single unexported ctx key. This is
   the one contract every consumer reads; there is intentionally exactly one key for the whole
   runtime, mirroring how `internal/api` scopes tenant/user with their own private keys.

**Wire once, at the request boundary.** The chat handlers detect the language immediately after the
input guardrail redacts the prompt, and place it on ctx before hydration/model-selection run:

- Non-streaming: `internal/api/core.go` `ChatCore`
- Streaming: `internal/api/stream_core.go` `StreamCore`

Both detect on the **redacted** prompt (`forwarded`), never the raw one — consistent with the
constitutional gate that a secret in the prompt never flows beyond the guardrail. Redaction
replaces structural secrets (emails, keys) with placeholders and leaves the surrounding natural
language intact, so language detection is unaffected. The same `ctx` then flows into
`hydrateSystemPrompt` (where persona selection reads it) and the rest of the pipeline.

**Write-once / read-everywhere.** The chat handlers are the ONLY writer. Consumers are readers:

- **Personas (R5, today):** `internal/api/persona.go`'s language-aware `PersonaProvider` selection
  reads `lang.FromContext(ctx)` to pick a per-language persona literal, falling back to the default
  language when the seam reports not-set.
- **Guardrails / tokenizer / embedder (R3/R4, later):** the same `lang.FromContext(ctx)` read is
  how language-conditional injection/toxicity detection, the facts FTS tokenizer choice, and the
  embedder selection will branch — without re-detecting.

Using a different ctx key than the one defined in `internal/lang` would silently sever detection
from its consumers; the single unexported key is the mechanism that prevents that.

### Library choice — `github.com/pemistahl/lingua-go` (license-gate PASS)

| Candidate | License | Verdict |
|---|---|---|
| **`github.com/pemistahl/lingua-go` v1.4.0** | **Apache-2.0** (verified: GitHub license API `spdx_id: Apache-2.0`, `LICENSE` header "Apache License Version 2.0") | ✅ **Chosen.** Permissive, on the allowed list (`.claude/rules/cos/license-policy.md`), consistent with the 100% MIT/Apache stack (`.claude/rules/constitutional-gates.md`). n-gram (1–5) + rule engine, strong on *short* text (the chat case). |
| `github.com/abadojack/whatlanggo` | MIT | Acceptable fallback (lighter, trigram) but weaker on short chat snippets — not chosen. |

**Memory posture.** lingua-go with all 75 languages in high-accuracy mode holds ~1.8 GB of n-gram
models resident — unacceptable for an in-process, µs-cheap request-path seam. The seam therefore
builds the detector in **low-accuracy mode** (`WithLowAccuracyMode()`) and **restricts it to a
language subset** (`FromLanguages(...)`): the loaded models scale with the selected set, not the
full catalogue. The default subset covers the research §4 Phase-2 corpus — Latin ES/PT/FR/DE/IT,
CJK JA/ZH/KO, Cyrillic RU, Arabic AR, plus the English baseline — and is a package var so it can be
widened/narrowed without touching `Detect`. The detector is built **once** (`sync.Once`) and reused
across all request goroutines (it is read-only after `Build`, so concurrency-safe).

### Persistence — deferred, honestly

R1 also recommends *storing the detected language per-user*. This ADR keeps the language
**request-scoped** (on ctx only) and does **not** add a per-(tenant,user) column/key. Rationale:
the facts/working-memory tiers are already Unicode-clean and a clean per-user language write wants
its own schema decision (which tier, upsert semantics, how a language that drifts across sessions is
reconciled) that would over-build this seam. Per-user language persistence is a named follow-on, not
a silent gap.

## Consequences

**Positive**
- The missing keystone exists: the runtime now determines the user's language once per request and
  carries it on the same context that carries identity. R3/R4/R5 can become language-conditional
  instead of English-fixed by reading one ctx value.
- Zero behavior change when detection is `Undetermined` — `FromContext` reports not-set and every
  consumer keeps its existing English/default path. The seam is inert-safe.
- One dependency, permissively licensed, gate-passed; detection is in-process and cheap (µs–ms on
  chat-length text), no network, no subprocess.
- Single canonical contract: one ctx key, one writer (the chat handlers), many readers — no drift
  between how layers learn the language.

**Negative / residual**
- Low-accuracy mode trades some precision for the memory bound; very short or code-mixed prompts may
  return `Undetermined` or a plausible-but-wrong neighbor. Acceptable: consumers fall back safely on
  not-set, and the value is advisory (persona/tokenizer selection), not an authorization decision.
- The restricted language subset means a prompt in an unlisted language classifies as the nearest
  listed one or `Undetermined`. Widening the set costs memory; this is a tunable, documented var.
- Per-user persistence is deferred (see above).

## Alternatives considered

- **`whatlanggo` (MIT, trigram):** lighter, but the research flagged it as weaker on the short chat
  snippets that dominate our traffic. Kept as the documented fallback if lingua's footprint ever
  becomes a problem.
- **Detect at the LLM layer / ask the model:** defeats the purpose — the seam exists so guardrails
  and retrieval can adapt *before* and *around* the model call (and cheaply, without a token spend),
  not after.
- **A `language` request field on `ChatRequest`:** rejected as the source of truth — it would let a
  client mislabel its language and, like identity, language should be *derived* at the boundary, not
  trusted from the body. (A future explicit client hint could still override detection, but that is
  out of scope here.)

## References

- Research: `docs/research/i18n-multilingual-readiness-2026-07-17.md` (R1, and the §4 Phase-2 corpus)
- Identity propagation this seam mirrors: `internal/api/chat.go` (`withIdentity`, `chatIdentity`),
  `docs/architecture/multi-tenancy.md`
- Constitutional license gate: `.claude/rules/constitutional-gates.md`,
  `.claude/rules/cos/license-policy.md`
- lingua-go: https://github.com/pemistahl/lingua-go (Apache-2.0)
- Consumers: `internal/api/persona.go` (R5, personas), `internal/guardrails/*` and
  `internal/facts/schema.go` (R3/R4, follow-on)
