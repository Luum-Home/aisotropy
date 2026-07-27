---
type: research-report
title: "Gap-claims verification — 12 claims from capabilities-and-gaps.md re-verified against code at HEAD"
description: Read-only adversarial re-verification of the 12 gap/partial/delta claims used to dispatch build work, each checked against code, tests, commits and ADRs rather than against a document's own assertion. Follows the prune-evidence-gate discipline after the first dispatched item (config-file, bdaa5ab/ADR-0040) turned out to be a stale doc row.
tags: [research, verification, code-truth, prune-evidence-gate, capabilities, gaps, kubernetes, benchmarks, bitrate, sleep-cycle, voice, as-built]
timestamp: 2026-07-27T00:00:00Z
---

# Gap-claims verification — 2026-07-27

**HEAD at verification:** `d476e87` (2026-07-27 09:51 -0300). Machine guard:
`bash scripts/resource-guard.sh` → exit 0 before any test lane.

**Why this exists.** A gap list compiled from
[`docs/architecture/capabilities-and-gaps.md`](../../architecture/capabilities-and-gaps.md)
(`timestamp: 2026-07-17`) was used to dispatch build work. The FIRST item dispatched
(runtime config file) was **already shipped** — the doc row was stale (`bdaa5ab`, ADR-0040).
That is the repo's named failure class: asserting a fact about code from a **doc read**
instead of the **code** ([`prune-evidence-gate.md`](../../../.claude/rules/prune-evidence-gate.md),
the `internal/routing` incident). Every claim below was therefore re-derived from code,
tests, commits and ADRs. No doc assertion was accepted as evidence for itself.

---

## Verdict table

