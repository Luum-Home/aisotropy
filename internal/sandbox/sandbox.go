// Package sandbox is the "hands" boundary of the aisotropy agent: an isolated
// execution environment where tool CALLS (shell/git/file ops the LLM decides to
// run) actually happen, physically separated from the process that talks to the
// model.
//
// # The broker invariant (why this package exists at all)
//
// aisotropy is a KEY-BROKER, not a key-forwarder. The provider API key
// (ANTHROPIC_API_KEY, OPENAI_API_KEY, …) lives HOST-SIDE and is read only by
// internal/llm. The LLM call — the one privileged operation that spends the
// key — happens in the host process and NEVER inside a sandbox. What crosses
// into the sandbox is only the *effect* of a tool the model chose: "run these
// argv", "read that file". The container therefore holds the agent's hands, not
// its wallet.
//
// This is the deliberate inversion versus sandcastle-style designs, where the
// agent (and its credentials) run *inside* the box and the box reaches out to
// the model. Here the model stays outside; only the untrusted, side-effecting
// work is boxed. A compromised tool, prompt-injected instruction, or runaway
// command can scribble on a throwaway git worktree — it cannot exfiltrate the
// provider key, because the key was never in the room.
//
// Concretely: a [Spec].Env carries NON-SECRET configuration only (PATH tweaks,
// LANG, a build flag). It is a hard rule — enforced by the task runner that
// builds Specs, and restated on every type here — that no provider credential
// is ever placed in Spec.Env or otherwise handed to a Provider. A Provider
// implementation must treat everything it receives as untrusted config and must
// not read ambient host secrets (os.Getenv on a provider key) on the caller's
// behalf.
//
// # Shape
//
//	Provider — a factory for sandboxes over one backend (Docker today).
//	Sandbox  — one live, isolated environment with a mounted work tree.
//	Spec     — the non-secret recipe a Provider instantiates.
//
// The Docker provider (docker.go) shells out to the `docker` CLI via os/exec —
// no Docker SDK, no CGO, matching the repo's "shell out to a stock binary"
// posture (see internal/sleepcycle for the sibling subprocess pattern). Git
// worktree plumbing (worktree.go) gives each task its own branch so a run's
// output can be reviewed and merged to HEAD, or thrown away, atomically.
package sandbox

import "context"

// Provider is a factory for [Sandbox]es over a single isolation backend. The
// Docker provider is the only implementation today; the interface exists so the
// task runner depends on the capability, not on Docker specifically (a future
// firecracker/gVisor/podman backend is a drop-in).
//
// A Provider MUST NOT be handed, and MUST NOT read, any provider API key. It
// receives a [Spec] of non-secret configuration and nothing else — the broker
// invariant documented on the package. See [Spec.Env].
type Provider interface {
	// Create instantiates one live sandbox from spec. The returned Sandbox is
	// ready for Exec/CopyIn/CopyOut immediately and MUST be released with
	// Close (a Provider typically also registers a process-exit cleanup so a
	// crash does not leak the backing container). ctx bounds only the
	// creation call, not the sandbox lifetime.
	Create(ctx context.Context, spec Spec) (Sandbox, error)

	// Name is a short, stable identifier for the backend ("docker"), used in
	// logs and errors.
	Name() string
}

