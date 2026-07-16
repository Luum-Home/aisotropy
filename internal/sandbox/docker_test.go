package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testImage is a tiny stock image with sh. Overridable so a fully-offline CI
// can point at an image it has cached.
func testImage() string {
	if v := os.Getenv("AISOTROPY_SANDBOX_TEST_IMAGE"); v != "" {
		return v
	}
	return "alpine:3.20"
}

// requireDocker skips the test unless a working docker CLI + daemon is present,
// mirroring the rqlite/qdrant/sleepcycle "skip, never fake" gate.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !dockerAvailable(ctx, "") {
		t.Skip("docker not available (docker version failed); skipping real-container tests")
	}
}

// createSandbox stands up a sandbox on testImage, skipping (not failing) if the
// image cannot be pulled — the honest offline path.
func createSandbox(t *testing.T, p *DockerProvider, spec Spec) Sandbox {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Pre-pull so an offline "image not found" becomes a clean Skip rather
	// than a confusing Create failure.
	if _, err := p.run(ctx, "pull", spec.Image); err != nil {
		t.Skipf("cannot pull %s (offline?): %v", spec.Image, err)
	}
	sb, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return sb
}

func TestDocker_RoundTrip(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()

	// Bind-mount a host work tree at the default workdir, as a task runner
	// would mount a git worktree.
	host := t.TempDir()
	spec := Spec{
		Image:      testImage(),
		Workdir:    DefaultWorkdir,
		BindMounts: []Mount{{Source: host, Target: DefaultWorkdir}},
		Env:        map[string]string{"AISOTROPY_TEST": "1"},
	}
	sb := createSandbox(t, p, spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Close(ctx)
	})

	ctx := context.Background()

	if sb.Workdir() != DefaultWorkdir {
		t.Fatalf("Workdir() = %q, want %q", sb.Workdir(), DefaultWorkdir)
	}

	// 1) Exec echo, capturing streamed lines AND the buffered result.
	var mu sync.Mutex
	var streamed []string
	res, err := sb.Exec(ctx, []string{"sh", "-c", "printf 'line1\\nline2\\nline3\\n'"}, ExecOptions{
		OnLine: func(l string) {
			mu.Lock()
			streamed = append(streamed, l)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Exec echo: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	wantLines := []string{"line1", "line2", "line3"}
	if strings.Join(streamed, ",") != strings.Join(wantLines, ",") {
		t.Fatalf("streamed lines = %v, want %v", streamed, wantLines)
	}
	if res.Stdout != "line1\nline2\nline3\n" {
		t.Fatalf("buffered stdout = %q", res.Stdout)
	}

	// 2) Non-secret env reached the process; provider key never would.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "echo $AISOTROPY_TEST"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec env: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "1" {
		t.Fatalf("env not propagated: stdout=%q", res.Stdout)
	}

	// 3) A non-zero command exit is a result, not a Go error.
	res, err = sb.Exec(ctx, []string{"sh", "-c", "exit 7"}, ExecOptions{})
	if err != nil {
		t.Fatalf("Exec exit7 returned Go error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}

	// 4) CopyIn a host file, read it back inside the sandbox.
	local := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(local, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sb.CopyIn(ctx, local, "/tmp/in.txt"); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}
	res, err = sb.Exec(ctx, []string{"cat", "/tmp/in.txt"}, ExecOptions{})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("cat copied file: err=%v exit=%d", err, res.ExitCode)
	}
	if res.Stdout != "payload" && strings.TrimSpace(res.Stdout) != "payload" {
		t.Fatalf("copied content = %q", res.Stdout)
	}

	// 5) Write a file inside the sandbox, CopyOut to the host.
	if _, err := sb.Exec(ctx, []string{"sh", "-c", "echo out > /tmp/out.txt"}, ExecOptions{}); err != nil {
		t.Fatalf("write out.txt: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.txt")
	if err := sb.CopyOut(ctx, "/tmp/out.txt", dst); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copied-out file: %v", err)
	}
	if strings.TrimSpace(string(got)) != "out" {
		t.Fatalf("copied-out content = %q", got)
	}

	// 6) The bind mount is bidirectional: a file written in the sandbox
	// workdir appears on the host mount source.
	if _, err := sb.Exec(ctx, []string{"sh", "-c", "echo shared > shared.txt"}, ExecOptions{}); err != nil {
		t.Fatalf("write shared.txt: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(host, "shared.txt")); err != nil {
		t.Fatalf("shared file not on host mount: %v", err)
	} else if strings.TrimSpace(string(b)) != "shared" {
		t.Fatalf("shared content = %q", b)
	}
}

func TestDocker_CloseIsIdempotentAndRemovesContainer(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()
	spec := Spec{Image: testImage(), Workdir: DefaultWorkdir, Name: "close-test"}
	sb := createSandbox(t, p, spec)

	ctx := context.Background()
	// Container is tracked while live.
	p.mu.Lock()
	tracked := len(p.running)
	p.mu.Unlock()
	if tracked == 0 {
		t.Fatal("expected a tracked running sandbox")
	}

	if err := sb.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close is a no-op, no error.
	if err := sb.Close(ctx); err != nil {
		t.Fatalf("second Close should be a no-op: %v", err)
	}
	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected 0 tracked sandboxes after Close, got %d", remaining)
	}

	// The container name is gone: docker inspect fails.
	if _, err := p.run(ctx, "inspect", "aisotropy-sbx-close-test"); err == nil {
		t.Fatal("expected docker inspect to fail on a removed container")
	}
}

func TestDocker_CleanupAll(t *testing.T) {
	requireDocker(t)
	p := NewDockerProvider()
	_ = createSandbox(t, p, Spec{Image: testImage(), Workdir: DefaultWorkdir})
	_ = createSandbox(t, p, Spec{Image: testImage(), Workdir: DefaultWorkdir})

	ctx := context.Background()
	p.CleanupAll(ctx)

	p.mu.Lock()
	remaining := len(p.running)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("CleanupAll left %d sandboxes tracked", remaining)
	}
}

func TestDocker_ExecEmptyArgvErrors(t *testing.T) {
	// Pure guard-path test — no daemon needed, so it runs even without docker.
	sb := &dockerSandbox{provider: NewDockerProvider(), name: "x", workdir: "/w"}
	if _, err := sb.Exec(context.Background(), nil, ExecOptions{}); err == nil {
		t.Fatal("expected error for empty argv")
	}
}

func TestCurrentUserSpec(t *testing.T) {
	if got := CurrentUserSpec(1000, 1000); got != "1000:1000" {
		t.Fatalf("CurrentUserSpec = %q", got)
	}
	if got := CurrentUserSpec(-1, 0); got != "" {
		t.Fatalf("CurrentUserSpec(-1,0) = %q, want empty", got)
	}
}
