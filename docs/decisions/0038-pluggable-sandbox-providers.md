---
type: ADR
title: "ADR-0038 — Pluggable sandbox providers: the SandboxProvider seam, a shipped Podman built-in, and the Spec.Runtime microVM knob, with Kubernetes/Firecracker as documented extension points and cloud sandbox services deliberately excluded"
description: >
  ADR-0036/0037 shipped the sandboxed task agent (RunTask) and its concurrent fan-out (RunTasks) with ONE
  Docker provider. This ADR makes the sandbox BACKEND a first-class pluggable seam and reconciles it to the
  shipped code. Three moves: (1) EXPORT the provider interface on the public façade —
  aisotropy.SandboxProvider (alias for internal/sandbox.Provider) plus Sandbox / SandboxSpec / the Exec I/O
  aliases — so a third party can bring their own backend by implementing one interface, selected per task via
  TaskRequest.Provider; (2) SHIP a second self-hostable built-in, PodmanSandbox() (rootless, daemonless,
  Apache-2.0), alongside DockerSandbox() (which already works transparently with any Docker-API-compatible
  engine, notably OrbStack) — both shell out to a stock CLI via os/exec, no SDK, no CGO, no new dependency;
  (3) keep the existing Spec.Runtime knob as the isolation-STRENGTH lever — a hardened OCI runtime name
  (Kata "kata-runtime" = a VM per container, gVisor "runsc" = a user-space kernel, both Apache-2.0) emitted
  as --runtime=<name>, so microVM-grade isolation is a config value, not a new provider. Kubernetes (a Pod
  per task in a per-tenant namespace via kubectl exec/cp, delete on Close) and Firecracker/Cloud Hypervisor
  microVMs are documented BRING-YOUR-OWN extension points (a reference sketch, not shipped code). Cloud
  sandbox services (Vercel/Daytona-cloud) are DELIBERATELY excluded from the core because a vendor-hosted
  control-plane dependency would break the single-binary, self-hostable, no-lock-in posture. Honest scope:
  Spec.Runtime is wired in internal/sandbox and is not yet a field on the public TaskRequest; the k8s /
  Firecracker providers are sketches in docs, not shipped implementations. STATUS: accepted — reconciled
  against the shipped aisotropy/sandbox.go + internal/sandbox (docker.go, podman.go, sandbox.go).
