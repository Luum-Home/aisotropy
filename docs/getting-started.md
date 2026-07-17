---
type: Guide
title: Getting Started
description: The newcomer front door to Aisotropy — what it is in 30 seconds, a keyless 2-minute hello agent, and the pointer into the tutorial ladder.
tags: [getting-started, tutorial, adoption, onboarding]
timestamp: 2026-07-16T00:00:00Z
---

# Getting Started

New to Aisotropy? You're in the right place. This is the guided front door over the
[knowledge base](index.md); when you want the deep, architecture-first reference, follow the
links out from here.

Two next steps once you finish this page:

1. [Concepts](concepts.md) — the one-page mental model (how to *think* about Aisotropy before you learn the API).
2. [The Tutorial](tutorial/index.md) — a 10-step ladder, each rung a short lesson with a runnable example.

## What Aisotropy is, in 30 seconds

Aisotropy is a **multi-tenant AI-agent runtime in a single Go binary**. It is *not* a chatbot and
*not* a thin LLM wrapper. One property defines it, and it is **structural, not bolted on**:

- **Isolation by construction.** A tenant's identity travels only through `context.Context`, injected
  once at the boundary, and every memory tier is scoped from that ctx. There is **no consumer-facing
  way to construct a cross-tenant read** — a different `TenantID` *cannot* reach this one's data, and
  the API surface makes the unscoped read unrepresentable. On top of that spine sits a **memory
  hierarchy by access temperature** (working → facts → semantic → offline consolidation).

If you're building the backend or platform that *runs other people's agents* — per-tenant,
embedded in your Go service, at scale — that is the job Aisotropy is for. The **default boot needs no
Docker, Rust, or Python**: `go get`, `go run .`, done. (One opt-in flagship module — key-brokering —
adds untrusted sandboxed execution and needs a container runtime; it is covered at the end of this page.)

## The 2-minute hello agent (keyless)

Aisotropy is a **library first**: you build the runtime once and **call it like a function**.
`app.Turn(ctx, in)` runs one full chat turn in-process — no HTTP server, no port. And **no API key
and no database are required to boot**: with no LLM credential the runtime falls back to a *keyless
stub* (a canned response — the honest "it wires end-to-end, but a real key does real work" default),
and the memory tiers default to in-memory. The program below runs as-is.

```bash
go mod init example.com/mybot
go get github.com/Luum-Home/aisotropy/aisotropy@latest
```

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	// Build the runtime once. With no options it uses the keyless stub LLM
	// (no ANTHROPIC_API_KEY) and in-memory stores. Close releases it at exit.
	app := aisotropy.New()
	defer func() { _ = app.Close() }()

	// Call it like a function. TenantID/UserID are the isolation identity: they
	// enter context.Context inside Turn and scope every memory tier — never a body
	// field. Reuse SessionID across calls to keep a conversation's working memory.
	out, err := app.Turn(context.Background(), aisotropy.ChatInput{
		TenantID:  "acme",
		UserID:    "alice",
		SessionID: "s1",
		Prompt:    "weather in Rosario?",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Reply) // out.Stub == true while no real model is configured
}
```

```bash
go run .   # prints a reply — no server, no port, no credentials
```

`out.Stub` is `true` here because no real model is configured: the stub proves the whole pipeline
runs keyless. Export `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY` / `OPENROUTER_API_KEY`) and the same
`Turn` call drives a real model — no code change. The runnable version of this is
[`examples/weatherbot`](../examples/weatherbot).

## Optional flagship capability: key-brokering (`RunTask`)

The hello agent above is a chat turn on the isolation substrate. The sharpest *demonstration* of that
substrate shows up when an **untrusted** agent *acts* on a repository. `app.RunTask` runs a sandboxed,
agentic coding task — the model call stays host-side, so the container gets the agent's **hands,
never its wallet**:

```go
// Your API key lives ONLY in this host process. Aisotropy runs the model here;
// the agent's actions run inside a throwaway sandbox that never gets the key.
app := aisotropy.New()

res, err := app.RunTask(context.Background(), aisotropy.TaskRequest{
	RepoDir: "./my-repo",
	Prompt:  "Add a CHANGELOG.md summarizing recent commits, then commit it.",
	Scopes:  aisotropy.SandboxToolScopes(), // run_shell / write_file / read_file / git_commit
})
```

There is **no field on `TaskRequest` for a provider key** — by design. The broker invariant is proven
against real Docker (a `docker exec printenv` never shows the key while the task still completes and
commits).

**This is an opt-in module, not part of the default boot.** It is the one capability that needs a
**container runtime (Docker or Podman) at call time**, so it is compiled behind a build tag —
`go build -tags keybroker ./...` — to keep the default binary Docker-unaware. Without the tag,
`App.RunTask`/`App.RunTasks` and the `aisotropy task` CLI are not compiled in; enabling it is an API
change that adds the `RunTask` surface. See [ADR-0036](decisions/0036-sandboxed-task-agent-key-brokering.md),
the build-tag note in [deployment](deployment/index.md), and the runnable
[`examples/task-agent`](../examples/task-agent).

## Next: the tutorial

You've seen the substrate (`Turn` + isolation by construction) and its flagship demonstration
(`RunTask`, opt-in behind `-tags keybroker`). To go from here to a real bot, take the ladder one rung
at a time:

- Start with the [Concepts](concepts.md) page for the mental model.
- Then work through [the Tutorial](tutorial/index.md) — 10 short lessons, each with a runnable example.

For the reference field manual, see [Build your own agent](guides/build-your-own-agent.md).
