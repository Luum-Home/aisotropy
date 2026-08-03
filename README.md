# Aisotropy

**Multi-tenant isolation by construction + tiered memory — in a single Go binary.**

A local-first, sovereign AI-agent runtime in Go. A different `TenantID` *cannot* reach another
tenant's data — the unscoped read is **unrepresentable**, enforced per statement/key and propagated
by `context.Context`, not by a filter you have to remember to add. On top of that isolation spine
sits a memory hierarchy organized by access temperature (working → facts → semantic → offline
consolidation). Zero opaque abstractions ("anti-magic"), a 100% permissively-licensed stack (MIT/BSD/Apache-2.0), and **no
Docker, Rust, or Python in the default boot** — `go get`, `go run .`, done.

> **New here?** Start at [Getting Started](docs/getting-started.md) → the [Tutorial](docs/tutorial/index.md).
> The rest of this README is the one-page pitch and the runnable proof.

## The differentiator: isolation you don't have to audit

Every memory read is scoped to the tenant that owns it, *by the type system*, before it runs. The
identity arrives once — a `TenantID`/`UserID` that enters `context.Context` at the boundary and
scopes every tier below it — and there is **no consumer-facing way to construct a cross-tenant
read**. That is the moat: the [arena programme](arenas/README.md) exists to show no competing Go
agent framework builds it (one rival [leaked across tenants](arenas/genkit-go) when we reproduced it).

**This is proven, not asserted.** [`aisotropy/isolation_e2e_test.go`](aisotropy/isolation_e2e_test.go)
is a black-box test that boots the real HTTP surface with two tenants — deliberately reusing the
*same* `session_id` — writes a secret marker as tenant A, then asserts every other identity reading
that session (tenant B, and other users) can never see it, across working memory, facts, and
semantic hydration, under both sequential and concurrent load. Run the proof:

```bash
git clone https://github.com/Luum-Home/aisotropy && cd aisotropy
go test -run 'TestIsolation.*NoCrossTenantLeak' ./aisotropy -v
# PASS — a different TenantID never surfaces another tenant's data (keyless stub LLM, no credentials)
```

## Quickstart: build your own agent (no Docker, no key, no server)

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

## Why Go — the audience *is* the differentiator

The Python and TypeScript worlds have great agent frameworks — Pydantic AI, Mastra, LangGraph. If you
want a quick chatbot, use one of those. Aisotropy is for a different job, in a different language:
**you're building the backend or platform that *runs other people's agents*** — per-tenant, embedded
in your Go service, at scale.

**Go has agent libraries, but no agent _runtime_** where multi-tenant isolation is **structural, not
bolted on**. Eino (ByteDance) is serious orchestration tied to CloudWeGo; Genkit-Go (Google) is
Firebase-tethered with convention-based multi-tenancy we [broke in the arena](arenas/genkit-go);
ADK-Go / LangChainGo are orchestration with no security spine. In Go, the people building agents are
backend, infra, and platform engineers who already think in single binaries, permissive licensing,
and tenant boundaries — Aisotropy's edges are their defaults. The full argument (competitor table,
the honest not-yet-market-validated caveat) is [`docs/vision/why-go.md`](docs/vision/why-go.md).

---

## Optional flagship capability: key-brokering (`RunTask`) — run untrusted agents that never touch your key

This is the sharpest **demonstration of the moat**: untrusted agentic execution where the provider
key never enters the sandbox. Every other agent framework hands your API key to the agent. Aisotropy
**brokers** it — the model call runs host-side (the key is spent in *your* process), and only the
agent's actions (shell, file writes, git) run inside a throwaway Docker sandbox that never receives
`ANTHROPIC_API_KEY`. Proven against real Docker (`TestRunTask_BrokerInvariant_KeyNeverEntersContainer`:
`docker exec printenv` never shows the key while the task still completes and commits).

```go
// Your API key lives ONLY in this host process. Aisotropy runs the model here;
// the agent's actions run inside a throwaway Docker sandbox that never gets the key.
app := aisotropy.New()

res, err := app.RunTask(context.Background(), aisotropy.TaskRequest{
	RepoDir: "./my-repo",
	Prompt:  "Add a CHANGELOG.md summarizing recent commits, then commit it.",
	Scopes:  aisotropy.SandboxToolScopes(), // run_shell / write_file / read_file / git_commit
})
```

**This is an opt-in module, not part of the default boot.** It is the one capability that needs a
**container runtime (Docker or Podman) at call time**, so it is compiled behind a build tag to keep
the default binary Docker-unaware:

```bash
go build -tags keybroker ./...     # RunTask / RunTasks + the `aisotropy task` CLI
```

