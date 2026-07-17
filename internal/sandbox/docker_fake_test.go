package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDocker_FakeCLIRoundTrip drives the Docker provider against the shared
// fake CLI (see fakeCLIScript in podman_fake_test.go), covering the same
// provider-agnostic surface without a real Docker daemon: Name/bin, Create,
// Exec streaming + exit codes, CopyIn/CopyOut, Close idempotency, CleanupAll.
// The real integration path (against a live daemon) is covered by
// TestDocker_RoundTrip; this fills the daemon-independent branches.
func TestDocker_FakeCLIRoundTrip(t *testing.T) {
	fake := writeFakeCLI(t)
	p := &DockerProvider{Binary: fake, running: map[string]*dockerSandbox{}}

	if p.Name() != "docker" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.bin() != fake {
		t.Errorf("bin() = %q", p.bin())
	}

	ctx := context.Background()
	sb, err := p.Create(ctx, Spec{Image: "alpine", Name: "unit", Env: map[string]string{"K": "v"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ds := sb.(*dockerSandbox)
	if ds.name != "aisotropy-sbx-unit" {
		t.Errorf("name = %q", ds.name)
	}
	if ds.containerID != "fake-container-id-abc123" {
		t.Errorf("containerID = %q", ds.containerID)
	}

	var mu sync.Mutex
	var streamed []string
	res, err := sb.Exec(ctx, []string{"sh", "-c", "printf 'x\\ny\\n'"}, ExecOptions{
		Env:    map[string]string{"E": "1"},
		OnLine: func(l string) { mu.Lock(); streamed = append(streamed, l); mu.Unlock() },
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || strings.Join(streamed, ",") != "x,y" {
		t.Errorf("exit=%d streamed=%v", res.ExitCode, streamed)
	}

	res, err = sb.Exec(ctx, []string{"sh", "-c", "exit 9"}, ExecOptions{})
	if err != nil || res.ExitCode != 9 {
		t.Errorf("exit9: err=%v exit=%d", err, res.ExitCode)
	}

	if err := sb.CopyIn(ctx, "/h/in", "/b/in"); err != nil {
		t.Errorf("CopyIn: %v", err)
	}
	if err := sb.CopyOut(ctx, "/b/out", "/h/out"); err != nil {
		t.Errorf("CopyOut: %v", err)
	}

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
		t.Errorf("remaining tracked = %d", remaining)
	}
}

// TestDocker_CopyAndCloseErrors covers the error-wrapping branches of CopyIn,
// CopyOut, and Close when the docker binary is missing.
func TestDocker_CopyAndCloseErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	p := &DockerProvider{Binary: missing, running: map[string]*dockerSandbox{}}
	sb := &dockerSandbox{provider: p, name: "aisotropy-sbx-x", workdir: "/w"}

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

// TestDocker_CreateMissingImage covers the required-Image guard.
func TestDocker_CreateMissingImage(t *testing.T) {
	if _, err := NewDockerProvider().Create(context.Background(), Spec{}); err == nil {
		t.Fatal("expected an error for a missing Spec.Image")
	}
}

// TestDocker_CreateRunFailure covers Create's run() error branch.
func TestDocker_CreateRunFailure(t *testing.T) {
	p := &DockerProvider{Binary: filepath.Join(t.TempDir(), "nope"), running: map[string]*dockerSandbox{}}
	if _, err := p.Create(context.Background(), Spec{Image: "alpine"}); err == nil {
		t.Fatal("expected Create to fail when the docker binary is missing")
	}
}

// TestDocker_CleanupAllFake covers CleanupAll over the fake CLI.
func TestDocker_CleanupAllFake(t *testing.T) {
	fake := writeFakeCLI(t)
	p := &DockerProvider{Binary: fake, running: map[string]*dockerSandbox{}}
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

// TestDockerAvailable_Fake covers dockerAvailable against the fake CLI (version
// exits 0) and against a missing binary.
func TestDockerAvailable_Fake(t *testing.T) {
	fake := writeFakeCLI(t)
	if !dockerAvailable(context.Background(), fake) {
		t.Error("dockerAvailable should be true against the fake CLI")
	}
	if dockerAvailable(context.Background(), filepath.Join(t.TempDir(), "nope")) {
		t.Error("dockerAvailable should be false for a missing binary")
	}
}
