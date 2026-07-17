---
type: ADR
title: "ADR-0053 — Per-(tenant,user) language persistence: remember the detected language across requests"
description: ADR-0050 added the language-detection seam but kept the language request-scoped (on ctx only) and explicitly deferred per-user persistence. This ADR delivers that follow-on. Decision — persist the detected language per (tenant,user) subject in BadgerDB working memory under a subject-scoped key (memory.subjectLangKey → "tenant:{tenant}:user:{user}:lang"), and reconcile it with the fresh per-request detection via a single precedence rule (lang.Resolve): a confident detection updates the remembered language (writing only on a genuine change); an Undetermined detection (short/code-mixed follow-up) inherits the remembered language instead of resetting downstream consumers to their English/default path. The value is advisory (conditions persona/guardrail prose, never authorization), isolated by construction (keyed strictly by tenant+user with the same reserved-character validation every working-memory op uses, so a blank identity is rejected rather than widened to a wildcard), erased with the subject (GDPR Art.17), and cheap (one keyed read plus, only on change, one write). No new dependency.
tags: [decisions, adr, i18n, multilingual, language-detection, persistence, working-memory, multi-tenancy, isolation, context-propagation]
timestamp: 2026-07-17T00:00:00Z
---

# ADR-0053 — Per-(tenant,user) language persistence: remember the detected language across requests

## Status

✅ **Accepted — 2026-07-17.** Completes the persistence half of recommendation **R1** of the Phase-0
i18n readiness research (`docs/research/i18n-multilingual-readiness-2026-07-17.md`), which
ADR-0050 delivered only in part. ADR-0050 added the detection seam (detect once, carry on ctx) and
**explicitly deferred** "store the detected language per-user" as a named follow-on (ADR-0050 →
*"Persistence — deferred, honestly"*). This ADR is that follow-on.

## Context

ADR-0050 detects the prompt language once at the request boundary and carries it on
`context.Context` via `lang.WithLanguage` / `lang.FromContext`. But the value was **request-scoped**:
it lived only for the lifetime of one request's ctx and was recomputed from scratch on the next
request, with nothing remembered in between.

That leaves two gaps the research called out:

1. **No cross-request memory.** A user who wrote three Spanish turns is re-detected every turn; there
   is no notion of "this subject's language is Spanish."
2. **Low-signal turns fall back to the default.** ADR-0050's own residual note: *"very short or
   code-mixed prompts may return Undetermined."* When that happens mid-conversation, every consumer
   (persona selection today) drops back to its English/default path — even though the user's language
   was unambiguous one turn ago. The information to do better existed; it just was not retained.

The deferral rationale in ADR-0050 was that a per-user language write "wants its own schema decision
(which tier, upsert semantics, how a language that drifts across sessions is reconciled)." This ADR
makes exactly those decisions.

## Decision

Persist the detected language **per (tenant,user) subject** in the BadgerDB working-memory tier, and
layer a single precedence rule over the existing per-request detection.

### Where it is stored — subject-scoped working-memory key

The value lives in `internal/memory` under a **subject-scoped** key built off the existing
`SubjectPrefix`:

```
memory.subjectLangKey(tenant, user)  →  "tenant:{tenant}:user:{user}:lang"
```

Two new methods on `WorkingMemory` are the only surface:

- `GetSubjectLanguage(ctx, tenant, user) (code string, found bool, err error)`
- `PutSubjectLanguage(ctx, tenant, user, code string) error`

Rationale for the tier and scope:

- **Working memory, not facts/semantic.** The language is a tiny, per-subject scalar read on the hot
  request path; BadgerDB (in-process, µs) is the right temperature. It is not a searchable fact and
  not a vector, so it belongs in neither the facts (rqlite/FTS5) nor the semantic (Qdrant) tier.
- **Subject scope (tenant,user), not session.** Language is a property of the *person*, not of one
  chat session, so it is keyed by `(tenant,user)` and shared across all of that subject's sessions —
  the same subject scope `SubjectPrefix` / `EraseSubject` already use. A brand-new session therefore
  inherits the subject's known language immediately.

### How detection and memory reconcile — one precedence rule

The reconciliation lives in exactly one place, `lang.Resolve(detected, stored) (effective, persist)`,
so the streaming and non-streaming paths behave identically. The rule:

| Fresh detection | Stored value | Effective language | Persist? |
|---|---|---|---|
| Confident (e.g. `es`) | anything | the detection | **only if it changed** |
| Undetermined | present (e.g. `es`) | **the stored value (inherit)** | no |
| Undetermined | absent | Undetermined | no |

In words: **a confident detection wins and teaches the subject** (writing only on a genuine change,
so a run of same-language turns costs no writes); **an Undetermined follow-up inherits** the
remembered language instead of resetting to the default; and a low-signal turn **never overwrites** a
previously learned language with `und`.

### How it is wired — the same boundary as detection

