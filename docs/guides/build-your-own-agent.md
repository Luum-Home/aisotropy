---
type: How-To
title: Build your own agent on Aisotropy
description: A 5-minute, copy-paste getting-started — boot a minimal agent, add a typed tool, a persona, authentication, and memory/knowledge, using only the public `aisotropy` package (no fork, no composition-root edits).
tags: [guide, howto, adoption, extension, phase-8, facade, builder, getting-started]
timestamp: 2026-07-16T00:00:00Z
---

# Build your own agent on Aisotropy

Aisotropy is a multi-tenant agent **runtime** ([what it is](../architecture/what-aisotropy-is.md)).
You don't fork it or edit its composition root — you **import one package** and register your bot's
*domain* (its tools, their behavior, and its persona). Everything else — tiered memory, tenant
isolation, quotas, circuit breaker, cost metering, SSE streaming, graceful shutdown — you inherit
for free.

This is a 5-minute path: **minimal agent → typed tool → persona → auth → memory**. Every snippet is
runnable and uses only the public package `github.com/Luum-Home/aisotropy/aisotropy`. If you find
yourself importing anything under `internal/`, you've left the supported surface — Go will refuse to
compile it from another module, by design ([ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)).

> **Stability:** the `aisotropy` package is **0.x** — additive within a minor version, with any
> breaking change called out in the CHANGELOG on a minor bump. Pin a version.

## What you supply vs. what you inherit

| You supply (domain) | Aisotropy provides (runtime) |
|---|---|
| Tool declarations + handlers (`WithTypedTool` / `WithTool` + `WithToolExecutor`) | Scope-gated tool injection into the LLM payload + the bounded tool-use loop |
| A persona (`aisotropy.Persona`, per `product_id`) | Working/facts/semantic memory hydration |
| An authenticator (`WithAuthenticator`) | The fail-closed identity gate + must-agree tenant/user bind |
| (optional) LLM client, embedder, quota numbers | Tenant isolation, quotas, breaker, cost meter, OTel, SSE, shutdown |

## 1. A minimal agent (boots with zero credentials)

Create a module and depend on the public façade:

```bash
mkdir mybot && cd mybot
go mod init example.com/mybot
go get github.com/Luum-Home/aisotropy/aisotropy@latest
```

The smallest thing that boots is `New(...).Run(ctx)`. Two facts make it run with **no setup**:

- **No LLM key → keyless stub.** With `ANTHROPIC_API_KEY` (or `OPENROUTER_API_KEY`) unset, the
  runtime uses a canned streaming stub — Aisotropy's TestModel equivalent. The in-process memory
  tiers also default to in-memory, so there is no database to provision.
- **No authenticator → default-deny.** The runtime is fail-closed: without an authenticator it
  rejects every `/v1` request with 401. `WithAuthenticator(aisotropy.NoAuth())` is the explicit
  local/dev opt-out that trusts the path `tenant_id`/`user_id` verbatim, so requests get through.

```go
package main

import (
	"context"
	"log"

	"github.com/Luum-Home/aisotropy/aisotropy"
)

func main() {
	app := aisotropy.New(
		aisotropy.WithAuthenticator(aisotropy.NoAuth()), // local/dev: trust path identity
	)
	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

```bash
go run .   # serves :8080; GET /healthz, POST /v1/tenant/{tenant_id}/user/{user_id}/chat[/stream]
```

`app.Run` boots the full runtime and blocks until `SIGINT`/`SIGTERM`, draining in-flight requests
and flushing memory on shutdown.

## 2. Add a typed tool

A tool's arguments become a Go struct whose `json`/`jsonschema` tags drive **both** the JSON Schema
the model is shown and the decode/validation of what it returns — the same "struct as the single
contract" idea Aisotropy uses for HTTP/OpenAPI, now for tool inputs
([ADR-0016](../decisions/0016-dual-surface-sealed-runtime-composition-sdk.md) lever #1). No executor switch, no
string-map parsing; invalid arguments are reflected back to the model (bounded by the agent's step
budget), never coerced.

```go
// forecastArgs drives the schema AND the decode — including the non-string Days
// field, which the string-map executor could not carry.
type forecastArgs struct {
	City  string `json:"city" jsonschema:"description=the city to look up"`
	Units string `json:"units" jsonschema:"enum=c,enum=f,description=temperature units"`
	Days  int    `json:"days" jsonschema:"minimum=1,maximum=14,description=forecast horizon in days"`
}

