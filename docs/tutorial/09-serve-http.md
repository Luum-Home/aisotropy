---
type: Tutorial
title: "09 — Serve over HTTP"
description: Put the same runtime behind a network boundary with App.Run — the HTTP contract, streaming with curl, and what boots.
tags: [tutorial, http, serve, run, streaming, deploy]
timestamp: 2026-07-16T00:00:00Z
---

# 09 — Serve over HTTP

**Goal:** expose the same runtime you've been calling in-process behind a network boundary — for
remote clients and other services.

Everything so far used `Turn` (in-process). When you want the network boundary, the **same `App`** runs
behind an HTTP contract via `app.Run(ctx)`. Nothing about the pipeline changes; you're adding a
transport. Because identity now arrives from an untrusted client, an authenticator is required
(lesson 04) — the runtime is default-deny until you configure one.

```go
app := aisotropy.New(
	aisotropy.WithTypedTool("get_weather", "get the weather for a city", "read:weather", getWeather),
	aisotropy.WithPersona("mybot", aisotropy.Persona{BasePrompt: "You are MyBot. Be concise."}),

	// Local/dev: trust the path identity. In production swap for
	// aisotropy.NewJWTAuthenticator(secret) (lesson 04).
	aisotropy.WithAuthenticator(aisotropy.NoAuth()),
)
if err := app.Run(context.Background()); err != nil { // serves :8080, blocks until shutdown
	log.Fatal(err)
}
```

`Run` boots the full runtime and the HTTP surface, then blocks until `SIGINT`/`SIGTERM` triggers
graceful shutdown (it drains in-flight streams and closes every store). The listen address is
`AISOTROPY_ADDR` or `:8080`; override in code with `aisotropy.WithAddr(":9000")`.

Tenant and user are **path params** (the isolation identity, straight into ctx); product and session
live in the body. Streaming is Server-Sent Events, and it respects context cancellation — a cancelled
client request stops the upstream model call immediately:

```bash
go run .          # serves :8080
curl -N -X POST localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"weather in Rosario?","product_id":"mybot","session_id":"s1"}'
```

Alongside the `/v1` chat routes you get, outside the OpenAPI contract: `GET /healthz` (liveness),
`GET /health` (readiness — cheap, read-only tier probes, no token spent), `GET /openapi.json`, and
`/docs`. The struct *is* the contract, so the OpenAPI schema is generated from the same types that
validate the request — no second source of truth to drift.

To run without the network boundary at all, go back to [`Turn`](00-hello-agent.md): same runtime,
called like a function. Choose per deployment — `Run` when you need the wire, `Turn` when your own
trusted Go code is the caller.

**Runnable example:** [`serve-http`](../../examples/serve-http) — a served bot with the `curl` commands
for both the buffered `/chat` and the streaming `/chat/stream` routes.

**Depth:** [adoption surface](../architecture/adoption-surface.md),
[request lifecycle](../architecture/request-lifecycle.md), and the
[HTTP API reference](../architecture/api-reference.md).

That's the ladder. Back to [the tutorial index](index.md), or on to
[Build your own agent](../guides/build-your-own-agent.md) for the reference field manual.
