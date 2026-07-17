---
type: Spec
title: "Live arena — one real model, many frameworks (agnes-2.0-flash held constant)"
description: The executable counterpart to arenas/BENCHMARKS.md. Where the stub arena holds the model constant as a zero-latency echo to isolate framework OVERHEAD, this arena holds the model constant as the REAL agnes-2.0-flash so the differences it measures are answer quality, end-to-end latency, token efficiency, tool-calling, and multi-turn memory — the things a keyless stub can never measure. Every framework adapter honors one uniform stdin/stdout contract, so the runner compares them apples-to-apples.
tags: [arenas, live, benchmarks, agnes, quality, latency, tokens, tool-calling, memory, llm-as-judge]
---

# Live arena — the model is real and held constant

> **The stub arena's question was:** *how much time does the framework add around a
> model call?* (model = zero-latency stub, every nanosecond is the framework).
> **This arena's question is:** *given the SAME real model, how well does each
> framework use it?* — answer quality, end-to-end latency, token spend, tool-calling,
> and multi-turn memory. Same model → measured differences are the **framework**, not
> the model.

## The thesis: hold the model constant, make it REAL

`arenas/BENCHMARKS.md` is explicit that its cross-framework quality numbers are
**"documented, NOT executed — because no key"**. It holds the model constant as
`internal/llm.StubClient` (`Delay ≈ 1 ns`, a deterministic echo) so that, with model
latency removed, what remains is pure framework overhead. That discipline is correct
for measuring *overhead* — and it is structurally unable to measure anything that
depends on a real model's outputs.

This arena removes exactly that limitation and keeps the rest of the discipline. The
model is held constant as **agnes-2.0-flash**, reached over its OpenAI-compatible
endpoint (chat, SSE streaming, native `tool_calls` — all standard). Every framework in
the arena calls the *same* model with the *same* credentials via the `OPENAI_*`
environment. Because the model is identical for every contestant, a difference in the
measured output is attributable to the framework's orchestration — its prompt
assembly, its memory handling, its structured-output enforcement, its tool plumbing —
not to a stronger or weaker model.

## What this measures that the stub arena could not

| Dimension | Why the stub could not measure it | How the live arena measures it |
|---|---|---|
| **Answer correctness** | The stub echoes a canned reply; there is no "right answer" to grade. | Real agnes answers grounded QA; an LLM judge scores correctness 1–5 against a reference. |
| **Faithfulness** | No real generation → nothing can be unfaithful. | Judge scores whether the reply stays grounded (no invented facts) 1–5. |
| **End-to-end latency** | Stub `Delay ≈ 1 ns` — latency is framework-only, model contributes zero. | Wall-clock of the whole adapter run, including the real model round-trip(s). |
| **Token efficiency** | The stub reports no provider usage; token counts are synthetic. | `input_tokens`/`output_tokens` come from the **provider's reported usage** for every call. |
| **Tool-calling** | The stub never emits a native `tool_call`. | A real tool spec is offered; the model's native `tool_calls` (name + arguments) are captured (single-shot, NOT executed). |
| **Multi-turn memory** | The stub has no context to carry. | A later turn depends on a fact stated in an earlier turn; the framework must carry it. |

The two arenas are complementary, not redundant: **overhead** (stub) and
**effectiveness with a real model** (live). Neither replaces the other.

## The self-judge caveat (stated up front)

The grader is an **LLM-as-judge** running on **the same model family** as the
contestants (agnes-2.0-flash judging agnes-2.0-flash outputs). This is a known bias: a
model tends to rate outputs from its own family generously, and cannot catch errors it
would itself make. The judge scores are therefore a **relative, directional** signal
for comparing frameworks under identical conditions — **not** an absolute measure of
truth. `RESULTS.md` repeats this caveat next to the scores. Latency and token counts
are objective measurements and carry no such caveat.

---

## The uniform adapter contract

Every framework adapter is a runnable command at `arenas/live/<fw>/run.sh`. The runner
discovers adapters by globbing `arenas/live/*/run.sh`, so adding a framework is purely
additive — no runner change.

An adapter reads **exactly one JSON object from STDIN**:

```json
{
  "system": "string — the system prompt",
  "messages": [
    {"role": "user" | "assistant", "content": "string"}
  ],
  "tools": [
    {"name": "string", "description": "string", "parameters": { /* JSON-Schema object */ }}
  ]
}
```

- `tools` is **optional** (absent or empty means a plain chat turn).
- `messages` is the conversation so far; the final `user` message is the one to answer.

