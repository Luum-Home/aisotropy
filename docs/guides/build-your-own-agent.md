---
type: How-To
title: Build your own agent on Aisotropy
description: Define a bot on top of the Aisotropy runtime from your own Go module — register a tool, a real executor, and a persona against the public `aisotropy` package, without forking or editing the composition root.
tags: [guide, howto, adoption, extension, phase-8, facade, builder]
timestamp: 2026-07-09T00:00:00Z
---

# Build your own agent on Aisotropy

Aisotropy is a multi-tenant agent **runtime** ([what it is](../architecture/what-aisotropy-is.md)).
You don't fork it or edit its composition root — you **import one package** and register your bot's
*domain* (its tools, their behavior, and its persona). Everything else — tiered memory, tenant
isolation, quotas, circuit breaker, cost metering, SSE streaming, graceful shutdown — you inherit
for free.

This guide uses only the public package `github.com/Luum-Home/aisotropy/aisotropy`. If you find
yourself importing anything under `internal/`, you've left the supported surface — Go will refuse to
compile it from another module, by design ([ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)).

> **Stability:** the `aisotropy` package is **0.x** — additive within a minor version, with any
> breaking change called out in the CHANGELOG on a minor bump. Pin a version.

## What you supply vs. what you inherit

| You supply (domain) | Aisotropy provides (runtime) |
|---|---|
| Tool declarations (`aisotropy.Tool`) | Scope-gated tool injection into the LLM payload |
| A real `aisotropy.ToolExecutor` | The bounded tool-use loop + friction recording |
| A persona (`aisotropy.Persona`, per `product_id`) | Working/facts/semantic memory hydration |
| (optional) LLM client, backends, quota numbers | Tenant isolation, quotas, breaker, cost meter, OTel, SSE, shutdown |

## 1. Create your module

```
mkdir weatherbot && cd weatherbot
go mod init example.com/weatherbot
go get github.com/Luum-Home/aisotropy/aisotropy@latest
```

## 2. Write a real tool executor

The executor is the behavior behind a tool name. It reads the caller's identity from `ctx` — it can
**read** the tenant, never forge it (the runtime owns tenant scoping; you cannot cross tenants).

```go
package main

import (
	"context"
	"fmt"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

type weatherExecutor struct{ apiKey string }

func (w weatherExecutor) Execute(ctx context.Context, name string, args map[string]string) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx) // read-only identity
	if name != "get_weather" {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	city := args["city"]
	// ... call your real weather API, scoped to `tenant` for your own side effects ...
	return fmt.Sprintf("It is 22°C and clear in %s (tenant %s).", city, tenant), nil
}
```

## 3. Register and run

```go
func main() {
	app := aisotropy.New(
		aisotropy.WithTool(aisotropy.Tool{
			Name:          "get_weather",
			Description:   "look up current weather for a city",
			RequiredScope: "read:weather", // omit RequiredScope for an always-available tool
		}),
		aisotropy.WithToolExecutor(weatherExecutor{apiKey: "..."}),
		aisotropy.WithPersona("weatherbot-v1", aisotropy.Persona{
			BasePrompt: "You are WeatherBot, a concise meteorology assistant.",
		}),
		aisotropy.WithRateLimit(10, 30), // per-second, burst
	)

	if err := app.Run(context.Background()); err != nil {
		panic(err)
	}
}
```

`app.Run` boots the full runtime and blocks until `SIGINT`/`SIGTERM`, draining in-flight requests
and flushing memory on shutdown.

### Typed variant (2026-07-14)

