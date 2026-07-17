//go:build keybroker

package aisotropy

import "github.com/Luum-Home/aisotropy/internal/sandbox"

// This file makes the sandbox backend a first-class, pluggable public seam:
// RunTask/RunTasks run the model's "hands" inside a SandboxProvider, and a
// third party can bring their own by implementing this interface. The built-in
// providers (DockerSandbox, PodmanSandbox) cover the self-hostable, no-lock-in
// cases; a custom provider covers the rest (Kubernetes, Firecracker, …).
//
// Deliberately NOT provided: cloud sandbox services (Vercel, Daytona-cloud) —
// they would contradict aisotropy's single-binary, self-hostable, no-lock-in
// posture. Bring your own provider for those if you must.

// SandboxProvider is the pluggable backend that stands up one isolated sandbox
// per task. Implement it to plug in a custom backend beyond the built-ins:
//
//   - Kubernetes: Create a Pod (ideally in a per-tenant namespace — k8s
//     namespaces map cleanly onto tenant isolation), Exec via `kubectl exec`,
//     CopyIn/CopyOut via `kubectl cp`, Close by deleting the Pod.
//   - Firecracker / Cloud Hypervisor microVMs: the same shape over a VMM.
//
// See docs/architecture/sandbox-providers.md for a reference sketch. The built-in
// providers are DockerSandbox and PodmanSandbox.
type SandboxProvider = sandbox.Provider

// Sandbox is one live isolated environment returned by SandboxProvider.Create:
// Exec (streaming) runs the model's hands inside it, CopyIn/CopyOut stage files,
// Close tears it down. The API key never enters it (the broker invariant).
type Sandbox = sandbox.Sandbox

// SandboxSpec is the recipe a SandboxProvider.Create receives. Its Runtime field
// selects an OCI runtime for the OCI-backed built-ins — e.g. "kata-runtime"
// (Kata Containers, a microVM per container) or "runsc" (gVisor, a sandboxed
// kernel) — so microVM-grade isolation is a config knob, not a new provider.
//
// It also carries the secure-by-default hardening knobs the built-in providers
// apply to every container (Network=none, cap-drop=ALL, no-new-privileges,
// read-only rootfs + tmpfs scratch, and pids/memory/cpu caps): Network,
// PidsLimit, Memory, CPUs, Writable, CapAdd, AllowNewPrivileges. Each is a
// widen-when-needed override — the zero value keeps the conservative default.
// See docs/architecture/sandbox-providers.md → "Secure-by-default container
// hardening".
type SandboxSpec = sandbox.Spec

// SandboxExecResult and SandboxExecOptions are the Exec I/O types a custom
// SandboxProvider implementation needs.
type (
	SandboxExecResult  = sandbox.ExecResult
	SandboxExecOptions = sandbox.ExecOptions
)

// BranchStrategy selects what a task does with the commits it produced:
// MergeToHead (default) merges the task branch into the repo's HEAD; ReturnBranch
// (a.k.a. NamedBranch) leaves the commits on the task branch for the caller to
// review/merge/open a PR from.
type BranchStrategy = sandbox.BranchStrategy

const (
	// MergeToHead auto-merges the task branch into HEAD (the default; also the
	// zero value, so an unset TaskRequest.BranchStrategy keeps this behavior).
	MergeToHead = sandbox.MergeToHead
	// ReturnBranch leaves the commits on the task branch, unmerged.
	ReturnBranch = sandbox.ReturnBranch
	// NamedBranch is an alias for ReturnBranch.
	NamedBranch = sandbox.NamedBranch
)

// DockerSandbox returns the built-in Docker sandbox provider. It shells out to
// the `docker` CLI, so it works transparently with any Docker-API-compatible
// engine — notably OrbStack (a fast Docker Desktop replacement). Set
// SandboxSpec.Runtime to route a task onto a Kata/gVisor microVM runtime. This
// is the default provider when TaskRequest.Provider is nil.
func DockerSandbox() SandboxProvider { return sandbox.NewDockerProvider() }

// PodmanSandbox returns the built-in Podman sandbox provider: rootless,
// daemonless, Apache-2.0 — the most self-hosting-aligned backend. It shells out
// to the `podman` CLI.
func PodmanSandbox() SandboxProvider { return sandbox.NewPodmanProvider() }
