# weatherbot — the minimal "hello agent"

The smallest possible bot on Aisotropy's public surface: **one typed tool, one
persona, keyless boot**. Start here, then graduate to [`../helpdesk`](../helpdesk)
for the realistic version (config-driven wiring, memory/knowledge, secure-by-config
auth).

It is a separate Go module that imports only
`github.com/Luum-Home/aisotropy/aisotropy`, so Go's `internal/` rule enforces
"public surface only" — if it compiles, it never reached a private package.

## What it shows

| Concept | Where |
|---|---|
| Typed tool (struct-as-contract) | `getWeatherArgs` + `WithTypedTool` — the struct drives the schema shown to the model AND validates its arguments (bad args reflect back, never coerced) |
| Scope-gated tools | `get_weather` needs the `read:weather` scope, or the model is never offered it |
| Tenant-aware handlers | `TenantFromContext(ctx)` — the calling tenant, read-only, from ctx |
| Per-product persona | `WithPersona` — a literal base prompt |
| Keyless local boot | `WithAuthenticator(NoAuth())` so `/v1` works with no credential (see note) |

## Run it (keyless — no API key, no Docker)

```bash
cd examples/weatherbot
go run .
# → listens on :8080 with the credential-less stub LLM
```

In another shell, call the chat endpoint. The path carries `tenant`/`user`; the
body's `product_id` and `session_id` are required, and `scopes` unlock the tool:

```bash
curl -s localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"What is the weather in Paris?","product_id":"weatherbot","session_id":"s1","scopes":["read:weather"]}' | jq
# → {"reply":"...","tenant_id":"acme","stub":true}   ("stub":true = no real model yet)

# Liveness / readiness (outside the OpenAPI contract):
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/health    # {"status":"ok","checks":[...]}
```

`"stub":true` means the reply came from the built-in credential-less stub, not a
real model — the whole request path (validation → scope-gated tools → memory →
reflection) ran, just without a live LLM.

## Use a real model

Set a provider key before `go run .` — the runtime picks the real client
automatically (`llm.FromEnv`), no code change:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run .
# now the model actually decides when to call get_weather, and "stub" is false
```

To secure it, replace `WithAuthenticator(NoAuth())` in `main.go` with a JWT
authenticator (`aisotropy.NewJWTAuthenticator([]byte(secret))` or
`aisotropy.AuthenticatorFromEnv()`) — the helpdesk example shows the
secure-by-config pattern.
