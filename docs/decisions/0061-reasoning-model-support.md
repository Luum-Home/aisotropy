---
type: ADR
title: "ADR-0061 — Reasoning-model support: a Reasoning channel on the token/message model, stateless intra-task carry-forward of the thinking block, and a consolidate-then-discard cost-governance lifecycle"
description: >
  Aisotropy's LLM seam is TEXT-ONLY today: internal/llm.Token carries Text/Done/Err/Usage
  (internal/llm/llm.go:45-62) and every reader appends only .Text, so reasoning ("thinking")
  tokens emitted by reasoning models (Claude extended thinking, OpenAI o-series, DeepSeek-R1,
  Qwen QwQ) are silently dropped: AnthropicClient emits only "text_delta" events
  (internal/llm/anthropic.go:112-117) and OpenAICompatClient reads only choices[].delta.content
  (internal/llm/openai_compat.go:220-242). This ADR designs first-class reasoning handling that
  fits aisotropy's provider-agnostic, Chat-Completions-only, cost-governed, tenant-isolated
  design WITHOUT breaking existing text-only consumers. Three pieces: (1) an additive Reasoning
  delta field on Token plus a ReasoningTokens count on Usage — a separate field, not a token-kind
  enum, so no existing .Text consumer branches; (2) stateless intra-task carry-forward of the
  reasoning block across tool-call steps via an opaque, provider-native, request-local
  ReasoningState threaded through agentloop.Run / RunToolAgent — re-sent each step (NOT
  reference-by-ID), which satisfies Anthropic's hard "return the signed thinking block alongside
  tool_result" requirement while degrading to a no-op on gateways that drop reasoning
  (agnes-2.0-flash); (3) a lifecycle that keeps reasoning EPHEMERAL in working memory within the
  task and CONSOLIDATES/DISCARDS it at task close (never persisted raw into the summary buffer,
  never exported to the sleep-cycle / semantic / facts tiers), with reasoning tokens counted by
  the cost meter, gated by the pre-spend quota gate, and degraded (reasoning_effort ladder) under
  quota pressure. STATUS: accepted — design implemented in full (see Implementation Plan below);
  all code landed in the ADR-0061 worktree, tests green, tenant-isolation verified under -race.
tags: [decisions, adr, llm, reasoning, chain-of-thought, extended-thinking, provider-agnostic, cost-governance, tenant-isolation, agent-loop, memory-tiers, accepted]
timestamp: 2026-07-23T00:00:00Z
---

# ADR-0061 — Reasoning-model support: a `Reasoning` channel on the token/message model, stateless intra-task carry-forward of the thinking block, and a consolidate-then-discard cost-governance lifecycle

## Status

✅ **Accepted — design implemented (2026-07-23).** All code from the implementation plan below has
been implemented and tested in the ADR-0061 worktree (`agent-a9bcfcf3abe979e30`). The reasoning
channel is OFF by default (`AISOTROPY_REASONING` unset) — with the flag off, `go build ./... &&
go test ./...` passes with zero behavior change, proving backward compatibility. With the flag on,
reasoning streams on `Token.Reasoning`, the loop reinjects prior reasoning natively across steps,
Anthropic signed thinking blocks round-trip, and a no-reasoning endpoint is a verified no-op.
Tenant-isolation test passes under `-race`. Cost meter reports `ReasoningTokens` per tenant.
This ADR builds directly on the provider-agnostic `Client` seam ([ADR-0009](0009-provider-agnostic-llm-layer.md)), the native
tool-calling `ToolCaller` capability ([ADR-0012](0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)),
the Gollem reflection loop ([ADR-0008](0008-reflection-loop-gollem-deferral.md),
[ADR-0010](0010-gollem-adoption-scope-and-observability.md)), and the granular-control cost axis
(Phase 7) — extending all of them additively rather than reshaping any.

> **ADR-number caveat (per the task):** an ADR for multi-agent graph orchestration
> ([0060](0060-multi-agent-graph-orchestration.md)) already occupies 0060 and a further one is
> being drafted concurrently. This file claims the next free number by listing `docs/decisions/`
> (0061). If it collides at merge, the orchestrator renumbers; this ADR does **not** edit
> `docs/decisions/index.md`.

## Context

### What exists today (code-truth)

Aisotropy's LLM abstraction is a single narrow streaming method and a flat token struct:

- **`Token` is text-only.** `internal/llm/llm.go:45-62`: `Token{ Text string; Done bool; Err error;
  Usage *Usage }`. There is **no `Reasoning` field**. `Usage` (`llm.go:38-43`) is
  `{ InputTokens int; OutputTokens int }` — no reasoning-token line.
- **`Client` is `StreamChat(ctx, system, prompt) (<-chan Token, error)` + `Name()`**
  (`llm.go:73-81`). `ToolCaller` (optional, discovered by type assertion) adds
  `CallTools(ctx, system, prompt string, tools []ToolSpec) (ToolCallResult, error)`
  (`internal/llm/tools.go:86-94`) — **single-prompt, no message history, no reasoning**.
