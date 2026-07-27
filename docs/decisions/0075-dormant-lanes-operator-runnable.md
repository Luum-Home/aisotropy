# ADR-0075: Dormant lanes — one operator-runnable command that builds the Rust workers and runs the real-service tests

**Status:** Accepted (2026-07-27 — implementation landed with the ADR: `scripts/dormant-lanes.sh`,
`deploy/dormant-lane/docker-compose.yml`, `docs/deployment/dormant-lanes.md`. Every number quoted
below is real output from the commands named alongside it; the one test that did NOT run is
labelled as such.)

**Delivers:** the missing *invocation* for two paths whose code, tests and workers already existed —
a status-first, resource-guarded lane that (a) `cargo build --release`s `crates/sleep-cycle` and
`crates/bitrate-scorer`, points `AISOTROPY_SLEEPCYCLE_BIN` / `AISOTROPY_BITRATE_BIN` at the results,
and runs the `-tags manual` live tests against the **real** binaries, and (b) stands up an
**isolated** rqlite + Qdrant stack, runs the four `-tags manual` real-service tests, and tears it
back down leaving the machine exactly as found.

**Related:** [ADR-0005](0005-datafusion-go-rust-boundary.md) (the Go↔Rust subprocess boundary this
exercises), [ADR-0014](0014-bits-per-token-reference-scorer-boundary.md) /
[ADR-0015](0015-bpt-eval-model-and-python-batch-runtime.md) (the bitrate scorer and its model
provisioning caveat), [ADR-0016](0016-dual-surface-sealed-runtime-composition-sdk.md) lever #3 (the reference compose
stack), [ADR-0071](0071-k8s-deploy-validation-lanes.md) (**capacity is not permission** — the gate
design this reuses), [ADR-0074](0074-gap-claims-guard.md) (the claims registry whose
`no-lane-invokes-manual-tests` claim this closes),
[`machine-resource-guard.md`](../architecture/machine-resource-guard.md),
[`.claude/rules/prune-evidence-gate.md`](../../.claude/rules/prune-evidence-gate.md),
[`.claude/rules/verify-before-execute.md`](../../.claude/rules/verify-before-execute.md).

## Context

The framing this work was dispatched under — "the dormant lanes have no worker and no tests" — was
**wrong**, and the read-only verification in
[`gap-claims-verification-2026-07-27.md`](../06-Daily/reports/gap-claims-verification-2026-07-27.md)
(claims 3, 4, 5) established why before a line was written:

- `crates/sleep-cycle` has been tracked since `75ff726` (2026-07-09) and `crates/bitrate-scorer`
  since `d5307d1` (2026-07-10). Both have Rust-side unit tests.
- Both Go↔worker subprocess contracts are **fully unit-tested** with executable stubs — argv, NDJSON,
  exit codes, retry/backoff, ctx-cancel — and both packages were green at verification time
  (`internal/sleepcycle` 3.351 s, `internal/bitrate` 2.859 s).
- Fourteen `//go:build manual` test files exist; four of them target live rqlite/Qdrant.

The single true finding was a one-line grep:

```sh
git grep -l "tags manual" -- scripts/ .github/     # → empty
```

**No script and no workflow has ever built a Rust worker or invoked a single manual-tagged test.**
`ci.yml` runs plain `go test ./... -race`, which excludes the `manual` tag by construction. The code
was live, the tests were written, and the button did not exist. That — not "build the workers", not
"write the tests" — is the gap this ADR closes. Writing either would have been the
`internal/routing` failure again: acting on a document's assertion instead of the code's.

Two hard constraints shape the answer:

1. **This repo is developed on a laptop running several concurrent agent sessions.** On 2026-07-23
   it hit 93% swap and macOS OOM-killed a test worker. A DataFusion release build plus two
   containers is exactly the workload that does that. A lane that cannot refuse is a lane that takes
   the machine down.
2. **The machine already has a reference stack running.** At implementation time, a concurrent
   session owned `docker compose -p aisotropy` on ports 4001/6333/6334. A lane that assumes those
   ports, or that runs `docker compose down` against the reference project, destroys another
   session's work.

## Options considered

