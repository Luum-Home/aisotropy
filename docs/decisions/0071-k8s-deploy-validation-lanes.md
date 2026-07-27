# ADR-0071: Kubernetes deploy validation — a deterministic offline lane, and an opt-in real-cluster lane

**Status:** Accepted (2026-07-27 — implementation landed with the ADR: `scripts/k8s-chart-guard.sh`,
`scripts/k8s-cluster-lane.sh`, HPA template + `autoscaling` values. Every number quoted below is
real output from the commands named alongside it; the one lane that was NOT executed here is
labelled as such.)
**Delivers:** a regression gate for the Kubernetes deploy surface that runs in **~2 seconds with no
cluster** and asserts what actually matters (probes hit the real endpoints, no secret literal
escapes into a workload, resources and image tags are set, the HPA owns replicas) across **both**
shipped surfaces — the Helm chart and the Kustomize manifests — plus an **opt-in**, four-gate
real-cluster lane that will not start a cluster on a machine that cannot take it. Also closes the
chart's missing HPA.
**Related:** ADR-0016 lever #3 (the reference `docker-compose.yml` this mirrors), ADR-0024 (the JWT
auth posture the negative-render tests pin), ADR-0005 (subprocess boundary discipline), the guard
family (`scripts/prune-guard.sh`, `scripts/arena-determinism-guard.sh`, `scripts/resource-guard.sh`),
the arena stub/real lane precedent (`.claude/rules/arena-full-matrix.md`),
`.claude/rules/verify-before-execute.md`.

## Context

Commit `3d39c44` ("ship mini/local Kubernetes deploy — Kustomize + Helm, verified on Kind") closed
the "zero manifests, zero charts" gap: `deploy/helm/aisotropy/` and `deploy/k8s/` both exist and
both work. Its commit message records a genuine end-to-end verification on a throwaway Kind
cluster — image built (55.7 MB), pod 1/1 Running, `/healthz` and `/health` returning ok.

