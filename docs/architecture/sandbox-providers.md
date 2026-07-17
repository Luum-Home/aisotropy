---
type: Concept
title: Pluggable sandbox providers — the SandboxProvider seam, the built-ins, the runtime knob, and bring-your-own
description: Code-grounded description of the sandbox backend seam (ADR-0038) — the exported SandboxProvider interface App.RunTask/RunTasks run the model's "hands" inside, the two self-hostable built-ins (Docker + OrbStack transparently, Podman rootless), microVM-grade isolation via the Spec.Runtime knob (Kata/gVisor), and how to bring your own provider with a concrete Kubernetes reference sketch. States plainly why cloud sandbox services are deliberately not provided.
tags: [architecture, sandbox, task-agent, providers, isolation, docker, podman, kubernetes, microvm, phase-9]
timestamp: 2026-07-16T00:00:00Z
---

# Pluggable sandbox providers

This is the code-grounded companion to [ADR-0036](../decisions/0036-sandboxed-task-agent-key-brokering.md)
(the sandboxed task agent + the key-broker invariant), [ADR-0037](../decisions/0037-concurrent-sandboxed-task-agents.md)
(the concurrent fan-out), and [ADR-0038](../decisions/0038-pluggable-sandbox-providers.md)
(the decision to make the sandbox backend pluggable, ship Podman, and expose the runtime knob).

When `App.RunTask` / `App.RunTasks` run the model's **hands** — the shell / git / file operations the
LLM decides to perform — those operations execute inside an **isolated sandbox**, physically separated
from the host process that talks to the model. The provider API key is spent HOST-SIDE and **never**
enters the sandbox (the broker invariant, ADR-0036). The backend that stands up that sandbox is a
**pluggable seam**: `aisotropy.SandboxProvider`.

## The seam: `SandboxProvider`

`SandboxProvider` (an alias for the internal `sandbox.Provider`) is the one interface a backend
implements. It is a factory: `Create` stands up one live `Sandbox` per task from a `SandboxSpec`; the
returned `Sandbox` runs argv (`Exec`, streaming), stages files (`CopyIn` / `CopyOut`), and tears down
on `Close`.

```go
// aisotropy/sandbox.go — exported aliases over internal/sandbox.
type SandboxProvider = sandbox.Provider   // Create(ctx, Spec) (Sandbox, error); Name() string
type Sandbox         = sandbox.Sandbox    // Exec / CopyIn / CopyOut / Workdir / Close
type SandboxSpec     = sandbox.Spec       // Image, Workdir, BindMounts, Env, User, Runtime, hardening knobs (Network/PidsLimit/Memory/CPUs/Writable/CapAdd/AllowNewPrivileges), Name
type SandboxExecResult  = sandbox.ExecResult
type SandboxExecOptions = sandbox.ExecOptions
```

A task selects its backend through `TaskRequest.Provider`:

```go
res, err := app.RunTask(ctx, aisotropy.TaskRequest{
    RepoDir:  "/path/to/repo",
    Prompt:   "add a health endpoint",
    Scopes:   aisotropy.SandboxToolScopes(),
    Provider: aisotropy.PodmanSandbox(), // nil defaults to DockerSandbox()
})
```

