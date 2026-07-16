# Aisotropy

**Run untrusted AI agents that never touch your API keys.**
Multi-tenant isolation, sandboxing, and key brokering — built into a single Go binary.

A multi-tenant, local-first, sovereign AI-agent runtime in Go + Rust. Aisotropy is not "a chat with
an LLM." It's a precision-engineering runtime where a **single Go struct is the source of truth**
that governs — all at once — HTTP validation, the OpenAPI 3.1 schema, and the contract enforced on
the LLM. Zero opaque abstractions ("anti-magic"), a 100% MIT/Apache-2.0 stack, and a memory system
organized by access temperature.

## The differentiator: an agent that never holds your key

Every other agent framework hands your API key to the agent. Aisotropy **brokers** it: the model
call runs host-side (the key is spent in *your* process), and only the agent's actions — shell,
file writes, git — run inside a throwaway Docker sandbox that never receives `ANTHROPIC_API_KEY`.
Proven against real Docker (`TestRunTask_BrokerInvariant_KeyNeverEntersContainer`: `docker exec
printenv` never shows the key while the task still completes and commits). See
[ADR-0036](docs/decisions/0036-sandboxed-task-agent-key-brokering.md) and the runnable
[`examples/task-agent`](examples/task-agent).

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	// Your API key lives ONLY in this host process. Aisotropy runs the model here;
	// the agent's actions run inside a throwaway Docker sandbox that never gets the key.
	app := aisotropy.New() // real provider from the env, or the keyless stub

	res, err := app.RunTask(context.Background(), aisotropy.TaskRequest{
		RepoDir: "./my-repo",
		Prompt:  "Add a CHANGELOG.md summarizing recent commits, then commit it.",
		Scopes:  aisotropy.SandboxToolScopes(), // run_shell / write_file / read_file / git_commit
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("the agent made %d commit(s) on %q — and never saw your API key\n",
		len(res.Commits), res.Branch)
}
```

> Bring the sophistication of Pydantic AI (strict types + a reflection loop) to the physical
> performance of Go + Rust, with tiered memory and granular control, without a single line of
> opaque abstraction.

## Why aisotropy — and why Go

The Python and TypeScript worlds have great agent frameworks — Pydantic AI, Mastra, LangGraph. If you
want a quick chatbot, use one of those. Aisotropy is for a different job, in a different language.

**Go has agent libraries, but no agent _runtime_.** None of them is an _isolation-first,
self-hostable runtime_ where multi-tenant isolation, sandboxing, and key-brokering are **structural,
not bolted on**:

| In Go | But |
|---|---|
| **Eino** (ByteDance) | serious orchestration, tied to the CloudWeGo/Volcengine ecosystem; no isolation-by-construction, no key-brokering |
| **Genkit-Go** (Google) | Firebase-tethered; multi-tenancy is convention — we [reproduced a cross-tenant leak](arenas/genkit-go) in the arena |
| **ADK-Go** (Google), **LangChainGo** | orchestration / a thin community port; no security spine |

**The audience and the differentiator line up.** In Python you'd be selling isolation to people who
want magic. In Go, the people building agents are backend, infra, and platform engineers — the ones
who already think in single binaries, permissive licensing, tenant boundaries, and production ops.
Aisotropy's edges are their defaults.

**So aisotropy is for one job:** you're building the backend or platform that _runs other people's
agents_ — per-tenant, per-task, sometimes untrusted — embedded in your Go service, at scale. That is
the pain no Go framework covers. Aisotropy does: `app.Turn` for in-process turns, `app.RunTask` for
sandboxed key-brokered tasks, and tenant isolation by construction under all of it.

> The full argument — the competitor table, why the audience aligns in Go, and the honest
> not-yet-market-validated caveat — is [`docs/vision/why-go.md`](docs/vision/why-go.md).

## Quickstart: build your own agent

Aisotropy is a **library first**: the runtime is a value you build once and **call like a
function** — `app.Turn(ctx, in)` runs one full chat turn in-process, no HTTP server in sight (the
Aisotropy answer to Pydantic AI's `run_sync`). **No API key and no database are required to boot** —
with no LLM credential the runtime falls back to a *keyless stub* (a canned response, Aisotropy's
TestModel equivalent), and the in-process memory tiers default to in-memory. The program below runs
as-is.

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

The first `Turn` boots the shared runtime (memory tiers, guardrails, gateway, cost meter, rate
limiter, background workers) and every later `Turn` reuses it, so working memory persists between
calls and isolation between tenants holds **by construction** — a different `TenantID` can never
read this one's data.

**Add a tool** — a typed Go function is the whole ergonomic surface (Aisotropy's answer to
`@agent.tool`). Its `jsonschema` tags drive BOTH the JSON Schema the model is shown and the
validate/decode of the model's arguments before your handler runs:

```go
type forecastArgs struct {
	City string `json:"city" jsonschema:"description=the city to look up"`
}

app := aisotropy.New(
	aisotropy.WithTypedTool("get_weather", "look up the weather for a city", "",
		func(ctx context.Context, args forecastArgs) (string, error) {
			tenant, _ := aisotropy.TenantFromContext(ctx) // read-only identity
			return fmt.Sprintf("Sunny, 22°C in %s (tenant %s)", args.City, tenant), nil
		}),
)
```

**Serve it over HTTP instead** — when you want the network boundary (a remote client, other
services), the SAME runtime runs behind an HTTP contract via `app.Run(ctx)`. Over HTTP identity
arrives from an untrusted client, so an authenticator is required — the runtime is **default-deny**
(every `/v1` request is 401) until you configure one:

```go
app := aisotropy.New(
	// NoAuth trusts the URL path identity — local/dev only. In production swap it
	// for aisotropy.NewJWTAuthenticator(secret) (verifies + binds must-agree to the path).
	aisotropy.WithAuthenticator(aisotropy.NoAuth()),
)
if err := app.Run(context.Background()); err != nil { // serves :8080, blocks
	log.Fatal(err)
}
```

```bash
go run .          # serves :8080
# tenant + user are path params, product/session in the body:
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"weather in Rosario?","product_id":"mybot","session_id":"s1"}'
```

Set `ANTHROPIC_API_KEY` (or `OPENROUTER_API_KEY`) to talk to a real model — nothing else changes,
in-process or over HTTP. Full 5-minute walkthrough (in-process turns → typed tools → persona →
memory → serve over HTTP): [Build your own agent](docs/guides/build-your-own-agent.md).

## Why

Building an *enterprise* AI agent — not a demo — demands four things that "magic" frameworks
sabotage: **token efficiency**, **hallucination governance**, **standard observability**, and
**real multi-tenant isolation**. Aisotropy's stance: *you build the payload, you see every token.*
Full rationale in [docs/vision/thesis.md](docs/vision/thesis.md).

## The stack (closed, 100% MIT/Apache-2.0)

| Layer | Technology | Language / License | Role |
|---|---|---|---|
| API & contracts | [Huma](https://github.com/danielgtaylor/huma) | Go / MIT | HTTP validation, OpenAPI 3.1, SSE with `ctx` cancellation |
| AI orchestration | [Gollem](https://github.com/fugue-labs/gollem) | Go / MIT | In-process cognitive engine, structured output, reflection loop |
| Working memory | [BadgerDB](https://github.com/dgraph-io/badger) | Go / Apache-2.0 | Live chat + summary buffer (µs); local transactional outbox |
| Semantic memory | [Qdrant](https://github.com/qdrant/qdrant) | Rust / Apache-2.0 | Vectors with per-tenant payload filtering during the HNSW scan |
| Facts memory | [rqlite](https://github.com/rqlite/rqlite) | Go / MIT | Engram-style task/rule journal, SQLite + FTS5, networked |
| Batch analytics | [Apache DataFusion](https://github.com/apache/datafusion) | Rust / Apache-2.0 | Offline "sleep-cycle" consolidation |

**Golden rule:** in-process (Go) where latency matters; dedicated services (Rust) where scale
matters. The only heavy service meant to scale independently is Qdrant.

## The "LLM OS" model

Aisotropy is modeled as an operating system whose CPU is the LLM (Karpathy's mental model):
Huma is the **syscall layer**, Gollem is the **kernel**, `context.Context` is the **process
descriptor** that isolates each tenant, and memory is a **hierarchy by temperature** — BadgerDB
(RAM) → rqlite (disk) → Qdrant (long-term associative) → DataFusion (sleep-cycle consolidation).
See [docs/architecture/overview.md](docs/architecture/overview.md) and
[docs/architecture/memory-tiers.md](docs/architecture/memory-tiers.md).

## The differentiator: compression as a cognitive layer

Aisotropy's research core treats **memory, retrieval, routing, and evaluation as one thing:
compression**. Following the Shannon → Hutter → Sutskever line (and Fabrice Bellard's `nncp`/
`ts_zip`), *compression = prediction = intelligence*: how many bits the system needs to predict
a user is the objective measure of how well it knows them. See the manifesto in
[docs/vision/compression-thesis.md](docs/vision/compression-thesis.md) and the six actionable
bets (with success criteria measured in bits) in [docs/research/](docs/research/index.md).

## Status

**Phases 0–7 of the [roadmap](docs/roadmap/index.md) are implemented, Phase 8 shipped, and
Phase 9 is partially delivered:**

- **Phases 0–7 — runtime:** typed contracts (Huma over chi, autogenerated `/openapi.json`), SSE
  streaming with `ctx` cancellation and a structured-output reflection loop, working memory
  (BadgerDB + transactional outbox), facts memory (in-process SQLite+FTS5, rqlite via
  `RQLITE_URL`), semantic memory (in-process cosine index, Qdrant via `QDRANT_HOST`), the
  DataFusion sleep cycle (dormant unless `AISOTROPY_SLEEPCYCLE_BIN` is set), and granular
  control (per-tenant rate limit, circuit breaker, cost meter, scope-gated tools).
- **Phase 8 — adoption surface (shipped):** the public builder `aisotropy.App`
  ([ADR-0013](docs/decisions/0013-public-extension-surface-facade-vs-promote.md));
  `cmd/aisotropyd` is a thin ~51-line consumer of it, and third parties build their own bot by
  importing `aisotropy/`.
- **Phase 9 — composition surface (partial):** typed tool args (`WithTypedTool[T]`), MCP-backed
  tools (`WithMCPServer`), the embeddable in-process [`App.Turn`](docs/decisions/0031-embeddable-in-process-turn.md)
  (call the full pipeline like a function, no HTTP hop), a reference
  [`docker-compose.yml`](docs/deployment/docker-compose.md) deployment that boots aisotropyd with
  rqlite + Qdrant active, and wiring-only YAML/JSON config (`LoadConfigFile`/`OptionsFromConfig`)
  are delivered. The broader L2 composition SDK is pending.

The detailed ledgers are [docs/roadmap/milestones.md](docs/roadmap/milestones.md) (per-phase
definition of done) and [docs/architecture/reality-ledger.md](docs/architecture/reality-ledger.md)
(what runs by default vs. what is dormant behind an env var).

## Running the reference binary

The repo also ships `cmd/aisotropyd` — a thin ~50-line consumer of the same `aisotropy.App` builder
(with demo tools) that doubles as the runnable reference. Requires Go 1.26+ (pinned via
`.go-version`).

```bash
# Build
go build ./...