| # | Claim (as dispatched) | Verdict | Evidence (command / file:line) | One-line truth |
|---|---|---|---|---|
| 1 | **k8s/Helm: "zero manifests, zero charts"** | **STALE** (already done: `3d39c44`, 2026-07-17) | `git log --oneline -- deploy/` → `3d39c44 feat(deploy): ship mini/local Kubernetes deploy — Kustomize + Helm, verified on Kind`; `git ls-tree -r HEAD --name-only \| grep -c '^deploy/\(helm\|k8s\)/'` → **34 tracked files** (17 Helm incl. `Chart.yaml`/`values.yaml`/`values-prod.yaml`, 17 Kustomize base+components+overlays), plus `docs/deployment/kubernetes.md` (233 lines) | Helm chart **and** Kustomize base/overlays shipped 10 days *before* the doc that calls them a gap; the doc row was never updated. |
| 2 | **Cross-framework wall-clock benchmark: "documented method, NOT executed"** | **PARTIALLY-STALE** | `sed -n '563,585p' arenas/live/RESULTS.md` → real p50/p95 latency table, **9 frameworks × native+docker**, run `2026-07-23 14:11 UTC`, LANE: REAL. Also TTFT + Peak RSS (`:588`) and **Docker image size** (`:614`). Producer: `arenas/live/runner/bench.go:44 runBenchmarks`. Still literally true: `arenas/BENCHMARKS.md:306` "Cross-framework method (overhead/latency: documented, NOT executed)" — that one is the **model-held-constant stub** overhead table. | The cross-framework wall-clock comparison **was executed** (real-model lane, 2026-07-23) — what remains unexecuted is the *stub-model overhead isolation* variant. The capabilities doc conflates the two; its §"Open decisions" ask for a "Docker footprint/weight benchmark the arenas lack" is **also stale** — RESULTS.md §Docker footprint has it. |
| 3 | **`internal/bitrate`: "CODE-ONLY, gated on `AISOTROPY_BITRATE_BIN`"** | **PARTIALLY-STALE** | Worker **is in-repo**: `crates/bitrate-scorer/{Cargo.toml,src/main.rs,tests/golden.rs}` tracked, added `d5307d1` (2026-07-10, *before* the doc). Built artifact present locally (untracked): `crates/bitrate-scorer/target/release/bitrate-scorer` (5.9 MB, 2026-07-10). Path **is** exercised: `GOMAXPROCS=2 nice -n 19 go test ./internal/bitrate/` → **ok 2.859s** (`worker_test.go` drives the real subprocess contract via an executable bash stub — success/retry/backoff/ctx-cancel/round-trip); `harness_test.go:145 TestBitrateMeasurementPathNoModel`; route-registration branch in `aisotropy/ops_gate_app_test.go:73-75`; `cmd/bitrate-harness` at 97.8% (`8b35173`). Live lane exists behind `//go:build manual` (`bitrate_manual_test.go:1`). | "CODE-ONLY" understates it: a sovereign Rust scorer crate ships in-repo, the Go↔worker contract is fully unit-tested, and a manual lane validates against the real binary. What is true is that **no automated lane builds or runs the Rust binary** — it stays env-gated. |
| 4 | **Sleep-cycle DataFusion: "DORMANT unless `AISOTROPY_SLEEPCYCLE_BIN`"** | **PARTIALLY-STALE** | Crate tracked since `75ff726` (2026-07-09): `crates/sleep-cycle/{Cargo.toml,Cargo.lock,src/main.rs}`; Rust-side inline tests at `crates/sleep-cycle/src/main.rs:185 #[cfg(test)]` (3 `#[test]`). Go↔Rust contract **is** tested: `go test ./internal/sleepcycle/` → **ok 3.351s** — `worker_test.go:12 writeStub` drives argv+NDJSON+exit-code contract (success, retry-then-fail, transient-recovery, ctx-cancel, `--min-importance` flag present/absent, summary parsing with leading log lines). Live Rust validation: `internal/sleepcycle/worker_manual_test.go:1 //go:build manual`. **Not built locally**: `ls crates/sleep-cycle/target/release/sleep-cycle` → absent. No cargo stage in `.github/workflows/ci.yml` or `scripts/pre-push-gate.sh`. | "Dormant at runtime" is correct; "untested contract" is not — the Go side of the contract is fully covered and the Rust side has unit tests. The real gap is **no automated build/e2e lane for the Rust worker** (binary absent on this machine; nothing in CI compiles it). |
| 5 | **Qdrant/rqlite networked: "code integration real, bare-binary default in-process"** | **STILL-TRUE** (with a caveat) | Env-gated factories: `internal/facts/factory.go`, `internal/semantic/factory.go`; 16 test files reference `QDRANT_HOST`/`RQLITE_URL`. Real-service tests exist but are **all** `//go:build manual`: `internal/facts/rqlite_manual_test.go:1`, `rqlite_reboot_test.go:1`, `internal/semantic/qdrant_manual_test.go:1`, `qdrant_newpaths_manual_test.go:1`. `git grep -l "tags manual" -- scripts/ .github/` → **no runner script or workflow exists**; only docs mention the invocation. `ci.yml:75` runs plain `go test ./... -race`, which excludes `manual`. | The claim holds: the code integration is real and the default is in-process. **There is no test *lane*** — the real-service tests are manual-tagged with no script/workflow to invoke them, so nothing automated ever runs against a live rqlite/Qdrant. |
| 6 | **OCR / `IngestFile`: "text layer only, scanned PDF needs BYO Extractor"** | **STILL-TRUE** | `aisotropy/ingest_file.go:16-18` ("Supply your own via WithExtractor to handle formats the built-in extractor does not (scanned/image PDFs via OCR…)"), `:23-25` (sentinel error to route such files to an OCR Extractor), `:80-82` ("A document that parses but has no text — typically a scanned/image PDF, which needs OCR (out of scope for the default extractor) — fails with an…"). Default is `ingest.DefaultExtractor()` (PDF via `ledongthuc/pdf`, HTML via `x/net/html`). | Verbatim accurate; the bound is explicit in code and in the doc comment, and it is a deliberate BYO seam, not an oversight. |
| 7 | **Hydration is SEQUENTIAL, not goroutine-parallel** | **STILL-TRUE** | `internal/api/chat_stream.go:300 hydrateSystemPrompt` — body runs `persona.BasePrompt` → `wm.Summary` → `wm.Recent` → `store.Search` → `searchSemanticContext` **strictly in order**; no `go func`, no `errgroup`, no `sync.WaitGroup` anywhere in the function (`:300-400`). | Confirmed by reading the function: four dependent I/O steps on one goroutine. The aspirational parallel hydration in `request-lifecycle.md` is still unbuilt. |
| 8 | **Fail-closed reflection loop ONLY on non-streaming `chat`** | **STILL-TRUE** | `git grep -n "chatReflect("` → declared `internal/api/chat.go:314`, called **exactly once**, at `internal/api/core.go:124` (the non-streaming turn path). `grep -n "Reflect" internal/api/chat_stream.go` → **zero hits**; `chat_agent.go` has one unrelated prose hit (`:268`, tool-arg error). Bound: `chatMaxReflectionRetries = 3` (`chat.go:49`). | The structured-output reflection loop runs on `chat`/`App.Turn` only; `chat/stream` and `chat/agent` have no reflection path. |
| 9 | **Routing seam + primitive self-improvement "present but not adopted"** | **HALF STALE** — routing: **STALE**; primitives: **STILL-TRUE** | Routing is **adopted**: `git grep -l internal/routing -- '*.go'` → 10 files, 6 non-test (`aisotropy/app.go`, `aisotropy/extend.go`, `aisotropy/knowledge.go`, `internal/facts/routing_store.go`, `internal/semantic/routing_store.go`), publicly re-exported as `TenantRoutingPolicy`; `runtime-as-built.md:~487` already self-corrects ("The tenant-tier routing seam IS adopted (correcting a former stale claim here)"). Primitives: write-half only — `aisotropy/app.go:542-544` builds `Store`+`FrictionDetector`, `internal/api/chat_agent.go:62` records friction, `app.go:971 runPrimitiveConsolidation` rewrites — but **nothing calls `Store.Get`/`ListForUser`** on any hydration path. | This claim is a **stale copy** of the exact line the routing incident already killed. `.claude/rules/architecture.md` still carries the dead phrasing ("the routing seam + primitive self-improvement are present but not yet adopted") — that file is now the last live source of the false half. Primitive read-back genuinely remains an open loop. |
| 10 | **Graceful shutdown: best-effort window, SSE-vs-`BadgerDB.Close` race** | **STILL-TRUE** (narrower than it sounds) | Sequence at `aisotropy/app.go:1315-1325`: `server.Shutdown(shutdownCtx)` (10 s, `:53 shutdownTimeout`) → `rt.close(shutdownCtx)`. `closeFn` (`:999-1023`) does `stopWorker()` → `waitWorkers(shutdownCtx,&workerWG)` → `closeMCP()` → `wm.Close()` → stores. `waitWorkers` (`:390-399`) is **bounded** and documented as degrading to best-effort. The worker-join race was fixed in `172f125`. `http.Server` does not cancel in-flight request contexts, so an SSE handler still writing past the grace period can reach a closed Badger. | The ordering is correct and the *worker* race is closed; what remains is the documented best-effort window for a pathological SSE handler outliving the 10 s drain. Closing it fully needs request-ctx cancellation wired into shutdown. |
| 11 | **pipecat pinned to 0.0.x** | **STILL-TRUE** | `examples/voice-pipecat/requirements.txt` → `pipecat-ai[silero,whisper,local]>=0.0.60,<0.1` | Verbatim accurate; the `<0.1` ceiling is an explicit pre-1.0 pin. |
| 12 | **Shim never verified against a real provider (all tests use the stub)** | **STILL-TRUE** | `grep -n "llm\." internal/api/openai_compat_test.go` → every router built with `llm.NewStubClient()` (`:143,:248,:273`) or `scriptedAGUIStub` (`:168,:221,:317,:363`); 11 tests, zero real-provider path. The round-trip test (`:326`) points an `OpenAICompatClient` at the **in-repo httptest server**, not a provider. Voice lane likewise: `examples/voice-pipecat/README.md:47-48` ("No ANTHROPIC/OPENROUTER key needed… falls back to the deterministic stub client"), `:112`, `:177`, `:187` — the 2026-07-23 16/16 run was explicitly **stub LLM + Kokoro CPU**. | Confirmed. The shim's wire contract is well-tested against stubs and against the real `aisotropyd` binary; no test or measured run has ever put a real model provider behind it. |

