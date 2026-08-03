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

One honesty nuance the seam adds (2026-07-14, ADR-0024): the `./aisotropyd` binary reaches those
`/v1` routes only because it **explicitly opts into `NoAuth()`** (path `tenant_id`/`user_id` trusted
verbatim, with a loud insecure-mode `slog.Warn` at boot). The **library** default
(`aisotropy.New` with no `WithAuthenticator`) is **default-deny** — every `/v1` request is rejected
401 until an authenticator is configured, so shipping insecure is an explicit choice, not an
accident. The end-to-end isolation guarantee is active only under a configured, non-`NoAuth`
Authenticator (e.g. `NewJWTAuthenticator`), which is verified in `aisotropy/isolation_e2e_test.go`.

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
| **Native tool-calling agent** (Gollem `Agent[T]` + `FuncTool`, ADR-0012/0063) | **REAL** with any real provider · falls back for the keyless stub | keyless stub → homegrown `agentloop` | **both** `OpenAICompatClient`/OpenRouter (ADR-0012) **and** `AnthropicClient` (native `tool_use`, ADR-0063) implement `ToolCaller` → native path; only the keyless stub falls back | unit tests via fake ToolCaller (local suite); live only under `manual` |
| **Sleep cycle** (DataFusion Rust subprocess, ADR-0005) | **CODE-ONLY** | disabled (only the pure-Go turn-vector pruner runs) | build the crate + set `AISOTROPY_SLEEPCYCLE_BIN` | the default suite uses a **bash stub**; real binary only under `manual` |
| **Granular control** (per-tenant rate limit, circuit breaker, cost meter, scope-gated tools, `/health`, `/costs`) | **REAL** | all wired | same | local suite |
| **Authenticated-identity seam** (ADR-0024: default-deny Authenticator middleware, must-agree bind, `NoAuth` opt-out, stdlib-HS256 `JWTBearer` ref impl) | **REAL** (seam + middleware + must-agree bind) · gap: verified scopes not yet used for tool-gating | library default is **default-deny** (401 all `/v1`); the `./aisotropyd` binary opts into `NoAuth()` (insecure, loudly warned) to stay zero-config | pass `aisotropy.WithAuthenticator(NewJWTAuthenticator(secret))` for a real credential check | `internal/auth` (9 JWT tests + `auth_test`), `internal/api/authn_test.go` (5), `aisotropy/isolation_e2e_test.go` regression anchor (local suite) — added 2026-07-14 |
| **Public extension surface** (`aisotropy/` façade + builder + nested example modules) | **REAL** | `cmd/aisotropyd` is a thin consumer | third parties import `aisotropy/` | 15 façade tests + 2 nested example modules (local suite) |
| **Physical tenant routing** (ADR-0007 Shared/Dedicated tiering) | **CODE-ONLY** (closed-loop when active) | shared pool (`AISOTROPY_DEDICATED_TENANTS` empty → router never consulted, [`app.go:440-448`](../../aisotropy/app.go)) | set `AISOTROPY_DEDICATED_TENANTS`: [`aisotropy/app.go:442-447`](../../aisotropy/app.go) builds the router, injected into `facts.NewRoutingStore` ([app.go:1283](../../aisotropy/app.go)) and `semantic.NewRoutingStore` ([app.go:1415](../../aisotropy/app.go)); `Router.Resource(base,tenant)` is then called per-request ([`facts/routing_store.go:159`](../../internal/facts/routing_store.go), [`semantic/routing_store.go:178`](../../internal/semantic/routing_store.go)) | `internal/routing` tests (100% cover, local suite); wiring dormant by default (reclassified 2026-07-14, line-refs corrected 2026-07-17) |
| **Primitive self-improvement** (friction-driven per-user rewriting) | **OPEN-LOOP** (write-half only) | write + consolidation halves wired unconditionally: [`aisotropy/app.go:885-887`](../../aisotropy/app.go) builds the store + friction detector and starts `runPrimitiveConsolidation`; a failing agent tool records friction (`internal/api/chat_agent.go:62`), and `primitives.Consolidate` rewrites over-threshold primitives on the hourly ticker ([app.go:1211-1225](../../aisotropy/app.go)). **The read-back is NOT wired** — no hydration path calls `Store.Get`/`ListForUser`, so the rewritten metadata never reaches the LLM and changes no output | same (read-back is unbuilt, not env-gated) | `internal/primitives` (96.6% cover) + `frictionExecutor` tests (local suite); classification corrected REAL→OPEN-LOOP 2026-07-17 to agree with [runtime-as-built §6](runtime-as-built.md) and [components/primitives.md](../components/primitives.md) |
| **Compression bet 5 first brick** (sovereign local embedder, `crates/embed`, ADR-0023/0049) | **CODE-ONLY** | Hash/API embedder | Rust candle `crates/embed` via `AISOTROPY_EMBED_BIN` (daemon default) | `internal/semantic` seam tests (local suite); live only under `manual` (`TestDaemonEmbedderLiveWiring`) — carved out of the "bets 1–5 aspirational" row 2026-07-17 (ADR-0023 status: implemented + wired) |
| **Compression bets 1–4** (surprisal-gated memory, working-memory codec, rate-distortion context, compressibility routing) | **ASPIRATIONAL** (Bets 1/2/4 **measured NEGATIVE**, ADR-0021) | — | — | Python research spikes only (`spikes/compression/*.py`); **Bet 6 (bits/token) is the one spike-validated offline** — the negative results are kept as science (ADR-0021), not shipped code |

