---
type: Reference
title: Reality ledger — what runs by default, what is dormant, what is aspirational
description: The canonical at-a-glance status node for Aisotropy. Classifies every stack layer and headline capability as REAL (runs in the default binary + verified by the local test suite), CODE-ONLY (implemented but dormant behind an env var / verified only under the manual build tag), or ASPIRATIONAL (design/docs only). It is the single source of truth the entry-point docs point to, and it exists to keep the knowledge base honest — "if you haven't measured it, it's not a claim."
tags: [architecture, as-built, status, reality, honesty, ledger]
timestamp: 2026-07-14T00:00:00Z
---

# Reality ledger — what runs, what is dormant, what is aspirational

This is the **canonical status node**. When any other document (README, `index.md`, roadmap,
a pitch) implies a capability is live, this ledger is the arbiter of whether that is true
*today*, verified by reading the code and the test surface rather than the design docs. (Note,
2026-07-14: there is no CI pipeline in this repo — no `.github/` — so "verified" here means the
local test suite, `go test ./...`, not an automated gate.)

It exists because the knowledge base's own epistemic rule — *"if you haven't measured it, it's
not a claim"* ([principles](../vision/principles.md)) — has to be enforced somewhere, and because
the differentiators this project sells (sovereign Rust services, tenant isolation, native
tool-calling) are exactly the ones most tempting to overstate. The verdict of the
[build-vs-adopt decision](../decisions/0020-build-vs-adopt-lang-suite-mastra.md) rests on this
ledger being accurate.

Three honesty labels are used throughout:

- **REAL** — wired into the default `aisotropyd` binary AND exercised by the local test suite
  (`go test ./...`, no build tags).
- **CODE-ONLY** — implemented and reviewed, but **dormant by default**: reached only via an env
  var / compose overlay, and verified only under a `//go:build manual` test outside the default
  test run.
- **ASPIRATIONAL** — described in the design docs; not implemented, or a designed-but-unadopted
  seam.

The deep, code-grounded companion is [runtime as built](runtime-as-built.md) (the step-by-step
walk); this node is the one-screen summary. Where the two disagree, the code wins and this ledger
is corrected first.

## What boots with ZERO configuration

`./aisotropyd` with no env vars and no API key is a **complete, single-process Go runtime**:
chi + Huma serve `/openapi.json`, `/docs`, `/healthz`, `/health`, `/costs`, and seven `/v1`
routes; a chat turn is validated by a Go struct, run through a **real Gollem reflection loop**
(homegrown fallback), streamed over **real SSE with correct ctx-cancellation**, persisted to
**BadgerDB working memory with a transactional outbox** drained by a background worker into
**in-process SQLite+FTS5 facts** and an **in-process brute-force cosine semantic index** — all
per-tenant rate-limited, circuit-broken, cost-metered, and scope-gated, with the canned stub LLM
answering when no key is set.

What does **not** engage with zero config is precisely the heaviest part of the pitch: the
**Qdrant gRPC tier, the networked rqlite tier, the DataFusion/Rust sleep cycle, and provider
native tool-calling** are all real code but dormant. The runtime is a legitimately well-architected
in-process engine today, with the "sovereign Rust-services moat" one integration-and-verification
step behind the code.

## The ledger