Without `-tags keybroker`, `App.RunTask` / `App.RunTasks` and the `aisotropy task` subcommand are
not compiled in — the default runtime is exactly the isolation + memory substrate above, with no
Docker intent anywhere in the binary. Enabling the tag is an **API change** (it adds the `RunTask`
surface). See [ADR-0036](docs/decisions/0036-sandboxed-task-agent-key-brokering.md), the build-tag
note in [`docs/deployment/index.md`](docs/deployment/index.md), the runnable
[`examples/task-agent`](examples/task-agent), and the [key-brokering arena](arenas/key-brokering)
that situates it against E2B / Daytona / the coding-CLI-in-a-box pattern.

---

## Go deeper

- **[Reality ledger](docs/architecture/reality-ledger.md)** — the single source of truth for what
  runs by default vs. what is dormant behind an env var. Read this before treating any capability as live.
- **[Getting Started](docs/getting-started.md)** and **[the Tutorial](docs/tutorial/index.md)** —
  the 30-second pitch, a keyless 2-minute agent, and a 10-rung ladder.
- **[The Capability Tour](docs/tour.md)** — 12 stops across the whole surface, each one a command
  you can run plus the evidence behind it (test, arena cell, measured number, ADR). Includes the
  dimensions where Aisotropy *loses*, taken verbatim from the same arena board as the wins.
- **[The stack](docs/architecture/overview.md)** (closed, 100% permissive: MIT/BSD/Apache-2.0): Huma (Go/MIT) for
  struct-as-contract HTTP + OpenAPI 3.1 + SSE; Gollem (Go/MIT) for the in-process reflection loop;
  BadgerDB (Go/Apache-2.0) working memory; Qdrant (Rust/Apache-2.0) and rqlite (Go/MIT) as the
  optional networked scale tiers; Apache DataFusion (Rust) for the offline "sleep-cycle." Golden
  rule: in-process (Go) where latency matters, dedicated services (Rust) where scale matters.
- **[The "LLM OS" model](docs/architecture/overview.md)** — Aisotropy modeled as an OS whose CPU is
  the LLM (Karpathy's mental model): Huma is the syscall layer, Gollem the kernel, `context.Context`
  the per-tenant process descriptor, and memory a hierarchy by temperature.
- **[The compression thesis](docs/vision/compression-thesis.md)** — the research north star
  (Shannon → Hutter → Sutskever; Bellard's `nncp`/`ts_zip`): *compression = prediction =
  intelligence*, with six bets scored in bits ([`docs/research/`](docs/research/index.md)). This is
  vision, not shipped runtime — see the reality ledger.
- **[The arenas](arenas/README.md)** — a 43-row competitive matrix across 12 frameworks in 5
  languages, the evidence base for the isolation claim.
- **[Why an *enterprise* agent needs this](docs/vision/thesis.md)** — token efficiency,
  hallucination governance, standard observability, real multi-tenant isolation.

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
  `cmd/aisotropyd` is a thin ~97-line consumer of it, and third parties build their own bot by
  importing `aisotropy/`.
- **Phase 9 — composition surface (partial):** typed tool args (`WithTypedTool[T]`), MCP-backed
  tools (`WithMCPServer`), the embeddable in-process [`App.Turn`](docs/decisions/0031-embeddable-in-process-turn.md)
  (call the full pipeline like a function, no HTTP hop), a reference
  [`docker-compose.yml`](docs/deployment/docker-compose.md) deployment that boots aisotropyd with
  rqlite + Qdrant active, wiring-only YAML/JSON config (`LoadConfigFile`/`OptionsFromConfig`), and
  the opt-in key-brokering module (`-tags keybroker`, above) are delivered. The broader L2
  composition SDK is pending.

The detailed ledgers are [docs/roadmap/milestones.md](docs/roadmap/milestones.md) (per-phase
definition of done) and [docs/architecture/reality-ledger.md](docs/architecture/reality-ledger.md)
(what runs by default vs. what is dormant behind an env var).

## Running the reference binary

The repo also ships `cmd/aisotropyd` — a thin ~97-line consumer of the same `aisotropy.App` builder
(with demo tools) that doubles as the runnable reference. Requires Go 1.26+ (pinned via
`.go-version`).

```bash
# Build (default: no Docker intent). Add -tags keybroker for the RunTask surface.
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

[Apache License 2.0](LICENSE) © 2026 Matías Nahuel Amendola — see [`NOTICE`](NOTICE) and
[ADR-0080](docs/decisions/0080-project-license-apache-2.md) for why (express patent grant,
trademark reservation, inbound-contribution terms). One exception, called out in `NOTICE`:
`bench/rag-quality/testdata/xquad_slice.json` is CC-BY-SA-4.0.
</content>
</invoke>
