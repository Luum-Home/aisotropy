---
type: Research
title: What the community already built, and what our arena reinvented worse
date: 2026-07-28
status: research — adopt list at the end, not yet actioned
---

# Prior-art sweep on agent-framework benchmarking

## Why this exists, and why it is late

`.claude/rules/dependency-reuse-first.md` says: before building a capability on
top of a dependency, inventory what already exists **from the source**, then
reuse or document the precise gap. We applied that discipline to gollem. We
never applied it to the ARENA ITSELF — we built a benchmark harness, a scoring
package, a task suite and a memory taxonomy without asking what the field had
already standardised.

This sweep is that question, asked late. The honest summary: **we reinvented a
memory taxonomy that exists in better form, we hit a failure mode the literature
has a name for, and we built a scorer where reusable harnesses exist — while the
one thing our arena measures that nobody else does went unnamed as our actual
contribution.**

## 1. Memory benchmarks — we reinvented this, and worse

| | ours | LoCoMo | LongMemEval |
|---|---|---|---|
| scale | **8 items** | 1,540 QA samples, 10 conversations, ~27 sessions each, ~16.6K tokens per conversation | 500 questions, ~115K-token histories |
| categories | ad hoc | single-hop (841), multi-hop (282), **temporal reasoning** (321), open-domain (96) | single-session user/assistant/preference recall, **knowledge update**, **temporal reasoning**, **multi-session recall** |

Read that category list against our own dimension names:

- LongMemEval's **multi-session recall** is our `memory_cross_session`;
- its **knowledge update** is our `mem-h01-overwrite`;
- its **temporal reasoning** is our `mem-03-deadline` — the very item we spent
  today discovering behaves differently from the others.

We derived, item by item and by accident, a taxonomy that is published, larger,
and peer-reviewed. Worse: because ours was ad hoc, the temporal/derived items sat
UNLABELLED next to pure-recall items, which is exactly what made the integration
lane's 6/8 ceiling look like a defect until it was investigated. LongMemEval
would have told us on day one that those are a different category.

