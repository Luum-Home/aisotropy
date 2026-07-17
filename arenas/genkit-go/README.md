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
adversarial isolation exercise through `genkit.Generate`. **It exits 0 on the expected outcome** —
a confirmed cross-tenant leak *is* the pass condition for a convention-based framework (the positive
control verified AND the leak reproduced); it exits 1 only if the leak could *not* be reproduced or
the positive control broke.

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

## Containerized smoke (Docker)

Proves the real Genkit build boots and isolates **as a production container**, not just as a local
`go run`. The server is packaged like a real Go service — multi-stage build → static `CGO_ENABLED=0`
binary on `gcr.io/distroless/static-debian12:nonroot`, with `GENKIT_ENV=prod` baked in so no
dev-mode Reflection server starts. The dotprompt files are compiled into the binary via `go:embed`,
so the runtime image is **just the binary (~5 MB)**.

```bash
cd arenas/genkit-go
bash docker/run.sh
```

`docker/run.sh` builds [`docker/Dockerfile`](docker/Dockerfile), runs the container (publishing
`:4116` to an ephemeral host port), waits for `/healthz`, then drives the isolation assertion
against the **containerized** server over HTTP:

1. **Seed** — `acme/alice` (session `s1`) stores a random `SECRET:` marker.
2. **Positive control** — `acme/alice` re-reads session `s1` → **must** replay the secret (the real
   Genkit generate pipeline works inside the container).
3. **Cross-tenant probe** — a *different* tenant reusing the same `session_id` → **must NOT** see
   the secret. The server's `tenant:user:session_id` key namespacing makes the key differ, so the
   read is isolated. (This is the *correct-convention* path; the framework does not **enforce** it —
   the [native smoke](scripts/smoke.go) drives the convention leak that namespacing avoids.)

It exits `0` only if the image built, the container became ready, and both assertions held; it
**skips cleanly** (exit `0`, no fake pass) when Docker is not installed or its daemon is
unreachable, and always removes its container on exit. This complements — does not replace — the
native `go run ./scripts/` and `go test ./orbit/` smokes above.

## What Genkit Go does genuinely well

This arena's core claim is narrow — *isolation-by-default* — and it would be a strawman to leave it
there. Genkit Go is a **strong, general-purpose AI application library** with genuinely excellent
DX, backed by Firebase/Google. Three real strengths, each **exercised offline against the pinned
v1.10.0 API** (run `GENKIT_ENV=prod go run ./cmd/strengths`, exit 0):

| # | Strength | Real v1.10.0 API (module cache) | Proven by |
|---|---|---|---|
| 1 | **Flows** — observable, composable, individually-runnable/deployable units. Tracing is automatic. | `genkit.DefineFlow` / `genkit.DefineStreamingFlow`; `core.Flow.Run` / `core.Flow.Stream` | a flow composing a flow ran and auto-emitted OTel spans on Genkit's own `TracerProvider`; a streaming flow delivered progressive chunks |
| 2 | **Dotprompt** — prompt-as-config with a typed input schema; model + template + schema live in a `.prompt` file, hot-editable, no recompile. | `genkit.LookupPrompt`; `ai.Prompt.Execute` + `ai.WithInput` | `orbit_support.prompt` executed with two different input structs through the same file |
| 3 | **Model-agnostic provider abstraction** — every provider is registered by name; caller code is provider-independent. | `genkit.DefineModel` / `genkit.LookupModel`; `ai.Model` + `ai.WithModelName` | one identical `genkit.Generate` call, switching only the model-name string, dispatched to two distinct providers |

Beyond these, the arena also exercises **structured output** (`genkit.GenerateData[T]`), a **real
tool-call loop** (`genkit.DefineTool`), **SSE streaming with ctx-cancel** (`ai.WithStreaming`), an
**offline vector store** (`plugins/localvec`), and an **evaluator runner** (`genkit.DefineEvaluator`
/ `genkit.Evaluate`) — see [FEATURES.md](FEATURES.md).

**Honest framing:** Genkit's flow + dotprompt + deployment DX is a real strength — for building and
shipping AI features (especially on Firebase/Google Cloud), it is excellent. **aisotropy is a
self-hosted, multi-tenant Go runtime — a different niche.** The two overlap on one axis:
isolation-by-default, where Genkit's memory primitive is experimental and tenant-unaware (next
section), so tenant binding is on the developer. Both statements are true; this arena keeps both.

