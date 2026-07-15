---
type: Developer notes
title: "ADK Go — DX notes (from building the real integration)"
description: Honest developer-experience assessment of Google ADK Go (google.golang.org/adk/v2 v2.0.0) from actually building the Orbit chatbot on it.
tags: [arenas, adk-go, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# ADK Go — DX Notes

These notes come from building the real `orbit/` integration in this arena on
`google.golang.org/adk/v2@v2.0.0`, not from docs alone.

## The good

### The `model.LLM` seam is clean
Implementing an offline fake model is a single interface:
`GenerateContent(ctx, *LLMRequest, stream bool) iter.Seq2[*LLMResponse, error]`.
Our `orbit.FakeModel` drops in wherever a real `gemini.NewModel` would, so the
whole runner + session path exercises for real with **no API key**. This is the
right shape for testing and is what makes this arena honest and offline.

### Session/state is real construction (composite key)
`session.Service` keys by `(appName, userID, sessionID)` and the in-memory
`Get` is a keyed lookup, not a scan. When you fold tenant into the key, cross-
tenant reads are clean misses — verified in `TestCompositeKeyIsolates`. This is
the same construction class as `adk-python`'s composite PK.

### OTel `gen_ai` observability out of the box
`internal/telemetry` emits spans with `gen_ai.usage.*` attributes. This is the
one dimension where ADK ties aisotropy and beats Eino (proprietary APM) and
Genkit (proprietary schema).

### Protocol breadth
MCP native (`mcptoolset`), A2A first-class (`agent/remoteagent`), workflow graph
engine (`workflowagent`, sequential/parallel/loop), structured output
(`OutputSchema`). It is a serious runtime, not a toy.

## The painful

### No tenant primitive, no identity binding
`runner.Run(ctx, userID, sessionID, …)` trusts the identity strings verbatim.
There is no `TenantID` and nothing binds `userID` to an authenticated caller.
Multi-tenancy is entirely "fold tenant into `userID` and never get it wrong."
The leaky path (drop the tenant component) is literally one token shorter than
the correct path — see `TestTenantDropLeaks`. `adk-python#1641` shows even the
keyed backend leaked when one sub-query forgot part of the key; ADK Go hands you
the same footgun and doesn't even own the tenant dimension.

### Heavy dependency graph
`go mod tidy` pulls the full cloud + genai + OTel + gRPC + sqlite tree (dozens
of modules, incl. `cloud.google.com/go/aiplatform`). Fine once cached, but it is
not a light embed.

### Vertex gradient
The self-hostable backends are in-memory / sqlite. The *rich* memory (vector
recall via MemoryBank) and managed session service are Vertex-tethered. The
gradient is real: "works standalone, works best on Vertex."

### Cost is measured, not gated
`gen_ai.usage.*` gives you post-hoc token counts. There is no pre-spend quota
primitive — you cannot reject/degrade before the model call the way aisotropy's
constitutional gate requires.

## The verdict

ADK Go is a **real, protocol-rich agent runtime** with clean model/session
seams and genuine OTel observability. Its isolation is convention: the composite
key is real, but the tenant dimension and identity binding are the developer's
job, and the leaky path is a one-liner.

**Best for:** teams (esp. in the Google ecosystem) wanting MCP/A2A + graph
orchestration + OTel, who own their multi-tenant discipline.

**Worst for:** regulated multi-tenant SaaS wanting isolation-by-construction and
pre-spend cost control without a Vertex gradient.

**vs. aisotropy:** ADK wins on protocol breadth and ties on OTel; aisotropy wins
on isolation-by-construction (tenant in `ctx`, unscoped read unrepresentable),
self-hostable tiered memory, pre-spend governance, and sovereignty.