That verification happened **once, by hand, and left nothing behind that re-runs.** The next edit to
a probe path, a resource block, or a Secret reference has nothing checking it. Meanwhile
`docs/architecture/capabilities-and-gaps.md` still carried the pre-`3d39c44` text ("Zero k8s
manifests, zero Helm charts") — a stale doc claim about shipped code, the exact failure class
`.claude/rules/prune-evidence-gate.md` exists to prevent, pointing the wrong way this time
(understating what exists rather than overstating it).

So the question is not "should we ship k8s manifests" (done) but **"what keeps them correct?"** —
under a hard constraint: this repo is developed on a laptop running several concurrent agent
sessions. On 2026-07-23 that machine hit 81–94% swap and macOS OOM-killed test workers. A validation
lane that needs a Kind cluster and a Go image build cannot be the default lane; it would either
never run or take the machine down.

There is also an asymmetry worth naming: a Kubernetes manifest is **declarative data**. Most of what
can be wrong with it — a probe pointing at a path the server does not serve, a credential inlined
into a Deployment, a missing resource request, a floating `latest` tag, a templated `replicas`
fighting an HPA — is visible **in the rendered YAML**. Booting a cluster proves the image starts; it
does not check any of those any better than reading the render does.

## Options considered

1. **Real cluster (Kind) on every change.** Highest fidelity, and the only thing that proves the
   image boots. Costs a cluster + a multi-GB image build per run; unusable as a default here, and
   would have been throttled or skipped in practice — a gate that does not run is not a gate.
2. **`helm lint` only.** Nearly free, but nearly worthless: `helm lint` checks chart *structure*, not
   whether the readiness probe points at an endpoint the binary actually serves. It passes happily
   on a chart with `/healthy` typoed and a JWT secret inlined into the Deployment env.
3. **Deterministic offline lane as PRIMARY, real cluster as OPT-IN SECONDARY.** Render every posture
   with `helm template` / `kubectl kustomize` (both fully offline) and assert on the resulting
   objects; keep the cluster lane available, off by default, and resource-gated. **Chosen.**
4. **Generate `deploy/k8s/` from `helm template`** so only one surface is hand-maintained. Rejected:
   the Kustomize surface exists precisely so a no-Helm workflow is first-class, and generated
   manifests are worse to read, review, and patch than hand-written bases. Rejected the redundancy
   differently — see the decision below.

## Decision

**The PRIMARY validation lane is deterministic and offline.** `scripts/k8s-chart-guard.sh` renders
the deploy surface and asserts on it. No cluster, no docker build, no network, no image pull. It
follows the repo guard convention (exit `0` ok / `1` findings / `2` error) and the guard family's
central discipline: **a guard that cannot evaluate must never absolve** — every check that cannot
run (helm absent, no YAML parser, no schema validator) prints an explicit SKIP with its reason and
is never counted as a pass.

This is the same shape as ADR-0069's voice lane and the arena stub/real split: **the free lane
proves correctness offline and runs always; the expensive lane is separate and opt-in.**

What it renders (10 postures) and asserts (146 assertions, all passing):

| Assertion | Why it is the one that matters |
|---|---|
| liveness → `/healthz`, readiness → `/health` | Ground truth is `aisotropy/app.go:1127` (`router.Get("/healthz", …)`) and `:1190` (`router.Get("/health", health.New(…))`). A probe pointing elsewhere is a silent outage: k8s keeps routing to a pod that cannot serve, or restart-loops a healthy one. |
| no credential-shaped env var carries an inline `value:` | An inline secret sits in the Deployment object, readable with `get deploy`, captured in `helm get manifest` and CI logs. Must be `secretKeyRef`. |
| a literal secret never appears outside a `kind: Secret` | Tested positively: a sentinel value is passed through `auth.jwtSecret` and must land in the Secret and **nowhere else**. |
| `existingSecret` renders no Secret of its own | The production contract: reference a Secret provisioned out-of-band, never template one. |
| the prod posture templates no Secret and no `CHANGE-ME` placeholder | Placeholder credentials must not survive into the hardened posture. |
| requests + memory limit on every container | Requests make the pod schedulable on a laptop node; limits stop one pod taking the node down; HPA utilisation is undefined without a request. |
| image tag pinned (never `latest`/bare) | Two pods of one ReplicaSet could otherwise run different code. |
| autoscaling: HPA rendered, Deployment omits `replicas`, `scaleTargetRef` matches | A templated `replicas` resets the HPA's chosen scale on every `helm upgrade`. |
| `runAsNonRoot`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem`, `drop: [ALL]` | Hardening must survive **every** posture, not just the default one. |
| the chart **refuses** unsafe combinations | Negative tests: `allowNoAuth=false` with no JWT verifier, and a sub-32-byte HS256 secret, must both fail the render rather than emit an unbootable or unauthenticated deployment (ADR-0024). |

**Both deploy surfaces are first-class and both are covered by the same assertions.** The chart and
`deploy/k8s/` stay hand-maintained (option 4 rejected), so the guard renders the Kustomize base and
both overlays with `kubectl kustomize` — also fully offline — and runs the identical checks over
them. Two hand-maintained surfaces drift; running one assertion set across both is what keeps them
honest about each other. Verified: breaking the readiness path in `deploy/k8s/base` alone turns the
three `kustomize-*` profiles red while all seven Helm profiles stay green.

**The chart gains an HPA** (`templates/hpa.yaml`, `autoscaling.*` in values, `autoscaling/v2`), off
by default, laptop-sane (`minReplicas: 1`, `maxReplicas: 3`, CPU target 80%). It hard-fails the
render if enabled without `resources.requests`, because HPA utilisation is a percentage *of the
request* and would otherwise never scale. The Deployment stops emitting `replicas` when it is on.

**The SECONDARY real-cluster lane is opt-in behind four independent gates**
(`scripts/k8s-cluster-lane.sh`): (1) `AISOTROPY_K8S_CLUSTER_LANE=1`, so wiring it into CI or a
pre-commit chain costs nothing and starts nothing; (2) `scripts/resource-guard.sh` — BLOCKED (exit 3)
is a hard refusal with no override, DEGRADED needs a second explicit opt-in; (3) tool presence, each
absence reported as a skip-with-reason rather than a failure; (4) an explicit `--yes`, without which
it prints the plan and exits having spent nothing. It delegates bring-up to the existing
`deploy/local/up.sh`, smoke-tests the real `/healthz` + `/health` through a port-forward, and tears
the cluster down on exit unless `KEEP_CLUSTER=1`.

**Gate 4 exists because of an incident during this ADR's own implementation.** Gate 2 was probed
twice, minutes apart. Between the probes the machine's memory pressure eased and resource-guard
flipped DEGRADED → OK, so a test written to assert "this should refuse" instead sailed through and
began creating a Kind cluster and a multi-GB Docker build, unattended. resource-guard behaved
exactly as specified; the flaw was architectural. **A resource verdict is a snapshot, and capacity
is not permission** — `verify-before-execute` applied to the tooling itself. The machine now says
whether it *can*; a human says whether it *should*. The cluster was torn down and the incident is
recorded here rather than quietly fixed, because the generalisable lesson is the point.

## Consequences

**Good.** The deploy surface has a real regression gate that costs ~2 s and runs anywhere helm is
installed — cheap enough that there is no incentive to skip it. It catches the failures that
actually ship broken deployments, proven by mutation testing rather than asserted: breaking the
liveness path, floating the image tag to `latest`, dropping the memory limit, and inlining the JWT
secret into the Deployment env each turn the guard red (the secret leak trips two independent
assertions). Both deploy surfaces are covered. The stale capabilities row is corrected.

**Costs and honest boundaries.**
- **Schema validation did not run here.** Neither `kubeconform` nor `kubeval` is installed on this
  machine, so object schemas were **not** checked against the upstream Kubernetes OpenAPI. The guard
  reports this as an explicit SKIP, never a pass; installing `kubeconform` activates it with no
  other change.
- **The real-cluster lane was NOT executed to completion here** — deliberately. It was exercised
  only far enough to verify its gates (off-by-default skip, resource refusal exit 3, usage exit 2,
  and the plan-only dry run), and the one accidental partial run described above was aborted and
  cleaned up. So the claim "the chart installs and serves on a real cluster" rests on commit
  `3d39c44`'s manual Kind verification, not on this lane's own output.
- **The offline lane cannot prove the image boots.** It validates declarative correctness only. A
  binary that panics at startup renders a perfectly valid Deployment. That is what the cluster lane
  is for, and why it is kept rather than deleted.
- **The RAM figures in the cluster lane's header are budgeting estimates** derived from the
  component defaults (Kind node ~0.8–1.5 GB, image build 2–4 GB peak, aisotropyd 256 Mi limit,
  +256 Mi rqlite / +512 Mi Qdrant with networked tiers), not measurements taken by the script.
- **The HPA needs `metrics-server`,** which Kind and k3d do not ship by default (k3s does). Enabled
  without it, the HPA renders and installs but never scales. Also: with `persistence.enabled` the
  pods share one ReadWriteOnce PVC, so that shape must not scale past one replica — documented in
  `values.yaml` next to the toggle.
- **A finding surfaced in passing, out of scope and tracked separately:** the Docker build context is
  **~3.5 GB** (buildkit reported `transferring context: 3.57GB`), because `.dockerignore` excludes
  docs and vendor trees but not the compiled binaries sitting at the repo root (`aisotropyd` 186 MB,
  `bitrate-harness` 186 MB, `token-economy` 27 MB, …). This dominates the cluster lane's cost and is
  why its dry-run plan prints the context size before asking for consent.
