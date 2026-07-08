---
type: ADR
title: "ADR-0009 — Provider-agnostic, multi-connection LLM layer"
description: internal/llm gains a Registry that holds multiple named provider connections simultaneously, instead of FromEnv picking a single client by precedence; the Client interface stays unchanged.
tags: [decisions, adr, llm, registry, providers, multi-tenancy-of-models]
timestamp: 2026-07-08T00:00:00Z
---

# ADR-0009 — Provider-agnostic, multi-connection LLM layer

## Status

**✅ Accepted (2026-07-08).** `internal/llm` gains `Registry` (`internal/llm/registry.go`), which
holds several named `Client` connections at the same time. `FromEnv()` is preserved for backward
compatibility and is now implemented as `RegistryFromEnv().Default()`. The `Client` interface
(`StreamChat` + `Name`) does not change — every existing caller (`internal/api`,
`internal/memory`) keeps working unmodified.

## Context

Before this change, `internal/llm` already covered OpenRouter, direct OpenAI, and Anthropic
through two implementations (`AnthropicClient`, `OpenAICompatClient`) plus a keyless `StubClient`.
But `FromEnv()` picked **exactly one** `Client` by precedence
(`OPENROUTER_API_KEY` > `OPENAI_API_KEY` > `ANTHROPIC_API_KEY` > stub) and returned it — the
runtime only ever had a single active provider connection, chosen once at startup in
`cmd/aisotropyd/main.go`.

Two problems with that shape:

1. **Implicit lock-in to whichever provider wins precedence.** If an operator configures both
   `OPENROUTER_API_KEY` and `ANTHROPIC_API_KEY` (e.g. to keep a native Anthropic connection for
   quality-sensitive calls while using OpenRouter for cheap ones), the single-client `FromEnv()`
   silently drops the Anthropic connection — there is no way to reach it at runtime.
2. **No foundation for per-request routing.** [Bet 4 — compressibility-based
   routing](../research/bet-04-compressibility-routing.md) needs more than one candidate provider
   to route between (e.g. route "easy"/high-compressibility prompts to a cheaper model, "hard"
   ones to a stronger one). A single global `Client` cannot express that; a registry of named,
   simultaneously-live connections can.

## Options considered

### A — Keep single-client `FromEnv`, add provider switching via a new env var
Add something like `AISOTROPY_PROVIDER=anthropic` to let the operator override which single
client `FromEnv()` builds. Rejected: still only one live connection at a time; does not solve the
"both OpenRouter and Anthropic configured" case, and does not give bet-04 the multiple candidates
it needs to route between.

### B — Introduce a `Registry` holding multiple named connections (chosen)
Build every provider that has credentials configured (not just the highest-precedence one),
register each under a stable name (`"openrouter"`, `"openai"`, `"anthropic"`, always `"stub"`),
and keep a default for callers that only want one `Client`. `FromEnv()` becomes a thin wrapper:
`RegistryFromEnv().Default()`.

### C — Full router/load-balancer now (pick provider per request based on cost/latency/compressibility)
Rejected for this ADR: that is the actual bet-04 mechanism (compressibility-gated routing) and
depends on the surprisal/compressibility signal from [bet-01](../research/bet-01-surprisal-gated-memory.md)
and [bet-03](../research/bet-03-rate-distortion-context.md), which are still spikes, not
production code. Building the Registry now — without a router yet — is the minimal step that
unblocks bet-04 later without over-building today.

## Decision

**Option B.** `internal/llm/registry.go` adds:

- `Registry`: `Register(name, client)`, `Get(name) (Client, bool)`, `Names() []string`,
  `Default() Client`, `DefaultName() string`, `SetDefault(name) error`.
- `RegistryFromEnv() *Registry`: registers every provider whose credentials are present
  (`OPENROUTER_API_KEY` → `"openrouter"`, `OPENAI_API_KEY` → `"openai"`,
  `ANTHROPIC_API_KEY` → `"anthropic"`), **always** registers `"stub"` as a keyless fallback, and
  sets the default by the same precedence `FromEnv()` used before
  (openrouter > openai > anthropic > stub).
- `FromEnv() Client`: kept for backward compatibility (`cmd/aisotropyd/main.go` still calls it
  unchanged), now implemented as `RegistryFromEnv().Default()` so the detection/precedence logic
  lives in exactly one place.

