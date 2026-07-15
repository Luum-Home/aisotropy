---
type: ADR
title: "ADR-0027 — Book-gap capability packages: guardrails, model gateway, response cache (standalone, tested, not yet wired)"
description: The "AI Engineering" (Huyen) readiness review scored Aisotropy against the canonical 5-stage LLM-application stack and found three gaps — stage-2 guardrails, a stage-3 model gateway, and a stage-4 response cache. Decision — ship each as a standalone, dependency-free capability package (internal/guardrails, internal/gateway, internal/responsecache) with its own tests, rather than inline them into the handlers. internal/guardrails does input PII/secret redaction + injection/prompt-leak detection + output toxicity/leak checks; internal/gateway is a rules/cost/capability model router (explicitly NOT the refuted Bet-4 difficulty predictor); internal/responsecache is an exact + semantic response cache, per-(tenant,user) isolated by construction with cross-tenant isolation tests. Consequence — the capabilities exist and are green standalone, but they are NOT wired into the live request path; they are REAL-but-unwired (the reality-ledger CODE-ONLY class). Orchestrator integration is the named follow-on.
tags: [decisions, adr, guardrails, gateway, cache, ai-engineering, security, cost, capabilities, code-only]
timestamp: 2026-07-15T00:00:00Z
---

# ADR-0027 — Book-gap capability packages: guardrails, model gateway, response cache (standalone, tested, not yet wired)

## Status

✅ **Accepted — 2026-07-15.** Three new packages — `internal/guardrails`, `internal/gateway`,
`internal/responsecache` — are implemented, reviewed, and pass their own test suites
(`go test ./internal/guardrails/... ./internal/gateway/... ./internal/responsecache/...` is green).
They are **standalone and NOT wired into the request path**: no `internal/api/chat*.go` handler
constructs or consults any of them today. In reality-ledger terms
([`docs/architecture/reality-ledger.md`](../architecture/reality-ledger.md)) these are
**CODE-ONLY** — "implemented and reviewed, but dormant by default" — with the sharper qualifier that
they are dormant because they are *unwired*, not because they hide behind an env var. Wiring them into
the live lifecycle is the named follow-on below; until then this ADR must not be read as a claim that
guardrails, gateway routing, or response caching run on any live request.

## Context

The product-evaluation review measured Aisotropy against the canonical production LLM-application
architecture from Chip Huyen's *AI Engineering* — the five-stage hardening pipeline the book lays out,
where a bare model call is progressively wrapped: **stage 1** the raw model call, **stage 2**
guardrails (in/out safety), **stage 3** a model gateway/router in front of the providers, **stage 4** a
cache to avoid re-answering equivalent prompts, and **stage 5** orchestration/observability tying the
rest together. The same 5-stage framing anchors ADR-0026 (the evaluation harness) and
[`docs/architecture/product-evaluation.md`](../architecture/product-evaluation.md).

Against that canon the runtime, as built through the authenticated-identity seam (ADR-0024), had
stage 1 (the `internal/llm` client + reflection loop) and much of stage 5 (SSE, cost meter, circuit
breaker, `/health`) but **three concrete gaps**:

- **Stage 2 — guardrails.** Nothing scrubbed PII/secrets out of user input before it reached the
  model, nothing scored input for prompt-injection / prompt-leak, and nothing inspected model output
  for toxicity or for leaking a secret or the system prompt back to the caller.
- **Stage 3 — model gateway.** Every caller was wired to a single `llm.Client`. There was no seam that
  could decide *which* model handles a request (cheap vs. premium, budget-aware, capability-aware,
  explicit UI override) by policy. Note the prior, refuted attempt at this: **Bet 4**
  ("compression-difficulty routing") tried to *predict* prompt difficulty from compressibility and
  route hard prompts to a premium model; a trivial `len(prompt)` baseline beat it (ADR-0021), so the
  learned predictor was abandoned. The gap was never "we lack a difficulty model" — it was "we lack a
  routing seam at all."
