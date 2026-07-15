# Genkit Go Arena

> **Framework:** Firebase Genkit for Go — `github.com/firebase/genkit/go` **v1.10.0** (Apache-2.0)
> **Integration status:** **REAL.** The Orbit chatbot is built on the genuine framework API
> (`genkit.Init`, `DefineModel`, `DefineTool`, `DefineFlow`, `Generate`, `GenerateData`,
> dotprompt). The offline path uses a **fake model registered via `genkit.DefineModel`** — not a
> live LLM and not a mock of the framework. No API key, no network.
> **Positioning:** General-purpose AI application library; Google steers "complex agent
> topologies" to ADK.

## What is real here

This arena actually depends on and compiles against Firebase Genkit:

```
$ go build ./...        # pulls github.com/firebase/genkit/go v1.10.0 + transitive deps
$ grep firebase go.mod
	require github.com/firebase/genkit/go v1.10.0
```

Every capability below runs through the real Genkit pipeline. The only thing faked is the
**model** — a deterministic, offline `ai.Model` registered with `genkit.DefineModel` so the whole
build (flows, tool-calling loop, structured output, streaming, dotprompt) executes with zero API
key. Genkit ships **no** built-in "stub model"; the fake lives in [`orbit/fakemodel.go`](orbit/fakemodel.go).

| Genkit API used | Where | Proven by |
|---|---|---|
| `genkit.Init` (+ `WithPromptFS`, `WithDefaultModel`) | `orbit/orbit.go` | server boots, tests pass |
| `genkit.DefineModel` (offline fake) | `orbit/fakemodel.go` | drives all generation |
| `genkit.DefineFlow` (traced) | `orbit/orbit.go` | `orbitChat`, `orbitClassify` |
| `genkit.Generate` (messages + tools) | `orbit/orbit.go` | isolation leak flows through it |
| `genkit.GenerateData[T]` (structured output) | `orbit/orbit.go` | `category=billing priority=high` |
| `genkit.DefineTool` (+ real tool-call loop) | `orbit/orbit.go` | model emits `ToolRequest`, tool runs |
| `ai.WithStreaming` (SSE) | `orbit/orbit.go`, `cmd/server` | streamed `data:` chunks |
| dotprompt `.prompt` file via `WithPromptFS` | `orbit/prompts/orbit_support.prompt` | `RunDotprompt` executes it |

## Setup

```bash
# Requires Go 1.25+ (genkit v1.10.0 pins go 1.25 transitively)
cd arenas/genkit-go
go mod tidy      # fetches github.com/firebase/genkit/go v1.10.0 and deps
```

## Smoke test (offline, fake model — no API key)

```bash
GENKIT_ENV=prod go run ./scripts/
```