- **Anthropic drops thinking.** `AnthropicClient.StreamChat` builds `MessageNewParams` with only
  the user text block + optional system (`internal/llm/anthropic.go:50-59`); there is **no
  `Thinking` request param**. The event demux emits **only `content_block_delta` of type
  `text_delta`** (`anthropic.go:112-117`); a `thinking_delta` / `thinking` block would hit the
  `default: return "", false` arm (`anthropic.go:118-120`) and be discarded. `maxTokens` is
  hardcoded to `1024` (`anthropic.go:34`). Critically, the code **never returns a thinking block
  back** on a subsequent call — which Anthropic's extended-thinking-with-tools contract *requires*.
- **OpenAI-compat drops reasoning.** `applyStreamChunk` decodes only `choices[].delta.content`
  (`internal/llm/openai_compat.go:220-242`); it does not read `delta.reasoning`,
  `delta.reasoning_content`, or `provider_specific_fields.reasoning`. `CallTools` is a single
  non-streaming request (`openai_compat.go:256-280`) returning `ToolCallResult{Calls, Message}`
  with no reasoning slot. (Per the task: some gateways expose `reasoning` in a provider field;
  agnes-2.0-flash returns it `null`.)
- **The agent loop carries results, not reasoning.** The homegrown structured-output loop
  `agentloop.Loop.Run` (`internal/agentloop/agentloop.go:132-155`) accumulates `current` = prompt
  + tool RESULTS across steps by string-appending
  `fmt.Sprintf("%s\n\nTool %q returned:\n%s\n\nContinue.", current, dec.Tool, result)`
  (`agentloop.go:152`). Nothing carries the model's *reasoning* between steps. The Gollem path
  flattens history identically: `buildGollemPrompt` (`internal/llm/gollem.go:302-336`) folds
  `SystemPromptPart` / `UserPromptPart` / `RetryPromptPart` / `ToolReturnPart` / prior
  `ModelResponse` into one `(system, prompt)` text pair — and the package comment already flags
  that tool RESULTS reach the model **flattened inside `prompt`**, not as native `role:"tool"`
  messages (`gollem.go:70-73`), because `ToolCaller.CallTools` takes only `(system, prompt)`. The
  native tool agent `RunToolAgent` (`internal/llm/toolagent.go:65-143`) has the same shape,
  bounded by `core.MaxTurns(maxSteps)`.
- **The persisted turn is text-only.** `memory.Message` = `{ Role, Content, Timestamp }`
  (`internal/memory/message.go:6-14`). Working memory persists it and condenses evicted messages
  into a summary via `Summarizer.Condense(ctx, existingSummary, oldest []Message)`
  (`internal/memory/summarizer.go:18-24`; wired at `working_memory.go:491-538`
  `condenseAndPrune`). The cold path exports `sleepcycle.Record{ ID, TenantID, UserID, Content,
  Importance, CreatedAt, UpdatedAt }` (`internal/sleepcycle/record.go`) to the DataFusion worker,
  and `consolidation.Sink.Consolidate` (`internal/consolidation/sink.go:44`) fans an outbox event
  into facts + semantic tiers. **None of these have a reasoning field** — which is the correct
  end state; the design must keep it that way.

### The cost-governance gates reasoning must respect (code-truth)

- **Pre-spend quota gate.** `memory.Limiter.Allow(ctx, tenant)` (`internal/memory/quota.go:74-115`)
  is a per-tenant **token BUCKET** — it takes **one bucket-token per REQUEST**, not per
  LLM-token. It is called *before* the LLM call (`internal/api/chat.go:214`), satisfying the
  constitutional "reject/degrade before spending" gate. It does **not** currently meter LLM-token
  *volume*.
- **Cost meter.** `costmeter.Meter.Record(tenant, inputTokens, outputTokens, costUSD)`
  (`internal/costmeter/costmeter.go:63-87`) accumulates `TenantStats{ Requests, InputTokens,
  OutputTokens, CostUSD }` per tenant; there is **no `ReasoningTokens`** line. It is fed on the
  reflect path by `chatRecordTurn` (`internal/api/chat.go:290-302`) and on the streaming path by
  `RegisterStream` (`internal/api/chat_stream.go:195`). `EstimateCostUSD` prices per-1M
  (`costmeter.go:132-136`).
- **Circuit breaker.** `WithCircuitBreaker` (`internal/llm/breaker.go:48-60`) trips on repeated
  `StreamChat` / `CallTools` start failures and preserves the `ToolCaller` capability through the
  decorator.
- **Cheap-model degrade.** `DegradeAt(pressure, threshold, primary, cheap)`
  (`internal/llm/degrade.go:39-47`) routes to a cheap model when `pressure >= 0.95`. Applied on
  the streaming route (`chat_stream.go:179`).
