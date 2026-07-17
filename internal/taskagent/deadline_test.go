package taskagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luum-Home/aisotropy/internal/agentloop"
	"github.com/Luum-Home/aisotropy/internal/sandbox"
)

// TestTaskDeadline_DefaultApplied proves a run with no ctx deadline and no
// MaxDuration gets DefaultTaskTimeout — the security default that stops a runaway
// task from idling the host forever.
func TestTaskDeadline_DefaultApplied(t *testing.T) {
	ctx, cancel := taskDeadline(context.Background(), 0)
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline to be applied when the ctx had none")
	}
	remaining := time.Until(dl)
	if remaining <= 0 || remaining > DefaultTaskTimeout+time.Second {
		t.Fatalf("deadline remaining = %v, want ~%v", remaining, DefaultTaskTimeout)
	}
}

// TestTaskDeadline_MaxDurationApplied proves an explicit MaxDuration is honored
// (and is far tighter than the default) and actually fires.
func TestTaskDeadline_MaxDurationApplied(t *testing.T) {
	ctx, cancel := taskDeadline(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("expected a deadline from MaxDuration")
	}
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline did not fire within 2s")
	}
}

// TestTaskDeadline_CallerDeadlineRespected proves an existing ctx deadline (a
// caller timeout, or a batch's parent ctx) is respected as-is when no MaxDuration
// is set — the runner does not loosen it.
func TestTaskDeadline_CallerDeadlineRespected(t *testing.T) {
	parent, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pcancel()

	ctx, cancel := taskDeadline(parent, 0)
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected the caller's deadline to be present")
	}
	// The caller's ~30s deadline, NOT DefaultTaskTimeout (10m).
	if remaining := time.Until(dl); remaining > time.Minute {
		t.Fatalf("caller deadline not respected: remaining = %v, want ~30s", remaining)
	}
}

// TestTaskDeadline_MaxDurationTightensParent proves MaxDuration caps the run even
// when the ctx has a looser deadline (WithTimeout keeps the earlier one).
func TestTaskDeadline_MaxDurationTightensParent(t *testing.T) {
	parent, pcancel := context.WithTimeout(context.Background(), time.Hour)
	defer pcancel()

	ctx, cancel := taskDeadline(parent, 100*time.Millisecond)
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if remaining := time.Until(dl); remaining > time.Second {
		t.Fatalf("MaxDuration should tighten the parent: remaining = %v, want <= ~100ms", remaining)
	}
}

// blockingProvider is a non-docker sandbox.Provider whose Create blocks until the
// per-task ctx is cancelled, then returns ctx.Err(). It models a task that hangs
// during sandbox setup: the ONLY thing that frees it is the per-task deadline
// firing, so a RunTask/RunTasks over it proves the wall-clock bound tears the task
// down.
type blockingProvider struct{}

