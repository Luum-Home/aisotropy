---
type: Reality-Audit
title: "Cognitive-features reality audit — what's real vs scaffold (2026-07-17)"
description: A code-truth, prune-evidence-gated audit of the four "cognitive" surfaces (primitives self-improvement, tenant-tier routing, the fail-closed reflection loop, and the compression bets 1–6). Each feature is classified REAL / DORMANT / OPEN-LOOP / DEAD with the actual prune-guard signals (git grep importers, go test -cover, composition-root wiring, façade re-export, deadcode reachability) quoted, plus a per-feature verdict. No deletion is recommended without all five prune-guard signals negative — and none of the audited packages meet that bar.
tags: [architecture, reality, audit, prune-evidence-gate, primitives, routing, reflection, compression, bets, code-truth]
timestamp: 2026-07-17T00:00:00Z
---

# Cognitive-features reality audit (2026-07-17)

> **Discipline.** This is a *code-truth* audit under the
> [prune-evidence-gate](prune-evidence-gate.md): every classification is backed by executable
> evidence (`scripts/prune-guard.sh` output — `git grep` importers, `go test -cover`,
> composition-root wiring, façade re-export, `deadcode` reachability), never a doc read. **No code
> was deleted, demoted-in-code, or modified.** The only writes are docs (this ledger + honest
> reclassifications in [reality-ledger.md](reality-ledger.md)). The gate exists because a prior
> "poda" wrongly recommended deleting live `internal/routing` from a stale doc line — so here every
> "dead / open-loop / adopted" word cites a command, and a prune is *proposed for operator approval*
> only when all five signals are negative.

## Classification vocabulary

| Class | Meaning |
|---|---|
| **REAL** | Adopted and **closed-loop**: wired in the composition root, called on a request/offline path, output is consumed. |
| **DORMANT** | Wired + tested + reachable, but **inert by config** (an env var / allowlist gates activation). Closed-loop *when active*. |
| **OPEN-LOOP** | **Write-half only** — the producer is wired and runs, but nothing reads the output back, so it changes no behavior. |
| **DEAD** | No importer, no coverage, unreachable — **all five prune-guard signals negative**. Only this class is prune-eligible. |

## Headline verdict

The "cognitive" surface is **legitimately built and tested, but only partially closed-loop.** Of the
four audited features: **reflection is REAL** (adopted on `/chat`), **routing is DORMANT-but-closed**
(a constitutional tenant-isolation seam that activates under `AISOTROPY_DEDICATED_TENANTS`),
**primitives is OPEN-LOOP** (the self-improvement write half runs; the read-back that would make it
behavioral memory is unbuilt), and the **compression surface is a live instrument (Bet 6) plus mostly
NEGATIVE research results (Bets 1/2/4, ADR-0021)** — good science in a cul-de-sac, kept as evidence.

**Nothing audited is DEAD. Nothing is prune-safe.** Every Go package tested BLOCKED on all five
prune-guard signals; the Rust crates and `cmd/*` instruments are env-gated or CI/doc-referenced tools,
not orphans. The honest split: ~1 of 4 features fully real, 1 dormant-but-real-when-lit, 1 open-loop
scaffold, 1 research instrument-plus-negative-results. That is **scaffold-heavy but not waste** — the
open loop is one hydration overlay from closing, and the negative results are documented assets.

---

## Feature 1 — Primitives self-improvement (`internal/primitives`)

**What it is.** Friction-driven rewriting of per-`(tenant, user)` tool *guidance metadata*
(description + few-shot examples; executable code is never mutated). A `FrictionDetector` records a
failure signal when an agent-route tool errors; an hourly `runPrimitiveConsolidation` goroutine calls
`primitives.Consolidate` with the `HeuristicRewriter` to sharpen high-friction primitives.

**prune-guard signals** (`scripts/prune-guard.sh internal/primitives` → exit 3, BLOCKED):

```
1) IMPORTERS outside the package: 4
     aisotropy/app.go
     internal/api/chat_agent.go
     internal/api/chat_agent_test.go
     internal/api/coverage_gaps_test.go
2) OWN TEST COVERAGE: 96.6%
3) WIRED IN COMPOSITION ROOT (app.go / cmd/aisotropyd): YES  (aisotropy/app.go)
4) RE-EXPORTED ON THE PUBLIC FAÇADE (aisotropy/): YES  (aisotropy/app.go)
5) REACHABILITY (deadcode from cmd/aisotropyd): unreachable funcs in pkg=0
     => reachable from main (0 of 32 funcs unreachable)
```

**Open-loop evidence** (the write half is wired; the read half is not):