| # | Option | Verdict |
|---|---|---|
| 1 | **Wire cargo + the manual tests into `.github/workflows/ci.yml`** | **Rejected (for now).** A 3-minute cold DataFusion build and two service containers on every push is a real recurring cost, and CI is not where the *operator* needs this. It is also a separate decision with a separate budget; conflating it here would have smuggled a CI-cost commitment into a local-tooling change. The registered claim `rust-workers-not-built-in-ci` therefore stays **true and green** — deliberately, and said out loud rather than quietly closed. |
| 2 | **Document the commands in a README and stop** | **Rejected.** The commands were *already* documented — in the manual tests' own doc comments and in `docs/components/datafusion-sleep-cycle.md`. Documentation is exactly what existed while the grep returned empty. A doc is not a lane. |
| 3 | **One script per dormant path (four scripts)** | **Rejected.** The paths share every hard part — the resource gate, the consent gate, the `-tags manual` invocation, the teardown discipline. Four copies means four places for the teardown bug. |
| 4 | **One script, three independently selectable stages, status-first** | **CHOSEN.** `status` (default) reports cost without paying it; `workers` and `services` are opt-in and independently runnable; `all` chains them. Shared gates, one teardown. |
| 5 | **Reuse the reference `docker-compose.yml` for the `services` stage** | **Rejected on evidence** — see below. It cannot serve host-side manual tests, for reasons that are structural rather than situational. |
| 6 | **Lane-owned compose file, own project, own ports, ephemeral storage** | **CHOSEN.** |

### Why option 5 failed (found by trying it, not by assuming)

The reference stack was probed first, exactly as the dependency-reuse-first discipline requires. It
is correct for what it is for — running `aisotropyd` *next to* its tiers — and unusable for a
host-side test client:

1. **rqlite advertises a container-internal address.** The reference compose runs rqlite without
   `-http-adv-addr`, so the node advertises its container hostname. Measured live:
   ```
   curl -s -u aisotropy:… http://localhost:4001/status
   → "cluster":{"api_addr":"424f8f2f3d74:4001"}
   ```
   `gorqlite` does cluster discovery and **follows** the advertised HTTP address, which the host
   cannot resolve. `internal/facts/rqlite_manual_test.go`'s own doc comment records this caveat; the
   reference file does not satisfy it.
2. **A recreated container permanently loses its Raft leader.** rqlite derives its node ID from the
   container hostname and the reference stack keeps a named volume, so a recreate leaves the
   persisted Raft configuration naming a node that no longer exists. Measured live on the running
   reference stack:
   ```
   docker logs aisotropy-rqlite-1 → [raft] not part of stable configuration, aborting election
   curl http://localhost:4001/readyz → 503  ([+]node ok / [+]leader does not exist)
   docker inspect … .State.Health → "unhealthy", FailingStreak 124
   ```
   This is a real defect in the reference deployment's durability story, found by this work and
   recorded here; fixing it (pin `-node-id`, add `-http-adv-addr`) is a separate change to a file a
   concurrent session was actively using, deliberately **not** made under this ADR.
3. **Port ownership.** 4001/6333/6334 were occupied by another session at implementation time.

## Decision

Ship **`scripts/dormant-lanes.sh`** — one command, three stages — plus a lane-owned
`deploy/dormant-lane/docker-compose.yml`.

### 1. Status is the default; consent is separate from capacity

Bare `scripts/dormant-lanes.sh` inspects and reports: cargo/go/docker presence, daemon
responsiveness, which worker binaries are built, which images are pulled, whether the lane ports are
free, whether a foreign qdrant/rqlite stack is running (reported, never touched), the
`resource-guard` verdict, and each stage's declared RAM budget and measured cost. It builds nothing,
starts nothing, pulls nothing. Exit `0` when everything is present, `1` when something is missing.

Doing anything requires naming a stage; *provisioning* (cargo build, `docker compose up`)
additionally requires an explicit `--yes`. Without it the stage prints its plan and exits 0 having
spent nothing. This is ADR-0071's gate 4, and it exists for the same reason: a resource verdict is a
snapshot that says the machine **can**, and only an operator says it **should**.

### 2. Every heavy step declares a MEASURED RAM budget

No bare `cargo`, `docker` or heavy `go test` anywhere in the script — each goes through
`scripts/with-heavy-lane.sh --need-mb N`, which takes the cross-session mutex and lets
`resource-guard` refuse. This lane is that flag's **first real consumer**.

The numbers are measured, not guessed. First full execution (2026-07-27, 24 GB M-series host, both
crates cold, `CARGO_BUILD_JOBS=4`), sampling the summed RSS of all cargo+rustc processes every 2 s:

| Step | `--need-mb` | Basis |
|---|---|---|
| `cargo build` sleep-cycle | 2560 | **measured stage peak 1924 MB** + ~33% headroom; 3m08s cold, ~330 crates, 72 MB binary |
| `cargo build` bitrate-scorer | 2560 | same figure — the sample is **stage-level** and cannot honestly be attributed to one crate; 41s cold, 6 MB binary |
| Go contract tests | 1024 | small packages, generous build-cache headroom |
| Containers (qdrant + rqlite) | 1024 | Qdrant idles ~300–400 MB RSS, rqlite ~60–100 MB, plus per-container engine overhead |
| Go manual tests | 512 | thin clients; both live suites finished in <2 s |

