---
type: ADR
title: "ADR-0073 — Provider prompt caching for the repeated per-turn prefix (ADR-0063 half-2)"
description: App.Turn re-sends the whole assembled system prefix — persona, hardening block, fenced memory/RAG context, tool specs — on EVERY turn, because the per-turn contract is one call per turn. That prefix is largely identical between consecutive turns of a session and is the dominant input-token cost. Providers cache it if asked. This ADR proposes adopting provider prompt caching, states exactly where it would attach, and — the reason it is a decision and not a patch — resolves it against ADR-0010's guardrail that nothing wraps a provider's HTTP call without a dedicated ADR. Proposed, awaiting operator approval; nothing is implemented.
tags: [decisions, adr, llm, cost, token-economy, caching, gollem, provider, proposed]
timestamp: 2026-07-27T00:00:00Z
supersedes: []
revisits: [0009, 0010, 0063]
---

# ADR-0073 — Provider prompt caching (ADR-0063 half-2)

## Status

**🟡 PROPOSED — not implemented, awaiting operator approval.** This ADR exists to *unblock a
decision*, not to record one already taken. It was written because the work it describes crosses
**ADR-0010's guardrail** ("no Gollem-native `Model`/provider owning the HTTP call without a
dedicated ADR"), and that guardrail is satisfied by an ADR that argues the case — not by an
implementation that quietly slips past it.

If approved, it closes the second half of ADR-0063, whose first half (Anthropic-native
tool-calling on the official SDK) shipped and whose second half — "measure wrapping
`provider/anthropic` for prompt caching" — was explicitly deferred as operator-gated.

## Context

### What we pay for today, and why

`App.Turn` answers one prompt per call. That contract is deliberate and stays (see
`aisotropy/history.go`, which added `IngestHistory` precisely so *loading* context no longer
abuses it). But it means every turn re-sends an assembled system prefix that is largely
**identical to the previous turn's**:

- the persona / base system prompt (`internal/api/persona.go`) — byte-identical across a session
- the optional persona-hardening block (ADR-0070) — byte-identical
- the structured-output schema rider (ADR-0070 moved it into the system region) — byte-identical
- the fenced untrusted-context block (`hydrateSystemPrompt`) — the working-memory summary and
  semantic hits, which change *slowly*: consecutive turns usually share most of it
- tool specs, when tools are in scope — byte-identical

Only the final user turn is genuinely new. The arena's own token table shows the shape of the
bill: the input side dominates, and it is dominated by this prefix.

### What providers offer

Both provider families we target expose explicit prompt caching: a caller marks a prefix as
cacheable, the provider stores it server-side for a short TTL, and subsequent requests that begin
with the same bytes are billed at a steep discount on the cached portion and skip re-processing
it. The mechanism is prefix-exact: it pays off precisely when the *front* of the prompt is stable
and the *tail* varies — which is exactly the shape described above, and exactly the shape
ADR-0070's rider relocation reinforced by moving varying instructions OUT of the tail.

### Why this is blocked on a decision rather than just done

`internal/llm` deliberately does not own provider HTTP semantics. ADR-0009 chose the official
SDKs; ADR-0010 drew the guardrail that gollem's own `Model`/provider layer is not to be adopted —
nor a provider call wrapped — without a dedicated ADR, so that the cognitive engine's boundary
stays legible. Prompt caching is a **provider-protocol feature**: using it means either setting
provider-specific cache markers on the request, or wrapping the provider client that builds it.
That is the guardrail, not an incidental detail.

## Decision (proposed)

Adopt provider prompt caching **at the existing `llm.Client` seam**, as an optional capability,
without adopting gollem's provider layer.

1. **Where it attaches.** An optional interface alongside the existing `ToolCaller` upgrade
   (ADR-0012's pattern), e.g. `CacheablePrefix` — implemented by `OpenAICompatClient` and
   `AnthropicClient`, absent on the stub. Capability-detected exactly like `ToolCaller`, so a
   client that does not implement it simply pays full price. **Interface-upgrade-loss is a known
   trap here** (the arena's tools fragility was exactly that): any decorator added later MUST
   forward this interface, and the invariant needs its own test.
2. **What is marked cacheable.** The system region only, up to and including the fenced context
   block — never the final user turn. This is a natural fit because ADR-0070 already moved the
   varying schema rider out of the tail into the system region: the boundary is a real seam, not
   an arbitrary cut.
3. **What is NOT adopted.** Gollem's `provider/*` layer stays unused; ADR-0009's official-SDK
   choice and ADR-0010's guardrail both survive. This ADR authorises **one** narrow crossing:
   setting cache markers on the request our own client already builds.
4. **Off by default**, env-gated, so a deployment opts in. Caching changes provider-side behaviour
   and billing shape; that should be a deliberate act.

## Consequences

- **Cost.** The repeated prefix is the dominant input cost of a multi-turn session; discounting it
  is the single largest token-economy lever left after `IngestHistory`.
- **Correctness risk — the one that matters.** Prefix caching is byte-exact. Anything that makes
  the prefix vary per turn silently destroys the hit rate without failing anything: a timestamp in
  the persona, a reordered memory hit, a non-deterministic fence. This is why the recent
  determinism work is a *precondition*, not a coincidence — `coalesceSiblings` and
  `sortByChunkIndex` exist partly so the assembled prefix is reproducible. Any adoption MUST ship
  a test asserting that two consecutive turns of an unchanged session produce a byte-identical
  cacheable prefix.
- **Multi-tenancy.** Cache keys are provider-side and derived from content. A prefix containing
  another tenant's data would already be a constitutional violation long before caching; caching
  does not introduce the risk, but it does *amplify the blast radius* of one, so the isolation
  tests remain the gate.
- **Observability.** Providers report cached vs uncached token counts. Those belong on the
  existing `gen_ai.*` span attributes (ADR-0068) so the saving is measured, not assumed.

## Open questions for the operator

1. **Approve the guardrail crossing?** This is the actual ask. Setting provider cache markers on
   our own client's request is narrow, but it is provider-protocol coupling that ADR-0010 asked to
   be argued explicitly.
2. **Which provider first?** The OpenAI-compatible path is the runtime default (and what the arena
   measures); the Anthropic path is where ADR-0063 originally scoped this.
3. **Measure before or after?** A cheap A/B (the shape `scripts/safeguard-ab.sh` established) can
   quantify the saving on a handful of real multi-turn sessions before committing to the
   integration.

Until (1) is answered, nothing here is built.