Both chat handlers call one shared helper, `resolveSubjectLanguage` (`internal/api/core.go`), at the
exact point ADR-0050 wired detection — right after the input guardrail redacts the prompt:

- Non-streaming: `internal/api/core.go` `ChatCore`
- Streaming: `internal/api/stream_core.go` `StreamCore`

The helper reads the stored language, runs `lang.Resolve`, writes back on a confident change, and
puts the **effective** language on ctx via `lang.WithLanguage`. Every downstream consumer keeps
reading `lang.FromContext` exactly as before — they are unaware persistence exists; they simply now
see the remembered language on turns whose own detection was inconclusive.

### Isolation, cost, erasure — by construction

- **Isolation.** `subjectLangKey` validates both identity components with the same
  reserved-character rule (`ValidateIdentityComponent`) every other working-memory operation uses. A
  blank or malformed tenant/user is **rejected at the key layer**, never widened into a wildcard key,
  so one subject's language is structurally unreachable from another user or another tenant. The
  `"lang"` leaf cannot collide with a session's `"...:session:{session}:..."` namespace because the
  reserved `":"` can never appear inside an identity component.
- **Advisory only.** The remembered language conditions downstream *prose* (persona selection today;
  language-conditional guardrails/tokenizer/embedder later). It is **never** an authorization input,
  so a wrong or stale value degrades output quality at worst — it can never grant or deny access.
- **Cheap.** One subject-scoped BadgerDB read per turn, plus — only on a genuine language change —
  one write. Same-language turns and low-signal turns write nothing.
- **Non-fatal.** A read/write error is logged and the turn proceeds on the request-scoped detection.
  A memory hiccup must never fail a chat over an advisory hint.
- **Erased with the subject.** Because the key lives *under* `SubjectPrefix`, `EraseSubject` (GDPR
  Art.17, ADR-0046) removes the remembered language along with the rest of the subject's data — no
  separate erasure path to keep in lockstep.

## Consequences

**Positive**
- The runtime now *remembers* a subject's language across requests and sessions. Short or code-mixed
  follow-ups keep the conversation in the user's language instead of snapping back to English.
- One precedence rule (`lang.Resolve`), shared by both chat paths, so streaming and non-streaming
  remember identically — no drift.
- No new dependency, no new tier, no schema migration: it reuses the working-memory key layout,
  validation, and subject-erasure primitive already in place.
- Isolation and erasure are inherited from the existing subject scope, not re-implemented.

**Negative / residual**
- A subject who genuinely switches languages carries the previous language for exactly one turn *if*
  that switching turn is itself low-signal (Undetermined) — the first *confident* turn in the new
  language updates it. Acceptable: the value is advisory and self-correcting on the next confident
  detection.
- The remembered language is last-writer-wins per subject; there is no per-session override. A future
  explicit client language hint (already noted as out of scope in ADR-0050) would layer above this.
- Working memory is the store, so with the bare in-process (in-memory) BadgerDB the remembered
  language does not survive a process restart; with `AISOTROPY_DATA_DIR` (on-disk) it does. This
  matches the durability posture of every other working-memory value and is intentional.

## Alternatives considered

- **Keep it request-scoped (status quo of ADR-0050):** rejected — that is exactly the gap this ADR
  closes; low-signal follow-ups would keep resetting to the default.
- **Store per session instead of per subject:** rejected — language is a property of the person, and
  a per-session key would make each new session re-learn the language from scratch, defeating the
  point.
- **Persist in the facts (rqlite) tier:** rejected — the language is a hot-path scalar, not a
  searchable fact; the facts tier is the wrong access temperature and would add a networked read to
  every turn.
- **Overwrite on every detection (including Undetermined):** rejected — it would let a single
  short/ambiguous turn erase a confidently-learned language. The precedence rule deliberately makes
  Undetermined a read-only fallback, never a write.
- **Trust a client-supplied language field:** rejected for the same reason ADR-0050 rejected it —
  language is *derived* at the boundary, not trusted from the body.

## References

- Prior ADR this completes: `docs/decisions/0050-language-detection-seam.md` (R1 detection seam;
  persistence deferred)
- Research: `docs/research/i18n-multilingual-readiness-2026-07-17.md` (R1)
- Subject scope & erasure reused: `internal/memory/key.go` (`SubjectPrefix`,
  `ValidateIdentityComponent`), `internal/memory/working_memory.go` (`EraseSubject`),
  `docs/decisions/0046-subject-level-erasure-gdpr-art17.md`
- Wiring points: `internal/api/core.go` (`ChatCore`, `resolveSubjectLanguage`),
  `internal/api/stream_core.go` (`StreamCore`)
- Precedence rule: `internal/lang/resolve.go` (`lang.Resolve`)
- Multi-tenant isolation gate: `.claude/rules/constitutional-gates.md`,
  `docs/architecture/multi-tenancy.md`