`WithTool` + `WithToolExecutor` (string-map args) still works, but the surface now also ships
`WithTypedTool[T]` (ADR-0016 lever #1): your tool's arguments become a Go struct whose
`json`/`jsonschema` tags drive both the schema the model is shown and the decode/validation of what
it returns — no executor switch, no string-map parsing:

```go
type getWeatherArgs struct {
	City  string `json:"city" jsonschema:"description=the city to look up"`
	Units string `json:"units" jsonschema:"enum=c,enum=f,description=temperature units"`
}

func getWeather(ctx context.Context, args getWeatherArgs) (string, error) { /* ... */ }

app := aisotropy.New(
	aisotropy.WithTypedTool("get_weather", "get the current weather for a city", "read:weather", getWeather),
	// ...
)
```

The repo's [`examples/weatherbot`](../../examples/weatherbot) uses this typed form. Tools whose
behavior lives in an external process can instead be wired with
`WithMCPServer(requiredScope, command, args...)` (ADR-0016 lever #2) — no Go handler at all.

## 4. Talk to your bot

The HTTP contract is the runtime's, unchanged. Persona is selected by `product_id`:

```
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"weather in Rosario?","product_id":"weatherbot-v1","scopes":["read:weather"]}'
```

- `tenant_id` / `user_id` are path params (isolation boundary, validated before your code runs).
- `scopes` gate tools: without `read:weather`, `get_weather` is **never injected** into the payload —
  the model isn't told it exists (governance by absence of information).
- `product_id` selects your registered persona; an unregistered `product_id` falls back to the
  default base prompt.

## The rules you inherit (and can't opt out of)

- **Tenant isolation by construction** — you read identity from `ctx`; you cannot construct a memory
  scope or read across tenants (ADR-0011).
- **Anti-magic** — your persona is the literal string you wrote; nothing is templated or injected
  behind your back (ADR-0002). Every token sent to the LLM traces to something you supplied.
- **Sovereignty** — 100% MIT/Apache runtime; bring your own LLM key via env (`ANTHROPIC_API_KEY`) or
  `aisotropy.WithLLM(...)`.

## Configuration knobs

Defaults are env-driven (same variables as the stock `aisotropyd` binary — `AISOTROPY_ADDR`,
`AISOTROPY_DATA_DIR`, `ANTHROPIC_API_KEY`, `QDRANT_HOST`, `RQLITE_URL`, `AISOTROPY_DEDICATED_TENANTS`,
`AISOTROPY_SLEEPCYCLE_BIN`, …). Override in-process only when you need to:

| Option | Purpose |
|---|---|
| `WithAddr(addr)` | HTTP listen address |
| `WithLLM(client)` | Inject an `aisotropy.LLMClient` instead of the env-selected one |
| `WithRateLimit(perSec, burst)` | Per-tenant token bucket |
| `WithCostPricing(inPer1M, outPer1M)` | Cost-meter pricing |
| `WithEmbedder(e)` | Swap the semantic-tier embedder (default: deterministic hash placeholder; ADR-0018) |
| `WithChunkSize(size, overlap)` | Tune knowledge-ingestion chunking, in runes (default 512/64) |

Deployment *values* (addr, rate limit, pricing, literal personas, MCP server references — never Go
behavior) can also come from a YAML file via `OptionsFromConfigFile(path)` /
`OptionsFromConfig(cfg)` (ADR-0016 lever #5); see
[`examples/aisotropy.example.yaml`](../../examples/aisotropy.example.yaml) and
[`examples/helpdesk`](../../examples/helpdesk), which composes a config file with Go-side
`WithTypedTool` handlers.

Memory backends (facts/semantic stores) are selected by environment only — there is no in-process
store-injection option in the public surface (exposing `facts.Store` would leak the
cross-tenant `ExportAll`; see [ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)).
`WithEmbedder` swaps the embedding function used by the semantic tier, not the store itself.

## A working example lives in the repo

See [`examples/weatherbot`](../../examples/weatherbot) — a nested module (its own `go.mod`) built
**only** against the public surface, registering its tool via `WithTypedTool` (the typed variant
above). Because it's a separate module, Go itself forbids it from importing `internal/…`, which is
exactly the guarantee this guide relies on. A second worked example,
[`examples/helpdesk`](../../examples/helpdesk), shows the config-file wiring path
(`OptionsFromConfigFile` + typed tools).

## References

- [ADR-0013 — Public extension surface](../decisions/0013-public-extension-surface-facade-vs-promote.md)
- [What Aisotropy is (and is not)](../architecture/what-aisotropy-is.md)
- [Granular control](../architecture/granular-control.md) — the scope/cost/behavior axes your bot inherits.
