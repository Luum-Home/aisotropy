---
type: Proposal
title: Consolidation / pruning proposal — a smaller defensible core
description: A read-only, evidence-grounded proposal that maps Aisotropy's whole surface honestly and recommends what to prune, demote, consolidate, or keep so a small defensible core (multi-tenant isolation-by-construction + key-brokering + tiered memory in one Go binary) is not buried by an over-extended periphery. Recommendations only — the operator decides. Every classification cites a ledger row, ADR, or file:line.
tags: [architecture, consolidation, pruning, scope, focus, proposal, reality]
timestamp: 2026-07-17T00:00:00Z
status: proposal — recommendations only, no code changed
---

# Consolidation / pruning proposal — toward a smaller defensible core

> ## ⚠️ POST-AUDIT CORRECTIONS (2026-07-17) — read before acting on any recommendation
> This proposal was independently re-verified against code + tests + `deadcode` (5 surgical audits
> + [`scripts/prune-guard.sh`](../../scripts/prune-guard.sh)). Its LOC/inventory numbers are all
> accurate, but four claims are **corrected** — do NOT act on them as written:
> 1. **M3 `internal/routing` "DELETE-CANDIDATE / nothing imports it" is WRONG.** It is imported by
>    6 files, 100% test-covered, wired in the composition root, publicly re-exported as
>    `TenantRoutingPolicy`, and reachable from `main`. Deleting it breaks the build and removes an
>    isolation-critical seam. **Reclassify CORE-DEFENSIBLE; reject M3.** (Root cause: the claim was
>    quoted verbatim from a then-stale `runtime-as-built.md §6`, now fixed. See
>    [`prune-evidence-gate.md`](prune-evidence-gate.md).)
> 2. **"Bets 1 & 4 refuted" is HALF WRONG.** Bet 4 is refuted; **Bet 1 is NOT** — its novelty
>    hypothesis is validated via embedding-density (AUC 0.89); only the surprisal hard-gate was
>    dropped. Any bets demotion must not label Bet 1 "refuted", and must not conflate Bet 1
>    (memory/novelty) with Bet 4 (difficulty/routing).
> 3. The **README "sells all 5 bets" premise is already resolved** by the positioning surgery
>    (commit `5f3297a`); the compression thesis is now a caveated "go deeper" link.
> 4. **`cmd/skill-import` "tested: partial" → actually 0 tests**; the ADR count is **42 (0000–0041)**,
>    not 39. `internal/primitives` "DORMANT" is more precisely "REAL write-half / open-loop /
>    effect-inert" (the code runs continuously; only the read-back is absent).
>
> Net verdict of the audit: mostly-sound (numbers exact, dormant/peripheral calls correct) with one
> serious inherited error (routing). All safe actions are **docs-only demotes; nothing is deleted.**

> **Operator diagnosis this proposal answers:** *"The core is robust and consolidated, but the
> periphery is over-extended — breadth outran depth; too much surface that claims more than it
> verifies."*

This is a **recommendations-only** document. Nothing is deleted or demoted by writing it; every
move below is a proposal for the operator to accept, reject, or defer. Each classification is
grounded in the [reality ledger](reality-ledger.md), [runtime-as-built](runtime-as-built.md), the
[ADR index](../decisions/index.md), or a `file:line`. Where the evidence is thin, it says so.

## 0. The headline finding (read this if nothing else)

The diagnosis is correct, and it has a sharper form: **the pitch itself forked.** The README now
leads with *"Run untrusted AI agents that never touch your API keys"* (key-brokering /
`RunTask`, [`README.md:1-20`](../../README.md)), while the reality ledger, the arenas, and 24 of 27
`internal/` packages are built around a *different* headline — **tenant isolation by construction +
tiered memory** ([reality-ledger.md:106-115](reality-ledger.md)). Both are real. Both are strong.
But a reader hits **two differentiators, a 43-row competitive matrix, 39 ADRs, 13 example modules,
and 12 competitor arenas** before they can run one thing — and cannot tell which claim is the moat.

Size of the surface, measured:

| Surface | Size | Source of truth |
|---|---|---|
| `internal/` packages | **27 packages, ~24,000 src LOC** | `find internal -name '*.go' ! -name '*_test.go'` |
| `aisotropy/` façade | ~3,525 src LOC | `find aisotropy -maxdepth 1` |
| `cmd/` binaries | **6 mains, ~4,030 src LOC** | `ls cmd/` |
| Rust crates | **3 crates, ~6,000 LOC — all dormant/env-gated** | `crates/{sleep-cycle,bitrate-scorer,embed}` |
| ADRs | **39 (0000–0038)** | [`docs/decisions/index.md`](../decisions/index.md) |
| Example modules | **13** (11 with their own `go.mod`) | `ls examples/` |
| Competitor arenas | **12 frameworks** across 5 languages + a key-brokering arena | [`arenas/README.md`](../../arenas/README.md) |

The core is genuinely consolidated (see §5 — do-not-prune). The periphery is where "claims more than
it verifies" lives: **three dormant Rust crates, an open-loop self-improvement system, an unadopted
routing seam, a plugin/WASM/sealed-driver open-core stack, two frontend protocol adapters, a
scaffold CLI, 12 arenas, and a compression research programme where 5 of 6 bets are unbuilt.**

---

## 1. Surface inventory

Classification legend:

- **CORE-DEFENSIBLE** — delivers the real differentiator, wired in the default binary, tested.
- **DORMANT** — real code, but off by default behind an env var / build tag / provider; verified
  only under a `manual` tag or a stub, not the default suite.
- **ASPIRATIONAL** — documented / spiked, not built into a working default path.
- **PERIPHERAL** — real and often tested, but widens the surface and dilutes the value prop.

Tested column: **yes** = default `go test ./...`; **partial** = tested via stub/fake or only under
`//go:build manual`; **no** = not exercised by the suite (docs/spike/sketch).

### 1a. The core runtime

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| API & contracts — Huma v2 + chi, struct-as-contract, OpenAPI 3.1, SSE + ctx-cancel (`internal/api`) | **CORE-DEFENSIBLE** | [ledger row "API & contracts"](reality-ledger.md); `internal/api` 3,780 src / 7,235 test LOC | yes (36 tests) |
| Tenancy — `Scope`, ctx propagation, tenant-scoped statement builder (`internal/tenancy`, ADR-0011) | **CORE-DEFENSIBLE** (the moat) | [ledger:106-115](reality-ledger.md); ADR-0011; `internal/tenancy` 706/900 LOC | yes |
| Working memory — BadgerDB + transactional outbox + cold-boot recovery (`internal/memory`) | **CORE-DEFENSIBLE** | [ledger row "Working memory"](reality-ledger.md); 1,309/2,613 LOC | yes (28 tests) |
| Facts memory in-process — SQLite+FTS5, tenant-scoped (`internal/facts`) | **CORE-DEFENSIBLE** | ledger row "Facts memory"; 1,547/2,857 LOC | yes (25 tests) |
| Consolidation outbox worker (`internal/consolidation`) | **CORE-DEFENSIBLE** | [runtime-as-built §3](runtime-as-built.md); 124/260 LOC | yes |
| Reflection loop — Gollem default + homegrown fallback, fail-closed 502 on `/chat` (`internal/llm`) | **CORE-DEFENSIBLE** | ledger row "Reflection loop"; ADR-0008/0010; `internal/llm` 2,906/2,894 LOC | yes |
| Granular control — per-tenant rate limit, circuit breaker, cost meter, scope-gated tools (`internal/memory`, `internal/costmeter`, `internal/tools`) | **CORE-DEFENSIBLE** | ledger row "Granular control"; [runtime-as-built §5](runtime-as-built.md) | yes |
| Authenticated-identity seam — default-deny middleware, must-agree bind, HS256 (`internal/auth`, ADR-0024/0025) | **CORE-DEFENSIBLE** | ledger row "Authenticated-identity seam"; `internal/auth` 843/980 LOC | yes (14 tests) |
| Public façade + builder + `App.Turn` (`aisotropy/`, ADR-0013/0031) | **CORE-DEFENSIBLE** | ledger row "Public extension surface"; ADR-0031 | yes (15 façade tests) |
| Semantic memory in-process cosine (`internal/semantic`) | **CORE-DEFENSIBLE** (in-proc) | ledger row "Semantic memory"; 2,639/4,261 LOC | yes (in-proc path) |