# Run (keyless stub — no credentials needed, streams a canned response)
go run ./cmd/aisotropyd
# → serves :8080  ·  GET /healthz  ·  GET /openapi.json  ·  POST /v1/tenant/{tenant_id}/user/{user_id}/chat[/stream]

# Run with a real model via OpenRouter (OpenAI-compatible; also reaches Google/OpenAI model ids)
export OPENROUTER_API_KEY=sk-or-...
export OPENROUTER_MODEL=qwen/qwen3.7-plus   # optional; this is the default
go run ./cmd/aisotropyd

# Stream a chat (SSE) — tenant + user in the path, session in the body
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"hello","product_id":"analytics-v1","session_id":"s1"}'
```

Provider selection is by environment precedence: `OPENROUTER_API_KEY` → `OPENAI_API_KEY`
(+ optional `OPENAI_BASE_URL`) → `ANTHROPIC_API_KEY` → keyless stub.

Other environment variables:
- `AISOTROPY_DATA_DIR` — directory for the working-memory store (BadgerDB). Unset → in-memory
  (state is lost on restart; fine for the walking skeleton). Set it to persist chat + summary
  state across restarts.
- `OTEL_EXPORTER_OTLP_ENDPOINT` — enable OpenTelemetry trace export (OTLP/HTTP). Unset → a no-op
  tracer, so no collector is required.

## Developer console

```bash
go run ./cmd/x          # interactive TUI: fmt / vet / lint / test / build / bench / run / check
go run ./cmd/x check    # non-interactive: run the quality gates (build + fmt + vet + test); suitable for CI or a local pre-commit gate
go run ./cmd/x list     # list all tasks
```
`lint` uses `golangci-lint` (config in `.golangci.yml`); install it with
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`.

## Documentation

The [`docs/`](docs/index.md) directory is a structured knowledge base (an
[Open Knowledge Format](docs/vision/principles.md) bundle — markdown + YAML frontmatter,
cross-linked, both human- and agent-readable):

- [Vision](docs/vision/index.md) — thesis, design principles, the compression manifesto, glossary
- [Architecture](docs/architecture/index.md) — LLM OS, request lifecycle, tiered memory, consistency, multi-tenancy, granular control, observability
- [Components](docs/components/index.md) — deep dives per stack piece
- [Decisions (ADRs)](docs/decisions/index.md) — architecture decisions with trade-offs
- [Research bets](docs/research/index.md) — the compression layer, six bets scored in bits
- [Roadmap](docs/roadmap/index.md) — MVP-first phases, OKRs, and milestones

## License

[Apache-2.0](LICENSE) © 2026 Luum.
