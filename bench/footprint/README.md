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
| **ADK-Go** | Go | distroless static | **Same-language control.** Packaged identically to aisotropy, so any gap vs. aisotropy is a *framework* difference, not a *language* one. |
| **LangGraph** | Python | `python:3.12-slim` (idiomatic) **+** alpine+prune (optimized) | Heavy-Python representative — the interpreter + a large dependency tree must ship. Two variants. |
| **Mastra** | TypeScript | `node:22-slim` (idiomatic) **+** alpine+esbuild (optimized) | TS/Node representative — `node_modules` + the Node runtime ship. Two variants. |

ADK-Go is the important inclusion: without a second Go subject, a size gap could
be dismissed as "Go vs. Python/Node." With it, the Go-vs-Go row isolates how much
is language and how much is the specific framework.

**Two variants per interpreted competitor (fairness).** Each of LangGraph and
Mastra is measured twice: the **idiomatic-minimal** image (smallest *standard*
`-slim` way to ship it) and an **optimized-floor** image squeezed as hard as
still-boots allows — so the idiomatic multiplier is not misread as a ceiling:

- **LangGraph optimized** (`langgraph/Dockerfile.optimized`): `python:3.12-alpine`,
  `pip --no-compile`, prune `__pycache__`/tests. ~2× smaller image; but musl +
  no bytecode cache visibly *regresses* cold-start — an honest trade, reported.
- **Mastra optimized** (`mastra/Dockerfile.optimized`): esbuild bundle +
  tree-shake to ONE file on `node:22-alpine`, shipping **no `node_modules`**.

Both optimized images are verified to boot + serve `/healthz` before their
numbers count.

### Each framework at ITS OWN official packaging (researched, cited)

So the multipliers can't be dismissed as "you built them a bad image," we looked
up each framework's *own* recommended Docker packaging:

- **ADK-Go** — the official `adkgo deploy cloudrun` tool compiles
  `CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w"` and generates
  `FROM gcr.io/distroless/static-debian11` + `COPY` the binary
  ([`cmd/adkgo/internal/deploy/cloudrun/cloudrun.go`](https://github.com/google/adk-go/blob/main/cmd/adkgo/internal/deploy/cloudrun/cloudrun.go)).
  **Our control row IS this pattern** — static binary → distroless/static (we use
  debian12:nonroot vs its debian11). ADK-Go's official packaging is measured.
- **LangGraph** — `langgraph build` / `langgraph dockerfile` (langgraph-cli)
  produce a full **Agent Server**: the compose stack pulls `redis:6` +
  `pgvector/pgvector:pg16` + `langchain/langgraph-api`
  ([docs](https://docs.langchain.com/langsmith/cli),
  [`langgraph_cli/docker.py`](https://github.com/langchain-ai/langgraph/blob/main/libs/cli/langgraph_cli/docker.py)).
  That is a heavier, multi-service deployment needing a DB — **not run here** as a
  footprint subject (it is a whole server, like Letta). Our minimal LangGraph
  images are a *floor below* LangGraph's own recommended packaging.
- **Mastra** — official deploy is `mastra build` → self-contained `.mastra/output/`
  (a Hono server), run via `node`; no official prebuilt image, `node:22-alpine`
  in the Lambda example
  ([server deploy](https://mastra.ai/docs/deployment/mastra-server),
  [Lambda guide](https://mastra.ai/guides/deployment/aws-lambda)). Our optimized
  variant **mirrors that recipe** (bundle to a self-contained output on alpine,
  no `node_modules`); the official output is a *fuller* server, i.e. heavier than
  our minimal, not lighter.

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
- **ADK-Go** (`adk-go/`) — real `google.golang.org/adk/v2` `llmagent` + `runner`
  + in-memory `session`, an offline `model.LLM` fake, one warmup turn, then a
  `net/http` `/healthz`. Compiled static → distroless, matching aisotropy (see
  `adk-go/Dockerfile`, which mirrors the repo-root `Dockerfile` recipe exactly).
- **LangGraph** (`langgraph/`) — `pip install langgraph` (pinned to the arena's
  1.2.9), a one-node `StateGraph` compiled + one warmup `invoke`, then a stdlib
  `/healthz`. Node "model" is a deterministic echo (no provider). Built idiomatic
  (`Dockerfile`) and optimized (`Dockerfile.optimized`).
- **Mastra** (`mastra/`) — prod-only `npm install @mastra/core@1.51.0`, one
  `Agent` with the same v2 offline fake model the Mastra arena uses, one warmup
  `generate`, then a `node:http` `/healthz`. Built idiomatic (`Dockerfile`) and
  optimized (`Dockerfile.optimized`).

"Ready" is defined identically for every subject and variant: **HTTP 200 on
`/healthz`** — an image that does not answer 200 is not counted.

## Fairness caveats (read these before quoting a number)

1. **Distroless (Go) vs. slim (Python/Node) is a real asymmetry, and it is the
   point.** A Go static binary needs no runtime in the image; Python and Node
   must ship their interpreter. That is not a trick — it is a *language
   packaging property* an operator genuinely pays for. We report the smallest
   *standard* image for each (idiomatic) **and** an aggressively-optimized floor
   (alpine/esbuild) so both ends of the range are visible — the optimized floors
   narrow the multiplier but do not close it (RSS is structural; LangGraph-opt
   even regresses cold-start under musl). The idiomatic multiplier is **not** the
   floor, and it is **not** the ceiling either.
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
6. **"Optimized" images are ours; official packaging is cited, not faked.** The
   optimized variants are our best-effort floor for each ecosystem. Where a
   framework publishes official Docker guidance we researched and cited it (see
   the *official packaging* subsection above): ADK-Go's official `adkgo` tooling
   ships exactly our control's static-binary→distroless pattern, so that row is
   the framework's own recommended packaging; LangGraph's and Mastra's official
   deployments are *fuller servers* than our minimal subjects — so our numbers
   are a floor below their official images, not a penalty against them. No
   "official" number is invented — it is measured, or labelled unmeasured.

## Reproduce

```sh
bash bench/footprint/run.sh            # build all 6 (2 Go + 2 LangGraph + 2 Mastra), measure, print, clean up
RUNS=9 bash bench/footprint/run.sh     # more cold-start samples (default 5)
KEEP=1 bash bench/footprint/run.sh     # keep the images after (default: delete)
```

Requires `docker`, `curl`, `python3` (used for a millisecond clock — macOS
`date` has no `%N`). Every build pulls from the public registry / PyPI / npm, so
network is required for the first run. The raw table is also written to
`bench/footprint/results.txt`. The run cleans up its own images and containers
unless `KEEP=1`.

Every number in the `## Class 3 — Framework footprint (run-verified)` section of
`arenas/BENCHMARKS.md` is copied from this harness's actual output — not
estimated.