### 1b. Key-brokering / sandbox (the second, newer headline)

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| Key-brokered task agent — `RunTask` (`internal/taskagent`, `internal/sandbox`, ADR-0036) | **CORE-DEFENSIBLE** *(candidate — see §2)* | README lead; ADR-0036; `taskagent` 1,369/1,930 + `sandbox` 1,184/1,259 LOC; broker invariant proven vs real Docker | yes (Docker-gated proof + unit) |
| Concurrent fan-out — `RunTasks` (ADR-0037) | **DORMANT→CORE candidate** | ADR-0037; `TestRunTasks_ConcurrentDocker_KeyFree_NoHeadCorruption` | partial (needs Docker) |
| Pluggable providers — Docker + Podman built-ins, `Spec.Runtime` knob (ADR-0038) | **PERIPHERAL** | ADR-0038 honest deltas: `Spec.Runtime` not a public field; k8s/Firecracker are doc sketches | partial |

### 1c. Dormant tiers (real code, off by default)

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| Qdrant networked semantic (`QDRANT_HOST`) | **DORMANT** | ledger row "Semantic memory" (CODE-ONLY networked) | partial (`manual`) |
| rqlite networked facts (`RQLITE_URL`) | **DORMANT** | ledger row "Facts memory" (CODE-ONLY networked) | partial (`manual`) |
| DataFusion sleep-cycle (`crates/sleep-cycle`, 4,779 Rust LOC, `AISOTROPY_SLEEPCYCLE_BIN`) | **DORMANT** | ledger row "Sleep cycle"; default suite uses a **bash stub** | partial (stub only) |
| Native provider tool-calling (`ToolCaller`, only OpenRouter, ADR-0012) | **DORMANT** | ledger row "Native tool-calling"; [ledger gap #3](reality-ledger.md): Anthropic/stub silently fall back | partial (fake) |
| Bet-6 bitrate path (`internal/bitrate` 961 LOC, `crates/bitrate-scorer` 687 LOC, `cmd/bitrate-harness`, `AISOTROPY_BITRATE_BIN`) | **DORMANT** | [runtime-as-built env table](runtime-as-built.md); ADR-0014/0015 | partial |
| Sovereign local embedder (`crates/embed` 547 LOC, ADR-0023 **proposed**) | **DORMANT / ASPIRATIONAL** | ADR-0023 still 🟡 proposed; `cmd/embed-bench` exists to decide if it's worth it | partial |
| Physical tenant routing (`internal/routing`, ADR-0007) | **DORMANT (unadopted)** | [runtime-as-built §6](runtime-as-built.md): *"nothing outside the package imports it"* | yes (unit) |
| Primitive self-improvement (`internal/primitives`, open loop) | **DORMANT (half-wired)** | [runtime-as-built §6](runtime-as-built.md): write-side wired, *"nothing reads the rewritten primitives back"* | yes (unit) |
| Response cache (`internal/responsecache`, LIVE only on `/ask`) | **DORMANT (narrow)** | [runtime-as-built §5](runtime-as-built.md); off the stateful chat path by decision | yes |

### 1d. Open-core extension stack

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| Plugin ecosystem — manifest + loader + registry (`internal/plugin`, ADR-0032) | **PERIPHERAL** | ADR-0032: `aisotropy plugins` CLI is a "named follow-on"; `plugin` 527/453 LOC | yes (unit) |
| WASM plugin sandbox — wazero (`internal/wasmtool`, ADR-0035) | **PERIPHERAL** | ADR-0035; only capability today is `aisotropy.log`; 677/326 LOC | yes (unit) |
| Tier-2 sealed drivers — `VectorBackend`/`DocBackend` + `plugintest` conformance (ADR-0033/0034) | **PERIPHERAL** | ADR-0034 honest delta: driver payload types *"not yet re-exported on the public façade"*; `plugintest` 933/525 LOC | yes (conformance) |

### 1e. Adjacent tooling & DX

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| Frontend protocol adapters — Vercel AI SDK (`internal/api/aisdk.go`, 18 KB) + AG-UI (`internal/api/agui.go`, 19 KB) | **PERIPHERAL** | `aisotropy/adapters_e2e_test.go:3-6`; adoption-learnings roadmap item | yes (e2e) |
| Dev UI — `WithDevUI` embedded HTML (`aisotropy/devui.go`/`devui.html`) | **PERIPHERAL** | `aisotropy/devui.go:10-18` | yes |
| Scaffold CLI — `aisotropy new` (`cmd/aisotropy/new.go`) | **PERIPHERAL** | `cmd/aisotropy/new.go:3` | yes |
| Operator CLI — `aisotropy plugins` / `aisotropy task` (`cmd/aisotropy`) | **PERIPHERAL** | `cmd/aisotropy/main.go`, `task.go` | yes |
| Dev console TUI — `cmd/x` (Bubbletea build/fmt/vet/lint/test/bench) | **PERIPHERAL** | `cmd/x/main.go:1-4` | yes |
| Skill-import — Agent-Skills importer (`cmd/skill-import`, ADR-0022) | **PERIPHERAL** | `cmd/skill-import/main.go`; ADR-0022 importer is "scoped future work" | partial |
| Eval harness — answer/retrieval quality (`internal/eval`, ADR-0026) | **PERIPHERAL** | ADR-0026: *"foundation only (no gold dataset / CI gate / dashboard yet)"*; 875/528 LOC | yes (unit) |
| Guardrails (`internal/guardrails`) + model gateway (`internal/gateway`) | **PERIPHERAL→CORE-adjacent** | ADR-0027/0028; wired always-on-detect, block opt-in; `guardrails` 828/611, `gateway` 438/402 LOC | yes |
| Knowledge ingestion routes (`internal/api` knowledge.go, ADR-0018/0019) | **PERIPHERAL→CORE-adjacent** | [runtime-as-built §1](runtime-as-built.md) route table | yes |

### 1f. Research, benchmarking & competitive artifacts

| Surface | Class | Evidence | Tested? |
|---|---|---|---|
| 12 competitor arenas (`arenas/`, 5 languages) | **PERIPHERAL** | [`arenas/README.md`](../../arenas/README.md); 43-row matrix in `ARENA-SPEC-v2.md` | n/a (each self-contained) |
| Token-economy bench (`bench/token-economy`) | **PERIPHERAL** | [ledger gap #2](reality-ledger.md): −72% figure is a synthetic-baseline artifact | n/a |
| Compression bets 1–5 (`spikes/compression`, `docs/research/`) | **ASPIRATIONAL** | [ledger row "Compression bets 1–5"](reality-ledger.md): *"only Bet 6 is spike-validated offline"* | no (spikes) |
| L2 composition SDK — graphs/workflows/teams (ADR-0016 lever #4) | **ASPIRATIONAL** | ADR-0016: deferred until "2–3 real consumers"; README: *"broader L2 SDK is pending"* | no |
| k8s / Firecracker sandbox providers (ADR-0038) | **ASPIRATIONAL** | ADR-0038: *"doc sketches, not shipped implementations"* | no |

---

## 2. The recommended defensible core

The minimal set that delivers the real differentiator — **multi-tenant isolation-by-construction +
key-brokering + tiered memory in a single Go binary** — and nothing that doesn't.

**Keep as the sealed, always-on core (§1a in full):**

1. `internal/api` (struct-as-contract, SSE, ctx-cancel) — the syscall layer.
2. `internal/tenancy` (`Scope`, ctx propagation) — **the moat; this is the one thing no competitor
   builds** ([arenas bottom line](../../arenas/README.md)).
3. `internal/memory` (BadgerDB working memory + transactional outbox).
4. `internal/facts` (SQLite+FTS5, in-process default).
5. `internal/semantic` (in-process cosine default).
6. `internal/consolidation` (outbox worker).
7. `internal/llm` (provider registry + reflection loop, fail-closed).
8. `internal/costmeter` + `internal/tools` + rate-limit/breaker (granular control).
9. `internal/auth` (authenticated-identity seam).
10. `aisotropy/` façade (`App`, `Turn`, `Run`, builder options).

**Core depends on:** Go stdlib, Huma, chi, BadgerDB, `modernc.org/sqlite` (FTS5), Gollem,
`golang.org/x/sync`. Every one is MIT/Apache-2.0 (constitutional gate holds). **Nothing in the
default core depends on Docker, Rust, Python, Qdrant, or rqlite.**

**The one genuine strategic decision (flagged, not resolved):** key-brokering (`internal/taskagent`
+ `internal/sandbox`, §1b) is now the **README's lead differentiator** but requires **Docker at
runtime** and is a large, separable surface (~2,550 src LOC). It is either (a) a **co-equal second
pillar** of the core, or (b) a **flagship optional module**. It cannot be both "the headline" and
"an optional add-on that needs Docker" without confusing the reader. **Recommendation: pick ONE
headline** (see §4). If isolation-by-construction stays the moat, key-brokering is the flagship
demo *of* that moat (untrusted agents, isolated). If key-brokering becomes the headline, the memory
tiers demote to "and it also has tiered memory." **I am uncertain which the operator wants** — this
is the single most important decision this proposal surfaces, and it drives everything in §4.

---

## 3. Prune / demote / consolidate recommendations

Each is tagged and carries a one-line rationale + a risk note. Ordered roughly by impact.

### Top-priority moves

**M1 — 12 arenas → keep 3 flagship, archive the rest.** Tag: **MOVE-TO-SEPARATE-REPO** (or
`arenas/archive/`).
- Rationale: 12 competitor integrations across 5 languages (Python venvs, Node, JVM, Workers) are
  *impressive* and bury the value prop — a newcomer meets a 43-row matrix before running anything.
  Keep the 3 that carry the argument: **one Go-native** (`eino` or `adk-go`, the honest same-language
  comparison), **Letta** (the "closest neighbor / validates the memory-OS thesis" concession), and
  **Cloudflare Agents** (the strongest rival isolation story). Move the other 9 to a companion repo
  (`aisotropy-arenas`) linked from the README.
- Risk: the arenas are strong evidence for the isolation claim; a reviewer doing due diligence may
  want the full set. Mitigation: keep them git-tracked in a companion repo, not deleted; the reality
  ledger and 3 flagship arenas still substantiate the claim.

**M2 — Compression bets 1–5 → demote to a clearly-fenced research annex.** Tag:
**DEMOTE-TO-EXPERIMENTAL**.
- Rationale: [the ledger already says](reality-ledger.md) only Bet 6 is spike-validated; Bets 1/4
  are **refuted** as difficulty proxies (ADR-0021). "Compression as a cognitive layer" is a
  prominent README section ([`README.md:216-223`](../../README.md)) selling 5 unbuilt bets as a
  differentiator. Keep the manifesto and the *measured* Bet-6 result; move the rest under
  `docs/research/experimental/` with an explicit "not in the runtime" banner.
- Risk: the compression thesis is intellectually the project's "north star." Demoting it in the
  README may feel like abandoning vision. Mitigation: keep it as vision, remove it from the
  *differentiator* framing until a second bet ships.

**M3 — `internal/routing` (physical tenant tiering) → demote or delete-candidate.** Tag:
**DELETE-CANDIDATE** (or DEMOTE-TO-EXPERIMENTAL).
- Rationale: [runtime-as-built §6](runtime-as-built.md) states *"nothing outside the package imports
  it."* It is a 215-LOC designed seam that has never been adopted in ~7 phases. Dead-on-arrival code
  that the ledger has to keep explaining is exactly "claims more than it verifies."
- Risk: ADR-0007 tiering is a real future scaling need. Mitigation: keep the ADR (the *decision* is
  still valid); remove the unadopted package or fence it under a build tag until a tenant needs it.

**M4 — `internal/primitives` (self-improvement) → demote until the loop closes.** Tag:
**DEMOTE-TO-EXPERIMENTAL**.
- Rationale: [runtime-as-built §6](runtime-as-built.md): the write half is wired but *"nothing reads
  the rewritten primitives back … the self-improvement loop is closed on the write half only."* A
  half-open loop that never affects a prompt is a claim ("primitive self-improvement") with no
  observable behavior. The ledger even marks it REAL, which overstates it.
- Risk: closing the read-back loop is a small, high-value piece of the memory-OS thesis. Mitigation:
  either finish the read-back (promote to real) or fence it — don't leave it advertised-but-inert.

**M5 — Open-core plugin/WASM/sealed-driver stack → consolidate + demote to one experimental
surface.** Tag: **MERGE-WITH** (fold `internal/plugin` + `internal/wasmtool` +
`internal/plugintest` into one "extensibility (experimental)" area) + **DEMOTE-TO-EXPERIMENTAL**.
- Rationale: ADR-0032/0033/0034/0035 shipped a plugin manifest, a WASM sandbox (one capability:
  `aisotropy.log`), and Tier-2 sealed drivers — but ADR-0034 admits the driver payload types are
  *"not yet re-exported on the public façade"* and ADR-0032's `aisotropy plugins` CLI is a "named
  follow-on." This is a whole open-core programme (4 ADRs, ~2,100 LOC) built ahead of a single
  external plugin author. It is depth-first engineering on a surface with zero demonstrated demand.
- Risk: open-core extensibility may be a real go-to-market lever later. Mitigation: keep the code,
  label it experimental, stop presenting it as a shipped capability until one real plugin exists.

### Secondary moves

**M6 — Rust crates (`sleep-cycle`, `bitrate-scorer`, `embed`) → keep code, demote the story.** Tag:
**KEEP** (code) + **DEMOTE-TO-EXPERIMENTAL** (the "sovereign Rust services" pitch).
- Rationale: all three are env-gated and verified only by stubs ([ledger](reality-ledger.md)). The
  "sovereign Rust-services moat" is, per the ledger, *"one integration-and-verification step behind
  the code."* Don't delete — they're the real scale story — but stop implying they're active.
- Risk: none to the code; this is a docs-honesty move. `crates/embed` (ADR-0023 still 🟡 proposed)
  is the weakest — consider parking it behind `cmd/embed-bench`'s verdict.

**M7 — Frontend adapters (aisdk + agui) + DevUI → keep one, demote the other.** Tag:
**DEMOTE-TO-EXPERIMENTAL** (pick one protocol).
- Rationale: two full frontend protocol adapters (37 KB combined) plus an embedded DevUI is DX
  breadth for a runtime whose audience (per the README) is *"backend, infra, and platform
  engineers"* who *"already think in single binaries."* Ship the one adapter real adopters asked for
  (adoption-learnings cites the Vercel AI SDK path); demote AG-UI to experimental.
- Risk: low. Both are additive routes; demoting AG-UI removes no core capability.

**M8 — Scaffold CLI + skill-import + eval harness → keep, but move out of the front-page pitch.**
Tag: **KEEP** (all real, tested) but do not surface in the top-level differentiator narrative.
- Rationale: `aisotropy new` (scaffold), `cmd/skill-import` (ADR-0022), and `internal/eval`
  (ADR-0026 "foundation only") are legitimate DX/quality tooling. They are not the moat and should
  not compete for attention on the entry path.
- Risk: none.

**M9 — `internal/responsecache` → keep, it's correctly scoped.** Tag: **KEEP**.
- Rationale: deliberately narrow (only on stateless `/ask`), and the *reasoning* is documented and
  sound (staleness/isolation footgun on the hydrated path, ADR-0028/0030). This is the model of a
  well-fenced feature — not over-extension.

---

## 4. The 5-minute onramp

**Problem:** the current entry path forces a reader through two differentiators, a 43-row matrix, 39
ADRs, and a compression manifesto before they run anything. The README's first *runnable* payoff is
good, but it competes with four other "differentiator" sections
([`README.md`](../../README.md) has sections titled "The differentiator: an agent that never holds
your key", "Why aisotropy — and why Go", "The differentiator: compression as a cognitive layer" —
**two things both called "the differentiator"**).

**Concrete restructure (README, top-to-bottom):**

1. **One sentence, one differentiator, one runnable proof — above the fold.** Pick the single
   headline (§2's open decision). If it's key-brokering: keep the current opening + the `RunTask`
   snippet, and cut the compression section entirely from the README. If it's
   isolation-by-construction: lead with the `Turn` + two-tenant snippet and the one-line claim *"a
   different TenantID cannot reach this one's data — and the unscoped read is unrepresentable."*
   Either way: **exactly one "the differentiator" heading.**
2. **A 60-second copy-paste block that proves the claim**, not just runs a chat. For isolation:
   a `go run .` that does two `Turn`s under different `TenantID`s and shows tenant B cannot read
   tenant A. For key-brokering: the existing `docker exec printenv` broker-invariant demo, reduced
   to 10 lines. The proof *is* the pitch.
3. **Then, and only then, one link each** to: the reality ledger ("what's real vs dormant"), the
   tutorial ladder, and the arenas — as *"go deeper"* links, not inline walls.
4. **Move the stack table, the "LLM OS" model, the compression thesis, and the why-Go essay** below
   the fold or into `docs/` — they are context, not onramp.
5. **Add a `docs/getting-started.md`-first pointer at the very top of the README** — literally
   `New here? Start at [Getting Started](docs/getting-started.md)` (that link text is written for the
   **repo-root README**, where `docs/getting-started.md` resolves; it is example content, not a link
   from this doc). That file already does the 30-second + 2-minute job well
   ([`getting-started.md:20-46`](../getting-started.md)) — the README should *defer* to it rather
   than duplicate-and-exceed it.

**Net effect:** a newcomer reaches a working, differentiator-proving `go run .` in under 5 minutes
and meets the 43-row matrix only if they choose to. The matrix stays — it just stops being the
gate.

---

## 5. Explicit non-recommendations (do NOT prune the moat)

Things that *look* prunable on a breadth pass but are the genuine defensible core. Cutting these to
"look decisive" would delete the differentiator.

- **`internal/tenancy` + the `Scope` value object.** This is the moat. The entire arenas programme
  exists to show that [no competitor builds it](../../arenas/README.md). **Never demote.**
- **The transactional outbox (`internal/memory` + `internal/consolidation`).** Looks like plumbing;
  is actually the crash-consistency property the arenas found is `NOT-POSSIBLE` in most rivals
  (arena row 7.1). Keep and keep advertising.
- **The authenticated-identity seam (`internal/auth`, ADR-0024).** Recent and large, but it is what
  makes the isolation claim true *end-to-end over HTTP* rather than only in-process. Without it the
  moat is "isolation given a trusted caller." Keep.
- **Key-brokering (`internal/taskagent` + `internal/sandbox`).** Even if it becomes a "flagship
  module" rather than co-core (§2), do **not** delete it — it is the one capability the README can
  honestly call unique-in-Go, proven against real Docker. Demote its *provider sprawl* (M-adjacent:
  Podman/Kata/gVisor/k8s), not the capability.
- **The in-process defaults for facts/semantic.** Tempting to call "just the dev fallback," but the
  zero-config single-binary boot **is** the sovereignty pitch. The Qdrant/rqlite tiers are the
  *scale* story on top; the in-process defaults are the *product*. Keep both, label which is which.
- **The reality ledger itself.** It is the honesty instrument that makes this whole proposal
  possible. Keep it as the arbiter and, if anything, *promote* it in the onramp (§4.3).

---

## 6. Uncertainties & where I may be wrong

- **§2's core-vs-flagship decision for key-brokering is genuinely open.** I flagged it rather than
  resolved it because it's a positioning call the operator owns. My read of the README is that the
  pitch *already* shifted toward key-brokering; if that's intentional, several §3 moves reorder.
- **Arena value is subjective.** I recommend keeping 3 of 12; the operator may judge the full set a
  load-bearing sales asset. The move is reversible (companion repo, not deletion).
- **I did not run the test suite** — tested/partial classifications come from the ledger, LOC ratios,
  ADR "honest deltas," and file inspection, not a fresh `go test ./...`. The recent commit history
  ("93.1% promoted-set") suggests the *core* is well-tested; the periphery's partial/gated status is
  the ledger's own claim.
- **`internal/eval`, guardrails, and gateway** sit on the CORE/PERIPHERAL boundary. I classed them
  peripheral because ADR-0026/0027 describe them as foundations, but guardrails+gateway are wired on
  the live chat path and could reasonably be called core-adjacent. Low-confidence classification.

## References

- [Reality ledger](reality-ledger.md) — the REAL / DORMANT / ASPIRATIONAL arbiter.
- [Runtime as built](runtime-as-built.md) — the code-grounded "what actually boots" (see §6 for the
  unadopted-routing and open-loop-primitives admissions this proposal leans on).
- [ADR index](../decisions/index.md) — 39 ADRs, with the "honest deltas" quoted above.
- [Arenas README](../../arenas/README.md) — the 12-arena competitive matrix (M1).
- [README](../../README.md) — the current entry path (§0, §4).
- [Getting Started](../getting-started.md) — the existing 5-minute front door (§4.5).
