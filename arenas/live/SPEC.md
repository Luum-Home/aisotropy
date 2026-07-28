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

## Full-matrix coverage law (a standing premise)

Every arena artifact is covered across the **full cartesian matrix** — **model lane** (`stub` +
`real`) × **execution form** (`native` + `docker` + `wasm`-where-WASI-feasible) × **dimension** (every
mode the adapter implements). This is a **premise, not a per-task question**: an agent building for the
arena covers all feasible cells and **declares the infeasible ones with a reason** — a silently missing
cell is an arena defect (it reads as "covered" when it is not). Each adapter ships a `matrix.json`
manifest; the runner **fails the report if any cell is left blank** (`pending`), so completeness is
machine-checked. `wasm` is `infeasible` for every networked framework — but for a PER-FRAMEWORK reason
(toolchain or dependency tree), never "WASI has no sockets", which has been false since WASI 0.2
(January 2024). See MATRIX-SCHEMA.md. It is feasible
only for aisotropy's wazero tool-sandbox — that reason is the cell's content, not an omission. The full
enforcement law is `arenas/live/FULL-MATRIX.md` (version-controlled, authoritative); it is also
projected into `.claude/rules/arena-full-matrix.md` so in-repo agents auto-load it.

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
    {"name": "string", "arguments": { /* object */ },
     "result": "string — the EXECUTED tool's output; empty string when the call was
                only captured and not executed. MUST be a STRING (JSON-encode
                structured output); a number or object breaks the whole record's parse."}
  ],
  "error": "string — empty when the run succeeded"
}
```

Contract rules every adapter MUST honor:

1. **Provider-reported tokens only.** `input_tokens` / `output_tokens` come from the
   provider's `usage` for the call(s) this run made. If usage is unavailable, report
   **`-1`** (never a fabricated estimate).
2. **Tool-calling is MODE-DEPENDENT — read this carefully, it is the single most
   misimplemented rule in the contract.**
   - **`tools` mode: EXECUTE the tool for real.** The `tools` dimension scores REAL
     execution: the runner marks a call executed only when `tool_calls[].result` is a
     **non-empty STRING** (`runner/eval.go`: `Executed = TrimSpace(c.Result) != ""`,
     scored by `scoring.ScoreTools`). So run the tool body, feed the result back, and
     report `{name, arguments, result}` with the executed output JSON-encoded **as a
     string**. Emitting the value under any other field name (e.g. `output`), as a raw
     number, or omitting it entirely, scores `exec 0/3` even though your tool ran —
     this exact mistake shipped in four adapters (genkit-go, pydantic-ai, mastra,
     ai-sdk) because an earlier version of this section said "do NOT execute".
     If the request carries no `tools`, register your own calculator (see the
     arithmetic tasks) — do not error out.
   - **`qa` mode: single-shot capture.** Report the name and arguments the model
     produced with `result: ""` and do NOT execute — an empty `result` is how the
     scorer correctly records "captured, not executed".
3. **STDOUT is JSON-only.** Exactly one object, nothing else. Diagnostics go to STDERR
   (prefix `PROGRESS:` is conventional).
4. **`model` echoes `OPENAI_MODEL`** (`agnes-2.0-flash`) so a run can be audited —
   **except under `ARENA_STUB=1`, where it MUST report your OFFLINE stub's name**
   (e.g. `mock-llm`, `FakeListChatModel (ARENA_STUB)`, `arena-stub-echo`), never the
   real model. A stub record claiming `agnes-2.0-flash` is dishonest output and trips
   the runner's lane-leak guard; three adapters shipped this bug by following the
   earlier, unqualified wording of this rule.
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
| `arenas/live/_shared/mcp-server/` | Shared **extensibility** foundation: a real MCP stdio server every adapter can spawn — see below. |

## Task kinds in the suite

| kind | What it stresses | Graded by |
|---|---|---|
| `single_shot_qa` | Grounded general-knowledge correctness + faithfulness. | LLM judge vs. `reference`. |
| `multi_turn_memory` | Whether the framework carries a fact from an earlier turn. | LLM judge vs. `reference` (the fact must survive). |
| `tool_calling` | Native tool selection: right tool, right arguments, single-shot. | `expected_tool` / `expected_args` vs. captured `tool_calls`. |
| `token_efficiency` | Token spend for an equivalent answer across a multi-turn scenario. | Objective `input_tokens`+`output_tokens` (judge also checks the answer holds). |
| `safeguard_decline` / `safeguard_nocontain` | RUNNER-INTERNAL safeguard probes (off-topic decline, identity concealment, system-prompt exfiltration, corpus injection, jailbreak) — on the wire each is an ordinary `qa`/`rag` turn, so this adds ZERO adapter-contract change; opt-in via `-modes safeguard` (a default run pays zero extra calls). See `docs/architecture/arena-safeguard-dimension.md`. | Deterministic `scoring.ScoreDecline` / `scoring.ScoreNoContain` / `scoring.ScoreNoContainGrounded` (never LLM-judged). The grounded variant is used where the probe also carries a legitimate question — corpus injection — so that obeying the injection's "do not answer" instruction fails instead of passing on absence alone. |
| `memory_cross_session` / `memory_semantic_recall` / `memory_consolidation` | RUNNER-INTERNAL memory-extension probes. On the wire each is an ordinary `memory` turn plus one ADDITIVE, `omitempty` field — `user_id` (a SECOND scope axis alongside `session_id`): the runner VARIES `session_id` while HOLDING `user_id` constant across a task's 2–3 invocations, so a session-keyed store returns empty for the fresh recall session and only genuine user-scoped memory recalls. A single-mode adapter that ignores `user_id` still parses the request unchanged; the competitor adapters are taught to read it in a separate per-adapter phase (until then a cross-session probe RUNS AND FAILS for them, the honest verdict). Opt-in via `-modes memory_cross_session,…` (a default run pays zero extra calls). See `docs/architecture/arena-memory-extensions.md`. | Deterministic `scoring.ScoreMemory` (answer-key containment). |
| `isolation_forgetful` | RUNNER-INTERNAL "forgetful integrator" tenancy probe. MEASURES the L1-vs-L3 difference the `memory_scoped`/`isolation_leak` ✓ only ASSERTS: a fact is written under an explicit scope (wire-mode `memory`, a unique-per-task `user_id`), then read back with the scope OMITTED (a second invocation, wire-mode `isolation_forgetful`, `user_id` blank — the exact real integrator mistake). The distinct recall wire mode forces the omission to happen at the framework's OWN API boundary, not adapter glue (fairness — the adapter forwards the blank scope RAW; aisotropy's `memory.Key.Validate` refuses before any model call). Three deterministic verdicts: **refuses** (enforcement → PASS) / **leak** (returned the scoped data → FAIL) / **silent_partition** (a different/empty scope, no error, no data — L1's invisible failure → FAIL). L0 frameworks (AI SDK, Pydantic AI, Eino, Genkit-Go) are `infeasible` (no native scope to forget); the L1 competitors are wired in a separate per-adapter phase (until then RUN-AND-FAIL, the honest verdict). Opt-in via `-modes isolation_forgetful` (a default run pays zero extra calls). See `docs/architecture/arena-tenancy-levels.md` §"forgetful integrator". | Deterministic `scoring.ScoreForgetful` (three-way refuses/leak/silent_partition verdict; never LLM-judged). |

## Extensibility dimension — MCP tool loading (`_shared/mcp-server/`)

A new arena dimension, alongside the per-adapter `mode` dispatch pattern used by
`rag`/`tools`/`memory`/`structured`/`stream`/`memory_scoped`/`isolation_leak`/
`crash_recovery`/`observability` (see e.g. `arenas/live/langgraph/modes/` or
`arenas/live/aisotropy/README.md` §Modes for that pattern): **can a framework load an
external extension at runtime and actually use it?**

[Model Context Protocol](https://modelcontextprotocol.io) is the emerging
cross-framework standard for exactly this, so it is the fair, comparable
extensibility test. `arenas/live/_shared/mcp-server/` provides the **shared
foundation** every adapter builds on — a real, minimal MCP stdio server (official
`@modelcontextprotocol/sdk`, MIT) exposing one deterministic tool,
`nimbus_status(line: string)`, tied to the existing Nimbus Rail corpus theme
(`_shared/corpus/`). An `extensibility`-mode adapter spawns this server, connects as
an MCP client using the framework's own MCP integration (LangGraph →
`langchain-mcp-adapters`, LlamaIndex → `llama-index-tools-mcp`, Mastra → `@mastra/mcp`,
AI SDK → `experimental_createMCPClient`, Genkit → its MCP plugin, Eino →
`eino-ext/components/tool/mcp`, aisotropy → native MCP consumption or its declarative
`aisotropy-plugin.yaml` manifest), lists the tools it discovers, invokes one inside a
real agent turn against `agnes-2.0-flash`, and reports whether the tool's result was
actually reflected in the final reply.

The full contract — stdout envelope, the framework→MCP-client mapping table, and the
PASS/FAIL scoring rule (`extension_loaded && tool_invoked && tool_result_used`) — is
specified in `arenas/live/_shared/mcp-server/README.md` §2, so it is documented once
and referenced rather than duplicated per adapter. Implementing the `extensibility`
mode in each framework adapter is follow-up work; this shared server and its
`smoke.sh` handshake proof are the foundation it builds on.
