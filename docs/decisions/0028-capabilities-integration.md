---
type: ADR
title: "ADR-0028 — Wiring the book-gap capabilities: guardrails always-on/block-opt-in, gateway as the model-selection seam, response cache deliberately unwired"
description: ADR-0027 shipped internal/guardrails, internal/gateway, internal/responsecache as standalone, tested, CODE-ONLY packages and named their integration as a follow-on. This ADR executes that follow-on and records the operator wiring decisions. Guardrails run on every chat route — input redaction + injection/output detection ALWAYS, blocking OPT-IN behind AISOTROPY_GUARDRAILS_BLOCK_INJECTION / AISOTROPY_GUARDRAILS_BLOCK_OUTPUT (default off, annotate + log). The gateway is wired as the single model-selection seam — its cost-budget policy is live (AISOTROPY_COST_BUDGET_USD + a cheap client), its request-shape rules are ready-but-inert because ChatInput carries no image/override signal yet, it is explicitly NOT the refuted Bet-4 difficulty router, and the streaming quota-pressure degrade stays as an override on top of the gateway pick. The response cache is DELIBERATELY NOT wired into the stateful, per-user-hydrated chat path (a personalized-response cache is a cross-tenant/staleness footgun) and remains a standalone primitive for stateless flows. Guardrails + gateway move from CODE-ONLY to REAL; the cache stays CODE-ONLY. Follow-ons: streaming hard output-blocking (needs buffering), request-shape signals in ChatInput, and attaching internal/eval's LLMJudge to the guardrails OutputCheck seam.
tags: [decisions, adr, guardrails, gateway, cache, integration, security, cost, request-lifecycle, wiring]
timestamp: 2026-07-15T00:00:00Z
---

# ADR-0028 — Wiring the book-gap capabilities: guardrails, model gateway, and the deliberately-unwired response cache

## Status

✅ **Accepted — 2026-07-15.** This ADR executes follow-on #1 of
[ADR-0027](0027-book-gap-capabilities.md) ("wire the capabilities into the live request path"). The
three packages ADR-0027 shipped as **standalone, tested, CODE-ONLY** — `internal/guardrails`,
`internal/gateway`, `internal/responsecache` — are addressed here as a single, reviewable wiring
step:

- **`internal/guardrails` → wired on all three chat routes → REAL.**
- **`internal/gateway` → wired as the model-selection seam on the two non-agent chat routes → REAL.**
- **`internal/responsecache` → DELIBERATELY NOT wired into the stateful chat path → stays CODE-ONLY**
  (a standalone primitive for stateless flows, per the reasoning below).

The wiring lives in `internal/api/guard.go` (the `Guard` bundle + `selectClient` seam), threaded
through `internal/api/chat.go`, `chat_stream.go`, and `chat_agent.go`, and constructed in
`aisotropy/app.go` (`App.Run`). It is verified against the code at HEAD; see the References for the
exact call sites. This ADR **supersedes ADR-0027's CODE-ONLY classification for guardrails and the
gateway** — those two now run on every live chat request — while **affirming** it for the cache.

## Context

ADR-0027 answered "should we close the *AI Engineering* (Huyen) stage-2/3/4 gaps" with three real,
green, dependency-free packages, but deliberately deferred the wiring decision so it would be made
once, on purpose, rather than smuggled into already-dense handlers. That decision is now due, and it
is not one decision but several, each with an operator-visible default that this ADR fixes:

1. **Guardrails — block, or annotate?** A safety layer that hard-blocks by default changes the
   failure mode of every request (a false-positive injection heuristic returns a 400; a
   false-positive toxicity heuristic replaces a legitimate answer with a refusal). A safety layer
   that only annotates is safe to turn on everywhere but does nothing on its own. The package is
   built **advisory** on purpose (every detector returns a `Verdict`, the *caller* decides), so this
   is genuinely the integrator's call.
2. **Gateway — which policies fire?** The gateway's reference policies span capability rules
   (`ImageRule`), explicit overrides (`OverridePolicy`/`UISelector`), and money-budget shaping
   (`CostAwarePolicy`). But `ChatInput` (the frozen struct-as-contract, ADR-0002) carries **no**
   image part and **no** UI selector — so the capability/override rules have no signal to route on.
   Only the cost-budget rule has a live input (cumulative spend from the shared cost meter). Wiring
   the seam means deciding which rules are active vs. present-but-inert, and how the seam relates to
   the pre-existing streaming quota-pressure degrade (`llm.DegradeAt`, ADR-0010/Phase 7).
