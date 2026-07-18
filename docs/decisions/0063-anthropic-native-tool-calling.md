---
type: ADR
title: "ADR-0063 — Anthropic-native tool-calling on the official SDK (close the ToolCaller gap)"
description: AnthropicClient (official anthropic-sdk-go) implemented no ToolCaller, so an ANTHROPIC_API_KEY deployment got degraded prompt-loop tool dispatch instead of native tool_use. This ADR closes that gap in-house via the SDK's native Messages tool_use API — no dependency swap — and defers the gollem provider/anthropic swap + prompt caching to a separate operator-gated follow-up.
tags: [decisions, adr, llm, tool-calling, anthropic, provider-agnostic, gollem-exploitation-audit]
timestamp: 2026-07-17T00:00:00Z
supersedes: []
revisits: [0009, 0012]
---

# ADR-0063 — Anthropic-native tool-calling on the official SDK (close the ToolCaller gap)

## Status

**✅ Accepted + Implemented (2026-07-17).** Option **(a)** — `AnthropicClient` now satisfies the
optional `llm.ToolCaller` seam (ADR-0012) using the official `anthropic-sdk-go`'s native
`tool_use` Messages API. **No dependency swap**; ADR-0009's official-SDK choice and ADR-0010's
"no Gollem-native `Model`/provider owning the HTTP call without a dedicated ADR" guardrail are
both preserved. Prompt caching + wrapping gollem's `provider/anthropic` remain a **separate,
operator-gated follow-up** (option b, deferred — see below).

This ADR is the "new ADR revisiting ADR-0009's guardrail" that the gollem-exploitation audit
(`docs/research/gollem-exploitation-audit-SYNTHESIS-2026-07-17.md` §P3.b) required. It answers
**only** half (1) of the P3.b spike — "close the Anthropic-native tool-calling gap" — which,
done on the official SDK, does *not* cross the ADR-0010 guardrail. Half (2) — "measure wrapping
`provider/anthropic` for prompt caching", which *does* cross it — stays open for operator review.

## Context (the gap, code-truth)

`internal/llm` exposes an optional `ToolCaller` capability (ADR-0012): a `Client` that also
implements `CallTools(ctx, system, prompt, []ToolSpec) (ToolCallResult, error)` drives the
provider's **native** function/tool-calling API. The agent endpoint (`internal/api/chat_agent.go`
→ `RunToolAgent`) capability-detects `client.(ToolCaller)` and takes the native path; a plain
`Client` falls back to the homegrown prompt-engineered `internal/agentloop` loop.

Before this change:

- `internal/llm/openai_compat.go` — `OpenAICompatClient` **implements** `ToolCaller`
  (`var _ ToolCaller = (*OpenAICompatClient)(nil)`, `openai_compat.go:45`). So the default
  OpenRouter runtime provider gets native `tool_use`.
- `internal/llm/anthropic.go` — `AnthropicClient` (built on the official
  `github.com/anthropics/anthropic-sdk-go`) implemented **only** `Client` (StreamChat + Name).
  It had **no** `CallTools` (verified: `grep -n CallTools internal/llm/anthropic.go` → no match
  pre-change).

