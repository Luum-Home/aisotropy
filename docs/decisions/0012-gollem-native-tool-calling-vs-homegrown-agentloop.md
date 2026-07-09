---
type: ADR
title: "ADR-0012 — Gollem as the agent-loop engine: native tool-calling vs. the homegrown agentloop"
description: Revisits ADR-0010's guardrail and ADR-0009's Client seam to decide whether the agent endpoint's tool dispatch should move from the homegrown structured-output agentloop onto Gollem's native FuncTool, and if so, how to expose tool-calling without subordinating the provider-agnostic Registry to Gollem's provider layer.
tags: [decisions, adr, gollem, tool-calling, agent-loop, llm, provider-agnostic]
timestamp: 2026-07-09T00:00:00Z
---

# ADR-0012 — Gollem as the agent-loop engine: native tool-calling vs. the homegrown agentloop

## Status

**🟡 Proposed (2026-07-09).** Awaiting operator approval. This ADR exists because
[ADR-0010](0010-gollem-adoption-scope-and-observability.md)'s "Guardrail on deeper adoption"
explicitly requires a **dedicated ADR revisiting [ADR-0009](0009-provider-agnostic-llm-layer.md)**
before any Gollem adoption that would widen the `llm.Client` seam toward native tool-calling. This
is that ADR. No code changes until it is Accepted.

## Context

Aisotropy today has **two structured-LLM mechanisms that do not share a code path**:

1. **Reflection / structured-output** — `internal/api/chat.go` → `llm.ReflectAuto[T]` →
   `GollemReflect[T]` builds a `core.Agent[T]` over `GollemModel` and runs
   `core.RunWithReflection[T]` (`internal/llm/gollem.go`). Gollem **is** the engine here, but only
   for schema-constrained output — it drives no tools.
2. **Agentic tool use** — `internal/api/chat_agent.go` drives a **homegrown** `agentloop.Loop`
   (`internal/agentloop/agentloop.go`). It does "tool calling" by prompting the model to emit a
   small JSON `Decision{tool, args}`, then re-checks the chosen tool name against the scope-permitted
   set before executing. There is no Gollem here at all.

The reason the agent endpoint is homegrown is structural, and stated in the code itself:

> "this adapter only covers the 'structured output via prompting' path … It does not support
> Gollem's native function-tool calling (`core.FuncTool`) **because our Client does not expose that
> primitive**." — `internal/llm/gollem.go:55-60`

> "scope-safe 'tool calling' for text-streaming LLM clients that have **no native function-calling
> API** (`internal/llm.Client` is a single `StreamChat` method)." — `internal/agentloop/agentloop.go:1-4`

The `llm.Client` seam is deliberately narrow — `StreamChat(ctx, system, prompt) (<-chan Token, error)`
plus `Name()` (`internal/llm/llm.go`) — and ADR-0009 built a provider-agnostic `Registry` on top of
exactly that seam so per-request compressibility routing (Bet 4) can pick among simultaneously-live
providers. ADR-0010 then drew a hard line: widening `Client` toward tool-calling, or letting a
Gollem-native provider own the HTTP call, is out of scope without this ADR, because it would
"subordinate ADR-0009's provider-agnostic `Registry` … to Gollem's own provider layer."

So the question is not "is Gollem wired?" (it is, as the reflection engine) but: **should the agent
endpoint's tool dispatch move onto Gollem's native `FuncTool`, and if so, how — without breaking the
provider-agnostic seam and the per-request routing plan it enables?**

### What is at stake if we do nothing

The homegrown `agentloop` is real, tested, and enforces the security invariant twice (never mention
a non-permitted tool; re-check the chosen name before executing — `agentloop.go:82-160`). But it
carries structural costs that grow as tool use grows:

- **Prompt-emulated tool-calling is weaker than native.** It relies on the model reliably emitting a
  bare JSON object; `parseDecision` already has to defend against prose, code fences, and stray
  braces (`agentloop.go`). Native function-calling APIs return structured tool calls directly, with
  provider-side arg typing and parallel/multi-tool calls the JSON-`Decision` shape cannot express
  (it is one tool per turn).
- **Two engines to maintain.** Reflection uses Gollem's `Agent[T]`; tool use hand-rolls the loop.
  Gollem's guardrails, `CostTracker`, and OTel span tree (the "ADOPT NOW" wins of ADR-0010) attach
  to `Agent[T]` — the homegrown loop gets none of them for free.