3. **Response cache — is a personalized runtime even cacheable?** `internal/responsecache` is
   isolation-correct by construction (tenant\0user\0 mixed into the exact key; per-`(tenant,user)`
   partitioned semantic search). But the chat path's response is a function of **far more than the
   prompt**: every turn is hydrated from that user's working memory (summary + recent), their facts
   (FTS5), and semantic RAG (ADR-0017/0018/0019). Two identical prompts from the same user at
   different points in their history legitimately produce different answers. A prompt-keyed cache over
   a memory-hydrated response is a **staleness/correctness footgun**, not a cost win — and that is
   before considering that the cache key deliberately excludes the hydrated context it would have to
   depend on.

The open question this ADR answers is **how each seam attaches and what its default is** — not
whether the capabilities exist (ADR-0027 already shipped them).

## Goals

- Move guardrails and the gateway from CODE-ONLY to **REAL** — running on every live chat request —
  with **operator-visible, default-safe** postures.
- Keep the wiring **nil-safe and behavior-preserving**: a handler wired without a `Guard`/`Gateway`
  (as every existing unit test is) behaves exactly as before.
- Change **zero** OpenAPI struct contract (ADR-0002): no new/changed JSON tag or field on
  `ChatInput`/`ChatRequest`. The seams live in middleware-adjacent handler code and builder options,
  not the request body.
- Be **honest about what does and does not run**: annotate-by-default (not block), streaming
  output-guardrail is annotate-only, capability/override gateway rules are inert for lack of signal,
  and the cache is not on the request path at all.

## Non-goals

- **NOT** wiring `internal/responsecache` into the stateful chat path. Its non-wiring is a *decision*
  recorded here, not an omission (see Decision §3).
- **NOT** reviving the refuted Bet-4 difficulty router (ADR-0021). The gateway routes on transparent,
  data-driven signals; there is deliberately no prompt-length/compressibility rule.
- **NOT** an LLM-as-judge inside the guardrail layer. The heavy, model-backed output grading remains
  an extension seam (`guardrails.OutputCheck`); attaching `internal/eval`'s `LLMJudge` to it is a
  named follow-on, not part of this cut.
- **NOT** adding image/UI-override signals to `ChatInput`. Activating the gateway's request-shape
  rules requires that contract change and is a named follow-on.
- **NOT** hard output-blocking on the streaming route. A token-by-token stream cannot be un-sent;
  enforcing a refusal would require buffering the whole response before the first byte (defeating
  streaming). This is annotate-only by construction and is a named follow-on.

## Options considered

### Guardrail posture

- **Block by default.** Maximal safety, but a heuristic false positive becomes a user-facing 400 /
  forced refusal on day one. Rejected as the default: the detectors are heuristic and advisory by
  design, and a silent behavioral break on every deployment is the wrong default for a
  reconstruction-phase runtime.
- **Annotate + log always, block only when explicitly enabled (CHOSEN).** Redaction and
  injection/output detection run on **every** request (secrets are scrubbed before they reach the
  model or any memory tier; suspicious verdicts are logged with signal names + counts, never raw
  values), but a request/response is only **rejected/replaced** when the operator flips an explicit
  toggle. This makes "guardrails on" the safe default and "guardrails enforce" a conscious,
  greppable opt-in — consistent with the anti-magic principle (ADR-0002: no silent behavior) and the
  default-safe posture of ADR-0024/0025.

### Gateway scope

- **Wire only the rules that have a live signal (CHOSEN).** Register the seam, enable the
  cost-budget policy (its input — cumulative spend — exists), and leave the capability/override rules
  as present-but-inert reference policies that will fire the moment `ChatInput` grows the
  corresponding signal. Honest: the seam is real, its full policy surface is not yet reachable.
- **Wire the full policy surface now.** Rejected: it would require inventing image/override fields on
  the frozen contract under wiring pressure — exactly the "decide under handler pressure" failure
  ADR-0027 avoided.

### Response cache placement

- **Wire it into the chat path (exact + semantic, before the LLM call).** Rejected: the chat
  response depends on per-user hydrated memory the cache key excludes, so a hit would serve a stale
  or context-wrong answer. The cache's isolation is correct; its *applicability* to a
  memory-hydrated, personalized response is not.
- **Leave it unwired; keep it a standalone primitive for stateless flows (CHOSEN).** The package
  stays green and available for a future stateless surface (e.g. a non-hydrated completion endpoint),
  where prompt-keying is sound. Recorded as a deliberate decision, not a gap.

## Decision

### 1. `internal/guardrails` — wired on all three chat routes; redact + detect ALWAYS, block OPT-IN

A single process-wide `guardrails.Default()` aggregator is bundled into an `api.Guard`
(`internal/api/guard.go`) with two opt-in blocking toggles and threaded into `Register`,
`RegisterStream`, and `RegisterAgent`. `App.Run` constructs it once (`aisotropy/app.go`):

