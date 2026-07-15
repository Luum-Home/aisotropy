---
type: Developer notes
title: "Eino — DX notes (from building the real integration)"
description: Honest developer-experience assessment of Eino v0.9.12 from actually building Orbit on it — multi-tenant support bot, offline stub model.
tags: [arenas, eino, dx, developer-experience]
timestamp: 2026-07-15T00:00:00Z
---

# Eino — DX Notes

These notes come from actually building the Orbit chatbot on **Eino v0.9.12** (real dependency,
offline stub model) — not from reading marketing.

## Building the real integration

### Getting a keyless smoke to run is easy — implement one interface
The offline path hinges on `model.ToolCallingChatModel`, which is small:
`Generate`, `Stream`, and `WithTools`. A ~120-line `StubChatModel` satisfies it and then the ENTIRE
real runtime — chains, streaming, the ReAct agent, tool dispatch — runs with no API key. This is a
genuine strength: the model is cleanly abstracted, so offline/deterministic testing is first-class.
`schema.StreamReaderFromArray` makes a fake streaming model trivial.

### The dependency tree is heavy
`go get github.com/cloudwego/eino` is one line, but `go mod tidy` then pulls a surprisingly large
transitive set: `bytedance/sonic` (+ its asm loader and `golang.org/x/arch`), `nikolalohinski/gonja`
(a Jinja-style template engine, for prompt templates), `eino-contrib/jsonschema`,
`wk8/go-ordered-map`, `google/uuid`, and more. For a "just orchestrate my LLM calls" library this is
a lot of surface. It builds cleanly on Go 1.26, but the vendor footprint is non-trivial.

### Tool calling is the nicest part
`utils.InferTool("name", "desc", func(ctx, Req) (Resp, error))` reflects your request struct into a
`schema.ToolInfo` automatically (via jsonschema tags), and `react.NewAgent` wires the tool-call loop.
This is ergonomic and type-safe — on par with aisotropy's `WithTypedTool[T]`.

### The checkpoint/state API is powerful but has a learning curve
State persistence only happens at an **interrupt**. You build a `compose.NewGraph` with
`WithGenLocalState`, compile it with `WithCheckPointStore(...)` + `WithInterruptBeforeNodes(...)`, and
the first `Invoke` returns an interrupt error carrying an `InterruptInfo`; you resume with
`compose.Resume(ctx, interruptID)` + the same `WithCheckPointID`. Discovering that resume needs the
interrupt ID from the first run (not just the checkpoint ID) took reading the framework's own
`checkpoint_test.go`. Powerful for human-in-the-loop workflows; more machinery than "save/load a
session" if that's all you want.

### The isolation gap is real and reproduced
The whole point: `compose.CheckPointStore` is `Get/Set(ctx, checkpointID string)`. A single string
keys all state. Building Orbit, there was no place to put a tenant — I had to invent
`sessionKey(tenant,user,session)` myself, and the isolation smoke proves that if a developer keys the
checkpoint by a raw client session ID (forgetting the tenant), tenant B resuming the same ID gets
tenant A's secret. **Eino's real runtime produced the leak** — this is not a simulated map.

## The good

- **Fastest-moving Go agent framework** — high release velocity; if you need the latest LLM provider
  integration, Eino likely has it first (via `eino-ext`).
- **Clean model abstraction** — offline/deterministic testing is first-class (see stub above).
- **Real, type-safe tools + ReAct agent** — ergonomic and it just works.
- **Same language as aisotropy** — Go-fluent teams can evaluate it directly. Slots into the CloudWeGo
  stack (Eino + Kitex + Hertz). Coze Studio's runtime depends on Eino — production-grade validation.

## The painful

- **Zero multi-tenancy.** Persistence is a bare-string keyspace with no tenant dimension and no
  default store. For regulated multi-tenant SaaS this is disqualifying — you build (and can silently
  break) isolation yourself. Repo-wide "multi-tenant" search returns 0 results, ever.
- **0.x with breaking changes.** No stated v1.0 timeline; occasional flagged breaks. `WithCheckPointID`
  vs `WithWriteToCheckPointID`, deprecated `InterruptAndRerun`, etc. — the checkpoint API is still
  moving.
- **Heavy transitive dependencies** (see above) for what is nominally an orchestration library.
- **No fail-closed structured-output loop.** You get a JSON parser, not a bounded schema-repair retry.
- **No pre-spend cost governance.** Token usage is post-hoc via callbacks only.
- **Observability leans proprietary.** APMPlus (ByteDance/Volcengine), CozeLoop, etc.; no native OTel
  `gen_ai.*` semconv in core. Combined with ByteDance operation, a sovereignty red flag for Western
  regulated buyers.

## The verdict

Eino is the **velocity play** in Go — fastest-moving, most integrations, clean model abstraction,
genuinely good tools/agent ergonomics. But it is fundamentally an **orchestration library**: no
isolation model, no tiered memory, no pre-spend cost governance. Everything I built for Orbit's
tenancy was app-level scaffolding the framework neither provides nor protects.

**Best for:** Go-fluent teams building single-tenant or trusted-multi-tenant agents who want the
latest LLM integrations fast and don't need isolation guarantees.

**Worst for:** Regulated multi-tenant SaaS where isolation, cost governance, and sovereignty are
non-negotiable.

**vs. aisotropy:** Eino wins on Go-native velocity and integration breadth; aisotropy wins on
isolation-by-construction, the complete built-in memory/cost stack, and sovereignty.
