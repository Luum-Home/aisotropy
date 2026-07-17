package sandbox

import "strconv"

// Secure-by-default isolation and resource limits applied to EVERY sandbox
// container by the Docker and Podman run-arg builders unless the corresponding
// [Spec] field explicitly widens them.
//
// These defaults exist because the sandbox is the "hands" boundary for
// UNTRUSTED, prompt-injectable work (see the package doc). Without them a
// single injected task could fork-bomb the host, exhaust its memory, or — where
// rqlite/Qdrant are deployed — reach their ports over the container network and
// bypass the app-layer tenant filter. The values are deliberately conservative;
// an operator widens per-task via the Spec when a task legitimately needs more.
const (
	// DefaultPidsLimit caps in-container processes/threads (fork-bomb guard).
	DefaultPidsLimit = 256
	// DefaultMemory caps container memory.
	DefaultMemory = "512m"
	// DefaultCPUs caps the container CPU quota.
	DefaultCPUs = "1.0"
	// DefaultNetwork is the secure default network mode: no network at all. The
	// v1 tool-hands need no egress, and "none" also prevents a compromised task
	// from reaching co-located rqlite/Qdrant ports.
	DefaultNetwork = "none"
	// scratchTmpfs is the writable in-memory mount added when the root filesystem
	// is read-only, so tools that need a scratch dir keep working without a
	// writable rootfs.
	scratchTmpfs = "/tmp"
)

// hardeningFlags renders the secure-by-default isolation + resource flags shared
// by dockerRunArgs and podmanRunArgs. It is a pure function of spec (no I/O), so
// the emitted argv can be asserted in a unit test without a container runtime.
//
// The flags it always emits by default (i.e. for a zero-valued Spec):
//
//	--network=none                        (no egress; no reach to rqlite/Qdrant)
//	--cap-drop=ALL                        (drop every Linux capability)
//	--security-opt=no-new-privileges      (block privilege escalation)
//	--read-only --tmpfs /tmp              (read-only rootfs + writable scratch)
//	--pids-limit 256                      (fork-bomb guard)
//	--memory 512m                         (OOM guard)
//	--cpus 1.0                            (CPU cap)
//
// The bind-mounted worktree is unaffected by --read-only (bind mounts stay
// writable unless mounted :ro), so a task can still read and write its workspace
// while the rest of the root filesystem is immutable.
func hardeningFlags(spec Spec) []string {
	var args []string

	// Network: none by default; explicit opt-in only.
	network := spec.Network
	if network == "" {
		network = DefaultNetwork
	}
	args = append(args, "--network="+network)

	// Drop ALL capabilities, then add back only those explicitly requested.
	args = append(args, "--cap-drop=ALL")
	for _, c := range spec.CapAdd {
		if c != "" {
			args = append(args, "--cap-add="+c)
		}
	}

	// Block privilege escalation unless the operator explicitly allows it.
	if !spec.AllowNewPrivileges {
		args = append(args, "--security-opt=no-new-privileges")
	}

	// Read-only rootfs + a small writable tmpfs scratch, unless the task needs a
	// writable rootfs. The worktree bind mount stays rw regardless.
	if !spec.Writable {
		args = append(args, "--read-only", "--tmpfs", scratchTmpfs)
	}

	// pids-limit: 0 → secure default; <0 → passthrough (backend "unlimited").
	pids := spec.PidsLimit
	if pids == 0 {
		pids = DefaultPidsLimit
	}
	args = append(args, "--pids-limit", strconv.Itoa(pids))

	// memory / cpus: empty → conservative default; any explicit value (including
	// "0", the backend's "unlimited") is passed through so an operator can widen.
	mem := spec.Memory
	if mem == "" {
		mem = DefaultMemory
	}
	args = append(args, "--memory", mem)

	cpus := spec.CPUs
	if cpus == "" {
		cpus = DefaultCPUs
	}
	args = append(args, "--cpus", cpus)

	return args
}