func getWeather(ctx context.Context, args forecastArgs) (string, error) {
	tenant, _ := aisotropy.TenantFromContext(ctx) // read-only identity; you cannot forge it
	return fmt.Sprintf("Sunny, 22°C in %s (tenant %s)", args.City, tenant), nil
}

app := aisotropy.New(
	// The empty requiredScope makes the tool always available; pass e.g.
	// "read:weather" to gate it behind a granted scope.
	aisotropy.WithTypedTool("get_weather", "look up the weather for a city", "", getWeather),
	aisotropy.WithAuthenticator(aisotropy.NoAuth()),
)
```

The handler reads the caller's identity from `ctx` — it can **read** the tenant, never forge it
(the runtime owns tenant scoping; you cannot cross tenants). Call `WithTypedTool` once per tool; it
accumulates.

> **0.x limitation:** on the keyless stub (no native tool-calling), arguments arrive flattened to
> strings, so only string-shaped fields are guaranteed there. Nested and non-string fields (like
> `Days`) survive on a real provider's native tool-calling path.

**String-map alternative.** If you prefer a single dispatcher over one struct per tool, declare
`aisotropy.Tool{Name, Description, RequiredScope}` with `WithTool` and supply a
`WithToolExecutor(exec)` whose `Execute(ctx, name string, args map[string]string) (string, error)`
switches on the tool name. Tools whose behavior lives in an external process can instead be wired
with `WithMCPServer(requiredScope, command, args...)` (ADR-0016 lever #2) — no Go handler at all.

## 3. Add a persona

A persona is the literal base system prompt for a `product_id` — no templating engine sits behind it
(anti-magic gate, [ADR-0002](../decisions/0002-anti-framework-magic-struct-as-contract.md)). The runtime
appends memory and tool context to this literal at request time; an unregistered `product_id` falls
back to the built-in default prompt.

```go
app := aisotropy.New(
	aisotropy.WithTypedTool("get_weather", "look up the weather for a city", "", getWeather),
	aisotropy.WithPersona("weatherbot", aisotropy.Persona{
		BasePrompt: "You are WeatherBot. Answer weather questions concisely and cite the city.",
	}),
	aisotropy.WithAuthenticator(aisotropy.NoAuth()),
)
```

The persona is selected per request by the body's `product_id` (see step 6).

## 4. Add authentication

`NoAuth()` is for local/dev only. For anything real, register a credential-verifying authenticator.
The built-in `NewJWTAuthenticator` verifies an `Authorization: Bearer <jwt>` using **only the Go
standard library** (no dependency), and the runtime binds the verified identity with **must-agree**
semantics: the token's tenant/user must equal the URL path tenant/user (mismatch → 403;
missing/invalid/expired token → 401).

```go
app := aisotropy.New(
	aisotropy.WithTypedTool("get_weather", "look up the weather for a city", "read:weather", getWeather),
	aisotropy.WithPersona("weatherbot", aisotropy.Persona{
		BasePrompt: "You are WeatherBot. Answer weather questions concisely and cite the city.",
	}),
	// HS256 shared secret (min 32 bytes). Optional hardening: WithJWTIssuer,
	// WithJWTAudience, WithJWTMaxTTL. Claims map tenant_id|tid -> TenantID,
	// user_id|uid|sub -> UserID, scope|scp|scopes -> Scopes.
	aisotropy.WithAuthenticator(aisotropy.NewJWTAuthenticator(
		[]byte(os.Getenv("MYBOT_JWT_SECRET")),
		aisotropy.WithJWTIssuer("https://issuer.example.com"),
	)),
)
```

For RS256 tokens from an external IdP, use `NewJWTAuthenticator(nil, aisotropy.WithJWTJWKS(url))`
(JWKS endpoint) or `WithJWTRSAPublicKey(pub)` (static key — parse a PEM with
`ParseRSAPublicKeyPEM`). Scopes verified here gate tools: a tool with `RequiredScope:"read:weather"`
is **never injected** into the payload for a caller who lacks that scope — the model isn't even told
it exists (governance by absence of information).

## 5. Point at memory and knowledge

You inherit the memory tiers automatically — working memory (BadgerDB), facts (SQLite+FTS5), and
semantic RAG all hydrate every chat turn, tenant-scoped, with no wiring on your part. Two knobs are
worth knowing on day one:

- **Persistence & backends via env** (same variables as the stock binary): `AISOTROPY_DATA_DIR`
  persists working memory + facts to disk; `RQLITE_URL` and `QDRANT_HOST` swap the facts/semantic
  tiers to their networked services. Unset → in-process defaults.
- **Preload knowledge from Go** (ADR-0018/0019). Ingest documents into the semantic tier before or
  alongside `Run`; they become retrievable in RAG hydration. Use a **real embedder** for meaningful
  retrieval — `WithEmbedder(e)`, or set `AISOTROPY_EMBED_URL` to an OpenAI-compatible
  `/v1/embeddings` endpoint; the default is a deterministic hash placeholder that wires the path but
  does not rank semantically.

```go
app := aisotropy.New(
	aisotropy.WithAuthenticator(aisotropy.NoAuth()),
	// aisotropy.WithEmbedder(myRealEmbedder), // else set AISOTROPY_EMBED_URL
)