## Known honesty gaps (fix these before quoting them)

1. **The entry-point status drifts and this ledger itself lags the ADR set.** ADRs now run
   **through 0080** (auth, plugins, encryption, GDPR, multilingual, voice, observability,
   Apache-2.0 relicense). The `milestones.md` banner still cites an old ADR range (see the roadmap
   reconciliation), and the capability table above does **not yet enumerate** the 0032–0080
   families (multilingual, voice, plugins, encryption/GDPR) — treat those as shipped-per-ADR but
   not-yet-rowed here. Timestamp this ledger and re-row it when reconciling; it is the arbiter only
   as far as it is current.
2. **The "−72% fewer input tokens than LangChain" figure is measured against a naive baseline**
   (full-history LangChain, no summarization middleware). The *mechanism* (surgical context
   assembly) is a real efficiency lever; the number is a synthetic-harness artifact and must be
   quoted only with its `bench/token-economy/` caveat — never as "72% more efficient than
   LangChain."
3. **Anthropic native tool-calling — RESOLVED (ADR-0063, 2026-07-17).** `AnthropicClient` now
   implements `CallTools` via native `tool_use`, so **both** OpenAI-compatible/OpenRouter and
   Anthropic take the native Gollem tool-calling path; only the **keyless stub** falls back to the
   homegrown `agentloop` (expected — the stub has no provider to call). The earlier "Anthropic
   silently loses tool-calling" gap no longer holds.
4. **`docker-compose.yml` is a reference artifact, not a recorded result.** There is no captured
   evidence the Qdrant/rqlite stack has been booted and health-checked in this repo; the "services
   are live / the moat is active" framing is aspirational verification.

## What this ledger does NOT diminish

The things that are **REAL** are the things that are hardest to retrofit later and that the
competitive analysis found genuinely superior: **struct-as-contract with no hidden prompts**,
**µs in-process working memory**, **tenant isolation propagated by `context.Context` and enforced
per statement/key — now fronted by a default-deny Authenticator that must-agree-binds a verified
principal to the path identity (ADR-0024), making the chain by-construction end-to-end given a
configured, non-`NoAuth` authenticator**, **circuit-breaker + per-tenant rate limit + cost meter
before any token is spent**, and a **binary-plus-one-service supply chain**. Those are running today. The dormant tiers
do not weaken that core — they are the *scale* story (independent Rust services) layered on top of
a sound in-process engine.

## References

- Deep walk: [runtime as built](runtime-as-built.md) · [phases 3–7 as built](phases-3-7-as-built.md)
- Positioning that depends on this ledger: [ADR-0020 — build vs adopt](../decisions/0020-build-vs-adopt-lang-suite-mastra.md)
- Measurement discipline: [principles](../vision/principles.md) · [roadmap OKRs](../roadmap/okrs.md)
- Wiring: [`aisotropy/app.go`](../../aisotropy/app.go) · thin binary [`cmd/aisotropyd/main.go`](../../cmd/aisotropyd/main.go)
