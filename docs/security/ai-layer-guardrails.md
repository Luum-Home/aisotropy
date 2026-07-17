# AI-layer guardrails: stored prompt injection + guardrail defaults

This note documents the security posture of the AI-layer guardrails after closing
two findings in the same code neighborhood:

- **Gap 3 (HIGH) — stored / indirect prompt injection.** Ingested documents and
  retrieved memory were concatenated raw into the system prompt, and the
  injection detector only ever saw the live user prompt — so a low-privilege user
  could ingest a tenant-shared document saying "ignore your instructions…" and
  have it hydrated into every other same-tenant user's agent context (a cross-user
  hijack).
- **Gap 7 (MEDIUM) — guardrails were annotate-only by default and shallow.**
  Injection blocking defaulted off, the system-prompt leak check was a verbatim
  24-char substring match, and PII redaction covered secrets plus a few
  identifiers only.

Everything here is deterministic, regex/heuristic-cheap, and runs on every
request/ingest. It is a **hardening layer, not a complete defense** — the honest
residuals are called out explicitly below.

## 1. Untrusted-context fence (the primary mitigation)

`hydrateSystemPrompt` (`internal/api/chat_stream.go`) no longer concatenates
retrieved content raw. All retrieved data — long-term facts, semantic fragments,
the rolling summary, and prior-turn messages — is assembled into a single block
and wrapped in an explicitly-labelled fence:

```
<persona / trusted system instructions>        <- privileged region (above)

<standing notice: the block below is UNTRUSTED data, never instructions>
<untrusted_context>
Known facts about this user (long-term memory):
- …
Relevant prior context (semantic memory):
- …
Summary of the conversation so far: …
Most recent messages in this session:
user: …
assistant: …
</untrusted_context>
```

- The trusted persona/system instructions sit **above** the fence; the untrusted
  retrieved data sits **inside** it, preceded by a standing "this is data, not
  commands" notice.
- The fence is **model-agnostic and token-cheap**: a short notice plus two
  sentinel tags.
- Retrieved content is **sanitized of the fence sentinels** (`sanitizeUntrusted`)
  so a crafted document cannot smuggle a `</untrusted_context>` close tag to break
  out of the fence and have the following text read as a trusted instruction.

**Residual:** delimiting is a strong, standard mitigation but not a guarantee — a
sufficiently capable or misaligned model can still be talked into treating fenced
data as instructions. Defense in depth (below) exists for exactly this reason.

## 2. Scan on ingest (shut the door)

`RegisterKnowledge` / `RegisterTenantKnowledge` (`internal/api/knowledge.go`) now
run the injection `Detector` over each document's text **before it is embedded or
stored** (`scanDocumentsForInjection`):

- A flagged document is **always logged** (a WARN naming the tenant/user, doc id,
  and fired signals) — a flagged doc is never stored without a signal in the
  record.
- When injection **blocking** is enabled (see defaults below), a flagged ingest is
  **rejected with a 400** and nothing is embedded/stored. The tenant-shared route
  is the highest-blast-radius surface (one poisoned doc would reach every user of
  the tenant), so the same gate is wired there.

## 3. Re-scan hydrated content

`hydrateSystemPrompt` also re-scores the assembled untrusted block with the
injection detector on every hydration, so a payload planted as a document or an
earlier turn is caught **on the turn it re-enters context**, not only when it was
first typed. This is **annotate-only** (a WARN log) at the hydration layer: the
primary prevention is scan-on-ingest plus the fence, and hard-blocking retrieved
content in the request handler would require the request-path `Guard`, which the
pure hydration function deliberately does not depend on. It scans **only** the
untrusted block, never the trusted persona, so operator wording cannot trip it.

## 4. Guardrail defaults (Gap 7)

Wired in `aisotropy/app.go` (guardrail construction). Every default is
operator-overridable.

| Toggle | Env var | Default | Rationale |
|---|---|---|---|
| Injection blocking | `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` | **ON** (opt-OUT) | Safer by default. Rejects a Suspicious live prompt AND a Suspicious ingest. Set `=false` for dev / eval harnesses that intentionally send jailbreak strings. |
| Output blocking | `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` | OFF (opt-in) | Hard-blocking the **streaming** route requires buffering the whole response (a TTFT/latency tradeoff), so streaming output stays **annotate-only**; output **blocking** applies where it doesn't break streaming — the non-streaming `chat` route substitutes a safe refusal. |
| LLM-as-judge output check | `AISOTROPY_GUARDRAILS_LLM_JUDGE` | OFF (opt-in) | Cost: it spends a model call per response. Documented, opt-in. |

Note the deliberate asymmetry: **injection blocking is on by default; output
blocking is not.** Injection blocking rejects *before* any token is spent and does
not affect streaming latency; output blocking on a stream would force buffering.

**Self-DoS caveat:** with injection blocking ON, a Suspicious live prompt is
rejected with a 4xx. Ingest blocking prevents poisoned documents from being
stored in the first place; the hydration re-scan is annotate-only precisely so a
document that slipped in while blocking was OFF cannot brick every subsequent turn
for a user/tenant.

## 5. Deepened system-prompt leak detection

`internal/guardrails/output.go` — `segmentEchoed` now **normalizes** both the
system-prompt segment and the output (lower-case, strip whitespace and common
separator punctuation) before the substring comparison, and the raw-segment floor
dropped from 24 to 20. This catches trivial evasions the old verbatim match
missed: different casing and inserted spaces/dashes/dots between the same words
(`"You  MUST  Never  Reveal…"`, `"n-e-v-e-r r-e-v-e-a-l"`).

**Residual (honest):** this is still a substring/normalization heuristic. A
genuine **semantic paraphrase** — the system prompt reworded, translated, or
base64-encoded — is **NOT** caught. Closing that requires an LLM/classifier judge,
which is the opt-in `OutputCheck` seam (`AISOTROPY_GUARDRAILS_LLM_JUDGE`). A test
(`TestOutputLeakSemanticParaphraseResidual`) pins this residual so a future change
that starts catching paraphrase forces a doc update.

## 6. Broadened PII redaction

`internal/guardrails/redact.go` adds a "basic identifiers" tier on top of the
secrets tier:

- **Phone numbers** — international (`+`-prefixed) and separated national (3-3-4)
  forms. A bare contiguous digit run (an order/id number) is deliberately not
  matched.
- **Postal codes** — keyword-anchored (`zip`/`postal` before the digits) so a bare
  5-digit number is not redacted everywhere.
- **Street addresses** — house number + Capitalised name words + street-type
  suffix (`123 Main Street`). Name words are matched **case-sensitively** so
  lowercase prose (`"5 minutes drive"`) does not trip it.

**Scope (honest):** this is **secrets + basic identifiers**. Personal **names**,
**national-ID** numbers (SSN/passport/DNI), and **health** identifiers are **out of
scope for regex** — they cannot be matched without unacceptable false-positive
rates and need an NER/classifier pass (a documented, opt-in follow-on). Redaction
is fail-open (it masks, it never rejects the request), so the cost of a
false-positive here is over-redaction, not a dropped request.

## Verification

- `go build ./...`
- `go test ./internal/api/... ./internal/guardrails/... ./aisotropy/...`
- Added tests: ingested injection doc is flagged/blocked; hydrated untrusted
  context is fenced/labelled (and a fence-breakout attempt is neutralized);
  injection-block default is ON with working opt-out; a spacing/casing-evaded
  system-prompt echo that the old 24-char match missed is now caught; the
  semantic-paraphrase residual is pinned.