The one hard rule every provider inherits: a `SandboxSpec.Env` (and per-`Exec` `Env`) carries
**non-secret configuration only**. A provider MUST NOT be handed, and MUST NOT read, any provider API
key — the LLM call happens host-side in `internal/llm` and the key must never cross into the box. The
task runner that builds `Spec`s enforces this; a custom provider that defeats it (by reading an ambient
host secret on the caller's behalf) breaks the broker invariant.

## The built-in providers

Both built-ins shell out to a stock CLI via `os/exec` — no container SDK, no CGO — matching the repo's
"shell out to a stock binary" posture. Both are self-hostable and impose no vendor lock-in.

### Docker — `aisotropy.DockerSandbox()`

The default provider (used when `TaskRequest.Provider` is nil). It shells out to the `docker` CLI, so
it works transparently with **any Docker-API-compatible engine** — notably **OrbStack**, a fast Docker
Desktop replacement on macOS: because the seam only talks to the `docker` CLI, OrbStack is picked up
with **no code change and no configuration** in aisotropy. `Name()` reports `"docker"`.

### Podman — `aisotropy.PodmanSandbox()`

A **rootless, daemonless** backend, licensed **Apache-2.0** — the most self-hosting-aligned built-in.
It shells out to the `podman` CLI and is the recommended choice where running a root Docker daemon is
undesirable (rootless containers run under the invoking user, not a privileged daemon). `Name()`
reports `"podman"`. Select it explicitly per task via `TaskRequest.Provider: aisotropy.PodmanSandbox()`,
or on the CLI with `aisotropy task run <repo> --prompt "…" --provider podman`.

## microVM-grade isolation without a new provider: the `Runtime` knob

The default OCI runtime (runc / crun) shares the host kernel. For stronger isolation you do **not** need
a new provider — you name a hardened **OCI runtime** in `SandboxSpec.Runtime`, and the built-in
Docker/Podman providers emit it as `--runtime=<name>` on `docker run` / `podman run`:

| `SandboxSpec.Runtime` | Backend runtime | Isolation | License |
|---|---|---|---|
| `""` (default) | runc / crun | shared host kernel | — |
| `"kata"` / `"kata-runtime"` | Kata Containers | a lightweight **VM per container** | Apache-2.0 |
| `"runsc"` | gVisor | a **user-space kernel** (syscall interception) | Apache-2.0 |

Both Kata and gVisor are themselves OCI runtimes that slot in under the existing CLI, so microVM- or
sandboxed-kernel-grade isolation is a **config value, not a code path**. They are **host binaries the
operator installs and registers** — neither is a dependency of this module (both fit the constitutional
license gate). An unknown or uninstalled runtime surfaces as a normal backend error from `Create`, not a
panic.

> Note: `SandboxSpec.Runtime` is an `internal/sandbox` field wired by the task runner. The public
> `TaskRequest` does not yet expose a `Runtime` field directly — a custom `SandboxProvider` that wraps a
> built-in, or a future `TaskRequest` field, is the path to setting it per task. This is stated plainly
> rather than implying a knob that is not yet on the public request.

## Secure-by-default container hardening

The sandbox is the **hands** boundary for untrusted, prompt-injectable work, so the built-in Docker and
Podman providers apply a conservative set of isolation and resource limits to **every** container by
default. A zero-valued `SandboxSpec` — the shape the task runner builds — already gets the full set; each
limit is a **widen-when-needed knob** on `SandboxSpec`, never something you must remember to switch on.

| Emitted flag (default) | `SandboxSpec` knob | Threat it closes |
|---|---|---|
| `--network=none` | `Network` (`""` → `none`) | A compromised task reaching co-located rqlite/Qdrant ports over the container network and bypassing the app-layer tenant filter; any egress/exfiltration. |
| `--cap-drop=ALL` | `CapAdd` (add back specific caps) | Ambient Linux capabilities (raw sockets, mount, etc.). |
| `--security-opt=no-new-privileges` | `AllowNewPrivileges` (`false` → set) | setuid/setgid/file-capability privilege escalation. |
| `--read-only` + `--tmpfs /tmp` | `Writable` (`false` → read-only) | Tampering with container system files; unbounded writes to the container layer. The worktree bind mount stays writable, and `/tmp` is a capped tmpfs scratch. |
| `--pids-limit 256` | `PidsLimit` (`0` → 256; `<0` → unlimited) | Fork bombs. |
| `--memory 512m` | `Memory` (`""` → `512m`; `"0"` → unlimited) | Host memory exhaustion (bounds the tmpfs scratch too). |
| `--cpus 1.0` | `CPUs` (`""` → `1.0`; `"0"` → unlimited) | CPU starvation of the host. |

A default wall-clock deadline complements the resource caps: `App.RunTask` bounds a run at
`DefaultTaskTimeout` (10m) when the caller's context has no deadline of its own, configurable per task
via `TaskRequest.MaxDuration`. Context cancellation already tears down `docker exec` (and the in-container
process); the default deadline guarantees it actually fires without an operator watching.

**Recommended runtime for untrusted prompts.** Defense in depth: run the model's hands under **rootless
Podman** (`aisotropy.PodmanSandbox()` — the user-namespace mapping means a container-internal root is an
unprivileged host user) and/or a **hardened OCI runtime** via `SandboxSpec.Runtime` — **gVisor**
(`"runsc"`, a user-space kernel) or **Kata** (`"kata-runtime"`, a microVM per container). The seccomp/
capability/namespace controls above sit *inside* that stronger boundary.

> **Honest limitation — `--read-only` and `docker cp`.** With a read-only rootfs, Docker's `docker cp`
> (which backs `Sandbox.CopyIn`/`CopyOut`) only traverses **bind-mount** paths (the worktree), not rootfs
> or tmpfs paths: a `docker cp` to/from `/tmp` is refused ("container rootfs is marked read-only" /
> "could not find the file") even though a runtime `Exec` write to `/tmp` succeeds. This is a documented
> Docker behavior, not an aisotropy bug. It does **not** affect real runs — the task path stages files
> through the writable worktree + `write_file` Exec and never `CopyIn`s to the rootfs — but a caller that
> needs `CopyIn`/`CopyOut` against a non-worktree path must either target the worktree or set
> `SandboxSpec.Writable = true` to lift the read-only rootfs. The **key-broker invariant is untouched**:
> none of this places a provider key inside the container.

## Bring your own provider

Beyond the built-ins, implement `SandboxProvider` to plug in any backend — the task runner depends on
the **capability**, not on Docker specifically. Your `Create` returns a `Sandbox` whose `Exec` streams
each stdout line to `opts.OnLine` as it is produced (streaming is **required**, not optional), whose
`CopyIn`/`CopyOut` stage files, whose `Workdir` reports the in-sandbox mount point, and whose `Close`
tears everything down idempotently. The same broker rule applies: never accept or read a provider key.

### Reference sketch — Kubernetes (a Pod per task, per-tenant namespace)

Kubernetes namespaces map cleanly onto tenant isolation, so a natural design is **one Pod per task in a
per-tenant namespace**, driven through `kubectl`:

```go
// Illustrative sketch — not shipped. Implements aisotropy.SandboxProvider.
type K8sProvider struct {
    namespaceFor func(ctx context.Context) string // derive per-tenant ns from ctx identity
}

func (p *K8sProvider) Name() string { return "kubernetes" }

func (p *K8sProvider) Create(ctx context.Context, spec aisotropy.SandboxSpec) (aisotropy.Sandbox, error) {
    ns := p.namespaceFor(ctx)
    pod := deriveName(spec.Name)
    // kubectl run: create a Pod from spec.Image in ns, sleeping so we can exec into it.
    // (spec.Env carries NON-SECRET config only — never a provider key.)
    if err := kubectl(ctx, "-n", ns, "run", pod, "--image="+spec.Image, "--restart=Never",
        "--command", "--", "sleep", "infinity"); err != nil {
        return nil, err
    }
    if err := waitReady(ctx, ns, pod); err != nil {
        return nil, err
    }
    return &k8sSandbox{ns: ns, pod: pod, workdir: orDefault(spec.Workdir)}, nil
}

// The Sandbox maps each interface method onto a kubectl verb:
func (s *k8sSandbox) Exec(ctx context.Context, argv []string, opts aisotropy.SandboxExecOptions) (aisotropy.SandboxExecResult, error) {
    // kubectl exec -n <ns> <pod> -- <argv...>, streaming stdout line-by-line into opts.OnLine.
}
func (s *k8sSandbox) CopyIn(ctx context.Context, hostPath, sandboxPath string) error  { /* kubectl cp host <ns>/<pod>:path */ }
func (s *k8sSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error { /* kubectl cp <ns>/<pod>:path host */ }
func (s *k8sSandbox) Workdir() string { return s.workdir }
func (s *k8sSandbox) Close(ctx context.Context) error { /* kubectl delete pod -n <ns> <pod> --now */ }
```

The mapping is one-to-one with the interface: **Create** = `kubectl run` (Pod in a per-tenant
namespace), **Exec** = `kubectl exec` (streaming), **CopyIn/CopyOut** = `kubectl cp`, **Close** =
`kubectl delete pod`. Deriving the namespace from the tenant identity carried in `ctx`
(`aisotropy.TenantFromContext`) keeps the "per-tenant namespace" boundary honest.

### The same shape over a VMM — Firecracker / Cloud Hypervisor

A **Firecracker** or **Cloud Hypervisor** microVM provider is the **same pattern** with the VMM's
tooling substituted for `kubectl`: **Create** boots a microVM from the image, **Exec** runs argv inside
it (over the VM's guest agent / a vsock channel), **CopyIn/CopyOut** stage files across the boundary,
and **Close** shuts the microVM down. Nothing about the seam is Kubernetes-specific — it is deliberately
just "stand up an isolated environment, run argv in it, tear it down."

## Deliberately NOT provided: cloud sandbox services

Cloud sandbox services — **Vercel Sandbox, Daytona-cloud**, and the like — are **deliberately not**
shipped as built-in providers. They would contradict aisotropy's single-binary, **self-hostable,
no-lock-in** posture: a built-in that only works against a specific vendor's hosted control plane makes
the runtime depend on that vendor to run the model's hands. The seam is open precisely so you can bring
one yourself if you must (implement `SandboxProvider` against the vendor's API), but the project will
not adopt one into the core. The supported, self-hostable answers are the two built-ins (Docker/OrbStack,
Podman), the `Runtime` knob for microVM-grade isolation, and a bring-your-own provider for Kubernetes /
Firecracker / Cloud Hypervisor.

## References

- **The seam + built-ins:** `aisotropy/sandbox.go` (`SandboxProvider`, `Sandbox`, `SandboxSpec`,
  `DockerSandbox`, `PodmanSandbox`), `internal/sandbox/` (`sandbox.go` interface + `Spec.Runtime`,
  `docker.go`, `podman.go`)
- **Where the sandbox is used:** `aisotropy/runtask.go` (`App.RunTask`, `TaskRequest.Provider`),
  `aisotropy/runtasks.go` (`App.RunTasks`) · **CLI:** `aisotropy task run … --provider docker|podman`
  (`cmd/aisotropy/task.go`)
- **Decisions:** [ADR-0036](../decisions/0036-sandboxed-task-agent-key-brokering.md) (one task +
  key-broker), [ADR-0037](../decisions/0037-concurrent-sandboxed-task-agents.md) (concurrent fan-out),
  [ADR-0038](../decisions/0038-pluggable-sandbox-providers.md) (pluggable providers + Podman + runtime
  knob + exclusion of cloud services)
