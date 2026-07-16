# Aisotropy examples

Worked examples of building a bot on Aisotropy's **public surface only**. Each is
a separate Go module with `replace github.com/Luum-Home/aisotropy => ../..` that
imports only `github.com/Luum-Home/aisotropy/aisotropy`. Because they are
distinct modules, Go's `internal/` rule physically forbids them from reaching a
private package — so the fact that they compile is the "public surface only"
guarantee.

Read them in order; each adds one layer of realism, and every one runs
**keyless** (no API key, no Docker) with a single `go run .`.

| Step | Example | Adds | Run |
|---|---|---|---|
| 1 | [`weatherbot/`](weatherbot) | The minimal hello agent: one typed tool, one persona, keyless boot | `cd examples/weatherbot && go run .` |
| 2 | [`helpdesk/`](helpdesk) | Realistic: YAML wiring, secure-by-config auth, typed tools, a seeded knowledge base (RAG), the full memory moat | `cd examples/helpdesk && go run . -config helpdesk.yaml` |

Each example's own `README.md` has the exact `curl` commands and how to switch to
a real model. For the reference field manual, see
[`docs/guides/build-your-own-agent`](../docs/guides).

## Building

Each example is its own module — build/run from inside its directory:

```bash
cd examples/weatherbot && go build ./...   # or: go run .
cd examples/helpdesk   && go build ./...   # or: go run . -config helpdesk.yaml
```

`go build ./...` drops a binary named after the directory (`weatherbot`,
`helpdesk`); those are build artifacts and are gitignored — never commit them.