It runs its framework **once** against agnes-2.0-flash using the `OPENAI_*` env
(`OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL`) and writes **exactly one JSON
object to STDOUT** (only that — all logs go to STDERR):

```json
{
  "framework": "string",
  "model": "agnes-2.0-flash",
  "reply": "string — the assistant's final text answer",
  "input_tokens": 0,
  "output_tokens": 0,
  "latency_ms": 0,
  "tool_calls": [
    {"name": "string", "arguments": { /* object */ }}
  ],
  "error": "string — empty when the run succeeded"
}
```

Contract rules every adapter MUST honor:

1. **Provider-reported tokens only.** `input_tokens` / `output_tokens` come from the
   provider's `usage` for the call(s) this run made. If usage is unavailable, report
   **`-1`** (never a fabricated estimate).
2. **Single-shot tool-calling.** When `tools` are present and the model chooses to call
   one, populate `tool_calls` with the name and arguments the model produced and **do
   NOT execute** the tool (no second round-trip, no tool body runs).
3. **STDOUT is JSON-only.** Exactly one object, nothing else. Diagnostics go to STDERR
   (prefix `PROGRESS:` is conventional).
4. **`model` echoes `OPENAI_MODEL`** (`agnes-2.0-flash`) so a run can be audited.
5. **Errors are data, not crashes.** On failure, emit the JSON with `error` set and
   tokens `-1`; exit so the runner records the row rather than losing the task.

### Multi-turn: framework-native, faithfully

A multi-turn task gives the whole `messages` history. Each framework carries context
its own way, and that *is* the thing under test:

- Frameworks that accept a message list (most) replay the full history in **one** model
  call — their `input_tokens` count that history once.
- **aisotropy** is a per-turn, memory-first runtime: its public `App.Turn` takes one
  prompt plus a `SessionID` and carries earlier turns through its working-memory tier.
  The reference adapter therefore **replays each user turn** through `App.Turn` on a
  shared session and **sums** the provider usage across those turns. That summed number
  is aisotropy's real multi-turn cost — its memory is re-hydrated per turn by design —
  and comparing it to a single-call framework is exactly the kind of architectural
  difference this arena exists to surface. `RESULTS.md` labels this explicitly.

---

## How to add a new adapter

1. Create `arenas/live/<fw>/` with whatever the framework needs (its own `go.mod`,
   `package.json`, or venv — keep it self-contained, do not touch the repo root).
2. Implement the STDIN→STDOUT contract above. Read the model config from `OPENAI_*`.
   Report **provider** token usage (or `-1`). Capture native `tool_calls` single-shot.
3. Provide `arenas/live/<fw>/run.sh` that:
   - sources the repo-root `.env` (`set -a; source .env; set +a`) so `OPENAI_*` is set,
   - never prints/echoes/logs the key,
   - pipes STDIN through to the adapter and lets it own STDOUT.
4. That's it — the runner auto-discovers `run.sh` and includes the framework on the
   next `go run .` in `arenas/live/runner/`. Nothing else changes.

### Licensing

Adapters and their dependencies stay **MIT / BSD / Apache-2.0** only (the repo's
constitutional license gate). No GPL/AGPL/SSPL/BSL.

---

## Files in this arena

| Path | Role |
|---|---|
| `arenas/live/SPEC.md` | This document — thesis + contract. |
| `arenas/live/tasks/suite.jsonl` | One task per line: `id`, `kind`, contract input, and (for gradable kinds) a `reference`. |
| `arenas/live/<fw>/run.sh` | A framework adapter honoring the contract. |
| `arenas/live/runner/` | Discovers adapters, runs the suite, LLM-judges the replies, writes `RESULTS.md`. |
| `arenas/live/RESULTS.md` | Generated: per-framework × per-dimension table + measured token spend. |

## Task kinds in the suite

| kind | What it stresses | Graded by |
|---|---|---|
| `single_shot_qa` | Grounded general-knowledge correctness + faithfulness. | LLM judge vs. `reference`. |
| `multi_turn_memory` | Whether the framework carries a fact from an earlier turn. | LLM judge vs. `reference` (the fact must survive). |
| `tool_calling` | Native tool selection: right tool, right arguments, single-shot. | `expected_tool` / `expected_args` vs. captured `tool_calls`. |
| `token_efficiency` | Token spend for an equivalent answer across a multi-turn scenario. | Objective `input_tokens`+`output_tokens` (judge also checks the answer holds). |
