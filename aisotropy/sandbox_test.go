package aisotropy

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/llm"
)

// fakeSandbox is a no-op Sandbox: Exec succeeds with empty output, Close records
// that teardown ran. It proves the public SandboxProvider seam end-to-end WITHOUT
// Docker — a custom backend (k8s, Firecracker, …) plugs in exactly this way.
type fakeSandbox struct {
	execCalls int
	closed    bool
}

func (f *fakeSandbox) Exec(_ context.Context, _ []string, _ SandboxExecOptions) (SandboxExecResult, error) {
	f.execCalls++
	return SandboxExecResult{}, nil // zero value = exit 0, empty output
}
func (f *fakeSandbox) CopyIn(context.Context, string, string) error  { return nil }
func (f *fakeSandbox) CopyOut(context.Context, string, string) error { return nil }
func (f *fakeSandbox) Workdir() string                               { return "/work" }
func (f *fakeSandbox) Close(context.Context) error                   { f.closed = true; return nil }

type fakeProvider struct {
	created int
	sb      *fakeSandbox
	gotSpec SandboxSpec
}

func (p *fakeProvider) Create(_ context.Context, spec SandboxSpec) (Sandbox, error) {
	p.created++
	p.gotSpec = spec
	return p.sb, nil
}
func (p *fakeProvider) Name() string { return "fake" }

// TestApp_RunTask_CustomProviderIsUsed proves the public extension point: a
// third-party SandboxProvider passed via TaskRequest.Provider is actually the
// backend RunTask stands the task up on — no Docker required. This is the seam a
// Kubernetes or Firecracker backend implements.
func TestApp_RunTask_CustomProviderIsUsed(t *testing.T) {
	if exec.Command("git", "--version").Run() != nil {
		t.Skip("git unavailable; skipping custom-provider test")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "seed").Run(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	fake := &fakeProvider{sb: &fakeSandbox{}}
	app := New(WithLLM(llm.NewStubClient()))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := app.RunTask(ctx, TaskRequest{
		RepoDir:  repo,
		Prompt:   "make a change",
		MaxSteps: 2,
		Scopes:   SandboxToolScopes(),
		Provider: fake, // the public extension point under test
	}); err != nil {
		t.Fatalf("RunTask with custom provider: %v", err)
	}

	if fake.created == 0 {
		t.Fatal("custom provider's Create was never called — TaskRequest.Provider is not wired through")
	}
	if !fake.sb.closed {
		t.Error("custom sandbox was not Closed — lifecycle not honored")
	}
	// The broker invariant reaches the custom provider too: the spec it received
	// must carry no provider API key.
	for k := range fake.gotSpec.Env {
		if k == "ANTHROPIC_API_KEY" || k == "OPENAI_API_KEY" || k == "OPENROUTER_API_KEY" {
			t.Errorf("provider secret %q leaked into the sandbox spec Env", k)
		}
	}
}

// TestDockerAndPodmanSandbox_Constructors proves the public built-in
// constructors return usable providers implementing SandboxProvider.
func TestDockerAndPodmanSandbox_Constructors(t *testing.T) {
	var _ SandboxProvider = DockerSandbox()
	var _ SandboxProvider = PodmanSandbox()
	if DockerSandbox().Name() == "" || PodmanSandbox().Name() == "" {
		t.Error("built-in providers must report a non-empty Name()")
	}
}