- Write half wired — `aisotropy/app.go:885-887` builds `primStore` + `frictionDetector` and launches
  `runPrimitiveConsolidation`; `internal/api/chat_agent.go:62` calls
  `f.detector.Observe(..., primitives.SignalFailure)` on tool failure; `app.go:1211-1225` runs the
  hourly `primitives.Consolidate(..., HeuristicRewriter{}, primitiveFrictionThreshold)`.
- Read half absent — grep for any read-back into hydration returns **nothing**:

```
$ git grep -nE "\.(Get|ListForUser|Snapshot)\(" -- 'internal/api/*.go' 'aisotropy/*.go' \
    | grep -iE "prim|frictionDetector|primStore"
   (no matches)
$ git grep -nE "ListForUser|\.Snapshot\(\)" -- '*.go' | grep -v internal/primitives/ | grep -v _test.go
   aisotropy/app.go:1078:  ... rt.costMeter.Snapshot()      # cost meter, NOT primitives
```

`Store.Get` / `ListForUser` / `Snapshot` are called **only** inside `internal/primitives`'s own
tests. No prompt-assembly path reads a rewritten primitive, so the improved metadata never reaches
the LLM.

**Status: OPEN-LOOP** (write-half only, effect-inert). **Not DEAD** (4 importers, 96.6% cover,
wired, re-exported, reachable).

**Verdict: CLOSE-LOOP.** Wire the read-back into system-prompt hydration: when assembling the agent
prompt for `(tenant, user)`, call `Store.ListForUser(tenant, user)` (or `Get` per referenced tool)
and inject the rewritten description/examples into the tool-guidance section before the LLM call. The
package already exposes the exact read API (`primitives.go:126 Get`, `:151 ListForUser`,
`:167 Snapshot`) with 96.6% coverage — this is a wiring change in the hydration path, not new
subsystem work. **Effort: ~S** (1 hydration call-site + a store handle threaded to the agent handler,
which already receives the `FrictionDetector`; plus a test asserting a consolidated primitive appears
in the assembled prompt). Until then it stays honestly labeled **experimental** in
[components/primitives.md](../components/primitives.md) (already reclassified 2026-07-17).

---

## Feature 2 — Tenant-tier routing (`internal/routing`)

**What it is.** ADR-0007 Shared/Dedicated tenant tiering: `Router.Resource(base, tenant)` maps a
Dedicated-tier tenant to a physically separate facts/semantic store, a Shared tenant to the pooled
resource. This is a **constitutional isolation seam** (`.claude/rules/constitutional-gates.md`), and
the exact package a prior poda wrongly called "dead."

**prune-guard signals** (`scripts/prune-guard.sh internal/routing` → exit 3, BLOCKED):

```
1) IMPORTERS outside the package: 6
     aisotropy/app.go  aisotropy/builders_test.go  aisotropy/coverage_gaps_test.go
     aisotropy/extend.go  aisotropy/extend_test.go  aisotropy/knowledge.go
2) OWN TEST COVERAGE: 100.0%
3) WIRED IN COMPOSITION ROOT (app.go / cmd/aisotropyd): YES  (aisotropy/app.go)
4) RE-EXPORTED ON THE PUBLIC FAÇADE (aisotropy/): YES  (app.go, extend.go, knowledge.go)
5) REACHABILITY (deadcode from cmd/aisotropyd): unreachable funcs in pkg=0
     => reachable from main (0 of 13 funcs unreachable)
```

**Closed-loop-when-active evidence.** `Router.Resource` is called on the live store path, not just
constructed:

```
$ git grep -nE "\.Resource\(" -- '*.go' | grep -v _test.go | grep -v router.go
   internal/facts/routing_store.go:119     toProvision = append(..., router.Resource(base, tenant))
   internal/facts/routing_store.go:159     return rs.storeForResource(rs.router.Resource(rs.base, tenant))
   internal/semantic/routing_store.go:137  resource := router.Resource(base, tenant)
   internal/semantic/routing_store.go:178  resource := rs.router.Resource(rs.base, tenant)
```

**Config-gated activation.** With `AISOTROPY_DEDICATED_TENANTS` empty (default), `openFactsStore`
returns `factory("facts")` directly and the router is never consulted (`app.go:1259-1283`, and the
comment at `app.go:440-441`). With a non-empty allowlist it returns
`facts.NewRoutingStore(router, "facts", dedicated, factory)` and every request resolves through
`Resource()`. Re-exported publicly as `TenantRoutingPolicy` / `WithTenantRoutingPolicy`
(`extend.go:259, 488`).