`GENKIT_ENV=prod` keeps Genkit from starting its dev-mode Reflection server. The smoke proves the
build runs on the real framework (dotprompt, structured output, tool-calling) and then drives the
adversarial isolation exercise through `genkit.Generate`. **It exits 1 by design** — a confirmed
cross-tenant leak is the expected result for a convention-based framework (same contract as the
other arenas' smokes).

```bash
GENKIT_ENV=prod go test ./orbit/ -v    # same proof as a pass/fail regression test
```

## Live serve

```bash
GENKIT_ENV=prod go run ./cmd/server/     # real Genkit build on :4116

# seed a secret, then recall it (same tenant):
curl -s -X POST localhost:4116/v1/tenant/acme/user/alice/chat \
  -d '{"prompt":"remember my code: SECRET:abc-123","session_id":"s1"}'
curl -s -X POST localhost:4116/v1/tenant/acme/user/alice/chat \
  -d '{"prompt":"what is my recovery code?","session_id":"s1"}'
# -> the real model replays acme's history and returns SECRET:abc-123

# SSE streaming (real ai.WithStreaming callback):
curl -sN -X POST localhost:4116/v1/tenant/acme/user/alice/chat/stream \
  -d '{"prompt":"hello","session_id":"s2"}'
```

The live server namespaces the store key as `tenant:user:session` — the *correct* convention. It
is still only a convention: nothing in Genkit forces or checks it (see the smoke for the leak).

## Isolation model

**Convention — developer-passed session key. Genkit Go ships no isolation primitive at all.**

Verified against v1.10.0: the `genkit` and `ai` packages expose Generate / Flows / Tools /
Prompts but **no** public `Session`, `Memory`, `Thread`, or `Conversation` type. Multi-tenancy is
entirely developer-built: you assemble `[]*ai.Message` yourself and hand them to `ai.WithMessages`.
The session store ([`orbit/store.go`](orbit/store.go)) is keyed by a bare string chosen by the
developer; nothing ties that key to a tenant.

```go
// orbit/orbit.go — the chat flow (real genkit.DefineFlow)
history := o.Store.History(in.SessionKey)     // <- developer-passed key, no tenant check
resp, _ := genkit.Generate(ctx, g,
    ai.WithSystem(systemInstruction),
    ai.WithMessages(history...),              // wrong key => wrong tenant's history
    ai.WithPrompt(in.Prompt),
    ai.WithTools(o.orderTool),
)
// The real model faithfully answers from whatever context it was handed.
// If the key belonged to another tenant, that tenant's secret is now in the reply.
```

The correct path and the leaky path are one string apart, and the framework validates neither.

## Features covered (11-point checklist)

See [FEATURES.md](FEATURES.md) for the full coverage map with honesty classes. Summary:

| # | Feature | Class | Notes |
|---|---|---|---|
| 1 | Multi-tenant isolation | **CONVENTION** | No `Session`/`Memory` type; developer-passed key. Leak proven through real `Generate`. |
| 2 | Structured output | **BUILT-IN** | `genkit.GenerateData[T]` with JSON-schema validation (proven offline). |
| 3 | Tools | **BUILT-IN** | `genkit.DefineTool` + real tool-call round trip (proven offline). |
| 4 | Streaming (SSE) | **BUILT-IN** | `ai.WithStreaming` callback; SSE endpoint streams real chunks. |
| 5 | Working memory | **DIY** | No built-in session store; `genkitx-temporal` (3rd-party) for durability. |
| 6 | Semantic memory | **DIY** | Retriever/indexer interfaces exist; concrete vector store is BYO/plugin. |
| 7 | Facts memory | **DIY** | No equivalent. |
| 8 | Cost governance | **DIY** | `ModelResponse.Usage` is post-hoc; no pre-spend gate. |
| 9 | Observability | **BUILT-IN (proprietary schema)** | OTel tracing, but `genkit:*` attributes, **not** `gen_ai.*` semconv (re-verified v1.10.0). |
| 10 | Sovereignty / licensing | **BUILT-IN** | Apache-2.0, fully self-hostable. |
| 11 | Language | Go | — |

**Correction vs. the earlier draft of this arena:** structured output is **built-in and shipped**
(`GenerateData`), not "Preview/Agents API". The earlier arena also claimed it "uses Genkit's stub
model" while importing no framework at all — both are fixed here.

## What actually executed (closed loop)

Ran on this machine, Go 1.25.3, genkit v1.10.0:

- `go build ./...` — clean
- `gofmt -l` — clean; `go vet ./...` — clean
- `GENKIT_ENV=prod go run ./scripts/` — features pass, isolation leak confirmed, exit 1 (expected)
- `GENKIT_ENV=prod go test ./orbit/ -v` — `TestIsolationLeak` PASS, `TestFeaturesRunOffline` PASS
- `GENKIT_ENV=prod go run ./cmd/server/` — `/healthz`, `/chat`, `/chat/stream` all respond

See [DX-NOTES.md](DX-NOTES.md) for the developer-experience assessment.