Peak build RAM scales with concurrent codegen units, so the lane pins `CARGO_BUILD_JOBS=4` and says
in the source that raising the cap invalidates the budgets. An **under**-declared budget is the
dangerous direction — it buys a green light the machine cannot honour — so the headroom is
deliberate and the derivation is written next to the constants.

### 3. The machine is left exactly as it was found

The `services` stage runs under its own compose project (`aisotropy-dormant-lane`), its own host
ports (14001/16333/16334, overridable), and **no named volumes at all**. It therefore cannot adopt,
disturb or tear down a stack it did not create. Ports are re-checked **at execution time, not at
plan time** (verify-before-execute), and an occupied lane port is a refusal, not a fallback.

Teardown runs from an `EXIT`/`INT`/`TERM` trap **and** explicitly at the end of the stage. The
explicit call exists because a trap firing after the verdict is printed can no longer influence the
exit code — an incomplete teardown must be a reported **finding**, since "leave the machine as
found" is an acceptance criterion, not a courtesy.

## Consequences

### Verified (first full execution, 2026-07-27)

```
--stage workers --yes
  cargo sleep-cycle    3m08s cold  → crates/sleep-cycle/target/release/sleep-cycle (72 MB)
  cargo bitrate-scorer   40.74s    → crates/bitrate-scorer/target/release/bitrate-scorer (6 MB)
  go test ./internal/sleepcycle/ ./internal/bitrate/       ok 3.339s / ok 2.663s
  go test -tags manual -run TestWorkerLive ./internal/sleepcycle/
      TestWorkerLive                          PASS  dedup 3 → 2 (dropped "a"), parquet written
      TestWorkerLiveNoisePrune                PASS  dedup+noise 4 → 2
      TestWorkerLiveNoiseFloorDisabledIsDedupOnly PASS
  peak cargo+rustc RSS 1924 MB; ~1.6 GB of target/ across both crates

--stage services --yes
  rqlite ready after 3s; qdrant ready after 0s
  go test -tags manual ./internal/facts/      TestRqliteLive PASS, TestRqliteRebootIdempotent PASS
  go test -tags manual ./internal/semantic/   TestQdrantLive PASS, TestQdrantLiveNewPaths PASS
  teardown → 0 containers / 0 volumes / 0 networks in project 'aisotropy-dormant-lane';
             the concurrent session's 'aisotropy' project still running(2), untouched
```

`bash -n` clean; `shellcheck -S style` clean; `docker compose config` valid.

### What this does NOT prove (stated, not implied)

- **`TestScorerLive` did not run.** The Rust bitrate scorer needs `AISOTROPY_BITRATE_MODEL_DIR`
  pointing at converted GPT-2 weights (`model.safetensors` + `tokenizer.json`, the one-time offline
  conversion in ADR-0015 §"Sovereign-Rust provisioning caveat"). Those weights are not on this
  machine. The lane reports an explicit **SKIP with the reason and the remedy**, never a pass — and
  the binary *is* built and the Go contract *is* covered, so what is missing is precisely the
  weights-dependent run and nothing more.
- **This is not CI.** `rust-workers-not-built-in-ci` stays true and green in the ADR-0074 registry.
- **It proves the tier contracts against real services, not the runtime under load.** End-to-end
  `aisotropyd`-plus-tiers behaviour remains the reference compose stack's job.
- **The RAM figure is stage-level.** Attributing peak RSS to one crate would need a per-build sample
  nobody took; the budgets say so instead of inventing a per-crate number.

### Costs accepted

- A **second compose file** that partially duplicates the reference stack's service definitions and
  can drift from it. Accepted because the divergence is the *point* (adv-addr, node-id, ephemeral
  storage, lane ports) and the image tags are pinned identically, with the three reasons written at
  the top of the file so a future reader does not "simplify" it back into a bug.
- A **~1.6 GB `target/` footprint** after the workers stage (gitignored). Reported by `status` so
  the operator sees it before paying it.
- **A registered claim goes red on commit.** ADR-0074's `no-lane-invokes-manual-tests` expects zero
  lanes; this lane makes it one. That is the designed behaviour of that guard — closing a registered
  gap must force the document to be updated — so this change also flips the claim's `expect` and
  rewrites its text, per the `on_close` instruction its author left.

### Follow-ups (deliberately not done here)

1. **Fix the reference `docker-compose.yml`'s rqlite durability defect** (pin `-node-id`, add
   `-http-adv-addr`) — a real bug, found here, in a file a concurrent session was actively using.
   Wants its own change and its own verification.
2. **Decide whether cargo belongs in CI**, with a cost budget attached. Until then the honest state
   is registered as a claim, not as a silence.
3. **Provision the bitrate model weights** (or ship the conversion as a lane sub-step) so
   `TestScorerLive` stops skipping.
