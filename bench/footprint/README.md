# Framework footprint benchmark

A **real, executed, reproducible** cross-framework comparison of *deployment
footprint* — container image size, cold-start, idle memory, and dependency
weight — with the **model held constant as an offline stub in every subject**.

## Why footprint (and not a wall-clock quality table)

`arenas/BENCHMARKS.md` deliberately refuses to publish a cross-language
wall-clock/latency table, because benchmarking framework A on model X against
framework B on model Y measures the *models*, not the frameworks — and holding
the model constant across five language runtimes fairly is exactly the thing
that is hard to do honestly offline.

**Footprint is the honest, fair, executable cross-framework number.** Image
size, RSS, and dependency weight are language-agnostic in a way wall-clock is
not: a 400 MB image is 400 MB whether it is fast or slow, and it is a *real
property every operator pays for* (registry storage, pull time, cold-start,
per-pod RAM, attack surface). So this harness measures footprint for real and
leaves per-token latency to the (correctly caveated) method note in
`BENCHMARKS.md`.

The one timing number here — **cold-start** — is included because it *is* a real
deployment property (time from `docker run` to serving), dominated by runtime
boot (interpreter import vs. static-binary exec), not by model behaviour. It is
still heavily caveated.

## Flagship set (why these 4, not all 12 arenas)

Per `docs/architecture/consolidation-proposal.md` (M1: "12 arenas → keep 3
flagship"), this benchmark does **not** run all 12. It picks a **cross-language
representative set** so the footprint axis is covered without noise:

| Subject | Language | Packaging | Role in the comparison |
|---|---|---|---|
| **aisotropy** | Go | distroless static | The subject. Single static binary, built from the repo's own `Dockerfile`. |
| **LangGraph** | Python | `python:3.12-slim` | Heavy-Python representative — the interpreter + a large dependency tree must ship. |
| **Mastra** | TypeScript | `node:22-slim` | TS/Node representative — `node_modules` + the Node runtime ship. |
| **ADK-Go** | Go | distroless static | **Same-language control.** Packaged identically to aisotropy, so any gap vs. aisotropy is a *framework* difference, not a *language* one. |

ADK-Go is the important inclusion: without a second Go subject, a size gap could
be dismissed as "Go vs. Python/Node." With it, the Go-vs-Go row isolates how much
is language and how much is the specific framework.

Letta (the "closest neighbour" arena) was **not** picked as a footprint subject:
`letta-client` is a client for a *separate Letta server*, so a "minimal Letta
deployment" is a whole server product, not a one-import hello-agent — an unfair,
non-minimal image. It stays a feature/isolation arena, not a footprint one.

## What each image contains (the "fair minimal app")

Every subject is the **smallest idiomatic "stand up the framework + one agent"**
app — not a stripped strawman, not a bloated one:

- **aisotropy** — the repo's production image, unmodified: multi-stage build of
  `./cmd/aisotropyd`, `CGO_ENABLED=0 -ldflags="-s -w"`, final stage
  `gcr.io/distroless/static-debian12:nonroot`. Boots the full runtime (memory
  tiers, quota gate, router) and serves the native `/healthz`. Keyless stub LLM.
- **LangGraph** (`langgraph/`) — `pip install langgraph` (pinned to the arena's
  1.2.9), a one-node `StateGraph` compiled + one warmup `invoke`, then a stdlib
  `/healthz`. Node "model" is a deterministic echo (no provider).
- **Mastra** (`mastra/`) — prod-only `npm install @mastra/core@1.51.0`, one
  `Agent` with the same v2 offline fake model the Mastra arena uses, one warmup
  `generate`, then a `node:http` `/healthz`.
- **ADK-Go** (`adk-go/`) — real `google.golang.org/adk/v2` `llmagent` + `runner`
  + in-memory `session`, an offline `model.LLM` fake, one warmup turn, then a
  `net/http` `/healthz`. Compiled static → distroless, matching aisotropy.

"Ready" is defined identically for all four: **HTTP 200 on `/healthz`**.

## Fairness caveats (read these before quoting a number)

1. **Distroless (Go) vs. slim (Python/Node) is a real asymmetry, and it is the
   point.** A Go static binary needs no runtime in the image; Python and Node
   must ship their interpreter. That is not a trick — it is a *language
   packaging property* an operator genuinely pays for. We do **not** try to
   "equalise" it by, say, cramming Python into distroless (non-idiomatic) — we
   report the smallest *standard* image for each and name the asymmetry.
2. **aisotropy's image does more work than the others' at boot** (full
   memory-tier + router init vs. a single node/agent). If anything this biases
   cold-start *against* aisotropy, not for it.
3. **Cold-start is machine- and cache-sensitive.** Median of `RUNS` runs (min/max
   shown). Absolute ms varies by host; the ordering (static binary << interpreted
   runtime) is the portable finding.
4. **Dependency weight is measured differently per ecosystem** (unavoidable):
   Python/Node report on-disk `site-packages`/`node_modules` size + package count
   (measured *inside* the image); Go reports `go.sum` module count and notes the
   binary is statically linked so no dependency files ship. These are not the
   same unit — do not add them across rows.
5. **The stub is not a model.** Nothing here measures answer quality or token
   throughput. This is footprint only.

## Reproduce

```sh
bash bench/footprint/run.sh            # build all 4, measure, print table, clean up
RUNS=9 bash bench/footprint/run.sh     # more cold-start samples (default 5)
KEEP=1 bash bench/footprint/run.sh     # keep the images after (default: delete)
```

Requires `docker`, `curl`, `python3` (used for a millisecond clock — macOS
`date` has no `%N`). Every build pulls from the public registry / PyPI / npm, so
network is required for the first run. The raw table is also written to
`bench/footprint/results.txt`. The run cleans up its own images and containers
unless `KEEP=1`.

Every number in the `## Framework footprint (run-verified)` section of
`arenas/BENCHMARKS.md` is copied from this harness's actual output — not
estimated.