**Tally: 12 claims → 1 fully STALE, 3 PARTIALLY-STALE, 1 HALF-STALE, 7 STILL-TRUE, 0 UNVERIFIABLE.**
Five of twelve (42%) were wrong or materially overstated. Consistent with the config-file precedent:
the doc's 2026-07-17 snapshot lags code that in two cases (claims 1, 3) landed *before* the doc was written.

---

## Reproduce

```sh
bash scripts/resource-guard.sh                                   # 0 = safe to spend
git log --oneline -- deploy/                                     # claim 1
git ls-tree -r HEAD --name-only | grep -c '^deploy/\(helm\|k8s\)/'
sed -n '555,625p' arenas/live/RESULTS.md                         # claim 2
git log --diff-filter=A --format='%h %ad %s' --date=short \
    -- crates/bitrate-scorer/src/main.rs crates/sleep-cycle/src/main.rs   # claims 3,4
GOMAXPROCS=2 nice -n 19 go test ./internal/bitrate/ ./internal/sleepcycle/
git grep -rn "go:build manual" -- '*_test.go'                    # claims 3,4,5
git grep -l "tags manual" -- scripts/ .github/                   # claim 5: empty = no lane
sed -n '300,346p' internal/api/chat_stream.go                    # claim 7
git grep -n "chatReflect("                                       # claim 8
git grep -l internal/routing -- '*.go'                           # claim 9
sed -n '390,399p;993,1023p;1315,1325p' aisotropy/app.go          # claim 10
cat examples/voice-pipecat/requirements.txt                      # claim 11
grep -n "llm\." internal/api/openai_compat_test.go               # claim 12
```

---

## In-flight work (NOT at HEAD — reported separately, untouched)