- **No response cache exists yet** in `internal/llm` (a repo grep for a cache in that package
  returns nothing) — so "respect the response cache" is a *future* hook point, not an existing
  component, and this ADR treats it as such.

### The constitutional invariants (`.claude/rules/constitutional-gates.md`)

- **Tenant isolation via `context.Context`.** No path may hold two tenants' data in one in-memory
  buffer/goroutine scope without a reviewed reason.
- **Quota BEFORE spend.** Reject/degrade before the LLM call.
- **Fail-closed structured output.** A schema violation triggers the bounded reflection loop
  (`internal/llm/reflect.go`, `reflect_auto.go`), never a silent unstructured fallback.
- **Secrets/PII never logged.** `gollemAuditLog` logs only message *count* (`gollem.go:542`);
  `RedactPII` scrubs the prompt before the model/audit see it (`gollem.go:490`). SSE must respect
  ctx cancellation as the spend cutoff.

### The problem

Reasoning models spend a large, separately-billed budget on hidden chain-of-thought before the
answer. Aisotropy can neither (a) **surface** that reasoning (thinking UI, debugging, eval), nor
(b) **preserve** it across tool-call steps so the model does not lose its train of thought
mid-task, nor (c) **exploit** Anthropic extended thinking / o-series reasoning at all — because
the token model has no channel for it and the agent loop has nowhere to put it. And doing (b)
naively is expensive: reasoning re-sent each step multiplies token cost, which must not escape the
cost-governance gates.

## Decision

Add reasoning as a **first-class but strictly ephemeral** dimension: a Reasoning channel on the
token/message model, a stateless intra-task carry-forward, and a consolidate-then-discard
lifecycle — all additive, all behind a config flag, all no-op on non-reasoning endpoints.

### Piece 1 — A `Reasoning` channel on the token/message model

**1a. `Token.Reasoning` — a separate delta field, not a token-kind enum.**

```go
// internal/llm/llm.go — additive; existing readers untouched.
type Token struct {
    Text      string // unchanged: answer-text delta
    Reasoning string // NEW: reasoning/"thinking" delta (not accumulated). "" on
                     // non-reasoning providers and on every answer-text token.
    Done      bool
    Err       error
    Usage     *Usage
}
```

Rationale for a **separate field** over a `Kind`/`Channel` enum: **every existing consumer reads
`tok.Text`** — `collectText` (`agentloop.go:198`), `generateFull` (`gollem.go:292`),
`gollemStream.Next` (`gollem.go:244`), `LLMSummarizer.Condense` (`summarizer.go:157`). A separate
field is invisible to all of them (they keep appending `.Text`; `.Reasoning` is simply ignored),
so the change is **strictly backward-compatible with zero consumer edits**. A `Kind` enum would
force every one of those switch/append sites to branch or risk mixing reasoning into the answer.
The terminal-token contract is unchanged: `Done` / `Err` / `Usage` still land only on the final
token; a reasoning delta is a mid-stream token exactly like a text delta.

**1b. `Usage.ReasoningTokens` — a separate cost line.**

```go
type Usage struct {
    InputTokens     int
    OutputTokens    int
    ReasoningTokens int // NEW: provider-reported reasoning tokens (OpenAI
                        // completion_tokens_details.reasoning_tokens; Anthropic
                        // counts thinking inside output_tokens — see mapping).
}
```

Kept **separate from `OutputTokens`** for cost *visibility* (you must be able to see how much a
tenant spent on hidden reasoning). Whether a provider double-counts (Anthropic bills thinking
*inside* `output_tokens`; OpenAI reports reasoning as a *subset* detail of completion tokens) is
handled in the provider mapping so the cost meter never double-charges — see 1c.

**1c. Provider shape mapping (graceful degradation is the default).**

| Provider / shape | Where reasoning appears | `Token.Reasoning` source | `Usage.ReasoningTokens` | Carry-back requirement |
|---|---|---|---|---|
| **Anthropic extended thinking** | `content_block_delta` of type `thinking_delta`; a `thinking` block with a cryptographic `signature`; possibly `redacted_thinking` (encrypted, opaque) | map the `thinking_delta` arm in `anthropicEventText` (`anthropic.go:112-120`) → `Token{Reasoning: delta.Thinking}` | thinking is billed inside `output_tokens`; expose the thinking portion as `ReasoningTokens` for visibility, do **not** add it to `OutputTokens` again | **HARD**: the *signed* `thinking` block MUST be returned verbatim as a `thinking` content block alongside the `tool_result` on the next turn or Anthropic rejects/ignores continued reasoning. Requires native re-attach — see Piece 2. |
| **OpenAI o-series** | `reasoning_effort` request param; reasoning is hidden; API returns `completion_tokens_details.reasoning_tokens`; optional reasoning *summary* | if a reasoning summary streams, map it → `Token.Reasoning`; else reasoning is countable-but-not-visible | read `reasoning_tokens` directly | **NONE** (stateless per call; no signed block to return). Reinjecting a prior reasoning summary is optional context, not a protocol requirement. |
| **Chat-Completions gateway exposing `reasoning`** (some OpenRouter routes, DeepSeek-R1 `reasoning_content`, Qwen QwQ) | `choices[].delta.reasoning` or `.reasoning_content`, or `provider_specific_fields.reasoning` | add these keys to `applyStreamChunk` (`openai_compat.go:220-242`) → `Token{Reasoning: ...}` | from `usage.completion_tokens_details.reasoning_tokens` when present, else 0 | **NONE** (stateless). Optional text reinjection only. |
| **No-reasoning endpoint (agnes-2.0-flash)** | field absent / `null` | nothing emitted; `Token.Reasoning` stays `""` | 0 | **NONE**. |