// Tenant-shared corpus: retrievable by every user of "acme" (ADR-0019).
// Per-user knowledge is app.IngestKnowledge(ctx, tenant, user, docs...).
if _, err := app.IngestTenantKnowledge(context.Background(), "acme",
	aisotropy.Document{ID: "faq-1", Content: "Refunds take 3-5 business days.", Source: "faq"},
); err != nil {
	log.Fatal(err)
}

if err := app.Run(context.Background()); err != nil {
	log.Fatal(err)
}
```

The HTTP arm is symmetric: `POST /v1/tenant/{tenant_id}/user/{user_id}/knowledge` and
`POST /v1/tenant/{tenant_id}/knowledge`. If you use the SDK ingestion methods **without** calling
`Run` (pure composition, no server), `defer app.Close()` to release the semantic store.

## 6. Talk to your bot

The HTTP contract is the runtime's, unchanged. Persona is selected by `product_id`:

```bash
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"weather in Rosario?","product_id":"weatherbot","session_id":"s1","scopes":["read:weather"]}'
```

- `tenant_id` / `user_id` are path params (the isolation boundary, validated before your code runs).
- `scopes` gate tools: without `read:weather`, `get_weather` is **never injected** into the payload.
- `product_id` selects your registered persona; an unregistered id falls back to the default prompt.

(With `NoAuth()` the `scopes` in the body are taken at face value; with a real authenticator the
scopes come from the verified token instead.)

## The rules you inherit (and can't opt out of)

- **Tenant isolation by construction** — you read identity from `ctx`; you cannot construct a memory
  scope or read across tenants ([ADR-0011](../decisions/0011-tenant-scoped-statement-builder.md)).
- **Anti-magic** — your persona is the literal string you wrote; nothing is templated or injected
  behind your back (ADR-0002). Every token sent to the LLM traces to something you supplied.
- **Sovereignty** — 100% MIT/Apache runtime; bring your own LLM key via env or `aisotropy.WithLLM(...)`.

## Configuration knobs

Defaults are env-driven (same variables as the stock `aisotropyd` binary — `AISOTROPY_ADDR`,
`AISOTROPY_DATA_DIR`, `ANTHROPIC_API_KEY`, `QDRANT_HOST`, `RQLITE_URL`, `AISOTROPY_DEDICATED_TENANTS`,
`AISOTROPY_SLEEPCYCLE_BIN`, …). Override in-process only when you need to:

| Option | Purpose |
|---|---|
| `WithAddr(addr)` | HTTP listen address |
| `WithAuthenticator(a)` | The identity gate — `NoAuth()`, `NewJWTAuthenticator(...)`, or your own |
| `WithLLM(client)` | Inject an `aisotropy.LLMClient` instead of the env-selected one |
| `WithRateLimit(perSec, burst)` | Per-tenant token bucket (default 10/30) |
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
store-injection option in the public surface (exposing `facts.Store` would leak the cross-tenant
`ExportAll`; see [ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md)).
`WithEmbedder` swaps the embedding function used by the semantic tier, not the store itself.

## Working examples in the repo

- [`examples/weatherbot`](../../examples/weatherbot) — a nested module (its own `go.mod`) built
  **only** against the public surface, registering its tool via `WithTypedTool`. Because it is a
  separate module, Go itself forbids it from importing `internal/…`, which is exactly the guarantee
  this guide relies on.
- [`examples/helpdesk`](../../examples/helpdesk) — the config-file wiring path
  (`OptionsFromConfigFile` + typed tools).

## References

- [ADR-0013 — Public extension surface](../decisions/0013-public-extension-surface-facade-vs-promote.md)
- [What Aisotropy is (and is not)](../architecture/what-aisotropy-is.md)
- [Granular control](../architecture/granular-control.md) — the scope/cost/behavior axes your bot inherits.
</content>
</invoke>