```go
guard := &api.Guard{
    G:              guardrails.Default(),
    BlockInjection: guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_INJECTION"),
    BlockOutput:    guardrailsBlockFromEnv("AISOTROPY_GUARDRAILS_BLOCK_OUTPUT"),
}
```

Both toggles default to **off** (unset / unparsable → `false`, per `strconv.ParseBool`).

- **Input (all three routes), pre-LLM, after the quota gate.** `guard.checkInput` runs
  `CheckInput(prompt)`. The returned **redacted** text (`forwarded`) — never the raw prompt — is what
  hydrates the system prompt, persists to working memory (and the consolidation outbox / Bet-6
  held-out sample), and reaches the model, so a secret in the prompt never leaks into the model or
  any memory tier. Redaction is fail-open on the text (the message is never dropped). A `Suspicious`
  injection verdict is **annotated + logged** by default and only **rejected (400)** when
  `BlockInjection` is enabled.
- **Output (non-streaming `chat` only), post-LLM, pre-persist/return.** `guard.checkOutput` runs
  `CheckOutput(ctx, input, output, system)`. When the response trips a toxicity/leak check **and**
  `BlockOutput` is enabled, the flagged text is replaced with a safe refusal — and the **refusal**
  (not the flagged text) is what both persists to working memory and returns to the client. Default
  posture: annotate + log, reply unchanged.
- **Output (streaming `chat/stream`), post-stream, annotate-only.** `guard.annotateOutput` inspects
  the **assembled** final text after a clean `done` and logs a tripped verdict, but never blocks: the
  tokens have already been sent and cannot be un-sent. This helper deliberately does **not** consult
  `BlockOutput`. Hard output-blocking here is a named follow-on (needs response buffering).
- **Output on `chat/agent`: none.** The agent response is a structured list of tool decisions, not
  free-form prose, so only the input guardrail applies there.

Logging is types + counts (redaction) and signal names (injection/output) only — never the raw
values or the response body.

### 2. `internal/gateway` — wired as the single model-selection seam (cost policy live; shape rules inert)

`App.Run` builds one `*gateway.Gateway` via `buildModelGateway(streamClient, cheapStreamClient,
costBudgetFromEnv())` and passes it to `Register` (non-streaming) and `RegisterStream`. `guard.go`'s
`selectClient(ctx, gw, fallback, meter, tenant, user, prompt)` runs the policy per turn and resolves
the decision to a concrete `llm.Client`, failing safe to `fallback` on any routing miss (a routing
decision never drops a request).

- **Cost-budget policy is LIVE — but only when configured.** `CostAwarePolicy{BudgetUSD, Cheap}` is
  registered **only when both** a cheap client exists **and** `AISOTROPY_COST_BUDGET_USD > 0`;
  otherwise the gateway is a clean pass-through to `primary`. It shapes an over-budget tenant (by
  cumulative spend read from the shared cost meter) to the cheap target — **request-shaping by
  money**, complementary to the quota-pressure degrade below.
- **Request-shape rules are READY-BUT-INERT.** `ImageRule` / `OverridePolicy` (`HasImage`,
  `UISelector`) are reference policies that never fire because `ChatInput` carries no image part or UI
  selector yet. Activating them requires a contract change (named follow-on). There is deliberately
  **no prompt-length/difficulty rule** — Bet-4 (compression-difficulty routing) was refuted
  (ADR-0021); `PromptTokensEstimate` is carried as honest routing metadata, not routed on.
- **The streaming quota-pressure degrade stays as an OVERRIDE.** On `chat/stream` only, after the
  gateway pick, `llm.DegradeAt(pressure, 0.95, active, cheap)` may still route a near-quota tenant to
  the cheap model — the gateway **complements**, never replaces, the Phase-7 degrade.
- **`chat/agent` is NOT routed through the gateway.** `RegisterAgent` receives no gateway and uses the
  breaker-wrapped client directly (consistent with the agent route also not metering cost or
  rate-limiting — see the runtime-as-built ledger).
- **Side effect worth naming:** the gateway's `primary` target is the OTel-instrumented
  `streamClient`, so routing the non-streaming reflection path through the gateway additionally wraps
  that path in OTel (a no-op unless a tracer is configured); its `fallback` remains the
  non-OTel `protectedClient`.

### 3. `internal/responsecache` — DELIBERATELY NOT wired into the stateful chat path

No chat handler constructs or consults the response cache. This is a **decision**, not an omission:
the chat response is a function of the caller's hydrated working/facts/semantic memory
(ADR-0017/0018/0019), which the prompt-keyed cache key deliberately excludes — so a hit would serve a
context-wrong or stale answer. The cache's by-construction `(tenant,user)` isolation is real and
tested (ADR-0027), but isolation-correctness is not applicability. The package remains a **standalone
primitive for stateless flows** (a future non-hydrated completion surface where prompt-keying is
sound), green and available, but off the personalized request path.

