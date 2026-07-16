package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// PodmanProvider is the Podman-backed [Provider]. It is the deliberate sibling
// of [DockerProvider]: same broker-safe contract, same "shell out to the stock
// CLI via os/exec — no SDK, no CGO" posture (see internal/sleepcycle), but it
// invokes `podman` instead of `docker`.
//
// Podman is the most thesis-aligned backend. It is Apache-2.0, daemonless (each
// `podman` invocation is its own short-lived process rather than a client of a
// long-running root daemon), and rootless by default — a container's processes
// run under the invoking UNPRIVILEGED host user via a user namespace, so a
// compromised tool call inside the box cannot escalate to host root. That
// matches aisotropy's self-hostable, no-privileged-daemon, no-vendor-lock-in
// stance far better than a root Docker daemon does.
//
// Like the Docker provider it accepts only a non-secret [Spec] and never reads
// a provider API key — the broker invariant documented on the package.
type PodmanProvider struct {
	// Binary is the podman CLI to invoke; empty means "podman" on PATH.
	Binary string

	// Rootless, true by default via [NewPodmanProvider], makes Create map the
	// container into the invoking host user's namespace (`--userns=keep-id…`)
	// so files written into the bind-mounted work tree are owned by the caller
	// on the host, not by a container-internal root. Set false only for a
	// deliberately rootful podman setup.
	Rootless bool

	mu      sync.Mutex
	running map[string]*podmanSandbox // container name -> live sandbox, for cleanup
}

// NewPodmanProvider returns a PodmanProvider using the `podman` binary on PATH,
// rootless by default (the daemonless, unprivileged posture podman is built
// for).
func NewPodmanProvider() *PodmanProvider {
	return &PodmanProvider{Rootless: true, running: map[string]*podmanSandbox{}}
}

// Name implements [Provider].
func (p *PodmanProvider) Name() string { return "podman" }

func (p *PodmanProvider) bin() string {
	if p.Binary != "" {
		return p.Binary
	}
	return "podman"
}

// Create implements [Provider]. It runs a detached container that idles on
// `sleep infinity` so subsequent Exec calls attach via `podman exec`. The
// container is named aisotropy-sbx-<uuid> (or derived from Spec.Name) and
// registered for cleanup so a Close — or the process-exit CleanupAll — reliably
// removes it.
//
// ctx bounds only this creation call.
func (p *PodmanProvider) Create(ctx context.Context, spec Spec) (Sandbox, error) {
	if spec.Image == "" {
		return nil, errors.New("sandbox: Spec.Image is required")
	}
	workdir := spec.Workdir
	if workdir == "" {
		workdir = DefaultWorkdir
	}

	name := spec.Name
	if name == "" {
		name = "aisotropy-sbx-" + uuid.NewString()
	} else {
		name = "aisotropy-sbx-" + name
	}

	out, err := p.run(ctx, p.podmanRunArgs(name, workdir, spec)...)
	if err != nil {
		return nil, fmt.Errorf("sandbox: podman run: %w", err)
	}
	containerID := strings.TrimSpace(out)

	sb := &podmanSandbox{
		provider:    p,
		name:        name,
		containerID: containerID,
		workdir:     workdir,
	}

	p.mu.Lock()
	p.running[name] = sb
	p.mu.Unlock()
	return sb, nil
}

