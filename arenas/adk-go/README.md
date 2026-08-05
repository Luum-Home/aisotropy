# ADK Go Arena

> **Framework:** Google ADK Go — `google.golang.org/adk/v2` v2.0.0 (Apache 2.0)
> **Integration:** REAL. This arena depends on the actual framework (`go get
> google.golang.org/adk/v2`), builds the Orbit chatbot with the real
> `llmagent` + `runner` + `session.Service`, and runs offline against a
> `FakeModel` that implements the real `model.LLM` interface — no API key.
> **Positioning:** Embeddable, code-first agent runtime with a graph/workflow
> engine, A2A + MCP + OTel. Session/state scoping is a composite
> `(appName, userID, sessionID)` key — **three native slots**, with `appName`
> bound at runner construction. aisotropy binds **four** axes from
> `context.Context`; the arena's thesis is that mapping four axes onto three
> native slots leaves exactly one axis to hand-composition.

## What is real here

| Piece | Real framework symbol used |
|---|---|
| Agent | `llmagent.New(llmagent.Config{...})` → `agent.Agent` |
| Runner | `runner.New(runner.Config{...})`, `Runner.Run(ctx, userID, sessionID, msg, cfg)` |
| Session/state | `session.InMemoryService()` (the real `session.Service`) |
| Model | our `orbit.FakeModel` implementing the real `model.LLM` (offline, no key) |
| Message types | `google.golang.org/genai` `Content`/`Part` |