## Isolation model

**Convention — developer-passed session key. Genkit-Go v1.10.0 ships an EXPERIMENTAL/preview
session-memory primitive, but NO isolation primitive.**

Verified against v1.10.0: Genkit-Go **does** ship a session/memory primitive — the experimental
`github.com/firebase/genkit/go/ai/exp` + `genkit/exp` Agents API, with a pluggable `SessionStore`
and a shipped in-memory implementation, `localstore.NewInMemorySessionStore` (it persists and
replays `Messages` across turns; Firestore via plugin). It was formalized in the **July-2026 Genkit
Agents API preview** (TS + Go). But it is keyed by a **developer-chosen session id with NO
tenant / user / product binding** — the `SessionSnapshot` / `SessionState` structs carry only a
`SessionID` field, so it provides **persistence, not isolation**. Two identities that reuse one
session id share memory. There is still **no** public `Session`-scoped-by-tenant / `Memory` /
`Thread` type that binds identity; multi-tenancy remains entirely developer-built.

- **JS-vs-Go asymmetry (honest):** Genkit-**JS** has shipped `ai.chat()` / `Session` / `SessionStore`
  for longer and more stably; Genkit-**Go** reached rough parity only via this July-2026
  preview/beta surface, which is gated behind `requireExperimental` and explicitly "may change."
- **We reproduce the leak against Genkit's REAL primitive.** `TestRealSessionStoreIsTenantUnaware`
  (`orbit/isolation_test.go`) drives `localstore.NewInMemorySessionStore` directly — no DIY store in
  the path — and shows a tenantB read of a shared session id returns tenantA's messages verbatim,
  because the store's only key is the session id. The isolation-leak flow below (`orbit/store.go`)
  uses a DIY stand-in with **identical** semantics for the end-to-end `genkit.Generate` path; the
  real `exp` SessionStore behaves the same way (session-id-keyed, tenant-unaware — see `cmd/parity`).

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

The correct path and the leaky path are one string apart, and the framework validates neither —
including its own `exp` SessionStore, which has no tenant field to validate against.

## Features covered (11-point checklist)

See [FEATURES.md](FEATURES.md) for the full coverage map with honesty classes. Summary:

| # | Feature | Class | Notes |
|---|---|---|---|
| 1 | Multi-tenant isolation | **CONVENTION** | An experimental `ai/exp` `SessionStore` (+ `localstore`) ships memory, but it is session-id-keyed with **no** tenant/user/product binding — persistence, not isolation. Leak proven through real `Generate` **and** against the real `localstore` SessionStore (`TestRealSessionStoreIsTenantUnaware`). |
| 2 | Structured output | **BUILT-IN** | `genkit.GenerateData[T]` with JSON-schema validation (proven offline). |
| 3 | Tools | **BUILT-IN** | `genkit.DefineTool` + real tool-call round trip (proven offline). |
| 4 | Streaming (SSE) | **BUILT-IN** | `ai.WithStreaming` callback; SSE endpoint streams real chunks. |
| 5 | Working memory | **BUILT-IN (experimental)** | `ai/exp` `SessionStore` + shipped `localstore.NewInMemorySessionStore` persist/replay `Messages` (July-2026 Agents API preview; gated behind `requireExperimental`, "may change"). Tenant binding is DIY. Durable exec is 3rd-party (`genkitx-temporal`). |
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
- `GENKIT_ENV=prod go run ./scripts/` — features pass, isolation leak confirmed, exit 0 (expected pass)
- `GENKIT_ENV=prod go run ./cmd/strengths/` — flows + dotprompt + provider-abstraction all exercised, exit 0
- `GENKIT_ENV=prod go test ./orbit/ -v` — `TestIsolationLeak` PASS, `TestRealSessionStoreIsTenantUnaware`
  PASS (leak reproduced against Genkit's real `localstore` SessionStore), `TestFeaturesRunOffline` PASS
- `GENKIT_ENV=prod go run ./cmd/server/` — `/healthz`, `/chat`, `/chat/stream` all respond

See [DX-NOTES.md](DX-NOTES.md) for the developer-experience assessment.