// podmanRunArgs builds the argv (after the `podman` binary) for the detached
// idle container that Create launches:
//
//	run -d --name <name> -w <workdir> [--userns=keep-id…] [--user u:g]
//	    [--runtime=<rt>] [-v src:tgt[:ro]…] [-e K=V…]
//	    --entrypoint sleep <image> infinity
//
// It is a pure function of (name, workdir, spec) with no I/O, so both the
// rootless `--userns=keep-id` mapping and the [Spec.Runtime] `--runtime=` knob
// can be asserted in a unit test without a podman install.
//
// Two podman-specific choices versus the Docker builder:
//
//   - Rootless mapping. When the provider is Rootless, we add
//     `--userns=keep-id[:uid=U,gid=G]`. keep-id maps the invoking host user
//     into the container so a file the run writes into the bind-mounted work
//     tree is owned by that host user, not by an unmapped id. When Spec.User is
//     a "uid:gid" pair we pin the mapping to it; otherwise plain keep-id maps
//     the current user.
//   - Entrypoint override. Podman honors the image's own ENTRYPOINT at run
//     time, so a trailing `sleep infinity` would be appended to whatever the
//     image declares rather than replacing it. We therefore set
//     `--entrypoint sleep` and pass `infinity` as the argument, guaranteeing the
//     idle-forever behaviour regardless of the base image.
func (p *PodmanProvider) podmanRunArgs(name, workdir string, spec Spec) []string {
	args := []string{"run", "-d", "--name", name, "-w", workdir}
	if p.Rootless {
		args = append(args, "--userns="+keepIDArg(spec.User))
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.Runtime != "" {
		// The OCI-runtime knob: shell the operator-chosen runtime (e.g. kata,
		// runsc) straight through. Empty leaves podman's default (crun/runc).
		args = append(args, "--runtime="+spec.Runtime)
	}
	for _, m := range spec.BindMounts {
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	// Spec.Env is NON-SECRET config only — the broker invariant forbids a
	// provider key here (see Spec.Env). Sorted (shared envFlags) for a
	// deterministic argv.
	args = append(args, envFlags(spec.Env)...)
	args = append(args, "--entrypoint", "sleep", spec.Image, "infinity")
	return args
}

// keepIDArg renders the rootless `--userns` value. Given a "uid:gid" Spec.User
// it pins the mapping — `keep-id:uid=<uid>,gid=<gid>` — so the container's
// process id maps exactly onto the host user that owns the work tree; given an
// empty or malformed User it falls back to plain `keep-id`, which maps the
// current host user.
func keepIDArg(user string) string {
	uid, gid, ok := splitUserSpec(user)
	if !ok {
		return "keep-id"
	}
	return fmt.Sprintf("keep-id:uid=%s,gid=%s", uid, gid)
}

// splitUserSpec parses a "uid:gid" string into its two halves, reporting ok
// only when both are present and non-empty.
func splitUserSpec(user string) (uid, gid string, ok bool) {
	parts := strings.SplitN(user, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// CleanupAll force-removes every sandbox this provider created that is still
// tracked. It is the belt-and-suspenders companion to per-sandbox Close, meant
// to be registered as a process-shutdown hook so an abrupt exit does not leak
// containers. Safe to call repeatedly.
func (p *PodmanProvider) CleanupAll(ctx context.Context) {
	p.mu.Lock()
	victims := make([]*podmanSandbox, 0, len(p.running))
	for _, sb := range p.running {
		victims = append(victims, sb)
	}
	p.mu.Unlock()
	for _, sb := range victims {
		_ = sb.Close(ctx)
	}
}

// run executes `podman <args…>` and returns stdout, surfacing stderr in the
// error on failure.
func (p *PodmanProvider) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, p.bin(), args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// podmanSandbox is one live container satisfying [Sandbox].
type podmanSandbox struct {
	provider    *PodmanProvider
	name        string
	containerID string
	workdir     string

	closeOnce sync.Once
}

// Workdir implements [Sandbox].
func (s *podmanSandbox) Workdir() string { return s.workdir }

// Exec implements [Sandbox]. It runs argv inside the container via
// `podman exec -i -w <cwd> [-e K=V] <name> <argv…>` and streams stdout
// line-by-line to opts.OnLine as the process produces it (a bufio.Scanner over
// the piped stdout, shared streamLines helper), while also buffering the full
// stdout/stderr for the returned ExecResult. A non-zero container-command exit
// is reported in ExecResult.ExitCode, not as a Go error; err is non-nil only
// when the command could not be run (podman missing, ctx cancelled). ctx
// cancellation kills the podman exec — and thus the in-container process — via
// CommandContext.
func (s *podmanSandbox) Exec(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error) {
	if len(argv) == 0 {
		return ExecResult{}, errors.New("sandbox: Exec requires a non-empty argv")
	}
	cwd := opts.Workdir
	if cwd == "" {
		cwd = s.workdir
	}

	podmanArgs := []string{"exec", "-i", "-w", cwd}
	for k, v := range opts.Env {
		podmanArgs = append(podmanArgs, "-e", k+"="+v)
	}
	podmanArgs = append(podmanArgs, s.name)
	podmanArgs = append(podmanArgs, argv...)

	cmd := exec.CommandContext(ctx, s.provider.bin(), podmanArgs...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("sandbox: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("sandbox: podman exec start: %w", err)
	}

	// Stream stdout line-by-line on THIS goroutine while the process runs —
	// the required live-streaming behaviour (OnLine fires per line as it
	// arrives, not after Wait returns).
	var stdout strings.Builder
	scanErr := streamLines(stdoutPipe, &stdout, opts.OnLine)

	waitErr := cmd.Wait()

	res := ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			// Command ran and exited non-zero: a normal result, not an error.
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("sandbox: podman exec: %w", waitErr)
	}
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		return res, fmt.Errorf("sandbox: reading stdout: %w", scanErr)
	}
	return res, nil
}

// CopyIn implements [Sandbox] via `podman cp <hostPath> <name>:<sandboxPath>`.
func (s *podmanSandbox) CopyIn(ctx context.Context, hostPath, sandboxPath string) error {
	_, err := s.provider.run(ctx, "cp", hostPath, s.name+":"+sandboxPath)
	if err != nil {
		return fmt.Errorf("sandbox: podman cp in: %w", err)
	}
	return nil
}

// CopyOut implements [Sandbox] via `podman cp <name>:<sandboxPath> <hostPath>`.
func (s *podmanSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	_, err := s.provider.run(ctx, "cp", s.name+":"+sandboxPath, hostPath)
	if err != nil {
		return fmt.Errorf("sandbox: podman cp out: %w", err)
	}
	return nil
}

// Close implements [Sandbox] via `podman rm -f <name>`. Idempotent: the actual
// removal runs once; subsequent calls are no-ops.
func (s *podmanSandbox) Close(ctx context.Context) error {
	var err error
	s.closeOnce.Do(func() {
		_, rmErr := s.provider.run(ctx, "rm", "-f", s.name)
		s.provider.mu.Lock()
		delete(s.provider.running, s.name)
		s.provider.mu.Unlock()
		if rmErr != nil {
			err = fmt.Errorf("sandbox: podman rm: %w", rmErr)
		}
	})
	return err
}

// podmanAvailable reports whether a working podman CLI is reachable, by running
// `podman version`. Tests gate real-container cases on this and t.Skip when it
// is false — the same "skip, never fake" honesty gate the docker/rqlite/qdrant/
// sleepcycle suites use, so `go test` never fails merely because podman is
// absent (it commonly is on macOS/CI). binary empty means "podman" on PATH.
func podmanAvailable(ctx context.Context, binary string) bool {
	bin := binary
	if bin == "" {
		bin = "podman"
	}
	return exec.CommandContext(ctx, bin, "version").Run() == nil
}