`orbit/` imports `google.golang.org/adk/v2/{agent,agent/llmagent,runner,session,model}`.
There is no simulation of the framework — only the **model** is faked (via the
framework's own `model.LLM` seam) so the smoke runs offline.

## Setup

```bash
# Requires Go 1.25+ (adk/v2 go.mod declares go 1.25.0)
go mod tidy          # pulls google.golang.org/adk/v2 v2.0.0 + genai
```

## Smoke test (offline, fake model — no API key)

```bash
go run ./scripts
```

Runs the adversarial isolation matrix against the real runner + session service.

## Cost + pre-spend gate + observability demo (offline, no API key)

```bash
go run ./cmd/costobs
```

Proves three things by executing against the real runner, printing a
`[BUILT-IN]/[DIY]/[PAID]` verdict for each (synthetic token counts, labeled):

1. **Per-tenant cost tracking** — usage is returned per call on
   `event.UsageMetadata` (BUILT-IN visibility); per-tenant aggregation is DIY.
2. **Pre-spend budget gate** — no ADK primitive (usage is post-hoc), so DIY; a
   call-counter on the fake model proves the model is **not** invoked on a
   rejected (over-budget) turn.
3. **Observability** — an in-memory OTel span recorder captures ADK's real
   `generate_content` / `invoke_agent` spans with `gen_ai.usage.*` attributes
   (BUILT-IN); `tenant.id` is not on ADK spans natively (DIY enrichment).

## Comprehensive feature-parity demo (offline, no API key)

```bash
go run ./cmd/parity
```

Walks **25 executable features** from `arenas/ARENA-SPEC-v2.md`, executing the
real ADK stack (real `llmagent` + `runner` + `session.Service` + `memory.Service`
+ `tool.Toolset`; only the model is faked via the framework's `model.LLM` seam)
and printing `FEATURE <n> <name>: [CLASS] <evidence>` for each. Run-verified
tally: **6 BUILT-IN · 5 PARTIAL · 1 PAID · 9 DIY · 4 NOT-POSSIBLE**. Headline
gap: ADK ships **2 memory tiers** (session KV + keyword recall) vs aisotropy's 4
(KV / FTS5 / vector / batch) — no self-hostable vector tier, no FTS5 facts
journal, no scoring/supersede, no transactional outbox, no sleep consolidation.
See the table in `FEATURES.md`.

## What ADK does genuinely well (steelman, offline, no API key)

```bash
go run ./cmd/strengths
```

This arena's isolation section is a *narrow* critique; it is not the whole
picture. ADK Go is Google's agent framework, and it is genuinely strong exactly
where aisotropy — a single cognitive **runtime**, not a multi-agent
orchestration toolkit — does not compete. `cmd/strengths` executes three of
those strengths against the real, pinned `google.golang.org/adk/v2@v2.0.0`
(only the model is faked via the `model.LLM` seam), each grounded in source and
run to exit 0:

1. **Multi-agent delegation — BUILT-IN.** A coordinator `llmagent` with
   `SubAgents` (`agent/llmagent/llmagent.go:194`) gets the framework-injected
   `transfer_to_agent` tool (`internal/llminternal/agent_transfer.go`,
   `transferAgentName`). The demo's coordinator emits
   `transfer_to_agent(agent_name="refunds_agent")`; the runner routes **in-process**
   to that specialist and a single `runner.Run` returns both the transfer event
   and the sub-agent's answer (final event `Author=="refunds_agent"`). This is
   ADK's flagship capability — aisotropy has no agent-to-agent transfer.
2. **The full callback pipeline — BUILT-IN.** `Before/AfterModelCallbacks`
   (`llmagent.go:228/237`) and `Before/AfterToolCallbacks` (`llmagent.go:316/322`)
   fire around every model and tool step. The demo records the real fire order on
   a tool-using turn: `BeforeModel → AfterModel → BeforeTool → AfterTool →
   BeforeModel → AfterModel` — the tool bracketed inside the model loop, a clean
   governance/observability seam.
3. **gen_ai OTel semantic-convention spans — BUILT-IN.** With no ADK config
   (`internal/telemetry` reads the global `otel.GetTracerProvider()`), the
   framework emits `invoke_agent <name>` and `generate_content <model>` spans
   carrying `gen_ai.*` attributes; the demo captures 7 of them with an in-memory
   recorder. Self-hostable via OTLP — the dimension where ADK ties aisotropy.

Honest framing: **ADK wins on multi-agent orchestration and ties on gen_ai
observability**; aisotropy's narrow edge is isolation-by-default (all four scope
axes bound from `context.Context` as an unforgeable `Scope`), which is a
*different tool* than agent delegation.

## Tests

```bash
go test ./...        # orbit/ has end-to-end + isolation tests
```

## Live serve (offline fake model)

```bash
go run ./cmd/server   # :4114, real ADK runner behind an HTTP surface

curl -s -X POST localhost:4114/v1/tenant/acme/user/alice/chat \
  -H 'Content-Type: application/json' -d '{"prompt":"MEMO: code is 42","session_id":"s1"}'
curl -s -X POST localhost:4114/v1/tenant/acme/user/alice/chat \
  -H 'Content-Type: application/json' -d '{"prompt":"what did I tell you?","session_id":"s1"}'
```

To use a real Gemini model instead of the fake one, swap `orbit.NewFakeModel()`
in `orbit/orbit.go` for `gemini.NewModel(ctx, "gemini-flash-latest", &genai.ClientConfig{APIKey: ...})`
and set `GOOGLE_API_KEY`.

## Containerized smoke (Docker, production-shaped image)

The native smoke above proves the real ADK stack in-process. `docker/` proves
the **same minimal app runs CONTAINERIZED** as a production-shaped Go image —
multi-stage build → static, CGO-free binary on
`gcr.io/distroless/static-debian12:nonroot` (no shell, no libc) — and re-runs the
core isolation assertion end-to-end over HTTP against that container:

```bash
bash docker/run.sh
```

It builds the image (`docker/Dockerfile`), starts the container, waits for
`/healthz`, then drives the assertion against the containerized server:
tenant A (`acme/alice`) writes a secret MEMO over the composite key; the
positive control (`acme/alice`) reads it back; and a cross-tenant probe
(`globex/alice`, same session id) **must not** see it — the server composes the
ADK userID as `tenant|user`, so a different tenant path is a different
`(appName, userID, sessionID)` key and a clean miss. On success it prints
`PASS` and the image size, then removes the container. **If Docker is absent or
its daemon is unreachable, the script SKIPs cleanly (exit 0) — it never fakes a
pass.**

Run-verified: `PASS`, single-platform image **~6.4 MB** (`docker image ls`
reports ~28 MB because it counts the multi-arch + attestation manifests
BuildKit attaches). Caveat: the **build stage must be Go 1.25+**
(`golang:1.25-bookworm`) because `adk/v2`'s `go.mod` declares `go 1.25.0`; a 1.24
base fails with `go.mod requires go >= 1.25.0`. The runtime image carries only
the static binary — the Go toolchain never ships.

Build the image manually (context is the arena root, not `docker/`):

```bash
docker build -f docker/Dockerfile -t adk-go-arena:smoke .
docker run --rm -p 4114:4114 adk-go-arena:smoke
```

## Isolation model — three native slots, four axes, one residual

**Real composite-key construction; the honest gap is arity, not scoping.**

ADK Go's `session.Service` scopes state by the composite
`(appName, userID, sessionID)` key — the same construction class as
`adk-python`'s composite primary key. The in-memory `Get` is a keyed lookup,
not a scan. Crucially, `appName` is bound at **runner construction**
(`runner.Config.AppName` → `r.appName`); `runner.Run(ctx, userID, sessionID, …)`
never takes an `appName`. So `appName` is a **native, non-droppable-per-request**
slot, while `userID`/`sessionID` are per-request strings.

The gap is arity. aisotropy binds **four** scope axes
(`tenant, user, session, product`) from `context.Context`; ADK exposes **three**
native slots. A competent developer binds any one extra axis to `appName` by
running **one runner per value of it** — and that axis becomes native and
un-droppable:

```go
// STRONGEST config for the product axis: one runner per product.
// appName=product is bound at construction and cannot be dropped per request,
// so a cross-product read is a storage miss BY CONSTRUCTION.
runnerX, _ := orbit.New("productX")      // appName = productX
runnerY, _ := orbit.New("productY")      // appName = productY
runnerX.Reply(ctx, "acme|alice", "s1", "MEMO: secret")
runnerY.Reply(ctx, "acme|alice", "s1", "recall?")  // clean miss — different appName
```

But three native slots cannot cover four axes: **exactly one axis** has no slot
of its own and must be concatenated into `userID` (no framework-enforced
separator). THAT axis is the developer-discipline, drop-vulnerable residual:
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — verdict HOLDS,
     NARROWER. `session.Service` is three plain unvalidated strings in v2.0.0 and v2.1.0,
     with no first-party Go extension adding an axis. But ADK does enforce reserved
     separators on other surfaces (WithRunID rejects '/' and '@'; artifact filenames reject
     path separators) — the absence is specific to the identity slots. And the hand-fold is
     not the only way to compose a 4th axis outside session.Service: Vertex AI Memory Bank
     takes an N-key `scope map[string]string` (exact match over all keys, no concatenation),
     and Google's own prescribed multi-tenancy answer is one Cloud project per tenant with
     PAB Policy + VPC-SC. Neither is reachable through ADK Go. -->

```go
// With appName spent on product (or on the app), tenant rides in userID.
// CORRECT: keep tenant in userID -> the composite key isolates.
bot.Reply(ctx, tenantID+"|"+userID, sessionID, prompt)
// RESIDUAL LEAK: drop tenant -> acme/alice and globex/alice collide (same session).
bot.Reply(ctx, userID, sessionID, prompt)
```

The choice is symmetric: spend `appName` on tenant and PRODUCT becomes the
residual. Exactly one of {tenant, product} always lacks a native slot; the
developer picks which. The leak is a **chosen-mapping residual**, not an ADK
inability to isolate — the correct path isolates cleanly (`TestCompositeKeyIsolates`,
and the `appName=product` construction miss in `TestProductAppNameIsolatesByConstruction`).
<!-- SWEPT: 2026-08-04 docs/architecture/arena-sweep-adk-go-2026-08-04.md — "exactly one of
     {tenant, product} lacks a native slot" holds for ADK Go: three slots in v2.0.0 and
     v2.1.0, and no first-party Go extension adds a fourth (proxy lists only `adk` and
     `adk/v2`). The N-key scope that would remove the fold exists one tier up, in Vertex AI
     Memory Bank, and ADK Go's binding does not expose it. -->

Contrast `adk-python` bug `google/adk-python#1641`: even with composite PKs, the
events sub-query **omitted the `user_id` filter**, a real cross-user disclosure
through v1.4.2 — the construction is only as strong as every query remembering
the whole key. That risk is per-query in both frameworks; aisotropy's edge is
that it hand-composes **zero** axes.

**Auth is a separate, near-parity layer.** Binding the key to an authenticated
caller is middleware, not key construction. ADK provides the seam (an `llmagent`
`BeforeAgentCallback` can reject an unbound identity *before* the model runs);
aisotropy itself trusts the path `tenant_id` verbatim in its default NoAuth mode,
verifying only when `AISOTROPY_JWT_SECRET` is set (ADR-0024). "Trusted until a
gate binds it" is true of both.

> The previous version of this arena claimed adk-go has "no scoping / untyped
> `map[string]any`." That was wrong: the real `session.Service` keys by
> `(appName, userID, sessionID)`. An intermediate version overstated the gap as
> "no tenant binding" and manufactured a product leak by dropping a hand-folded
> component — a same-key tautology. The accurate story is **three native slots,
> four axes, one hand-composed residual** — corrected here against v2.0.0 source.

## Features covered (see FEATURES.md for detail)

| # | Feature | Status | Real symbol |
|---|---|---|---|
| 1 | Multi-tenant isolation | BUILT-IN key + 1 hand-composed axis | `session.Service` composite `(appName, userID, sessionID)` |
| 2 | Structured output + retry | BUILT-IN | `llmagent.Config.OutputSchema` (`*genai.Schema`) |
| 3 | Tool calling | BUILT-IN | `tool.Tool`, `functiontool`, `mcptoolset` |
| 4 | Streaming (SSE) | BUILT-IN | `agent.StreamingModeSSE` |
| 5 | Working memory | BUILT-IN | `session.Service` (in-mem / sqlite / vertexai) |
| 6 | Semantic memory | PARTIAL | `memory.Service` (in-mem search; vectors = VertexAI MemoryBank) |
| 7 | Facts memory (journal) | DIY | none |
| 8 | Cost governance | PARTIAL — usage [BUILT-IN], aggregation + pre-spend gate [DIY] | `event.UsageMetadata` per call; no pre-spend gate (run-verified `cmd/costobs`) |
| 9 | Observability | BUILT-IN (OTel `gen_ai.*`); tenant_id [DIY] | OTel spans w/ `gen_ai.usage.*`; no `tenant.id` natively (run-verified `cmd/costobs`) |
| 10 | Sovereignty / licensing | Apache 2.0 (Google); rich memory Vertex-tethered | — |
| 11 | Language | Go | — |

## Adversarial isolation result

**Verified on the real framework.** The smoke, `cmd/productiso`, and
`go test ./...` show:

- Positive control: `acme|alice` recalls its own memo through the real runner. ✓
- Correct-key probes (`globex|bob`, `acme|bob`, `globex|alice`) **isolate** —
  the composite key is a real construction. ✓
- STRONGEST config (`appName=product`, one runner per product): a cross-product
  read of the SAME `userID`+`sessionID` is a **construction miss** — `appName` is
  a native slot bound at runner construction, un-droppable per request. ✓
- Residual axis (the one axis with no native slot, e.g. tenant folded into
  `userID`): kept in the key it isolates; **dropped, it collides** — a genuine
  same-session read. A chosen-mapping cost, not an ADK inability. ✗ (leaky path)
- Auth is a separate, near-parity layer: the bare key is trusted until a gate
  binds it (ADK `BeforeAgentCallback`; aisotropy JWT / else NoAuth) — a parity
  note, not an ADK-specific flaw.

See `orbit/isolation.go` + `orbit/product_isolation.go` for the matrices and
`scripts/smoke.go` / `cmd/productiso` for the runners.