**Status: DORMANT** by default, **REAL/closed-loop when a dedicated allowlist is set.** **Not
OPEN-LOOP** (the read path consults the router), **not DEAD** (6 importers, 100% cover, wired,
re-exported, reachable).

**Verdict: KEEP as-is (no action).** This is exactly the isolation seam the prune-evidence-gate was
written to protect; deleting or demoting-in-code would remove a constitutional gate and break the
build. Docs already correct ([runtime-as-built.md §6](runtime-as-built.md) line 478, "the tenant-tier
routing seam IS adopted — correcting a former stale claim"). Line-number refs in
[reality-ledger.md](reality-ledger.md) corrected here to point at the current `app.go` wiring.

---

## Feature 3 — Fail-closed reflection loop (`internal/llm`, `internal/api`)

**What it is.** Structured-output enforcement on the non-streaming `/chat` route: `chatReflect`
(`internal/api/chat.go:268`) runs `llm.ReflectAuto[chatStructuredReply]` (Gollem backend by default,
homegrown `Reflect[T]` fallback under `AISOTROPY_REFLECTION_BACKEND=own`), bounded by
`chatMaxReflectionRetries = 3`, and **fails closed with 502** on a persistent schema violation
(constitutional gate: "structured output must fail closed").

**Reachability / adoption evidence** (reflection is not a standalone package — it is functions in
`internal/llm` invoked from the registered `/chat` handler):

- `internal/api/chat.go:268` `chatReflect` → `:269` `llm.ReflectAuto[...]`, invoked from the
  `huma.Register` handler at `chat.go:337 Register(...)`.
- `internal/llm/reflect_auto.go:62 ReflectAuto` dispatches to `GollemReflect` (`gollem.go:564`) or the
  homegrown `Reflect[T]` (`reflect.go:70`) — both reachable via `ReflectAuto`.
- `internal/bitrate`, `internal/routing`, `internal/primitives` prune-guard runs all showed the
  broader `deadcode ./cmd/aisotropyd/...` set has **0 unreachable funcs** in the audited packages; the
  reflection functions are reached through the registered `/chat` operation.

**Known (documented) delta:** reflection/fail-closed applies to `/chat` **only** — `chat/stream`
forwards raw tokens with no schema enforcement (a token stream cannot be un-sent). This is already
stated in [runtime-as-built.md §6](runtime-as-built.md) ("Reflection/fail-closed applies only to
non-streaming chat") and in the `.claude/rules/architecture.md` "Status vs. target" note.

**Status: REAL** (adopted, closed-loop on `/chat`). **Not OPEN-LOOP, not DORMANT** (runs by default on
every `/chat` turn), **not DEAD**.

**Verdict: KEEP (no action).** Optional future work (out of audit scope): extend a buffered
fail-closed variant to streaming, which ADR-0029 already frames as an opt-in
(`AISOTROPY_GUARDRAILS_BLOCK_OUTPUT`) — a product decision, not a correctness gap.

---

## Feature 4 — Compression bets 1–6

The compression surface spans one live Go instrument, two Rust crates, two `cmd/*` CLIs, and a folder
of Python research spikes. It must be split by artifact — "the bets were negative" is true for the
*hypotheses* (Bets 1/2/4) but **not** for the *instrument* (Bet 6) or the *adopted brick* (Bet 5's
embedder).

### 4a. Bet 6 instrument — `internal/bitrate` (Go)

Bits-per-token North Star: on the stream path, a deterministic ~5% of turns are reserved as held-out
samples (`stream_core.go:228` `bitrate.ShouldSample`, `:238` `BPTSampler.Reserve`) for **offline**
scoring by a pinned reference model; per-tenant lift summaries surface at the ops-gated `GET /bitrate`.

**prune-guard** (`scripts/prune-guard.sh internal/bitrate` → exit 3, BLOCKED):

```
1) IMPORTERS outside the package: 9
     aisotropy/app.go  cmd/bitrate-harness/main.go  internal/api/agui.go  internal/api/aisdk.go
     internal/api/chat_stream.go  internal/api/stream_core.go  (+3 manual-tag probes)
2) OWN TEST COVERAGE: 94.9%
3) WIRED IN COMPOSITION ROOT: YES (aisotropy/app.go)
4) RE-EXPORTED ON THE PUBLIC FAÇADE: YES (aisotropy/app.go)
5) REACHABILITY: unreachable funcs in pkg=0  => reachable from main (0 of 82 funcs unreachable)
```

**Status: REAL** (adopted measurement instrument; the reserve→offline-score→`/bitrate` loop is closed
offline). **Verdict: KEEP.** Component doc [bitrate-bpt-scorer.md](../components/bitrate-bpt-scorer.md)
is honest (explicitly "never runs on the request path").

### 4b. Bet 5 first brick — `crates/embed` (Rust) + `cmd/embed-bench`

ADR-0023/0049: a Rust `candle` crate that loads a pinned MIT `multilingual-e5-small` and speaks the
NDJSON subprocess contract, wired behind `semantic.EmbedderFromEnv` (`embedder_env.go:50`:
`AISOTROPY_EMBED_BIN` → `localEmbedderFromEnv`, daemon default). ADR status:
**"Accepted — implemented and wired."** `cmd/embed-bench` benchmarks it (referenced by
`.github/workflows/ci.yml`, `docs/components/embedder.md`, ADR-0023).

**Status: DORMANT** (wired via env var; falls back to Hash/API embedder when unset — not aspirational,
not open-loop). **Verdict: KEEP.** Reconciled in [reality-ledger.md](reality-ledger.md): carved out of
the former "bets 1–5 ASPIRATIONAL" row, which was stale post-ADR-0023.

### 4c. Bet 6 offline scorer — `crates/bitrate-scorer` (Rust) + `cmd/bitrate-harness`

The reference-model scorer subprocess (ADR-0014/0015) and its longitudinal driver CLI. Env-gated by
`AISOTROPY_BITRATE_BIN` (`cmd/bitrate-harness/main.go:103` skips cleanly when unset). Referenced by
`.github/scripts/coverage-floor.sh`, `docs/guides/cli-tools.md`, `docs/research/evaluation.md`.

**Status: DORMANT** (env-gated research instrument; the Go side `internal/bitrate` is the always-on
half). **Verdict: KEEP.**

### 4d. Bets 1/2/4 hypotheses — `spikes/compression/*.py` (Python)

Surprisal-gated memory (Bet 1), LM arithmetic codec (Bet 2), compressibility routing (Bet 4). Per
**ADR-0021**, these returned **measured NEGATIVE results**: subword bits/token from a small LM
measures syntactic/lexical predictability, not the semantic novelty or reasoning difficulty the bets
needed. The code lives in `spikes/compression/` as Python research spikes — **not** compiled into the
runtime, **not** imported by `main`, **not** in `internal/` or `cmd/`.

**Status: research artifact (negative result), out of the compiled runtime entirely.**
**Verdict: KEEP as science.** ADR-0021 explicitly retains them as the measured evidence behind the
prioritization decision (route the semantic bets through embedding-density / Bet 5, stop iterating
small-LM-surprisal spikes). "Buena ciencia en un callejón" — a documented negative result is an asset,
not dead code. (These files are outside this audit's write scope; noted for completeness.)

---

## PRUNE PROPOSAL

**None.** Every audited package returned `prune-guard` **exit 3 (BLOCKED — LIVE code)**: all five
signals positive (importers > 0, coverage > 0, wired in the composition root, re-exported on the
façade, reachable from `main`). The Rust crates and `cmd/*` CLIs are env-gated subprocess instruments
or CI/doc-referenced tools, not orphans. The Bet 1/2/4 Python spikes are negative-result research
retained by ADR-0021.

Per the prune-evidence-gate, a prune may be *proposed for operator approval* only when **all five
signals are negative** — no audited artifact meets that bar. **No deletion is recommended.**

## Docs reconciled (this session, docs-only)

- [reality-ledger.md](reality-ledger.md): **Primitive self-improvement** row reclassified
  **REAL → OPEN-LOOP (write-half only)** with corrected `app.go` line refs — it had contradicted
  [runtime-as-built.md §6](runtime-as-built.md) and [components/primitives.md](../components/primitives.md),
  both already correct as of 2026-07-17.
- [reality-ledger.md](reality-ledger.md): **Bet 5 sovereign embedder** carved out as its own
  **CODE-ONLY** row (was buried in "bets 1–5 ASPIRATIONAL," stale since ADR-0023 landed wired); the
  remaining **Bets 1–4** row now states the **measured-NEGATIVE (ADR-0021)** result explicitly.
- [reality-ledger.md](reality-ledger.md): **Physical tenant routing** row line-refs corrected to the
  current `app.go`/`routing_store.go` wiring, with the `Resource()`-per-request closed-loop note added.
- No component-doc demotion was needed: [components/primitives.md](../components/primitives.md) already
  reads "REAL write-half / OPEN-LOOP (effect-inert)" (2026-07-17) and
  [components/bitrate-bpt-scorer.md](../components/bitrate-bpt-scorer.md) is already honest about being
  offline-only.