func (p *blockingProvider) Name() string { return "blocking-provider" }
func (p *blockingProvider) Create(ctx context.Context, _ sandbox.Spec) (sandbox.Sandbox, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunTasks_PerTaskDeadline_TearsDownRunawaySibling is the Residual-2 proof: in
// a batch whose OWN ctx is generously long, a single task that would hang forever
// is torn down at its per-task MaxDuration while its sibling completes normally.
// This proves the deadline is enforced PER TASK inside the fan-out, not merely by
// the batch-wide ctx. Docker-free (git-gated): the runaway task hangs in a fake
// provider's Create; the sibling runs a fake provider to a real completion.
func TestRunTasks_PerTaskDeadline_TearsDownRunawaySibling(t *testing.T) {
	if !gitReady() {
		t.Skip("git unavailable")
	}

	const runawayIdx = 0
	const siblingIdx = 1

	reqs := make([]Request, 2)
	for i := range reqs {
		repo := t.TempDir()
		gitInit(t, repo)
		reqs[i] = Request{
			RepoDir:  repo,
			Prompt:   "task",
			Image:    "alpine:3.20",
			MaxSteps: 1,
			Scopes:   HandScopes(),
		}
	}
	// The runaway hangs in Create; only its 150ms per-task deadline frees it.
	reqs[runawayIdx].Provider = &blockingProvider{}
	reqs[runawayIdx].MaxDuration = 150 * time.Millisecond
	// The sibling runs to completion via the fake provider + one run_shell step.
	reqs[siblingIdx].Provider = &fakeProvider{}

	client := &constClient{dec: agentloop.Decision{
		Tool: ToolRunShell,
		Args: map[string]string{"cmd": "echo hi"},
	}}

	// The BATCH ctx is generous — far longer than the runaway's per-task deadline —
	// so if the teardown came from the batch ctx (the old, buggy behavior) this test
	// would hang until this 30s timeout instead of finishing in ~150ms.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	outcomes, err := RunTasks(ctx, client, nil, reqs, Options{Concurrency: 2})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("top-level error should be nil (a task deadline is not a batch failure): %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("batch took %v; the runaway task was not bounded by its per-task deadline", elapsed)
	}

	// The runaway task was torn down with a deadline error.
	if outcomes[runawayIdx].Err == nil {
		t.Fatalf("runaway task should carry a deadline error, got nil")
	}
	if !errors.Is(outcomes[runawayIdx].Err, context.DeadlineExceeded) {
		t.Fatalf("runaway task Err = %v, want it to wrap context.DeadlineExceeded", outcomes[runawayIdx].Err)
	}

	// The sibling completed normally, unaffected by the runaway's teardown.
	if outcomes[siblingIdx].Err != nil {
		t.Fatalf("sibling task should have completed, got Err: %v", outcomes[siblingIdx].Err)
	}
	if len(outcomes[siblingIdx].Result.Steps) != 1 {
		t.Fatalf("sibling: got %d steps, want 1", len(outcomes[siblingIdx].Result.Steps))
	}
}

// TestAssertNoProviderSecret_PatternDenylist proves the broker-invariant env guard
// is a PATTERN check, not a fixed enumeration: an unlisted provider/cloud key
// (Gemini, AWS, Azure, Cohere, Mistral, Google creds, an HF token, a generic
// *_API_KEY / *_SECRET / *_TOKEN) is caught, while ordinary non-secret vars pass.
func TestAssertNoProviderSecret_PatternDenylist(t *testing.T) {
	// Each of these carries a dummy value; only the NAME is inspected by the guard.
	forbidden := []string{
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AZURE_OPENAI_API_KEY",
		"COHERE_API_KEY",
		"MISTRAL_API_KEY",
		"HF_TOKEN",
		"HUGGINGFACEHUB_API_TOKEN",
		"SOMENEWPROVIDER_API_KEY", // a provider nobody enumerated yet
		"MY_CLIENT_SECRET",
		"DB_PASSWORD",
		"anthropic_api_key", // case-insensitive
	}
	for _, k := range forbidden {
		if err := assertNoProviderSecret(map[string]string{k: "dummy-value"}); err == nil {
			t.Errorf("assertNoProviderSecret must reject secret-shaped key %q", k)
		}
	}

	safe := []string{"LANG", "PATH", "HOME", "TERM", "TZ", "PWD", "SHELL", "LC_ALL", "GOFLAGS"}
	safeEnv := make(map[string]string, len(safe))
	for _, k := range safe {
		safeEnv[k] = "x"
	}
	if err := assertNoProviderSecret(safeEnv); err != nil {
		t.Fatalf("ordinary non-secret env should pass, got: %v", err)
	}
}

// TestAssertNoProviderSecret_ErrorNamesKeyNotValue proves the guard surfaces the
// offending key NAME (safe to log) but never the secret VALUE.
func TestAssertNoProviderSecret_ErrorNamesKeyNotValue(t *testing.T) {
	const secretValue = "super-secret-value-must-not-leak"
	err := assertNoProviderSecret(map[string]string{"GEMINI_API_KEY": secretValue})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); !contains(got, "GEMINI_API_KEY") {
		t.Errorf("error should name the offending key, got: %q", got)
	}
	if got := err.Error(); contains(got, secretValue) {
		t.Errorf("error must NOT contain the secret value, got: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