Sources: [LoCoMo/LongMemEval overview](https://www.emergentmind.com/topics/locomo-and-longmemeval-_s-benchmarks),
[mem0's 2026 memory-benchmark survey](https://mem0.ai/blog/ai-memory-benchmarks-in-2026).

## 2. Contamination — a named, studied failure mode we rediscovered by hand

Today's inflated 8/8 was benchmark contamination: the store retained answers a
REAL model had computed in earlier runs, and the mock replayed them.

The field has a literature on exactly this. Reported contamination reaches **up
to 45% on popular benchmarks**; over 90% of examples in some suites were flagged
during GPT-3 evaluation. There is a research line on **contamination-resistant
benchmark design**, and a documented warning that the usual mitigations
(paraphrasing, translation, context tweaks) are often assumed effective without
validation and can change question difficulty.

Our instance was store-side rather than training-side, but the design lesson
transfers directly: **a benchmark must be built so that a stale artifact cannot
satisfy it.** We got there by finding an arithmetic answer an echo could not have
produced. A design rule would have been cheaper.

Sources: [contamination-resistant benchmarks](https://arxiv.org/html/2605.19999v1),
[awesome-data-contamination](https://github.com/lyy1994/awesome-data-contamination).

## 3. Harnesses — reusable ones exist, and they model something ours does not

**Inspect AI** (UK AI Safety Institute), **promptfoo**, **DeepEval**, **LangSmith**.

The capability gap that matters: these model agent **trajectories** as
first-class — the chain of reasoning, tool calls and intermediate steps across
many turns — whereas our scorer grades the FINAL TEXT. DeepEval and LangSmith
both treat multi-step traces natively.

That is not a cosmetic difference. Several of our dimensions ask a question about
a trajectory ("did the framework dispatch the tool and fold the result back")
and answer it by inspecting a final string plus a side-channel `tool_calls`
array. A trajectory-aware harness expresses that directly.

Sources: [DeepEval agent evaluation](https://deepeval.com/guides/guides-ai-agent-evaluation),
[eval framework comparison](https://blog.agentailor.com/posts/top-ai-agent-eval-frameworks-2026),
[awesome-evals](https://github.com/benchflow-ai/awesome-evals).

## 4. Methodology conventions — mostly we already match

Community framework benchmarks report end-to-end latency P50/P95/P99,
throughput, peak RSS, CPU, cold-start, and **determinism rate (same output
across runs)** over a fixed request count at fixed concurrency.

We have all of these except one: we enforce determinism of the SCORER
(`arena-determinism-guard.sh`) but never report **output determinism per
framework** as a measured number. That is a cheap addition and a real signal —
it is exactly the axis on which a framework's own retry/temperature/caching
behaviour shows up.

Source: [2026 framework benchmark methodology](https://dev.to/saivishwak/benchmarking-ai-agent-frameworks-in-2026-autoagents-rust-vs-langchain-langgraph-llamaindex-338f).

## 5. What is genuinely OURS, and was never named as the contribution

None of LoCoMo, LongMemEval, AgentBench, GAIA or SWE-bench asks:

- does framework X leak tenant A's data to tenant B;
- does state survive `kill -9` and reload in a separate process;
- is the scope enforced by construction (L3) or by a key the caller must
  remember to pass (L1);
- does the store fail loudly or degrade silently when it is unreachable.

Those are **infrastructure properties of an integration**, not memory quality of
a model. They are what our arena measures that the field does not, and they are
where today's only surviving competitive claim lives (8/8 competitors hand-roll
a compound scope key; aisotropy enforces by construction).

The irony worth recording: we spent effort reinventing the part that was
standardised and left the genuinely novel part unlabelled until an authorship
sweep forced it out.

## Adopt list

1. **Replace the memory taxonomy with LongMemEval's categories.** Not the whole
   dataset — the CATEGORY SPLIT (single-session recall, knowledge update,
   temporal reasoning, multi-session recall). It immediately explains the
   integration lane's ceiling: temporal-reasoning items are a named category
   that a mock model cannot serve, rather than two anomalies.
2. **Make contamination-resistance an explicit design property**, and state it
   where the suite is defined. Today's fix (run-scoped session ids) is the
   mechanism; the principle deserves writing down so the next store-backed
   dimension is built with it.
3. **Evaluate Inspect AI as the trajectory layer** before extending our scorer
   again. Keep our deterministic scoring package for the primary number
   (`.claude/rules/arena-deterministic-judging.md` is not negotiable), but stop
   hand-rolling trajectory inspection if a maintained harness expresses it.
4. **Report output determinism per framework** — one column, real signal,
   already conventional elsewhere. **But not where it first looked cheap.** The
   integration lane's mock echoes the prompt, so it is deterministic BY
   CONSTRUCTION and would report 100% for everyone: the cheap place to measure
   it is the place where the measurement means nothing. It has to run on the
   REAL lane, where a framework's retry, temperature and caching behaviour
   actually shows, and there it costs 2x model calls per measured item. That
   makes it a spending decision, not a free column — scope it to a small fixed
   sample (e.g. the 3 qa items) rather than the whole suite.
5. **Name the isolation/durability dimensions as the arena's contribution**, and
   consider citing LoCoMo/LongMemEval for the memory-quality half rather than
   competing with them. A benchmark that says "we use the standard suite for
   memory quality and add multi-tenant isolation, which the standard suite does
   not cover" is more credible than one that implies it invented both.

## Related

- `.claude/rules/dependency-reuse-first.md` — the rule this sweep should have
  followed before the arena was built
- `docs/architecture/arena-integration-lane-ceiling.md` — the 6/8 ceiling that
  LongMemEval's taxonomy would have predicted
- `docs/architecture/arena-authorship-lens-sweep.md` — where the isolation
  finding came from