The last row is the load-bearing one: **on any endpoint that does not emit reasoning, every part
of this design is a structural no-op** — `Token.Reasoning` is `""`, the carry-forward state is
empty, cost lines are 0, and the loop behaves byte-for-byte as it does today. This is verified by a
graceful-degradation test (Piece "Tests").

**1d. The persisted turn stays text-only.** `memory.Message` (`message.go:6-14`) is **not**
extended in v1. Reasoning is never written to BadgerDB in the default configuration (see Piece 3
lifecycle for why: task-close discard is the whole point). A debug-only, flag-gated ephemeral
`Reasoning` field on the in-loop turn is permitted, but it is **hard-stripped** before
`Summarizer.Condense` and never reaches `sleepcycle.Record` / `consolidation.Sink`. The safest
default — and the recommended one — is that raw reasoning lives **only in Go request-local memory**
for the duration of the loop and is garbage-collected when the loop returns.

### Piece 2 — Agent-loop carry-forward (stateless reinjection, provider-native)

**The tension.** Intra-task CoT coherence requires the model to see its own prior reasoning at each
step. Aisotropy is **Chat-Completions-only / stateless** (`ToolCaller.CallTools` takes only
`(system, prompt)`, `tools.go:93`; the Gollem adapter flattens history to text, `gollem.go:70-73`).
So carry-forward must be **stateless reinjection** — re-send the reasoning each step — NOT
reference-by-ID (the Responses API `previous_response_id` path is an explicit non-goal). But
Anthropic's requirement is stricter than "re-send as text": the *signed* thinking block must be
returned as a **native `thinking` content block**; flattening it into `current` as plain text
(the way `agentloop.go:152` handles tool results) **loses the signature** and Anthropic will not
accept it as continued reasoning.

**The mechanism: an opaque, request-local `ReasoningState` threaded through the loop.**

```go
// internal/llm/tools.go (or a new reasoning.go) — provider-agnostic, opaque.
// It is a VALUE carried on the goroutine's call stack (like agentloop's `current`
// local, agentloop.go:134) — never a shared map, never keyed by tenant, so two
// tenants' loops are structurally incapable of sharing it.
type ReasoningState struct {
    // Blocks holds the provider-native carry for the NEXT step. For Anthropic
    // these are the signed thinking / redacted_thinking blocks to re-attach
    // verbatim; for OpenAI-compat this is nil or an optional reasoning-summary
    // string. Opaque to the loop: only the producing Client interprets it.
    Blocks []ReasoningBlock
}

// Optional capability, discovered by type assertion exactly like ToolCaller
// (tools.go:86). A Client that does not implement it → reasoning is simply not
// carried between steps (graceful degradation: works, loses cross-step CoT).
type ReasoningToolCaller interface {
    ToolCaller
    // CallToolsReasoning is CallTools plus a prior ReasoningState in and a fresh
    // one out. The Anthropic impl re-attaches prior.Blocks as native `thinking`
    // content blocks in MessageNewParams (satisfying the signature requirement)
    // and returns the new signed blocks; the OpenAI-compat impl treats prior as
    // optional context and returns whatever reasoning it streamed.
    CallToolsReasoning(ctx context.Context, system, prompt string,
        tools []ToolSpec, prior ReasoningState) (ToolCallResult, ReasoningState, error)
}
```

**Threading through the loops.** Both `agentloop.Loop.Run` (`agentloop.go:132-155`) and
`RunToolAgent` (`toolagent.go:65-143`) gain a request-local `reasoning ReasoningState` accumulator
alongside the existing `current`/steps state:

- If the client implements `ReasoningToolCaller`, each step passes the prior `reasoning` in and
  stores the returned one out — **stateless reinjection, provider-native**.
- If it does not, the loop calls the plain `CallTools` / `StreamChat` path exactly as today, and
  `reasoning` stays empty — **no-op degradation**.
