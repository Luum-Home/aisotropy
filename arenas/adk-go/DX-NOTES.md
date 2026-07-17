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

### Three native slots, four axes — one axis is hand-composed
ADK's session key has three native slots: `appName` (bound at runner
construction), `userID`, `sessionID`. `appName` is genuinely non-droppable per
request — one runner per product (or per tenant) makes that axis native, and a
cross-value read is a construction miss (`TestProductAppNameIsolatesByConstruction`).
The friction is arity: aisotropy scopes on four axes, ADK offers three slots, so
exactly one axis must be concatenated into `userID` by hand. Keep it in the key
and it isolates; drop it and two values collide — a same-session read
(`TestResidualAxisDropLeaks`). It is a real footgun, but a bounded one: it is one
axis, on the developer's chosen mapping, not "ADK cannot isolate." `adk-python#1641`
is the reminder that even a native slot leaks if a query forgets it — per-query
discipline matters in both frameworks.

### Auth is a seam you must wire (near-parity)
`runner.Run(ctx, userID, sessionID, …)` trusts the identity strings it is handed;
binding them to an authenticated caller is middleware. ADK gives you the seam — an
`llmagent` `BeforeAgentCallback` can reject an unbound identity before the model
runs (`FEATURE 16`, exercised for real in `cmd/parity`). Worth noting aisotropy is
in the same boat by default: it trusts the path `tenant_id` verbatim in NoAuth
mode and only verifies when `AISOTROPY_JWT_SECRET` is set (ADR-0024). This is a
wire-it-yourself layer in both, not an ADK-specific gap.

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
seams and genuine OTel observability. Its isolation is real composite-key
construction across three native slots; a competent developer makes any one
extra axis native by running one runner per value of it (`appName`). The honest
residual is arity — four axes onto three slots leaves exactly one axis
hand-composed in `userID`, and dropping it is the leaky path.

**Best for:** teams (esp. in the Google ecosystem) wanting MCP/A2A + graph
orchestration + OTel, who scope on ≤3 axes (or bind the extra one to `appName`).

**Worst for:** regulated multi-tenant SaaS wanting all four axes bound by
construction and pre-spend cost control without a Vertex gradient.

**vs. aisotropy:** ADK wins on protocol breadth and ties on OTel; aisotropy wins
on all-axes isolation-by-construction (four axes in `ctx`, no axis hand-composed),
self-hostable tiered memory, and pre-spend governance. Auth is near-parity (both
trust the identity until a gate binds it).
