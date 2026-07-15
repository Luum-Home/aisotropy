# Eino Arena — REAL integration

> **Framework:** [CloudWeGo Eino](https://github.com/cloudwego/eino) (ByteDance, Apache-2.0), pinned at **v0.9.12**
> **Positioning:** LangGraph-for-Go. Same-language peer to aisotropy, but **no isolation model**. Competes on orchestration, not tenancy.
> **Status:** This arena depends on and runs the **real** framework. The Orbit chatbot is a real
> `compose.Chain` + real `flow/agent/react` agent + real `components/tool`; the isolation proof
> uses Eino's **real** `compose.CheckPointStore` + `compose.WithCheckPointID` state model. The only
> fake is the *ChatModel* (an offline `StubChatModel` that satisfies `model.ToolCallingChatModel`),
> so the smoke test needs **no API key**.

## What is real vs stubbed

| Thing | Real? | Evidence |
|---|---|---|
| Framework dependency | **Real** | `go.mod` requires `github.com/cloudwego/eino v0.9.12`; `go.sum` pins it |
| Chat orchestration | **Real** | `compose.NewChain[[]*schema.Message, *schema.Message]().AppendChatModel(...)` in `orbit/orbit.go` |
| Streaming | **Real** | the compiled runnable's `Stream()` returns `*schema.StreamReader[*schema.Message]` |
| Tool calling | **Real** | `react.NewAgent` + `utils.InferTool` real `components/tool.InvokableTool` (`lookup_order`) |
| State / working memory persistence | **Real** | `compose.CheckPointStore` (Get/Set by string) + `compose.WithCheckPointID` + interrupt/resume |
| The LLM itself | **Stub** | `orbit.StubChatModel` implements the real `model.ToolCallingChatModel` interface; deterministic, keyless |

Swap `NewStubChatModel()` for `eino-ext`'s `openai`/`claude` ChatModel and the **exact same graphs**
talk to a live LLM — nothing else changes.

## Setup

```bash
# Requires Go 1.23+ (verified building on Go 1.26).
cd eino
go mod download        # deps are already pinned in go.mod / go.sum
# (or, to re-resolve from scratch: go get github.com/cloudwego/eino@v0.9.12 && go mod tidy)
```

## Smoke test (offline, stub model — no API key)

```bash
go run ./scripts/smoke.go
```

Runs, through the real Eino runtime: a chain chat turn, a streamed turn, a ReAct tool call, and the
full adversarial isolation matrix against Eino's real checkpoint store.

## Unit tests (closed-loop)

```bash
go test ./...        # 6 tests: chat, stream, ReAct+tool, + 3 isolation cases
```

## Live serve

```bash
go run ./cmd/server/main.go   # :4113, real Eino runtime, stub model, no key

curl -s -X POST http://localhost:4113/v1/tenant/acme/user/alice/chat \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"how do I reset my password?","session_id":"s1","note":"prefers email"}'
```

## Isolation model — **None** (real proof)

Eino is an orchestration library, not a multi-tenant runtime. Its persistence surface is the real
interface (`github.com/cloudwego/eino/internal/core`, re-exported as `compose.CheckPointStore`):

```go
type CheckPointStore interface {
    Get(ctx context.Context, checkPointID string) ([]byte, bool, error)
    Set(ctx context.Context, checkPointID string, checkPoint []byte) error
}
```

**One bare developer-supplied string keys all persisted state. There is no tenant/user dimension
anywhere in the interface, and there is no default implementation** — the developer owns the key
and therefore owns (or forgets) the scoping.

`orbit/memory.go` builds a real Eino graph (`START → remember → (interrupt) → recall → END`),
compiles it with `WithCheckPointStore` + `WithInterruptBeforeNodes`, seeds tenant A's secret under a
checkpoint ID via `WithCheckPointID`, and then resumes:

```
Correct path  : Seed("tenantA:shared-session", secret); Read("tenantB:shared-session") → miss (isolated)
THE LEAK      : Seed("shared-session",        secret); Read("shared-session")         → tenant B gets tenant A's secret
```

Both paths are one line apart — the only difference is whether the developer remembered to prefix
the checkpoint ID with the tenant. The leak is produced by **Eino's real runtime** resuming a
checkpoint by its bare string key, not by a hand-rolled map. See `scripts/smoke.go` Part 2 and
`orbit/orbit_test.go` (`TestIsolationSharedIDLeaks`).

Additionally, Eino's per-node graph state is developer-defined (`WithGenLocalState`) and carries no
built-in tenant scoping primitive — you get exactly the fields you declare, keyed by exactly the
checkpoint string you pass.

## Features covered (11-point Orbit checklist)

See `FEATURES.md` for the full coverage map with coverage classes. Summary:

| # | Feature | Class | Notes |
|---|---|---|---|
| 1 | Multi-tenant isolation | **NOT-POSSIBLE** (by framework) | `CheckPointStore` is a bare string key; proven leak |
| 2 | Structured output + retry | DIY / partial | provider JSON mode + `schema` parser; no fail-closed reflection loop |
| 3 | Tool calling | **BUILT-IN** | `react.NewAgent` + `components/tool`; exercised in smoke |
| 4 | Streaming | **BUILT-IN** | real `StreamReader`; exercised in smoke |
| 5 | Working memory | CONVENTION | `CheckPointStore` + `WithCheckPointID`, developer-keyed |
| 6 | Semantic memory (vectors) | DIY | not in core; issue #995 proposes vector middleware |
| 7 | Facts memory (journal) | DIY | no equivalent |
| 8 | Cost governance | DIY / partial | post-hoc token callbacks; no pre-spend gate |
| 9 | Observability | DIY / PAID | APMPlus/CozeLoop/Langfuse/LangSmith; no native OTel gen_ai semconv |
| 10 | Sovereignty / licensing | Apache-2.0 | ByteDance-operated — sovereignty caveat for Western regulated buyers |
| 11 | Language | Go | same language as aisotropy |

## What actually executed here

- `go build ./...` — clean (Go 1.26)
- `go vet ./...` — clean
- `go test ./...` — **6/6 pass** (`ok eino-arena/orbit`)
- `go run ./scripts/smoke.go` — Part 1 (chat/stream/ReAct) all ✓; Part 2 leak reproduced as expected

The live server (`cmd/server`) is code-complete and compiles; the smoke + unit tests are what were
executed end-to-end.
