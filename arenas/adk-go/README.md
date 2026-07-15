# ADK Go Arena

> **Framework:** Google ADK Go — `google.golang.org/adk/v2` v2.0.0 (Apache 2.0)
> **Integration:** REAL. This arena depends on the actual framework (`go get
> google.golang.org/adk/v2`), builds the Orbit chatbot with the real
> `llmagent` + `runner` + `session.Service`, and runs offline against a
> `FakeModel` that implements the real `model.LLM` interface — no API key.
> **Positioning:** Embeddable, code-first agent runtime with a graph/workflow
> engine, A2A + MCP + OTel. Session/state scoping is a developer-passed
> `(appName, userID, sessionID)` key with **no tenant binding**.

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

## Isolation model — developer-passed keys, no tenant binding

**Convention, not construction — but for a specific, correctable reason.**

ADK Go's `session.Service` **does** scope state by the composite
`(appName, userID, sessionID)` key — the same construction class as
`adk-python`'s composite primary key. The in-memory `Get` is a keyed lookup,
not a scan:

```go
// runner.Run(ctx, userID, sessionID, ...) -> session key (appName, userID, sessionID)
// The composite key isolates: a *distinct* key is a clean miss.
```

The gap is upstream of the key:

1. **No tenant primitive.** ADK has `AppName`, `UserID`, `SessionID` — and no
   `TenantID`. A multi-tenant developer must fold "tenant" into one of those
   fields themselves. Forget it (a one-line bug) and two tenants' `alice`
   collide on the same session → silent cross-tenant read.
2. **No identity binding.** `runner.Run(ctx, userID, sessionID, …)` trusts
   `userID`/`sessionID` verbatim. Nothing ties them to an authenticated caller.
   Attacker-controlled or replayed identity strings read the victim's session.

```go
// THE CORRECT PATH (this arena's server): fold tenant into the ADK userID.
identity := tenantID + "|" + userID
bot.Reply(ctx, identity, sessionID, prompt)

// THE LEAKY PATH (one line shorter): drop the tenant component.
bot.Reply(ctx, userID, sessionID, prompt)   // acme/alice and globex/alice collide
```

Contrast `adk-python` bug `google/adk-python#1641`: even with composite PKs,
the events sub-query **omitted the `user_id` filter**, producing a real
cross-user event disclosure through v1.4.2. The construction is only as strong
as every query remembering the whole key — and ADK Go leaves the *tenant* part
of that key entirely to developer discipline.

> The previous version of this arena claimed adk-go has "no scoping / untyped
> `map[string]any`." That was wrong: the real `session.Service` keys by
> `(appName, userID, sessionID)`. The accurate story is **developer-passed
> keys with no tenant binding** — corrected here against v2.0.0 source.

## Features covered (see FEATURES.md for detail)

| # | Feature | Status | Real symbol |
|---|---|---|---|
| 1 | Multi-tenant isolation | CONVENTION (no tenant binding) | `session.Service` composite key |
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

**Verified on the real framework.** The smoke and `go test ./...` show:

- Positive control: `acme|alice` recalls its own memo through the real runner. ✓
- Correct-key probes (`globex|bob`, `acme|bob`, `globex|alice`) **isolate** —
  the composite key is a real construction. ✓
- Leaky path (tenant dropped from the key) **leaks** — `globex/alice` reads
  `acme/alice`'s memo because ADK has no tenant field to tell them apart. ✗
- Raw `session.Service` probe: a replayed/attacker key is **trusted verbatim**
  (no identity binding). ✗

See `orbit/isolation.go` for the matrix and `scripts/smoke.go` for the runner.
