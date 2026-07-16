---
type: Section Index
title: The Tutorial
description: A 10-step ladder from a keyless hello agent to a served, extensible, multi-tenant Aisotropy runtime — each rung a short lesson with a runnable example and its ADR.
tags: [tutorial, ladder, onboarding, examples]
timestamp: 2026-07-16T00:00:00Z
---

# The Tutorial

Ten rungs from "hello agent" to a served, multi-tenant, extensible runtime. Each lesson is short: it
states **one goal**, shows the **minimal real-API snippet**, and links its **runnable example** plus
the **ADR** that carries the design depth.

Take them in order — each adds exactly one layer over the last. Before you start, read
[Concepts](../concepts.md) for the mental model; the rungs below make it concrete.

Every example runs **keyless** (the stub model — see each lesson's honesty note) unless it needs
Docker, which is called out. Set a real provider key (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` /
`OPENROUTER_API_KEY`) to turn any stubbed run into a real one — no code change.

## The ladder

| # | Lesson | You'll learn | Example |
|---|---|---|---|
| 00 | [Hello agent](00-hello-agent.md) | `App.Turn` — one chat turn in-process, keyless | [`weatherbot`](../../examples/weatherbot) |
| 01 | [A typed tool](01-typed-tool.md) | `WithTypedTool` — a Go struct as the tool contract | [`weatherbot`](../../examples/weatherbot) |
| 02 | [Memory & sessions](02-memory-sessions.md) | what persists across turns, and how | [`memory-sessions`](../../examples/memory-sessions) |
| 03 | [Multi-tenant isolation](03-multi-tenant.md) | isolation by construction — the core | [`multi-tenant`](../../examples/multi-tenant) |
| 04 | [Auth & scopes](04-auth-scopes.md) | `WithAuthenticator` + JWT + scope-gated tools | [`auth-scopes`](../../examples/auth-scopes) |
| 05 | [Knowledge & RAG](05-knowledge-rag.md) | `IngestKnowledge` + retrieval-augmented turns | [`knowledge-rag`](../../examples/knowledge-rag) |
| 06 | [Sandboxed tasks](06-sandboxed-task.md) | `App.RunTask` — key-brokering (needs Docker) | [`task-agent`](../../examples/task-agent) |
| 07 | [Concurrent tasks](07-concurrent-tasks.md) | `App.RunTasks` — parallel fan-out (needs Docker) | [`task-fanout`](../../examples/task-fanout) |
| 08 | [Extend the runtime](08-extend.md) | plugins + a custom `SandboxProvider` | [`plugin-template`](../../examples/plugin-template) |
| 09 | [Serve over HTTP](09-serve-http.md) | `App.Run` — the network boundary, curl, deploy | [`serve-http`](../../examples/serve-http) |

> Rungs 00–01 and 06–08 ship with runnable examples in this repo today. Rungs 02–05 and 09 have their
> runnable companions landing alongside this tutorial; until each is in place, the lesson's snippet is
> complete and correct against the current public API — you can paste it into your own module and run it.

## After the ladder

- [Build your own agent](../guides/build-your-own-agent.md) — the reference field manual.
- [Writing a plugin](../guides/writing-a-plugin.md) — ship a capability without recompiling the core.
- [Architecture](../architecture/index.md) — the full concept reference behind every rung.
