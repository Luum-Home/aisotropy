---
type: Concept
title: Adoption surface as built — the public aisotropy package + builder
description: Code-grounded description of the Phase 8 extension surface as implemented (ADR-0013) — the importable `aisotropy` façade package (type aliases + read-only identity), the `App` builder that internalizes the composition root, the two internal seams (PersonaProvider, injectable ToolExecutor), and the example nested module that proves public-surface-only.
tags: [architecture, as-built, adoption, extension, facade, builder, phase-8]
timestamp: 2026-07-09T00:00:00Z
---

# Adoption surface as built

This is the code-grounded companion to [what Aisotropy is](what-aisotropy-is.md)'s "gap" section
and the as-built counterpart to [ADR-0013](../decisions/0013-public-extension-surface-facade-vs-promote.md).
It describes the Phase 8 extension surface **as the code implements it today**, verified against a
green `go build ./...` + `go test ./...`. The consumer-facing walkthrough is
[build your own agent](../guides/build-your-own-agent.md); this document is the architecture view.

## What changed

A third party can now build a bot from **another Go module** by importing one package and supplying
only *domain* (tools + executor + persona). The reusable engine stays under `internal/`; nothing was
promoted out of it. `cmd/aisotropyd/main.go` went from ~480 lines of hand-wiring to a **~51-line thin
consumer** of the same builder every external bot uses — the runtime is dogfooded through its own
public surface.

## The public package `github.com/Luum-Home/aisotropy/aisotropy`

Two files, no engine logic:

- [`aisotropy/aisotropy.go`](../../aisotropy/aisotropy.go) — **type aliases** (so a consumer gets the
  real internal types without naming `internal/…`) + read-only identity re-exports.
- [`aisotropy/app.go`](../../aisotropy/app.go) — the `App` builder, functional `Option`s, and
  `App.Run`, which internalizes everything the composition root used to do.

### Surface (the entire public contract)

```go
type Tool = tools.Tool                        // {Name, Description, RequiredScope}
type ToolExecutor = agentloop.ToolExecutor    // Execute(ctx, name string, args map[string]string) (string, error)
type LLMClient = llm.Client
type Token = llm.Token
type Usage = llm.Usage
type Persona struct{ BasePrompt string }      // value type, not an alias

func TenantFromContext(ctx context.Context) (string, bool) // read-only
func UserFromContext(ctx context.Context) (string, bool)

type App struct{ /* unexported */ }
type Option func(*App)
func New(opts ...Option) *App
func (a *App) Run(ctx context.Context) error  // boots the full runtime, blocks until graceful shutdown

func WithAddr(addr string) Option
func WithTool(t Tool) Option                  // accumulates
func WithToolExecutor(exec ToolExecutor) Option
func WithPersona(productID string, p Persona) Option
func WithLLM(client LLMClient) Option
func WithRateLimit(perSec, burst float64) Option
func WithCostPricing(inPer1M, outPer1M float64) Option
```

**Deliberately not exposed** (ADR-0013 / ADR-0011 / ADR-0002): `tools.Registry`,
`facts.Scope`/`NewScope`/`facts.Store.ExportAll`, `memory.Key`, `internal/tenancy`,
`internal/routing`, and every `llm` decorator. Scope construction stays runtime-owned; personas are
literal strings. Memory backends (facts/semantic) are **env-selected only** — there is no public
backend-injection option, because exposing `facts.Store` would leak the cross-tenant `ExportAll`.
Every option in the public surface is therefore callable by an external consumer (no dead API).

## The two internal seams that made it possible

The gap was never only visibility — two things were not injectable at all:

1. **Injectable executor.** [`internal/api/chat_agent.go`](../../internal/api/chat_agent.go)
   previously hardcoded `echoExecutor{}`. It is now an exported `DemoExecutor`, and `RegisterAgent`
   takes an `agentloop.ToolExecutor` parameter (still wrapped by the friction-recording executor).
   The builder threads the consumer's executor through; the default binary passes `DemoExecutor`.
2. **Injectable persona.** The base prompt was a package-private `systemPromptFor(productID)` in
   `chat_stream.go`. It now lives behind a `PersonaProvider` interface in
   [`internal/api/persona.go`](../../internal/api/persona.go):
   `BasePrompt(ctx, productID) (string, error)`. `defaultPersonaProvider` reproduces the original
   strings byte-for-byte; `NewMapPersonaProvider` composes per-`product_id` personas over that
   default. `Register`/`RegisterStream`/`hydrateSystemPrompt` all take a `PersonaProvider`. The
   provider returns a **literal string** — no templating (anti-magic gate, ADR-0002).

## `App.Run` — what a consumer inherits for free

`Run` is the extracted composition root. It constructs and wires, in order: working memory
(`internal/memory`, in-mem or `AISOTROPY_DATA_DIR`), physical tenant-tier routing
(`AISOTROPY_DEDICATED_TENANTS`), env-driven facts + semantic stores, the consolidation sink + outbox
worker + cold-boot `RecoverOutbox`, the OTel tracer, the circuit breaker, optional cheap-model
degradation, the optional sleep-cycle worker (`AISOTROPY_SLEEPCYCLE_BIN`), the per-tenant rate
limiter, the cost meter + `GET /costs`, `GET /healthz`, `api.Register`/`RegisterStream`/`RegisterAgent`,
the primitives friction consolidation loop, and `SIGINT`/`SIGTERM` graceful shutdown that closes
every store. The env-var contract is identical to the pre-Phase-8 binary.

## The example proves the guarantee mechanically

[`examples/weatherbot`](../../examples/weatherbot) is a **nested module** — its own `go.mod` with
`replace github.com/Luum-Home/aisotropy => ../..`. It defines a real `weatherExecutor` and imports
**only** `…/aisotropy`. Because it is a separate module, Go's own `internal/` visibility rule
*physically forbids* it from importing `internal/…` — so "public surface only" is enforced by the
compiler, not by lint. `grep -rn internal/ examples/weatherbot` returns zero. The `cmd/x` dev
console gained `example-build` / `example-vet` / `example` tasks (and `check` runs them) so CI
exercises it.

## Honest limitations / follow-ups

- **Not wired into the stock binary's behavior beyond parity.** The dogfooded `main.go` registers the
  same 3 demo tools + `DemoExecutor` + default persona as before; routes and endpoints are unchanged
  (verified via `/openapi.json`, `/healthz`, `/costs`).
- **`ToolExecutor` args are `map[string]string`.** Kept for the 0.x façade; widening to typed/nested
  JSON is an open question tied to [ADR-0012](../decisions/0012-gollem-native-tool-calling-vs-homegrown-agentloop.md)
  (native tool-calling) — a documented future 0.x break, not silent.
- **Persona equivalence** is pinned at the base-prompt layer by a test; the fully-hydrated prompt
  (base + memory + tools) was not diffed against the pre-Phase-8 path for a live turn.
- **0.x stability.** The `aisotropy` package is pre-1.0: additive within a minor, breaking allowed on
  a minor bump with a CHANGELOG entry. A `v1` commitment waits for 2–3 real consumers (ADR-0013).

## References

- [ADR-0013 — Public extension surface](../decisions/0013-public-extension-surface-facade-vs-promote.md)
- [What Aisotropy is (and is not)](what-aisotropy-is.md) · [Runtime as built](runtime-as-built.md)
- [Build your own agent](../guides/build-your-own-agent.md) — the consumer walkthrough.