The `Client` interface is untouched. Extending to a new provider requires only: implement
`Client`, then either call `Register(name, client)` on a `*Registry` or add one `if` branch to
`RegistryFromEnv`. `OpenAICompatClient` already reaches most providers that speak the OpenAI
`/chat/completions` SSE protocol (OpenRouter, OpenAI, Groq, Together, self-hosted gateways, and
— via model ids like `"google/gemini-..."` — Google/Gemini through OpenRouter) by only varying
base URL + model, so most new providers need zero new Go code, just a new `Register` call with
the right base URL.

## On the OpenAI-compatible protocol and vendor lock-in

A fair question: does `OpenAICompatClient` (which speaks OpenAI's `/chat/completions` wire
format) lock us into OpenAI? **No — it is the opposite.** The reasoning:

- **The seam is the `Client` interface, not the wire format.** Every upstream caller
  (`cmd/aisotropyd`, `internal/api.RegisterStream`, `internal/memory`'s `LLMSummarizer`) depends
  on `llm.Client`, never on the concrete `OpenAICompatClient`. Swapping or adding a provider is a
  one-file change behind the interface; no upstream code moves.
- **The interface carries zero OpenAI-isms.** It is `StreamChat(ctx, system, prompt) -> <-chan Token`
  plus `Name()`. No `messages[]`, no `tool_calls`, no `choices` leak upward — so OpenAI's schema
  never becomes load-bearing outside the one file that implements it.
- **OpenAI `/chat/completions` is a de-facto interop standard, not a single vendor.** One
  implementation reaches OpenRouter, OpenAI, Groq, Together, Fireworks, DeepInfra, Mistral, xAI,
  and local runtimes (vLLM, llama.cpp server, Ollama, LM Studio) by varying only base URL + model.
  It is the *lingua franca* that maximizes provider reach with the least code — an anti-lock-in
  workhorse.
- **Native (non-OpenAI) clients are already first-class.** `AnthropicClient` uses Anthropic's
  native SDK, not the OpenAI shim, and sits behind the same `Client` interface. This proves the
  design supports native provider protocols, not only the compatibility layer.

**The real risk is not lock-in but the lowest-common-denominator:** the OpenAI-compatible path
cannot express provider-native differentiators (Anthropic prompt caching / extended thinking,
Gemini-native features, local-runtime knobs). **Mitigation (already in the design):** when those
features matter, add a *native* `Client` for that provider — exactly as `AnthropicClient` does —
rather than forcing everything through the compatibility format.

**Guardrails to preserve this property:** keep the `Client` interface free of any OpenAI-format
assumptions, and keep upstream code depending on `llm.Client` (never on a concrete client type).

## Consequences

**Positive**
- **No more silent connection loss**: an operator with both `OPENROUTER_API_KEY` and
  `ANTHROPIC_API_KEY` set gets both connections registered and reachable via `Get("anthropic")` /
  `Get("openrouter")`, not just whichever wins precedence.
- **Unblocks bet-04** (compressibility routing): a future router can iterate `Registry.Names()`
  and pick a `Client` per request instead of being stuck with one global client.
- **Zero migration cost for existing callers**: `Client` is unchanged and `FromEnv()` keeps its
  exact signature and precedence, so `cmd/aisotropyd/main.go`, `internal/api`, and
  `internal/memory` needed no changes.
- **Extensibility is documented in code**: adding a provider is either "implement Client" (new
  wire protocol) or "one Register call" (any OpenAI-compatible endpoint) — no framework, no new
  abstraction layer.

**Negative / technical debt**
- **No router yet.** `Registry` only stores connections; nothing today picks a non-default
  connection per request. That is deliberately left to a follow-up ADR once bet-04's
  compressibility signal graduates from spike to production code.
- **`Default()` panics on an empty registry.** Acceptable because `RegistryFromEnv()` always
  registers `"stub"`, so the only way to hit an empty registry is constructing one manually via
  `NewRegistry()` and never calling `Register` — a programmer error, not a runtime env
  misconfiguration.

## References

- [Bet 4 — compressibility-based routing](../research/bet-04-compressibility-routing.md)
- [Decisions index](index.md)
- Current implementation: `internal/llm/registry.go` (+ `registry_test.go`).
- Prior art in this package: `internal/llm/llm.go` (Client interface), `internal/llm/openai_compat.go`,
  `internal/llm/anthropic.go`, `internal/llm/stub.go`.
