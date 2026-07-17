package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeCLIScript is a POSIX-sh stand-in for the `podman`/`docker` CLI. It lets
// the provider-agnostic sandbox logic (Create → Exec → CopyIn/Out → Close, the
// run() wrapper, CleanupAll) run end to end WITHOUT a real container runtime:
//
//   - `version`            → exit 0 (so *Available() reports true)
//   - `run …`              → prints a fake container id
//   - `exec … <name> argv` → runs argv on the host (identical streaming/exit
//     semantics to a real `exec`, which is all the Go code observes)
//   - `cp …` / `rm …`      → exit 0
//
// This is a stdlib-only mock of an external binary (os/exec against a script we
// wrote), not a faked Go test double of our own code — the actual production
// os/exec + StdoutPipe + bufio streaming path is exercised for real.
const fakeCLIScript = `#!/bin/sh
cmd="$1"; shift
case "$cmd" in
  version) exit 0 ;;
  run) echo "fake-container-id-abc123"; exit 0 ;;
  rm) exit 0 ;;
  cp) exit 0 ;;
  exec)
    while [ $# -gt 0 ]; do
      case "$1" in
        -i) shift ;;
        -w) shift 2 ;;
        -e) shift 2 ;;
        *) break ;;
      esac
    done
    shift            # drop the container name
    exec "$@" ;;     # run the remaining argv on the host
  *) exit 3 ;;
esac
`

// writeFakeCLI writes fakeCLIScript to an executable temp file and returns its
// path. The test is skipped on non-POSIX hosts where /bin/sh is unavailable.
func writeFakeCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX-sh CLI mock is not portable to Windows")
	}
	path := filepath.Join(t.TempDir(), "fakecli.sh")
	if err := os.WriteFile(path, []byte(fakeCLIScript), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return path
}

// TestPodman_FakeCLIRoundTrip drives the full podman provider surface against
// the fake CLI: Name/bin, Create (both generated-name and Spec.Name), Workdir,
// Exec streaming + exit codes, CopyIn/CopyOut, and Close idempotency + tracking.
func TestPodman_FakeCLIRoundTrip(t *testing.T) {
	fake := writeFakeCLI(t)
	p := &PodmanProvider{Rootless: true, Binary: fake, running: map[string]*podmanSandbox{}}

	if p.Name() != "podman" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.bin() != fake {
		t.Errorf("bin() = %q, want %q", p.bin(), fake)
	}

	ctx := context.Background()

	// Create with an explicit Spec.Name → container name derived from it.
	sb, err := p.Create(ctx, Spec{Image: "alpine", Workdir: DefaultWorkdir, Name: "unit"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ps := sb.(*podmanSandbox)
	if ps.name != "aisotropy-sbx-unit" {
		t.Errorf("container name = %q", ps.name)
	}
	if ps.containerID != "fake-container-id-abc123" {
		t.Errorf("containerID = %q", ps.containerID)
	}
	if sb.Workdir() != DefaultWorkdir {
		t.Errorf("Workdir() = %q", sb.Workdir())
	}

	// Exec streams lines and buffers stdout.
	var mu sync.Mutex
	var streamed []string
	res, err := sb.Exec(ctx, []string{"sh", "-c", "printf 'a\\nb\\nc\\n'"}, ExecOptions{
		Workdir: "/custom",
		Env:     map[string]string{"K": "v"},
		OnLine:  func(l string) { mu.Lock(); streamed = append(streamed, l); mu.Unlock() },
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.Join(streamed, ",") != "a,b,c" {
		t.Errorf("streamed = %v", streamed)
	}
	if res.Stdout != "a\nb\nc\n" {
		t.Errorf("buffered stdout = %q", res.Stdout)
	}

	// Non-zero exit is a result, not a Go error.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "exit 5"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec exit5 returned Go error: %v", err)
	}
	if res.ExitCode != 5 {
		t.Errorf("ExitCode = %d, want 5", res.ExitCode)
	}

	// CopyIn / CopyOut happy paths (fake cp exits 0).
	if err := sb.CopyIn(ctx, "/host/in", "/box/in"); err != nil {
		t.Errorf("CopyIn: %v", err)
	}
	if err := sb.CopyOut(ctx, "/box/out", "/host/out"); err != nil {
		t.Errorf("CopyOut: %v", err)
	}

	// Sandbox is tracked while live.
	p.mu.Lock()
	tracked := len(p.running)
	p.mu.Unlock()
	if tracked != 1 {
		t.Errorf("tracked = %d, want 1", tracked)
	}

	// Close removes tracking; a second Close is a no-op.
	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Errorf("second Close: %v", err)
	}
	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Errorf("remaining tracked = %d, want 0", remaining)
	}
}

// TestPodman_FakeCLIGeneratedName covers the Spec.Name=="" branch (a
// uuid-derived container name) and podmanAvailable against the fake CLI.
func TestPodman_FakeCLIGeneratedName(t *testing.T) {
	fake := writeFakeCLI(t)
	if !podmanAvailable(context.Background(), fake) {
		t.Fatal("podmanAvailable should be true against the fake CLI")
	}
	p := &PodmanProvider{Rootless: false, Binary: fake, running: map[string]*podmanSandbox{}}
	sb, err := p.Create(context.Background(), Spec{Image: "alpine"}) // no Workdir, no Name
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ps := sb.(*podmanSandbox)
	if !strings.HasPrefix(ps.name, "aisotropy-sbx-") || len(ps.name) <= len("aisotropy-sbx-") {
		t.Errorf("generated name = %q, want aisotropy-sbx-<uuid>", ps.name)
	}
	if sb.Workdir() != DefaultWorkdir {
		t.Errorf("default Workdir() = %q, want %q", sb.Workdir(), DefaultWorkdir)
	}
}

// TestPodman_CreateMissingImage covers the required-Image guard.
func TestPodman_CreateMissingImage(t *testing.T) {
	p := NewPodmanProvider()
	if _, err := p.Create(context.Background(), Spec{}); err == nil {
		t.Fatal("expected an error for a missing Spec.Image")
	}
}

// TestPodman_CreateRunFailure points Binary at a nonexistent binary so run()
// fails, covering Create's error-wrapping branch.
func TestPodman_CreateRunFailure(t *testing.T) {
	p := &PodmanProvider{Binary: filepath.Join(t.TempDir(), "does-not-exist"), running: map[string]*podmanSandbox{}}
	if _, err := p.Create(context.Background(), Spec{Image: "alpine"}); err == nil {
		t.Fatal("expected Create to fail when the podman binary is missing")
	}
}

// TestPodman_CopyAndCloseErrors covers the error-wrapping branches of CopyIn,
// CopyOut, and Close when the underlying CLI invocation fails.
func TestPodman_CopyAndCloseErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	p := &PodmanProvider{Binary: missing, running: map[string]*podmanSandbox{}}
	sb := &podmanSandbox{provider: p, name: "aisotropy-sbx-x", workdir: "/w"}

	if err := sb.CopyIn(context.Background(), "/h", "/s"); err == nil {
		t.Error("expected CopyIn error")
	}
	if err := sb.CopyOut(context.Background(), "/s", "/h"); err == nil {
		t.Error("expected CopyOut error")
	}
	if err := sb.Close(context.Background()); err == nil {
		t.Error("expected Close error when rm fails")
	}
}

