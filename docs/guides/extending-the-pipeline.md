---
type: Guide
title: Extending the pipeline
description: Plug custom answer-time policy at the WithGuardrailCheck seam instead of wrapping the LLM client — input-facing and output-facing checks, the grounded-output pattern, and an honest map of what ships today.
tags: [guides, howto, guardrails, extension, policy]
timestamp: 2026-07-16T00:00:00Z
---

# Extending the pipeline: plug policy at the seam, don't wrap the client

You want to run a policy check before an answer reaches the user — redact or
classify what came in, validate what the model produced, refuse when something is
off. The instinct is often to wrap the LLM client: supply a custom `WithLLM`
implementation that calls the real model, then inspects or rewrites the reply.

That works, but it is the **wrong layer**. To get one inspection hook you
re-implement token streaming, usage accounting, and context cancellation; your
check sits **outside** the runtime's own guardrail aggregation (the built-in
toxicity and secret-leak heuristics, and the block-output substitution); and you
own a fragile ~100-line shim forever. The policy you actually wanted was a few
lines of decision logic.

Aisotropy already has the seam for this: **`WithGuardrailCheck`** (ADR-0033). This
guide shows how to use it for both input-facing and output-facing policy — without
touching the client — and is honest about what is implementable from where today.

Pairs with a runnable, keyless example you can copy:
[`examples/guardrail-policy`](../../examples/guardrail-policy).

## The seam

`WithGuardrailCheck` registers one or more **output checks**. Each is a small
interface (aliased as `aisotropy.GuardrailCheck`; see
[`aisotropy/extend.go`](../../aisotropy/extend.go)):

```go
Name() string
Check(ctx context.Context, in GuardrailCheckInput) GuardrailVerdict
```

The runtime runs your check on **every model response**, alongside its built-in
toxicity and leak checks, and aggregates the verdicts. A check returns an
**advisory** verdict: `Suspicious` (with named `Signals` and a `0..1` `Score`).
When any check is `Suspicious` **and** `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT` is
enabled, the non-streaming chat path (and the in-process `App.Turn` that shares it)
substitutes a safe refusal for the model's reply. With blocking off, the verdict is
recorded and surfaced but the reply is returned unchanged.

Two properties make the seam more capable than it first looks:

1. **A check sees the whole exchange.** The `Check` payload carries the original
   user **`Input`**, the model's **`Output`**, and the **`SystemPrompt`** in force.
   So a single output check can carry *input-facing* policy (decide from what the
   user sent) as well as *output-facing* policy (decide from what the model said).

2. **Checks accumulate.** Call `WithGuardrailCheck` once per check or pass several
   at once; each is appended to the inspector. Compose small, single-purpose checks
   rather than one monolith.

### What it is NOT (honesty gate)

- **It is the OUTPUT, advisory stage.** The seam runs *after* generation and can
  only flag/refuse — it does **not** mutate the model's input. If you need to
  scrub secrets *before* the model sees them, that is a different, **built-in**
  stage: the runtime's `Redactor` and prompt-injection `Detector` run on input
  automatically. You do not wrap the client for those, and they are not this seam.

- **`WithGuardrailCheck` is output-only.** There is no consumer *input*-check
  interface — input defence is built in. When this guide says "input-facing
  check", it means an output check that *reads `in.Input`* to decide, not a
  pre-model hook.

## Input-facing policy: classify / flag from `in.Input`

Read the user's message off the check payload and annotate or refuse. A coarse
example: classify intent, and flag a turn whose input carries a secret-looking
token so, with block-output on, the runtime refuses instead of transcribing a
credential into the answer.

```go
func (intentClassifier) Name() string { return "intent-classifier" }

func (intentClassifier) Check(_ context.Context, in GuardrailCheckInput) GuardrailVerdict {
    intent := "statement"
    if strings.Contains(in.Input, "?") {
        intent = "question"
    }
    if secretPattern.MatchString(in.Input) { // e.g. sk-… / "api key:" / "password:"
        return GuardrailVerdict{
            Suspicious: true,
            Signals:    []string{"intent:" + intent, "input-secret"},
            Score:      1.0,
        }
    }
    return GuardrailVerdict{Signals: []string{"intent:" + intent}} // annotate, don't block
}
```