## Decision drivers

1. **Preserve ADR-0009.** The provider-agnostic `Registry` and per-request routing (Bet 4) must
   survive. Nothing may force every provider through Gollem's provider layer.
2. **Preserve the sovereign / small-binary thesis** (ADR-0001, ADR-0010 Option A rejection): no
   transitive pull of gin/fiber/temporal/websocket, no second HTTP client owning provider calls.
3. **Preserve the scope-gating security invariant** (`agentloop.go:1-17`,
   `.claude/rules/constitutional-gates.md`): a tool the caller's scopes did not permit must never
   run, regardless of what the model emits. Whatever replaces `agentloop` must enforce this at least
   as strongly.
4. **Backward compatibility of the `Client` contract** (constitutional gate: the Go struct/interface
   is the contract). A change that forces `StubClient`, `AnthropicClient`, and `OpenAICompatClient`
   to all implement tool-calling is a breaking change to every implementer.
5. **Don't regress providers without native tools.** Not every routed provider exposes
   function-calling; the structured-output path must remain the fallback.

## Options considered

### A — Status quo: keep the homegrown agentloop, do not adopt Gollem tools
Reflection stays on Gollem; the agent endpoint stays hand-rolled. Zero risk, zero seam change, but
permanently forgoes native tool-calling and keeps two engines. Rejected as the long-term answer, but
it is the safe fallback if this ADR is not approved.

### B — Widen `llm.Client` itself with a tool-calling method
Add e.g. `CallTools(...)` directly to the `Client` interface. **Rejected.** This is the exact
seam-widening ADR-0010 guards against: it breaks every existing implementer (`StubClient`,
`AnthropicClient`, `OpenAICompatClient` would all need tool-calling), it forces a tool-calling shape
into the one interface the `Registry` and routing depend on, and it is a breaking change to the
contract (driver 4). It couples provider-agnostic routing to a capability not all providers share.

### C — Add an OPTIONAL, additive tool-calling capability interface; bridge Gollem `FuncTool` to it (proposed)
Leave `Client` untouched. Add a **separate, optional** interface in `internal/llm` that a provider
*may* also implement when its backend supports native function-calling, discovered by type assertion
at the call site:

```go
// ToolCaller is an OPTIONAL capability. A Client MAY also implement it when its
// provider supports native tool/function calling. It is discovered by type
// assertion (`tc, ok := client.(ToolCaller)`) — a Client that does not implement
// it is unaffected, and callers fall back to the structured-output agentloop.
// Adding this interface does NOT change the Client interface: ADR-0009's seam,
// the Registry, and per-request routing are all preserved.
type ToolCaller interface {
    Client
    CallTools(ctx context.Context, system, prompt string, tools []ToolSpec) (ToolCallResult, error)
}
```

- `internal/llm/gollem.go`'s `GollemModel` bridges Gollem's `core.FuncTool` to a `ToolCaller` when
  the wrapped client is one, closing the documented gap at `gollem.go:55-60`. When the client is not
  a `ToolCaller`, the adapter behaves exactly as today (structured output via prompting).
- `internal/api/chat_agent.go` prefers the native path when `client.(ToolCaller)` holds, and falls
  back to the existing `agentloop.Loop` otherwise. **Both paths keep the double scope-gate**: the
  `tools []ToolSpec` handed to `CallTools` are already `Registry.Permitted(scopes)`, and the
  returned tool name is re-checked against the permitted set before `Executor.Execute` — the same
  invariant `agentloop.SelectTool` enforces today (`agentloop.go:120-146`).
- The `Registry` (ADR-0009) is untouched; routing still selects among providers by `Client`, and a
  routed provider that happens to implement `ToolCaller` simply unlocks the native path for that
  request.

This threads all five drivers: no `Client` breaking change (additive interface, driver 4), no forced
Gollem provider ownership (driver 1/2), scope-gate preserved on both paths (driver 3), and non-tool
providers keep working via fallback (driver 5).

### D — Let a Gollem-native `Model`/provider own the HTTP call to the LLM
Use Gollem's own provider clients as the transport for tool-calling. **Rejected.** This is precisely
what ADR-0010's guardrail forbids: it subordinates ADR-0009's `Registry` to Gollem's provider layer,
re-introduces OpenAI-isms into the seam, and risks the transitive-dependency bloat ADR-0010 Option A
was rejected for.