| Layer / capability | Status | Default (zero-config) | Production form (how to light it up) | Verified by |
|---|---|---|---|---|
| **API & contracts** (Huma v2 + chi, OpenAPI 3.1, SSE + ctx-cancel) | **REAL** | Serves all 7 routes + docs | same | 36 tests in `internal/api` (local suite) |
| **Working memory** (BadgerDB, summary buffer, transactional outbox, cold-boot recovery) | **REAL** | in-memory BadgerDB | on-disk via `AISOTROPY_DATA_DIR` | 28 tests (local suite) |
| **Facts memory** (SQLite + FTS5, Engram-style, tenant-scoped statement builder) | **REAL** (in-process) · **CODE-ONLY** (networked) | in-memory SQLite+FTS5 | file SQLite, or **rqlite** via `RQLITE_URL` | 25 local-suite tests; rqlite only under `manual` tag |
| **Reflection loop** (Gollem default, homegrown fallback, fail-closed 502 on `/chat`) | **REAL** | Gollem reflection on `/chat` | same | local suite (`reflect_auto`, `gollem`) |
| **Semantic memory** (Qdrant, payload-filter isolation, multi-kind Document/Fact/Turn) | **CODE-ONLY** | in-process brute-force cosine | **Qdrant** gRPC via `QDRANT_HOST` | in-process path in the local suite; Qdrant only under `manual` tag |
| **Native tool-calling agent** (Gollem `Agent[T]` + `FuncTool`, ADR-0012) | **CODE-ONLY** | falls back to homegrown `agentloop` | needs a `ToolCaller` provider (**only** `OpenAICompatClient`/OpenRouter today) | unit tests via fake ToolCaller (local suite); live only under `manual` |
| **Sleep cycle** (DataFusion Rust subprocess, ADR-0005) | **CODE-ONLY** | disabled (only the pure-Go turn-vector pruner runs) | build the crate + set `AISOTROPY_SLEEPCYCLE_BIN` | the default suite uses a **bash stub**; real binary only under `manual` |
| **Granular control** (per-tenant rate limit, circuit breaker, cost meter, scope-gated tools, `/health`, `/costs`) | **REAL** | all wired | same | local suite |
| **Public extension surface** (`aisotropy/` façade + builder + nested example modules) | **REAL** | `cmd/aisotropyd` is a thin consumer | third parties import `aisotropy/` | 15 façade tests + 2 nested example modules (local suite) |
| **Physical tenant routing** (ADR-0007 Shared/Dedicated tiering) | **CODE-ONLY** | shared pool (`AISOTROPY_DEDICATED_TENANTS` empty → no routing) | set `AISOTROPY_DEDICATED_TENANTS`: [`aisotropy/app.go:247-248`](../../aisotropy/app.go) builds the router, injected into `facts.NewRoutingStore` (app.go:677) and `semantic.NewRoutingStore` (app.go:769) | `internal/routing` tests (local suite); wiring dormant by default (reclassified 2026-07-14) |
| **Primitive self-improvement** (friction-driven per-user rewriting) | **REAL** | wired unconditionally: [`aisotropy/app.go:534-536`](../../aisotropy/app.go) builds the store + friction detector and starts the consolidation loop; a failing agent tool records friction (`internal/api/chat_agent.go:59-65`), and `primitives.Consolidate` rewrites over-threshold primitives on every sleep-cycle tick (app.go:614-624) | same | `internal/primitives` + `frictionExecutor` tests (local suite) (reclassified 2026-07-14) |
| **Compression bets 1–5** (surprisal-gated memory, working-memory codec, rate-distortion context, compressibility routing, sovereign NN core) | **ASPIRATIONAL** | — | — | framework/spikes only; **only Bet 6 (bits/token) is spike-validated offline** |

## Known honesty gaps (fix these before quoting them)

1. **The entry-point status was stale — now reconciled (2026-07-14).** `README.md` announced
   "Phase 0–1" and the `milestones.md` banner lagged on the ADR range; both now state the real
   state (Phases 0–7 implemented, Phase 8 shipped, Phase 9 partial, ADRs through 0023 accepted).
   This ledger remains the arbiter if they drift again.
2. **`runtime-as-built.md` is itself behind.** It predates the Phase-8 façade (the wiring point is
   now [`aisotropy/app.go`](../../aisotropy/app.go), not `cmd/aisotropyd/main.go`), ADR-0012 native
   tool-calling, and ADR-0016…0019 — e.g. it still says "there is no native provider
   function-calling," which ADR-0012 changed. It needs a refresh (tracked, not done here).
3. **The "−72% fewer input tokens than LangChain" figure is measured against a naive baseline**
   (full-history LangChain, no summarization middleware). The *mechanism* (surgical context
   assembly) is a real efficiency lever; the number is a synthetic-harness artifact and must be
   quoted only with its `bench/token-economy/` caveat — never as "72% more efficient than
   LangChain."
4. **Anthropic silently loses native tool-calling.** `FromEnv` precedence is OpenRouter > OpenAI >
   Anthropic > stub, but only `OpenAICompatClient` implements `CallTools`. With the stub or the
   Anthropic client, `/chat/agent` always takes the homegrown `agentloop` fallback. The
   best-supported provider for the *full* feature set is OpenAI-compatible/OpenRouter, not
   Anthropic.
5. **`docker-compose.yml` is a reference artifact, not a recorded result.** There is no captured
   evidence the Qdrant/rqlite stack has been booted and health-checked in this repo; the "services
   are live / the moat is active" framing is aspirational verification.

## What this ledger does NOT diminish

The things that are **REAL** are the things that are hardest to retrofit later and that the
competitive analysis found genuinely superior: **struct-as-contract with no hidden prompts**,
**µs in-process working memory**, **tenant isolation propagated by `context.Context` and enforced
per statement/key**, **circuit-breaker + per-tenant rate limit + cost meter before any token is
spent**, and a **binary-plus-one-service supply chain**. Those are running today. The dormant tiers
do not weaken that core — they are the *scale* story (independent Rust services) layered on top of
a sound in-process engine.

## References

- Deep walk: [runtime as built](runtime-as-built.md) · [phases 3–7 as built](phases-3-7-as-built.md)
- Positioning that depends on this ledger: [ADR-0020 — build vs adopt](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
- Measurement discipline: [principles](../vision/principles.md) · [roadmap OKRs](../roadmap/okrs.md)
- Wiring: [`aisotropy/app.go`](../../aisotropy/app.go) · thin binary [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go)
