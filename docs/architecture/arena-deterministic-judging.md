---
type: Process
title: "Arena Deterministic Judging — the primary score is pure, not an LLM"
description: The live arena's PRIMARY quality score is a pure, deterministic, unit-tested function per dimension (arenas/live/runner/scoring), enforced by scripts/arena-determinism-guard.sh. The LLM-as-judge (agnes judging agnes) is demoted to a SECONDARY, opt-in, caveated signal behind -llm-judge (default off). Generalizes the project's "executable gate + inherited rule" pattern (scripts/prune-guard.sh, the runner's matrix-completeness gate) to quality scoring.
tags: [architecture, process, arena, scoring, judging, determinism, verification, tooling]
timestamp: 2026-07-18T00:00:00Z
---

# Arena Deterministic Judging — the primary score is pure, not an LLM

> **Origin (the problem this fixes):** the live arena's original quality signal was an
> **LLM-as-judge** — agnes scoring agnes's own outputs (`arenas/live/runner/eval.go`'s
> `judgeClient`, calling agnes's `/chat/completions`). A same-family judge rates its own
> family's outputs **generously and non-discriminatingly**: observed behavior was a flat
> Correctness/Helpfulness/Faithfulness of roughly **5.0** across frameworks that behave very
> differently on the objective, mode-specific signals (retrieval hit@1, trap-refusal, tool
> execution, structured-validity). A judge that cannot distinguish a framework that works from
> one that doesn't is not a quality signal — it is noise wearing a number. This document is the
> premise agents inherit: **the arena's PRIMARY score is deterministic by construction**, and
> that claim is backed by an executable gate, not a comment — the same discipline as
> `scripts/prune-guard.sh` (no deletion claim without executable evidence) and the runner's own
> full-matrix completeness gate (`arenas/live/runner/matrix.go`).

## The rule (always active for arena scoring work)

**The live arena's PRIMARY quality score MUST be a pure, deterministic function of its inputs —
no LLM call, no network call, no randomness, no wall-clock-dependent decision.** An LLM-as-judge
may exist only as a **SECONDARY, opt-in, clearly caveated** signal, never as the number a
framework comparison is decided on. Any change proposing an LLM (or any non-deterministic
process) as the PRIMARY judge is rejected by construction — `scripts/arena-determinism-guard.sh`
will not pass a scoring package that imports an LLM client, `net/http`, `math/rand`, or `time`.

## The three pieces

### 1. `arenas/live/runner/scoring` — the pure scorer

One deterministic function per dimension, each returning a `Score{Value float64, Pass bool,
Detail string}`:

| Function | Dimension | Deterministic rule |
|---|---|---|
| `ScoreQA` | qa | Normalized exact match (case-fold, punctuation-stripped) scores 1.0; else a token-containment ratio (what fraction of the reference's non-stopword tokens appear in the reply) against an **0.8** pass threshold — chosen to reject a reply that echoes the question's subject words without the actual answer value (see `TestScoreMemory_ForgottenFact`). |
| `ScoreMemory` | memory | Same reduction as `ScoreQA` (recall is scored identically to QA — exact-or-containment against the persisted fact's reference). |
| `ScoreRAG` | rag (grounded + trap) | Grounded: hit@1 — the top retrieved doc ID must be one of the gold `supporting_docs`. Trap: PASS iff the reply reads as a refusal (`LooksLikeRefusal`), never an invented answer. |
| `ScoreTools` | tools (exact-arg contract) | Exact tool-name match + every `expected_args` key present with a normalized-equal value (case-fold strings, numeric-string equivalence so `"47"` == `47` == `"47.0"`) + the call actually executed (non-empty result). Wired against `arenas/live/tasks/suite.jsonl`'s `expected_tool`/`expected_args` fields when a workItem carries them. |
| `ScoreToolsNumeric` | tools (arena's built-in arithmetic set) | PASS iff a tool call executed AND the expected numeric result appears in the reply (thousands-separator-normalized). Used for `arenas/live/runner/tasks.go`'s `arithmeticTasks`, whose expected ARGUMENT string is not pinned (many equivalent tool-call formats), but whose expected numeric RESULT is. |
| `ScoreStructured` | structured | Non-nil value that marshals to a well-formed JSON object/array (not null, not a bare scalar). `ScoreStructuredWithSchema` additionally validates `type`/`required`/one level of `properties` against a supplied JSON Schema fragment, for future wiring once a per-task schema is threaded through. |
| `ScoreStream` | stream | PASS iff a positive TTFT was actually reported. Deliberately carries **no** arbitrary latency SLA (e.g. "TTFT under Nms") — an invented threshold would itself be a non-objective judgment call; the raw ms value is reported in the benchmark tables for comparison. |

**Purity contract** (enforced by the guard, §2): the package's own `.go` files import only
`encoding/json`, `fmt`, `regexp`, `sort`, `strconv`, `strings` — none of `net/http`, `math/rand`,
`time`, `internal/llm`, or any agnes/LLM client. Every function is a pure function of its
arguments: same input → byte-identical output, forever. No package-level mutable state, no
goroutines, no wall-clock reads.

**Consolidation, not reinvention.** The objective mode-signal checks (`retrievalHit`,
`looksLikeRefusal`, `toolExecuted`, `numberPresent`) already existed, scattered across
`arenas/live/runner/eval.go`, before this change — they were secondary to the LLM-judge. This
package **consolidated** them into one tested, importable, reproducible package and **promoted**
them to the primary score; it did not invent new scoring logic from scratch.

### 2. `scripts/arena-determinism-guard.sh` — the enforcement gate

Two checks, either of which BLOCKS (exit 3):

1. **Purity** — `go list -f '{{join .Imports "\n"}}' ./scoring` (the package's own **direct**
   imports, not the full transitive stdlib closure — almost any Go package transitively reaches
   `time` via `fmt`/`os`, so a transitive check would false-positive on everything) must contain
   none of `net/http`, `math/rand`, `time`, `internal/llm`, or anything matching
   `agnes|anthropic|openai|/llm(/|$)`.
2. **Reproducibility** — `TestDeterministic_Reproducible` (in `scoring_test.go`) is re-run 5 times
   as separate **process** invocations (`-count=5`), each internally looping every fixture 100
   times and diffing against its own first-iteration baseline. Both in-process nondeterminism
   (unordered map iteration reaching a formatted string) and process-level nondeterminism would
   surface as a failure.

Run standalone: `bash scripts/arena-determinism-guard.sh`. Wired into the runner's gate path:
`arenas/live/runner/main.go`'s `runDeterminismGate` runs it before any billable model call unless
`-skip-determinism-gate` is passed (mirrors the existing `gateMatrices` full-matrix gate). CI:
treat it as a required check alongside `go build ./... && go vet ./... && gofmt -l .` for any
change touching `arenas/live/runner/scoring/**` (see ADR-131's local-CI-first posture for Go
quality gates, `.claude/rules/cos/RULES-COMPACT.md` §14).

### 3. This rule — the premise agents inherit

Projected into [`.claude/rules/arena-deterministic-judging.md`](../../.claude/rules/arena-deterministic-judging.md)
so in-repo agents auto-load it. An agent proposing an LLM (or any network/random/wall-clock-driven
process) as the arena's PRIMARY judge does not ask the operator whether that's acceptable — it is
rejected by the guard, the same way a prune claim without `prune-guard.sh` evidence is rejected.

## Wiring: primary vs. secondary, in the runner

- `arenas/live/runner/eval.go`'s `scoreRecord` **always** computes `rec.Det` (the deterministic
  `scoring.Score`) via `scoreDeterministic` — regardless of whether the LLM-judge is enabled. This
  is the PRIMARY score.
- `main.go` adds `-llm-judge` (default **false**) as the flag that opts INTO the SECONDARY
  LLM-judge pass; `-skip-judge` is kept working (forces it off even if `-llm-judge` is set). The
  fair/deterministic result is now the default with no flag needed.
- `results.go`'s `## Correctness / quality` table (the primary table) renders the deterministic
  `pass/total` score; the LLM-judge's Correctness/Helpfulness/Faithfulness axes moved to their own
  `## LLM-as-judge — SECONDARY, opt-in` section, rendered only when `-llm-judge` actually ran, with
  a self-judge caveat repeated inline. The per-item detail table shows both: `Det` (primary,
  pass/fail + one-line reason) and `Corr/Help/Faith` (secondary, blank unless opted in).

## Collision fix (fairness): unique Qdrant collection per framework

A separate but adjacent fairness bug, fixed alongside this change: `arenas/live/runner/main.go`'s
`frameworkCollectionEnv` now sets a **unique** `QDRANT_COLLECTION` (and `ARENA_COLLECTION`, since
`llamaindex` reads that name instead) per framework — `arena_rag_<framework>` — appended after
`os.Environ()` so it always wins over any inherited value. Before this fix, 6 adapters (eino,
genkit-go, langgraph, llamaindex, ai-sdk, mastra) defaulted to the literal, shared `arena_rag`
collection, and two of them (genkit-go's Go `const`, pydantic-ai's Python default parameter) did
not even read the env var — a real cross-framework vector-bleed risk against one shared Qdrant
instance. Both were fixed to read `QDRANT_COLLECTION` (see `genkit-go/rag.go`
`ragCollectionName()`, `pydantic-ai/arena_common.py` `qdrant_search`'s env-derived default). The
aisotropy reference adapter is deliberately excluded — it self-ingests the Nimbus-Rail corpus into
its own tiered memory at runtime rather than reading a shared, pre-populated collection, so its
`aisotropy_arena` collection was already unique. `arenas/live/_shared/populate-qdrant.py` now seeds
`arena_rag_<fw>` for every discovered adapter by default (embedding the corpus once and reusing the
vectors across collections), keeping a single-collection legacy mode via an explicit
`ARENA_COLLECTION`/`QDRANT_COLLECTION` override for manual smoke-testing.

## Why this generalizes (code-truth, extended to score-truth)

The project's constitutional discipline is: **a claim about code (dead/live, tested/untested) is
verified against the code, never asserted from a doc read** (`prune-evidence-gate.md`). This
document is the same discipline applied to **quality claims**: a "framework A is better than
framework B" claim from the arena must be backed by a **pure, reproducible measurement**, never by
a same-family LLM's subjective, generously-biased opinion presented as the primary number. An
LLM-as-judge is a legitimate SECONDARY, clearly-labeled signal — it is not legitimate as the
un-caveated headline number.