A non-`Suspicious` verdict may still carry `Signals`: that is annotation without a
block, useful for downstream logging/metrics.

## Output-facing policy: the grounded-output pattern

The most valuable output check is usually a **corpus-grounded validator**: flag a
response claim that the tenant's ingested knowledge does not support. This is the
generic core of "approved-claims validation" — no domain policy baked in, just *is
this claim backed by what we actually ingested?*

Read `in.Output`, extract the claims it asserts, and check each against a corpus
(here a tiny in-memory map standing in for the tenant's ingested facts):

```go
func (g groundedValidator) Name() string { return "grounded-output" }

func (g groundedValidator) Check(_ context.Context, in GuardrailCheckInput) GuardrailVerdict {
    var ungrounded []string
    for _, claim := range extractClaims(in.Output) {
        supported, known := g.corpus[claim.subject]
        if known && supported != claim.value {
            ungrounded = append(ungrounded, "ungrounded:"+claim.String())
        }
    }
    if len(ungrounded) > 0 {
        return GuardrailVerdict{Suspicious: true, Signals: ungrounded, Score: 1.0}
    }
    return GuardrailVerdict{}
}
```

In production the corpus lookup is retrieval + entailment (or an LLM judge) over
the tenant's ingested documents rather than a map — but the **seam is identical**:
the check reads the output, compares against what the tenant is entitled to
assert, and returns an advisory verdict the runtime aggregates and (optionally)
enforces. The [example](../../examples/guardrail-policy) implements exactly this
shape with a keyless stub so you can watch it fire.

## Wire it

```go
app := aisotropy.New(
    aisotropy.WithGuardrailCheck(intentClassifier{}, newGroundedValidator()),
    // ... other options; NO WithLLM wrapper needed for policy.
)
```

Set `AISOTROPY_GUARDRAILS_BLOCK_OUTPUT=true` to have a flagged turn become a safe
refusal on the non-streaming path; leave it unset to run the checks in
annotate-only mode.

## Where other policy plugs in

`WithGuardrailCheck` is one of a family of "bring your own" seams in
[`aisotropy/extend.go`](../../aisotropy/extend.go). Each replaces a hand-rolled
wrapper with a typed decision point:

| Seam | Replaces | Decides |
|---|---|---|
| `WithGuardrailCheck` | wrapping the client to inspect replies | output-safety / grounding verdicts |
| `WithReranker` | re-ordering RAG hits outside the runtime | which retrieved fragments enter context (streaming/`/ask` paths) |
| `WithSummarizer` | a custom condensation loop | how the working-memory summary buffer is condensed |
| `WithModelPolicy` | routing logic in front of the client | which registered model target a request uses |
| `WithEvalJudge` | a bespoke scoring harness | the guardrails output-safety judge |

Prefer the seam over a wrapper wherever one exists: you inherit the runtime's
streaming, cancellation, usage accounting, and aggregation instead of rebuilding
them.

## Honest state of the API (what ships today)

- **The seam is wired end-to-end.** A registered check is invoked by the running
  pipeline and, with block-output on, its `Suspicious` verdict replaces the reply.
  This is proven by the repo's own tests (`aisotropy/extend_test.go`).

- **Implementing the check from a fully external module works today.** The `Check`
  payload and verdict are re-exported on the public façade as `GuardrailCheckInput`
  and `GuardrailVerdict` (type aliases for the internal types), so an external
  consumer implements `GuardrailCheck` against `github.com/Luum-Home/aisotropy/aisotropy`
  alone — no `internal/...` import. The runnable
  [example](../../examples/guardrail-policy) uses only the public façade, proving it.

That distinction is the whole point of documenting the seam now: so an adopter
reaches for `WithGuardrailCheck` instead of re-deriving a client wrapper — and
knows exactly what is and isn't implementable from where.