- The reasoning state is a **local variable on the request's goroutine**, mirroring `current`
  (`agentloop.go:134`). It is never placed in a package-level map, never keyed by tenant, and dies
  when the loop returns — this is the structural tenant-isolation guarantee (Piece "Invariants").

**Token cost is explicit.** Stateless reinjection means the signed thinking blocks (Anthropic) or
reasoning summary (others) are re-sent as **input** on every step, so per-step input grows with the
accumulated reasoning. This is the price of statelessness (vs reference-by-ID) and is **paid for by
Piece 3's cost governance**, not hidden.

### Piece 3 — Cost-governance lifecycle: preserve within the task, consolidate/discard at task close

**Where reasoning lives, and when it dies:**

```
request arrives
  └─ quota.Allow (pre-spend gate, chat.go:214) — UNCHANGED, still one bucket-token/request
       └─ agent loop starts: reasoning ReasoningState = {} (request-local, on the goroutine)
            ├─ step 1: model reasons → Token.Reasoning streamed to client (display) +
            │          signed block captured into ReasoningState
            ├─ step 2..N: prior ReasoningState reinjected (native) → tool call → result appended
            │          (each step: Usage.ReasoningTokens metered into costmeter)
            └─ final structured answer (fail-closed reflection unchanged)
  └─ task close = loop returns:
       ├─ ReasoningState goes out of scope → reasoning is GARBAGE-COLLECTED (never persisted)
       ├─ ONLY the final answer text is persisted: working_memory.Append(Message{Content: answer})
       ├─ eviction → Summarizer.Condense sees text-only Messages (summarizer.go:56/124) — reasoning
       │  was never in them, so the summary buffer never contains raw CoT
       └─ cold path → sleepcycle.Record / consolidation.Sink export text only (record.go has no
          reasoning field — kept that way): raw CoT NEVER reaches long-term/semantic/facts memory
```

This is the **consolidate-then-discard** recommendation: reasoning is a *scratchpad* that improves
answer quality *within* a task and is thrown away at task boundary. The "consolidation" is implicit
— the value of the reasoning is already baked into the final answer that *is* persisted and
summarized; the raw CoT itself is noise for long-term memory (and a privacy liability), so it is
discarded, not stored.

**How the cost gates account for it:**

1. **Cost meter counts reasoning.** Add `ReasoningTokens int64` to `costmeter.TenantStats` and a
   parameter to `Meter.Record` (`costmeter.go:63`). Price reasoning at the provider's output-token
   rate via `EstimateCostUSD`. Feed it from `Usage.ReasoningTokens` at `chatRecordTurn`
   (`chat.go:290-302`) and the streaming record path. Result: a tenant's reasoning spend is
   *visible* in the `/costs` view, not hidden inside output tokens.
2. **Pre-spend gate stays "before spend."** The existing `quota.Allow` request-bucket gate
   (`quota.go`) is unchanged and still fires before the first LLM call. Because reinjection grows
   the *prompt*, its cost is already visible to any prompt-size ceiling — the
   `gollemMaxPromptChars = 100_000` input guardrail (`gollem.go:480`) counts reinjected reasoning
   toward the cap and aborts a runaway loop. **Recommended addition (optional, flag-gated):** a
   per-tenant *token-volume* budget checked before each *step* (not just each request), so a
   multi-step reasoning loop that would blow a tenant's budget is degraded mid-loop rather than
   after. This is additive to the request-rate bucket, not a replacement.
3. **Reasoning-effort ladder under quota pressure.** Extend the degrade policy (`degrade.go`) so
   that *before* dropping to the cheap model, the system first lowers `reasoning_effort`
   (`high → medium → low → off`) as `Limiter.Pressure` rises. Reasoning is the most expensive and
   most compressible lever, so it is the first thing shed under pressure:

   | pressure | policy |
   |---|---|
   | `< 0.80` | primary model, configured `reasoning_effort` |
   | `0.80–0.95` | primary model, `reasoning_effort` stepped down one notch |
   | `>= 0.95` | `DegradeAt` cheap model (existing behavior, `degrade.go:43`), reasoning off |

4. **Response cache (future hook).** No cache exists in `internal/llm` today. When one is added,
   its key MUST include the reasoning-inclusive prompt and MUST be tenant-scoped (a cache shared
   across tenants would be an isolation leak). This ADR only reserves the constraint; it does not
   build the cache.

**Eval / benchmark: measure the reinjection tradeoff.** The whole point of preserving reasoning is
a quality gain that must justify its cost. `internal/eval` already has the machinery:
`LLMJudge`/`CodeJudge` (`internal/eval/judge.go:53,189`) score answer quality, and
`arenas/live` + `arenas/COST-OBS.md` are the cost-observability harness. Design a matched-pair
benchmark: run the same task suite **with reinjection ON vs OFF**, and report per suite:

