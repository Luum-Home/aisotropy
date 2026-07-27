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
3. [The Capability Tour](tour.md) — if you would rather see the whole surface first: 12 stops, each
   one a command you can run plus the evidence behind it (test, arena cell, measured number, ADR).

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

## Two ways to adopt it — and which to pick

Aisotropy is **one engine exposed through two faces** (the dual surface of
[ADR-0016](decisions/0016-dual-surface-sealed-runtime-composition-sdk.md), built on the sealed
façade of [ADR-0013](decisions/0013-public-extension-surface-facade-vs-promote.md)). Both faces run
the *same* pipeline — there is no second implementation to drift
([ADR-0031](decisions/0031-embeddable-in-process-turn.md)). The golden rule holds for both: a
consumer only ever imports `github.com/Luum-Home/aisotropy/aisotropy`, never `internal/*` — and
because they are separate Go modules, the compiler *physically* forbids reaching a private package,
so "public surface only" is enforced by `go build`, not by convention.

- **Face A — library / SDK (Go).** `go get` the module, build the runtime once with the `App`
  builder (`WithTool` / `WithToolExecutor` / `WithPersona` / `WithLLM` / `WithAuthenticator` / …),
  then run it either **in-process** (`app.Turn(ctx, in)` — one chat turn as a function call, no
  server, tools executed in µs) or **served** (`app.Run(ctx)` — the same runtime over HTTP with
  Huma/chi, SSE, `/health`, `/costs`, graceful shutdown). The stock `cmd/aisotropyd` binary is
  itself a ~97-line consumer of this exact builder (the runtime is dogfooded through its own public
  surface). Rungs 00–07 of the [tutorial](tutorial/index.md) are in-process; rung 09
  (`examples/serve-http`) serves.
- **Face B — standalone service.** Run the prebuilt `aisotropyd` (or `docker compose up`) and talk
  to it over the HTTP API (`POST /v1/tenant/{t}/user/{u}/chat`, `.../chat/stream`, …) from a client
  in **any language**. Because the Go struct *is* the OpenAPI 3.1 contract (Huma), you codegen a
  typed client straight from `/openapi.json`.

Which is optimal depends on the integrator:

| You are… | Optimal path | Why |
|---|---|---|
| Writing **Go**, want deep control — custom tools executed in-process, custom persona, embedded in your own service | **Face A (library)** — `go get` + `App` builder, `app.Turn` (or `app.Run` to serve) | one binary, tools in µs, no network hop, tenant isolation carried natively by `context.Context` |
| **Not Go** (Python / TS / …), or you want Aisotropy as a separate deployable | **Face B (service)** — run `aisotropyd`, call the HTTP API | language-decoupled; the OpenAPI 3.1 contract gives you client codegen for free |

**Key detail either way:** the memory backends (working / facts / semantic) are selected **by
environment variable, not by code injection** — deliberately, because exposing `facts.Store` in the
public API would leak the cross-tenant `ExportAll`. So "optimal" always means: import the façade,
bring your own tools / persona / LLM / authenticator, and choose the backend tier via env. And past
localhost, an authenticator is **required** — the runtime refuses to boot unauthenticated on a
non-loopback bind (see [deployment](deployment/index.md)).

## Next: the tutorial

You've seen the substrate (`Turn` + isolation by construction) and its flagship demonstration
(`RunTask`, opt-in behind `-tags keybroker`). To go from here to a real bot, take the ladder one rung
at a time:

- Start with the [Concepts](concepts.md) page for the mental model.
- Then work through [the Tutorial](tutorial/index.md) — 10 short lessons, each with a runnable example.

For the reference field manual, see [Build your own agent](guides/build-your-own-agent.md).
