---
type: Component
title: "Bitrate bpt scorer — the bits-per-token North Star instrumentation"
description: The offline batch reference-model scorer that measures bits-per-token-per-tenant (Bet 6) — how it maps memory hydration to a with/without-memory lift, the Go↔subprocess contract, the held-out sampler that prevents leakage, the durable tenant-scoped store, and the /bitrate + OTel surfaces.
tags: [components, bet-06, compression, bits-per-token, scorer, subprocess, north-star, observability]
timestamp: 2026-07-09T00:00:00Z
---

# Bitrate bpt scorer

The **North Star instrumentation** ([Bet 6](../research/bet-06-bits-per-token-metric.md)): it turns
"the agent learned the user" into a number, in bits. Decision record:
[ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md) (boundary + staged scorer)
and [ADR-0015](../decisions/0015-bpt-eval-model-and-python-batch-runtime.md) (pinned model +
dependency).

## Why it's offline, not a hot-path attribute

`bpt = − (1/N) Σ log₂ P(token_i | context)` needs per-token log-probs from a **fixed** model. No
serving provider exposes log-probs ([`internal/llm/llm.go`](../../internal/llm/llm.go): `StreamChat`
yields text + `{InputTokens, OutputTokens}` only), and the serving model's self-surprisal would not be
comparable across time anyway. So bpt is computed **offline/batch by a pinned reference model**, over a
held-out sample of a tenant's traffic — the same profile as the [sleep cycle](datafusion-sleep-cycle.md)
(ADR-0005). It never runs on the request path.

## With-memory vs without-memory (one code path, no magic)

The lift is `memory_lift = bpt_without − bpt_with`. The two contexts are the *existing*
[`hydrateSystemPrompt`](../../internal/api/chat_stream.go) outputs — the scorer reuses that code path
rather than a parallel eval-mode hydration (anti-magic, [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)):

- **context_without** = the bare persona base prompt (hydration's empty-tiers branch).
- **context_with** = persona + **consolidated** memory (facts FTS5 + semantic RAG). The scorer calls
  `hydrateSystemPrompt` with a **nil working memory**, so the live session buffer (summary/recent) is
  deliberately excluded — bpt measures what the system has *learned/consolidated* (the signal that
  trends down over time), not the live conversation.

`lift > 0` means memory made the held-out text more predictable — memory is *working*, quantified.

## Leakage guard (correctness-critical)

If the scored text is itself retrievable from the "with memory" context, "with memory" wins trivially
and the lift is spurious. Two layers close this:

1. A **held-out sampler** reserves a deterministic ~5% of user turns (hash-based) into a per-tenant
   `bitrate_samples` table **excluded from that turn's facts/semantic consolidation** — a real
   train/test split: held-out turns are scored against memory built from the *other* turns.
2. The scorer hydrates `context_with` with a **nil working memory**, so the held-out turn — which
   still lives in that session's live buffer (summary/recent) — cannot leak in through the buffer.

Together: `context_with` = persona + facts + semantic, none of which contain the held-out sample.
Locked by `internal/api` `TestHydrateNilWMExcludesLiveBuffer` (a real wm leaks the turn; nil wm does
not).

## The Go↔scorer contract (staged Python→Rust)

A Go coordinator (`internal/bitrate`, mirroring `internal/sleepcycle`) owns sampling, context
construction, tenant scoping, storage, and OTel. The subprocess is a **pure, tenant-agnostic surprisal
calculator** — swappable without touching the Go side (Phase 1 Python spike → Phase 2 Rust candle,
[ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md)).

```
argv:   --input <requests.ndjson> --output <results.ndjson>
input:  {"id":"<sample>_with|_without","text":"...","context":"..."}
output: {"id":"...","bits_total":F,"bits_per_token":F,"n_tokens":N,"model_id":"distilgpt2@<rev>","unit":"..."}
final:  {"scored":N,"model_id":"...","backend":"LM_REAL|FALLBACK_NGRAM"}
exit:   0 = ok; non-zero = failure (reason on stderr), retried with backoff — as ADR-0005
```

Phase 1 scorer: [`spikes/compression/bitrate_scorer.py`](../../spikes/compression) wrapping the
validated `surprisal.py` (default model **distilgpt2**, Apache-2.0). Enabled only when
`AISOTROPY_BITRATE_BIN` is set (dormant by default, like `AISOTROPY_SLEEPCYCLE_BIN`).

> **Building and exercising the Phase-2 Rust scorer:**
> `scripts/dormant-lanes.sh --stage workers --yes` builds `crates/bitrate-scorer` (measured
> 2026-07-27: 41s cold, 6 MB binary), exports `AISOTROPY_BITRATE_BIN` at the built path, and runs
> the Go-side contract tests. The `-tags manual` **live** test (`TestScorerLive`) additionally needs
> `AISOTROPY_BITRATE_MODEL_DIR` pointing at converted GPT-2 weights
> ([ADR-0015](../decisions/0015-bpt-eval-model-and-python-batch-runtime.md)); without them the lane
> reports an explicit SKIP, never a pass. See [Dormant lanes](../deployment/dormant-lanes.md) and
> [ADR-0075](../decisions/0075-dormant-lanes-operator-runnable.md).

## Durable, tenant-scoped store

The **trend over weeks** is the whole point, so history must survive restarts — *not*
[`costmeter`](../../internal/costmeter/costmeter.go) (in-memory, per-process) and *not* the
[facts tier](rqlite-facts-memory.md) (that is memory *about* the user; bpt is telemetry). A dedicated
SQLite/rqlite store (`internal/bitrate/store.go`, modeled on `internal/facts/routing_store.go`) holds:

```sql
bpt_measurements(tenant_id, user_id, measured_at, bpt_with, bpt_without,
                 lift, n_tokens, model_id, sample_id)
```

Every read/write is filtered by `(tenant_id, user_id)` through [`internal/tenancy`](../architecture/multi-tenancy.md)
([ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)). `model_id` on every row makes
cross-time comparability explicit: a model change starts a new series, never mixes curves.

## Surfaces

- **OTel:** one span per scorer run + an `agent.compression.bpt` span event per measurement
  (`tenant_id`, `bpt_with`, `bpt_without`, `lift`, `model_id`, `n_tokens`) — the correct home for the
  attribute the bet wanted, on the *measurement* span, not the serving span.
- **HTTP:** `GET /bitrate` (next to `/costs`, outside the OpenAPI contract): per-tenant
  `{latest_lift, latest_bpt_with, trend_slope, model_id, n_measurements}` from the store.

## Success criterion (Bet 6)

- **Positive lift** consistently (`bpt_with < bpt_without`) — memory helps.
- **Downward `bpt(tenant, t)` trend** for active tenants — real learning.
- **Sanity for the other bets:** Bets 1–3 must move this needle; if they optimize and bpt doesn't
  drop, they aren't working.

## References

- [Bet 6](../research/bet-06-bits-per-token-metric.md) · [compression thesis](../vision/compression-thesis.md) · [OKR O8](../roadmap/okrs.md)
- [ADR-0014](../decisions/0014-bits-per-token-reference-scorer-boundary.md) · [ADR-0015](../decisions/0015-bpt-eval-model-and-python-batch-runtime.md)
- Pattern precedent: [sleep cycle](datafusion-sleep-cycle.md) ([ADR-0005](../decisions/0005-datafusion-go-rust-boundary.md))
- Spike: [`spikes/compression/`](../../spikes/compression) (bet06_memory_lift.py, surprisal.py)
