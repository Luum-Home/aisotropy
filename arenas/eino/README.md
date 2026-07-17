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
| State persistence (interrupt/resume KV) | **Real** | `compose.CheckPointStore` (Get/Set by string) + `compose.WithCheckPointID` + interrupt/resume — a checkpoint KV, not a memory tier |
| The LLM itself | **Stub** | `orbit.StubChatModel` implements the real `model.ToolCallingChatModel` interface; deterministic, keyless |

Swap `NewStubChatModel()` for `eino-ext`'s `openai`/`claude` ChatModel and the **exact same graphs**
talk to a live LLM — nothing else changes.

## What Eino does genuinely well (steelman — RUN-VERIFIED)

A fair comparison has two sides. Eino is ByteDance's **production graph-orchestration framework**
(LangGraph-for-Go), and on its home axis — **type-safe, composable, streaming orchestration in Go** —
it is genuinely strong. aisotropy is a multi-tenant *runtime*, not a general graph-orchestration
library; on this axis Eino is the reference, and it is a real strength, not a consolation prize.

`cmd/strengths/eino_strengths_demo.go` proves three of them against the pinned **v0.9.12** API,
offline, exit 0:

| Strength | Real v0.9.12 API (cited) | Run-verified evidence |
|---|---|---|
| **Type-safe graph orchestration + free mode boxing** | `compose.NewGraph[I,O]`, `compose.InvokableLambda[I,O]`, `compose.Runnable[I,O]` | A typed `string→int` graph (`START→parse→double→END`) — node wiring is **compile-checked** by Go generics. The *same* compiled `Runnable` served both `Invoke` (→10) and `Stream` (→`[10]`) with no extra code: author Invoke-only, get all four modes (`Invoke/Stream/Collect/Transform`) boxed for free. |
| **First-class stream primitives** | `schema.Pipe[T]`, `StreamReader.Copy(n)`, `schema.MergeStreamReaders[T]` | `Copy(3)` fanned one producer to 3 independent readers that each saw the full `[1 2 3 4 5]` (framework-managed buffering); `MergeStreamReaders` folded 2 streams into `[10 20 30 40 50]` exactly-once. Generic, composable plumbing most agent frameworks make you hand-roll. |
| **Callbacks / aspects (AOP seam)** | `callbacks.NewHandlerBuilder().OnStartFn/OnEndFn().Build()`, `compose.WithCallbacks` | One handler attached at call time wrapped every component — captured **3 start / 3 end** spans (`[Graph Lambda Lambda]`) around a 2-node graph with the node bodies untouched. This is the exact seam eino-ext's Langfuse/APMPlus/LangSmith backends plug into. |

**Run:** `go run ./cmd/strengths` (from `eino/`) — exits 0.

This does not soften the de-strawman finding — Eino still has no tenancy/memory-isolation axis (see
below). The two sit side by side: **Eino is great at type-safe streaming graph orchestration; it
simply doesn't play in the isolation-by-default space** that aisotropy is built around.

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

## Containerized smoke (Docker — additive, native tests stay)

The native `go test`/`smoke.go` above run in-process. `docker/` additionally proves the **same
minimal app works packaged as a production Go image** — multi-stage build → static, CGO-free binary
→ `gcr.io/distroless/static-debian12:nonroot` (no shell, no libc, non-root UID 65532), serving HTTP
on `:4113`.

```bash
bash docker/run.sh
```

One command: builds `docker/Dockerfile`, runs the container (publishes `:4113`), waits for
`GET /healthz`, then drives the tenant-isolation assertion **against the containerized server over
HTTP** — seeds `secret-A` as tenant `acme`, reads it back (must see it), then cross-reads the **same
`session_id` as tenant `globex`** (must **not** see it, because the server composes the
working-memory key from the path `tenant_id` — the "correct path" isolation below). It asserts,
prints the image size, and cleans up (container always; image unless `KEEP_IMAGE=1`). If Docker is
unavailable the script **skips cleanly (exit 0)** — it never fakes a pass.

Run-verified: image **≈26 MB** (distroless static), health OK, tenant A sees its secret, tenant B
does not. Env knobs: `HOST_PORT` (default 4113), `IMAGE`, `KEEP_IMAGE`.

## Isolation model — **None** (real proof)

Eino is an orchestration library, not a multi-tenant runtime — the same category as LangGraph or
Pydantic AI: no tenancy/memory-isolation primitive at all. Its persistence surface is the real
interface (`github.com/cloudwego/eino/internal/core`, re-exported as `compose.CheckPointStore`) — a
human-in-the-loop **interrupt/resume KV**, not a memory tier:

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
checkpoint ID via `WithCheckPointID`, and then resumes both paths at equal weight:

```
Correct path (isolated): Seed("tenantA:shared-session", secret); Read("tenantB:shared-session") → miss
Footgun  (collapses)   : Seed("shared-session",        secret); Read("shared-session")         → returns the seeded secret
```

Both paths are one line apart — the only difference is whether the developer remembered to prefix
the checkpoint ID with the tenant. The footgun path is **not Eino returning the wrong thing**: a
string-keyed KV faithfully returns whatever was stored under the key you query, exactly as any such
store does (including aisotropy's BadgerDB). The classification is **NOT-POSSIBLE by framework**
because Eino has no tenant dimension to notice a dropped segment — scoping is 100% the developer's
key-composition. aisotropy's edge is upstream: it composes the scope key from `context.Context` by
construction, so a wrong/short key is unrepresentable. See `scripts/smoke.go` Part 2 and
`orbit/orbit_test.go` (`TestIsolationSharedIDCollapses`).

Additionally, Eino's per-node graph state is developer-defined (`WithGenLocalState`) and carries no
built-in tenant scoping primitive — you get exactly the fields you declare, keyed by exactly the
checkpoint string you pass.

## Features covered (11-point Orbit checklist)

See `FEATURES.md` for the full coverage map with coverage classes. Summary:

| # | Feature | Class | Notes |
|---|---|---|---|
| 1 | Multi-tenant isolation | **NOT-POSSIBLE** (by framework) | `CheckPointStore` is a bare string key with no tenant dimension; scoping is DIY key-composition |
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
- `go run ./scripts/smoke.go` — Part 1 (chat/stream/ReAct) all ✓; Part 2 isolation matrix behaves as classified (correct path isolates, footgun path collapses)
- `go run ./cmd/strengths` — **3/3 Eino strengths** demonstrated on the real v0.9.12 runtime (type-safe graph + mode boxing, stream primitives, callbacks/aspects); exit 0

The live server (`cmd/server`) is code-complete and compiles; the smoke + unit tests are what were
executed end-to-end.