tags: [decisions, adr, sandbox, task-agent, providers, isolation, docker, podman, kubernetes, microvm, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# ADR-0038 — Pluggable sandbox providers: the `SandboxProvider` seam, a shipped Podman built-in, and the `Spec.Runtime` microVM knob

## Status

✅ **Accepted — shipped 2026-07-16** (`aisotropy/sandbox.go` exports the seam; `internal/sandbox/podman.go`
ships the Podman provider; `internal/sandbox/sandbox.go`'s `Spec.Runtime` carries the microVM knob — all in
the tree with tests). It is the **backend-pluggability** layer over
[ADR-0036](0036-sandboxed-task-agent-key-brokering.md) and
[ADR-0037](0037-concurrent-sandboxed-task-agents.md): where 0036 established the sandboxed task agent (one
`RunTask`, **key-brokered** — the LLM call and the key stay host-side, only tool EXECUTION runs in a
container the agent never fully trusts) and 0037 ran N of them in parallel, both shipped with **one** Docker
provider. This ADR turns "the container backend" into a **seam a third party can replace**, adds a second
self-hostable built-in (Podman), and names the isolation-strength knob that was already present in `Spec`.

> **The broker invariant is a provider-wide contract, not a Docker detail.** Every `SandboxProvider` —
> built-in or bring-your-own — receives a `SandboxSpec` of **non-secret configuration only** and MUST NOT
> read a provider API key. `TestRunTask_RejectsBrokerSecretInEnv` (ADR-0036) already proves the task runner
> refuses to place a secret in `Spec.Env`; making the interface public does not widen that surface — a
> custom provider inherits the same rule, restated on every exported type.

This ADR **extends** 0036/0037 without touching their kernel: `RunTask` / `RunTasks` are unchanged; they
already routed through the `Provider` interface, so exporting that interface and adding a second
implementation is additive. It holds the constitutional **license gate**: **no new dependency** — Podman is
driven by shelling out to the stock `podman` CLI (`os/exec`), exactly as the Docker provider shells to
`docker`; Kata and gVisor are host binaries the operator installs, not module dependencies; all are
permissive (Apache-2.0). It holds the **multi-tenancy gate**: a provider receives no tenant and no key; the
Kubernetes sketch derives its per-tenant namespace from the identity already in `ctx`.

## Context

[ADR-0036](0036-sandboxed-task-agent-key-brokering.md) deliberately shipped **one** provider — a
`DockerProvider` that shells out to the `docker` CLI — and listed additional backends as named follow-ons.
The `Provider` interface already existed in `internal/sandbox` precisely "so the task runner depends on the
capability, not on Docker specifically." But `internal/` is not importable by another module, so a third
party could **select** Docker but could not **bring their own** backend, and the runtime shipped no
alternative for operators who cannot or will not run a root Docker daemon.

Three real pressures make this the right time to open the seam:

1. **Self-hosting without a root daemon.** Docker's daemon runs as root; many self-hosting and CI
   environments prefer **rootless, daemonless** containers. Podman is the obvious permissively-licensed
   answer, and it is CLI-compatible enough that the provider is a near-clone of the Docker one.
2. **Isolation strength is a spectrum, not a binary.** Some tasks are fine on a shared-kernel container;
   some want a **VM per task**. That knob (`Spec.Runtime`) already existed but was undocumented as a
   first-class decision — and it means microVM-grade isolation does **not** require a new provider.
3. **The long tail is unbounded.** Kubernetes, Firecracker, Cloud Hypervisor, and vendor cloud sandboxes
   all exist. The core cannot (and should not) ship all of them; it should ship a **seam** plus the
   self-hostable built-ins, and document how to bring the rest — while being honest about which the project
   will **not** adopt.

## Goals

- **Export the provider seam on the public façade** so a third party can implement a custom backend against
  the genuine internal contract (a type alias, not a shadow copy) and select it per task via
  `TaskRequest.Provider`.
- **Ship a second self-hostable built-in (Podman)** — rootless, daemonless, Apache-2.0 — alongside Docker
  (which already covers OrbStack transparently), **with no new dependency** (CLI shell-out).
- **Name the isolation-strength knob (`Spec.Runtime`)** as a first-class decision: a hardened OCI runtime
  (Kata / gVisor) upgrades a sandbox to VM- or sandboxed-kernel isolation **without a new provider**,
  because those runtimes slot in under the existing Docker/Podman CLI.
- **Document bring-your-own** with a concrete, faithful **Kubernetes** reference sketch (Pod per task in a
  per-tenant namespace; `kubectl exec` / `kubectl cp`; delete on `Close`) and note that Firecracker / Cloud
  Hypervisor are the **same pattern** over a VMM.
- **Exclude cloud sandbox services from the core, explicitly.** Vercel Sandbox / Daytona-cloud are named and
  ruled **out** of the built-in set — not silently omitted — with the reason stated (they would break the
  self-hostable, no-lock-in posture).
- **Preserve the broker invariant across all providers.** The key never enters any sandbox, custom ones
  included; the rule is restated on every exported type.

## Non-goals (v1 scope, held deliberately small)

- **NOT a change to `RunTask` / `RunTasks` trust model or scope.** The kernel is unchanged; this ADR only
  exports an interface that already existed and adds a second implementation of it. Everything 0036/0037
  held out of scope stays out of scope.
- **NOT a new public `TaskRequest.Runtime` field.** `Spec.Runtime` is wired in `internal/sandbox` and
  emitted by the built-in providers, but the public `TaskRequest` does not (yet) expose it directly. Setting
  it per task today is a custom-provider concern (wrap a built-in and set `Spec.Runtime` in `Create`); a
  public request field is a named follow-on. This deviation is documented, not hidden.
- **NOT shipped Kubernetes / Firecracker providers.** Those are **reference sketches** in
  `docs/architecture/sandbox-providers.md` showing the interface-to-`kubectl` (or interface-to-VMM) mapping,
  not compiled, tested code in the tree. Shipping a real k8s provider (with its own conformance tests) is a
  follow-on.
- **NOT a cloud-sandbox provider, in the core, ever-by-default.** The seam is open so a consumer can write
  one against a vendor API; the project will not adopt one — a vendor-hosted control-plane dependency is
  incompatible with the single-binary, self-hostable posture (see Consequences).
- **NOT a relaxation of the key-broker or tenant isolation invariants.** A provider gets non-secret config
  and nothing else; tenant identity travels via `ctx`, never through a provider field.

## Options considered

### How does a third party get a custom sandbox backend?

- **Promote `internal/sandbox` wholesale.** Rejected for the same reason ADR-0013 rejected promoting
  `internal/` generally: it freezes a churning package and leaks more surface than a consumer needs.
- **Export exactly the provider contract as façade aliases (CHOSEN).** `aisotropy/sandbox.go` re-exports
  `SandboxProvider = sandbox.Provider`, `Sandbox`, `SandboxSpec`, and the `SandboxExecResult` /
  `SandboxExecOptions` I/O types as **type aliases** — the consumer implements the genuine internal
  interface, and the runtime stores the genuine value. The minimal set to implement a backend is exported;
  nothing isolation-critical (no `Scope`, no key handling) is.

### Which second built-in provider?

- **A cloud provider (Vercel/Daytona).** Rejected for the core — see the exclusion decision below.
- **Podman (CHOSEN).** Rootless, daemonless, Apache-2.0, and **CLI-compatible** with Docker enough that
  `PodmanProvider` mirrors `DockerProvider` (shell out to `podman` instead of `docker`), so it is the
  highest-value, lowest-risk second built-in and adds **no dependency**. It is also the most
  self-hosting-aligned backend (no privileged daemon).

### How is stronger-than-container isolation offered?

- **A new "microVM provider" per technology.** Rejected — Kata and gVisor are **OCI runtimes**, not
  separate container backends; a whole new provider would duplicate the Docker/Podman plumbing to change one
  `--runtime` flag.
- **A `Spec.Runtime` knob shelled through to the existing CLI (CHOSEN).** Naming a hardened runtime
  (`"kata-runtime"`, `"runsc"`) in `Spec.Runtime` emits `--runtime=<name>` on `docker run` / `podman run`.
  microVM- or sandboxed-kernel isolation becomes a **config value, not a code path**; an unknown/uninstalled
  runtime surfaces as a normal `Create` error, not a panic. The operator owns installing and registering the
  runtime; the module takes no dependency on it.

### Do we ship a cloud sandbox provider in the core?

- **Yes, adopt Vercel/Daytona as a built-in.** Rejected. A built-in that only works against a vendor's
  hosted control plane makes running the model's hands depend on that vendor — the exact **lock-in** the
  project's positioning ([ADR-0020](0020-build-vs-adopt-lang-suite-mastra.md)) exists to avoid, and it
  cannot run in a fully self-hosted / air-gapped deployment.
- **No — leave it to bring-your-own; document the exclusion (CHOSEN).** The seam is open, so a consumer who
  must use a cloud sandbox implements `SandboxProvider` against the vendor API themselves. The core ships
  only self-hostable answers (Docker/OrbStack, Podman, the runtime knob) and states the exclusion plainly so
  it reads as a **decision**, not an oversight.

## Decision (v1 / minimal)

### 1. `aisotropy/sandbox.go` — the exported seam

The public façade re-exports the provider contract as type aliases over `internal/sandbox`:
`SandboxProvider` (= `sandbox.Provider`), `Sandbox`, `SandboxSpec` (whose `Runtime` field is the isolation
knob), and `SandboxExecResult` / `SandboxExecOptions`. `TaskRequest.Provider SandboxProvider` selects the
backend per task; nil defaults to `DockerSandbox()`. The broker rule ("`Spec.Env` is non-secret only; a
provider must not read a key") is documented on each exported type.

### 2. Two built-in constructors

- **`DockerSandbox() SandboxProvider`** — `sandbox.NewDockerProvider()`. Shells out to the `docker` CLI, so
  it works transparently with any Docker-API-compatible engine, notably **OrbStack**, with no code change.
  The default provider. `Name()` → `"docker"`.
- **`PodmanSandbox() SandboxProvider`** — `sandbox.NewPodmanProvider()`. Rootless, daemonless, Apache-2.0;
  shells out to the `podman` CLI. `Name()` → `"podman"`. Selected via `TaskRequest.Provider` or the CLI's
  `--provider podman`.

### 3. `Spec.Runtime` — the isolation-strength knob (already present, now a decision)

`internal/sandbox.Spec.Runtime` is emitted as `--runtime=<name>` by both built-in providers. Empty leaves
the backend default (runc / crun, shared host kernel). `"kata"` / `"kata-runtime"` routes onto **Kata
Containers** (a lightweight VM per container); `"runsc"` routes onto **gVisor** (a user-space kernel). Both
are Apache-2.0 host binaries the operator opts into — neither is a module dependency. Kubernetes /
Firecracker-style orchestration is explicitly **not** modelled by this field; it is a separate provider
concern.

### 4. Bring-your-own — documented extension points (not shipped code)

`docs/architecture/sandbox-providers.md` gives a concrete **Kubernetes** reference sketch mapping the
interface one-to-one onto `kubectl`: **Create** = `kubectl run` a Pod in a **per-tenant namespace** (derived
from `ctx` identity), **Exec** = `kubectl exec` (streaming), **CopyIn/CopyOut** = `kubectl cp`, **Close** =
`kubectl delete pod`. It states that **Firecracker / Cloud Hypervisor** microVM providers are the **same
pattern** with a VMM substituted for `kubectl`. These are sketches showing the seam is sufficient, not
compiled providers.

### 5. The CLI surface

`aisotropy task run <repo-dir> --prompt "…" [--provider docker|podman] [--image X] [--max-steps N]`
(`cmd/aisotropy/task.go`) is the operator front door: it builds `aisotropy.New()`, maps `--provider` to
`DockerSandbox()` / `PodmanSandbox()`, streams the run's shell output live via `TaskRequest.OnLine`, and
prints the resulting commits / branch / steps. Only the two self-hostable built-ins are exposed as flag
values; bring-your-own is a library concern, not a CLI flag.

### The decision, restated in one line

The sandbox backend is a **pluggable seam** (`SandboxProvider`) with **two self-hostable built-ins**
(Docker/OrbStack, Podman) and a **config-value isolation knob** (`Spec.Runtime` → Kata/gVisor); Kubernetes
and Firecracker are **documented bring-your-own** extension points, and **cloud sandbox services are
deliberately excluded from the core** because they would break self-hostability.

## Consequences

- **Backends are now a consumer's choice, not the project's.** A third party runs the key-brokered task
  agent on Docker, OrbStack, Podman, a Kata/gVisor microVM, or their own Kubernetes/Firecracker provider —
  selected per task — without forking. The core stays small.
- **Rootless self-hosting is a first-class path.** Podman gives operators who reject a root Docker daemon a
  supported, tested, permissively-licensed built-in, with no new dependency.
- **microVM isolation costs one config value.** Naming a hardened runtime upgrades a sandbox to VM- or
  sandboxed-kernel isolation without new code; the operator owns installing the runtime, and an
  uninstalled one is a clean `Create` error.
- **The broker invariant scales to every backend.** Because the exported `SandboxProvider` gets non-secret
  config only, "the key never enters the box" holds for custom providers too — the rule is a property of the
  interface, restated on every alias, not a Docker-specific test.
- **No lock-in, by construction.** The core ships only self-hostable providers; the deliberate exclusion of
  cloud sandbox services keeps a fully self-hosted / air-gapped deployment viable. A consumer who wants a
  cloud sandbox can still write one against the open seam — the choice is theirs, not baked into the core.
- **Honest deltas, named not hidden.** `Spec.Runtime` is not yet a public `TaskRequest` field (set it via a
  custom provider today; a public field is a follow-on). The Kubernetes and Firecracker providers are
  **reference sketches** in docs, not shipped, tested implementations. A real k8s provider with its own
  conformance suite, and an SSE-wired multi-provider task route, are named follow-ons.

## References

- **Builds directly on:** [ADR-0036](0036-sandboxed-task-agent-key-brokering.md) (the sandboxed task agent +
  the key-broker invariant — this ADR exports its `Provider` interface and adds a second implementation) ·
  [ADR-0037](0037-concurrent-sandboxed-task-agents.md) (the concurrent fan-out — every provider is used
  per-task under `RunTasks` unchanged)
- **Same execution-isolation axis:** [ADR-0035](0035-wasm-plugin-sandbox.md) (WASM pure-function tier) —
  the container/microVM "hands" tier is the sibling
- **Positioning it protects:** [ADR-0020](0020-build-vs-adopt-lang-suite-mastra.md) (sovereignty / no
  lock-in — the rationale for excluding cloud sandbox services from the core)
- **Subprocess boundary discipline:** [ADR-0005](0005-datafusion-go-rust-boundary.md) (a `docker` / `podman`
  / `git` CLI non-zero exit is a Go-observable error, no in-process crash) — applied to every CLI-shelling
  provider
- **Different axis (held, not relaxed):** [ADR-0034](0034-open-core-tier2-sealed-driver.md) /
  [ADR-0011](0011-tenant-scoped-statement-builder.md) (tenant *data* isolation — ctx-scoping + sealed
  drivers; a sandbox provider is single-tenant by `ctx` and holds no tenant field)
- **As shipped:** `aisotropy/sandbox.go` (`SandboxProvider`, `Sandbox`, `SandboxSpec`,
  `SandboxExecResult`/`SandboxExecOptions`, `DockerSandbox`, `PodmanSandbox`, `BranchStrategy`) +
  `internal/sandbox/` (`sandbox.go` interface + `Spec.Runtime`, `docker.go`, `podman.go`) + the CLI
  `aisotropy task run` (`cmd/aisotropy/task.go`) · **as documented:**
  `docs/architecture/sandbox-providers.md` (the seam, the built-ins, the runtime knob, the Kubernetes
  reference sketch, the cloud-exclusion rationale)
