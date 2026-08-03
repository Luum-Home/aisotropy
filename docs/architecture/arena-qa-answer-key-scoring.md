---
type: Concept
title: "Arena QA/memory scoring — answer keys, not verbosity"
description: >
  Why the deterministic QA and memory scorer moved from reference token-containment to explicit answer keys as a fairness fix for every framework.
tags: [architecture, arena, scoring, qa, memory, fairness]
timestamp: 2026-08-03T00:00:00Z
---

# Arena QA/memory scoring — answer keys, not verbosity

> Why the deterministic QA and memory scorer moved from **reference token-containment**
> to an explicit **answer key**, and why that is a fairness fix for every framework
> (not a thumb on the scale for any one). Companion to
> `.claude/rules/arena-deterministic-judging.md`.

## The bug: the scorer rewarded verbosity, not correctness

The old `ScoreQA(reply, reference)` scored the **fraction of the REFERENCE's tokens
present in the reply** (pass at ≥ 0.80). On a full-sentence reference this silently
measured *how much of the reference's wording the reply echoes* — which is verbosity,
not correctness.

Observed in a real Tier-B sweep (`agnes-2.0-flash`, held constant for every framework):

| Question | Reference | A terse-correct reply | old score |
|---|---|---|---|
| capital of France? | "Paris is the capital of France." | **"Paris"** | 0.33 ✗ |
| chemical symbol for gold? | "The chemical symbol for gold is Au." | **"Au"** | 0.25 ✗ |
| light speed km/s? | "…299,792 kilometers per second (about 300,000 km/s)…" | **"299792"** | 0.14 ✗ |
| favorite color (recall)? | "Your favorite color is teal." | **"Your favorite color is teal."** | passes only verbatim |

Every one of those replies is **correct**. They failed only because they don't repeat
the reference's framing words ("capital", "France", "kilometers", …). Meanwhile a
verbose reply that echoes the framing ("The capital of France is Paris") scored 1.0.

Two things are wrong with that:
1. **It penalizes concision.** A QA system answering "Paris" is giving a *better* answer
   than one padding it out — but the scorer rewarded the padding. This hits **any**
   framework that answers tersely; in the sweep it happened to be aisotropy (qa 1/6,
   memory 0/3) while the verbose adapters scored 0.9, but a concise ai-sdk or mastra
   reply would have been failed identically.
2. **It can't tell a correct answer from an echo of the question.** "Paris" (right) and
   "France" (the question's own subject, wrong) are both single reference tokens; a
   containment metric has no way to prefer the answer over the question word.

## The fix: score against an explicit answer key

Each QA/memory task now carries an `answer` field — the **objectively-correct answer
string(s)** — in `arenas/live/tasks/suite.jsonl`:

```json
{"id":"qa-01-capital", "reference":"Paris is the capital of France.", "answer":["Paris"]}
{"id":"qa-05-lightspeed", "reference":"…299,792…(about 300,000 km/s)…",
 "answer":["299792","299,792","300000","300,000"]}
{"id":"mem-02-pets", "reference":"Your two pets are Pepper … and Miso …",
 "answer":["Pepper Miso"]}
```

`ScoreQA(reply, reference, answers)` (and `ScoreMemory`, same mechanics):
- **exact normalized match** → 1.0;
- else, **with an answer key**, the reply PASSES iff it fully contains at least one
  acceptable key (`containmentRatio(keyTokens, replyTokens) == 1.0`);
- else (no key) → the **legacy reference token-containment** fallback (backward compatible).

Key semantics:
- **Multiple keys are alternatives** — any one suffices. So `["299792","299,792",…]`
  accepts either the exact or rounded figure, comma or not (needed because `normalize`
  turns "299,792" into the two tokens "299 792").
- **A multi-token key requires ALL its tokens.** `"Pepper Miso"` for a two-fact recall
  passes only if the reply names both pets — an incomplete recall correctly fails.

## Why this is fair to every framework (not a thumb on the scale)

The metric now measures **correctness, not verbosity**, symmetrically:

| Reply | old (containment) | new (answer key) |
|---|---|---|
| "Paris" (terse-correct) | ✗ 0.33 | ✅ pass |
| "The capital of France is Paris" (verbose-correct) | ✅ 1.0 | ✅ pass |
| "France" (echoes the question) | ✗/borderline | ✗ fail |
| "The capital is Berlin" (wrong) | ✗ | ✗ fail |
| "I don't know" (non-answer) | ✗ | ✗ fail |

A concise correct answer from **any** framework now passes; a wrong or evasive answer
from **any** framework still fails. The change RAISES a framework's score only to the
extent that it was giving **correct** answers the old metric wrongly failed — which is
the definition of removing a bias, not adding one. In the validating re-run, competitors
that were already verbose stay ~unchanged; aisotropy (and any concise answerer) rises to
reflect the answers it was actually getting right.

The answer keys are the plain correct answers (Paris, Au, seven, Jupiter, teal, 13, …),
chosen from the reference and the question — not tuned to any adapter's output.

## Deterministic-judging compliance

- `ScoreQA`/`ScoreMemory` stay **pure** (only `encoding/json`, `fmt`, `regexp`, `sort`,
  `strconv`, `strings` — no LLM/network/random/clock). The purity check in
  `scripts/arena-determinism-guard.sh` is unchanged and still green.
- New reproducibility fixtures (`qa-answerkey-concise`, `qa-answerkey-echo-fail`,
  `memory-answerkey-terse`) are added to `TestDeterministic_Reproducible`, so the
  guard proves the new path byte-identical across 5 fresh processes × 100 iterations.
- Fairness is regression-tested: `TestScoreQA_AnswerKey_ConciseAndVerboseBothPass`,
  `TestScoreQA_AnswerKey_WrongAndQuestionEchoFail`,
  `TestScoreQA_AnswerKey_EquivalentNumberForms`,
  `TestScoreMemory_AnswerKey_TerseRecallPasses`,
  `TestScoreMemory_AnswerKey_MultiTokenRequiresAll`.
- The legacy token-containment path remains for any task without an `answer` key, and its
  tests (`TestScoreQA_ContainmentPass`, `TestScoreMemory_ForgottenFact`) still pass.