// Sandbox is one live, isolated execution environment with a work tree mounted
// at [Sandbox.Workdir]. It is the agent's hands: commands run here, files are
// staged in and pulled out here. It never holds the provider key.
//
// A Sandbox is safe for sequential use by one task. Concurrent Exec calls
// against the same Sandbox are not guaranteed by this interface — a task runner
// drives one command at a time.
type Sandbox interface {
	// Exec runs argv inside the sandbox and returns once the process exits.
	//
	// Streaming is REQUIRED, not optional: when opts.OnLine is set, each line
	// of stdout is delivered to the callback AS IT IS PRODUCED, so a caller
	// (and, upstream, an SSE client) sees output live rather than after the
	// command finishes. An implementation that only buffers and returns the
	// full output at the end is INCORRECT. The complete stdout/stderr are also
	// returned in ExecResult for convenience and post-hoc inspection.
	//
	// argv is passed as a vector (no host-side shell splitting by the caller);
	// the provider is responsible for running it in the sandbox. A non-zero
	// exit is reported via ExecResult.ExitCode and is NOT a Go error — err is
	// non-nil only for a failure to run the command at all (backend down,
	// ctx cancelled, copy failure). ctx cancellation MUST stop the underlying
	// process, not merely stop reading its output.
	Exec(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error)

	// CopyIn copies a host file or directory (hostPath) to sandboxPath inside
	// the sandbox. Used to stage inputs that are not part of the mounted work
	// tree.
	CopyIn(ctx context.Context, hostPath, sandboxPath string) error

	// CopyOut copies a file or directory from sandboxPath inside the sandbox
	// to hostPath on the host. Used to retrieve artifacts produced by a run
	// that live outside the (already host-visible) mounted work tree.
	CopyOut(ctx context.Context, sandboxPath, hostPath string) error

	// Workdir is the absolute path INSIDE the sandbox where the task's work
	// tree is mounted and where Exec runs by default. For the Docker provider
	// this is the container-side target of the work-tree bind mount, so the
	// same files are visible on the host (at the mount source) and in the box.
	Workdir() string

	// Close tears the sandbox down and releases its backing resources
	// (removes the container, deregisters cleanup). It is idempotent: a second
	// Close is a no-op. ctx bounds the teardown call.
	Close(ctx context.Context) error
}

// ExecOptions tunes a single [Sandbox.Exec] call.
type ExecOptions struct {
	// OnLine, when non-nil, is invoked once per line of stdout as it is
	// produced (newline-delimited, the terminator stripped). This is the
	// live-streaming hook the Exec contract requires; leaving it nil means the
	// caller only wants the buffered ExecResult. The callback runs on the
	// goroutine that scans the process output — it should be cheap and must
	// not block (do fan-out/SSE work asynchronously).
	OnLine func(line string)

	// Workdir overrides the directory the command runs in, as an absolute
	// path INSIDE the sandbox. Empty means [Sandbox.Workdir] (the mounted work
	// tree).
	Workdir string

	// Env are additional NON-SECRET environment variables for this command
	// only, merged over the sandbox's Spec.Env. Same hard rule as Spec.Env: no
	// provider API key, ever. See the package doc's broker invariant.
	Env map[string]string
}

// ExecResult is the outcome of a completed [Sandbox.Exec] call.
type ExecResult struct {
	// ExitCode is the process exit status. A non-zero code is a normal result
	// (the command ran and failed), NOT a Go error — inspect this, don't rely
	// on the error return, to detect a failing command.
	ExitCode int

	// Stdout is the complete standard output, buffered even when OnLine
	// streamed it line-by-line, so a caller that wants the whole thing at the
	// end does not have to reassemble it.
	Stdout string

	// Stderr is the complete standard error.
	Stderr string
}

// Mount is one host→sandbox bind mount. For the Docker provider it becomes a
// `-v Source:Target[:ro]` flag.
type Mount struct {
	// Source is the absolute host path.
	Source string
	// Target is the absolute path inside the sandbox.
	Target string
	// ReadOnly mounts the path read-only when true.
	ReadOnly bool
}

