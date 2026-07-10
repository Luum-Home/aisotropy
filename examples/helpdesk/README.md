# helpdesk — a realistic bot on Aisotropy's public surface

A worked example of a third-party support bot that uses **every** extension
surface together, importing only `github.com/Luum-Home/aisotropy/aisotropy`
(a separate module, so Go's `internal/` rule enforces "public surface only" —
`grep -rn internal/ .` returns 0).

What it demonstrates:

| Surface | Here | ADR-0016 |
|---|---|---|
| Declarative wiring from YAML | `OptionsFromConfigFile("helpdesk.yaml")` — persona, rate limit, cost pricing, (optional) MCP servers | lever #5 |
| Typed tools (struct-as-contract) | `create_ticket` (priority `enum`), `lookup_order` (id `pattern`) — schema shown to the model + validated, invalid args reflect | lever #1 |
| Polyglot tools via MCP | commented `mcp_servers` block in `helpdesk.yaml` (uncomment to add a filesystem server; behavior lives in that process) | lever #2 |
| Inherited runtime | quota, breaker, cost meter, tiered memory (rqlite + Qdrant), scope-gated tools, graceful shutdown — for free | — |

## Run against the reference moat

```bash
# From the repo root: bring up the memory tiers.
docker compose up -d qdrant rqlite

# Run the bot wired to them (keyless stub if no LLM key is set).
cd examples/helpdesk
RQLITE_URL=http://localhost:4001 QDRANT_HOST=localhost QDRANT_PORT=6334 \
  go run . -config helpdesk.yaml

# In another shell: the readiness probes now hit the REAL tiers.
curl -s localhost:8080/health | jq
#   {"status":"ok","checks":[{"name":"facts","status":"ok",...},
#                            {"name":"semantic","status":"ok",...}]}

# Point a real model to actually invoke the typed tools:
export OPENROUTER_API_KEY=sk-or-...
```

The tools' behavior is Go (`main.go`); the wiring is YAML (`helpdesk.yaml`); the
memory moat is the compose stack. No private package is imported.
</content>