- **quality**: `LLMJudge` score delta (answer correctness with vs without carried CoT);
- **cost**: `Usage.ReasoningTokens` + input-token growth per step (from `costmeter`);
- **latency**: TTFT / total.

The deliverable is a cost/quality table that says "reinjection buys +X quality for +Y tokens on
task class Z" — turning the on/off flag into an *informed* per-deployment (or per-tenant) decision.
(The `internal/eval` hooks are in scope for the implementation plan; `arenas/live` is the
*measurement target* and is **not edited by this ADR's author** — another agent owns `arenas/`.)

### Constitutional invariants (how each is preserved)

- **Tenant isolation (hard gate).** `ReasoningState` is a **request-local value on the loop's
  goroutine**, exactly like `agentloop`'s `current` local (`agentloop.go:134`). It is never a
  package-level or shared map, never keyed by anything, and is GC'd at loop return. Tenant identity
  continues to travel via `ctx`. A dedicated test asserts two concurrent tenants' loops never share
  a reasoning byte (Piece "Tests"). Because reasoning is never persisted, there is also no
  cross-tenant risk in BadgerDB / rqlite / Qdrant.
- **Never logged.** Reasoning content joins the "content never logged" rule: `gollemAuditLog`
  keeps logging only counts (`gollem.go:542`); no new `slog` site emits `Token.Reasoning` or the
  thinking blocks. `redacted_thinking` (Anthropic) is opaque/encrypted and is passed back verbatim,
  never inspected or logged. A grep-based test asserts no reasoning reaches a log sink.
- **Quota BEFORE spend.** Unchanged: `quota.Allow` still gates before the first LLM call
  (`chat.go:214`). Reasoning cost is metered after the fact (visibility) and, optionally,
  budget-checked per step before subsequent spend.
- **Fail-closed structured output.** Unchanged. Reasoning is auxiliary; the final answer still
  validates against the Go struct via `ReflectAuto` (`reflect_auto.go:62`) and triggers the bounded
  reflection loop on a schema break. Reasoning tokens are not part of the structured contract.
- **SSE ctx cancellation = spend cutoff.** Unchanged. A cancelled request stops reasoning and text
  streaming identically (the reasoning delta path uses the same `select { case <-ctx.Done() }`
  send as text, `anthropic.go:82`, `openai_compat.go:426`).

## Alternatives considered

- **A `Kind`/`Channel` enum on `Token` instead of a separate `Reasoning` field.** Rejected: it
  breaks the zero-edit backward-compat property — every `.Text` consumer (`agentloop.go:198`,
  `gollem.go:244,292`, `summarizer.go:157`) would need a branch, and a missed branch silently mixes
  reasoning into the answer. The separate field is invisible to old readers.
- **Reference-by-ID carry-forward (Responses API `previous_response_id`).** Rejected as a
  **non-goal**: it makes the provider stateful and abandons aisotropy's Chat-Completions-only,
  stateless posture ([ADR-0009](0009-provider-agnostic-llm-layer.md)). Stateless reinjection keeps
  every provider on the same seam at the cost of re-sent tokens — a cost we govern explicitly.
- **Flattening the thinking block into `current` as text (like tool results).** Rejected for
  Anthropic: it **loses the signature** and fails Anthropic's continued-reasoning contract. Hence
  the opaque, provider-native `ReasoningState` re-attached as a real `thinking` block. For
  stateless providers with no signature, text reinjection *is* acceptable and the same seam allows
  it.
- **Persisting raw CoT into the summary buffer / semantic tier.** Rejected: raw reasoning is noise
  and a privacy liability in long-term memory; its value is already captured in the persisted final
  answer. Consolidate-then-discard keeps `sleepcycle.Record` / `consolidation.Sink` reasoning-free
  by construction.
- **Metering reasoning as plain `OutputTokens`.** Rejected: hides the single biggest cost driver of
  reasoning models. A separate `ReasoningTokens` line makes the spend auditable per tenant.

## Non-goals

- **Not implementing a reasoning model.** This is about *handling* provider reasoning, not building
  a reasoner.
- **Not the Responses API / reference-by-ID stateful path.** Stateless reinjection only.
- **Not persisting raw chain-of-thought** into working-memory summaries, facts, or semantic memory.
- **Not changing `Client.StreamChat`'s text semantics.** `Reasoning` is additive; `Text` continues
  to mean answer text.
- **Not editing `arenas/`** (owned by another agent) — the eval *design* names the harness; the
  code hooks land in `internal/eval`.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| **Cost blowup** from reinjection growing per step | reasoning-effort ladder under pressure; existing `MaxTurns`/`MaxSteps` step cap (`toolagent.go:116`); `gollemMaxPromptChars` ceiling (`gollem.go:480`) counts reinjected reasoning; optional per-step token budget; separate `ReasoningTokens` meter for visibility. |
| **Context-window pressure** (reasoning + tool results overflow) | reinjected reasoning counts toward the prompt-length guardrail which aborts before overflow; a follow-on can compress the carried reasoning to a summary between steps. |
| **Provider variance** (Anthropic signed blocks vs OpenAI stateless vs gateways that drop reasoning) | opaque `ReasoningState` + `ReasoningToolCaller` type-assertion: each provider interprets its own carry; absent capability → no-op. |
| **Anthropic `redacted_thinking`** (encrypted, un-inspectable) | passed back verbatim, never decoded or logged; treated as an opaque block in `ReasoningState`. |
| **Tenant leak via a shared reasoning buffer** | `ReasoningState` is a goroutine-local value, never a shared/keyed map; enforced by a `-race` isolation test. |
| **Reasoning accidentally logged** | joins the "content never logged" rule; grep test asserts no reasoning at any log sink. |
| **Silent double-billing** (thinking counted in output AND reasoning) | provider mapping decides which bucket owns the tokens; `ReasoningTokens` is visibility-only for Anthropic (already in `output_tokens`), additive-source-of-truth for OpenAI detail. |

---

# Implementation Plan — reasoning-model support (complexity: **Large+ / Critical-adjacent**)

**Complexity classification.** Large+ (multi-file, cross-cutting: LLM seam + agent loop + memory +
cost + eval + config). It is **Critical-adjacent** because it touches the tenant-isolation
invariant and the pre-spend cost path — so the DoD includes a **security review** (tenant isolation)
and the tenant-isolation test is a merge blocker, per `.claude/rules/constitutional-gates.md` and
`definition-of-done.md` (Large/Critical rows). Per the reconstruction-phase research-first
protocol, this ADR IS the Phase-0 research artifact; operator approval of this design was the gate to
implementation.

**Implementation status (2026-07-23):** All 10 tasks (T1–T10) from the plan below have been
implemented in the ADR-0061 worktree (`agent-a9bcfcf3abe979e30`). The reasoning channel is OFF by
default (`AISOTROPY_REASONING` unset) — with the flag off, `go build ./... && go test ./...` passes
with zero behavior change, proving backward compatibility. With the flag on, reasoning streams on
`Token.Reasoning`, the loop reinjects prior reasoning natively across steps, Anthropic signed
thinking blocks round-trip, and a no-reasoning endpoint is a verified no-op. Tenant-isolation test
passes under `-race`. Cost meter reports `ReasoningTokens` per tenant. All six non-negotiable tests
are green.

**Guiding rule for the whole plan: reasoning is OFF by default.** With the flag off,
`go build ./... && go test ./...` must pass with **zero behavior change** — that is the primary
backward-compat acceptance proof (T0 below).

### Task order (each task = one reviewable unit)

| # | Task | Files | Tests it needs |
|---|---|---|---|
| **T1** | Add `Token.Reasoning` + `Usage.ReasoningTokens` (additive; no consumer edits) | `internal/llm/llm.go` | Existing `internal/llm`, `agentloop`, `memory` suites pass unchanged (proves the field is invisible to `.Text` readers). |
| **T2** | Anthropic: opt-in `Thinking` request param (flag); demux `thinking_delta` → `Token.Reasoning`; capture signed `thinking`/`redacted_thinking` blocks; map thinking tokens → `Usage.ReasoningTokens` (no double count) | `internal/llm/anthropic.go` | Unit test with a recorded/mocked thinking stream: reasoning deltas emitted, text unaffected, usage split correct. |
| **T3** | OpenAI-compat: read `delta.reasoning` / `reasoning_content` / `provider_specific_fields.reasoning` → `Token.Reasoning`; read `completion_tokens_details.reasoning_tokens`; **no-op when absent** | `internal/llm/openai_compat.go` | Graceful-degradation unit test: a chunk with no reasoning field (agnes-shaped, `reasoning: null`) yields `Token.Reasoning == ""` and identical behavior to today. |
| **T4** | `ReasoningState` + `ReasoningBlock` + `ReasoningToolCaller` optional interface; Anthropic re-attaches signed blocks natively on the next call; OpenAI-compat implements as optional-context/no-op | `internal/llm/tools.go` (or new `internal/llm/reasoning.go`), `anthropic.go`, `openai_compat.go` | Anthropic round-trip test (mock): a signed block passed in `prior` is re-attached as a `thinking` content block on the next request. Type-assertion test: a plain `Client` is unaffected. |
| **T5** | Thread `ReasoningState` through the loops (stateless reinjection, request-local) | `internal/agentloop/agentloop.go` (`Run`), `internal/llm/toolagent.go` (`RunToolAgent`), `internal/llm/gollem.go` (bridge in `requestWithTools`) | **Tenant-isolation test (MERGE BLOCKER, `-race`)**: two concurrent loops for tenant A and tenant B; assert no reasoning byte from A appears in B's reinjected prompt/state. Multi-step CoT-coherence test: step 2 sees step 1's reasoning when the client supports it. |
| **T6** | Cost accounting: `ReasoningTokens` on `TenantStats` + `Meter.Record`; price as output; feed from `Usage.ReasoningTokens` | `internal/costmeter/costmeter.go`, `internal/api/chat.go` (`chatRecordTurn`), `internal/api/chat_stream.go` | **Cost test**: a turn with N reasoning tokens increments the tenant's `ReasoningTokens` and `CostUSD` correctly; negative-clamp preserved. |
| **T7** | Reasoning-effort ladder tied to `Limiter.Pressure`; extend/adjacent to `DegradeAt` | `internal/llm/degrade.go` (+ pressure read in `chat_stream.go`) | Unit test of the ladder table: effort steps down at 0.80, cheap model at 0.95. |
| **T8** | Config flag + effort default (`AISOTROPY_REASONING` on/off, `AISOTROPY_REASONING_EFFORT`) wired at composition | `aisotropy/app.go`, LLM `FromEnv`/registry construction | Flag-off test = T0 (no behavior change). Flag-on smoke test against a reasoning-capable mock. |
| **T9** | Memory guard: keep `Message`/`sleepcycle.Record`/`consolidation.Sink` reasoning-free; if a debug ephemeral field is added, strip it before `Condense` and export | `internal/memory/summarizer.go`, `internal/memory/working_memory.go` (guard only), `internal/sleepcycle/record.go` (assert unchanged) | **Consolidation-purity test**: after a reasoning-on task, BadgerDB summary + any NDJSON export contain zero reasoning (grep/structural assertion). |
| **T10** | Eval hooks: reinjection ON/OFF matched-pair benchmark (quality via `LLMJudge`, cost via `costmeter`/`ReasoningTokens`, latency) | `internal/eval/*` (new benchmark harness only; **not** `arenas/`) | Benchmark runs and emits the cost/quality table; deterministic on `StubClient` for CI. |

### Tests that are non-negotiable (called out explicitly)

1. **Tenant-isolation test (T5)** — reasoning never crosses tenants; runs under `-race`; **merge
   blocker**.
2. **Cost test (T6)** — quota/cost accounting counts reasoning tokens (`Usage.ReasoningTokens` →
   `costmeter`).
3. **Graceful-degradation test (T3/T8)** — a no-reasoning endpoint (agnes-shaped / `StubClient`)
   still works, `Token.Reasoning == ""`, behavior identical to today.
4. **Backward-compat test (T0/T1)** — full suite green with the flag OFF, proving zero change to
   text-only consumers.
5. **Never-logged test** — no reasoning content at any `slog` sink.
6. **Consolidation-purity test (T9)** — no raw CoT in working-memory summary, sleep-cycle export, or
   semantic/facts tiers.

### Acceptance criteria

- `go build ./... && go test ./...` pass with reasoning **OFF** — zero behavior change (primary
  backward-compat proof).
- With reasoning **ON** against a reasoning-capable mock: reasoning deltas stream on `Token.Reasoning`;
  the loop reinjects the prior reasoning natively across steps; Anthropic signed blocks round-trip;
  a no-reasoning endpoint is a verified no-op.
- Tenant-isolation test passes under `-race`.
- `costmeter` reports non-zero `ReasoningTokens` for a reasoning turn; `/costs` view exposes it.
- Grep/structural proof: **zero** reasoning bytes in BadgerDB summaries or `sleepcycle` NDJSON
  export after a reasoning-on task.
- No new copyleft/BUSL dependency (license gate): the design adds **no dependency** — reasoning
  fields ride the existing Anthropic SDK and stdlib `net/http` JSON paths.
- Security review sign-off on tenant isolation of `ReasoningState`.

### Definition of Done (Large/Critical rows)

`readiness_check` · all affected packages build · unit + integration tests (incl. the six
non-negotiable tests) · architecture compliance (provider-agnostic seam intact, no provider-isms
leak past the `llm` boundary) · docs updated (`docs/architecture/*`, `docs/components/*`) ·
adversarial review · **security review (tenant isolation)** · reasoning-OFF backward-compat proof.

## Consequences

**Positive.** Aisotropy can exploit reasoning models on every provider it already speaks, with CoT
coherence preserved *within* a task, full cost visibility (`ReasoningTokens`), and a
privacy-respecting lifecycle that keeps raw chain-of-thought out of long-term memory. Every change
is additive and flag-gated; the text-only path is untouched when reasoning is off.

**Negative / accepted.** Stateless reinjection costs re-sent tokens per step (the price of not going
stateful) — governed, not hidden. The Anthropic signed-block requirement forces the
`ReasoningToolCaller` seam widening (one new optional interface). Providers that drop reasoning lose
cross-step CoT coherence — an accepted degradation, since those endpoints have no reasoning to carry
anyway.