- **Stage 4 — response cache.** No lever checked, before an expensive LLM call, whether an equivalent
  prompt had already been answered. For a multi-tenant runtime this gap has a **correctness** edge, not
  just a cost one: a naive cache that isn't tenant-scoped by construction is a cross-tenant data-leak
  hazard (one tenant reading another's model output).

The open question this ADR answers is **not** "should we close these gaps" (the review already said
yes) but **how**: inline the logic into the chat handlers, or ship each capability as its own
standalone, testable package first and wire later.

## Goals

- Close the three named stage gaps with **real, tested code**, not design notes.
- Keep each capability **dependency-free** (Go stdlib + already-stable internal packages only) so it
  satisfies the 100% MIT/Apache constitutional gate with no new supply-chain surface.
- Make each capability **deterministic and standalone-testable** — no hidden I/O, no LLM call baked
  into the safety/routing/cache logic — so its behavior is pinned by table-driven tests independent of
  the request path.
- For the cache specifically: make **per-(tenant, user) isolation a by-construction property**, tested
  adversarially, consistent with the isolation floor the rest of the runtime enforces (ADR-0007,
  ADR-0011).
- Be **honest about wiring**: ship the packages as CODE-ONLY and record integration as an explicit
  follow-on, rather than implying the capabilities are live.

## Non-goals

- **NOT** wiring these into `internal/api/chat*.go` in this cut. Integration into the live lifecycle is
  the named follow-on; this ADR ships the packages, not their activation.
- **NOT** a learned/ML router. The gateway is deliberately rules/cost/capability based — see the Bet-4
  refutation above. Reviving a difficulty predictor is explicitly rejected, not deferred.
- **NOT** an LLM-as-judge inside the guardrail layer. The heavy, model-backed toxicity/faithfulness
  grading is left as an extension seam (`guardrails.OutputCheck`) so the package never imports the
  evaluation tier; attaching a judge is a follow-on.
- **NOT** a replacement for provider-level prompt caching. `internal/responsecache` operates *outside*
  the model call (a hit sends no request at all); provider prefix-caching operates *inside* one call.
  They stack; adopting a provider's caching feature is a separate follow-on.

## Options considered

### Option A — Inline the logic into the chat handlers

Add redaction/detection/routing/cache calls directly inside `internal/api/chat.go`,
`chat_stream.go`, `chat_agent.go`.

- **+** One less indirection; the capability is "live" the moment it merges.
- **−** Couples safety/routing/cache logic to the HTTP layer, so none of it is testable without the
  full handler harness; three concerns land in already-dense handlers at once; and it forces the
  wiring decision (policy thresholds, block-vs-degrade, cache TTLs) to be made *now*, under handler
  pressure, instead of deliberately. Rejected.

### Option B — Standalone capability packages, wired later (CHOSEN)

Ship each capability as its own `internal/<pkg>` with a clean seam (`Guardrails.CheckInput/CheckOutput`,
`Gateway.Route`, `ResponseCache.Get/Set`), fully unit-tested in isolation, and integrate into the
handlers as a separate, reviewable step.

- **+** Each capability is deterministic and table-tested on its own; the isolation guarantee of the
  cache is provable by a focused adversarial test rather than an end-to-end one; the wiring decision
  (where in the lifecycle, what policy, block vs. advisory) is deferred to an explicit integration ADR
  instead of being smuggled in. Matches the runtime's existing "seam, then adopt" pattern
  (ADR-0009 LLM seam, ADR-0024 auth seam).
- **−** The capabilities are **not live** until wired — a reader could mistake "package exists" for
  "feature runs." Mitigated by classifying them CODE-ONLY here and in the reality ledger, and by naming
  the wiring follow-on explicitly. Accepted: honest-but-unwired beats live-but-untested-in-a-handler.

## Decision

**Adopt Option B.** Ship three standalone, dependency-free, tested capability packages and defer their
wiring to a named follow-on.

### `internal/guardrails` — stage 2 (in/out safety)

Deterministic, stdlib-only, no network/LLM call. Surface:

- **Input redaction** (`redact.go`): `Redactor.Redact(text) (string, RedactionReport)` scrubs emails,
  Luhn-validated credit cards, provider API keys (`sk-…`, `ghp_…`, `AKIA…`, `xox[baprs]-…`), `Bearer`
  tokens, PEM private-key blocks, internal `AISOTROPY_*` secret env names, and range-validated IPv4 —
  plus integrator `CustomPattern`s. Redaction is **fail-open on the text** (the message is never
  dropped) and the `RedactionReport` records *types and counts only, never the raw values*, so it is
  safe to log.
- **Injection / prompt-leak detection** (`injection.go`): `Detector.Inspect(text) Verdict` sums
  weighted, case-insensitive heuristics (ignore-previous-instructions, reveal-system-prompt,
  role-override, jailbreak-persona, delimiter-break, base64 payload, …) into a 0..1 `Score` with a
  default `Suspicious` threshold of 0.5. This is the "McDonald's-chatbot" class of attack the book
  illustrates.
- **Output guardrails** (`output.go`): `OutputInspector.Inspect(ctx, input, output, systemPrompt)`
  runs a heuristic keyword toxicity check and a leak check (re-runs the redactor over the output to
  catch a secret echoed back, and flags a verbatim system-prompt segment reproduced in the output). An
  `OutputCheck` interface is the extension seam for an LLM-backed judge.
- **Aggregator** (`guardrails.go`): `Guardrails.CheckInput` / `CheckOutput` are the surface an
  integrator wires in (pre-LLM for input, post-LLM/pre-response for output). Every detector is
  **advisory** — it returns a `Verdict`/report and the *caller* decides block/degrade/allow; the
  guardrail layer classifies, it does not unilaterally reject.

### `internal/gateway` — stage 3 (model gateway)

A rules/cost/capability router that decides *which* model target handles a request, then resolves that
target to a concrete `llm.Client`. Surface:

- `route.go` — `Request` (routing-relevant projection: prompt, token estimate, `HasImage`, tenant/user,
  `UISelector`, `CostSpentUSD`), `Decision` (target name + reason), and the pure `Policy` seam.
- `policies.go` — reference policies: `RulesPolicy` (ordered data-driven `Rule`s: `ImageRule`,
  `KeywordRule`, `LargePromptRule`, first match wins), `OverridePolicy` (honors an explicit UI
  selector), `CostAwarePolicy` (routes to the cheap target once cumulative spend crosses a money
  budget — **request-shaping**, complementary to `internal/llm`'s `DegradeAt` quota-pressure
  degradation), and `ChainPolicy` (try policies in order, first non-empty target wins).
- `gateway.go` — `Gateway`: a name→`llm.Client` registry; `Route(ctx, req)` runs the policy and
  resolves the decision to a client, **failing safe** to the configured default (logged at Warn) on an
  unknown/empty target so a routing miss never drops a request.

**Explicitly NOT the refuted Bet-4 difficulty predictor.** Policies are transparent, data-driven, and
deterministic. Routing on prompt size uses exactly the `len(prompt)` signal that beat the abandoned
compressibility model (ADR-0021); the value is in the *seam* (one place that decides routing), not in a
clever predictor.

### `internal/responsecache` — stage 4 (response cache)

A two-layer inference cache consulted before an LLM call and populated after a successful generation.
Surface:

- **Exact cache** (`exact.go`): `ExactCache` over a bounded LRU `Store` with per-entry TTL; the key is
  `sha256(tenant \0 user \0 normalizedPrompt)`. O(1), no embedding cost, matches
  byte-for-byte-after-normalization repeats.
- **Semantic cache** (`semantic.go`): `SemanticCache` embeds the prompt (via the stable
  `internal/semantic.Embedder`), searches **only** the caller's `(tenant, user)` partition, and returns
  the closest entry's response when its cosine similarity ≥ threshold and it is within TTL; catches
  paraphrases the exact layer misses. Optional — a cache built without one is pure-exact.
- **Facade** (`cache.go`): `ResponseCache.Get/Set` — exact-first, then semantic — with hit/miss/error
  metrics.

**Isolation is the #1 correctness property, by construction.** The exact key *mixes* `tenant\0user\0`
into the SHA-256 (not a post-filter); the semantic layer confines the nearest-neighbor search to a
per-`(tenant, user)` partition, so another scope's vectors are **never in the candidate set** — never
scored, never compared. `Scope` is a plain `{Tenant, User}` struct the caller MUST derive from the
request's validated path identity (never the body), keeping the package standalone-testable rather than
importing `tenancy.Scope`. The invariant is pinned adversarially by
`TestResponseCacheCrossTenantIsolation` and `TestSemanticCacheNeverMatchesAcrossScope`.

## Consequences

- **Three stage gaps are closed at the capability level.** The stage-2/3/4 levers now exist as real,
  reviewed, green code, satisfying the readiness review's findings — *as libraries*.
- **They are NOT live.** No handler constructs or calls them; on every real request today, no
  guardrail runs, no gateway routes, and no response cache is consulted. Classified **CODE-ONLY /
  REAL-but-unwired** here and in the reality ledger. This ADR is a claim about tested libraries, not
  about runtime behavior.
- **No new dependency, no new license surface.** All three are Go stdlib plus already-stable internal
  packages (`internal/llm.Client`, `internal/semantic.Embedder`); the 100% MIT/Apache gate is
  preserved.
- **The isolation floor extends to the (future) cache.** When wired, the response cache cannot become a
  cross-tenant leak, because isolation is in the key/partition — the same posture as every storage
  tier (ADR-0007, ADR-0011), and proven by focused tests before any handler touches it.
- **Wiring decisions are deliberately deferred, not lost.** Where in the lifecycle each seam attaches,
  and the policy each enforces (block vs. advisory guardrails, routing table, cache TTL/threshold), are
  left to the integration follow-on rather than decided under handler pressure now.

## Follow-ons (named, not yet done)

1. **Wire the capabilities into the live request path** — construct and consult them in
   `internal/api/chat.go`, `internal/api/chat_stream.go`, and `internal/api/chat_agent.go`:
   `Guardrails.CheckInput` pre-LLM and `CheckOutput` pre-response; `Gateway.Route` to select the
   client; `ResponseCache.Get` before the call and `Set` after a successful generation, with the
   `Scope` derived from the ctx-propagated validated identity (ADR-0024), never the body. This is the
   step that moves them from CODE-ONLY to REAL.
2. **Attach an LLM-as-judge to the output guardrail** — implement `guardrails.OutputCheck` backed by
   the evaluation tier for toxicity/faithfulness grading, without `internal/guardrails` importing that
   tier.
3. **Provider-level prompt caching** — adopt a provider's prefix caching (e.g. Anthropic
   `cache_control`, OpenAI automatic prompt caching) as a complementary, *inside-the-call* lever that
   stacks with `internal/responsecache`'s outside-the-call cache.