## Proposed decision (pending approval)

**Option C.** Introduce an optional `ToolCaller` capability interface, bridge Gollem `FuncTool` to
it in the adapter, and make the agent endpoint prefer native tool-calling when available while
retaining the homegrown `agentloop` as the fallback for providers (and the stub) that do not
implement it. This is the smallest change that unlocks native tool-calling and unifies the agent
endpoint onto Gollem's `Agent[T]` (inheriting its guardrails / `CostTracker` / OTel span tree per
ADR-0010's "ADOPT NOW") **without** widening the `Client` seam or subordinating the provider-agnostic
`Registry` to Gollem's provider layer.

### Scope of the follow-up implementation (only if Accepted)

1. `internal/llm/tools.go` (new): the `ToolCaller` interface + `ToolSpec` / `ToolCallResult` types,
   expressed in aisotropy's own vocabulary (no OpenAI-isms leaking into the seam).
2. `internal/llm/gollem.go`: bridge `core.FuncTool` ⇄ `ToolCaller`; leave the text/structured-output
   path unchanged for non-`ToolCaller` clients.
3. One provider implements `ToolCaller` first (whichever native API is validated) — the others and
   `StubClient` remain plain `Client` and exercise the fallback.
4. `internal/api/chat_agent.go`: capability-detect and dispatch; both branches funnel through the
   same `Registry.Permitted(scopes)` + post-selection name re-check.
5. Tests: a `ToolCaller` fake proving (a) a non-permitted tool name from the native path is rejected
   before execution (the `agentloop` security test, ported), (b) a non-`ToolCaller` client still
   drives the `agentloop` fallback unchanged.

### Explicit non-goals

- Not touching the streaming chat path (`chat_stream.go`) — it stays `Client.StreamChat`-direct per
  ADR-0010's two-path observability decision.
- Not adopting `ext/memory`, `autocontext`, or auto-`Retrieve` — those remain rejected by ADR-0010.
- Not widening the base `Client` interface. `ToolCaller` is additive and optional.

## Consequences

**Positive**
- Native, provider-typed tool-calling on the agent endpoint; multi/parallel tool calls become
  expressible (the JSON-`Decision` shape cannot).
- The agent endpoint inherits Gollem `Agent[T]`'s guardrails, `CostTracker`, and OTel span tree —
  the ADR-0010 "ADOPT NOW" wins that the homegrown loop cannot get for free.
- ADR-0009's `Registry` + per-request routing and the small-binary thesis are preserved intact; the
  change is additive and capability-gated.

**Negative / technical debt**
- Two dispatch branches on the agent endpoint (native vs. `agentloop` fallback) must be kept
  behavior-equivalent on the security invariant — a scope-gate regression in one branch would be a
  real isolation bug, so both branches must share the permitted-set check and be tested against it.
- The `ToolCaller` bridge adds surface to `internal/llm/gollem.go`, the seam ADR-0010 wants "the
  thinnest it already is." The optional-interface shape keeps the *base* seam thin, but the adapter
  itself grows.
- Only meaningful once at least one provider implements `ToolCaller`; until then it is scaffolding
  plus the unchanged fallback.

## References

- [ADR-0009 — Provider-agnostic, multi-connection LLM layer](0009-provider-agnostic-llm-layer.md) — the `Client` seam and `Registry` this ADR must preserve.
- [ADR-0010 — Gollem adoption scope and observability](0010-gollem-adoption-scope-and-observability.md) — the guardrail requiring this ADR; the "ADOPT NOW" `Agent[T]` wins this unlocks for the agent endpoint.
- [ADR-0008 — Reflection loop Gollem deferral](0008-reflection-loop-gollem-deferral.md) — why the current Gollem integration is reflection-only.
- `internal/agentloop/agentloop.go` — the homegrown structured-output tool loop and its scope-gate security invariant.
- `internal/llm/gollem.go:55-60` — the documented `core.FuncTool` gap this ADR addresses.
- `internal/llm/llm.go` — the narrow `Client` interface.
- [Bet 4 — Compressibility routing](../research/bet-04-compressibility-routing.md) — the per-request routing the `Registry` seam enables.
- [Decisions index](index.md)
