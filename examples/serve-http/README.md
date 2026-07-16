# serve-http — the smallest HTTP server (Lesson 09)

The minimal deployment shape: serve the Aisotropy runtime over HTTP with
`App.Run` and stream a chat over Server-Sent Events. Where the library-first
examples call `App.Turn` in-process, this one exposes the same runtime on a
port. No tools, no persona, no knowledge base — just the keyless stub LLM and an
explicit `NoAuth()` so every `/v1` route works with no credential.

It is a separate Go module that imports only
`github.com/Luum-Home/aisotropy/aisotropy`, so Go's `internal/` rule enforces
"public surface only" — if it compiles, it never reached a private package.

## The whole program

```go
app := aisotropy.New(
    aisotropy.WithAddr(":8080"),
    aisotropy.WithAuthenticator(aisotropy.NoAuth()), // explicit keyless opt-out
)
app.Run(context.Background()) // boots the full runtime, blocks, shuts down gracefully
```

Without `NoAuth()` the runtime is **default-deny** and returns `401` on every
`/v1` request — failing closed is the deliberate default. `NoAuth()` is the
explicit opt-out for local/dev; a real deployment swaps it for a JWT
authenticator (see the `auth-scopes` example).

## Run it (keyless — no API key, no Docker)

```bash
cd examples/serve-http
go run .
# → serve-http listening on :8080 (keyless stub LLM, NoAuth)
```

### Non-streaming chat

```bash
curl -s localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"Say hi","product_id":"demo","session_id":"s1"}' | jq
# → {"reply":"…","tenant_id":"acme","stub":true}
```

### Streaming chat (SSE)

The `/chat/stream` route takes the same body and emits SSE events — `token`
events carrying incremental fragments, then a final `done` event. Use `curl -N`
to keep the connection open and see tokens arrive:

```bash
curl -sN localhost:8080/v1/tenant/acme/user/alice/chat/stream \
  -H 'content-type: application/json' \
  -d '{"prompt":"Stream me a short greeting","product_id":"demo","session_id":"s1"}'
# → event: token
#    data: {"text":"…"}
#    …
#    event: done
#    data: {"tenant_id":"acme"}
```

SSE honors context cancellation: cut the client (Ctrl-C) and the upstream LLM
call stops immediately — a partial assistant turn is never persisted.

### Operational endpoints (outside the OpenAPI contract)

```bash
curl -s localhost:8080/healthz   # liveness  → {"status":"ok"}
curl -s localhost:8080/health    # readiness → {"status":"ok","checks":[...]}
curl -s localhost:8080/openapi.json | jq '.components.schemas | keys'   # generated OpenAPI 3.1
# interactive docs: open http://localhost:8080/docs
```

The operator endpoints `/costs` and `/bitrate` are **closed by default** (they
return `503` telling you to configure them) — they carry cross-tenant operator
data, so they sit behind their own operator token, not the per-tenant `/v1` gate.
Enable them by setting `AISOTROPY_OPS_TOKEN` and sending it as a bearer:

```bash
AISOTROPY_OPS_TOKEN=secret-operator-token go run .
# then, in another shell:
curl -s localhost:8080/costs -H 'authorization: Bearer secret-operator-token'
```

`"stub":true` means the reply came from the built-in credential-less stub, not a
real model. Set a provider key to switch to a live model — no code change:

```bash
export ANTHROPIC_API_KEY=sk-ant-…
go run .   # llm.FromEnv() now selects the real client; stub=false
```

## Deploying

The bare binary serves with everything in-process (BadgerDB working memory,
in-process facts and semantic tiers). To back the memory tiers with real
networked services, use the repo's reference `docker-compose.yml` at the project
root — the same code, driven by environment variables:

```bash
# from the repo root
docker compose up -d qdrant rqlite
RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
  go run ./examples/serve-http
```

`docker compose up` (no service filter) boots `aisotropyd` itself wired to those
services; a sleep-cycle overlay lives in `docker-compose.sleepcycle.yml`. For a
secured, config-driven deployment (JWT auth, persona, pricing, tools from YAML),
graduate to the `helpdesk` example.
