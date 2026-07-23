# Aisotropy examples — the learning ladder

Worked examples of building a bot on Aisotropy's **public surface only**. Each is a
separate Go module with `replace github.com/Luum-Home/aisotropy => ../..` that imports
only `github.com/Luum-Home/aisotropy/aisotropy`. Because they are distinct modules,
Go's `internal/` rule physically forbids them from reaching a private package — so the
fact that they compile is the "public surface only" guarantee.

These are **not a grab-bag** — they are a ladder. Read them in order: each rung adds
exactly one layer over the last, from a keyless hello agent to a served, multi-tenant,
extensible runtime. Every rung pairs with a short [tutorial lesson](../docs/tutorial)
(the *why*) and a runnable module here (the *how*).

## The ladder

| # | Example | What it teaches | Run it | Lesson |
|---|---|---|---|---|
| 00 | [`weatherbot/`](weatherbot) | `App.Turn` — one chat turn in-process, keyless | `cd examples/weatherbot && go run .` | [00 · Hello agent](../docs/tutorial/00-hello-agent.md) |
| 01 | [`weatherbot/`](weatherbot) | `WithTypedTool` — a Go struct as the tool contract | `cd examples/weatherbot && go run .` | [01 · A typed tool](../docs/tutorial/01-typed-tool.md) |
| 02 | [`memory-sessions/`](memory-sessions) | What persists across turns, and how (working memory) | `cd examples/memory-sessions && go run .` | [02 · Memory & sessions](../docs/tutorial/02-memory-sessions.md) |
| 03 | [`multi-tenant/`](multi-tenant) | Isolation by construction — the core property | `cd examples/multi-tenant && go run .` | [03 · Multi-tenant isolation](../docs/tutorial/03-multi-tenant.md) |
| 04 | [`auth-scopes/`](auth-scopes) | `WithAuthenticator` + JWT + scope-gated tools | `cd examples/auth-scopes && go run .` | [04 · Auth & scopes](../docs/tutorial/04-auth-scopes.md) |
| 05 | [`knowledge-rag/`](knowledge-rag) | `IngestKnowledge` + retrieval-augmented turns | `cd examples/knowledge-rag && go run .` | [05 · Knowledge & RAG](../docs/tutorial/05-knowledge-rag.md) |
| 06 | [`task-agent/`](task-agent) | `App.RunTask` — the key-broker sandbox (needs Docker) | `cd examples/task-agent && go run .` | [06 · Sandboxed tasks](../docs/tutorial/06-sandboxed-task.md) |
| 07 | [`task-fanout/`](task-fanout) | `App.RunTasks` — concurrent fan-out (needs Docker) | `cd examples/task-fanout && go run .` | [07 · Concurrent tasks](../docs/tutorial/07-concurrent-tasks.md) |
| 08 | [`plugin-template/`](plugin-template) | Ship a capability without recompiling the core | `cd examples/plugin-template && node server.js` | [08 · Extend the runtime](../docs/tutorial/08-extend.md) |
| 09 | [`serve-http/`](serve-http) | `App.Run` — the network boundary, curl, deploy | `cd examples/serve-http && go run .` | [09 · Serve over HTTP](../docs/tutorial/09-serve-http.md) |

Rungs **00–05** run entirely **keyless and Docker-less** — a single `go run .` with no
setup. Rungs **06–07** need Docker (the sandbox is a real container). Rung **08** is an
out-of-process MCP plugin — the `node server.js` line runs the tool server on its own; the
lesson shows how the runtime loads it. Rungs **04** and **09** boot an HTTP server and print
`curl` commands.

## Keyless honesty

With no provider key set, the runtime uses its built-in **stub** LLM: the agent loop
completes end-to-end and returns a schema-valid canned reply, so every example runs and
its structural property (memory persisted, tenant isolated, scope enforced, chunks
ingested, task brokered) is real and observable. What the stub does *not* do is real
reasoning. Set a provider key to turn any stubbed run into a real one — **no code change**:

```bash
export ANTHROPIC_API_KEY=sk-ant-...   # or OPENAI_API_KEY / OPENROUTER_API_KEY
```

Each example's own `README.md` states exactly what is real keyless vs. what a key unlocks.

## Beyond the ladder — the capstone

| Example | What it is |
|---|---|
| [`helpdesk/`](helpdesk) | A realistic bot that combines the whole ladder: YAML wiring, secure-by-config auth, typed tools, a seeded knowledge base (RAG), and the full memory moat. Run: `cd examples/helpdesk && go run . -config helpdesk.yaml` |
| [`voice-pipecat/`](voice-pipecat) | The voice sandwich (ADR-0066): Pipecat + local STT/TTS over the OpenAI-compat shim (`POST /v1/chat/completions`), tenant JWT as the api_key, plus two executable latency metrics. Python — see its README for bring-up |

For the reference field manual, see
[`docs/guides/build-your-own-agent`](../docs/guides/build-your-own-agent.md); for the
plugin trust model, [`docs/guides/writing-a-plugin`](../docs/guides/writing-a-plugin.md).

## Building

Each example is its own module — build/run from inside its directory:

```bash
cd examples/weatherbot && go build ./...   # or: go run .
```

`go build ./...` (or `go build .`) drops a binary named after the directory; those are
build artifacts and are gitignored — never commit them. When you add an example, add its
binary to the root `.gitignore` (or a per-module `.gitignore`) following the existing
convention.