A concurrent session is building the k8s validation lane. As of this verification the working
tree carries (`git status --porcelain`):

| Path | State |
|---|---|
| `deploy/helm/aisotropy/templates/hpa.yaml` | untracked (new) |
| `deploy/helm/aisotropy/templates/deployment.yaml` | modified |
| `deploy/helm/aisotropy/values.yaml` | modified |
| `scripts/k8s-chart-guard.sh` | untracked (new) |
| `scripts/k8s-cluster-lane.sh` | untracked (new) |
| `docs/decisions/0071-k8s-deploy-validation-lanes.md` | untracked (new ADR) |
| `docs/architecture/capabilities-and-gaps.md`, `docs/decisions/index.md` | modified |

This is exactly the right residual scope for claim 1: the manifests already existed, so the
in-flight work is **validation lanes + HPA**, not the charts themselves. Nothing above was read
for content or modified by this verification.

---

## What this means for the program

Five planned SDD changes were dispatched off this list (config-file already closed by `bdaa5ab`).
Re-scoped against code-truth:

### DROP

- **`arena-benchmark-execution`** — the premise is false. The cross-framework wall-clock
  benchmark **ran on 2026-07-23** across 9 frameworks × {native, docker}, with p50/p95, cold-start,
  TTFT, Peak RSS and Docker image size, all in `arenas/live/RESULTS.md`. The capabilities doc's
  companion ask ("Docker footprint/weight benchmark — the number the arenas lack") is stale for the
  same reason. Residual, if wanted at all, is a **small separate item**: execute the
  *model-held-constant stub* overhead comparison described in `arenas/BENCHMARKS.md:306` — a
  different measurement with a real blocker (other frameworks' runtimes are not installed here),
  which is why it was documented-not-executed in the first place. Do not fund it as "the benchmark gap".

### SHRANK (re-scope before building)

- **`k8s-local-lane`** — the charts are NOT the deliverable; `3d39c44` shipped Helm + Kustomize +
  `docs/deployment/kubernetes.md`. The genuine residual is validation (chart-guard, Kind cluster lane,
  HPA), which is **already in flight** in the working tree with its own ADR-0071. Verify and land the
  concurrent session's work rather than re-dispatching; the SDD change, if kept, should be titled for
  *validation lanes*, not manifests.
- **`dormant-lanes-local`** — the "no code / no worker" framing is wrong for both crates. Both Rust
  workers exist in-repo with tracked source, both Go↔worker subprocess contracts are fully unit-tested
  (stub-executable driven; both packages green: 2.859 s / 3.351 s), and both have `manual`-tagged live
  lanes. The real, much narrower gap is **build + invoke automation**: nothing compiles
  `crates/sleep-cycle` (binary absent locally, no cargo stage in `ci.yml` or `pre-push-gate.sh`), and
  **no script or workflow invokes any `-tags manual` test** — which is also the true content of claim 5.
  Merge claims 3, 4 and 5 into ONE change: *"one operator-run lane that builds the Rust workers and
  drives the manual-tagged rqlite/Qdrant/sleep-cycle/bitrate tests against real services."* That is a
  script + docs, not a build-from-scratch.
- **`as-built-deltas`** — 3 of the 4 deltas survive (sequential hydration, reflection-loop route
  scope, shutdown best-effort window), but the routing half of claim 9 is a **stale copy of the very
  line the `internal/routing` prune incident already killed**. `runtime-as-built.md` self-corrected;
  `.claude/rules/architecture.md` did not, and is now the last live source of the false claim. Fix that
  one line first (a docs edit), then scope the change to the three real deltas. Note claim 10 is a
  *documented deliberate posture*, not a bug — treat it as an explicit decision to revisit, not a fix
  to ship silently.

### STILL JUSTIFIED

- **`voice-followons`** — both claims verified accurate at HEAD. pipecat is pinned `>=0.0.60,<0.1`;
  the shim has never had a real provider behind it (all 11 shim tests use `llm.NewStubClient()` or a
  scripted stub, and the 2026-07-23 voice run was explicitly stub-LLM). This was the only dispatched
  item whose premises held up unmodified — unsurprising, since it was the most recent.

### Standing lesson

The failure rate here (5/12 wrong, two of them describing work that landed *before* the doc's own
timestamp) means **`capabilities-and-gaps.md` must not be used as a dispatch source again**. A gap
list should be generated by executable probes at dispatch time — the same discipline
`scripts/prune-guard.sh` enforces for deletion claims. Recommend adding a
`scripts/gap-claims-guard.sh` that re-derives the volatile rows (manifest counts, benchmark
freshness, crate presence, manual-lane wiring) so the next list is code-derived rather than
doc-copied.
