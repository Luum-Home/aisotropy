---
type: Developer notes
title: "Genkit Go — DX notes (built against the real framework)"
description: Honest developer-experience assessment of Firebase Genkit Go v1.10.0 from building the Orbit multi-tenant support chatbot on the real API.
tags: [arenas, genkit-go, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# Genkit Go — DX Notes

Assessment from actually building Orbit on `github.com/firebase/genkit/go` **v1.10.0**: real
`genkit.Init`, flows, tools, `Generate`/`GenerateData`, streaming, and dotprompt, driven offline by
a fake model registered with `genkit.DefineModel`.

## The good

### `genkit.DefineModel` is a first-class offline seam
Genkit has no built-in "stub"/"test" model, but `DefineModel` makes writing one trivial: you get
the real `*ai.ModelRequest` (messages, tools, output config, streaming callback) and return an
`*ai.ModelResponse`. The entire arena — tool-calling loop, structured-output validation, streaming,
dotprompt — runs with **no API key** through the genuine pipeline. This is a genuinely good testing
story once you know the pattern.

### Strong, explicit, typed generation API
`GenerateData[TicketCategory]` for structured output and `DefineTool[In,Out]` with a real
tool-call round trip are clean and type-safe. The generics-heavy API reads well in Go. Structured
output is **shipped and validated** (it rejects non-conforming JSON), not a preview.

### dotprompt via `WithPromptFS`
`.prompt` files embed cleanly with `go:embed` + `WithPromptFS`, and `LookupPrompt(...).Execute(...)`
renders `{{handlebars}}` templates through the same pipeline. Good separation of prompt from code.

### Apache-2.0, self-hostable, active
1.0 GA (2025-09), now v1.10.0. No SaaS lock-in for the core runtime.

## The painful

### A memory primitive exists, but no isolation primitive — and the gap is invisible
The single biggest gap for a multi-tenant runtime. Genkit-Go v1.10.0 **does** ship a session-memory
primitive — the experimental `ai/exp` `SessionStore` + `localstore.NewInMemorySessionStore`
(July-2026 Agents API preview, gated behind `requireExperimental`) — so "no memory at all" would be
wrong. But that primitive is keyed by a developer-chosen session id with **no** tenant/user/product
binding (`SessionSnapshot`/`SessionState` carry only a `SessionID`): it gives you **persistence, not
isolation**. There is no tenant-scoped `Session`/`Memory`/`Thread` type. You still hand-roll the
tenant binding and pass keys into `ai.WithMessages`; nothing binds a key to a tenant, and nothing
warns you. The correct path (`tenant:user:session`) and the leaky path (`session`) are one string
apart — `TestIsolationLeak` shows a wrong key returning another tenant's secret through the real
model, and `TestRealSessionStoreIsTenantUnaware` reproduces the same read against the framework's
**real** `localstore` SessionStore. (JS-vs-Go asymmetry: Genkit-JS has had stable-ish
`ai.chat()`/`Session`/`SessionStore` longer; Go got there only via this preview.) The docs are quiet
exactly where you'd want guidance.

### Schema instructions leak into the prompt text
A concrete gotcha: `GenerateData` appends the JSON schema as instruction text to the user message.
A naive fake model that echoes the prompt back into its output re-emits the schema's backticks and
escaped quotes, which then corrupts Genkit's own markdown-JSON extractor on the return path
(`data is not valid JSON: invalid character '\'`). You have to strip the injected instructions.
Fine once understood, but a sharp edge.

### Proprietary OTel schema
Genkit ships OTel tracing, but the attributes are `genkit:*` / `genkit:metadata:*`, **not** the
`gen_ai.*` semantic conventions (re-verified against v1.10.0 source — zero `gen_ai.*` keys). For a
team standardizing on OTel gen_ai dashboards/alerts, this is a real integration liability.

### Tenant binding / cost governance are on you
Genkit ships an experimental working-memory store (`ai/exp` SessionStore) and an in-tree offline
vector store (`plugins/localvec`), but no facts/FTS journal and no pre-spend cost gate.
`ModelResponse.Usage` is post-hoc only. The tenant/user/product binding on the session store, the
tiered memory of different nature, and every governance gate are DIY or third-party
(`genkitx-temporal` for durability).

### Dev-mode side effects
`genkit.Init` starts a Reflection server on `:3100` unless `GENKIT_ENV` is unset/non-dev. Set
`GENKIT_ENV=prod` for smoke/CI so it doesn't bind a port.

## The verdict

Genkit Go is a **complement, not a direct competitor** to aisotropy, and Google frames it that way
(complex agent topologies → ADK). Its generation primitives are legitimately strong and shipped.
But for a **multi-tenant** production runtime, the three things that matter most — enforced
isolation, tiered memory, and cost governance — are absent or DIY.

**Best for:** Firebase/Google-stack teams that want a clean, typed generation API (structured
output, tools, streaming, dotprompt) and will own tenancy/memory/cost themselves.

**Worst for:** Teams that need isolation-by-construction, a built-in memory hierarchy, pre-spend
cost control, or standard OTel `gen_ai` observability out of the box.

**vs. aisotropy:** Genkit wins on shipped generation breadth and Firebase fit; aisotropy wins on
isolation-by-construction, the complete built-in memory/cost stack, and `gen_ai` semconv. The trade
is Firebase ecosystem + generation ergonomics vs. sovereign runtime + structural guarantees.
