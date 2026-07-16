# helpdesk — a realistic bot on Aisotropy's public surface

The "realistic" step after [`../weatherbot`](../weatherbot): a third-party support
bot that uses **every** extension surface together, importing only
`github.com/Luum-Home/aisotropy/aisotropy` (a separate module, so Go's `internal/`
rule enforces "public surface only" — `grep -rn internal/ .` returns 0).

## What it shows

| Surface | Here | ADR |
|---|---|---|
| Declarative wiring from YAML | `OptionsFromConfigFile("helpdesk.yaml")` — persona, rate limit, cost pricing, (optional) MCP servers | 0016 #5 |
| Typed tools (struct-as-contract) | `create_ticket` (priority `enum`), `lookup_order` (id `pattern`) — schema shown to the model + validated, invalid args reflect | 0016 #1 |
| Secure-by-config auth | `AuthenticatorFromEnv()` — keyless by default, HS256 JWT when `AISOTROPY_JWT_SECRET` is set, no code fork | 0024 |
| Seeded knowledge (RAG) | `IngestTenantKnowledge()` seeds return/shipping/escalation policy; the semantic tier hydrates it into the system prompt | 0018/0019 |
| Polyglot tools via MCP | commented `mcp_servers` block in `helpdesk.yaml` (uncomment to add a filesystem server; behavior lives in that process) | 0016 #2 |
| Inherited runtime | quota, breaker, cost meter, tiered memory, scope-gated tools, graceful shutdown — for free | — |

## Run it (keyless — no API key, no Docker)

The bare binary falls back to in-process memory tiers and the credential-less
stub LLM, so it runs with zero setup:

```bash
cd examples/helpdesk
go run . -config helpdesk.yaml
# → auth mode "noauth", seeds the "acme" knowledge base, listens on :8080
```

In another shell, talk to the agent as tenant `acme` (the seeded tenant). The
body's `product_id` and `session_id` are required; `scopes` unlock the typed
tools:

```bash
# A policy question — answered from the seeded knowledge base (no tool call):
curl -s localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"What is your return policy?","product_id":"helpdesk","session_id":"s1"}' | jq

# A tool-shaped request — needs the write:tickets scope for create_ticket to exist:
curl -s localhost:8080/v1/tenant/acme/user/alice/chat \
  -H 'content-type: application/json' \
  -d '{"prompt":"Open a high priority ticket: order never arrived","product_id":"helpdesk","session_id":"s2","scopes":["write:tickets"]}' | jq

# Readiness (in-process tiers now, real rqlite/Qdrant probes under Docker):
curl -s localhost:8080/health | jq
```

`"stub":true` in the reply means the built-in stub answered (no real model key
set) — the whole path still ran: config wiring → auth → scope-gated tools →
seeded-KB hydration → memory → reflection.

## Run it against the reference moat (real memory tiers)

```bash
# From the repo root: bring up the networked memory tiers.
docker compose up -d qdrant rqlite

cd examples/helpdesk
RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
  go run . -config helpdesk.yaml

curl -s localhost:8080/health | jq
#   {"status":"ok","checks":[{"name":"facts","status":"ok",...},
#                            {"name":"semantic","status":"ok",...}]}
```

## Use a real model (and real retrieval)

No code change — the runtime reads the environment:

```bash
export ANTHROPIC_API_KEY=sk-ant-...          # real LLM (llm.FromEnv), "stub":false
export AISOTROPY_EMBED_URL=https://api.openai.com/v1/embeddings  # real embeddings for KB ranking
export AISOTROPY_EMBED_API_KEY=sk-...
```

Without `AISOTROPY_EMBED_URL` the semantic tier uses a deterministic **hash**
embedder: it wires and exercises the ingest/RAG path but does not rank
semantically — good enough to see the plumbing, not for real retrieval.

## Secure it

Set a signing secret (≥ 32 bytes) and the bot verifies HS256 bearer JWTs whose
`tenant_id`/`user_id` must match the URL path:

```bash
export AISOTROPY_JWT_SECRET=$(head -c 32 /dev/urandom | base64)
go run . -config helpdesk.yaml   # → auth mode "jwt-hs256"; unauthenticated /v1 requests now 401
```

The tools' behavior is Go (`main.go`); the wiring is YAML (`helpdesk.yaml`); the
knowledge is seeded in-process; the memory moat is the compose stack. No private
package is imported.