**Consequence:** an aisotropy deployment configured with `ANTHROPIC_API_KEY` (Claude) got
**degraded** tool dispatch — `GollemModel.Request`'s non-`ToolCaller` branch does not
prompt-engineer `params.FunctionTools`, so the agent endpoint routed Claude to the non-native
prompt-loop fallback instead of Anthropic's first-class `tool_use` blocks. Native tool-calling was
*entirely absent* on the one provider whose SDK aisotropy deliberately vendors. This contradicts
the "herramientas especializadas" thesis (the LLM-OS should use each provider's best primitive)
and is exactly the diligence gap the audit flagged: ADR-0009 justified the hand-rolled Anthropic
client "for prompt caching / extended thinking" but the tool-calling half of that client was never
finished.

## Reuse-of-dependency inventory (dependency-reuse-first, `docs/architecture/dependency-reuse-first.md`)

Two candidate "already-provides-it" sources were inventoried from source (not docs/memory):

1. **Official `anthropic-sdk-go` v1.56.0** (already vendored) — native tool-calling IS present:
   `MessageNewParams.Tools []ToolUnionParam` (`message.go:9857`), a `ToolParam{Name, InputSchema,
   Description}` built via `ToolUnionParam{OfTool: ...}` (`message.go:6692`, `7681`), non-streaming
   `MessageService.New` (`message.go:56`) returning `*Message` whose `Content []ContentBlockUnion`
   (`message.go:3512`) carries `tool_use` blocks (`.Type=="tool_use"`, `.Name`, `.Input
   json.RawMessage` — `message.go:1553`). **Nothing new needed to build on top of; the SDK already
   has the primitive.**
2. **gollem `provider/anthropic.Provider`** — ALSO ships native tool-calling + prompt caching
   (default on) + server-side tool search + 1152 lines of tests. But adopting it means the raw-HTTP
   provider *owns the HTTP call*, replacing the official SDK, and requires a downward
   `core.Model → llm.Client` adapter (a mirror of `gollemStream`) that does not exist — which is
   precisely what ADR-0010's guardrail fences off without a dedicated revisiting ADR.

The gap that justifies building (a) rather than adopting (2): a **layer + risk** difference. The
official SDK already exposes the exact primitive we need (native `tool_use`), so building `CallTools`
on it is a *reuse* of the SDK's tool API — not a reinvention — that costs ~40 lines and no new
dependency. Adopting gollem's provider would *swap* the authoritative, Anthropic-maintained SDK for a
raw-HTTP reimplementation of uncertain currency, to gain prompt caching we have not yet measured — a
strictly larger, operator-gated decision. Reuse-first is satisfied by (a): the cheapest primitive that
closes the confirmed gap.

## Options considered

### (a) Implement `ToolCaller` on our own `AnthropicClient` via the official SDK's native `tool_use` — **CHOSEN**
Add `CallTools` to `AnthropicClient`, structurally mirroring `OpenAICompatClient.CallTools`: one
**non-streaming** `Messages.New` request advertising the offered `ToolSpec`s as native `ToolParam`
tools, parsing returned `tool_use` blocks into `[]ToolCall` (reusing the shared `parseToolArgs` /
`rawToolArgs` helpers). Same ctx/tenant/scope behavior as the OpenAI path.

- **Pros:** No dependency change. Keeps the official, Anthropic-maintained SDK (ADR-0009 intact).
  Does **not** cross ADR-0010's guardrail (no Gollem-native `Model` owning the HTTP call). Native
  tool-calls automatically inherit the identical governed path — the breaker/otel decorators
  (`circuitToolCaller`/`otelToolCaller`) and `RunToolAgent`/`chat_agent`'s double scope-gate are
  **generic over `ToolCaller`**, so implementing the seam is sufficient; nothing else changes.
  Low blast radius, fully unit-testable against a mocked Messages endpoint (no API key).
- **Cons:** Does not deliver prompt caching (a separate lever). `CallTools` is single-prompt
  (ADR-0012's accepted 0.x limitation — multi-turn tool RESULTS flatten into prompt text via
  `buildGollemPrompt`), inherited unchanged here.

### (b) Adopt gollem `provider/anthropic.Provider`
Swap the official SDK for gollem's raw-HTTP provider (native tools + prompt caching + tool search).

- **Pros:** Native tools AND prompt caching AND server-side tool search "for free"; 1152 lines of
  upstream tests.
- **Cons:** **Swaps the authoritative Anthropic-maintained SDK** for a raw-HTTP reimplementation of
  uncertain API currency — reverses ADR-0009's deliberate choice. Requires an unbuilt, unmeasured
  downward `core.Model → llm.Client` adapter. **Crosses ADR-0010's explicit guardrail** ("no
  Gollem-native `Model`/provider owning the HTTP call without a dedicated ADR"). Touches the
  provider seam the sovereign-binary thesis protects. **Risk: Medium-High; needs operator sign-off.**

### (c) Hybrid — official SDK default, gollem `provider/anthropic` as an optional provider
Keep the official-SDK `AnthropicClient` as the default; add gollem's provider behind an opt-in flag
for operators who want prompt caching, selected in the Registry.

- **Pros:** Preserves the default posture while making prompt caching reachable; per-operator choice.
- **Cons:** Two Anthropic code paths to maintain and test; still requires the unbuilt `core.Model →
  llm.Client` adapter and still crosses ADR-0010's guardrail for the optional path — so it *also*
  needs the operator-gated ADR before the gollem half can land. Larger surface than (a) for the same
  immediate gap-closure.

## Decision

Implement **(a)** now. It is the minimal, reuse-first, guardrail-respecting closure of the confirmed
gap: it makes Claude deployments get first-class native tool-calling using the SDK aisotropy already
vendors, with no dependency swap and no crossing of ADR-0009/ADR-0010.

**Deferred (operator-gated, NOT in this change):** prompt caching via gollem `provider/anthropic`
(options b/c). That half genuinely swaps the provider seam and crosses ADR-0010's guardrail; it must
be a separate ADR with a measured spike (cache hit-rate, cost delta, API-currency risk of the
raw-HTTP provider) and explicit operator approval. Closing the tool-calling gap here does **not**
prejudge it — the two levers are independent.

## Consequences

- `AnthropicClient` now satisfies `ToolCaller` (`var _ ToolCaller = (*AnthropicClient)(nil)`), so
  `client.(ToolCaller)` at the agent endpoint succeeds for Claude, and the native `tool_use` path is
  taken instead of the prompt-loop fallback.
- **Constitutional gates preserved by construction.** Native Anthropic tool-calls flow through the
  SAME governed path as the OpenAI path: the `circuitToolCaller` / `otelToolCaller` decorators
  forward `CallTools` on the same breaker / emit the same `chat.tools` span (they type-assert the
  inner `ToolCaller` generically), and `RunToolAgent` + `chat_agent.go` register only
  scope-permitted tools and re-validate the returned name before execution. `CallTools` only ever
  relays what the model returned; there is no path from "not offered" to "executed" (governance by
  absence). Tenant isolation via `context.Context` is unchanged.
- **Follow-ups (separate work, not this ADR):**
  - **Prompt caching** — deferred to the operator-gated options (b)/(c) ADR above.
  - **`ReasoningToolCaller` for Anthropic native tools** — `OpenAICompatClient` implements the
    optional reasoning carry-forward `CallToolsReasoning` (ADR-0061); `AnthropicClient` here
    implements the base `ToolCaller` only. Carrying signed `thinking` blocks across native
    tool-call steps (re-attaching them on the follow-up Messages request) is a strictly larger,
    signature-sensitive surface and is left as a follow-up. Absent it, reasoning simply is not
    carried between Anthropic native tool steps — graceful degradation, identical to any
    `ToolCaller` that is not a `ReasoningToolCaller`.

## Reuse of `anthropic-sdk-go` primitives

`CallTools` is built **on** the SDK's native tool API, not around it:
`anthropic.MessageNewParams.Tools` (`message.go:9857`), `anthropic.ToolParam` /
`anthropic.ToolInputSchemaParam` (`message.go:6692`, `6745`), `anthropic.MessageService.New`
(`message.go:56`), and the `tool_use` `ContentBlockUnion` fields `.Type` / `.Name` /
`.Input json.RawMessage` (`message.go:1553`). The provider-agnostic `ToolSpec`/`ToolCall` vocabulary
and the `parseToolArgs`/`rawToolArgs` arg helpers are shared verbatim with the OpenAI path — no
Anthropic tool-envelope leaks into the `llm.ToolCaller` seam (ADR-0012 / ADR-0009 boundary held).

## Verification (evidence)

- `go build ./...` — exits 0.
- `go test ./internal/llm/... -race -count=1` — green, including the new Anthropic tool-calling
  tests (`internal/llm/anthropic_calltools_test.go`): a canned `tool_use` Messages response →
  asserts the `ToolCall` name + flattened `Args` + preserved `RawArgs`, single-shot; the request
  body advertises exactly the offered tools (`name` + `input_schema`, scope-gate-by-absence); the
  no-tools and error-status branches.
- `var _ ToolCaller = (*AnthropicClient)(nil)` (`internal/llm/anthropic_tools.go`) — compile-time
  proof the seam is satisfied.
- `gofmt -l` clean + `go vet ./internal/llm/...` clean on touched files.