// Spec is the NON-SECRET recipe a [Provider] instantiates into a [Sandbox]. It
// is deliberately a plain data struct: everything a backend needs to stand up
// an environment, and nothing it must not have.
type Spec struct {
	// Image is the container image reference the backend runs (Docker
	// provider). Required.
	Image string

	// Workdir is the absolute path INSIDE the sandbox where the work-tree
	// mount lands and where Exec runs by default. Empty defaults to
	// DefaultWorkdir. The task runner typically also lists the work tree in
	// BindMounts with this same Target.
	Workdir string

	// BindMounts are host→sandbox bind mounts. The task's git worktree is
	// mounted here (Target == Workdir) so the run's changes are visible on the
	// host for review and merge-to-HEAD (see worktree.go).
	BindMounts []Mount

	// Env is NON-SECRET configuration exposed to processes in the sandbox
	// (PATH, LANG, a build flag). It is a HARD INVARIANT that the provider API
	// key is NEVER placed here: the LLM call happens host-side in internal/llm
	// and the key must never enter the sandbox. The task runner that builds
	// Specs enforces this; a Provider must not defeat it by reading ambient
	// host secrets on the caller's behalf. See the package doc.
	Env map[string]string

	// User is the "uid:gid" the sandbox process runs as (Docker `--user`).
	// Empty leaves the image default; the task runner sets it to the host
	// user so files written into the bind-mounted work tree are owned by the
	// caller, not root.
	User string

	// Runtime selects the OCI runtime the backend hands the container to,
	// emitted as `--runtime=<Runtime>` on `docker run` / `podman run`. Empty
	// (the default) leaves the backend's own default runtime (runc / crun) in
	// place.
	//
	// This is the isolation-strength knob. The default runtime shares the host
	// kernel; naming a hardened runtime here upgrades a sandbox to VM- or
	// sandboxed-kernel isolation WITHOUT a new Provider, because Kata Containers
	// (a lightweight VM per container, runtime "kata"/"kata-runtime") and gVisor
	// (a user-space kernel, runtime "runsc") are themselves OCI runtimes that
	// slot in under the existing Docker/Podman CLI. It is a config value, not a
	// code path: aisotropy shells the name through to the backend and the
	// operator is responsible for having installed and registered that runtime
	// on the host. An unknown/uninstalled runtime surfaces as a normal backend
	// error from Create, not a panic.
	//
	// Both Kata (Apache-2.0) and gVisor (Apache-2.0) fit the constitutional
	// license gate; neither is a dependency of this module — they are host
	// binaries the operator opts into. Kubernetes/Firecracker-style orchestration
	// is a separate, out-of-scope extension point, not modelled here.
	Runtime string

	// --- Isolation & resource limits (secure-by-default; see hardening.go) ---
	//
	// Every field below is applied by the run-arg builders with a CONSERVATIVE
	// SECURE DEFAULT when left at its zero value, so a Spec built with none of
	// them set (as the task runner does) still gets the full hardening. Each is
	// a widen-when-needed knob: set it to loosen an isolation control for a task
	// that legitimately requires it. Widening should be a deliberate, per-task
	// decision — never the default.

	// Network is the container network mode (`--network`). Empty is the SECURE
	// DEFAULT of "none": the v1 tool-hands need no egress, and none also closes
	// off reaching co-located rqlite/Qdrant ports over the container network
	// (which would bypass the app-layer tenant filter). Set it EXPLICITLY (e.g.
	// "bridge", "host", a named network) only for a task type that genuinely
	// needs network — an opt-in, never the default.
	Network string

	// PidsLimit caps the number of processes/threads in the container
	// (`--pids-limit`) — the fork-bomb guard. Zero applies the secure default
	// DefaultPidsLimit. A NEGATIVE value (e.g. -1, docker's "unlimited"
	// sentinel) is passed through for a task that must be uncapped.
	PidsLimit int

	// Memory caps container memory (`--memory`, e.g. "512m", "2g"). Empty applies
	// the secure default DefaultMemory. Any explicit value is passed through —
	// including "0", the backend's "unlimited" sentinel — so an operator can widen.
	Memory string

	// CPUs caps the CPU quota (`--cpus`, e.g. "1.0", "2"). Empty applies the
	// secure default DefaultCPUs. Any explicit value is passed through (including
	// "0" for unlimited).
	CPUs string

	// Writable, when false (the SECURE DEFAULT), runs the container root
	// filesystem READ-ONLY (`--read-only`) with a small writable tmpfs scratch at
	// scratchTmpfs (/tmp). The task's bind-mounted worktree stays writable — bind
	// mounts are unaffected by --read-only — so the task can still read and write
	// its workspace. Set true to drop --read-only for a task that must write
	// outside its worktree and /tmp.
	Writable bool

	// CapAdd lists Linux capabilities to add back on top of the default
	// `--cap-drop=ALL`. Empty (the secure default) means the container keeps NO
	// capabilities. Add only the specific caps a task needs (e.g. "NET_BIND_SERVICE");
	// the drop-all-then-add pattern keeps the grant minimal and explicit.
	CapAdd []string

	// AllowNewPrivileges, when false (the SECURE DEFAULT), sets
	// `--security-opt=no-new-privileges`, blocking setuid/setgid/file-capability
	// privilege escalation inside the container. Set true only for a task that
	// legitimately needs to gain privileges (rare).
	AllowNewPrivileges bool

	// Name, when set, is used to derive the backing container name; empty lets
	// the provider generate a unique one. Mainly a test/observability hook.
	Name string
}

// DefaultWorkdir is the in-sandbox mount point used when Spec.Workdir is empty.
const DefaultWorkdir = "/workspace"
