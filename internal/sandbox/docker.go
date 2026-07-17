package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// DockerProvider is the Docker-backed [Provider]. It shells out to the `docker`
// CLI via os/exec — no Docker SDK, no CGO — matching the repo's stock-binary
// subprocess posture (see internal/sleepcycle). This keeps the dependency
// surface at MIT/Apache stdlib + uuid and adds no client library the
// constitutional license gate would have to weigh.
//
// The provider is a broker-safe factory: it accepts a [Spec] of non-secret
// config and never reads a provider API key. See the package doc.
type DockerProvider struct {
	// Binary is the docker CLI to invoke; empty means "docker" on PATH.
	Binary string

	mu      sync.Mutex
	running map[string]*dockerSandbox // container name -> live sandbox, for cleanup
}

// NewDockerProvider returns a DockerProvider using the `docker` binary on PATH.
func NewDockerProvider() *DockerProvider {
	return &DockerProvider{running: map[string]*dockerSandbox{}}
}

// Name implements [Provider].
func (p *DockerProvider) Name() string { return "docker" }

func (p *DockerProvider) bin() string {
	if p.Binary != "" {
		return p.Binary
	}
	return "docker"
}

// Create implements [Provider]. It runs a detached container
// (`docker run -d … sleep infinity`) that idles so subsequent Exec calls attach
// to it via `docker exec`. The container is named aisotropy-sbx-<uuid> (or
// derived from Spec.Name) and registered for cleanup so a Close — or the
// process-exit CleanupAll — reliably removes it.
//
// ctx bounds only this creation call.
func (p *DockerProvider) Create(ctx context.Context, spec Spec) (Sandbox, error) {
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

	out, err := p.run(ctx, dockerRunArgs(name, workdir, spec)...)
	if err != nil {
		return nil, fmt.Errorf("sandbox: docker run: %w", err)
	}
	containerID := strings.TrimSpace(out)

	sb := &dockerSandbox{
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

// CleanupAll force-removes every sandbox this provider created that is still
// tracked. It is the belt-and-suspenders companion to per-sandbox Close, meant
// to be registered as a process-shutdown hook (defer / signal handler) so an
// abrupt exit does not leak containers. Safe to call repeatedly.
func (p *DockerProvider) CleanupAll(ctx context.Context) {
	p.mu.Lock()
	victims := make([]*dockerSandbox, 0, len(p.running))
	for _, sb := range p.running {
		victims = append(victims, sb)
	}
	p.mu.Unlock()
	for _, sb := range victims {
		_ = sb.Close(ctx)
	}
}

// dockerRunArgs builds the argv (after the `docker` binary) for the detached
// idle container that Create launches:
//
//	run -d --name <name> -w <workdir> [--user u:g] [--runtime=<rt>]
//	    --network=none --cap-drop=ALL --security-opt=no-new-privileges
//	    --read-only --tmpfs /tmp --pids-limit 256 --memory 512m --cpus 1.0
//	    [-v src:tgt[:ro]…] [-e K=V…] <image> sleep infinity
//
// The middle block is the secure-by-default hardening (see hardeningFlags);
// each flag is a Spec knob an operator can widen. It is a pure function of
// (name, workdir, spec) with no I/O, so the flag wiring — the [Spec.Runtime]
// `--runtime=` knob and the hardening defaults — can be asserted in a unit test
// without a Docker daemon. Docker's default image entrypoint does not swallow
// the trailing `sleep infinity`, so the command is appended directly (contrast
// the Podman provider, which must override the entrypoint).
func dockerRunArgs(name, workdir string, spec Spec) []string {
	args := []string{"run", "-d", "--name", name, "-w", workdir}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.Runtime != "" {
		// The OCI-runtime knob: shell the operator-chosen runtime (e.g. kata,
		// runsc) straight through. Empty leaves docker's default (runc).
		args = append(args, "--runtime="+spec.Runtime)
	}
	// Secure-by-default isolation + resource limits (shared with podman). Applied
	// to every container unless the Spec explicitly widens a knob. See hardening.go.
	args = append(args, hardeningFlags(spec)...)
	for _, m := range spec.BindMounts {
		v := m.Source + ":" + m.Target
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	// Spec.Env is NON-SECRET config only — the broker invariant forbids a
	// provider key here (see Spec.Env). We pass what we are given verbatim and
	// add nothing from the host environment. Keys are emitted in sorted order so
	// the constructed argv is deterministic (reproducible logs, testable).
	args = append(args, envFlags(spec.Env)...)
	args = append(args, spec.Image, "sleep", "infinity")
	return args
}

// envFlags renders a NON-SECRET env map as a stable, sorted `-e K=V` flag
// sequence shared by the Docker and Podman run-arg builders. Sorting makes the
// argv deterministic regardless of Go's randomized map iteration.
func envFlags(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	flags := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		flags = append(flags, "-e", k+"="+env[k])
	}
	return flags
}

// run executes `docker <args…>` and returns combined-safe stdout, surfacing
// stderr in the error on failure.
func (p *DockerProvider) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, p.bin(), args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// dockerSandbox is one live container satisfying [Sandbox].
type dockerSandbox struct {
	provider    *DockerProvider
	name        string
	containerID string
	workdir     string

	closeOnce sync.Once
}

// Workdir implements [Sandbox].
func (s *dockerSandbox) Workdir() string { return s.workdir }

// Exec implements [Sandbox]. It runs argv inside the container via
// `docker exec -i -w <cwd> [-e K=V] <name> <argv…>` and streams stdout
// line-by-line to opts.OnLine as the process produces it (a bufio.Scanner over
// the piped stdout), while also buffering the full stdout/stderr for the
// returned ExecResult. A non-zero container-command exit is reported in
// ExecResult.ExitCode, not as a Go error; err is non-nil only when the command
// could not be run (docker down, ctx cancelled). ctx cancellation kills the
// docker exec — and thus the in-container process — via CommandContext.
func (s *dockerSandbox) Exec(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error) {
	if len(argv) == 0 {
		return ExecResult{}, errors.New("sandbox: Exec requires a non-empty argv")
	}
	cwd := opts.Workdir
	if cwd == "" {
		cwd = s.workdir
	}

	dockerArgs := []string{"exec", "-i", "-w", cwd}
	for k, v := range opts.Env {
		dockerArgs = append(dockerArgs, "-e", k+"="+v)
	}
	dockerArgs = append(dockerArgs, s.name)
	dockerArgs = append(dockerArgs, argv...)

	cmd := exec.CommandContext(ctx, s.provider.bin(), dockerArgs...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("sandbox: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ExecResult{}, fmt.Errorf("sandbox: docker exec start: %w", err)
	}

	// Stream stdout line-by-line on THIS goroutine while the process runs.
	// This is the required live-streaming behaviour: OnLine fires per line as
	// it arrives, not after Wait returns.
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
			// The command ran and exited non-zero: a normal result, not an
			// error. Surface the code and return nil error.
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		// Ran, but not a clean exit status (ctx cancelled, docker missing).
		return res, fmt.Errorf("sandbox: docker exec: %w", waitErr)
	}
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		return res, fmt.Errorf("sandbox: reading stdout: %w", scanErr)
	}
	return res, nil
}

// streamLines copies r into buf while delivering each completed line to onLine
// (terminator stripped). It returns any non-EOF scan error.
func streamLines(r io.Reader, buf *strings.Builder, onLine func(string)) error {
	scanner := bufio.NewScanner(r)
	// Grow the buffer so a long line (e.g. a minified file) does not overflow
	// bufio's default 64KiB token cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}
	return scanner.Err()
}

// CopyIn implements [Sandbox] via `docker cp <hostPath> <name>:<sandboxPath>`.
func (s *dockerSandbox) CopyIn(ctx context.Context, hostPath, sandboxPath string) error {
	_, err := s.provider.run(ctx, "cp", hostPath, s.name+":"+sandboxPath)
	if err != nil {
		return fmt.Errorf("sandbox: docker cp in: %w", err)
	}
	return nil
}

// CopyOut implements [Sandbox] via `docker cp <name>:<sandboxPath> <hostPath>`.
func (s *dockerSandbox) CopyOut(ctx context.Context, sandboxPath, hostPath string) error {
	_, err := s.provider.run(ctx, "cp", s.name+":"+sandboxPath, hostPath)
	if err != nil {
		return fmt.Errorf("sandbox: docker cp out: %w", err)
	}
	return nil
}

// Close implements [Sandbox] via `docker rm -f <name>`. Idempotent: the actual
// removal runs once; subsequent calls are no-ops.
func (s *dockerSandbox) Close(ctx context.Context) error {
	var err error
	s.closeOnce.Do(func() {
		_, rmErr := s.provider.run(ctx, "rm", "-f", s.name)
		s.provider.mu.Lock()
		delete(s.provider.running, s.name)
		s.provider.mu.Unlock()
		if rmErr != nil {
			err = fmt.Errorf("sandbox: docker rm: %w", rmErr)
		}
	})
	return err
}

// dockerAvailable reports whether a working docker CLI + daemon is reachable,
// by running `docker version`. Tests gate real-container cases on this and
// t.Skip when it is false — the same honesty pattern the rqlite/qdrant/
// sleepcycle suites use, so `go test` never fails merely because Docker is
// absent. binary empty means "docker" on PATH.
func dockerAvailable(ctx context.Context, binary string) bool {
	bin := binary
	if bin == "" {
		bin = "docker"
	}
	// `docker version` returns non-zero if the daemon is unreachable, which is
	// exactly the "cannot run containers" condition we want to skip on.
	return exec.CommandContext(ctx, bin, "version").Run() == nil
}

// CurrentUserSpec is a small helper: the "uid:gid" string for Spec.User so
// files written into a bind-mounted work tree are owned by the invoking host
// user rather than root. Returns "" if the ids are unavailable (Windows), which
// leaves the image default.
func CurrentUserSpec(uid, gid int) string {
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}