// TestPodman_CleanupAllFake covers CleanupAll over the fake CLI: two live
// sandboxes, all removed and untracked.
func TestPodman_CleanupAllFake(t *testing.T) {
	fake := writeFakeCLI(t)
	p := &PodmanProvider{Rootless: true, Binary: fake, running: map[string]*podmanSandbox{}}
	if _, err := p.Create(context.Background(), Spec{Image: "alpine", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Create(context.Background(), Spec{Image: "alpine", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	p.CleanupAll(context.Background())
	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("CleanupAll left %d tracked", remaining)
	}
}

// TestPodman_ExecStartFailure covers the exec-start error branch: pointing the
// binary at a nonexistent path makes cmd.Start fail.
func TestPodman_ExecStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	p := &PodmanProvider{Binary: missing, running: map[string]*podmanSandbox{}}
	sb := &podmanSandbox{provider: p, name: "aisotropy-sbx-x", workdir: "/w"}
	if _, err := sb.Exec(context.Background(), []string{"echo", "hi"}, ExecOptions{}); err == nil {
		t.Fatal("expected Exec to fail when the binary is missing")
	}
}

// TestRunArgs_BindMountsAndUser covers the BindMount (ro + rw) and User arg
// branches of both run-arg builders, plus the bin() default (empty Binary).
func TestRunArgs_BindMountsAndUser(t *testing.T) {
	spec := Spec{
		Image: "alpine",
		User:  "1000:1000",
		BindMounts: []Mount{
			{Source: "/h/rw", Target: "/w/rw"},
			{Source: "/h/ro", Target: "/w/ro", ReadOnly: true},
		},
	}
	d := dockerRunArgs("box", DefaultWorkdir, spec)
	if !argvContains(d, "/h/rw:/w/rw") || !argvContains(d, "/h/ro:/w/ro:ro") {
		t.Errorf("docker bind mounts not rendered: %v", d)
	}
	if !argvContains(d, "--user") || !argvContains(d, "1000:1000") {
		t.Errorf("docker --user not rendered: %v", d)
	}

	pargs := NewPodmanProvider().podmanRunArgs("box", DefaultWorkdir, spec)
	if !argvContains(pargs, "/h/rw:/w/rw") || !argvContains(pargs, "/h/ro:/w/ro:ro") {
		t.Errorf("podman bind mounts not rendered: %v", pargs)
	}

	// bin() default branch: empty Binary → the CLI name on PATH.
	if (&DockerProvider{}).bin() != "docker" {
		t.Error("docker default bin() should be \"docker\"")
	}
	if (&PodmanProvider{}).bin() != "podman" {
		t.Error("podman default bin() should be \"podman\"")
	}
}