## References

- [`docs/architecture/product-evaluation.md`](../architecture/product-evaluation.md) · [ADR-0026](0026-product-evaluation-harness.md) (the same *AI Engineering* 5-stage framing; the readiness review that surfaced these gaps)
- [ADR-0021](0021-compression-signal-small-lm-surprisal-ceiling.md) (Bet 4 "compression-difficulty routing" refuted — why the gateway is rules-based, not a learned predictor)
- [ADR-0009](0009-provider-agnostic-llm-layer.md) (the provider-agnostic-seam precedent) · [ADR-0024](0024-authenticated-identity-seam.md) (the validated identity the cache `Scope` and gateway `Request` must derive from)
- [ADR-0007](0007-multitenancy-fisico-db-unica-vs-por-tenant.md) / [ADR-0011](0011-tenant-scoped-statement-builder.md) (the by-construction isolation floor the response cache extends)
- [`docs/architecture/reality-ledger.md`](../architecture/reality-ledger.md) (the REAL / CODE-ONLY / ASPIRATIONAL classification this ADR files these packages under)
- Packages: `internal/guardrails` (`doc.go`, `redact.go`, `injection.go`, `output.go`, `guardrails.go`), `internal/gateway` (`doc.go`, `route.go`, `policies.go`, `gateway.go`), `internal/responsecache` (`doc.go`, `exact.go`, `semantic.go`, `cache.go`)
- Isolation acceptance tests: `internal/responsecache/cache_test.go` (`TestResponseCacheCrossTenantIsolation`), `internal/responsecache/semantic_test.go` (`TestSemanticCacheNeverMatchesAcrossScope`)