## Consequences

- **Guardrails + gateway are REAL.** Every live chat request now redacts secrets out of the prompt
  before the model/memory ever see it, scores input for injection and (non-streaming) output for
  toxicity/leak, and selects its model through one routing seam. This **supersedes ADR-0027's
  CODE-ONLY classification** for these two — the [reality ledger](../architecture/reality-ledger.md)
  should be updated to REAL (guardrails, gateway) and left CODE-ONLY (response cache).
- **Default posture is annotate + log, not block.** Turning guardrails "on" is the default; turning
  them "enforcing" is an explicit `AISOTROPY_GUARDRAILS_BLOCK_INJECTION` /
  `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` opt-in. Operators who want fail-closed enforcement must set the
  toggles; the docs must not imply blocking is on by default.
- **Streaming output enforcement is asymmetric with non-streaming — on purpose.** Non-streaming
  `chat` can replace a flagged reply with a refusal (`BlockOutput`); streaming `chat/stream` can only
  annotate. This is a documented limitation, not a bug.
- **The gateway's advertised policy surface exceeds what runs.** Only the cost-budget rule can fire
  today; capability/override rules are inert for lack of a `ChatInput` signal. A reader must not
  infer image/override routing is live.
- **No new dependency, no new license surface, no contract change.** All three packages are Go stdlib
  + already-stable internals; `ChatInput`/`ChatRequest` and the generated OpenAPI 3.1 schema are
  untouched (ADR-0002 preserved).
- **The cache remains a claim about a tested library, not runtime behavior** — exactly as ADR-0027
  filed it, now with an explicit reason it stays that way for the chat path.

## Follow-ons (named, not yet done)

1. **Streaming hard output-blocking.** Buffer the assembled response before the first byte so a
   flagged streamed reply can be replaced with a refusal (making `BlockOutput` meaningful on
   `chat/stream`), accepting the latency/streaming trade-off it forces. Until then `chat/stream` is
   annotate-only.
2. **Request-shape signals in `ChatInput`.** Add an image part and/or a UI-selector field (a
   deliberate, versioned struct-contract change, ADR-0002) so the gateway's `ImageRule` /
   `OverridePolicy` route on real input instead of sitting inert.
3. **Attach `internal/eval`'s `LLMJudge` to the guardrails `OutputCheck` seam.** Implement
   `guardrails.OutputCheck` (`OutputInspector.WithChecks`) backed by `internal/eval`'s model-backed
   `LLMJudge` for real toxicity/faithfulness grading, without `internal/guardrails` importing the
   evaluation tier — closing ADR-0027 follow-on #2.

## References

- [ADR-0027](0027-book-gap-capabilities.md) (the standalone CODE-ONLY packages this ADR wires; follow-on #1 executed here, #2 restated above)
- [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md) (Bet-4 difficulty routing refuted — why the gateway has no length/difficulty rule)
- [ADR-0002](0002-anti-framework-magic-struct-as-contract.md) (struct-as-contract kept unchanged; no silent behavior) · [ADR-0009](0009-provider-agnostic-llm-layer.md) (the provider-agnostic seam the gateway sits over) · [ADR-0010](0010-gollem-adoption-scope-and-observability.md) (the streaming quota-pressure degrade the gateway complements)
- [ADR-0024](0024-authenticated-identity-seam.md) / [ADR-0025](0025-security-hardening-authn-ops.md) (the default-safe posture this wiring mirrors) · [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) / [ADR-0011](0011-tenant-scoped-statement-builder.md) (the isolation floor the — unwired — cache would extend)
- Wiring: [`internal/api/guard.go`](../../internal/api/guard.go) (`Guard`, `checkInput`/`checkOutput`/`annotateOutput`, `selectClient`), [`internal/api/chat.go`](../../internal/api/chat.go), [`chat_stream.go`](../../internal/api/chat_stream.go), [`chat_agent.go`](../../internal/api/chat_agent.go); construction in [`aisotropy/app.go`](../../aisotropy/app.go) (`guard`, `buildModelGateway`, `guardrailsBlockFromEnv`, `costBudgetFromEnv`)
- Packages: [`internal/guardrails`](../../internal/guardrails), [`internal/gateway`](../../internal/gateway), [`internal/responsecache`](../../internal/responsecache); the output-judge seam [`internal/eval`](../../internal/eval) (`LLMJudge`)
- [runtime as built](../architecture/runtime-as-built.md) (the request-lifecycle ledger updated alongside this ADR) · [`docs/architecture/reality-ledger.md`](../architecture/reality-ledger.md) (REAL/CODE-ONLY classification to update)
